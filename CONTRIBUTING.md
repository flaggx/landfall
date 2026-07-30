# Contributing to vpsdeploy

Thanks for helping improve this project.

## Development setup

Requirements:

- Go 1.22 or later
- Make (optional, but recommended)

```bash
git clone git@github.com:flaggx/vpsdeploymentautomation.git
cd vpsdeploymentautomation
make build
./bin/vpsdeploy --help
```

## Common tasks

```bash
make build    # compile to bin/vpsdeploy
make install  # build and copy to ~/bin/vpsdeploy
make test     # run all tests
make vet      # static analysis
make fmt      # format Go files
make clean    # remove build artifacts
```

## Project layout

```
cmd/vpsdeploy/          CLI entrypoint
internal/cli/           Cobra commands
internal/config/        TOML config and secrets storage
internal/deploy/        Deploy pipeline
internal/bootstrap/     VPS first-time setup
internal/ssh/           SSH client
templates/              Example config files
```

## Making changes

1. Create a branch from `main`
2. Make your changes
3. Run `make fmt test vet` before opening a PR
4. Update the README if you add or change user-facing behavior

## Adding a new command

1. Add the command in `internal/cli/`
2. Wire it up in `internal/cli/root.go`
3. Document it in `README.md`
4. Add tests if the logic is non-trivial

## Secrets handling

Secrets live in `~/.config/vpsdeploy/secrets.toml` and are managed via:

```bash
vpsdeploy secrets init
vpsdeploy secrets set my_secret
vpsdeploy secrets check
```

When working on secrets-related code:

- Never log secret values
- Never commit `secrets.toml` or `.env` files
- Keep file permissions at `0600` for the secrets file
- Prefer masked output unless `--reveal` is explicitly passed

## Testing

```bash
go test ./...
go test -v ./internal/config/...
```

Tests should not require a real VPS or SSH connection.

## Pull requests

- Keep changes focused
- Include a short description of what changed and why
- Note any README or config format updates

## Reporting issues

Include:

- OS and Go version
- The command you ran
- Full error output (redact IPs, hostnames, and secrets)
