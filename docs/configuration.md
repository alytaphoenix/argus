# Configuration

argus reads TOML from:

```text
~/.config/argus/config.toml
```

Override the path with `ARGUS_CONFIG`. On first run, if the Proxmox connection
is missing or invalid, the TUI starts a setup screen instead of failing.

The interactive editor is available with:

```bash
argus config
```

`config.toml.example` in the repository is the full annotated reference.

## Secret handling

Prefer the operating-system keychain:

- macOS: Keychain
- Linux: Secret Service (GNOME Keyring / KWallet)
- Windows: Credential Manager

The Keys screen (`?` on the Proxmox screen, or the **Keys** tab) manages global
credentials and per-node SSH passwords. Config-file values are only fallbacks;
when a keychain entry exists, it wins.

The config file is written with mode `0600`. The SQLite database and log file
are also restricted to the owner. Cloud-init profile passwords and Servarr API
keys are stored in the database/config, so protect backups of
`~/.config/argus/` accordingly.

## Proxmox authentication

Use an API token where possible:

```toml
host     = "https://pve.example.com:8006"
token_id = "argus@pve!argus"
insecure = false

# Put the secret in the keychain (key: proxmox_secret), or provide it via
# the environment (PROXMOX_SECRET). Avoid committing it to this file.
```

Ticket auth is also supported:

```toml
host     = "https://pve.example.com:8006"
username = "argus@pve"
insecure = false
```

The password is read from the keychain (`proxmox_password`) or
`PROXMOX_PASSWORD`. Ticket auth takes priority when `username` is set.

Create a token on a Proxmox host:

```bash
pveum user add argus@pve --comment "argus automation"
pveum user token add argus@pve argus --privsep 0
pveum acl modify / --users argus@pve --roles PVEAdmin
```

Choose the smallest role that supports your workflows. `PVEAdmin` is broad but
does not include permission management. A root token is root-equivalent; see
[security](security.md#proxmox-api-tokens).

## Environment overrides

Every common setting has an environment override. The `ARGUS_*` names are
preferred; legacy `RATATASKR_*` names are still accepted.

| Setting | Environment |
| --- | --- |
| Proxmox host | `PROXMOX_HOST` |
| Proxmox token id / secret | `PROXMOX_TOKEN_ID` / `PROXMOX_SECRET` |
| Proxmox username / password | `PROXMOX_USERNAME` / `PROXMOX_PASSWORD` |
| Salt URL / user / password | `SALT_URL` / `SALT_USERNAME` / `SALT_PASSWORD` |
| Nomad URL / token | `NOMAD_URL` / `NOMAD_TOKEN` |
| Prometheus URL / token | `PROMETHEUS_URL` / `PROMETHEUS_TOKEN` |
| Metrics listener | `ARGUS_METRICS_LISTEN` |
| Technitium URL / token | `TECHNITIUM_URL` / `TECHNITIUM_TOKEN` |
| step-ca URL | `STEPCA_URL` |
| Matchbox URL / token | `MATCHBOX_URL` / `MATCHBOX_TOKEN` |
| CrowdSec URL / API key | `CROWDSEC_URL` / `CROWDSEC_API_KEY` |
| Wazuh URL / user / password | `WAZUH_URL` / `WAZUH_USERNAME` / `WAZUH_PASSWORD` |
| TrueNAS URL / API key | `TRUENAS_URL` / `TRUENAS_API_KEY` |
| Anthropic API key | `ANTHROPIC_API_KEY` |
| SSH user / key | `ARGUS_SSH_USER` / `ARGUS_SSH_KEY` |
| Homelab domain / timezone | `ARGUS_DOMAIN` / `ARGUS_TIMEZONE` |

## Sections

Only Proxmox is required. Omit any other section to disable that integration.

| Section | What it configures |
| --- | --- |
| `[globals]` | SSH user/key, domain, timezone, admin email. |
| `[defaults]` | Cloud-init VM defaults (template, CPU, memory, disk, storage, bridge, CI user/key/IP, nameserver, search domain, backup storage). |
| `[salt]` | salt-api URL, credentials, eauth, master minion, pillar/state roots. Requires `salt-api` with CherryPy. |
| `[nomad]` | Nomad URL and optional ACL token. |
| `[prometheus]` | Prometheus query URL/token and argus's own metrics listener. |
| `[loki]` | Loki URL and optional bearer token. |
| `[truenas]` | TrueNAS REST URL, API key, TLS mode. |
| `[technitium]` | Technitium DNS URL and API token. |
| `[stepca]` | step-ca URL and optional SHA-256 root fingerprint. |
| `[matchbox]` | Matchbox PXE URL and optional token. |
| `[crowdsec]` | CrowdSec Local API URL and key. |
| `[wazuh]` | Wazuh REST URL, credentials, TLS mode. |
| `[plex]` | Plex URL, token, optional VMID/node for status badges. |
| `[servarr.*]` | Sonarr/Radarr/Lidarr/Readarr/Prowlarr/Bazarr/Overseerr/Tautulli URL, API key, VMID/node. |
| `[homeassistant]` | Home Assistant URL and long-lived token. |
| `[hermes]` | Hermes agent URL/API key, Salt target, Signal phone. |
| `[claude]` | Anthropic model; API key via keychain or `ANTHROPIC_API_KEY`. |

## TLS

`insecure = true` disables certificate verification. It is intended only for a
specific self-signed service on a trusted network. Prefer installing the
service's CA into the system trust store. For step-ca, set `fingerprint` to the
CA root's SHA-256; argus verifies and pins it before using TLS.

Wazuh verifies TLS by default; set `insecure = true` only if it uses a
self-signed certificate that you cannot trust another way.

## Metrics listener

```toml
[prometheus]
listen = "127.0.0.1:9099"
```

The metrics endpoint has no authentication. Keep it on loopback, or expose it
only through a firewall/authenticated reverse proxy. Use `0.0.0.0:9099` only
when you understand that trade-off.

## MCP server

Build and register the stdio server:

```bash
go build -o argus-mcp-server ./cmd/mcp-server/
claude mcp add argus -- /path/to/argus-mcp-server
```

The MCP server uses the same config and keychain as the TUI. Treat any MCP
client as a trusted administrator: tools can power-cycle VMs and apply Salt
states.

## First-run and upgrades

With no working Proxmox configuration, `argus` walks through a setup screen,
tests the connection, writes a minimal config, and stores the password in the
keychain. On success it opens the Bootstrap tab.

Existing `ratataskr` installs are migrated automatically: config and database
are copied from `~/.config/ratataskr/`, and keychain lookups fall back to the
legacy `ratataskr` service before copying values forward.
