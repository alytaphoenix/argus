# Changelog

All notable changes to argus are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Stack-driven provisioning: an optional `[provision]` block creates the
  stack's target VM from a Proxmox cloud image or template, injects cloud-init
  SSH access, installs and accepts the Salt minion key, then applies the stack.
  Stacks also gained per-service `command` and `config_owner` fields, so a
  container can run a non-default command and own its bind-mounted config
  directory. See [docs/service-stacks.md](docs/service-stacks.md).
- Declarative service stacks: TOML definitions under
  `~/.config/argus/stacks/*.toml` describe a target Salt minion, install root,
  media mount, PUID/PGID, timezone, and `[[service]]` containers. argus renders
  a deterministic Compose file and Salt SLS, merges the stack into the formula
  registry, and applies `argus.<stack>` on the declared minion. Includes a
  built-in `servarr` stack for Prowlarr, Sonarr, Radarr, Lidarr, and Whisparr,
  plus `argus stack list|show|init|apply`. See
  [docs/service-stacks.md](docs/service-stacks.md).
- Guest destroy: `DELETE /nodes/<node>/<type>/<vmid>` in the Proxmox client,
  `argus vm list|delete` (stopped-only, `--yes`, optional `--name` identity
  check, purge and task wait), and a TUI `d` action with an explicit
  confirmation banner. Local provisioning rows are removed on success.
- Whisparr support in `[servarr.whisparr]`, the config editor, orchestration
  health checks, and the media service catalog.
- [docs/review-servarr-orchestration.md](docs/review-servarr-orchestration.md):
  orchestrator/DCIM and usability review from the phoenix-servarr case.

- VM SSH key bootstrap: every new QEMU VM is now created with a working SSH
  public key. First-run setup gained an optional "SSH key" field; leaving it
  blank generates an Ed25519 keypair at `~/.ssh/argus_ed25519` (directory `0700`,
  private key `0600`, public `0644`) before the connection test runs.
- Keys screen third section "VM cloud-init": shows the injected key's SHA-256
  fingerprint and the managed private key path, with `g` generate, `i` paste and
  `d` clear. Rotating or clearing asks first, and only `[defaults] ci_ssh_key`
  is cleared — private key files and `[globals] ssh_key` are left alone.
- `sshkey/` package: Ed25519 generation with atomic writes, `Ensure` reuse of an
  existing valid pair, line-strict authorized-keys normalization, fingerprints.
  Only public material is ever stored in the config or shown in the UI.
- Generating the keypair records its path in `[globals] ssh_key` only when that
  setting is empty or already points at the managed pair. An operator key living
  there is the node SSH / bootstrap key, and repointing it at a key no host
  authorizes would break SSH behind an action that promises "future VMs only".

- TrueNAS 26.0/27.x are supported. TrueNAS 26 deleted the REST v2.0 API, so
  `TrueNASClient` now probes `GET /api/v2.0/system/info` once and falls back to
  the JSON-RPC 2.0 WebSocket API at `/api/current`
  (`auth.login_with_api_key`), mapping each internal REST-style path onto the
  matching method (`pool.query`, `pool.dataset.query`, `alert.list`,
  `service.query`, `sharing.nfs.query|create|delete`,
  `service.start|stop|restart`). One binary drives a 25.10 and a 27.x box with
  no configuration change, and `[truenas] insecure` governs the first dial *and*
  every reconnect.
- `argus truenas` CLI: `status`, `datasets`, `services`,
  `start|stop|restart <service>`, `nfs-list`, `nfs-create <path> <network>`,
  `nfs-delete <id>`, `ds-create <name> [type]`, `ds-delete <name>`. Backed by
  new client methods — `CreateDataset`, `DeleteDataset`, `StartService`,
  `StopService`, `RestartService`, `NFSShares`, `CreateNFSShare`,
  `DeleteNFSShare` — and `TrueNASClient.Host()` for NFS mount devices.
- [docs/truenas.md](docs/truenas.md): transport matrix, per-subcommand
  REST/JSON-RPC mapping, and behaviour notes. Verified against
  `TrueNAS-27.0.0-RC.1`.

### Changed

- TrueNAS writes over REST send an explicit `Content-Length` instead of relying
  on chunked framing.
- Provisioning resolves the cloud-init key the same way on every path — form
  field first, then `[defaults].ci_ssh_key` — and refuses to clone with a clear
  error when neither yields a valid key, instead of creating a VM nobody can log
  into. A cloud-init profile with a malformed key no longer silently loads
  nothing. Existing VMs are never modified.
- `proxmox_clone_vm` (MCP) falls back to `[defaults].ci_ssh_key` and validates
  the key before asking Proxmox for a VMID or starting a clone.
- The new Keys section keeps `tab`/`shift+tab` while its paste dialog or a
  confirmation is open, like every other form screen.

### Fixed

- Prowlarr and Lidarr showed as offline in orchestration and status views. The
  *arr client hardcoded `/api/v3`, but both apps only serve `/api/v1` (Sonarr,
  Radarr, and Whisparr serve `/api/v3`), so every call returned 404. The client
  now selects the API version per app, so system status, health, and queue work
  for all five.
- The built-in `servarr` stack pulled `lscr.io/linuxserver/whisparr:latest`,
  which no longer exists on the registry (`denied` / Docker Hub 404), so
  `stack apply` failed while pulling Whisparr. Whisparr now uses
  `ghcr.io/hotio/whisparr:v3`: upstream publishes no official image and the
  Servarr wiki points at hotio, whose `latest` tag still tracks the 2.2 line.
- TrueNAS datasets came back as pool roots only. `pool.dataset.query` nests
  children inside their parent row unless the query asks for
  `extra={"flat":true}`, so the Plex screen's media-root picker (which matches
  dataset names like `media`/`movies`/`tv` and builds `/mnt/<name>`) found no
  library path on a pool named `PhoenixNest`, and the orchestrator's TrueNAS
  ingest wrote the pool root `/mnt/PhoenixNest` into the cluster pillar instead
  of `/mnt/PhoenixNest/Media`. Both transports request flat rows now; entries
  deeper than one level stay filtered out as content folders.
- TrueNAS readiness: `system.info` stopped reporting `system_ready` on 26+, which
  decoded to `false` and made the Services → TrueNAS badge and the
  `truenas_status` MCP tool call a healthy NAS "not ready". Readiness now comes
  from `system.ready`.
- `salt_apply_highstate` (MCP) passed the literal `"highstate"` to
  `state.apply`, which newer Salt interprets as an SLS name and rejects.
  Highstate is `state.apply` with no positional argument, so the tool now passes
  an empty state.
- Cancelling a VM cloud-init rotate/clear left the action armed, so a later
  `d` + `Y` on a Global or Per-node credential row ran the stale cloud-init
  action instead of deleting the selected row. Arming a delete now resets it.
- Keys screen text was shoved to the right because lipgloss pads the blank lines
  created by a newline inside `Render()`; newlines now stay outside the styled
  string (also fixes the Per-node SSH header).
- `ui` tests no longer read the developer's real OS keychain; they run against
  go-keyring's in-memory mock instead of triggering an authorization prompt.

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
