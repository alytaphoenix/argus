# Changelog

All notable changes to argus are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `H` help overlay with context-sensitive key bindings on every screen.
- `argus --version` and version reporting from the MCP server.
- Idempotent `scripts/bootstrap-pve.sh` installer for Proxmox VE hosts,
  including `--dry-run`, `--no-systemd`, `--build`, and additive `--token`
  modes.
- `make dist-linux` for Linux/amd64 release binaries.
- Public-release documentation: architecture, configuration, deployment,
  security, troubleshooting, contributing, and changelog.
- CI, GoReleaser release automation, Dependabot, and issue/PR templates.

### Changed

- Provision/Resize forms and confirmation dialogs now own the Tab key; global
  section navigation no longer steals focus or bypasses confirmations.
- Provision form: `q` is typed into fields instead of quitting; Enter submits
  from the last field; numeric/VMID/template inputs are validated.
- Proxmox auth failures now show an actionable hint on the Nodes screen.
- Metrics exporter binds to `127.0.0.1:9099` by default and uses explicit
  HTTP server timeouts.
- Proxmox API tokens are preferred; the default SSH user is now the local
  username instead of a hard-coded developer name.
- Wazuh TLS verification is on by default (`insecure = true` is opt-in).
- Plex authentication uses the `X-Plex-Token` header instead of a URL query
  parameter.
- The SQLite database and log file are created/forced to mode `0600`.

### Fixed

- Nil-database panics in Salt profile application, pillar push, TrueNAS sync,
  and TrueNAS ingest.
- Confirmations are no longer dismissed by unrelated keys.
- step-ca fingerprint pinning now verifies the CA root and trusts it for
  subsequent TLS requests instead of merely disabling verification.
- Health-check and Salt-detail staticcheck findings.
- Terminal escape/control-character injection through remote names, logs,
  alerts, and media metadata is sanitized before rendering.
- SSH sessions pass passwords via `SSHPASS` (not argv) and use
  `StrictHostKeyChecking=accept-new` rather than disabling host-key checks.

### Security

- Updated the build toolchain to a patched Go release; `govulncheck ./...`
  reports no known vulnerabilities for the module.
- Added a security policy and hardening checklist.
- Removed agent-local editor state from the public tree.

## [0.1.0] - 2026-10-19

Initial public release.

### Highlights

- Proxmox nodes, guests, snapshots, provisioning, resizing, migration, and
  maintenance workflows.
- Salt orchestrator with key acceptance, pending-VM applies, cluster pillar,
  and a guided Bootstrap tab.
- Optional integrations: Salt, Nomad, Prometheus, Loki, TrueNAS, Plex and the
  Servarr suite, CrowdSec, Wazuh, Technitium DNS, step-ca, Matchbox PXE,
  Home Assistant, Hermes, and Anthropic Claude.
- MCP stdio server exposing the same integrations as tools for AI agents.
- Prometheus metrics exporter and generated scrape configuration.
- Guided first-run setup and an interactive config editor.
