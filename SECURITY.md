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
