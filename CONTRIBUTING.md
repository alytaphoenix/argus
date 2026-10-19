# Contributing to argus

Thanks for helping improve argus. This project is a Go/Bubble Tea TUI for
Proxmox homelabs; the most useful contributions are bug reports with
reproductions, documentation fixes, additional integrations, and focused
patches.

## Development setup

Requirements:

- Go 1.25 or newer (the `go.mod` toolchain pins a patched Go 1.26.x for
  security fixes).
- A Proxmox VE host or cluster for anything that talks to the PVE API. You do
  **not** need one to work on the UI or unit tests.

```bash
git clone https://github.com/alytaphoenix/argus.git
cd argus
make build
make test
make run
```

Useful targets:

```bash
make build          # argus
make build-all      # argus + metrics + MCP server
make test           # go test ./...
make vet            # go vet ./...
make lint           # staticcheck ./...
make vuln           # govulncheck ./...
make dist-linux     # linux/amd64 release binaries
```

## Before opening a pull request

1. Run `gofmt -w .` (or `make fmt`).
2. Run `make test`, `make vet`, `make lint`, and `make vuln`.
3. Add or update tests for behavior changes.
4. Keep commits focused and explain the *why* in the commit message.
5. Update `README.md`, `config.toml.example`, and the relevant file under
   `docs/` when user-facing behavior changes.
6. Do not include real credentials, hostnames, IP addresses, email addresses,
   tokens, database files, or screenshots containing them.

## Code style

- Follow existing package boundaries and the sub-model pattern in `ui/`.
- Prefer small, testable functions and explicit error handling.
- Do not silently ignore errors unless the operation is genuinely best-effort;
  add a comment when it is.
- Keep optional integrations nil-safe: an unconfigured client should disable
  its screen/feature rather than crash.
- Never add a destructive action without a confirmation in the TUI and a clear
  warning in the docs.

## Security

Please read [SECURITY.md](SECURITY.md) before changing credential handling,
TLS behavior, MCP tools, runbooks, or the bootstrap installer. Report
vulnerabilities privately, not in a public issue.

## License

By contributing, you agree that your contributions are licensed under the
project's [MIT License](LICENSE).
