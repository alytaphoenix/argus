#!/bin/sh
# bootstrap-pve.sh — install argus on a Proxmox VE host, idempotently.
#
# This script ONLY manages argus itself:
#   * copies argus / argus-metrics-server / argus-mcp-server into a bin dir
#   * creates /etc/argus/config.toml if (and only if) it does not exist
#   * installs + starts the argus-metrics systemd unit
#
# Without --token it never touches /etc/pve, VMs, containers, storage,
# networking, firewall rules, or any existing Proxmox service. With --token it
# only ADDS root@pam!argus to /etc/pve/priv/token.cfg (never removes users,
# VMs, storage, or other tokens). Re-running is safe: unchanged binaries and
# config are left alone, and the service is only restarted when a binary
# actually changed.
#
# Usage:
#   sudo ./scripts/bootstrap-pve.sh                 # install from ./dist/linux-amd64
#   sudo ./scripts/bootstrap-pve.sh --build         # cross-build first (needs go)
#   sudo ./scripts/bootstrap-pve.sh --dry-run       # show what would change
#   sudo ./scripts/bootstrap-pve.sh --no-systemd    # binaries + config only
#
# Flags:
#   --source DIR      directory containing the three binaries (default: ./dist/linux-amd64)
#   --prefix DIR      install binaries here (default: /usr/local/bin)
#   --config-dir DIR  config directory (default: /etc/argus)
#   --state-dir DIR   service HOME / database directory (default: /var/lib/argus)
#   --build           build linux/amd64 binaries into --source before installing
#   --no-systemd      don't install/enable the metrics unit
#   --token           create root@pam!argus API token if config has no token yet
#   --dry-run         print actions, change nothing
#   --force           run even when Proxmox VE isn't detected
#   -h, --help        show this help

set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)

SOURCE_DIR="$REPO_DIR/dist/linux-amd64"
PREFIX="/usr/local/bin"
CONFIG_DIR="/etc/argus"
STATE_DIR="/var/lib/argus"
DO_BUILD=0
DO_SYSTEMD=1
DO_TOKEN=0
DRY_RUN=0
FORCE=0

usage() {
    sed -n '2,40p' "$0" | sed 's/^# \{0,1\}//'
    exit 0
}

while [ $# -gt 0 ]; do
    case "$1" in
        --source)     SOURCE_DIR="$2"; shift 2 ;;
        --prefix)     PREFIX="$2"; shift 2 ;;
        --config-dir) CONFIG_DIR="$2"; shift 2 ;;
        --state-dir)  STATE_DIR="$2"; shift 2 ;;
        --build)      DO_BUILD=1; shift ;;
        --no-systemd) DO_SYSTEMD=0; shift ;;
        --token)      DO_TOKEN=1; shift ;;
        --dry-run)    DRY_RUN=1; shift ;;
        --force)      FORCE=1; shift ;;
        -h|--help)    usage ;;
        *) echo "unknown flag: $1 (try --help)" >&2; exit 2 ;;
    esac
done

say()  { printf '%s\n' "$*"; }
run() {
    if [ "$DRY_RUN" -eq 1 ]; then
        say "  [dry-run] $*"
    else
        "$@"
    fi
}

# Portable sha256: macOS has shasum, most Linux hosts have sha256sum.
hash_file() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" | awk '{print $1}'
    else
        cksum "$1" | awk '{print $1"-"$2}'
    fi
}

# ── Preflight ────────────────────────────────────────────────────────────────

if [ "$(id -u)" -ne 0 ] && [ "$DRY_RUN" -eq 0 ]; then
    check_dirs="$PREFIX $CONFIG_DIR $STATE_DIR"
    if [ "$DO_SYSTEMD" -eq 1 ]; then
        check_dirs="$check_dirs /etc/systemd/system"
    fi
    for d in $check_dirs; do
        if [ -d "$d" ]; then
            writable=0
            [ -w "$d" ] && writable=1
        else
            writable=0
            [ -w "$(dirname "$d")" ] && writable=1
        fi
        if [ "$writable" -eq 0 ]; then
            echo "bootstrap-pve: must run as root (or pass writable --prefix/--config-dir/--state-dir)" >&2
            exit 1
        fi
    done
fi

if [ "$DO_SYSTEMD" -eq 1 ] && [ "$DRY_RUN" -eq 0 ] && [ ! -d /run/systemd/system ]; then
    say "bootstrap-pve: systemd not running — continuing with --no-systemd behaviour"
    DO_SYSTEMD=0
fi

if [ -f /etc/pve/.version ] || command -v pveversion >/dev/null 2>&1; then
    say "✓ Proxmox VE detected$(command -v pveversion >/dev/null 2>&1 && printf ' (%s)' "$(pveversion 2>/dev/null | head -1)")"
elif [ "$FORCE" -eq 0 ]; then
    say "! This doesn't look like a Proxmox VE host (no /etc/pve/.version or pveversion)."
    say "  Re-run with --force to install anyway."
    exit 1
fi

# ── Optional build ───────────────────────────────────────────────────────────

if [ "$DO_BUILD" -eq 1 ]; then
    if ! command -v go >/dev/null 2>&1; then
        echo "bootstrap-pve: --build needs go in PATH" >&2
        exit 1
    fi
    say "building linux/amd64 binaries into $SOURCE_DIR"
    run mkdir -p "$SOURCE_DIR"
    for b in argus argus-metrics-server argus-mcp-server; do
        case "$b" in
            argus)              pkg="." ;;
            argus-metrics-server) pkg="./cmd/metrics-server" ;;
            argus-mcp-server)   pkg="./cmd/mcp-server" ;;
        esac
        if [ "$DRY_RUN" -eq 1 ]; then
            say "  [dry-run] GOOS=linux GOARCH=amd64 go build -o $SOURCE_DIR/$b $pkg"
        else
            (cd "$REPO_DIR" && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
                go build -trimpath -ldflags="-s -w" -o "$SOURCE_DIR/$b" "$pkg")
        fi
    done
fi

BINS="argus argus-metrics-server argus-mcp-server"
for b in $BINS; do
    if [ ! -f "$SOURCE_DIR/$b" ] && [ "$DRY_RUN" -eq 0 ]; then
        echo "bootstrap-pve: missing $SOURCE_DIR/$b" >&2
        echo "  run with --build, or copy prebuilt linux/amd64 binaries there" >&2
        exit 1
    fi
done

# ── Directories ──────────────────────────────────────────────────────────────

say ""
say "Installing into:"
say "  binaries: $PREFIX"
say "  config:   $CONFIG_DIR/config.toml"
say "  state:    $STATE_DIR"
say ""

run mkdir -p "$PREFIX" "$CONFIG_DIR" "$STATE_DIR"

# ── Binaries (atomic, backed up, skipped when unchanged) ─────────────────────

STAMP=$(date +%Y%m%d-%H%M%S)
CHANGED=0
for b in $BINS; do
    src="$SOURCE_DIR/$b"
    dst="$PREFIX/$b"
    if [ ! -f "$src" ]; then
        say "  ! $b: source missing (dry-run) — skipping"
        continue
    fi
    if [ -f "$dst" ] && [ "$(hash_file "$src")" = "$(hash_file "$dst")" ]; then
        say "  = $b: unchanged"
        continue
    fi
    if [ -f "$dst" ]; then
        say "  ↑ $b: updating (backup: $dst.bak.$STAMP)"
        if [ ! -e "$dst.bak.$STAMP" ]; then
            run cp -p "$dst" "$dst.bak.$STAMP"
        fi
    else
        say "  + $b: installing"
    fi
    tmp="$dst.tmp.$$"
    run install -m 0755 "$src" "$tmp"
    run mv -f "$tmp" "$dst"
    CHANGED=1
done

# ── Config: never overwrite ──────────────────────────────────────────────────

cfg="$CONFIG_DIR/config.toml"
if [ -f "$cfg" ]; then
    say "  = config: preserving existing $cfg"
else
    say "  + config: creating minimal $cfg (no secrets written)"
    if [ "$DRY_RUN" -eq 1 ]; then
        say "  [dry-run] write $cfg"
    else
        umask 077
        cat > "$cfg" <<'EOF'
# argus configuration (created by scripts/bootstrap-pve.sh)
# The default install writes no secrets. Either set a password environment
# variable when running the TUI, or re-run with --token to write a 0600 token.
host     = "https://127.0.0.1:8006"
username = "root@pam"
insecure = true

[prometheus]
# loopback-only by default; this endpoint has no authentication
listen = "127.0.0.1:9099"
EOF
        chmod 0600 "$cfg"
    fi
fi

# ── Optional Proxmox API token (root@pam!argus) ──────────────────────────────

if [ "$DO_TOKEN" -eq 1 ]; then
    if ! command -v pveum >/dev/null 2>&1; then
        echo "bootstrap-pve: --token requires pveum (Proxmox VE)" >&2
        exit 1
    fi
    has_secret=$(python3 - "$cfg" <<'PY'
import sys
tid = sec = False
try:
    for line in open(sys.argv[1]):
        s = line.strip()
        tid = tid or s.startswith('token_id')
        sec = sec or s.startswith('secret')
except Exception:
    pass
print(1 if tid and sec else 0)
PY
)
    if [ "$has_secret" = "1" ]; then
        say "  = token auth: preserving token_id/secret in $cfg"
    elif [ "$DRY_RUN" -eq 1 ]; then
        say "  [dry-run] create Proxmox token root@pam!argus and write token_id/secret to $cfg"
    else
        has_token=$(pveum user token list root@pam --output-format json 2>/dev/null | python3 -c 'import sys,json
try: d=json.load(sys.stdin)
except Exception: d=[]
print(1 if any(t.get("tokenid")=="argus" for t in d) else 0)')
        if [ "$has_token" = "1" ]; then
            say "  ! existing root@pam!argus has no stored secret — recreating it"
            pveum user token remove root@pam argus >/dev/null
        fi
        token_json=$(pveum user token add root@pam argus --privsep 0 \
            --comment 'argus automation' --output-format json)
        TOKEN_SECRET=$(printf '%s' "$token_json" | python3 -c 'import sys,json; print(json.load(sys.stdin)["value"])')
        export TOKEN_SECRET
        python3 - "$cfg" <<'PY'
import os, sys, pathlib, shutil, time, json
p = pathlib.Path(sys.argv[1]); sec = os.environ['TOKEN_SECRET']
old = p.read_text() if p.exists() else ''
out, inserted = [], False
for line in old.splitlines():
    s = line.strip()
    if s.startswith(('username', 'password', 'token_id', 'secret')):
        continue
    out.append(line)
    if s.startswith('host') and not inserted:
        out.append('token_id = ' + json.dumps('root@pam!argus'))
        out.append('secret = ' + json.dumps(sec))
        inserted = True
if not inserted:
    out = ['token_id = "root@pam!argus"', 'secret = ' + json.dumps(sec)] + out
if p.exists():
    shutil.copy2(p, str(p) + '.bak.' + str(int(time.time())))
p.write_text('\n'.join(out) + '\n')
p.chmod(0o600)
PY
        unset TOKEN_SECRET
        say "  + token auth: root@pam!argus (secret in $cfg, mode 0600)"
    fi
fi

# ── systemd unit for the metrics endpoint ────────────────────────────────────

if [ "$DO_SYSTEMD" -eq 1 ]; then
    unit="/etc/systemd/system/argus-metrics.service"
    desired=$(mktemp)
    cat > "$desired" <<EOF
[Unit]
Description=argus metrics exporter
Documentation=https://github.com/alytaphoenix/argus
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
Environment=HOME=$STATE_DIR
Environment=ARGUS_CONFIG=$CONFIG_DIR/config.toml
ExecStart=$PREFIX/argus-metrics-server
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true

[Install]
WantedBy=multi-user.target
EOF

    changed_unit=0
    if [ -f "$unit" ] && cmp -s "$desired" "$unit"; then
        say "  = systemd unit unchanged"
    else
        say "  + systemd unit $unit (backup kept if replacing)"
        if [ -f "$unit" ]; then
            run cp -p "$unit" "$unit.bak.$STAMP"
        fi
        run install -m 0644 "$desired" "$unit"
        changed_unit=1
    fi
    rm -f "$desired"

    if [ "$DRY_RUN" -eq 1 ]; then
        say "  [dry-run] systemctl daemon-reload; systemctl enable --now argus-metrics"
    else
        if [ "$changed_unit" -eq 1 ]; then
            systemctl daemon-reload
        fi
        systemctl enable argus-metrics >/dev/null 2>&1 || true
        if [ "$CHANGED" -eq 1 ]; then
            systemctl restart argus-metrics
        else
            systemctl start argus-metrics
        fi
        healthy=0
        for _ in 1 2 3 4 5 6 7 8 9 10; do
            if curl -fsS --max-time 2 http://127.0.0.1:9099/healthz >/dev/null 2>&1; then
                healthy=1
                break
            fi
            sleep 1
        done
        if [ "$healthy" -eq 1 ]; then
            say "  ✓ argus-metrics healthy on :9099"
        else
            say "  ! argus-metrics did not answer /healthz after 10s — check: journalctl -u argus-metrics -n 50"
        fi
    fi
fi

say ""
if [ "$DRY_RUN" -eq 1 ]; then
    say "dry run complete — nothing was changed."
else
    say "bootstrap complete."
    say ""
    say "The TUI is interactive; run it with the host config:"
    if [ "$DO_TOKEN" -eq 1 ]; then
        say "  HOME=$STATE_DIR ARGUS_CONFIG=$CONFIG_DIR/config.toml $PREFIX/argus"
    else
        say "  PROXMOX_PASSWORD='…' $PREFIX/argus"
    fi
    say ""
    say "Existing VMs, containers, storage, networking and Proxmox services were not touched."
fi
