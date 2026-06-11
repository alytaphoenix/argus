# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
go run .                # run without building
go build -o ratataskr .  # build binary
make build
make run
go vet ./...            # lint
go test ./...           # run all tests
go test ./api/...       # test API clients only
go test ./db/...        # test database layer only
```

## Architecture

Bubble Tea TUI for managing a Proxmox homelab. Integrates Proxmox, Salt, Nomad, Prometheus, TrueNAS, Plex/Servarr, CrowdSec, Wazuh, Technitium DNS, Smallstep CA, Matchbox PXE, and Anthropic Claude.

**Navigation flow:**
- Proxmox: Nodes → (enter) Guests → (enter) Guest detail
- Salt:    Nodes → (m) Minions → (enter) Minion detail
- Nomad:   Nodes → (n) Jobs    → (enter) Job detail + allocations
- `b`/`esc` always goes back one level; all second-level screens return to Nodes
- `tab`/`shift+tab` cycles through top-level screens

**Top-level screens (tab order):**
1. `Proxmox` — nodes list + resource utilization badges
2. `Salt` — minion list + state apply (optional; omit [salt] config to hide)
3. `Nomad` — job list + allocations (optional; omit [nomad] config to hide)
4. `Profiles` — cloud-init VM templates
5. `Metrics` — Prometheus + DB-backed resource history
6. `Services` — DNS/CA/TrueNAS/health checks/service registry
7. `PXE` — Matchbox profiles, groups, spawn wizard
8. `Security` — CrowdSec decisions + Wazuh agents/alerts + TCP port scan
9. `Maintenance` — scheduled cluster-wide OS/Proxmox updates with HA awareness
10. `Plex` — Plex Media Server + Servarr companion services checklist
11. `Settings` — DB-backed configurable thresholds and timeouts
12. `Keys` — macOS Keychain credential manager

**Packages:**

- `config/` — loads `~/.config/ratataskr/config.toml` or env vars. `[salt]`, `[nomad]`, `[plex]`, and `[servarr.*]` are optional; omitting them disables those screens/features.
- `api/client.go` — Proxmox HTTP client, `PVEAPIToken` header auth, response unwrapper (`{"data": ...}`).
- `api/proxmox.go` — `Nodes`, `Guests` (merges qemu+lxc sorted by VMID), `GuestAction`, `GuestStatus`, `NodeVersion`, `HAResources`, `MigrateHAResourcesOffNode`, `NodeAPTUpdate`, `WaitForTask`, `NodeReboot`.
- `api/salt.go` — session login → `X-Auth-Token`. `Minions` = GET /minions (cached grains) + live `test.ping`. `ApplyState` uses `local_async` (returns JID immediately). `JobsForMinion` filters recent jobs by target.
- `api/nomad.go` — `NomadClient` with `X-Nomad-Token` (optional ACL). `Jobs` (GET /jobs?meta=true with summaries), `JobAllocations`, `StopJob` (DELETE /job/{id}). `NomadJob.Totals()` sums running/failed/lost across task groups.
- `api/plex.go` — `PlexClient` (X-Plex-Token auth): `ServerInfo`, `Libraries`, `Sessions`, `Ping`, `AddLibrary`, `FetchAll`. `ServarrClient` (X-Api-Key auth): `SystemStatus`, `Health`, `Queue`, `Ping`, `FetchStatus`. `MediaServices` catalog (12 services).
- `api/truenas.go` — `TrueNASClient`: pools, datasets, alerts, services. Dataset `Name` is the ZFS path (e.g. `tank/media`); mount path = `/mnt/` + Name.
- `api/scan.go` — concurrent TCP port scanner, `ScanFinding`, `HealthResult`.
- `ui/app.go` — root `tea.Model`. Owns all sub-models. `saltClient`, `nomadClient`, `plexClient` are nil when unconfigured — their `open*Msg` are silently ignored. Fires `checkMediaStatusCmd` at startup for Plex/servarr badge on nodes screen.
- `ui/messages.go` — all inter-model message types for all integrations.
- `ui/keys.go` — key bindings + per-screen help strings. `nodeHelpStr(hasSalt, hasNomad bool)` shows `m`/`n` hints only when the respective client is configured.
- `ui/styles.go` — shared styles and helpers (`progressBar`, `fmtBytes`, `fmtUptime`, `statusDot`, `truncate`).
- `ui/nomad.go` — defines `nomadJobDot`/`nomadAllocDot` (shared within package by `nomad_detail.go`).
- `ui/plex.go` — `PlexModel` with 4 tabs: Status (active sessions), Libraries (TrueNAS auto-configure), Services (Servarr checklist), Provision (VM deployment instructions).
- `ui/maintenance.go` — `MaintenanceModel` with tabs: Runs, Schedules, Configure wizard. HA-aware: migrates VMs off node before reboot. Order: non-master nodes first, master last. Configurable via K/J to reorder, 1-5 to toggle task flags.
- `ui/settings.go` — `SettingsModel` for editing `app_settings` DB keys (Wazuh thresholds, scan timeout, ban lookback, health check timeout).
- `ui/security.go` — CrowdSec + Wazuh + port scan. Cross-references `service_hosts` with Wazuh agents (Managed column). Scan findings update `discovered_ports` in DB. Configurable thresholds from `AppSettings`.
- `ui/services.go` — DNS/CA/TrueNAS/service registry/health checks. Shows CrowdSec ban badge (`⚡CS`) for banned service hosts. Provision dialog emits `openPXEMsg{}` to navigate to PXE spawn.
- `db/db.go` — SQLite WAL mode, versioned migrations v1–v10. Tables: `provisioned_vms`, `ssh_host_keys`, `salt_events`, `nomad_job_events`, `services`, `health_checks`, `certificates`, `pxe_profiles`, `pxe_groups`, `pxe_boot_events`, `service_hosts`, `security_events`, `app_settings`, `maintenance_schedules`, `maintenance_runs`, `maintenance_run_steps`.
- `keyring/` — macOS Keychain via `github.com/zalando/go-keyring`. Keys: `proxmox_secret`, `salt_password`, `nomad_token`, `ssh_password`, `wazuh_password`, `crowdsec_api_key`, `matchbox_token`, `technitium_token`, `prometheus_token`, `anthropic_api_key`, `truenas_api_key`, `plex_token`.

**Sub-model pattern:** Each screen model has `Update(msg tea.Msg) (ConcreteType, tea.Cmd)` — returns its own type so `App` can reassign struct fields directly. Navigation is emitted as a `tea.Cmd` returning a typed message.

**Back navigation** in `App.Update` is explicit:
- `screenGuests`, `screenSalt`, `screenNomad`, `screenPXE`, `screenSecurity`, `screenSettings`, `screenMaintenance`, `screenPlex` → `screenNodes`
- `screenDetail` → `screenGuests`
- `screenSaltDetail` → `screenSalt`
- `screenNomadDetail` → `screenNomad`

**DB migrations:** Each version adds tables/columns atomically. Current version: v10.
- v1–v8: core tables (VMs, SSH keys, Salt/Nomad events, services, PXE, service_hosts, security_events)
- v9: `app_settings` — configurable thresholds (Wazuh levels, scan timeout, ban lookback, health check timeout)
- v10: `maintenance_schedules`, `maintenance_runs`, `maintenance_run_steps`

**app_settings keys:**
| key | default | description |
|-----|---------|-------------|
| `wazuh_critical_level` | 12 | Wazuh alert level for CRITICAL |
| `wazuh_high_level` | 10 | Wazuh alert level for HIGH |
| `wazuh_medium_level` | 7 | Wazuh alert level for MEDIUM |
| `scan_timeout_s` | 5 | TCP port scan timeout per host (seconds) |
| `ban_lookback_hours` | 24 | Hours to look back for CrowdSec bans in service badges |
| `health_check_timeout_s` | 10 | HTTP health check timeout (seconds) |
| `maintenance_reboot_wait_s` | 30 | Seconds to wait after node reboot before continuing |

## API notes

**Proxmox:** All responses wrapped `{"data": ...}`. Guest `cpu` field is fraction of total host CPUs; display as `cpu * 100 / CPUs`. Actions: POST `/nodes/{node}/{type}/{vmid}/status/{action}`. HAResources: GET `/cluster/ha/resources`. APT update: POST `/nodes/{node}/apt/update` → returns UPID; poll `/nodes/{node}/tasks/{upid}/status`. Reboot: POST `/nodes/{node}/status` command=reboot.

**Salt:** `eauth` defaults to `"pam"`. `GET /minions` returns cached grains (offline minions included). Ping result is best-effort — missing from ping result = offline.

**Nomad:** No auth needed if ACLs disabled (common in homelab). `CreateTime` in allocations is nanoseconds since epoch — divide by `1e9` for seconds. `StopJob` is a soft stop (allows drain); job can be restarted by re-submitting the job spec.

**Plex:** Auth via `X-Plex-Token` query param. `/` returns server info. `/library/sections` lists libraries. `/status/sessions` returns active streams. Library `Location[].path` contains the filesystem path. `AddLibrary` = POST `/library/sections` (agent/scanner vary by type).

**Servarr (*arr apps):** Auth via `X-Api-Key` header. Shared API shape: `/api/v3/system/status`, `/api/v3/health`, `/api/v3/queue`. Works for Sonarr v3/v4, Radarr v3/v4, Lidarr, Readarr, Prowlarr, Bazarr (uses `/api/v1`), Overseerr, Tautulli (uses own API — health/queue endpoints may differ).

**TrueNAS:** REST v2, Bearer token auth. Datasets have `Name` (ZFS path, e.g. `tank/media`) — mount path is `/mnt/` + Name. Pools have `name`, `status`, `healthy`, `size`, `used`.

## Configuration

Copy `config.toml.example` to `~/.config/ratataskr/config.toml`. All sections except `[host]`/`[token_id]`/`[secret]` are optional.

- Proxmox token: Datacenter → Permissions → API Tokens. Needs `VM.PowerMgmt` + `VM.Monitor` on `/vms/*`.
- Salt API: requires `salt-api` with CherryPy. User needs `test.ping`, `state.apply`, `grains.item` on `*`.
- Nomad: default port 4646, no TLS in dev; ACL token optional.
- Plex token: visit https://support.plex.tv/articles/204059436 or use Plex Web developer tools. Store in Keychain via Keys screen (key: `plex_token`).
- Servarr: get API key from each app's Settings → General → API Key. Store secrets in Keychain or config file.

## Testing

Tests live alongside the code they test (`_test.go` files). Run with `go test ./...`.

Key test files:
- `api/plex_test.go` — PlexClient and ServarrClient HTTP tests (httptest server)
- `api/scan_test.go` — port scanner tests
- `db/db_test.go` — migration, CRUD, and settings tests
- `config/config_test.go` — config load, env override, ApplyGlobals tests
