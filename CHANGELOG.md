# Changelog

All notable changes to Landfall are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- `redis migrate` no longer refuses an environment because another one on
  the same host uses the same DB index after that one has already moved to
  its own instance (for example staging migrated first, now on
  `127.0.0.1:6380/0`, blocking prod on `6379/0`). Only environments whose
  `REDIS_URL` is still on the shared Redis count. The error no longer
  suggests `--from-db`, which did not get past this check.

## [0.5.0] - 2026-11-05

### Upgrading

- **Redis: each environment now has its own instance** (see Changed). Move
  every existing environment, prod included, with
  `landfall redis migrate --env <env>`: it copies the environment's keys and
  switches the app over, with a few seconds of downtime. Until then
  `landfall check` warns and `redis bootstrap` refuses to start the
  environment on an empty instance (`--fresh` does that on purpose).
  `redis.port = 6379` is no longer allowed: that is the shared Redis.

### Changed

- **Redis: one instance per environment.** `redis bootstrap` runs each
  environment's Redis as its own `redis-server@<project>-<env>` instance
  (the `redis-server` package's template unit), with its own port (picked
  from 6380 up unless `port` is set), data, `aclfile`, admin password and
  memory limit (`maxmemory`, default 128 MB, no limit for prod;
  `maxmemory_policy`, default `noeviction`). On the shared Redis every
  environment could switch to another's DB index, `FLUSHALL` wiped them
  all, and one environment filling memory evicted another's keys.
- The `redis.database` default is now 0, since each instance has its own
  databases.

### Security

- Scripts, their results (which can hold new passwords) and uploaded unit
  and Caddy files no longer use fixed names in `/tmp` on the server, which
  any other user there could create first or symlink. Each session uses its
  own `mktemp -d` directory (mode `700`) and removes it at the end.
- Uploaded files are created with their final mode (for example `600` for
  `.env.production`) instead of `644` followed by a chmod, so secrets are
  never briefly readable by other users on the server.

### Added

- `landfall redis migrate --env <env>`: moves an environment from the shared
  Redis to its own instance with its data (types, TTLs and key names kept;
  the shared Redis keeps its copy for a rollback), then switches the app's
  `REDIS_URL` and restarts it. It refuses to copy a DB index that another
  environment on the host also uses.
- `landfall redis bootstrap --fresh` starts an environment on an empty
  instance even though it still has data on the shared Redis.
- [scripts/test-redis-migrate.sh](scripts/test-redis-migrate.sh): runs
  `redis migrate` end to end on a real server, on a non-prod environment,
  and checks the copy, the switch, the downtime, a restart of the new
  instance and a deploy afterwards.
- [templates/cache-handler.js.example](templates/cache-handler.js.example):
  a Next.js cache handler that keeps ISR pages in the app's writable cache
  directory, so regenerated pages survive restarts while the code stays
  read-only. The systemd unit sets `LANDFALL_ISR_CACHE_DIR` for it, and
  `landfall check` warns when the service's log shows failed ISR cache
  writes. See "ISR and the read-only code" in the README.

### Fixed

- Connecting no longer fails with a false "REMOTE HOST IDENTIFICATION HAS
  CHANGED" when `known_hosts` has a different key type for the server than
  the one Go picked (for example ed25519 recorded, ECDSA negotiated). Only
  the recorded key types are negotiated now.
- `landfall redis bootstrap` works on Redis 6.0 (Ubuntu 22.04). It used
  channel rules (`&*`) and the `-` bind prefix, which only exist from 6.2,
  and the app user could not run `CLIENT SETNAME`, which client libraries
  send while connecting.
- `redis bootstrap --reset-password` no longer locks the app out if a later
  step fails: every check runs before the password changes, and the new
  `REDIS_URL` is saved or printed even when the run fails afterwards.
  Inline `user` lines in `redis.conf` now stop the run before anything
  changes.
- A backup whose `pg_dump` fails partway no longer leaves a truncated
  `.dump` that `db list` shows and `db restore` accepts.
- Deploy paths inside a home directory work with the app user: it gets an
  ACL entry to pass through parent directories it cannot search (Ubuntu
  21.04+ creates homes as `0750`). The `acl` package is installed if needed.
- `landfall check` reports missing secrets in its table instead of stopping
  with "secret ... is not set" before printing anything.
- `bootstrap --caddy` keeps existing sites in `/etc/caddy/Caddyfile`. Only a
  missing, empty or stock demo file is replaced; any other file gets
  `import landfall/*` appended. The old file is backed up, and if Caddy
  rejects the result it is restored and Caddy is not reloaded.
- File uploads no longer risk a panic from a race between setting up the
  scp session and starting it.
- The app's sandbox allows `AF_NETLINK`, so `os.networkInterfaces()` no
  longer throws inside the service.
- Deploys accept every ref git accepts (for example `v1.2.0+build.5`,
  `feat/a,b`, `_wip`); a leading `-` and shell metacharacters are still
  refused.
- With `build = "local"`, uploaded files are written as
  `deploy:<app group>` with their final modes, so the running app can read
  new files straight away instead of getting `EACCES` until the end of the
  deploy.
- The permissions step runs right after activate, before
  `release_command`, so new files don't stay unreadable to the app while
  migrations run.

## [0.4.0] - 2026-10-23

### Added

- `landfall db schedule --ping-url <url>`: the scheduled backup requests the
  URL after each successful run and `<url>/fail` when any step fails, for a
  dead man's switch such as Healthchecks.io.

### Fixed

- `db schedule --upload` without S3 settings now fails instead of installing
  a backup that never uploads.
- A scheduled `--upload` backup now fails when the `aws` CLI is missing and
  can't be installed, instead of finishing as if the dump had been uploaded.

## [0.3.1] - 2026-10-23

### Fixed

- The permissions step names the deploy user's group explicitly. `chown
  user:` leaves the group unchanged with uutils coreutils (Ubuntu 26.04's
  default), so `.env.production` kept the app user's group. Its mode was
  still `600`, so the app user could not read it.

## [0.3.0] - 2026-10-23

### Upgrading from 0.2.x

Run `landfall bootstrap --env <env>` once per environment, then deploy. It
creates the app user, hands it the code read-only and installs the sandboxed
unit; the restart takes a few seconds. Until then deploys keep working as
before and `landfall check` reports `service user` and `service sandbox` as
failing. If your app writes to disk anywhere other than Next.js's cache, move
that to object storage or a database first.

### Security

- The app no longer runs as the deploy user. That user has passwordless
  sudo, so any code execution in the app was root on the VPS. `bootstrap`
  now creates an unprivileged system user per environment (default
  `<project>-<env>`, or `app_user`) with no shell, home or sudo, and the
  systemd unit runs the app as it.
- The unit is sandboxed: `NoNewPrivileges` (sudo and setuid programs no
  longer work from the app), `ProtectSystem=strict` with only Next.js's
  runtime cache writable, `PrivateTmp`, `ProtectHome` (unless the app lives
  under `/home`), and the kernel, device, clock, namespace and address-family
  restrictions.
- The deploy user owns the code and the app user's group can only read it, so
  a compromised app can't change its own code. Group write bits left by
  Ubuntu's umask are removed on every deploy.

### Added

- `app_user` per environment.
- `landfall check` reports the service's user and whether `NoNewPrivileges`
  is on.

### Changed

- Local-build uploads no longer set group ownership (`rsync --no-group`) and
  leave the running app's cache in place instead of deleting it.

## [0.2.0] - 2026-10-13

### Upgrading from 0.1.x

Run `landfall redis bootstrap --env <env>` once per server that uses Redis.
It adds an `aclfile`, puts a password on Redis's `default` user and removes
admin commands from the environment's user, without changing the
environment's password. If `landfall check` then reports the ACL user as
missing (0.1.x lost it at the first Redis restart and could not tell), run
`landfall redis bootstrap --env <env> --reset-password --save-secret` and
deploy.

### Added

- Local builds: `build = "local"` (or `deploy --build local`) compiles the
  app on the machine running the CLI and uploads it with rsync, instead of
  building on the VPS. Requires the same OS, CPU and Node.js major version as
  the server (`--allow-node-mismatch` overrides the Node check).
- `release_command`: a per-environment command run on the VPS after the build
  is in place and before the restart, for migrations. `LANDFALL_RELEASE=1` and
  `LANDFALL_BUILD` are exported to the build.
- `redis status` and `check` report whether Redis's `default` user is
  password protected and whether ACL users are persisted.

### Security

- Redis's built-in `default` user had no password, so any process on the VPS
  could use Redis as an administrator. `redis bootstrap` now sets one, stored
  on the server in `/etc/landfall/redis-admin-password` (root only).
- Environment ACL users no longer get admin commands (`+@all -@admin`).
- Redis passwords are sent to `redis-cli` on stdin or through the
  environment instead of on the command line, where other users on the host
  could see them in the process list.
- Database dumps were written to a predictable, world-readable file in `/tmp`
  before being moved into the backup directory. They are now streamed
  straight into the root-only directory.
- The scheduled-backup installer wrote the S3 keys through an unquoted
  heredoc, so a key containing `$` or a backtick would have been expanded or
  executed. The backup script is now installed through a quoted heredoc.
- Git refs passed to `deploy --ref` are validated, so a value starting with
  `-` can no longer be read by git as an option.

### Fixed

- Redis ACL users were never persisted: `redis bootstrap` ran `ACL SAVE`
  without an `aclfile` configured and ignored the failure, so the app's user
  vanished at the next Redis restart. Bootstrap now sets
  `aclfile /var/lib/redis/users.acl` and fails if the save does not succeed.
- `redis bootstrap`, `redis status` and `check` tested for the ACL user with
  `ACL GETUSER`, which exits 0 for a user that does not exist, so a missing
  user was always reported as present. They now match the name in
  `ACL USERS`.
- `redis bootstrap` tested for `redis.conf` without sudo, which always failed
  because `/etc/redis` is private to the redis user, so its config step never
  ran. It also restarted Redis on every run; it now restarts only when it
  changed the config.
- `db backup` reported failure after writing the dump, `db backups` always
  listed none, and `db restore` and `db backup --upload` could not read the
  dump, all because the backup directory is root-only. `db schedule --upload`
  failed while installing its script.
- `check --env <env>` failed on secrets that only other environments use.

### Changed

- `bootstrap` installs Node.js 22 instead of 20, which reached end of life in
  April 2026. Re-running `bootstrap` on an existing server upgrades Node.

## [0.1.2] - 2026-08-26

### Security

- SSH client used `InsecureIgnoreHostKey()`, accepting any server key with
  no verification and leaving every connection open to MITM. Now verifies
  against `~/.ssh/known_hosts` with trust-on-first-use, and rejects a host
  whose key has changed.
- `.env.production` values were only escaped for double-quoted shell
  strings, so a secret containing `$`, `` ` ``, or `\` could be expanded or
  executed when `buildApp` sourced the file with bash. Values are now
  single-quoted, which bash treats as fully literal.

## [0.1.1] - 2026-08-20

### Fixed

- `landfall version` reports the module version when installed via `go install`
  (falls back to `runtime/debug` build info when ldflags are absent)

## [0.1.0] - 2026-08-20

First recommended public release. Dogfooded on a production Next.js app.
This is an early cut — not a stability-guaranteed 1.0.

### Added

- `landfall` CLI: init, bootstrap, deploy, status, logs, check
- Local secrets store (`~/.config/landfall/secrets.toml`) with check/set/get/list/delete
- Ubuntu security hardening and status
- Optional Caddy HTTPS during bootstrap
- PostgreSQL bootstrap/status plus local backup, list, restore, and daily schedule
- Redis bootstrap/status with ACL users
- Dual config compatibility: `landfall.toml` or legacy `vpsdeploy.toml`;
  `~/.config/landfall` or legacy `~/.config/vpsdeploy`
- `landfall version` and release binaries (Linux amd64/arm64)

### Supported

Core path: `init` → `security harden` → `bootstrap` → optional `db`/`redis`
bootstrap → `secrets` → `deploy` → `check` / `status` / `logs`, plus local DB backups.

### Experimental / known limitations

- `db replica bootstrap` and `db pooler` — available but require careful review / manual auth finish
- S3 backup upload — optional; verify credentials before relying on off-site copies
- No zero-downtime / blue-green deploys (systemd restart + health check)
- HA failover (Patroni) is roadmap only
- Best-fit stack: Ubuntu 22.04/24.04 + Node/Next.js standalone

### Changed

- Project renamed from `vpsdeploy` / `vpsdeploymentautomation` to **Landfall**
  (`github.com/flaggx/landfall`)

[Unreleased]: https://github.com/flaggx/landfall/compare/v0.5.0...HEAD
[0.5.0]: https://github.com/flaggx/landfall/releases/tag/v0.5.0
[0.4.0]: https://github.com/flaggx/landfall/releases/tag/v0.4.0
[0.3.1]: https://github.com/flaggx/landfall/releases/tag/v0.3.1
[0.3.0]: https://github.com/flaggx/landfall/releases/tag/v0.3.0
[0.2.0]: https://github.com/flaggx/landfall/releases/tag/v0.2.0
[0.1.2]: https://github.com/flaggx/landfall/releases/tag/v0.1.2
[0.1.1]: https://github.com/flaggx/landfall/releases/tag/v0.1.1
[0.1.0]: https://github.com/flaggx/landfall/releases/tag/v0.1.0
