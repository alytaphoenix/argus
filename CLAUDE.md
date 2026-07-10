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
go build ./cmd/mcp-server/...  # build MCP server
```

## Architecture

Bubble Tea TUI for managing a Proxmox homelab. Integrates Proxmox, Salt, Nomad, Prometheus, Loki, TrueNAS, Plex/Servarr, CrowdSec, Wazuh, Technitium DNS, Smallstep CA, Matchbox PXE, Home Assistant, and Anthropic Claude.

**Navigation flow:**
- Proxmox: Nodes → (enter) Guests → (enter) Guest detail
- Salt:    Nodes → (m) Minions → (enter) Minion detail
- Nomad:   Nodes → (n) Jobs    → (enter) Job detail + allocations
- `b`/`esc` always goes back one level; all second-level screens return to Nodes
- `tab`/`shift+tab` cycles through top-level screens

**Top-level screens (tab order):**
1. `Proxmox` — nodes list + resource utilization badges
2. `Salt` — minion list + state apply (optional; omit [salt] config to hide)
3. `Orch` — Salt orchestrator: key acceptance, pending VM salt apply, cluster pillar management, Bootstrap tab with live service checks + provision/ingest actions (optional; requires [salt] config)
4. `Nomad` — job list + allocations (optional; omit [nomad] config to hide)
5. `Profiles` — cloud-init VM templates + salt profiles (each may carry `pillar_yaml` written to master before state.apply)
6. `Metrics` — Prometheus + DB-backed resource history + Loki log browser tab
7. `Services` — DNS/CA/TrueNAS/HA/health checks/service registry (5 tabs)
8. `PXE` — Matchbox profiles, groups, spawn wizard
9. `Security` — CrowdSec decisions + Wazuh agents/alerts + TCP port scan
10. `Maintenance` — scheduled cluster-wide OS/Proxmox updates with HA awareness
11. `Plex` — Plex Media Server + Servarr companion services checklist
12. `Runbooks` — user-defined salt/shell step sequences with run history
13. `Alerts` — alert routing rules (CrowdSec/Wazuh events → webhooks)
14. `Lifecycle` — VM lifecycle policies (stop/snapshot long-running tagged VMs)
15. `Settings` — DB-backed configurable thresholds and timeouts
16. `Keys` — macOS Keychain credential manager

Scheduled maintenance runs and due schedules are fired by a once-a-minute
scheduler tick in `ui/app.go` (`schedulerTickCmd` → `runDueSchedulesCmd`),
independent of which screen is open.

**Packages:**

- `config/` — loads `~/.config/ratataskr/config.toml` or env vars. `[salt]`, `[nomad]`, `[plex]`, `[servarr.*]`, `[homeassistant]`, `[loki]` are optional; omitting them disables those screens/features.
- `api/client.go` — Proxmox HTTP client, response unwrapper (`{"data": ...}`). Two auth modes: `PVEAPIToken` header (token_id/secret) or PVE ticket auth (username/password → `PVEAuthCookie` + `CSRFPreventionToken` on writes, re-login before the 2h ticket expiry). Username set = ticket auth wins.
- `api/proxmox.go` — `Nodes`, `Guests` (merges qemu+lxc sorted by VMID), `GuestAction`, `GuestStatus`, `NodeVersion`, `Snapshots`, `CreateSnapshot`, `DeleteSnapshot`, `HAResources`, `MigrateHAResourcesOffNode`, `NodeAPTUpdate`, `WaitForTask`, `NodeReboot`.
- `api/salt.go` — session login → `X-Auth-Token`, auto-refreshed via `ensureToken` when the cached token nears expiry (salt-api tokens expire, default 12h). `Minions` = GET /minions (cached grains) + live `test.ping`. `ApplyState` uses `local_async` (returns JID immediately). `JobResult`, `JournalLogs` (rejects unsafe unit names), `JobsForMinion`, `WriteFile` (rejects paths with quotes/newlines), `RunCmd` (cmd.run on a target). Wheel client: `ListKeys`, `AcceptKey`, `RejectKey`, `DeleteKey` (use `"client": "wheel"` via saltRun). `WritePillar(ctx, masterMinion, pillarBase, name, content)` writes `<pillarBase>/<name>.sls` to the master.
- `api/salt_wiring.go` — `WiringStateTemplate` (DNS/Traefik/step-ca wiring via Jinja). `NASMountTemplate` (NFS mounts from `cluster_pillar.storage` entries; apply as `storage.nas_mounts` state).
- `api/nomad.go` — `NomadClient` with `X-Nomad-Token` (optional ACL). `Jobs` (GET /jobs?meta=true with summaries), `JobAllocations`, `StopJob` (DELETE /job/{id}). `NomadJob.Totals()` sums running/failed/lost across task groups.
- `api/plex.go` — `PlexClient` (X-Plex-Token auth): `ServerInfo`, `Libraries`, `Sessions`, `Ping`, `AddLibrary`, `FetchAll`. `ServarrClient` (X-Api-Key auth): `SystemStatus`, `Health`, `Queue`, `Ping`, `FetchStatus`. `MediaServices` catalog (12 services).
- `api/truenas.go` — `TrueNASClient`: pools, datasets, alerts, services, `FetchAll` (combined status). Dataset `Name` is the ZFS path (e.g. `tank/media`); mount path = `/mnt/` + Name.
- `api/homeassistant.go` — `HAClient` (Bearer token auth): `Ping`, `Version`, `States`, `GetState`, `CallService`. `CallService` accepts optional `extraData map[string]any` for service parameters like brightness, color_temp.
- `api/hermes.go` — `HermesClient` (optional Bearer token): `Ping`, `Status` (returns `HermesStatus{Online, Version, GatewayURL}`). Health endpoint tries `/health` first, falls back to `/`. MCP tool: `hermes_status`.
- `api/loki.go` — `LokiClient` (optional Bearer token): `QueryRange(ctx, query, start, end, limit)`, `Labels`. Returns `[]LokiEntry{Timestamp, Labels, Line}`. Checks `result.status` in body in addition to HTTP status (some gateways return HTTP 200 with `status:"error"`).
- `api/scan.go` — concurrent TCP port scanner, `ScanFinding`, `HealthResult`.
- `ui/app.go` — root `tea.Model`. Owns all sub-models. Optional clients (`saltClient`, `nomadClient`, `plexClient`, `haClient`, etc.) are nil when unconfigured — their `open*Msg` are silently ignored. Fires `checkMediaStatusCmd` at startup for Plex/servarr badge on nodes screen.
- `ui/messages.go` — all inter-model message types for all integrations.
- `ui/keys.go` — key bindings + per-screen help strings. `nodeHelpStr(hasSalt, hasNomad bool)` shows `m`/`n` hints only when the respective client is configured.
- `ui/styles.go` — shared styles and helpers (`progressBar`, `fmtBytes`, `fmtUptime`, `statusDot`, `truncate`).
- `ui/nomad.go` — defines `nomadJobDot`/`nomadAllocDot` (shared within package by `nomad_detail.go`).
- `ui/plex.go` — `PlexModel` with 4 tabs: Status (active sessions), Libraries (TrueNAS auto-configure), Services (Servarr checklist), Provision (VM deployment instructions).
- `ui/maintenance.go` — `MaintenanceModel` with tabs: Runs, Schedules, Configure wizard. HA-aware: migrates VMs off node before reboot. Order: non-master nodes first, master last. Configurable via K/J to reorder, 1-5 to toggle task flags.
- `ui/settings.go` — `SettingsModel` for editing `app_settings` DB keys (Wazuh thresholds, scan timeout, ban lookback, health check timeout).
- `ui/security.go` — CrowdSec + Wazuh + port scan. Cross-references `service_hosts` with Wazuh agents (Managed column). Scan findings update `discovered_ports` in DB. Configurable thresholds from `AppSettings`.
- `ui/services.go` — thin coordinator (454 lines): struct, `NewServicesModel`, `Update` delegator, `handleKey`, `View`, `renderTabBar`, `lazyLoadTab`. Each tab is its own file.
- `ui/services_svc.go` — Services tab: health checks, service registry, provision/register dialogs, Salt wiring.
- `ui/services_dns.go` — DNS + step-ca tab (Technitium zones/records, add/delete).
- `ui/services_certs.go` — Certificates tab (DB-backed cert list).
- `ui/services_truenas.go` — TrueNAS tab (pools, alerts, services).
- `ui/services_ha.go` — Home Assistant tab (entity list, lazy-loaded on first visit).
- `ui/orchestrate.go` — Salt orchestrator screen (4 tabs: Keys/Pending/Pillar/Bootstrap). Keys tab: live pending key queue from wheel API, `a`=accept, `r`=reject (both require confirmation). Pending tab: provisioned VMs with `salt_profile` but no `salt_applied_at`; `s`=apply states now. Pillar tab: cluster_pillar CRUD, `t`=sync TrueNAS datasets, `p`=push YAML to `<pillar_base>/ratataskr_cluster.sls`, `n`=new manual entry, `d`=delete. Bootstrap tab: live service check grid for DNS/certs/TrueNAS/Plex/Servarr/Hermes/Signal; `x`=check all, `enter`=action dialog (wire up existing / provision missing via Salt / reprovision partial), `s`=skip. Provision prompts for target minion + Salt state, then runs `state.apply` and re-checks. Wire-up ingests service config into cluster_pillar and service_hosts. Key acceptance auto-applies the matched salt profile: writes per-profile pillar then applies each comma-separated state sequentially, polling for completion.
- `db/db.go` — SQLite WAL mode. Migrations run as sequential `CREATE TABLE IF NOT EXISTS` / `ALTER` blocks at open (not strictly version-ordered in source); the destructive v16 certificates rebuild is guarded by `PRAGMA user_version`. Tables: `cloud_init_profiles`, `salt_profiles` (+ v17 `pillar_yaml` column), `provisioned_vms`, `salt_apply_events`, `nomad_job_snapshots`, `nomad_job_events`, `services`, `service_health_checks`, `certificates`, `dns_records_cache`, `vm_resize_events`, `pxe_profiles`, `pxe_groups`, `pxe_boot_events`, `security_events`, `service_hosts`, `node_ssh_credentials`, `scan_results`, `app_settings`, `node_metrics_history`, `service_uptime_events`, `runbooks`, `runbook_runs`, `alert_routes`, `vm_lifecycle_policies`, `maintenance_schedules`, `maintenance_runs`, `maintenance_run_steps`, `cluster_pillar` (v17: key, value, source, updated_at).
- `keyring/` — macOS Keychain via `github.com/zalando/go-keyring`. Keys: `proxmox_secret`, `proxmox_password`, `salt_password`, `nomad_token`, `ssh_password`, `wazuh_password`, `crowdsec_api_key`, `matchbox_token`, `technitium_token`, `prometheus_token`, `anthropic_api_key`, `truenas_api_key`, `plex_token`, `homeassistant_token`, `loki_token`, `hermes_api_key`.
- `cmd/mcp-server/` — MCP stdio server exposing all integrations as tools for Claude/Hermes agents. Build separately with `go build ./cmd/mcp-server/`. Register with `claude mcp add ratataskr -- /path/to/ratataskr-mcp-server`.

**Sub-model pattern:** Each screen model has `Update(msg tea.Msg) (ConcreteType, tea.Cmd)` — returns its own type so `App` can reassign struct fields directly. Navigation is emitted as a `tea.Cmd` returning a typed message.

**Services tab pattern:** Each tab file implements `updateXxx(msg tea.Msg) (ServicesModel, tea.Cmd, bool)` — returns `true` if the message was handled. `services.go` calls them in sequence; first handler that returns `true` wins. Adding a new integration tab requires touching 6 marked spots in `services.go` and creating one new `ui/services_<name>.go` file.

**Back navigation** in `App.Update` is explicit:
- `screenGuests`, `screenSalt`, `screenOrchestrate`, `screenNomad`, `screenPXE`, `screenSecurity`, `screenSettings`, `screenMaintenance`, `screenPlex` → `screenNodes`
- `screenDetail` → `screenGuests`
- `screenSaltDetail` → `screenSalt`
- `screenNomadDetail` → `screenNomad`

**DB migrations:** `migrate()` executes a series of `CREATE TABLE IF NOT EXISTS`
and idempotent `ALTER TABLE` blocks in source order (the labels v1–v16 in
comments are historical, not literally ordered). `seedDefaultSettings()` inserts
`app_settings` defaults with `ON CONFLICT DO NOTHING`. The only destructive step
(v16 certificates rebuild) is gated by `PRAGMA user_version` and wrapped in a
transaction so it runs exactly once.

**app_settings keys** (note dotted key names; defaults live in `defaultSettings`):
| key | default | description |
|-----|---------|-------------|
| `wazuh.critical_level` | 12 | Wazuh rule level threshold for CRITICAL |
| `wazuh.high_level` | 10 | Wazuh rule level threshold for HIGH |
| `wazuh.medium_level` | 7 | Wazuh rule level threshold for MEDIUM |
| `scan.timeout_ms` | 600 | TCP port scan connect timeout per port (ms) |
| `scan.extra_ports` | (empty) | Extra comma-separated ports to scan |
| `services.ban_lookback_hours` | 48 | Hours to look back for CrowdSec bans in service badges |
| `services.healthcheck_timeout_s` | 15 | HTTP/TCP health check timeout (seconds) |
| `maintenance.reboot_wait_s` | 30 | Seconds to wait after node reboot before polling for it to come back |

## API notes

**Proxmox:** All responses wrapped `{"data": ...}`. Guest `cpu` field is fraction of total host CPUs; display as `cpu * 100 / CPUs`. Actions: POST `/nodes/{node}/{type}/{vmid}/status/{action}`. Snapshots: GET/POST/DELETE `/nodes/{node}/{type}/{vmid}/snapshot`. HAResources: GET `/cluster/ha/resources`. APT update: POST `/nodes/{node}/apt/update` → returns UPID; poll `/nodes/{node}/tasks/{upid}/status`. Reboot: POST `/nodes/{node}/status` command=reboot.

**Salt:** `eauth` defaults to `"pam"`. `GET /minions` returns cached grains (offline minions included). Ping result is best-effort — missing from ping result = offline. `ApplyState` with state=`"highstate"` runs highstate. `JobResult` polls for completion: returns `(done, success bool, failedStates []string, err error)`.

**Nomad:** No auth needed if ACLs disabled (common in homelab). `CreateTime` in allocations is nanoseconds since epoch — divide by `1e9` for seconds. `StopJob` is a soft stop (allows drain); job can be restarted by re-submitting the job spec.

**Plex:** Auth via `X-Plex-Token` query param. `/` returns server info. `/library/sections` lists libraries. `/status/sessions` returns active streams. Library `Location[].path` contains the filesystem path. `AddLibrary` = POST `/library/sections` (agent/scanner vary by type).

**Servarr (*arr apps):** Auth via `X-Api-Key` header. Shared API shape: `/api/v3/system/status`, `/api/v3/health`, `/api/v3/queue`. Works for Sonarr v3/v4, Radarr v3/v4, Lidarr, Readarr, Prowlarr, Bazarr (uses `/api/v1`), Overseerr, Tautulli (uses own API — health/queue endpoints may differ). `FetchStatus` runs all three calls concurrently in the MCP server.

**TrueNAS:** REST v2, Bearer token auth. Datasets have `Name` (ZFS path, e.g. `tank/media`) — mount path is `/mnt/` + Name. Pools have `name`, `status`, `healthy`, `size`, `used`.

**Home Assistant:** REST API, Bearer token auth (`Authorization: Bearer <token>`). `/api/` → ping. `/api/config` → version (`version` field). `/api/states` → all entities. `/api/states/<entity_id>` → single entity. `/api/services/<domain>/<service>` → call service (POST with JSON body including `entity_id` and optional service_data fields).

**Loki:** `/loki/api/v1/query_range` (LogQL), `/loki/api/v1/labels`. Bearer token optional. Always check `result.status` in the response body — some gateways return HTTP 200 with `{"status":"error","error":"..."}`.

## MCP Server

`cmd/mcp-server/` implements a JSON-RPC 2.0 / MCP stdio server. All integrations are optional — tools only appear in `tools/list` when the corresponding client is non-nil (configured).

**Build and register:**
```bash
go build -o ratataskr-mcp-server ./cmd/mcp-server/
claude mcp add ratataskr -- /path/to/ratataskr-mcp-server
```

**Tool groups:**
- Proxmox: `proxmox_list_nodes`, `proxmox_list_guests`, `proxmox_guest_status`, `proxmox_guest_action`, `proxmox_next_vmid`, `proxmox_clone_vm`, `proxmox_node_version`, `proxmox_list_snapshots`, `proxmox_create_snapshot`, `proxmox_delete_snapshot`
- Salt: `salt_list_minions`, `salt_apply_highstate`, `salt_apply_state`, `salt_job_result`, `salt_journal_logs`
- Nomad: `nomad_list_jobs`, `nomad_job_allocations`, `nomad_stop_job`
- Prometheus: `prom_query`, `prom_alerts`
- Loki: `loki_query`
- TrueNAS: `truenas_status`
- CrowdSec: `crowdsec_decisions`, `crowdsec_unban`
- Wazuh: `wazuh_agents`, `wazuh_alerts`
- Technitium DNS: `dns_list_zones`, `dns_list_records`, `dns_add_record`, `dns_delete_record`
- Plex: `plex_status`
- Servarr: `servarr_status` (one service or `all`; fetches concurrently)
- step-ca: `ca_health`
- Matchbox: `matchbox_list_profiles`, `matchbox_list_groups`
- Home Assistant: `hass_list_entities`, `hass_get_state`, `hass_call_service` (accepts optional `service_data` JSON string)

Credentials are loaded from config.toml first, then overlaid from the macOS Keychain (same keys as the TUI).

## Configuration

Copy `config.toml.example` to `~/.config/ratataskr/config.toml`, or just run `ratataskr` — with no working Proxmox config it shows a first-run setup screen (`ui/setup.go`) that tests the connection, writes a minimal config, and stores the password in the Keychain (`proxmox_password`). All sections beyond the Proxmox connection are optional; the orchestrator introspects the cluster at runtime. `RATATASKR_CONFIG` env var overrides the config file path.

- Proxmox auth: either API token (`token_id`/`secret`; Datacenter → Permissions → API Tokens, needs `VM.PowerMgmt` + `VM.Monitor` on `/vms/*`) or ticket auth (`username`/`password`, e.g. `root@pam`).
- Salt API: requires `salt-api` with CherryPy. User needs `test.ping`, `state.apply`, `grains.item` on `*`.
- Nomad: default port 4646, no TLS in dev; ACL token optional.
- Plex token: visit https://support.plex.tv/articles/204059436 or use Plex Web developer tools. Store in Keychain via Keys screen (key: `plex_token`).
- Servarr: get API key from each app's Settings → General → API Key. Store secrets in Keychain or config file.
- Home Assistant: long-lived access token from Profile → Security → Long-Lived Access Tokens. Store in Keychain (key: `homeassistant_token`).
- Loki: Bearer token if behind an auth proxy. Store in Keychain (key: `loki_token`).
- Hermes: `[hermes]` section — `url`, `api_key` (Keychain: `hermes_api_key`), `salt_target` (minion where Hermes + signal-cli run), `signal_phone` (+E.164 number for Signal account). Signal CLI check uses Salt `cmd.run signal-cli` on `salt_target`.

## Testing

Tests live alongside the code they test (`_test.go` files). Run with `go test ./...`.

Key test files:
- `api/loki_test.go` — LokiClient HTTP tests including status-body error handling
- `api/homeassistant_test.go` — HAClient HTTP tests (httptest server)
- `api/plex_test.go` — PlexClient and ServarrClient HTTP tests (httptest server)
- `api/scan_test.go` — port scanner tests
- `db/db_test.go` — migration, CRUD, and settings tests
- `api/client_test.go` — Proxmox ticket + token auth tests (httptest server)
- `config/config_test.go` — config load, env override, ApplyGlobals, SaveProxmoxSetup tests (isolated from the real config file via `RATATASKR_CONFIG` in `TestMain`)
- `ui/services_test.go` — serviceSlug, lazyLoadTab guard tests
- `ui/setup_test.go` — first-run setup model tests
- `ui/detail_test.go` — snapDeleting/acting flag independence tests
- `cmd/mcp-server/main_test.go` — helper functions and tool catalogue tests
