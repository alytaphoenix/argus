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

## Host audit and remediation

The deployed host is a single Proxmox VE 9.2.3 node. The following items were
found and remediated:

| Before | After |
| --- | --- |
| Only `root@pam` existed; the installer token `root@pam!argus` was root-equivalent. | Dedicated `argus@pve` user with `PVEAdmin` at `/`; `argus@pve!argus` token; the `root@pam!argus` token was revoked. |
| Proxmox firewall disabled, no rules; `8006`/`22`/`9099` reachable on the LAN. | Cluster and node firewall enabled; `policy_in = DROP`; explicit ACCEPT for `192.168.0.0/24`, `127.0.0.1`, and `::1`; outside traffic is dropped in `pve-firewall simulate`. |
| Metrics exporter listened on `*:9099`. | Listener is `127.0.0.1:9099`; not reachable from the LAN. |
| Deployed binaries were built with Go 1.26.3. | Redeployed `0.1.1` binaries built with Go 1.26.9. |
| Proxmox client used `insecure = true`. | PVE CA installed in the host trust store and the macOS login keychain; `insecure = false` on both; API/TUI verified over verified TLS. |
| Stale/rejected `proxmox_password` remained in the macOS keychain. | Removed; only the scoped token secret remains. |
| No Salt automation layer. | Salt 3008.3 runs from an isolated venv; `salt-api` uses a PVE-CA cert and dedicated `saltapi` PAM user; `netapi_enable_clients` is limited to `local`, `local_async`, `wheel`, `runner`; local minion key accepted. |
| `config.toml` backups could contain the old root token secret. | Backups removed; current config is mode `0600`. |

VM firewall flags are unset for VMs `100`, `101`, and `103`; all three remained
running and the host API/SSH stayed reachable after enabling the firewall.

## Residual recommendations

1. Consider tightening the LAN allow rule to the admin workstation/VLAN and
   Prometheus host once those addresses are stable.
2. Protect or encrypt backups of `/etc/argus` and `/var/lib/argus`; the SQLite
   database contains cloud-init profile passwords.
3. Review runbooks and MCP client permissions; they are intentionally
   high-trust.
4. Prefer SSH key-only root login; the firewall and key auth should be tested
   from an out-of-band console before disabling password auth.
5. Keep Proxmox, the firewall packages, and the Go toolchain patched.

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
