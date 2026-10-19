# Architecture

argus is a Go application with four entry points:

| Binary | Purpose |
| --- | --- |
| `argus` | The interactive Bubble Tea TUI. |
| `argus-metrics-server` | Background Prometheus exporter for the local SQLite database. |
| `argus-mcp-server` | JSON-RPC 2.0 / MCP stdio server exposing integrations as tools. |
| `argus` with `config` | Interactive config-file editor. |

The TUI is the primary interface. All integrations are optional except
Proxmox; an unconfigured integration disables its screen or tool rather than
crashing the application.

## Packages

```text
main.go              TUI entry point, embedded metrics server, first-run setup
cmd/metrics-server   Standalone Prometheus exporter
cmd/mcp-server       MCP stdio server
api/                 HTTP/Salt/Proxmox/... clients, one file per integration
bootstrap/           Salt installer, platform planner, formula materialization
formula/             Opinionated Salt formula registry and dependency resolver
config/              TOML + environment loading, validation, config editor I/O
db/                  SQLite schema, migrations, and query helpers
keyring/             OS keychain wrapper (macOS Keychain, Secret Service, Windows)
ui/                  Bubble Tea screens and shared styles/helpers
scripts/             Idempotent Proxmox host installer
docs/                User and contributor documentation
```

## TUI structure

`ui/app.go` owns the root model and every sub-model. Each screen has its own
`Update`/`View` pair and returns its concrete model type so the root can
reassign it without type assertions.

Navigation:

- `tab` / `shift+tab` cycles top-level sections.
- `enter` opens the selected node → guests → guest detail.
- `b` / `esc` goes back one level.
- `H` opens the help overlay.
- `q` / `ctrl+c` quits (except inside text forms that consume the key).

Top-level sections:

1. Proxmox nodes and guests
2. Salt minions (optional)
3. Orchestrator: keys, pending VMs, cluster pillar, Bootstrap
4. Bootstrap: platform readiness + Salt formula registry
5. Nomad jobs (optional)
6. Profiles: cloud-init and Salt profiles
7. Metrics: Prometheus, local history, Loki
8. Services: health checks, DNS, certificates, TrueNAS, Home Assistant
9. PXE / Matchbox
10. Security: CrowdSec, Wazuh, port scan
11. Maintenance: cluster updates and reboots
12. Plex / Servarr
13. Runbooks
14. Alert routes
15. Lifecycle policies
16. Settings
17. Keys / credentials

Some screens are hidden when their integration is not configured (Salt,
Nomad), while Orchestrator and Bootstrap remain available because Bootstrap is
the guided setup path for those services.

## Data flow

- **Proxmox** is the source of truth for nodes, guests, task status, snapshots,
  storage, and HA resources. Auth is either a PVE API token
  (`PVEAPIToken` header) or ticket auth (username/password → cookie + CSRF
  token, refreshed before the two-hour expiry).
- **Salt** uses the salt-api session login, caches the auth token, and refreshes
  it near expiry. Key management uses the wheel client; state application uses
  `local_async` and polls the job result.
- **Bootstrap** checks the configured services, inventories Proxmox guests,
  and only proposes provisioning when a service is missing. Existing guests
  are treated as unmanaged and are never reprovisioned automatically.
- **SQLite** stores profiles, provisioning history, service registry entries,
  health checks, metrics history, runbooks, maintenance schedules, cluster
  pillar entries, platform profiles, formula installations, and bootstrap
  runs. WAL mode is enabled; migrations are idempotent and run at open.
- **Bootstrap** plans from live Proxmox/Salt/SSH state, installs Salt in an
  isolated venv, materializes the formula registry under `/srv/salt/argus`,
  applies states through salt-api, and records each run.
- **Keychain** stores secrets that should not be written to disk. Config file
  values are fallbacks; keychain values win when present.

## API clients

Each `api/*.go` client is independently testable with `httptest` and honors a
per-integration `insecure` flag where self-signed TLS is common. Notable
behavior:

- `api/client.go`: Proxmox token and ticket auth, task polling, timeouts.
- `api/salt.go`: token refresh, key wheel operations, validated unit names and
  file paths, async job polling.
- `api/prometheus.go` / `api/loki.go`: query clients with optional bearer auth.
- `api/plex.go`: Plex and Servarr clients; tokens are sent in headers.
- `api/stepca.go`: pinned step-ca CA root and health/cert helpers.
- `api/scan.go`: bounded concurrent TCP port scanning.
- `api/healthcheck.go`: HTTP/TCP health checks with certificate capture.

## MCP server

`cmd/mcp-server` speaks JSON-RPC 2.0 over stdio. Tools are only advertised when
the corresponding integration is configured. It is intended for local agents
(Claude, Hermes, etc.), not as a network service. See
[configuration](configuration.md#mcp-server) for registration.

## Metrics

`argus-metrics-server` (and the embedded server) reads the local SQLite
database and exposes Prometheus text metrics at `/metrics`, a health endpoint
at `/healthz`, and a generated scrape snippet at `/scrape-config`. The default
listener is loopback-only and the endpoint has no authentication; see
[security](security.md#metrics-endpoint).
