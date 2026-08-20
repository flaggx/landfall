# Changelog

All notable changes to Landfall are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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

[0.1.0]: https://github.com/flaggx/landfall/releases/tag/v0.1.0
