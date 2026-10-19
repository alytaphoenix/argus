# Service DNS, Traefik, and automatic TLS

argus keeps a registry of services in the **Services** screen. When a service
is added, updated, or removed, argus regenerates a Salt pillar and applies the
`services.wiring` state on the Salt master. The same refresh runs after a
provisioned node's Salt profile is applied, so newly managed nodes pick up the
current DNS and TLS configuration.

This replaces the old manual `w` step. `w` still exists as a force-refresh.
No `/srv/pillar/top.sls` change is needed: argus passes the generated pillar to
`state.apply` inline. It also writes `/srv/pillar/argus_services.sls` (`0600`)
for inspection or for operators who prefer a conventional top-file include.

## What gets created

For every enabled service in the registry:

| Service scheme | DNS record | Traefik route | Certificate |
| --- | --- | --- | --- |
| `http` / `https` | `A <slug>.<domain>` → Traefik ingress | `/etc/traefik/dynamic/<slug>.yml` | step-ca ACME (when configured) |
| `tcp` (or anything else) | `A <slug>.<domain>` → service host, or `CNAME` when the host is a name | none | none |

- `<slug>` is the lower-case, hyphenated service name (for example
  `Proxmox Web UI` → `proxmox-web-ui`).
- `tcp` services are not proxied: the DNS record points directly at the
  service so tools that speak non-HTTP protocols still work.
- Disabled services are removed from Traefik on the next refresh.
- Traefik obtains and renews certificates itself through the step-ca ACME
  resolver. If ACME is not configured, Traefik serves its built-in self-signed
  certificate for `websecure`.

## Requirements

```toml
[globals]
domain = "home.arpa"

[technitium]
url = "http://dns.home.arpa:5380"
# token stored in the Keychain (technitium_token)

[salt]
url           = "https://pve.home.arpa:8000"
username      = "saltapi"
master_minion = "proxmox"
# password stored in the Keychain (salt_password)

[traefik]
# Optional. Defaults to the host in the Proxmox URL. This must be the address
# clients and step-ca use to reach Traefik (port 80 for ACME HTTP-01).
ingress_ip = "192.168.1.10"

[stepca]
url              = "https://ca.home.arpa:8443"
fingerprint      = "..."      # optional, recommended
acme_provisioner = "acme"     # default when [stepca] url is set
# acme_url = "https://ca.home.arpa:8443/acme/acme/directory"  # optional
```

The Salt master must also have the Traefik formula installed:

```bash
argus bootstrap apply --yes --formulas base,docker,traefik
```

Applying the formula creates `/etc/traefik/traefik.yml`, mounts
`/etc/traefik` into the Traefik container, and enables the file provider.
Existing installations need the formula re-applied once to pick up the
`--configFile` flag and mount.

## step-ca ACME

Create an ACME provisioner on the CA host if you do not already have one:

```bash
step ca provisioner add acme --type ACME
systemctl restart step-ca
```

The CA must be able to reach the Traefik ingress on port 80 for the HTTP-01
challenge. The default ACME directory is:

```text
<stepca.url>/acme/<acme_provisioner>/directory
```

Use `[stepca] acme_url` if your step-ca uses a non-standard ACME path.

Check the result from the Services screen:

- **DNS** tab: the `<service>.<domain>` record points at the Traefik ingress.
- **Certificates** tab: run a health check on the service; TLS metadata is
  captured and stored.
- Traefik logs: `docker logs traefik` should show ACME orders completing.

## Provisioned nodes

When a VM is cloned from the Provision screen and `[globals] domain` +
`[technitium]` are configured, argus waits up to 90 seconds for the QEMU guest
agent to report an IPv4 address and creates:

```text
<vm-name>.<domain>  A  <guest-ip>
```

The record is best-effort: if the guest agent is not installed or does not
report an IP, provisioning still succeeds and the Provision screen shows a
warning. The IP is also written back to `provisioned_vms` for later operations.

Per-node certificates are intentionally **not** issued. Certificates are issued
for the service names that Traefik exposes, which is what browsers and clients
actually validate.

## Security and failure behavior

- The Technitium token is written to `/etc/argus/technitium.token` with mode
  `0600` and is never embedded in the pillar or the Salt state.
- The pillar is written with mode `0600`; the state is `0644`.
- DNS and Traefik updates are best-effort. A failed wiring apply is reported in
  the UI but does not fail a VM clone or a Salt profile application.
- The dynamic directory `/etc/traefik/dynamic` is argus-managed. Files ending in
  `.yml` that do not correspond to a registered service are removed on refresh.
- DNS records are **not** deleted automatically when a service is removed; a
  stale record points at the ingress and returns a Traefik 404. Delete it from
  the **DNS** tab if needed.
