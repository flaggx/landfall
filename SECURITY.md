# Security Policy

## Reporting a vulnerability

Please report security issues privately by emailing the maintainer via GitHub
(see the profile on the repository) rather than opening a public issue.

Do **not** include production secrets, private keys, or customer data in reports.

## Private configuration

`vpsdeploy.toml` with real hostnames/IPs belongs in your **application** repository
(or stays untracked locally). Never commit it to this tooling repository.

Secrets live only in `~/.config/vpsdeploy/secrets.toml` on your machine and are
injected at deploy time — they must never be committed.
