# vpsdeploy

**Open-source CLI to deploy Git apps to your own VPS** — the convenience of a managed platform (deploy, secrets, Postgres, Redis, HTTPS, backups) without locking into a big cloud.

MIT licensed. Contributions welcome — see [CONTRIBUTING.md](CONTRIBUTING.md).

```bash
cd /path/to/your-webapp
vpsdeploy deploy --env prod
```

## Why this exists

Managed hosts are great until bills, lock-in, or limits get in the way. `vpsdeploy` is for tech-savvy developers who want:

- Push-to-deploy style workflows you control
- Secrets that never live in git
- Optional Postgres / Redis / Caddy on the same (or separate) VPS
- Backups and a clear path to scale out later

You run the CLI from your laptop (or ask your AI agent to run it). The VPS builds and runs the app under systemd.

## Table of contents

- [Install](#install)
- [Use manually](#use-manually)
- [Use with your AI agent](#use-with-your-ai-agent)
- [Day-to-day commands](#day-to-day-commands)
- [Commands reference](#commands-reference)
- [Managing secrets](#managing-secrets)
- [PostgreSQL](#postgresql-on-the-vps)
- [Redis](#redis-on-the-vps)
- [Database backups & scale](#database-backups--scale)
- [Security hardening](#vps-security-hardening)
- [Setup validation](#setup-validation-vpsdeploy-check)
- [Configuration](#configuration)
- [How it works](#how-it-works)
- [Troubleshooting](#troubleshooting)
- [Contributing](#contributing)
- [Security & license](#security--license)

---

## Install

Requires Go (see `go.mod`) and SSH access to an Ubuntu 22.04/24.04 VPS.

```bash
git clone https://github.com/flaggx/vpsdeploymentautomation.git
cd vpsdeploymentautomation
make install    # builds and copies to ~/bin/vpsdeploy
```

Or:

```bash
go build -o ~/bin/vpsdeploy ./cmd/vpsdeploy/
```

Ensure `~/bin` is on your `PATH`, then:

```bash
vpsdeploy --help
```

**Makefile:** `make build` · `make install` · `make test` · `make vet` · `make fmt` · `make clean`

---

## Use manually

### Prerequisites

- Ubuntu VPS with root SSH (first-time user setup)
- App repo on GitHub (Next.js works best with `output: 'standalone'`)
- Health endpoint that returns JSON with `"ok": true` — see [templates/health-route.ts.example](templates/health-route.ts.example)
- Domain pointed at the VPS if you want HTTPS via Caddy

### One-time setup

Run these from your **webapp** repo (not this tooling repo). Real `vpsdeploy.toml` belongs with the app.

```bash
# 1. Install the CLI (once on your laptop)
cd /path/to/vpsdeploymentautomation && make install

# 2. Create config + secrets store
cd /path/to/your-webapp
vpsdeploy init
vpsdeploy secrets init
# Commit vpsdeploy.toml to the app repo. Never commit secrets.
```

On the VPS as **root**, create a deploy user:

```bash
adduser deploy
usermod -aG sudo deploy
echo "deploy ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/deploy
chmod 440 /etc/sudoers.d/deploy
```

From your laptop:

```bash
ssh-copy-id -i ~/.ssh/id_ed25519 deploy@YOUR_VPS_IP
ssh deploy@YOUR_VPS_IP "echo connected"
```

Then, from the webapp repo:

```bash
vpsdeploy security harden --env prod
vpsdeploy bootstrap --env prod          # add printed deploy key to GitHub → Deploy keys
# optional:
vpsdeploy db bootstrap --env prod --save-secret
vpsdeploy redis bootstrap --env prod --save-secret
vpsdeploy secrets check

vpsdeploy deploy --env prod
vpsdeploy check --env prod
```

For HTTPS, bootstrap with `--caddy` and point DNS at the VPS.

### Day-to-day

```bash
cd /path/to/your-webapp
git push origin main
vpsdeploy deploy --env prod
vpsdeploy status --env prod
vpsdeploy logs --env prod -f
```

Rollback:

```bash
vpsdeploy deploy --env prod --ref <commit-or-tag>
```

---

## Use with your AI agent

`vpsdeploy` is designed so a coding agent (Cursor, Claude Code, Codex, etc.) can operate it the same way you would — from your machine, against your VPS, using your local secrets.

### What the agent needs

| Item | Notes |
|------|--------|
| CLI on `PATH` | `make install` so `vpsdeploy` works in the agent terminal |
| Webapp as the working directory | Commands expect `vpsdeploy.toml` in the app repo |
| SSH key already trusted | `ssh deploy@YOUR_VPS` works without prompts |
| Secrets already set (or agent asks you to set them) | Values live in `~/.config/vpsdeploy/secrets.toml` — **never** paste secrets into chat if you can avoid it |

### Paste this into your agent (starter prompt)

```text
You are helping me operate vpsdeploy for my webapp.

Rules:
- Run all vpsdeploy commands from the webapp repo (where vpsdeploy.toml lives).
- Never commit ~/.config/vpsdeploy/secrets.toml, .env*, or private keys.
- Never print secret values; use `vpsdeploy secrets get <name>` (masked) unless I explicitly ask to reveal.
- Prefer `vpsdeploy secrets set <name>` (interactive) over putting secrets in command lines or chat.
- Before first deploy: ensure harden → bootstrap → optional db/redis → secrets check → deploy → check.
- After code changes I push to GitHub: run `vpsdeploy deploy --env <env>` and confirm health.
- If a command fails, read `vpsdeploy logs --env <env>` and fix forward; don't force-push or wipe the VPS unless I ask.

Project path: /path/to/your-webapp
Environment: prod
```

### Suggested Cursor / project rule

Save something like this as a project rule so every chat inherits it:

```text
This app deploys with vpsdeploy (CLI). Deploy config is vpsdeploy.toml in this repo.
Secrets are local only (~/.config/vpsdeploy/secrets.toml).
To ship: commit/push, then `vpsdeploy deploy --env prod` from the repo root.
Never commit secrets or put production IPs into the public vpsdeploy tooling repo.
```

### Safe agent workflows

**Deploy latest**

```text
Push isn't needed if I already pushed. From the webapp root, run
vpsdeploy deploy --env prod and summarize success or paste the failing step + logs.
```

**First-time bring-up**

```text
Walk me through vpsdeploy first-time setup for this repo. Run non-destructive
commands yourself; stop and ask before harden, bootstrap, db bootstrap, or deploy.
```

**Add a secret**

```text
I need RESEND_API_KEY in prod. Show me the vpsdeploy.toml env entry to add,
then tell me to run: vpsdeploy secrets set resend_api_key
Do not ask me to paste the key into chat.
```

**Debug a bad deploy**

```text
vpsdeploy deploy --env prod failed. Run status + logs, identify the failing
stage (sync/build/health), and propose a fix. Don't restart services in a loop.
```

### What not to let the agent do

- Commit or push secrets, `.pem` keys, or full connection strings into git
- Put the API key in the **secret name** (name is `resend_api_key`; value is the key)
- `git push --force` to `main` / rewrite history without an explicit ask
- Expose Postgres/Redis ports on the public internet
- Dump `--reveal` secrets into the conversation transcript

---

## Day-to-day commands

| Goal | Command |
|------|---------|
| Deploy | `vpsdeploy deploy --env prod` |
| Deploy a ref | `vpsdeploy deploy --env prod --ref v1.2.0` |
| Status | `vpsdeploy status --env prod` |
| Logs | `vpsdeploy logs --env prod -f` |
| Full audit | `vpsdeploy check --env prod` |
| DB backup | `vpsdeploy db backup --env prod` |
| Schedule backups | `vpsdeploy db schedule --env prod` |

Typical loop:

```bash
git add . && git commit -m "…" && git push origin main
vpsdeploy deploy --env dev    # optional
vpsdeploy deploy --env prod
```

---

## Commands reference

| Command | Description |
|---------|-------------|
| `vpsdeploy init` | Create `vpsdeploy.toml` |
| `vpsdeploy bootstrap --env prod` | One-time VPS app setup |
| `vpsdeploy bootstrap --env prod --caddy` | Bootstrap + Caddy HTTPS |
| `vpsdeploy deploy --env prod` | Pull, build, restart, health check |
| `vpsdeploy deploy --env prod --ref <ref>` | Deploy a specific git ref |
| `vpsdeploy status` / `logs` | Service status and systemd logs |
| `vpsdeploy secrets …` | Local secrets store (see below) |
| `vpsdeploy db bootstrap` | Install Postgres + create DB/user |
| `vpsdeploy db backup` / `backups` / `restore` / `schedule` | Backups |
| `vpsdeploy db replica bootstrap --replica-host <ip>` | Streaming read replica |
| `vpsdeploy db pooler` | PgBouncer on the DB host |
| `vpsdeploy redis bootstrap` | Redis + ACL user |
| `vpsdeploy security harden` / `status` | Ubuntu hardening |
| `vpsdeploy check` | Full setup validation |

**Global flag:** `--project-dir` (default `.`) — directory containing `vpsdeploy.toml`.

---

## Managing secrets

Secrets live in `~/.config/vpsdeploy/secrets.toml` (mode `0600`). Reference them from the app's `vpsdeploy.toml`:

```toml
[environments.prod.env]
DATABASE_URL = "{{secret:prod_db_url}}"
```

```bash
vpsdeploy secrets init
vpsdeploy secrets set prod_db_url          # hidden prompt (preferred)
vpsdeploy secrets list
vpsdeploy secrets get prod_db_url          # masked
vpsdeploy secrets check                    # from webapp repo
vpsdeploy secrets delete prod_db_url --yes
```

At deploy time, values are resolved **on your machine** and written to the VPS as `.env.production`. They are never committed by this tool.

---

## PostgreSQL on the VPS

```bash
vpsdeploy db bootstrap --env prod --save-secret
vpsdeploy db status --env prod
```

Defaults for project `my-webapp` / env `prod`: database + user `my_webapp_prod`, secret `prod_db_url`.

```toml
[environments.prod.env]
DATABASE_URL = "{{secret:prod_db_url}}"

# Optional overrides / dedicated DB host:
# [environments.prod.postgres]
# database = "my_webapp_prod"
# user = "my_webapp_prod"
# host = "203.0.113.20"       # dedicated DB VPS
# app_host = "203.0.113.10"   # app VPS allowed in pg_hba + UFW
```

Co-located default uses `localhost` only (not exposed publicly).

---

## Redis on the VPS

```bash
vpsdeploy redis bootstrap --env prod --save-secret
vpsdeploy redis status --env prod
```

```toml
[environments.prod.env]
REDIS_URL = "{{secret:prod_redis_url}}"
```

Redis binds to `127.0.0.1`. Prod DB index `0`, dev `1` by default.

---

## Database backups & scale

### Backups

```bash
vpsdeploy db backup --env prod
vpsdeploy db backup --env prod --upload   # needs S3-compatible config + secrets
vpsdeploy db backups --env prod
vpsdeploy db restore --env prod --file /var/backups/vpsdeploy/prod/NAME.dump --yes
vpsdeploy db schedule --env prod          # daily systemd timer (UTC)
vpsdeploy db schedule --env prod --upload --hour 3
```

For uploads, set in `vpsdeploy.toml`:

```toml
[environments.prod.postgres]
backup_s3_endpoint = "https://<account>.r2.cloudflarestorage.com"
backup_s3_bucket = "my-backups"
backup_s3_prefix = "vpsdeploy/my-webapp/prod"
backup_s3_region = "auto"
```

And secrets: `backup_s3_access_key`, `backup_s3_secret_key`.

### Scale ladder (self-hosted “managed” feel)

1. **Vertical** — resize the VPS  
2. **Backups** — `db backup` / `db schedule` (+ off-site)  
3. **Dedicated DB host** — `postgres.host` + `app_host`, then `db bootstrap`  
4. **Pooler** — `vpsdeploy db pooler --env prod`  
5. **Read replica** — `vpsdeploy db replica bootstrap --env prod --replica-host <ip>`  
6. **HA failover** — roadmap (Patroni), not automated yet  

---

## VPS security hardening

```bash
vpsdeploy security harden --env prod
vpsdeploy security status --env prod
```

Configures unattended-upgrades, UFW (22/80/443), fail2ban, SSH hardening drop-in, and secret file permissions. Run once per VPS after the deploy user can SSH with a key.

Optional: `--ssh-disable-password` (only after keys work), `--auto-reboot`.

---

## Setup validation (`vpsdeploy check`)

```bash
vpsdeploy check --env prod
```

Audits secrets, SSH, hardening, Node, systemd, Postgres/Redis (if configured), and the health endpoint (`"ok": true`).

---

## Configuration

| File | Location | In git? |
|------|----------|---------|
| `vpsdeploy.toml` | **Your webapp** repo root | Yes (no secrets) |
| `secrets.toml` | `~/.config/vpsdeploy/secrets.toml` | **No** |
| `config.toml` | `~/.config/vpsdeploy/config.toml` | **No** (e.g. custom SSH key path) |

Example config: [templates/vpsdeploy.toml.example](templates/vpsdeploy.toml.example).

**Required per environment:** `host`, `user`, `path`, `branch`, `port`.

Do **not** commit real project `vpsdeploy.toml` into *this* tooling repository — only into your private/public **app** repo.

---

## How it works

```mermaid
sequenceDiagram
    participant CLI as vpsdeploy_CLI
    participant VPS as VPS
    participant GH as GitHub

    CLI->>VPS: SSH connect
    CLI->>VPS: git fetch and checkout ref
    VPS->>GH: pull via deploy key
    CLI->>VPS: write .env.production
    CLI->>VPS: npm ci and build
    CLI->>VPS: systemctl restart plus health check
    VPS-->>CLI: deploy result
```

1. Preflight → 2. Sync git → 3. Write env → 4. Build on VPS → 5. Activate → 6. Restart systemd → 7. Health check  

### VPS layout after bootstrap

```
/var/www/my-webapp-prod/
/etc/systemd/system/my-webapp-prod.service
/etc/caddy/vpsdeploy/my-webapp-prod.caddy   # if --caddy
```

---

## Troubleshooting

| Problem | Fix |
|---------|-----|
| SSH fails | Check `ssh_key_path` in `~/.config/vpsdeploy/config.toml`; test `ssh deploy@host` |
| Git clone fails on VPS | Add bootstrap deploy key to GitHub → Deploy keys |
| Build fails | SSH in: `cd /var/www/… && npm ci --include=dev && npm run build` |
| Health check fails | Confirm `/api/health` returns `"ok": true`; `vpsdeploy logs --env prod` |
| systemd restart fails | Passwordless sudo for `deploy`; `sudo systemctl status …` |
| Cloudflare `502` with HTML body | Prefer app JSON errors on `4xx`; origin `502` may be replaced by CF |

---

## Contributing

This is a community-friendly FOSS project. Bug reports, docs fixes, and features are welcome.

1. Fork and branch from `main`
2. `make fmt test vet`
3. Open a PR with a short “why”

Details: [CONTRIBUTING.md](CONTRIBUTING.md) · Issues: please redact IPs, hostnames, and secrets.

AI-assisted PRs are fine — please still run tests and review the diff yourself.

---

## Security & license

- Report vulnerabilities privately — see [SECURITY.md](SECURITY.md)
- **MIT** — see [LICENSE](LICENSE)
