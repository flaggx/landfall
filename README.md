# vpsdeploy

Deploy Git repos to a VPS from your local machine — similar to the core loop of Vercel or Supabase, but triggered manually via CLI.

## Features

- **Manual deploys** — `vpsdeploy deploy --env prod` pulls latest code, builds, and restarts
- **Multi-environment** — prod and dev on the same VPS (or different hosts)
- **Build on VPS** — avoids macOS/Linux native module mismatches for Node/Next.js
- **systemd** — reliable process management with auto-restart
- **Optional Caddy** — HTTPS reverse proxy via `bootstrap --caddy`
- **Secrets** — inject env vars from a local secrets file, never committed to git

---

## Install

Requires Go 1.22+.

```bash
git clone git@github.com:flaggx/vpsdeploymentautomation.git
cd vpsdeploymentautomation
make install    # builds and copies to ~/bin/vpsdeploy
```

Or build manually:

```bash
go build -o ~/bin/vpsdeploy ./cmd/vpsdeploy/
```

Make sure `~/bin` is on your `PATH`, then verify:

```bash
vpsdeploy --help
```

**Makefile targets:** `make build`, `make install`, `make test`, `make vet`, `make fmt`, `make clean`

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup.

---

## Usage

### First-time setup (do once)

This is the full path from zero to a live deploy.

#### Step 1: Prepare your Next.js app

In the app you want to deploy, enable standalone output:

```js
/** @type {import('next').NextConfig} */
const nextConfig = {
  output: 'standalone',
};

module.exports = nextConfig;
```

Add a health endpoint that returns HTTP 200, for example `app/api/health/route.ts`:

```ts
export async function GET() {
  return Response.json({ ok: true });
}
```

Commit and push those changes to GitHub before deploying.

#### Step 2: Create project config

From your **webapp repo** (not this tool repo):

```bash
cd /path/to/your-webapp
vpsdeploy init
```

Answer the prompts (VPS IP, deploy user, paths, ports, branches). This writes `vpsdeploy.toml` in your app repo. Commit that file — it contains no secrets.

See [templates/vpsdeploy.toml.example](templates/vpsdeploy.toml.example) for the full format.

#### Step 3: Set up secrets (optional)

If your app needs secrets (database URLs, API keys), use the built-in secrets manager:

```bash
# Create the local secrets file (~/.config/vpsdeploy/secrets.toml)
vpsdeploy secrets init

# Add secrets (prompts securely, input is hidden)
vpsdeploy secrets set prod_db_url
vpsdeploy secrets set prod_api_key

# Or pass the value directly
vpsdeploy secrets set dev_db_url --value "postgresql://user:pass@localhost:5432/mydb_dev"

# Verify everything referenced in vpsdeploy.toml is set
cd /path/to/your-webapp
vpsdeploy secrets check
```

Reference secrets in `vpsdeploy.toml`:

```toml
[environments.prod.env]
DATABASE_URL = "{{secret:prod_db_url}}"
API_KEY = "{{secret:prod_api_key}}"

[environments.dev.env]
DATABASE_URL = "{{secret:dev_db_url}}"
```

Secrets are stored locally at `~/.config/vpsdeploy/secrets.toml` (mode `0600`), resolved on your machine at deploy time, and written to the VPS as `.env.production`. They never go into git.

See [Managing secrets](#managing-secrets) for the full command reference.

Optional: if your SSH key is not the default (`~/.ssh/id_ed25519`), create `~/.config/vpsdeploy/config.toml`:

```toml
ssh_key_path = "/home/you/.ssh/my_deploy_key"
```

#### Step 4: Prepare the VPS

On a fresh Ubuntu 22.04/24.04 VPS, create a deploy user:

```bash
# SSH in as root
ssh root@your-vps-ip

adduser deploy
usermod -aG sudo deploy
echo "deploy ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/deploy
```

From your local machine, copy your SSH key so `vpsdeploy` can connect:

```bash
ssh-copy-id -i ~/.ssh/id_ed25519 deploy@your-vps-ip
```

Test the connection:

```bash
ssh deploy@your-vps-ip "echo connected"
```

#### Step 5: Bootstrap each environment

Run once per environment from your **webapp repo**:

```bash
cd /path/to/your-webapp

# Production
vpsdeploy bootstrap --env prod

# Development (add --caddy if you want HTTPS via Caddy)
vpsdeploy bootstrap --env dev --caddy
```

Bootstrap installs Node 20, git, creates deploy directories, sets up a systemd service, and generates a GitHub deploy key on the VPS.

**Important:** copy the deploy key printed at the end and add it to GitHub:

> Your webapp repo → **Settings** → **Deploy keys** → **Add deploy key** (read-only, no write access needed)

#### Step 6: Set up PostgreSQL (optional)

If your app uses a database, install PostgreSQL on the VPS and create a dedicated database per environment:

```bash
cd /path/to/your-webapp

# Prod database — installs PostgreSQL on first run, creates DB + user, saves secret
vpsdeploy db bootstrap --env prod --save-secret

# Dev database on the same VPS (reuses PostgreSQL, creates separate DB)
vpsdeploy db bootstrap --env dev --save-secret

# Check status
vpsdeploy db status --env prod
```

This creates a database like `my_webapp_prod` with a local-only connection string:

```
postgresql://my_webapp_prod:<password>@localhost:5432/my_webapp_prod
```

The connection string is saved to your local secrets as `prod_db_url` / `dev_db_url` when using `--save-secret`. Your app receives it as `DATABASE_URL` at deploy time via `vpsdeploy.toml`:

```toml
[environments.prod.env]
DATABASE_URL = "{{secret:prod_db_url}}"
```

PostgreSQL listens on `localhost` only — it is not exposed to the internet. Only your app on the same VPS can connect.

See [PostgreSQL on the VPS](#postgresql-on-the-vps) for more options.

#### Step 7: First deploy

```bash
vpsdeploy deploy --env prod
```

You should see output like:

```
Deploying my-webapp to prod (deploy@203.0.113.10)...
→ preflight
→ sync
→ env
→ build
→ activate
→ restart
→ health
Deploy succeeded in 2m15s (commit a1b2c3d)
```

If using Caddy with a domain, point your DNS A record at the VPS IP before visiting the site.

---

### Day-to-day use

Once setup is done, deploying is a single command from your webapp repo:

```bash
cd /path/to/your-webapp

# Deploy latest from the configured branch
vpsdeploy deploy --env prod
vpsdeploy deploy --env dev

# Deploy a specific commit, tag, or branch
vpsdeploy deploy --env prod --ref abc1234
vpsdeploy deploy --env prod --ref v1.2.0

# Check what's running
vpsdeploy status --env prod

# View logs
vpsdeploy logs --env prod
vpsdeploy logs --env prod -f    # follow live
```

#### Typical workflow

```bash
# 1. Make changes locally
git add .
git commit -m "Add new feature"
git push origin main

# 2. Deploy to dev first
vpsdeploy deploy --env dev

# 3. Verify dev looks good
vpsdeploy status --env dev
vpsdeploy logs --env dev

# 4. Deploy to prod
vpsdeploy deploy --env prod
```

#### Rollback to a previous commit

```bash
# Find the commit you want
git log --oneline

# Deploy that specific commit
vpsdeploy deploy --env prod --ref abc1234
```

---

## Commands reference

| Command | Description |
|---------|-------------|
| `vpsdeploy init` | Interactive wizard to create `vpsdeploy.toml` |
| `vpsdeploy bootstrap --env prod` | One-time VPS setup for an environment |
| `vpsdeploy bootstrap --env prod --caddy` | Bootstrap + install Caddy reverse proxy |
| `vpsdeploy deploy --env prod` | Pull, build, restart, health check |
| `vpsdeploy deploy --env prod --ref <ref>` | Deploy a specific git ref |
| `vpsdeploy status --env prod` | Show service status and deployed commit |
| `vpsdeploy logs --env prod` | Show recent systemd logs |
| `vpsdeploy logs --env prod -f` | Follow logs live |
| `vpsdeploy secrets init` | Create the local secrets file |
| `vpsdeploy secrets set <name>` | Set or update a secret |
| `vpsdeploy secrets list` | List stored secret names |
| `vpsdeploy secrets get <name>` | Show a masked secret value |
| `vpsdeploy secrets delete <name>` | Delete a secret |
| `vpsdeploy secrets check` | Verify required secrets are set |
| `vpsdeploy db bootstrap --env prod` | Install PostgreSQL and create environment database |
| `vpsdeploy db bootstrap --env prod --save-secret` | Bootstrap DB and save connection string to secrets |
| `vpsdeploy db status --env prod` | Show PostgreSQL install and database status |

**Global flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--project-dir` | `.` | Directory to search for `vpsdeploy.toml` |

---

## Managing secrets

Secrets are stored locally at `~/.config/vpsdeploy/secrets.toml` and referenced from `vpsdeploy.toml` using `{{secret:name}}` placeholders.

### Setup

```bash
vpsdeploy secrets init
```

### Add or update a secret

```bash
# Hidden prompt (recommended)
vpsdeploy secrets set prod_db_url

# Pass value directly (useful for scripts)
vpsdeploy secrets set prod_db_url --value "postgresql://user:pass@localhost:5432/mydb"

# Pipe a value in
echo "my-api-key" | vpsdeploy secrets set prod_api_key
```

### List and inspect secrets

```bash
vpsdeploy secrets list
vpsdeploy secrets get prod_db_url           # masked: po**********db
vpsdeploy secrets get prod_db_url --reveal   # full value
```

### Delete a secret

```bash
vpsdeploy secrets delete prod_db_url
vpsdeploy secrets delete prod_db_url --yes   # skip confirmation
```

### Verify before deploy

From your webapp repo, check that every `{{secret:...}}` in `vpsdeploy.toml` has a value:

```bash
cd /path/to/your-webapp
vpsdeploy secrets check
```

Example output:

```
Required secrets (2):
  ok       prod_db_url
  MISSING  prod_api_key

Set missing secrets with:
  vpsdeploy secrets set prod_api_key
```

### Reference secrets in vpsdeploy.toml

```toml
[environments.prod.env]
NODE_ENV = "production"
DATABASE_URL = "{{secret:prod_db_url}}"
STRIPE_SECRET_KEY = "{{secret:stripe_secret_key}}"
```

At deploy time, `vpsdeploy` resolves these locally and writes `.env.production` on the VPS. The secrets file itself is never uploaded or committed.

---

## PostgreSQL on the VPS

`vpsdeploy db bootstrap` installs PostgreSQL via apt (Ubuntu/Debian), creates a dedicated database and user per environment, and gives you a connection string for your app.

### Bootstrap

```bash
# First environment — installs PostgreSQL + creates database
vpsdeploy db bootstrap --env prod --save-secret

# Second environment on same VPS — creates another database
vpsdeploy db bootstrap --env dev --save-secret
```

**Flags:**

| Flag | Description |
|------|-------------|
| `--save-secret` | Save `DATABASE_URL` to local secrets (`prod_db_url`, `dev_db_url`, etc.) |
| `--reset-password` | Generate a new password for an existing database user |

### Default naming

For project `my-webapp` and env `prod`:

- Database: `my_webapp_prod`
- User: `my_webapp_prod`
- Secret: `prod_db_url`

Override in `vpsdeploy.toml`:

```toml
[environments.prod.postgres]
database = "my_webapp_prod"
user = "my_webapp_prod"
```

### Wire up your app

```toml
[environments.prod.env]
DATABASE_URL = "{{secret:prod_db_url}}"
```

Then verify and deploy:

```bash
vpsdeploy secrets check
vpsdeploy deploy --env prod
```

### Check status

```bash
vpsdeploy db status --env prod
```

### Security notes

- PostgreSQL binds to `localhost` only (default Ubuntu install)
- Database credentials live in your local secrets file and `.env.production` on the VPS
- Use separate databases for prod and dev on the same VPS
- Do not expose port 5432 in your firewall

---

## Configuration

### File locations

| File | Location | Committed to git? |
|------|----------|-------------------|
| `vpsdeploy.toml` | Your webapp repo root | Yes |
| `secrets.toml` | `~/.config/vpsdeploy/secrets.toml` | No — local only |
| `config.toml` | `~/.config/vpsdeploy/config.toml` | No — local only |

### vpsdeploy.toml example

```toml
[project]
name = "my-webapp"
repo = "git@github.com:you/my-webapp.git"

[environments.prod]
host = "203.0.113.10"
user = "deploy"
path = "/var/www/my-webapp-prod"
branch = "main"
port = 3000
domain = "app.example.com"                          # optional, for Caddy
health_check = "http://127.0.0.1:3000/api/health"   # optional

[environments.prod.env]
NODE_ENV = "production"
DATABASE_URL = "{{secret:prod_db_url}}"

[environments.dev]
host = "203.0.113.10"
user = "deploy"
path = "/var/www/my-webapp-dev"
branch = "develop"
port = 3001
domain = "dev.app.example.com"
health_check = "http://127.0.0.1:3001/api/health"

[environments.dev.env]
NODE_ENV = "development"
```

**Required fields per environment:** `host`, `user`, `path`, `branch`, `port`

---

## How it works

```mermaid
sequenceDiagram
    participant CLI as vpsdeploy CLI
    participant VPS as VPS
    participant GH as GitHub

    CLI->>VPS: SSH connect
    CLI->>VPS: git fetch + checkout ref
    VPS->>GH: pull latest (deploy key)
    CLI->>VPS: npm ci && npm run build
    CLI->>VPS: systemctl restart + health check
    VPS-->>CLI: deploy result
```

Each deploy runs these steps remotely:

1. **Preflight** — verify git, node, npm, disk space
2. **Sync** — clone or fetch latest from GitHub
3. **Env** — write `.env.production` from config + secrets
4. **Build** — `npm ci && npm run build`
5. **Activate** — copy Next.js static assets, write `.deploy-meta`
6. **Restart** — `systemctl restart <app>-<env>`
7. **Health check** — curl configured endpoint with retries

### VPS layout after bootstrap

```
/var/www/my-webapp-prod/              # prod git checkout
/var/www/my-webapp-dev/               # dev git checkout
/etc/systemd/system/my-webapp-prod.service
/etc/systemd/system/my-webapp-dev.service
/etc/caddy/vpsdeploy/my-webapp-prod.caddy   # if using --caddy
~/.ssh/id_ed25519                         # deploy key (on VPS)
```

---

## Troubleshooting

| Problem | Fix |
|---------|-----|
| SSH connection fails | Check `ssh_key_path` in `~/.config/vpsdeploy/config.toml`. Test with `ssh deploy@your-vps-ip`. |
| Git clone fails on VPS | Add the deploy key from `bootstrap` to GitHub deploy keys. |
| Build fails | SSH in manually: `cd /var/www/my-webapp-prod && npm ci && npm run build` |
| Health check fails | Confirm the health endpoint exists and returns 200. Check logs: `vpsdeploy logs --env prod` |
| systemd restart fails | Ensure deploy user has passwordless sudo. Check: `sudo systemctl status my-webapp-prod` |
| Wrong Node version | Bootstrap installs Node 20. Verify: `node --version` on VPS should show `v20.x` |
| Port already in use | Change `port` in `vpsdeploy.toml`, re-run `bootstrap`, then `deploy` |

---

## License

MIT
