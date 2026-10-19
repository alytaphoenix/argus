# Platform bootstrap and Salt formula registry

`argus bootstrap` takes a Proxmox host from "just a hypervisor" to a managed
platform:

1. inventory nodes, guests, and storage;
2. install an isolated Salt master + salt-api on the host;
3. create a dedicated PAM API user and a PVE-CA-signed TLS certificate;
4. install the local minion and accept its key;
5. materialize argus's opinionated formula files under `/srv/salt/argus`;
6. apply the baseline formulas;
7. record platform state, formula installations, and bootstrap runs in SQLite.

The same engine is available from the TUI at **Bootstrap** and from the CLI.

## Quick start

```bash
# inspect only — no changes
argus bootstrap plan

# preview (still no changes)
argus bootstrap apply --dry-run

# install/update Salt and apply the safe baseline
argus bootstrap apply --yes

# apply additional formulas
argus bootstrap apply --yes --formulas base,docker,node-exporter,technitium

# registry only
argus bootstrap formulas
argus bootstrap formulas --json
```

`apply` requires `--yes`. The TUI asks for confirmation instead.

## Formula registry

The registry is opinionated: every formula has an ID, category, kind,
dependencies, SLS states, health metadata, and documentation.

| Kind | Meaning |
| --- | --- |
| `builtin` | Ships SLS files inside argus; argus writes them to the master and applies them. |
| `external` | Assumes state files already exist on the master; argus applies the named state. |
| `integration` | Represents an existing service (TrueNAS, Plex, Servarr, Hermes). Argus wires and health-checks it; it does not install it. |

Built-ins are deliberately small and safe:

| Formula | Category | What it does |
| --- | --- | --- |
| `base` | Platform | ca-certificates, chrony, curl, jq, unattended-upgrades. |
| `salt-minion` | Platform | Configures a minion to point at the argus master. |
| `docker` | Runtime | Debian docker.io + docker-compose, Docker enabled. |
| `node-exporter` | Observability | prometheus-node-exporter on `:9100`. |
| `technitium` | Network | Technitium DNS container (`:53`, `:5380`). |
| `traefik` | Network | Traefik v3 container (`:80`, `:443`) with the file provider and `/etc/traefik` mounted. |
| `uptime-kuma` | Observability | Uptime Kuma container (`:3001`). |

Integrations: `truenas`, `plex`, `servarr`, `hermes`.

The **baseline** is `base` + `node-exporter`. The **recommended** next step is
`docker` + `technitium` + `uptime-kuma`. Nothing that publishes ports or
replaces an existing service is applied implicitly.

## TUI

Open the **Bootstrap** tab (after **Orch**).

| Key | Action |
| --- | --- |
| `x` / `r` | Re-inspect the platform and registry |
| `space` | Select a formula |
| `a` | Apply selected formulas (confirmation) |
| `A` | Apply the baseline (confirmation) |
| `f` | Show installable formulas only / all |
| `b` | Back |

The readiness panel shows Proxmox inventory, SSH, Salt runtime, Salt API/keys,
and the state tree. The registry shows `applied`, `available`, or `external`
for each formula.

Service wiring is now automatic: adding, removing, or changing a service in the
Services registry pushes the DNS/Traefik pillar and applies `services.wiring`.
The same refresh runs after a provisioned node's Salt profile is applied. See
[Service DNS and TLS](service-wiring.md) for the required `[technitium]`,
`[traefik]`, and `[stepca]` configuration.

## Salt runtime layout

Debian 13 does not ship Salt in its main archive, so argus installs Salt 3008.x
in an isolated venv instead of touching Proxmox's system Python:

```text
/opt/argus-salt/venv/                Salt 3008.x
/etc/salt/master.d/argus.conf        master config, PAM auth, netapi clients
/etc/salt/master.d/api.conf          rest_cherrypy + PVE-CA TLS
/etc/salt/minion.d/argus.conf        local minion
/etc/argus/salt-api-password         0600 API password
/etc/argus/tls/salt-api.crt|key      PVE-CA-signed API certificate
/var/cache/salt, /var/log/salt       Salt state
/srv/salt/argus/<formula>/           generated formula files
```

Systemd units:

- `argus-salt-master.service`
- `argus-salt-api.service`
- `argus-salt-minion.service`

The API listens on `:8000` with TLS; minions use the standard `4505/4506`
ports. Make sure the Proxmox firewall allows those ports only from the
management/minion networks. The reference deployment allows the LAN and drops
outside traffic.

## Configuration

`argus bootstrap` uses `[bootstrap]` defaults and can be overridden per run:

```toml
[bootstrap]
ssh_user       = "root"
ssh_key        = "~/.ssh/argus_bootstrap"
salt_master_id = "proxmox"
salt_api_url   = "https://pve.example.com:8000"
salt_api_user  = "saltapi"
```

The resulting Salt connection is written to `[salt]` and the password is stored
in the OS keychain (`salt_password`). The host's `/etc/argus/config.toml` can
use `https://127.0.0.1:8000` with the same user.

Environment overrides:

- `ARGUS_BOOTSTRAP_SSH_USER`
- `ARGUS_BOOTSTRAP_SSH_KEY`
- `ARGUS_BOOTSTRAP_SALT_MASTER_ID`
- `ARGUS_BOOTSTRAP_SALT_API_URL`
- `ARGUS_BOOTSTRAP_SALT_API_USER`

## Tracking and idempotency

SQLite tables:

- `platform_profiles` — host, Salt master/API, status, last inspection.
- `formula_installations` — per-formula/target applied state, version, error.
- `bootstrap_runs` — applied plan, log, status, timestamps.

Every plan/apply is additive and idempotent:

- existing Salt runtime is reused if `salt-master`/`salt-api` are active;
- existing config, keys, TLS certs, and API passwords are preserved;
- unchanged formula files are rewritten identically;
- package/service/container states are Salt-idempotent;
- no VM, container, storage, or Proxmox config is deleted.

## Safety

- The first bootstrap needs root SSH to the Proxmox host. After that, argus uses
  the salt-api and a scoped PVE token.
- The `saltapi` PAM user is granted `.*`, `@wheel`, and `@runner` through
  Salt's external auth because argus manages keys and states. Treat this as a
  privileged account.
- `netapi_enable_clients` explicitly enables `local`, `local_async`, `wheel`,
  and `runner`; keep this list minimal.
- Docker socket mounts in container formulas are privileged interfaces.
- Formula application is recorded but not automatically rolled back. Review
  the plan before applying.

## Adding a formula

Add a `formula.Formula` to `formula/catalog.go` (or register an external one)
with:

- a unique ID and category;
- `DependsOn` for ordering;
- `States` / `Files` for built-ins;
- optional `Health` for verification;
- `Docs` and a non-destructive description.

Keep built-ins small, idempotent, and safe by default. Add a test to
`formula/formula_test.go` and `bootstrap/bootstrap_test.go` if the formula
introduces new rendering or dependency behavior.
