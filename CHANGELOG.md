# Changelog

All notable changes to Landfall are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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

[0.3.1]: https://github.com/flaggx/landfall/releases/tag/v0.3.1
[0.3.0]: https://github.com/flaggx/landfall/releases/tag/v0.3.0
[0.2.0]: https://github.com/flaggx/landfall/releases/tag/v0.2.0
[0.1.2]: https://github.com/flaggx/landfall/releases/tag/v0.1.2
[0.1.1]: https://github.com/flaggx/landfall/releases/tag/v0.1.1
[0.1.0]: https://github.com/flaggx/landfall/releases/tag/v0.1.0
