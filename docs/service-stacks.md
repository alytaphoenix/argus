# Service stacks

A stack is a small TOML file that describes a group of containers, the Salt
minion that runs them, and every host path they use. argus renders the
definition into a Compose file and a Salt state, so you can review the exact
change before it reaches a host.

Stacks are installed through the existing Salt bootstrap path. argus writes
`/srv/salt/argus/<stack>/compose.yaml` on the master, then applies
`argus.<stack>` on the stack's target minion.

## Location

```text
~/.config/argus/stacks/*.toml
```

`ARGUS_STACKS_DIR` overrides the directory. Files are loaded at startup by the
TUI, `argus bootstrap`, and `argus stack`. A user file with the same name as a
built-in stack replaces it, which is how you customize the built-in `servarr`
stack without editing the binary.

## Commands

```bash
argus stack list                     # built-in + user stacks
argus stack show servarr             # target, install root, services
argus stack show servarr --compose   # what Compose will receive
argus stack show servarr --sls       # what Salt will apply
argus stack init servarr             # write ~/.config/argus/stacks/servarr.toml
argus stack apply servarr --yes      # install/update on the target minion
argus stack apply servarr --dry-run  # readiness plan only
argus stack apply servarr --yes --target media01   # one-off target override
```

`apply` requires SSH access to the Proxmox host (for the Salt master) and an
accepted Salt key for the target minion. Without a target the plan reports a
warning and `apply` refuses to run.

A stack with a `[provision]` block creates the target VM itself, then installs
a Salt minion on it and applies the stack. When the target key is already
accepted, provisioning is skipped, so re-applying is safe. `--dry-run` prints
`would provision <name> on <node> (vmid <id>)` and never changes a host.

## Format

```toml
name        = "servarr"              # also the Salt state name: argus.servarr
description = "Media automation"
target      = "phoenix-servarr"      # accepted Salt minion ID
node        = "proxmox"              # Proxmox node, informational
vmid        = 101                    # Proxmox VMID, informational
install_root = "/opt/argus/servarr"  # compose.yaml and service configs
media_root   = "/mnt/media"          # mounted at /data in every service
puid         = 1000
pgid         = 1000
timezone     = "UTC"

[[service]]
id          = "sonarr"
name        = "Sonarr"
image       = "lscr.io/linuxserver/sonarr:latest"
port        = 8989                   # host:container, same port
config_path = "/opt/argus/servarr/sonarr/config"
health_path = "/ping"

[[service]]
id     = "worker"
image  = "example/worker:1"
config_path = "worker"               # relative to install_root
config_target = "/config"
env = { MODE = "batch" }

[[service.volume]]
host = "/mnt/downloads"
container = "/downloads"
read_only = false
```

Rules enforced before rendering:

- `name`, `id` must match `[a-z0-9][a-z0-9._-]*`.
- `install_root`, `media_root`, `config_path`, volume hosts and container paths
  must be absolute, clean Linux paths with no `..`.
- A relative `config_path` is resolved under `install_root`.
- Service IDs and published ports must be unique.
- Image names cannot contain whitespace.
- Unknown TOML keys are errors, so a typo never silently changes behavior.

## Provisioning the target VM

Add a `[provision]` block when the stack should own its guest. `apply` creates
the VM, waits for SSH, installs and starts `salt-minion`, accepts its key on the
master, and only then applies the stack.

```toml
[provision]
node = "proxmox"
vmid = 104
name = "phoenix-hermes"
cloud_image = "local:iso/noble-server-cloudimg-amd64.img"
storage = "local-lvm"
cores = 2
memory_mb = 4096
disk_gb = 32
bridge = "vmbr0"
ci_user = "ubuntu"          # never "admin": Ubuntu cloud images already have an admin group
ipconfig = "ip=192.168.0.104/24,gw=192.168.0.1"
nameserver = "192.168.0.42"
searchdomain = "phoenix.arpa"
ssh_public_key = "ssh-ed25519 AAAA... argus"
ssh_private_key = "/Users/alytaphoenix/.ssh/argus_bootstrap"
```

How it works:

- `cloud_image` is a Proxmox volume ID that already exists on the master
  (usually under `local:iso/`). argus runs `qm create`, `qm importdisk`,
  `qm set`, an optional `qm resize`, and `qm start` on the Proxmox host over
  SSH. The public key is written through a temporary file, never inline in a
  shell command.
- `template` (an existing VMID) is the alternative to `cloud_image`: argus
  performs a full clone, resizes it, applies cloud-init, and starts it. Exactly
  one of `template` or `cloud_image` is required.
- `node`, `name`, `storage`, `ipconfig`, and `ssh_public_key` are required.
  `vmid` is optional; argus asks Proxmox for the next free ID when it is 0.
- `minion_id` defaults to the stack's `target`.
- `cores`, `memory_mb`, `disk_gb`, `bridge`, `ci_user`, `nameserver`, and
  `searchdomain` fall back to `[defaults]` in `config.toml` when omitted.
  `ssh_private_key` falls back to `[globals] ssh_key`.
- The target address comes from `ipconfig`; use `ip=dhcp` to wait for the QEMU
  guest agent to report a lease.
- If the target's Salt key is already accepted the whole step is skipped, and
  an existing VMID is started rather than recreated.
- Keys never appear in logs. `--dry-run` only prints
  `would provision <name> on <node> (vmid <id>)`.

## Built-in servarr stack

`stack/builtin/servarr.toml` defines Prowlarr, Sonarr, Radarr, Lidarr, and
Whisparr with standard ports. The first four use the LinuxServer images;
Whisparr uses `ghcr.io/hotio/whisparr:v3` because upstream ships no official
image, `lscr.io/linuxserver/whisparr` no longer exists on the registry, and
hotio's `latest` tag still tracks the 2.2 release line.

| Service | Port | Config |
| --- | --- | --- |
| Prowlarr | 9696 | `/opt/argus/servarr/prowlarr/config` |
| Sonarr | 8989 | `/opt/argus/servarr/sonarr/config` |
| Radarr | 7878 | `/opt/argus/servarr/radarr/config` |
| Lidarr | 8686 | `/opt/argus/servarr/lidarr/config` |
| Whisparr | 6969 | `/opt/argus/servarr/whisparr/config` |

All five mount `media_root` at `/data`, which keeps imports and hardlinks on the
same filesystem. Keep downloads and the media library under one mount to avoid
cross-device moves.

Customize it rather than applying the built-in target blindly:

```bash
argus stack init servarr --target phoenix-servarr
$EDITOR ~/.config/argus/stacks/servarr.toml
argus stack show servarr --compose
argus stack apply servarr --yes
```

## Prerequisites on the target minion

The rendered Salt state includes `argus.docker`, which installs Docker and
Compose. It does not prepare storage or secrets:

- Mount NFS/TrueNAS media at `media_root` before applying. Docker will create an
  empty directory if the mount is missing, and the apps will see an empty
  library instead of an error.
- Set `puid`/`pgid` to the owner of the media files.
- Reserve the published ports; argus does not check for conflicts on the host.
- API keys are generated by each app on first boot. Add them to
  `[servarr.<app>]` in `config.toml` (or the config editor) after the containers
  are healthy. argus intentionally does not read them from container files.

## After apply

`argus stack apply` registers each published service endpoint in the local
service registry, so it appears under **Services** with health checks and can be
wired for DNS/Traefik. Registration uses `<target>.<globals.domain>` when a
domain is configured.

Applying the same stack again is safe: Salt keeps the Compose file current and
the `cmd.wait` state restarts the project when the file changes.

## Relationship to formulas

Formulas in `formula/catalog.go` remain the opinionated platform baseline
(base, Docker, DNS, certificates, and so on). A stack is an installable formula
generated from a TOML definition: it carries the same `KindBuiltin`, file
materialisation, dependency resolution, and apply status as the built-ins. The
difference is where it runs: a stack has a `target` minion, while platform
formulas apply on the Salt master.
