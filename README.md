# argus

A Bubble Tea TUI for managing a Proxmox homelab. See [CLAUDE.md](CLAUDE.md) for
architecture and integration details.

## Install

```bash
go install github.com/alytaphoenix/argus@latest
```

This repo is private, so Go needs to fetch it via `git` (authenticated)
instead of the public module proxy/checksum database:

```bash
export GOPRIVATE=github.com/alytaphoenix/*
```

Add that to your shell profile (already done in this environment's
`~/.zshrc`), and make sure `git` can authenticate to GitHub over HTTPS or SSH
(e.g. via `gh auth login` + `gh auth setup-git`, or an SSH key). Then make
sure `$(go env GOPATH)/bin` (typically `~/go/bin`) is on your `PATH` so the
installed `argus` binary is runnable.

To upgrade to the latest commit on `main`:

```bash
go install github.com/alytaphoenix/argus@latest
```

## Build from source

```bash
git clone https://github.com/alytaphoenix/argus.git
cd argus
make build        # argus
make build-all     # argus + argus-metrics-server + argus-mcp-server
```

## Configure

Copy `config.toml.example` to `~/.config/argus/config.toml`, or just run
`argus` — with no working Proxmox config it walks you through first-run
setup.
