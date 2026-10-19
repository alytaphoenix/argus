# argus

[![CI](https://github.com/alytaphoenix/argus/actions/workflows/ci.yml/badge.svg)](https://github.com/alytaphoenix/argus/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/alytaphoenix/argus)](https://github.com/alytaphoenix/argus/releases)
[![Go Report Card](https://goreportcard.com/badge/github.com/alytaphoenix/argus)](https://goreportcard.com/report/github.com/alytaphoenix/argus)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

A keyboard-first terminal UI for managing a Proxmox VE homelab, plus a
Prometheus exporter and an MCP server for AI agents.

argus brings the day-to-day homelab work into one place: nodes and guests,
provisioning, snapshots, Salt orchestration, service health, storage, media,
DNS, certificates, security alerts, maintenance windows, and runbooks. All
integrations except Proxmox are optional.

> argus is an independent project and is not affiliated with or endorsed by
> Proxmox Server Solutions GmbH.

```text
▸ Proxmox │  Orch │  Profiles │  Metrics │  Services │  PXE │  Security │  Maintenance
  Plex │  Runbooks │  Alerts │  Lifecycle │  Settings │  Keys        tab/shift+tab  H help
──────────────────────────────────────────────────────────────────────────────────────
⚡ argus  proxmox homelab

  NODE       STATUS    CPU                  MEMORY                    UPTIME
  proxmox    ● online  ░░░░░░░░░░░░   0.2%  ███████░░░░░ 20.4GiB/31.1GiB  80d 17h

  ↑/k up  ↓/j down  enter open  p profiles  ? keys  R refresh  q quit
```

## Features

- **Proxmox:** node/guest inventory, start/stop/reboot, snapshots, cloning,
  cloud-init provisioning, right-sizing recommendations, migration, and
  maintenance.
- **Salt orchestration:** minion keys, pending VM applies, cluster pillar,
  guided Bootstrap, and service wiring.
- **Bootstrap:** checks DNS, certificates, TrueNAS, Plex, Servarr, Hermes, and
  Signal; inventories existing guests before proposing anything; never
  reprovisions an existing VM automatically.
- **Services:** health checks, service registry, DNS records, certificates,
  TrueNAS pools/datasets, Home Assistant.
- **Observability:** Prometheus queries, local resource history, Loki log
  browsing, and a built-in metrics exporter.
- **Security:** CrowdSec decisions, Wazuh agents/alerts, TCP port scanning.
- **Operations:** HA-aware maintenance schedules, runbooks, alert routes, VM
  lifecycle policies, and DB-backed settings.
- **AI:** optional Claude explanations in the TUI and a full MCP stdio server
  for agents.
- **Multi-platform:** macOS, Linux, and Windows; uses the OS keychain for
  secrets.

## Install

```bash
go install github.com/alytaphoenix/argus@latest
argus
```

Go 1.25+ is required; the module pins a patched Go 1.26.x toolchain for
security fixes. Make sure `$(go env GOPATH)/bin` is on your `PATH`.

### Build from source

```bash
git clone https://github.com/alytaphoenix/argus.git
cd argus
make build        # argus
make build-all    # argus + argus-metrics-server + argus-mcp-server
make test
```

### First run

With no working Proxmox config, `argus` shows a setup screen, tests the
connection, writes a minimal config, and stores the password in the keychain.
On success it opens **Orch → Bootstrap** so the rest of the homelab can be
walked through in the same sitting.

```bash
argus config       # interactive config editor
argus --help
argus --version
```

## Documentation

- [Documentation index](docs/README.md) — start here.
- [Configuration](docs/configuration.md) — auth, secrets, env vars, sections.
- [Deployment](docs/deployment.md) — workstation, Proxmox host, systemd,
  upgrades.
- [Architecture](docs/architecture.md) — packages, screens, data flow.
- [Security and hardening](docs/security.md) — tokens, TLS, metrics, MCP,
  runbooks.
- [Troubleshooting](docs/troubleshooting.md).
- [Contributing](CONTRIBUTING.md) and [Code of Conduct](CODE_OF_CONDUCT.md).
- [Changelog](CHANGELOG.md).

## Proxmox host install

`scripts/bootstrap-pve.sh` installs the TUI binaries, a 0600 config, and the
`argus-metrics` systemd unit on a Debian/Proxmox host. It is idempotent and
non-destructive: without `--token` it does not touch `/etc/pve`, VMs,
containers, storage, networking, or Proxmox services.

```bash
# workstation
make dist-linux
scp -r dist/linux-amd64 root@pve:/root/argus-dist
scp scripts/bootstrap-pve.sh root@pve:/root/

# Proxmox host
/root/bootstrap-pve.sh --source /root/argus-dist --dry-run
/root/bootstrap-pve.sh --source /root/argus-dist
```

Add `--token` to create a Proxmox API token on first run. See
[deployment](docs/deployment.md) for the full guide, including service
management, upgrades, and token scoping.

The metrics endpoint binds to `127.0.0.1:9099` by default and has no
authentication. Put a firewall or authenticated reverse proxy in front of it
before exposing it remotely.

## Configuration

Copy [`config.toml.example`](config.toml.example) to
`~/.config/argus/config.toml`, or run `argus config`. Secrets belong in the OS
keychain; the config file is a fallback and is written with mode `0600`.

`ARGUS_CONFIG` overrides the config path. `ARGUS_*` environment variables
override individual settings; legacy `RATATASKR_*` names are still accepted.

## Security

argus can hold privileged credentials and can power-cycle VMs, reboot nodes,
apply Salt states, and run runbooks. Treat the workstation and any MCP client as
trusted administrators. See [SECURITY.md](SECURITY.md) for reporting,
[docs/security.md](docs/security.md) for the threat model and hardening
checklist, and the [October 2026 security review](docs/security-review-2026-10.md)
for recent findings.

## Contributing

Issues and pull requests are welcome. Read [CONTRIBUTING.md](CONTRIBUTING.md)
and run `make test`, `make vet`, `make lint`, and `make vuln` before opening a
PR. Please report security issues privately.

## License

[MIT](LICENSE) © 2026 Alyta Phoenix
