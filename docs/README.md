# Documentation

Start with the [project README](../README.md) if you are new to argus.

| Document | What it covers |
| --- | --- |
| [Bootstrap and formulas](bootstrap.md) | Platform bootstrap, Salt runtime, opinionated formula registry, TUI/CLI. |
| [Service stacks](service-stacks.md) | Declarative TOML stacks, install paths, target minions, built-in servarr. |
| [Service DNS and TLS](service-wiring.md) | Automatic Technitium records, Traefik routes, and step-ca ACME certificates. |
| [Configuration](configuration.md) | Auth, secrets, env vars, every config section, MCP registration. |
| [TrueNAS](truenas.md) | REST vs JSON-RPC/WebSocket transports, `argus truenas` CLI reference. |
| [Deployment](deployment.md) | Workstation install, Proxmox host bootstrap, systemd, upgrades, uninstall. |
| [Architecture](architecture.md) | Packages, screens, data flow, clients, MCP, metrics. |
| [Security and hardening](security.md) | Threat model, keychain/DB handling, token scoping, TLS, metrics, runbooks, terminal output. |
| [Troubleshooting](troubleshooting.md) | First-run, 401s, keychain, metrics, Bootstrap, SSH, upgrades. |
| [Releasing](releasing.md) | Maintainer release checklist and tagging. |
| [Servarr orchestration review](review-servarr-orchestration.md) | Orchestrator/DCIM review, stack design, and usability findings from the phoenix-servarr case. |
| [Security review (Oct 2026)](security-review-2026-10.md) | Hardening review findings, host audit, and residual actions. |
| [Historical code review](history/code-review-2026-07.md) | Internal review from July 2026, kept for history. |

Project policies:

- [Security policy](../SECURITY.md)
- [Contributing](../CONTRIBUTING.md)
- [Code of Conduct](../CODE_OF_CONDUCT.md)
- [Changelog](../CHANGELOG.md)
