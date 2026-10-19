# Security Policy

argus is a local, keyboard-driven control plane for a Proxmox homelab. It can
hold credentials for Proxmox, Salt, Nomad, storage, media, DNS, certificate,
and security services, and it can trigger privileged operations. Treat a
machine that runs argus as a trusted administration workstation.

## Supported versions

Security fixes are applied to the latest tagged release and to `main`.

| Version | Supported |
| --- | --- |
| Latest release | Yes |
| `main` | Yes |
| Older tags | Best effort |

## Reporting a vulnerability

Please **do not open a public issue** for a security problem.

Use GitHub's private vulnerability reporting:

1. Open the repository's **Security** tab.
2. Choose **Advisories** → **Report a vulnerability**.
3. Include affected version/commit, reproduction steps, impact, and any
   suggested fix.

You can expect an acknowledgement within a few business days. Please give us
a reasonable window to release a fix before public disclosure. We will credit
reporters who want to be credited.

## Security model

- **Trusted local machine.** The TUI, MCP server, and local SQLite database are
  trusted components. Anyone who can run code as your user can read the
  credentials available to that user.
- **Credentials.** Secrets should live in the operating-system keychain:
  macOS Keychain, Linux Secret Service, or Windows Credential Manager. The
  config file is written with mode `0600` and is a fallback, not the preferred
  secret store. Cloud-init profile passwords and Servarr API keys are stored
  in the local database/config; protect backups accordingly.
- **Proxmox API tokens.** Prefer a dedicated Proxmox user and a token with the
  smallest role that supports the operations you need. The bundled installer's
  `--token` option creates `root@pam!argus` with `privsep=0` for convenience;
  that token is root-equivalent. Revoke it with
  `pveum user token remove root@pam argus` and create a scoped token if that is
  too broad for your environment.
- **TLS.** TLS verification is on by default. `insecure = true` / `insecure`
  flags disable verification and should only be used for a documented
  self-signed service. Prefer installing the service CA. step-ca's
  `fingerprint` is pinned: argus fetches the CA root, verifies its SHA-256
  fingerprint, and then uses that root as the TLS trust anchor.
- **Metrics endpoint.** `argus-metrics-server` / the embedded exporter expose
  metrics over HTTP with **no authentication**. They bind to
  `127.0.0.1:9099` by default. Do not bind to `0.0.0.0` without a firewall or
  an authenticated reverse proxy.
- **MCP server.** `argus-mcp-server` is a local stdio server. Any process that
  can launch it and speak MCP can invoke its tools, including VM power
  operations and Salt state applications. Do not expose it over a network
  without an authenticated broker.
- **Runbooks.** `shell` runbook steps execute arbitrary commands on Salt
  minions via `cmd.run`. Runbooks are high-trust automation; review them before
  running and restrict who can edit them.
- **Terminal output.** Data returned by remote services is sanitized before
  rendering (control characters, ANSI escapes, and bidi overrides are removed)
  to prevent terminal escape injection from guest names, logs, alerts, or
  media metadata.

## Hardening checklist

- [ ] Use a dedicated Proxmox API user/token with least privilege.
- [ ] Store the token/password in the OS keychain, not a shell history or repo.
- [ ] Keep the config file (`~/.config/argus/config.toml` or
      `/etc/argus/config.toml`) at mode `0600`.
- [ ] Keep the database and its backups private; they can contain cloud-init
      passwords and provisioning metadata.
- [ ] Leave `insecure = false` unless a self-signed endpoint is unavoidable.
- [ ] Keep the metrics listener on loopback or behind an authenticated proxy.
- [ ] Run `govulncheck ./...` and update the Go toolchain regularly.
- [ ] Treat MCP clients and runbooks as privileged administrators.
