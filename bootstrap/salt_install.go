package bootstrap

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

// SaltInstallOptions controls the idempotent Salt master/API installer.
type SaltInstallOptions struct {
	MasterID      string
	APIUser       string
	APIPort       int
	HostIP        string
	Interface     string
	InstallMinion bool
	UsePVECA      bool
	SaltVersion   string
}

var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// Validate checks values that are interpolated into the remote shell script.
func (o SaltInstallOptions) Validate() error {
	if !safeName.MatchString(o.MasterID) {
		return fmt.Errorf("invalid salt master id %q", o.MasterID)
	}
	if o.APIUser == "" {
		o.APIUser = "saltapi"
	}
	if !safeName.MatchString(o.APIUser) {
		return fmt.Errorf("invalid salt api user %q", o.APIUser)
	}
	if o.APIPort == 0 {
		o.APIPort = 8000
	}
	if o.APIPort < 1 || o.APIPort > 65535 {
		return fmt.Errorf("invalid salt api port %d", o.APIPort)
	}
	if o.Interface == "" {
		o.Interface = "0.0.0.0"
	}
	if net.ParseIP(o.Interface) == nil && o.Interface != "0.0.0.0" && o.Interface != "::" {
		return fmt.Errorf("invalid salt interface %q", o.Interface)
	}
	if o.HostIP != "" && net.ParseIP(o.HostIP) == nil {
		return fmt.Errorf("invalid host IP %q", o.HostIP)
	}
	if o.SaltVersion == "" {
		o.SaltVersion = "3008.3"
	}
	if !regexp.MustCompile(`^[0-9]+(\.[0-9]+){1,3}$`).MatchString(o.SaltVersion) {
		return fmt.Errorf("invalid salt version %q", o.SaltVersion)
	}
	return nil
}

// SaltInstallScript returns a root shell script that installs/updates Salt
// master, salt-api, and (optionally) a local minion. It is idempotent: package
// installation, config writes, TLS issuance, passwords, and service restarts
// are all guarded by existing state.
func SaltInstallScript(o SaltInstallOptions) (string, error) {
	if details := o.Validate(); details != nil {
		return "", details
	}
	if o.APIUser == "" {
		o.APIUser = "saltapi"
	}
	if o.APIPort == 0 {
		o.APIPort = 8000
	}
	if o.Interface == "" {
		o.Interface = "0.0.0.0"
	}
	if o.SaltVersion == "" {
		o.SaltVersion = "3008.3"
	}

	sans := []string{"DNS:salt-api", "DNS:" + o.MasterID, "DNS:localhost", "IP:127.0.0.1"}
	if o.HostIP != "" {
		sans = append(sans, "IP:"+o.HostIP)
	}
	sanLine := strings.Join(sans, ",")

	minion := "0"
	if o.InstallMinion {
		minion = "1"
	}
	pveCA := "0"
	if o.UsePVECA {
		pveCA = "1"
	}

	var b strings.Builder
	fmt.Fprintf(&b, `#!/usr/bin/env bash
set -euo pipefail

MASTER_ID=%q
API_USER=%q
API_PORT=%q
SALT_INTERFACE=%q
SAN=%q
INSTALL_MINION=%s
USE_PVE_CA=%s
SALT_VERSION=%q
VENV=/opt/argus-salt/venv

export DEBIAN_FRONTEND=noninteractive

if [ "$(id -u)" -ne 0 ]; then
  echo "salt bootstrap: must run as root" >&2
  exit 1
fi

install -d -m 0700 /etc/argus /etc/argus/tls /etc/salt/master.d /etc/salt/minion.d /srv/salt/argus /srv/pillar
install -d -m 0755 /opt/argus-salt /var/cache/salt /var/log/salt /var/run/salt /var/run/salt/master

# ── isolated Salt runtime ────────────────────────────────────────────────────
# Debian trixie does not ship Salt in its main archive. Keep Salt in a venv so
# it cannot disturb Proxmox's system Python or pve-manager dependencies.
if [ ! -x "$VENV/bin/salt-master" ] || ! "$VENV/bin/salt-master" --version 2>/dev/null | grep -q "$SALT_VERSION"; then
  apt-get update
  apt-get install -y python3-venv python3-dev build-essential libssl-dev libffi-dev
  if [ ! -x "$VENV/bin/python" ]; then
    python3 -m venv "$VENV"
  fi
  "$VENV/bin/pip" install --upgrade pip
  "$VENV/bin/pip" install "salt==$SALT_VERSION"
fi
VENV_BIN="$VENV/bin"

# ── API user + password ──────────────────────────────────────────────────────
PASSFILE=/etc/argus/salt-api-password
if [ ! -s "$PASSFILE" ]; then
  openssl rand -base64 32 | tr -d '\n' > "$PASSFILE"
  chmod 600 "$PASSFILE"
fi
API_PASSWORD=$(cat "$PASSFILE")
if ! id -u "$API_USER" >/dev/null 2>&1; then
  useradd --system --create-home --shell /usr/sbin/nologin "$API_USER"
fi
printf '%%s:%%s\n' "$API_USER" "$API_PASSWORD" | chpasswd

# ── TLS certificate ──────────────────────────────────────────────────────────
CRT=/etc/argus/tls/salt-api.crt
KEY=/etc/argus/tls/salt-api.key
CSR=/etc/argus/tls/salt-api.csr
EXT=/etc/argus/tls/salt-api.ext
if [ ! -s "$CRT" ] || [ ! -s "$KEY" ] || ! openssl x509 -checkend 2592000 -noout -in "$CRT" >/dev/null 2>&1; then
  openssl req -new -newkey rsa:2048 -nodes -keyout "$KEY" -out "$CSR" \
    -subj "/CN=salt-api" >/dev/null 2>&1
  printf 'subjectAltName=%%s\n' "$SAN" > "$EXT"
  if [ "$USE_PVE_CA" = "1" ] && [ -s /etc/pve/pve-root-ca.pem ] && [ -s /etc/pve/priv/pve-root-ca.key ]; then
    openssl x509 -req -in "$CSR" -CA /etc/pve/pve-root-ca.pem \
      -CAkey /etc/pve/priv/pve-root-ca.key -set_serial "0x$(openssl rand -hex 16)" \
      -out "$CRT" -days 825 -sha256 -extfile "$EXT" >/dev/null 2>&1
  else
    openssl req -x509 -newkey rsa:2048 -nodes -keyout "$KEY" -out "$CRT" \
      -days 825 -subj "/CN=salt-api" -addext "subjectAltName=$SAN" >/dev/null 2>&1
  fi
  chmod 600 "$KEY"
  chmod 644 "$CRT"
  rm -f "$CSR" "$EXT"
fi

# ── master + API config ──────────────────────────────────────────────────────
cat > /etc/salt/master.d/argus.conf <<EOF
interface: $SALT_INTERFACE
auto_accept: False
auth.pam.python: $VENV_BIN/python
auth.pam.service: login
netapi_enable_clients:
  - local
  - local_async
  - wheel
  - runner
file_roots:
  base:
    - /srv/salt
pillar_roots:
  base:
    - /srv/pillar
external_auth:
  pam:
    $API_USER:
      - .*
      - '@wheel'
      - '@runner'
EOF

cat > /etc/salt/master.d/api.conf <<EOF
rest_cherrypy:
  port: $API_PORT
  host: 0.0.0.0
  ssl_crt: $CRT
  ssl_key: $KEY
EOF
chmod 600 /etc/salt/master.d/argus.conf /etc/salt/master.d/api.conf

# ── optional local minion ────────────────────────────────────────────────────
if [ "$INSTALL_MINION" = "1" ]; then
  cat > /etc/salt/minion.d/argus.conf <<EOF
master: 127.0.0.1
id: $MASTER_ID
EOF
  chmod 600 /etc/salt/minion.d/argus.conf
fi

# ── systemd units ────────────────────────────────────────────────────────────
cat > /etc/systemd/system/argus-salt-master.service <<EOF
[Unit]
Description=argus Salt master
Documentation=https://docs.saltproject.io/
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
Environment=PYTHONPATH=$VENV/lib/python3.13/site-packages
ExecStart=$VENV_BIN/salt-master
Restart=on-failure
RestartSec=5
ExecStartPre=/usr/bin/install -d -m 0755 /var/run/salt /var/run/salt/master
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF

cat > /etc/systemd/system/argus-salt-api.service <<EOF
[Unit]
Description=argus Salt API
Documentation=https://docs.saltproject.io/
After=network-online.target argus-salt-master.service
Wants=network-online.target
Requires=argus-salt-master.service

[Service]
Type=simple
Environment=PYTHONPATH=$VENV/lib/python3.13/site-packages
ExecStart=$VENV_BIN/salt-api
Restart=on-failure
RestartSec=5
ExecStartPre=/usr/bin/install -d -m 0755 /var/run/salt /var/run/salt/master
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF

if [ "$INSTALL_MINION" = "1" ]; then
cat > /etc/systemd/system/argus-salt-minion.service <<EOF
[Unit]
Description=argus Salt minion
Documentation=https://docs.saltproject.io/
After=network-online.target argus-salt-master.service
Wants=network-online.target
Requires=argus-salt-master.service

[Service]
Type=simple
Environment=PYTHONPATH=$VENV/lib/python3.13/site-packages
ExecStart=$VENV_BIN/salt-minion
Restart=on-failure
RestartSec=5
ExecStartPre=/usr/bin/install -d -m 0755 /var/run/salt /var/run/salt/master
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF
fi

chmod 0644 /etc/systemd/system/argus-salt-*.service
systemctl daemon-reload
systemctl enable --now argus-salt-master.service argus-salt-api.service >/dev/null 2>&1 || true
systemctl restart argus-salt-master.service argus-salt-api.service
if [ "$INSTALL_MINION" = "1" ]; then
  systemctl enable --now argus-salt-minion.service >/dev/null 2>&1 || true
  systemctl restart argus-salt-minion.service
fi

# ── accept the local minion key ──────────────────────────────────────────────
if [ "$INSTALL_MINION" = "1" ]; then
  for _ in $(seq 1 30); do
    "$VENV_BIN/salt-key" -L 2>/dev/null | grep -qx "$MASTER_ID" && break
    sleep 1
  done
  "$VENV_BIN/salt-key" -a "$MASTER_ID" -y >/dev/null 2>&1 || true
fi

# ── verify ───────────────────────────────────────────────────────────────────
for _ in $(seq 1 30); do
  systemctl is-active --quiet argus-salt-master.service && systemctl is-active --quiet argus-salt-api.service && break
  sleep 1
done
systemctl is-active argus-salt-master.service >/dev/null
systemctl is-active argus-salt-api.service >/dev/null
echo "ARGUS_SALT_MASTER=ok"
echo "ARGUS_SALT_API=ok"
echo "ARGUS_SALT_API_USER=$API_USER"
echo "ARGUS_SALT_API_PORT=$API_PORT"
if [ "$INSTALL_MINION" = "1" ]; then
  echo "ARGUS_SALT_MINION=$MASTER_ID"
fi
`, o.MasterID, o.APIUser, fmt.Sprintf("%d", o.APIPort), o.Interface, sanLine, minion, pveCA, o.SaltVersion)

	return b.String(), nil
}

// SaltAPIPasswordCommand reads the generated salt-api password without
// printing it in the install script output.
func SaltAPIPasswordCommand() string {
	return "cat /etc/argus/salt-api-password"
}
