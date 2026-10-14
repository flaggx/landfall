# TODO

Findings from a code review of `internal/deploy` and `internal/ssh`.

## Deploys

- [ ] Add rollback. Deploys reset and rebuild the directory the running app
      is served from (`syncRepo`, `buildApp`), so a failed build leaves it
      half-built and there is no way back to the previous version. Build
      into a per-release directory, switch a `current` symlink on success,
      keep the last few releases, and add `landfall rollback`.

## Shell safety

- [ ] Validate `project.name` and environment names (for example
      `^[a-z0-9][a-z0-9-]*$`). They are interpolated unquoted into shell
      commands as the service name and into `.deploy-meta`
      (`restartService`, `activateRelease`, `Status`, `Logs`).
- [ ] Upload remote scripts to a private, unpredictable path (`mktemp`)
      instead of fixed names under `/tmp` (`RunScript`).

## Testing

- [ ] Add an integration test that runs a deploy against a real SSH server
      (for example a container in CI). Current tests cover config and
      script generation only.

## Cleanup

- [ ] Remove the unused-import placeholder at the end of
      `internal/deploy/deploy.go`.
- [ ] Share one `shellQuote` helper instead of the copies in
      `internal/deploy` and `internal/ssh`.
- [ ] Handle the `StdinPipe` error in `UploadFile`.
