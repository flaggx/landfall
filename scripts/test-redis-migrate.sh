#!/usr/bin/env bash
# End-to-end test of `landfall redis migrate` against a real VPS.
#
# Moves one NON-PROD environment from the shared Redis (port 6379) to its own
# instance and checks every step: the copy (with a canary key), the switch of
# REDIS_URL, the app's downtime, a restart of the new instance, and a normal
# deploy afterwards. It changes that environment for real; on failure it
# prints how to roll back (the shared Redis keeps the old data).
#
# Usage:
#   scripts/test-redis-migrate.sh --env dev --project-dir ../my-app
#
# Options:
#   --env NAME          environment to migrate (default: dev; prod refused
#                       unless --allow-prod)
#   --project-dir DIR   directory with landfall.toml (default: .)
#   --landfall PATH     landfall binary to test (default: build this checkout
#                       into bin/landfall-pr2)
#   --skip-deploy       don't run a deploy at the end
#   --allow-prod        allow --env prod/production
#   -y, --yes           don't ask for confirmation
#
# Needs: go (unless --landfall), python3 (3.11+), ssh, and the SSH key
# Landfall uses. The deploy user needs passwordless sudo, as for Landfall.

set -euo pipefail

ENV_NAME=dev
PROJECT_DIR=.
LANDFALL=""
SKIP_DEPLOY=false
ALLOW_PROD=false
YES=false
REPO_DIR=$(cd "$(dirname "$0")/.." && pwd)

while [ $# -gt 0 ]; do
  case "$1" in
    --env) ENV_NAME=$2; shift 2 ;;
    --project-dir) PROJECT_DIR=$2; shift 2 ;;
    --landfall) LANDFALL=$2; shift 2 ;;
    --skip-deploy) SKIP_DEPLOY=true; shift ;;
    --allow-prod) ALLOW_PROD=true; shift ;;
    -y|--yes) YES=true; shift ;;
    -h|--help) sed -n '2,25p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown option: $1 (see --help)" >&2; exit 2 ;;
  esac
done

if { [ "$ENV_NAME" = prod ] || [ "$ENV_NAME" = production ]; } && [ "$ALLOW_PROD" != true ]; then
  echo "Refusing to test on $ENV_NAME: run this on a non-prod environment first (or pass --allow-prod)." >&2
  exit 2
fi

# --- output helpers ---
if [ -t 1 ]; then G=$'\e[32m'; R=$'\e[31m'; Y=$'\e[33m'; B=$'\e[1m'; N=$'\e[0m'; else G=; R=; Y=; B=; N=; fi
RESULTS=()
step() { printf '\n%s== %s ==%s\n' "$B" "$1" "$N"; }
pass() { printf '%s  PASS%s %s\n' "$G" "$N" "$1"; RESULTS+=("PASS  $1"); }
warn() { printf '%s  WARN%s %s\n' "$Y" "$N" "$1"; RESULTS+=("WARN  $1"); }
FAILED=false
fail() { printf '%s  FAIL%s %s\n' "$R" "$N" "$1"; RESULTS+=("FAIL  $1"); FAILED=true; }
die() { fail "$1"; finish; }

SECRET="${ENV_NAME}_redis_url"
WORK=$(mktemp -d)
chmod 700 "$WORK"
BACKUP="$WORK/old_redis_url"
MIGRATED=false

finish() {
  [ -n "${MONITOR_PID:-}" ] && kill "$MONITOR_PID" 2>/dev/null || true
  printf '\n%s== Summary ==%s\n' "$B" "$N"
  printf '  %s\n' "${RESULTS[@]}"
  if [ "$FAILED" = true ]; then
    printf '\n%sThe test failed.%s Logs and output are in %s\n' "$R" "$N" "$WORK"
    if [ "$MIGRATED" = true ] || [ -s "$BACKUP" ]; then
      cat <<EOF

To roll back to the shared Redis (it still has the old data and user):
  $LANDFALL --project-dir $(printf %q "$PROJECT_DIR") secrets set $SECRET --value "\$(cat $BACKUP)"
  $LANDFALL --project-dir $(printf %q "$PROJECT_DIR") deploy --env $ENV_NAME
The old URL is kept in $BACKUP (mode 600); delete it when you are done.
EOF
    fi
    exit 1
  fi
  printf '\n%sAll checks passed.%s\n' "$G" "$N"
  cat <<EOF
Next:
  - Use the app on $ENV_NAME for a bit.
  - Remove the old user from the shared Redis: $LANDFALL redis status --env $ENV_NAME (shows the command)
  - Delete the saved old URL: rm -r $WORK
EOF
  exit 0
}
trap 'fail "unexpected error on line $LINENO"; finish' ERR

# --- landfall binary ---
step "Build"
if [ -z "$LANDFALL" ]; then
  LANDFALL="$REPO_DIR/bin/landfall-pr2"
  ( cd "$REPO_DIR" && go build -o "$LANDFALL" ./cmd/landfall )
  pass "built $(cd "$REPO_DIR" && git rev-parse --short HEAD) ($(cd "$REPO_DIR" && git branch --show-current)) into $LANDFALL"
fi
lf() { "$LANDFALL" --project-dir "$PROJECT_DIR" "$@"; }
lf redis migrate --help >/dev/null 2>&1 || die "$LANDFALL has no 'redis migrate' (is this the PR #2 branch?)"

# --- what the test needs from the config ---
CONF_JSON=$(python3 - "$PROJECT_DIR" "$ENV_NAME" <<'PY'
import json, os, sys, tomllib
proj, env = sys.argv[1], sys.argv[2]
path = next((p for p in (os.path.join(proj, n) for n in ("landfall.toml", "vpsdeploy.toml")) if os.path.exists(p)), None)
if not path:
    sys.exit(f"no landfall.toml in {proj}")
cfg = tomllib.load(open(path, "rb"))
e = cfg.get("environments", {}).get(env)
if e is None:
    sys.exit(f"environment {env!r} not in {path}")
key = os.path.expanduser("~/.ssh/id_ed25519")
for d in ("landfall", "vpsdeploy"):
    g = os.path.expanduser(f"~/.config/{d}/config.toml")
    if os.path.exists(g):
        key = tomllib.load(open(g, "rb")).get("ssh_key_path") or key
        break
print(json.dumps({"host": e["host"], "user": e["user"], "path": e["path"], "health": e.get("health_check", ""),
                  "service": f'{cfg["project"]["name"]}-{env}', "key": os.path.expanduser(key),
                  "uses_secret": any("{{secret:%s_redis_url}}" % env in str(v) for v in e.get("env", {}).values())}))
PY
) || die "could not read the config"
cfg() { python3 -c 'import json,sys; print(json.loads(sys.argv[1])[sys.argv[2]])' "$CONF_JSON" "$1"; }
HOST=$(cfg host); SSH_USER=$(cfg user); DEPLOY_PATH=$(cfg path); HEALTH=$(cfg health); SERVICE=$(cfg service); KEY=$(cfg key)
[ "$(cfg uses_secret)" = True ] || die "environments.$ENV_NAME.env must use {{secret:$SECRET}} for REDIS_URL"
remote() { ssh -i "$KEY" -o BatchMode=yes -o StrictHostKeyChecking=yes "$SSH_USER@$HOST" "$@"; }

# url_part URL user|pass|port|db
url_part() {
  python3 -c '
import sys, urllib.parse as u
p = u.urlparse(sys.argv[1])
print({"user": p.username or "", "pass": u.unquote(p.password or ""), "port": p.port or 6379, "db": (p.path.strip("/") or "0")}[sys.argv[2]])' "$1" "$2"
}
# rcli_url URL ARGS...: redis-cli on the server with the URL's credentials,
# the password passed through the environment (not on a command line).
rcli_url() {
  local url=$1; shift
  printf '%s' "$(url_part "$url" pass)" | remote "REDISCLI_AUTH=\$(cat) redis-cli --no-auth-warning --user $(printf %q "$(url_part "$url" user)") -p $(url_part "$url" port) -n $(url_part "$url" db) $(printf '%q ' "$@")"
}

echo "Environment $ENV_NAME on $SSH_USER@$HOST, service $SERVICE"
if [ "$YES" != true ]; then
  printf '%sThis migrates %s to its own Redis instance for real, with a few seconds of downtime.%s Continue? [y/N] ' "$Y" "$ENV_NAME" "$N"
  read -r answer
  [ "$answer" = y ] || [ "$answer" = Y ] || { echo "Aborted."; exit 0; }
fi

# --- 1. before ---
step "1. Before: on the shared Redis"
remote true || die "cannot SSH to $SSH_USER@$HOST with $KEY"
pass "SSH to $SSH_USER@$HOST"
lf secrets get "$SECRET" --reveal > "$BACKUP" 2>/dev/null || die "secret $SECRET is not set locally"
chmod 600 "$BACKUP"
OLD_URL=$(cat "$BACKUP")
[ "$(url_part "$OLD_URL" port)" = 6379 ] || die "$SECRET points at port $(url_part "$OLD_URL" port), not the shared Redis (6379): nothing to migrate"
pass "saved the old URL to $BACKUP (shared Redis, DB $(url_part "$OLD_URL" db))"

lf check --env "$ENV_NAME" > "$WORK/check-before.txt" 2>&1 || true
if grep -q 'on the shared Redis' "$WORK/check-before.txt"; then pass "check warns: on the shared Redis"
else warn "check did not warn about the shared Redis (see $WORK/check-before.txt)"; fi
lf redis status --env "$ENV_NAME" > "$WORK/status-before.txt" 2>&1 || true
INSTANCE=$(sed -n 's/^Instance: *\([^ ]*\) .*/\1/p' "$WORK/status-before.txt" | head -n1)
[ -n "$INSTANCE" ] || die "could not read the instance name from redis status (see $WORK/status-before.txt)"
pass "instance will be $INSTANCE"

OLD_KEYS=$(rcli_url "$OLD_URL" DBSIZE) || die "the old URL does not work against the shared Redis"
CANARY="landfall:migrate-canary:$(date +%s)"
CANARY_VALUE="ok-$RANDOM-$RANDOM"
[ "$(rcli_url "$OLD_URL" SET "$CANARY" "$CANARY_VALUE" EX 3600)" = OK ] || die "could not write the canary key with the old URL"
pass "old DB has $OLD_KEYS key(s); wrote canary $CANARY"

# --- 2. migrate, while watching the app's health ---
step "2. Migrate"
if [ -n "$HEALTH" ]; then
  # Poll the health endpoint on the server every 0.5s and log up/down.
  remote "while true; do if curl -fsS --max-time 2 $(printf %q "$HEALTH") 2>/dev/null | grep -q '\"ok\"[[:space:]]*:[[:space:]]*true'; then echo up; else echo down; fi; sleep 0.5; done" > "$WORK/health.log" 2>/dev/null &
  MONITOR_PID=$!
  sleep 2
fi
START=$(date +%s)
if lf redis migrate --env "$ENV_NAME" 2>&1 | tee "$WORK/migrate.txt"; then
  MIGRATED=true
  pass "migrate finished in $(( $(date +%s) - START ))s"
else
  grep -q 'stop app' "$WORK/migrate.txt" && MIGRATED=true
  die "migrate failed (output in $WORK/migrate.txt)"
fi
if [ -n "${MONITOR_PID:-}" ]; then
  sleep 3
  kill "$MONITOR_PID" 2>/dev/null || true
  wait "$MONITOR_PID" 2>/dev/null || true
  MONITOR_PID=""
  DOWN=$(grep -c '^down$' "$WORK/health.log" || true)
  if [ "$(tail -n1 "$WORK/health.log")" != up ]; then
    fail "health endpoint is not up after the migration (see $WORK/health.log)"
  elif [ "$DOWN" -le 20 ]; then
    pass "app was down for about $(( DOWN / 2 ))s (health polled every 0.5s)"
  else
    warn "app was down for about $(( DOWN / 2 ))s, longer than expected"
  fi
else
  warn "no health_check configured: downtime not measured"
fi
grep -q 'Copied [0-9]* key(s)' "$WORK/migrate.txt" && pass "$(grep -o 'Copied [0-9]* key(s)[^)]*)' "$WORK/migrate.txt")"

# --- 3. after ---
step "3. After: on its own instance"
NEW_URL=$(lf secrets get "$SECRET" --reveal)
[ "$NEW_URL" != "$OLD_URL" ] || die "secret $SECRET was not changed"
NEW_PORT=$(url_part "$NEW_URL" port)
[ "$NEW_PORT" != 6379 ] || die "the new URL still points at the shared Redis"
pass "secret now points at port $NEW_PORT"

[ "$(rcli_url "$NEW_URL" GET "$CANARY")" = "$CANARY_VALUE" ] && pass "canary key was copied" || fail "canary key missing in the new instance"
NEW_KEYS=$(rcli_url "$NEW_URL" DBSIZE)
[ "$NEW_KEYS" -ge "$OLD_KEYS" ] && pass "new instance has $NEW_KEYS key(s) (old DB had $OLD_KEYS, plus the canary)" || fail "new instance has $NEW_KEYS key(s), fewer than the old DB's $OLD_KEYS"
[ "$(rcli_url "$OLD_URL" GET "$CANARY")" = "$CANARY_VALUE" ] && pass "the shared Redis still has its copy (rollback possible)" || warn "the shared Redis no longer has the canary"

if remote "grep -q '^REDIS_URL=.*:$NEW_PORT/' $(printf %q "$DEPLOY_PATH/.env.production")"; then
  pass "the server's .env.production uses the new instance"
else
  fail "the server's .env.production does not point at port $NEW_PORT"
fi
lf check --env "$ENV_NAME" > "$WORK/check-after.txt" 2>&1 || true
grep -q "own instance $INSTANCE" "$WORK/check-after.txt" && pass "check: own instance $INSTANCE" || fail "check does not report the own instance (see $WORK/check-after.txt)"
grep -q 'redis default user .*password protected' "$WORK/check-after.txt" && pass "check: default user locked" || fail "check: default user not reported as locked"
lf redis status --env "$ENV_NAME" > "$WORK/status-after.txt" 2>&1 || true
grep -q 'Default user locked: yes' "$WORK/status-after.txt" && pass "status: default user locked" || fail "status does not show a locked default user"
MEM=$(sed -n 's/^Memory: *//p' "$WORK/status-after.txt")
[ -n "$MEM" ] && pass "status: memory $MEM" || warn "status shows no memory line"
remote "systemctl is-active --quiet redis-server@$INSTANCE" && pass "redis-server@$INSTANCE is active" || fail "redis-server@$INSTANCE is not active"
remote "systemctl is-enabled --quiet redis-server@$INSTANCE" && pass "redis-server@$INSTANCE is enabled (starts on boot)" || fail "redis-server@$INSTANCE is not enabled"

# --- 4. restart the instance ---
step "4. Restart the instance"
remote "sudo systemctl restart redis-server@$INSTANCE"
ok=false
for _ in $(seq 1 30); do
  [ "$(rcli_url "$NEW_URL" GET "$CANARY" 2>/dev/null)" = "$CANARY_VALUE" ] && { ok=true; break; }
  sleep 1
done
[ "$ok" = true ] && pass "users and data survive a restart" || fail "canary or user lost after restarting redis-server@$INSTANCE"
if [ -n "$HEALTH" ]; then
  ok=false
  for _ in $(seq 1 30); do
    remote "curl -fsS --max-time 2 $(printf %q "$HEALTH")" 2>/dev/null | grep -q '"ok"[[:space:]]*:[[:space:]]*true' && { ok=true; break; }
    sleep 1
  done
  [ "$ok" = true ] && pass "app healthy after the Redis restart" || fail "app not healthy after the Redis restart"
fi

# --- 5. a normal deploy uses the new secret ---
if [ "$SKIP_DEPLOY" = true ]; then
  warn "deploy skipped (--skip-deploy)"
else
  step "5. Deploy"
  if lf deploy --env "$ENV_NAME" > "$WORK/deploy.txt" 2>&1; then
    pass "deploy succeeded"
    [ "$(rcli_url "$NEW_URL" GET "$CANARY")" = "$CANARY_VALUE" ] && pass "data still there after the deploy" || fail "canary missing after the deploy"
  else
    fail "deploy failed (see $WORK/deploy.txt)"
  fi
fi

# --- clean up the canary ---
rcli_url "$NEW_URL" DEL "$CANARY" >/dev/null 2>&1 || true
rcli_url "$OLD_URL" DEL "$CANARY" >/dev/null 2>&1 || true
trap - ERR
finish
