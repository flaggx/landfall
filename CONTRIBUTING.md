# Contributing to Landfall

Thanks for helping make self-hosted deploys easier for everyone.

This project is **MIT-licensed open source** (CLI: `landfall`). You do not need to ask permission to open an issue or PR. Small docs fixes are as welcome as features.

## Ways to contribute

- Fix bugs or clarify the README
- Improve Ubuntu/Postgres/Redis/Caddy automation
- Add tests around config parsing and scripts
- Share failure modes you hit on real VPSes (redact secrets and IPs)

AI-assisted contributions are welcome. Please run the checks below and read your own diff before opening a PR.

## Development setup

- Go version: see `go.mod`
- Make (optional)

```bash
git clone https://github.com/flaggx/landfall.git
cd landfall
make build
./bin/landfall --help
```

## Common tasks

```bash
make build    # bin/landfall
make install  # ~/bin/landfall
make check    # fmt + vet + race tests (required before PR)
make test
make vet
make fmt
make clean
```

## Project layout

```
cmd/landfall/       CLI entrypoint
internal/cli/        Cobra commands
internal/config/     TOML + secrets
internal/deploy/     Deploy pipeline
internal/bootstrap/  First-time VPS setup
internal/db/         Postgres bootstrap, backups, replica, pooler
internal/redis/      Redis bootstrap
internal/security/   Hardening + permissions
internal/ssh/        SSH client
templates/           Examples for apps (not real project configs)
```

## Making changes

1. Branch from `main`
2. Make focused changes
3. Run the contributor gate before opening a PR:

```bash
make check
```

That runs `gofmt` verification, `go vet`, and `go test -race ./...`. CI runs the same target.

4. Update the README if user-facing behavior changes
5. Open a PR with a short summary of **why**

### Adding a command

1. Implement under `internal/`
2. Wire in `internal/cli/`
3. Document in `README.md` (manual + agent sections if relevant)
4. Add unit tests for pure logic and generated scripts (no real VPS required)

## Test expectations

- Prefer table-driven unit tests
- Assert on generated bash scripts with `strings.Contains` (or goldens) for bootstrap/backup/harden paths
- Use `t.TempDir` + `t.Setenv("HOME", …)` for secrets/config file tests
- Do **not** require SSH, apt, or a live VPS in CI tests
- Cover new CLI commands at least with `--help` smoke tests when adding cobra commands

## Secrets & privacy

- Never commit `landfall.toml` with real IPs into **this** repo — only example hosts like `203.0.113.10`
- Never commit `secrets.toml`, `.env*`, or keys
- Never log secret values in tests or CLI output (mask by default)
- Prefer `0600` permissions for secrets files

## Testing

```bash
go test ./...
go test -v ./internal/config/...
```

Tests must not require SSH or a live VPS.

## Pull requests

- Keep the diff focused
- Note README / config format changes
- CI runs `go vet`, `go test`, and `gofmt` checks

## Reporting issues

Include:

- OS and Go version (`go version`)
- Command you ran
- Error output with **IPs, hostnames, and secrets redacted**
