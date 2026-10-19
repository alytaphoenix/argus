# Security and hardening

argus is an administration tool. It can hold API tokens and passwords, power
off VMs, reboot Proxmox nodes, apply Salt states, and run arbitrary commands on
minions through runbooks. Treat it as privileged software.

See [SECURITY.md](../SECURITY.md) for reporting vulnerabilities and the
supported-version policy.

## Credential storage

| Location | Contents | Protection |
| --- | --- | --- |
| OS keychain | Proxmox/Salt/Nomad/Wazuh/CrowdSec/Matchbox/Technitium/Prometheus/TrueNAS/Plex/HA/Loki/Hermes secrets, per-node SSH passwords | OS keychain ACLs |
| `~/.config/argus/config.toml` or `/etc/argus/config.toml` | URLs, usernames, non-keychain fallback secrets, Servarr API keys, token secret if `--token` was used | `0600` |
| SQLite database | Profiles, cloud-init passwords/SSH keys, provisioning metadata, cached service data | database and directory are `0700`/`0600` |
| Salt master (`/etc/argus/technitium.token`) | Technitium API token used by `services.wiring` | written `0600`, root-only |
| Salt master (`/srv/pillar/argus_services.sls`) | Traefik static config, DNS metadata, service targets | written `0600`; contains no tokens |
| `argus.log` | Operational errors | `0600` in a `0700` directory |

**Cloud-init caveat:** `cloud_init_profiles` stores the cloud-init password and
SSH public key in SQLite. The database is not encrypted. If you back it up,
encrypt the backup or delete the profile secrets first. A future release may
move these to the keychain.

## Proxmox API tokens

Prefer a token over a root password:

```bash
pveum user add argus@pve --comment "argus automation"
pveum user token add argus@pve argus --privsep 0
pveum acl modify / --users argus@pve --roles PVEAdmin
```

- `PVEAdmin` is broad but excludes permission management.
- Use `PVEAuditor` for read-only dashboards.
- Do not use `root@pam` for routine operation if a scoped user is practical.
- Revoke unused tokens with `pveum user token remove <user> <token>`.
- The installer's `--token` mode creates `root@pam!argus` with `privsep=0`
  because it is the least surprising zero-config option. Replace it for
  production use.

## TLS

TLS verification is the default. `insecure = true` (or the per-integration
equivalent) disables certificate checking and should be treated as a
last-resort workaround.

- Install the service CA in the OS trust store where possible.
- For step-ca, set `fingerprint` to the CA root's SHA-256. argus fetches the
  root, verifies the fingerprint, and uses it as the trust anchor for subsequent
  requests.
- Wazuh verification is on by default; opt out explicitly if it uses a
  self-signed cert.
- Health checks intentionally accept self-signed certificates so they can
  report certificate metadata; they do not send credentials.

## Metrics endpoint

`argus-metrics-server` and the embedded exporter serve `/metrics`,
`/scrape-config`, and `/healthz` over HTTP with **no authentication**. The
default bind is `127.0.0.1:9099`.

- Keep it on loopback when Prometheus runs on the same host.
- For remote scraping, use a firewall allowlist or an authenticated reverse
  proxy; do not expose the raw port to the internet.
- `/scrape-config` contains internal hostnames and placeholder credentials
  only; review it before sharing.

## Salt master and formula registry

The bootstrap installer runs Salt 3008.x from an isolated venv
(`/opt/argus-salt/venv`), with systemd units `argus-salt-master`,
`argus-salt-api`, and `argus-salt-minion`.

- `salt-api` uses HTTPS with a certificate signed by the Proxmox CA and a
  dedicated PAM user (`saltapi`), not the root password.
- `netapi_enable_clients` is limited to `local`, `local_async`, `wheel`, and
  `runner` — the clients argus needs.
- External auth grants `saltapi` `.*`, `@wheel`, and `@runner`; this is a
  privileged automation account. Protect the password in
  `/etc/argus/salt-api-password` (0600) and the keychain copy.
- The Salt API (`8000/tcp`) and minion ports (`4505/4506/tcp`) must be reachable
  only from trusted management/minion networks. The reference firewall allows
  the LAN and drops outside traffic.
- Formula application is additive and tracked in SQLite. There is no automatic
  rollback; review the plan and keep backups.

## MCP server and runbooks

`argus-mcp-server` is a stdio server. Any local process that can launch it and
speak MCP can invoke its tools, including VM power operations and Salt state
applications. There is no authentication layer because stdio is the trust
boundary.

Runbooks with `shell` steps run arbitrary commands on Salt minions via
`cmd.run`. They are powerful automation, not a sandbox. Review runbooks before
running them and restrict edit access to the database.

## SSH sessions

The interactive SSH action:

- passes passwords through `SSHPASS` (`sshpass -e`) instead of argv, so the
  password does not appear in `ps`;
- uses `StrictHostKeyChecking=accept-new`, so a changed key for a known host is
  rejected while first contact is accepted.

Prefer SSH keys and an agent over password auth. Per-node passwords are stored
in the keychain.

## Terminal output

Remote data (guest names, logs, alerts, library titles, DNS records, etc.) is
sanitized before rendering: C0/C1 control characters, ANSI escapes, and Unicode
bidi overrides are removed. This prevents terminal escape injection and
clipboard/cursor manipulation from a compromised managed service.

## Dependency and build hygiene

- The `go.mod` toolchain pins a patched Go release.
- Run `govulncheck ./...` before release.
- CI runs tests, vet, formatting, staticcheck, and govulncheck.
- Release binaries are built with `CGO_ENABLED=0`, `-trimpath`, and stripped
  symbols.

## Proxmox host baseline

The deployer does not change the Proxmox host firewall, SSH configuration, or
network. A reasonable baseline for a single-node homelab:

- Enable the Proxmox firewall (Datacenter → Firewall → Options) **only after**
  you have console/IPMI access; a mistake can lock you out.
- Allow `8006/tcp` (web UI/API) and `22/tcp` (SSH) only from your admin
  VLAN/workstation.
- Allow `9099/tcp` only from the Prometheus host, or keep it on loopback.
- Prefer SSH keys; disable password login for root once keys work.
- Keep Proxmox and the `pve-firewall` / `proxmox-firewall` services updated.
- Review `pveum user list`, `pveum user token list`, and `pveum acl list`
  periodically; remove unused tokens and over-broad ACLs.
- Back up `/etc/pve`, `/etc/argus`, and `/var/lib/argus` securely.

The bundled installer intentionally does **not** enable the firewall because
doing so remotely can cut off access. Apply host firewall rules from the
console or with an out-of-band path. The reference deployment in the
[October 2026 review](security-review-2026-10.md) has the firewall enabled
with an explicit LAN allow rule and `policy_in = DROP`.

## Hardening checklist

- [ ] Replace the installer's root token with a scoped user/token.
- [ ] Store secrets in the OS keychain; keep config at `0600`.
- [ ] Encrypt or restrict backups of `~/.config/argus/` and `/var/lib/argus/`.
- [ ] Leave `insecure = false` unless a self-signed service is unavoidable.
- [ ] Keep the metrics listener on loopback or behind an authenticated proxy.
- [ ] Review runbooks and MCP client permissions.
- [ ] Run `govulncheck` and update the toolchain regularly.
- [ ] Rotate tokens/passwords after sharing logs, screenshots, or databases.
