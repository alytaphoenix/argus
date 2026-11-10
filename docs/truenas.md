# TrueNAS

argus reads NAS state — pools, datasets, alerts, services — and can manage
datasets, services, and NFS shares, from the TUI (**Services → TrueNAS**) or the
shell (`argus truenas`).

## Configuration

```toml
[truenas]
url      = "https://truenas.internal"
api_key  = ""      # store in the OS keychain as argus/truenas_api_key
insecure = false   # true only for a self-signed certificate you cannot trust
```

Env vars: `TRUENAS_URL`, `TRUENAS_API_KEY`. Omit the section to disable the
integration everywhere (TUI tab, MCP tools, CLI). Read access is enough for
`status`/`datasets`/`services`/`nfs-list`; the management subcommands need a key
with write privileges on the affected services.

`insecure` applies to the first dial **and** to every reconnect — a dropped
WebSocket is re-dialled with the same TLS policy, never with verification
silently disabled.

## Two API generations

| TrueNAS | What argus speaks |
| --- | --- |
| ≤ 25.10 | REST v2.0 at `/api/v2.0`, `Authorization: Bearer <api_key>` |
| 26.0, 27.x | JSON-RPC 2.0 over WebSocket at `/api/current` (`wss://`), authenticated with `auth.login_with_api_key` |

TrueNAS 26.0 removed the REST v2.0 surface. `NewTrueNASClient` probes
`GET /api/v2.0/system/info` once: an answer that is not 404 and not 5xx means
REST exists and the client stays on it, otherwise it flips to WebSocket and maps
each internal REST-style path onto the equivalent JSON-RPC method. Call sites are
identical for both generations, so the same binary drives a 25.10 box and a 27.x
box with no configuration difference.

Differences the mapping absorbs:

- **Readiness.** 26+ dropped `system_ready` from `system.info`. Left alone it
  decodes to `false`, and the TUI badge plus the `truenas_status` MCP tool report
  a healthy NAS as "not ready". On the WebSocket path the client fills the field
  from `system.ready`.
- **Mutations.** REST `POST /service/start` with a `{"service": …}` body becomes
  `service.start(service, {"timeout": 30})`; `POST`/`DELETE` on `/pool/dataset`
  and `/sharing/nfs` become `pool.dataset.*` / `sharing.nfs.*`.
- **Writes over REST** send an explicit `Content-Length` rather than a chunked
  body.

Verified against `TrueNAS-27.0.0-RC.1`: status, datasets, services, NFS share
list/create/delete, dataset create/delete, and service start/stop.

## CLI

```bash
argus truenas status                        # system info, alerts, pools, datasets, services
argus truenas datasets                      # ZFS datasets with free space
argus truenas services                      # every NAS service and its state
argus truenas start   <service>             # e.g. nfs
argus truenas stop    <service>
argus truenas restart <service>
argus truenas nfs-list
argus truenas nfs-create <path> <network>   # e.g. /mnt/PhoenixNest/Media 192.168.0.0/24
argus truenas nfs-delete <id>
argus truenas ds-create <name> [type]       # type defaults to FILESYSTEM
argus truenas ds-delete <name>
```

| Subcommand | REST (≤25.10) | JSON-RPC (26.0+) |
| --- | --- | --- |
| `status` | `GET /system/info`, `/pool`, `/pool/dataset`, `/alert/list`, `/service` | `system.info`, `system.ready`, `pool.query`, `pool.dataset.query`, `alert.list`, `service.query` |
| `datasets` | `GET /pool/dataset` | `pool.dataset.query` |
| `services` | `GET /service` | `service.query` |
| `start` / `stop` / `restart` | `POST /service/{action}` | `service.start` / `service.stop` / `service.restart` |
| `nfs-list` | `GET /sharing/nfs` | `sharing.nfs.query` |
| `nfs-create` | `POST /sharing/nfs` | `sharing.nfs.create` |
| `nfs-delete` | `DELETE /sharing/nfs/{id}` | `sharing.nfs.delete` |
| `ds-create` | `POST /pool/dataset` | `pool.dataset.create` |
| `ds-delete` | `DELETE /pool/dataset/{id}` | `pool.dataset.delete` |

Behaviour notes:

- Dataset names are full ZFS paths (`PhoenixNest/Media`); the mount path is
  `/mnt/` + name. `ds-delete` takes that full path and is **not** recursive —
  a dataset that still has children is refused (`pool.dataset.delete: Method
  call error`) rather than destroyed.
- `nfs-create` publishes an enabled `SYS`-security share for exactly one network
  (measured: the created share reports `security=["SYS"]`). It sets no `ro`,
  mapall, or alias options; tune those in the TrueNAS UI.
- `status` treats pool/system errors as fatal and dataset/alert/service errors as
  non-fatal, so a partially responsive NAS still renders.
- Failures print `truenas <op>: <error>` and exit 1; bad arguments exit 2. The
  whole invocation runs under a 120 s deadline.

## Dataset scope

`Datasets` asks for **flat** rows (`extra={"flat":true}`). Asked plainly,
`pool.dataset.query` nests every child inside its parent row, so argus would see
one entry per pool and nothing else — which silently starves both consumers:

- the Plex screen picks media roots by matching dataset names (`media`,
  `movies`, `tv`, `music`, `photos`, `plex`) and builds `/mnt/<name>`;
- the orchestrator's TrueNAS ingest writes one `storage` pillar entry per
  dataset with `nfs_export`/`mount_path` = `/mnt/<name>`.

On a pool named `PhoenixNest` the picker finds no library path at all and the
pillar exports `/mnt/PhoenixNest` instead of `/mnt/PhoenixNest/Media`. `name` is
the full ZFS path, so `/mnt/` + name is the real mountpoint.

Entries deeper than one level (`PhoenixNest/Media/Movies`) are dropped — content
folders, not export candidates.
