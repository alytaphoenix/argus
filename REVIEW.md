# Codebase Review — 2026-07-05

Full review of all packages. Build, vet, and tests passed at time of review.
**All items below were fixed in the same session** (build, vet, and full test
suite pass). This file is the record of findings and resolutions.

Resolution summary:
- #1 keyring order → `Config.Validate()` runs after overlay in both entrypoints;
  `keyring.Overlay` helper replaces the duplicated blocks.
- #2 alert routes → `AlertRoutesModel` constructed in `NewApp`.
- #3 maintenance safety → `runMaintenance` polls Salt jobs / evacuation / node
  reboot to completion, skips a node's remaining steps after a failure, and
  marks the run `failed` when any step fails; reboot wait is a setting.
- #4 scheduler → once-a-minute `schedulerTickCmd` fires due schedules;
  `DueMaintenanceSchedules` + `AdvanceMaintenanceSchedule` prevent double-fire.
  (Lifecycle policies remain manual-run by design, but now refuse empty tags.)
- #5 lifecycle → empty tag rejected at save and run; `snapshot` action now
  actually creates a Proxmox snapshot; labels/help say "uptime", not "idle".
- #6 provision → CPU/memory applied via `ResizeVMConfig`; clone task awaited
  before config in both the TUI and MCP `proxmox_clone_vm`.
- #7 cert migration → gated by `PRAGMA user_version` inside a transaction.
- #8 sshkeys → full `url.QueryEscape` (spaces→%20).
- #9 metrics → HELP/TYPE emitted once per family.
- #10 salt token refresh; #11 Proxmox client timeout + ctx-aware WaitForTask;
  #12 unit-name/path validation in Salt helpers; #13 scan timeout + extra ports
  wired; #14 Technitium MX delete; #15 partial service-host merge; #16 wiring
  token via env + cert renewal via `needs-renewal`; #17 CLAUDE.md updated;
  #18 MCP `isError`; #19 plex token escaping; #20 Wazuh TLS toggle; #21 Claude
  request path deduped + Prometheus `io.ReadAll`; #22 runbook shell steps
  implemented (`RunCmd`) and runs fail on step error; #23 `UpdateVMStatus`
  scoped to newest row; #24 stepca health status check; #25 keyring overlay
  helper shared.

---


## High severity

1. **Keychain-only Proxmox secret can never work.** `config.Load()` errors with
   "PROXMOX_TOKEN_ID and PROXMOX_SECRET required" (`config/config.go`) before the
   Keychain overlay in `main.go` / `cmd/mcp-server/main.go` runs. The
   `proxmox_secret` keyring key is dead code.
   Fix: validate host/token/secret after the overlay (`Config.Validate()`).

2. **Alert routes never fire until the Alerts tab is visited.** `App.Update` calls
   `a.alertRoutes.FireMatchingRoutes(...)` on new CrowdSec/Wazuh events, but
   `a.alertRoutes` is a zero-value model until `switchSection` constructs it.
   Fix: construct the model in `NewApp`.

3. **Maintenance runs unsafe around reboots** (`ui/maintenance.go` executeRunCmd):
   - Salt `os_update` dispatched `local_async`, marked done as soon as a JID
     returns — the reboot step can reboot mid-`apt upgrade`.
   - `evacuate` sleeps a flat 10 s; migrations take minutes.
   - `reboot` sleeps 30 s and proceeds without verifying the node came back.
   - Run always marked `done` even when steps failed.
   - Documented `maintenance_reboot_wait_s` setting not seeded or read.

4. **Scheduled maintenance never executes.** Nothing polls
   `maintenance_schedules` for due runs; only the manual `r` key works.

5. **Lifecycle "idle" is actually uptime** (`ui/lifecycle.go`): stops any running
   VM whose *uptime* exceeds the threshold; an empty tag matches **all** VMs.
   `snapshot` action is a no-op that still increments the applied counter.

6. **Provision form silently ignores CPU and Memory** (`ui/provision.go`,
   `_ = cpu; _ = memory`). Also `SetCloudInit` is called while Proxmox still
   holds the clone lock (no `WaitForTask`); same in MCP `proxmox_clone_vm`.

7. **"v16" certificates migration reruns on every startup** (`db/db.go`). No
   schema-version tracking; every launch copies `certificates` into
   `certificates_new`, drops, renames — not in a transaction, so a crash between
   DROP and RENAME loses all certificate history.

8. **Cloud-init `sshkeys` encoding incomplete** (`api/cloudinit.go`): only
   `\n`→`%0a`; Proxmox requires full percent-encoding (keys contain `+ / =` and
   spaces).

9. **Invalid Prometheus exposition output** (`main.go` writeMetricsText): emits
   `# HELP`/`# TYPE` per sample, so looped metrics produce duplicate TYPE lines
   and Prometheus rejects the scrape.

## Medium

10. **Salt token never refreshed** (`api/salt.go`): login once in constructor;
    salt-api tokens expire (~12 h). Long-lived TUI/MCP sessions get 401s.
11. **Proxmox HTTP client has no timeout** (`api/client.go`); `WaitForTask`
    sleep not context-aware.
12. **Shell quoting in Salt helpers**: `JournalLogs` concatenates `unit`
    unquoted into `cmd.run`; `WriteFile` doesn't handle `'` in paths.
13. **Dead settings**: `scan.timeout_ms` / `scan.extra_ports` never read
    (PortScan hardcodes 600 ms and CommonPorts).
14. **Technitium `DeleteRecord` can't delete MX records** (no MX case → no
    `exchange` param).
15. **`SaveServiceHost` full-row upsert used for partial updates**:
    `markProvisionPendingCmd` clobbers vmid/node/ports/notes.
16. **Salt wiring template leaks the Technitium token** into `cmd.run` command
    lines; cert step's `--force` + `creates:` guard means certs are never
    renewed after first issue.
17. **CLAUDE.md drift**: migrations "v1–v10" wrong; app_settings key names wrong
    (`wazuh_critical_level` vs `wazuh.critical_level`, `scan_timeout_s` vs
    `scan.timeout_ms`); Runbooks/Alerts/Lifecycle screens missing from docs.

## Low / polish

18. MCP `nomad_job_allocations` does `a.ID[:8]` — panics on short IDs; tool
    errors returned as text rather than `isError: true`.
19. Plex token appended to URLs without escaping (`api/plex.go`).
20. Wazuh client hardcodes `InsecureSkipVerify` with no config toggle.
21. `api/claude.go` two near-identical request implementations;
    `api/prometheus.go` hand-rolls `io.ReadAll`.
22. Runbooks: kinds `shell`/`http`/`nomad_restart` advertised but
    unimplemented; salt steps fire-and-forget; run always ends `done`.
23. `UpdateVMStatus` updates all rows for a vmid (multiple provision rows
    possible).
24. stepca `Health` doesn't check HTTP status before decoding.
25. `main.go` has ~60 lines of repeated keyring-overlay blocks (MCP server has a
    compact helper).

## In good shape

Consistent sub-model / per-tab handler patterns, disciplined nil-handling for
optional integrations, health-check/TLS-capture code, Wazuh token refresh,
Nomad state-diff sync (proper transaction), useful test coverage.
