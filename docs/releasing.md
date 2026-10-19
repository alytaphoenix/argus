# Releasing

Maintainer checklist for a public release.

## 1. Prepare

- [ ] Update `CHANGELOG.md` with the release version and date.
- [ ] Confirm `go.mod` points at a patched Go toolchain.
- [ ] Run the full suite:

```bash
make fmt
make check
make test-race
```

- [ ] Scan the tree and history for secrets:

```bash
git grep -n -I -E 'BEGIN (OPENSSH|RSA|EC) PRIVATE KEY|PVEAPIToken=[A-Za-z0-9@!_-]+='
git log -p --all | grep -aE 'BEGIN (OPENSSH|RSA|EC) PRIVATE KEY|PVEAPIToken=[A-Za-z0-9@!_-]+='
```

- [ ] Verify `README.md`, `docs/`, and `config.toml.example` have no private
      hostnames, personal usernames, or live credentials.

## 2. Tag and push

```bash
git tag -a v0.1.0 -m "argus v0.1.0"
git push origin main
git push origin v0.1.0
```

The `Release` workflow runs GoReleaser and publishes:

- `argus`, `argus-metrics-server`, `argus-mcp-server` for Linux, macOS, and
  Windows (amd64/arm64)
- `checksums.txt`
- archives containing the README, license, changelog, config example, and docs

## 3. Verify

- [ ] Download one archive per OS/arch and run `argus --version`.
- [ ] Confirm `argus --help` and `argus config` start.
- [ ] Verify the release notes and checksums.
- [ ] Confirm the CI badge and release badge render on the README.

## 4. Post-release

- [ ] Bump `CHANGELOG.md` back to `[Unreleased]`.
- [ ] Announce in the project's discussion/issue tracker if desired.
- [ ] Rotate any token that was used during release testing.
