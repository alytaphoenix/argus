# Security and hardening review — October 2026

Scope: the argus codebase, the Proxmox host installer, and a read-only audit
of a deployed single-node Proxmox host.

## Resolved in this review

| Finding | Severity | Resolution |
| --- | --- | --- |
| SSH password exposed in `sshpass -p` argv | High | Use `sshpass -e` with `SSHPASS` in the environment (`ui/ssh.go`). |
| SSH host-key verification disabled (`StrictHostKeyChecking=no`) | High | Switched to `accept-new`; changed keys for known hosts are rejected. |
| step-ca fingerprint mode disabled TLS verification without actually pinning the root | High | Fetch the CA root, verify its SHA-256 against `fingerprint`, then use it as `RootCAs` for subsequent requests (`api/stepca.go`). |
| SQLite database file could be `0644` | Medium | Database and WAL/SHM files are forced to `0600` on open (`db/db.go`). |
| Metrics/log files created with broad modes | Low | Log directory/file are `0700`/`0600`; metrics listener defaults to loopback. |
| Metrics endpoint bound to all interfaces by default | Medium | Default is `127.0.0.1:9099`; remote scraping requires explicit config. |
| Wazuh TLS verification defaulted off | Medium | Verification is on by default; `insecure = true` is opt-in. |
| Plex token sent in URL query strings | Medium | Token is sent in the `X-Plex-Token` header (`api/plex.go`). |
| Remote text could inject terminal escapes | Medium | Control characters, ANSI escapes, and bidi overrides are stripped in shared render paths (`ui/styles.go` and callers). |
| Tab key could bypass confirmations / steal form focus | Medium | Forms/dialogs own Tab; confirmations ignore unrelated keys. |
| Nil database panics in Salt/TrueNAS paths | Medium | Guarded at the command boundary. |
| Toolchain had known standard-library vulnerabilities | High | `go.mod` pins Go 1.26.9; `golang.org/x/sys` updated; `govulncheck ./...` reports no vulnerabilities. |
| Hard-coded developer SSH username default | Low | Default is now the local username. |
| Agent-local `.claude/` state tracked in git | Low | Untracked and ignored for the public release. |

## Host audit (read-only)

The deployed host is a single Proxmox VE 9.2.3 node. Observations:

- Only `root@pam` exists; no ACL entries. The installer token
  `root@pam!argus` has `privsep=0` and is therefore root-equivalent.
- The Proxmox firewall is **disabled** (`/nodes/proxmox/firewall/options`
  `enable: 0`) and has no rules. The web UI/API (`8006`), SSH (`22`), and the
  metrics exporter (`9099`) are reachable on the LAN.
- `proxmox-firewall` and `pve-firewall` services are running, but the cluster
  firewall is not enforcing rules.
- The node uses the Proxmox-generated CA; the leaf certificate is valid until
  2028. The argus client was configured with `insecure = true` for the
  self-signed UI certificate.
- The deployed binaries predate this review (Go 1.26.3) and the metrics
  listener still uses the old `:9099` config. Rebuild/redeploy and update the
  listener to `127.0.0.1:9099` (or firewall it).

## Residual recommendations

1. Replace the root API token with a dedicated `argus@pve` user and a token
   scoped to the operations you use (start with `PVEAdmin`; use `PVEAuditor`
   for read-only).
2. Enable the Proxmox firewall from the console/IPMI after confirming access:
   allow `8006`/`22` from the admin network and `9099` only from Prometheus;
   deny the rest.
3. Change `insecure = true` to a trusted CA or pinned certificate where
   possible.
4. Protect or encrypt backups of `/etc/argus` and `/var/lib/argus`; the SQLite
   database contains cloud-init profile passwords.
5. Review runbooks and MCP client permissions; they are intentionally
   high-trust.
6. Redeploy the hardened binaries and restart `argus-metrics` when SSH access
   is available.

## Verification performed

```bash
make check          # go vet, tests, staticcheck, govulncheck
make test-race
govulncheck ./...
goreleaser check
goreleaser release --snapshot --clean
```

`govulncheck` reports no vulnerabilities for the module. The GoReleaser
snapshot build produced Linux, macOS, and Windows archives successfully.
