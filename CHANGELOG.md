# Changelog

All notable changes to Landfall are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- Redis ACL users were never persisted: `redis bootstrap` ran `ACL SAVE`
  without an `aclfile` configured and ignored the failure, so the app's user
  vanished at the next Redis restart. Bootstrap now sets
  `aclfile /var/lib/redis/users.acl` and fails if the save does not succeed.
- `redis bootstrap`, `redis status` and `check` tested for the ACL user with
  `ACL GETUSER`, which exits 0 for a user that does not exist, so a missing
  user was always reported as present. They now match the name in
  `ACL USERS`.
- `redis bootstrap` restarted Redis on every run; it now restarts only when
  it changed the config.

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

[0.1.2]: https://github.com/flaggx/landfall/releases/tag/v0.1.2
[0.1.1]: https://github.com/flaggx/landfall/releases/tag/v0.1.1
[0.1.0]: https://github.com/flaggx/landfall/releases/tag/v0.1.0
