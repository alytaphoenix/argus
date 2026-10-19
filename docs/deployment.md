# Deployment

There are two deployment targets:

1. A workstation (macOS, Linux, or Windows) running the interactive TUI.
2. A Proxmox VE host running the optional metrics exporter, and possibly the
   TUI over SSH.

## Workstation

```bash
go install github.com/alytaphoenix/argus@latest
argus
```

Or build from source:

```bash
git clone https://github.com/alytaphoenix/argus.git
cd argus
make build-all
```

Choose **Proxmox** → first-run setup if the config is missing. The setup screen
tests the connection and stores the password in the OS keychain. On success it
opens **Orch → Bootstrap**.

## Proxmox VE host (idempotent)

`scripts/bootstrap-pve.sh` installs:

- `/usr/local/bin/argus`
- `/usr/local/bin/argus-metrics-server`
- `/usr/local/bin/argus-mcp-server`
- `/etc/argus/config.toml` (only if absent, mode `0600`)
- `/var/lib/argus` (service state/database)
- `/etc/systemd/system/argus-metrics.service`

It never touches VMs, containers, storage, networking, `/etc/pve`, or Proxmox
services unless you explicitly pass `--token`, which only adds
`root@pam!argus` to `/etc/pve/priv/token.cfg`.

### Build and copy

```bash
make dist-linux
scp -r dist/linux-amd64 root@pve:/root/argus-dist
scp scripts/bootstrap-pve.sh root@pve:/root/
```

### Dry run, then apply

```bash
ssh root@pve
/root/bootstrap-pve.sh --source /root/argus-dist --dry-run
/root/bootstrap-pve.sh --source /root/argus-dist
```

### Token auth

```bash
/root/bootstrap-pve.sh --source /root/argus-dist --token
```

On first use this creates a Proxmox token and writes `token_id`/`secret` to
`/etc/argus/config.toml`. On later runs it detects the stored token and leaves
it alone. The token has full root privileges (`privsep=0`); for a public or
multi-operator deployment, create a dedicated user/token with the smallest role
that works and replace the token:

```bash
pveum user add argus@pve --comment "argus automation"
pveum user token add argus@pve argus --privsep 0
pveum acl modify / --users argus@pve --roles PVEAdmin
pveum user token remove root@pam argus
```

### Run the TUI on the host

Use the same HOME as the service so the TUI and metrics exporter share a
database:

```bash
HOME=/var/lib/argus ARGUS_CONFIG=/etc/argus/config.toml argus
```

### Service management

```bash
systemctl status argus-metrics
journalctl -u argus-metrics -f
curl http://127.0.0.1:9099/healthz
curl http://127.0.0.1:9099/metrics
```

The metrics listener is loopback-only by default and has no authentication.
For remote Prometheus scraping, put a firewall or authenticated reverse proxy
in front of it, then set:

```toml
[prometheus]
listen = "0.0.0.0:9099"
```

### Upgrades

> **Existing installs:** the installer deliberately preserves
> `/etc/argus/config.toml`. If it still contains `listen = ":9099"`, change it
> to `listen = "127.0.0.1:9099"` and run `systemctl restart argus-metrics` to
> get the loopback-only default.

Re-run the installer with new binaries. Unchanged binaries and the existing
config are preserved; a replaced binary gets a timestamped `.bak` copy:

```bash
# on the workstation
make dist-linux
scp -r dist/linux-amd64 root@pve:/root/argus-dist

# on the host
/root/bootstrap-pve.sh --source /root/argus-dist --dry-run
/root/bootstrap-pve.sh --source /root/argus-dist
systemctl status argus-metrics
```

### Uninstall

The installer does not have an uninstall path because that would risk deleting
a working config/database. To remove argus manually:

```bash
systemctl disable --now argus-metrics
rm -f /etc/systemd/system/argus-metrics.service
systemctl daemon-reload
rm -f /usr/local/bin/argus /usr/local/bin/argus-metrics-server /usr/local/bin/argus-mcp-server
# Keep /etc/argus and /var/lib/argus unless you intend to delete the config/DB.
```

If you created the installer token, revoke it:

```bash
pveum user token remove root@pam argus
```

## Docker / containers

The TUI needs an interactive terminal and keychain access; it is not designed to
run in a headless container. The metrics exporter and MCP server can run in a
container if you mount the config/database, but keychain-backed credentials will
not work unless the container has a Secret Service or explicit config secrets.
For a homelab, running the binaries directly on the host or workstation is
simpler.

## Verification

After deployment:

```bash
argus --version
argus                       # Proxmox node list
systemctl is-active argus-metrics
curl -fsS http://127.0.0.1:9099/healthz
```

Open **Orch → Bootstrap** and press `x` to check service status. Existing VMs
are inventoried as unmanaged; Bootstrap does not reprovision them.
