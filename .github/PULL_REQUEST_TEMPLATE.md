## What changed

<!-- A short summary of the change and the problem it solves. -->

## Why

<!-- Link the issue or explain the motivation. -->

## How tested

<!-- Commands, hosts, or screens used. Do not include secrets or public IPs. -->

- [ ] `make test`
- [ ] `make vet`
- [ ] `make lint`
- [ ] `make vuln`
- [ ] Docs updated (README / docs/ / config.toml.example) if behavior changed

## Security checklist

- [ ] No credentials, tokens, private hostnames, or personal data added.
- [ ] Destructive actions still require confirmation.
- [ ] New external input is treated as untrusted (terminal output, file paths,
      shell/Salt commands).
- [ ] New secrets use the keychain or a `0600` file, not logs or argv.
