# Security Policy

## Supported versions

Security fixes are applied on the latest tagged release on `main` (currently
the `v0.1.x` line).

## Reporting a vulnerability

Please report security issues **privately**:

1. Prefer [GitHub Security Advisories](https://github.com/flaggx/landfall/security/advisories/new)
   (Private vulnerability reporting) on this repository, or
2. Contact the maintainer via the email on their GitHub profile.

Do **not** open a public issue for vulnerabilities.

Do **not** include production secrets, private keys, connection strings, or
customer data in reports. Describe impact and reproduction with placeholders.

## Private configuration

`landfall.toml` with real hostnames/IPs belongs in your **application**
repository (or stays untracked locally). Never commit it to this tooling
repository.

Secrets live only in `~/.config/landfall/secrets.toml` (or legacy
`~/.config/vpsdeploy/secrets.toml`) on your machine and are injected at deploy
time — they must never be committed.

Hardening commands (`landfall security harden`) can lock you out of SSH if
used incorrectly (for example disabling password auth before keys work). Test
access carefully.

## Local builds

With `build = "local"` the app is compiled on your machine instead of the VPS.
Landfall limits what that exposes:

- Environment values, including secrets, are passed to the build through its
  process environment. They are not written to disk on your machine and are
  never part of the upload; the server still receives them only as
  `.env.production` over Landfall's own SSH connection.
- `npm ci` runs without those values in its environment. Only `npm run build`
  receives them.
- The build runs in a private clone of the exact pushed commit, not your
  working tree, so local files cannot be uploaded by accident.
- The upload uses your system `ssh` with `StrictHostKeyChecking=yes`, so it
  only connects to a host key already in `known_hosts`. Symlinks that point
  outside the build are not sent.
- Git refs, the host, the user and the key path are validated before they are
  handed to `git`, `rsync` or `ssh`, and none of them pass through a shell.

What a local build does **not** protect against: dependency install and build
scripts run on your machine with your user's access (SSH keys, other
projects, the secrets file) instead of on the VPS. Build remotely, or in a
dedicated machine or container, if you do not trust a project's dependencies
with that.
