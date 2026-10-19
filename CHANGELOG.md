# Changelog

All notable changes to argus are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.3.0] - 2026-10-19

### Added

- Automatic service wiring: enabled services in the Services registry now get
  a Technitium DNS record and, for HTTP/HTTPS services, a Traefik route without
  pressing `w`. Wiring refreshes after add/delete and after a provisioned node's
  Salt profile is applied.
- Automatic TLS through a step-ca ACME resolver. Traefik obtains and renews
  certificates for service names when `[stepca]` is configured; non-ACME setups
  fall back to Traefik's self-signed certificate.
- `[traefik] ingress_ip` and `[stepca] acme_provisioner` / `acme_url` settings,
  including environment overrides.
- Provisioning creates `<vm-name>.<domain>` once the QEMU guest agent reports
  an IP, records the IP in `provisioned_vms`, and shows a warning when DNS could
  not be created.
- `wiring/` package with tests for pillar generation, DNS record type selection,
  static Traefik config, and config defaults.

### Changed

- The Traefik formula now runs from `/etc/traefik/traefik.yml`, mounts
  `/etc/traefik` into the container, and enables the file provider. Re-apply the
  `traefik` formula on existing hosts.
- The Technitium token is written to `/etc/argus/technitium.token` with mode
  `0600`; the generated pillar is also `0600` and contains no tokens.
- Service wiring removes stale `*.yml` routes from `/etc/traefik/dynamic` when a
  service is deleted.

### Fixed

- Service wiring previously hard-coded `cert: false` and required a manual `w`
  press, so services never received certificates.
- Traefik dynamic configuration was written to a directory the container did not
  mount, so routes were not loaded.

## [0.2.0] - 2026-10-19

### Added

- `argus bootstrap plan|apply|status|formulas` with an idempotent,
  state-aware platform bootstrap.
- Isolated Salt 3008.x runtime on Proxmox hosts: venv install, systemd units,
  PVE-CA-signed salt-api TLS, dedicated `saltapi` PAM user, local minion, and
  firewall-friendly ports.
- Opinionated Salt formula registry with dependency resolution, built-in SLS
  files, integration entries, baseline/recommended sets, and health metadata.
- Top-level **Bootstrap** TUI screen showing platform readiness and formula
  apply status, with select/apply/baseline/filter actions.
- SQLite tracking for `platform_profiles`, `formula_installations`, and
  `bootstrap_runs`.
- `[bootstrap]` configuration and environment overrides.

### Security

- Salt API is TLS-only with a PVE-CA certificate, a non-root PAM user, and a
  minimal `netapi_enable_clients` list (`local`, `local_async`, `wheel`,
  `runner`).
- Bootstrap only reports and applies additive states; it never deletes guests,
  storage, or Proxmox configuration.

## [0.1.1] - 2026-10-19

First public release. Includes the security hardening, host deployment
tooling, and documentation prepared for public distribution.

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
- Hardened the reference Proxmox deployment: scoped `argus@pve` token, revoked
  the root token, enabled the Proxmox firewall with an explicit LAN allow rule,
  loopback-only metrics, verified TLS, and removed the stale password keychain
  entry.
