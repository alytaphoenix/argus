# Troubleshooting

## First run shows the setup screen even though config.toml exists

`Config.Validate()` runs after the keychain overlay. If the file has a
`token_id` but no `secret`, and no `proxmox_secret` keychain entry exists, argus
asks for credentials. Fix one of:

```bash
# store the token secret (macOS example)
security add-generic-password -U -s argus -a proxmox_secret -w 'TOKEN_SECRET'

# or use an environment variable
export PROXMOX_SECRET='TOKEN_SECRET'
```

For a ticket-auth setup, store `proxmox_password` instead.

## Proxmox returns 401 / "authentication failure"

- Verify the token id format: `user@realm!tokenname`.
- Verify the token is enabled and has ACLs on `/` and `/vms`.
- Ticket auth (`username`) takes priority over token auth; remove `username`
  if you intend to use a token.
- Check the API from the shell:

```bash
curl -sk -H "Authorization: PVEAPIToken=user@pve!token=SECRET" \
  https://pve.example.com:8006/api2/json/version
```

## `argus config` reports a parse error

The config file is TOML. A common cause is an unquoted URL or a duplicated
section. Fix the file manually or move it aside:

```bash
mv ~/.config/argus/config.toml ~/.config/argus/config.toml.broken
argus
```

## Keychain access fails on Linux

Install and unlock a Secret Service provider (GNOME Keyring / KWallet) or use
config-file/env secrets. Headless containers usually have no Secret Service;
run argus on the workstation or supply `PROXMOX_SECRET` explicitly.

## Metrics exporter does not start

```bash
systemctl status argus-metrics
journalctl -u argus-metrics -n 100
ss -ltnp | grep 9099
```

Common causes:

- another process is already on port 9099;
- the config file is unreadable by the service user;
- the database path is not writable (`HOME=/var/lib/argus` in the unit).

The endpoint is loopback-only by default. Test locally:

```bash
curl -fsS http://127.0.0.1:9099/healthz
curl -fsS http://127.0.0.1:9099/metrics | head
```

## Prometheus cannot reach the exporter

The default listener is `127.0.0.1:9099`. Either scrape from the same host or
change the listener and add a firewall/authenticated proxy:

```toml
[prometheus]
listen = "0.0.0.0:9099"
```

Do not expose an unauthenticated metrics port to an untrusted network.

## Bootstrap says `[CONFIG]` for everything

Bootstrap only checks integrations that are configured. Add the relevant
section to `config.toml` (or `argus config`), store the secret in the keychain,
then press `x` to re-check. Salt must be configured before Salt-based
provisioning can work.

## Bootstrap found my VMs but does not manage them

Discovery lists every Proxmox guest and marks only VMs recorded in
`provisioned_vms` as argus-managed. Existing guests are intentionally treated
as unmanaged and are never reprovisioned automatically. Accept the Salt key on
the **Keys** tab, then target the minion from **Bootstrap** if you want argus to
manage it.

## SSH action says "could not get IP"

The QEMU guest agent must be installed and running in the guest, and the guest
must report an IP. Check in the Proxmox UI or with:

```bash
qm agent <vmid> network-get-interfaces
```

## SSH prompts for a host key or rejects a known host

argus uses `StrictHostKeyChecking=accept-new`: first contact is accepted, but a
changed key for an existing host is rejected. Remove the stale entry only after
verifying the change:

```bash
ssh-keygen -R <host-or-ip>
```

## Password-auth SSH fails

Password SSH requires `sshpass` in `PATH`. Prefer key-based auth. Passwords are
passed via `SSHPASS`, not argv.

## `go install` cannot find the module

The repository is public; ensure `GOPRIVATE`/`GONOSUMDB` do not include
`github.com/alytaphoenix/*`, and check `GOPROXY`:

```bash
go env GOPROXY GOPRIVATE
go install github.com/alytaphoenix/argus@latest
```

## Upgrading from ratataskr

argus migrates `~/.config/ratataskr/config.toml`, the database, and legacy
keychain entries under the `ratataskr` service on first use. The old files are
left in place.

## Reporting a security issue

Do not open a public issue. See [SECURITY.md](../SECURITY.md).
