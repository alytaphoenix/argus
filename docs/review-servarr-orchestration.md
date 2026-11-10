# Orchestrator and DCIM review: the phoenix-servarr case

Date: 2026-11-10

## Scope

Requested outcome:

1. Delete the incomplete `phoenix-servarr` guest (VM 101).
2. Define a node that runs Prowlarr, Lidarr, Radarr, Sonarr, and Whisparr for
   `phoenixplex` from the Servarr wiki stack.
3. If argus cannot express or install that stack, add the missing capability.
4. Review argus as an orchestrator and DCIM against this workflow.

Evidence used: live Proxmox API inventory, the local argus SQLite database,
argus source, and the commands below. No Servarr containers were started during
this review.

## What VM 101 actually was

| Property | Value |
| --- | --- |
| Guest | VM 101, `qemu`, `phoenix-servarr` |
| State | `stopped`; no snapshots except `current` |
| Resources | 2 cores, 2048 MiB, 32 GiB on `local-lvm` |
| Boot | `scsi0;ide2;net0` |
| Installer media | `ubuntu-25.10-live-server-amd64.iso` still attached to `ide2` |
| Cloud-init | No cloud-init drive; no guest agent network data |
| argus DB | No `provisioned_vms` row, no service registration |

This is an abandoned Ubuntu installer boot, not a configured Servarr host.
Deleting it loses no application configuration or media.

## Before: what argus could and could not do

| Capability | Before | Consequence |
| --- | --- | --- |
| Stop/start/reboot guests | Yes | Fine |
| Destroy a guest | No API method or UI action | VM 101 could not be removed from argus |
| Provision from a template | Yes, required a template VMID | No template exists in this cluster |
| Provision from a cloud image | No | Rebuilding the node required manual Proxmox work |
| Install a service stack | Formula registry only had the platform baseline | Servarr was an integration entry, not an installable formula |
| Target a non-master minion | `Apply` always used the master minion | Any service installed by argus landed on the Proxmox host |
| Define install paths | Hard-coded in Go SLS constants | No way to express `/opt/...`, `/mnt/media`, PUID/PGID |
| Whisparr | Absent from config, checks, and media catalog | Could not be represented at all |
| Review a change before apply | Formula description only | No rendered Compose/SLS preview |

The bootstrap service named "Servarr suite" only health-checked URLs already in
`[servarr.*]`; it never installed anything.

## What was added

- **Declarative stack format** in `stack/`, loaded from
  `~/.config/argus/stacks/*.toml`:
  - target Salt minion, Proxmox node/VMID metadata
  - `install_root`, `media_root`, PUID/PGID, timezone
  - `[[service]]` blocks with image, port, config path, env, volumes, health path
  - strict TOML decoding; unknown keys, relative roots, `..`, duplicate IDs, and
    duplicate ports are rejected
  - deterministic Compose and Salt SLS renderers
- **Built-in `servarr` stack** with Prowlarr (9696), Sonarr (8989), Radarr
  (7878), Lidarr (8686), and Whisparr (6969), all sharing `media_root:/data`.
- **Stack-aware formula registry**: a stack becomes a normal installable
  formula carrying rendered files; `Manager.Apply` sends its state to
  `target_minion` instead of the master, and records the actual target.
- **`argus stack` CLI**: `list`, `show` (`--compose`, `--sls`, `--toml`), `init`,
  and `apply` (`--dry-run`, `--target`, `--yes`).
- **`argus vm` CLI and TUI destroy**: list guests; destroy only a stopped guest,
  with `--yes` plus an optional `--name` identity check; wait for the Proxmox
  task; remove the local provisioning row.
- **Whisparr** in config, config editor, orchestration health checks, media
  service catalog, and `config.toml.example`.
- **Service registration after stack apply** so published endpoints appear in
  Services for health checks and DNS/Traefik wiring.

## Usability review

### What works well after the change

- The destructive path is explicit: stopped-only, `--yes`, optional name match,
  task wait, and a confirmation banner in the TUI. This is the right shape for
  a DCIM operation.
- Stack files are reviewable artifacts. `argus stack show servarr --compose`
  answers "what will Docker receive?" without SSH.
- Install paths are no longer hidden in Go source. PUID/PGID and the shared
  `/data` mount are visible and editable.
- Applying twice is safe: the Compose file is managed, and a `cmd.wait` state
  restarts the project only when the file changes.

### Friction and risks

| Priority | Finding | Why it matters | Recommendation |
| --- | --- | --- | --- |
| ~~P0~~ resolved | No VM template or cloud image in this cluster | The stack targeted a minion that did not exist after VM 101 was deleted | Resolved: the `[provision]` block imports the Ubuntu cloud image, and VM 101 was rebuilt, joined to Salt, and applied (see "Verification performed") |
| P0 | Stack dependencies are applied on the master | `docker` is applied to the master, then again on the target via the included SLS; harmless here, wrong for a strict per-minion plan | Add per-minion state planning and only apply dependencies where needed |
| P1 | No storage preparation | `/mnt/media` may be created empty by Docker, yielding empty libraries instead of a mount error | Add a stack `mounts`/NFS section and fail when `media_root` is not a mountpoint |
| P1 | API keys are manual | Every app needs a key copied into `[servarr.*]` before argus health checks and Plex wiring work | Add a post-apply step that reads each app's `/config/config.xml` over Salt and stores the key in the DB/keychain |
| ~~P1~~ resolved | No provisioning from the stack definition | The TOML named `node`/`vmid` but could not create the VM | Resolved by the optional `[provision]` block (template or cloud image, storage, bridge, CPU/RAM/disk, `ipconfig`, `ci_user`); it also accepts the minion's Salt key |
| P0 | The `[provision]` minion installer fetches a retired URL | `saltMinionScript` runs `curl -fsSL https://bootstrap.saltproject.io \| sudo sh`; that host now serves a 1883-byte HTML "Salt Bootstrap Script Migration Warning" for every user agent, so the shell receives HTML and the minion is never installed | Fetch `https://github.com/saltstack/salt-bootstrap/releases/latest/download/bootstrap-salt.sh` instead, or install Salt from the Broadcom package repo / a pinned venv the way the master is installed |
| P1 | Stack services are registered with a hostname this workstation cannot resolve | `registerStackServices` writes `<target>.<globals.domain>` (e.g. `phoenix-servarr.phoenix.arpa`); that name is only served by Technitium at 192.168.0.42, which the workstation resolver does not query, so links and health checks fail while `http://192.168.0.101:<port>` works | Register the minion's address, or resolve the stack host through Technitium, or add a split-DNS resolver for `phoenix.arpa` |
| P2 | One unknown stack file disables every stack command | A file with keys the running binary does not know aborts the whole directory: `argus stack: ~/.config/argus/stacks/hermes.toml: unknown key(s): provision, …` | Report the bad file and continue with the rest, or gate it behind `stack show --strict` |
| P2 | `stack apply` requires SSH to the Proxmox host even when Salt is already configured | Remote operators or hosts without SSH key access cannot apply | Use salt-api file writing or a Salt orchestration state when SSH is unavailable |
| P2 | No drift/plan diff for stacks | `--dry-run` shows platform readiness, not changed container fields | Render and diff the current SLS/Compose before apply; show a per-service diff |
| P2 | No rollback | A bad stack apply has no previous definition retained | Keep the last rendered stack bundle and add `stack rollback` |
| P2 | `stack list` sources are implicit | A user file silently overrides a built-in | Show `built-in` vs file path in the list, and warn on override |
| P3 | TUI has no stack editor | CLI is required to edit paths | Add a Stacks screen or an `e` action that opens `$EDITOR` on the stack file |
| P3 | Destroy is list-only in the TUI | Operators naturally open a guest detail first | Add the same `d` action to the detail screen |

### DCIM correctness notes

- The destroy API deletes only stopped guests. It does not force-stop, which is
  correct: a force-stop policy needs an explicit, separate confirmation.
- `purge=1` removes backup/replication/HA references. That is the safer default
  for an abandoned VM, but it should be surfaced in the TUI banner as "and
  backup/HA references".
- The TUI captures the target guest when the confirmation is armed, so a
  refresh cannot retarget the delete to a different cursor row.
- Stack path validation is intentionally strict. A stack cannot write outside
  `install_root`/`media_root` through `..` or a relative config path.
- Secrets are not rendered into Compose files. All app secrets remain in the
  app's own config volume; argus should read them only after the container is
  healthy, not bake them into the SLS.

## Operating sequence for phoenixplex

```bash
# 1. Delete the abandoned installer guest (stopped, identity-checked).
argus vm delete --vmid 101 --node proxmox --name phoenix-servarr --yes

# 2. Create an editable copy of the built-in stack.
argus stack init servarr --target phoenix-servarr
$EDITOR ~/.config/argus/stacks/servarr.toml   # install_root, media_root, PUID/PGID

# 3. Review before touching the target.
argus stack show servarr --compose
argus stack show servarr --sls
argus stack apply servarr --dry-run

# 4. Build/start the target VM, mount media, and accept its Salt key.
#    Then install.
argus stack apply servarr --yes

# 5. Add generated API keys.
argus config
```

Step 4 is complete. VM 101 was rebuilt from the Ubuntu 24.04 cloud image
(`qm importdisk` of `noble-server-cloudimg-amd64.img`, reused MAC and VMID),
joined to Salt as `phoenix-servarr`, given the NFS media mount, and applied.

Three host-level faults had to be fixed on the way, and each is worth encoding
in `[provision]` rather than rediscovering:

1. `net0` must use `firewall=0` (as the `[provision]` script now does). With
   `firewall=1` and the datacenter's `policy_in: DROP`, the guest answered ARP
   for its address but every IP packet was dropped before it reached the tap,
   which looks exactly like a DHCP or boot failure.
2. `ci_user` must not be `admin`: the cloud image ships group `admin`
   (gid 110), so `useradd admin` exits 9 and cloud-init aborts the whole
   `users_groups` module, leaving no SSH key and no login.
3. Salt must be installed without `bootstrap.saltproject.io`, which is retired.

Media is mounted with `_netdev,nfsvers=4.2` from
`192.168.0.240:/mnt/PhoenixNest/Media`; the export is world-writable and a
write as uid 1000 succeeded, so the stack's `puid`/`pgid` 1000 need no change
even though existing files are owned by 3000/3001.

## Verification performed

```bash
gofmt -w <changed files>
go test ./stack ./formula ./api ./bootstrap ./ui ./config ./db
go build -o /tmp/argus-test .
/tmp/argus-test stack list
/tmp/argus-test stack show servarr
/tmp/argus-test vm list --node proxmox
```

After VM 101 was rebuilt and its Salt key accepted, the same commands reported
no target warning:

```text
Stack servarr → minion phoenix-servarr (5 service(s), install root /opt/argus/servarr)
  [ok   ] Proxmox inventory: 1 node(s), 4 guest(s) discovered
  [ok   ] Host access: SSH reachable as root
  [ok   ] Salt runtime: Salt 3008.x runtime installed in /opt/argus-salt
  [ok   ] Salt API: reachable, 2 accepted minion(s)
  [ok   ] Formula state tree: /srv/salt/argus present
  [ok   ] Stack servarr: target phoenix-servarr
```

VM 101 was deleted through argus after saving its config:

```text
Deleting qemu 101 "phoenix-servarr" on proxmox (purge=true)
Deleted VM 101 "phoenix-servarr"
```

`argus vm list` no longer shows VM 101, and `local-lvm` has no `vm-101-disk-0`
volume. Unit tests cover the delete request path and purge query, stack
validation, deterministic rendering, built-in override behavior, and formula
conflict rejection.

`argus stack apply servarr --yes` then applied `argus.servarr` on
`phoenix-servarr`, and every service answered `/ping` with `{"status": "OK"}`:

```text
prowlarr  lscr.io/linuxserver/prowlarr:latest  Up  0.0.0.0:9696->9696/tcp
sonarr    lscr.io/linuxserver/sonarr:latest    Up  0.0.0.0:8989->8989/tcp
radarr    lscr.io/linuxserver/radarr:latest    Up  0.0.0.0:7878->7878/tcp
lidarr    lscr.io/linuxserver/lidarr:latest    Up  0.0.0.0:8686->8686/tcp
whisparr  ghcr.io/hotio/whisparr:v3            Up  0.0.0.0:6969->6969/tcp
```

Each app's `config.xml` key was read over SSH and stored in `[servarr.*]`;
`/api/v1/system/status` and `/api/v3/system/status` returned 200 for
Prowlarr 2.7.0, Sonarr 4.0.20, Radarr 6.4.4, Lidarr 3.1.6, and
Whisparr 3.6.5.
