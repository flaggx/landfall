package redis

import (
	"fmt"
	"sort"
	"strings"

	"github.com/flaggx/landfall/internal/config"
	"github.com/flaggx/landfall/internal/util"
)

// Layout is where Redis's files live on the server. Each environment runs its
// own instance through the redis-server package's template unit,
// redis-server@<instance>, which reads <EtcDir>/redis-<instance>.conf and may
// only write under LibDir, LogDir and <RunDir>/redis-<instance>.
type Layout struct {
	EtcDir   string // /etc/redis
	LibDir   string // /var/lib/redis
	LogDir   string // /var/log/redis
	RunDir   string // /run
	AdminDir string // /etc/landfall: admin passwords, root only
	// SharedPort is the host-wide Redis environments used before they got
	// their own instances (the package's default instance).
	SharedPort int
}

// DefaultLayout is the layout on a Debian or Ubuntu VPS.
var DefaultLayout = Layout{
	EtcDir:     "/etc/redis",
	LibDir:     "/var/lib/redis",
	LogDir:     "/var/log/redis",
	RunDir:     "/run",
	AdminDir:   "/etc/landfall",
	SharedPort: config.LegacySharedRedisPort,
}

// Instance is one environment's Redis instance.
type Instance struct {
	Name      string
	Unit      string // systemd unit, redis-server@<name>
	Conf      string
	DataDir   string
	ACLFile   string
	LogFile   string
	PidFile   string
	AdminFile string // password of the instance's "default" (admin) user
}

func (l Layout) Instance(name string) Instance {
	data := l.LibDir + "/" + name
	return Instance{
		Name:      name,
		Unit:      "redis-server@" + name,
		Conf:      l.EtcDir + "/redis-" + name + ".conf",
		DataDir:   data,
		ACLFile:   data + "/users.acl",
		LogFile:   l.LogDir + "/redis-server-" + name + ".log",
		PidFile:   l.RunDir + "/redis-" + name + "/redis-server.pid",
		AdminFile: l.AdminDir + "/redis-admin-" + name,
	}
}

// SharedAdminFile holds the admin password of the host-wide Redis
// (port 6379) that environments shared before they got their own instances.
func (l Layout) SharedAdminFile() string {
	return l.AdminDir + "/redis-admin-password"
}

// shellVars renders name=value assignments, shell-quoted, in a stable order.
// Scripts are a header of these plus a fixed body, so the body is plain bash
// with no format-string escaping.
func shellVars(vars map[string]string) string {
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + util.ShellQuote(vars[k]) + "\n")
	}
	return b.String()
}

func instanceVars(l Layout, inst Instance) map[string]string {
	return map[string]string{
		"INSTANCE":          inst.Name,
		"UNIT":              inst.Unit,
		"CONF":              inst.Conf,
		"DATA_DIR":          inst.DataDir,
		"ACL_FILE":          inst.ACLFile,
		"LOG_FILE":          inst.LogFile,
		"PID_FILE":          inst.PidFile,
		"REDIS_ADMIN_FILE":  inst.AdminFile,
		"ETC_DIR":           l.EtcDir,
		"LOG_DIR":           l.LogDir,
		"SHARED_PORT":       fmt.Sprint(l.SharedPort),
		"SHARED_ADMIN_FILE": l.SharedAdminFile(),
	}
}

// cliSnippet defines rcli (redis-cli as the instance's admin user, on
// $REDIS_PORT) and shared_rcli (the same for the old host-wide Redis).
// Passwords go through the environment, never on a command line.
const cliSnippet = `
rcli() {
  if sudo test -s "$REDIS_ADMIN_FILE"; then
    REDISCLI_AUTH="$(sudo cat "$REDIS_ADMIN_FILE")" redis-cli --no-auth-warning -p "$REDIS_PORT" "$@"
  else
    redis-cli -p "$REDIS_PORT" "$@"
  fi
}
shared_rcli() {
  if sudo test -s "$SHARED_ADMIN_FILE"; then
    REDISCLI_AUTH="$(sudo cat "$SHARED_ADMIN_FILE")" redis-cli --no-auth-warning -p "$SHARED_PORT" "$@"
  else
    redis-cli -p "$SHARED_PORT" "$@"
  fi
}
# instance_port prints the port in the instance's config, if it exists.
instance_port() {
  sudo awk '$1 == "port" { p = $2 } END { if (p != "") print p }' "$CONF" 2>/dev/null || true
}
# on_shared_redis: the environment's user still exists on the shared Redis.
on_shared_redis() {
  shared_rcli ACL USERS 2>/dev/null | grep -qxF "$REDIS_USER"
}
`

// versionSnippet sets the parts of the config that differ between Redis
// versions. Ubuntu 22.04 still ships 6.0:
//
//   - CHANNELS is the ACL rule granting every pub/sub channel. Channel rules
//     (&pattern) only exist from 6.2; 6.0 rejects them as a syntax error and
//     allows every channel anyway, so there it is empty.
//   - BIND_LINE listens on loopback only. The "-" prefix (don't fail if ::1 is
//     unavailable) only exists from 6.2; 6.0 refuses to start with it, so
//     there it binds IPv4 loopback only.
//   - CLIENT_RULES re-allows the CLIENT subcommands client libraries send
//     while connecting (SETNAME, GETNAME, ID). Before 7.0 the whole CLIENT
//     command is in @admin, so the app user's -@admin would block them; from
//     7.0 they are in @connection and need nothing extra.
//
// An unknown version is treated as modern (7.x defaults to resetchannels, so
// the channel rule is required there).
const versionSnippet = `
REDIS_VERSION=$( (redis-server --version 2>/dev/null || redis-cli --version 2>/dev/null) | grep -oE '[0-9]+\.[0-9]+(\.[0-9]+)?' | head -n 1 || true)
IFS=. read -r V_MAJOR V_MINOR _ <<<"${REDIS_VERSION:-7.0}"
CHANNELS='&*'
BIND_LINE='bind 127.0.0.1 -::1'
CLIENT_RULES=''
if [ "${V_MAJOR:-7}" -lt 7 ]; then
  CLIENT_RULES='+client|setname +client|getname +client|id'
fi
if [ "${V_MAJOR:-7}" -lt 6 ] || { [ "${V_MAJOR:-7}" -eq 6 ] && [ "${V_MINOR:-0}" -lt 2 ]; }; then
  CHANNELS=''
  BIND_LINE='bind 127.0.0.1'
fi
`

// waitSnippet defines wait_for_redis: wait up to 20s for Redis to answer on
// $REDIS_PORT at all (an auth error counts: it is up).
const waitSnippet = `
wait_for_redis() {
  for _ in $(seq 1 40); do
    if redis-cli -p "$REDIS_PORT" ping 2>&1 | grep -qE 'PONG|NOAUTH'; then
      return 0
    fi
    sleep 0.5
  done
  return 1
}
`

type bootstrapParams struct {
	Layout     Layout
	EnvName    string
	Instance   string
	User       string
	Port       int // 0: pick a free port
	DB         int
	Maxmemory  string
	Policy     string
	ResultPath string
	Reset      bool
	// Fresh allows creating the instance although the environment still has
	// a user (and data) on the shared Redis: migrate (which copies the data
	// afterwards) and --fresh (which starts empty) set it.
	Fresh bool
	// PortMin and PortMax bound the ports picked when Port is 0 (0: the
	// defaults). Tests narrow them.
	PortMin, PortMax int
}

func boolVar(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func buildBootstrapScript(p bootstrapParams) string {
	if p.PortMin == 0 {
		p.PortMin, p.PortMax = config.RedisInstancePortMin, config.RedisInstancePortMax
	}
	inst := p.Layout.Instance(p.Instance)
	vars := instanceVars(p.Layout, inst)
	for k, v := range map[string]string{
		"RESULT":         p.ResultPath,
		"ENV_NAME":       p.EnvName,
		"REDIS_USER":     p.User,
		"PORT_SETTING":   fmt.Sprint(p.Port),
		"PORT_MIN":       fmt.Sprint(p.PortMin),
		"PORT_MAX":       fmt.Sprint(p.PortMax),
		"REDIS_DB":       fmt.Sprint(p.DB),
		"MAXMEMORY":      p.Maxmemory,
		"POLICY":         p.Policy,
		"RESET_PASSWORD": boolVar(p.Reset),
		"FRESH":          boolVar(p.Fresh),
	} {
		vars[k] = v
	}
	return "set -euo pipefail\nexport DEBIAN_FRONTEND=noninteractive\n" + shellVars(vars) +
		cliSnippet + versionSnippet + waitSnippet + bootstrapBody
}

const bootstrapBody = `
CREATED=false
PASSWORD_ROTATED=false
DEFAULT_LOCKED=false
REDIS_PASS=""
REDIS_PORT=""
# Never let a caller read a result left over from an earlier run.
rm -f "$RESULT"

if command -v apt-get >/dev/null 2>&1; then
  if ! command -v redis-server >/dev/null 2>&1 || ! command -v redis-cli >/dev/null 2>&1; then
    sudo apt-get update -qq
    sudo apt-get install -y -qq redis-server
  fi
else
  echo "apt-get not found; install Redis manually" >&2
  exit 1
fi

# The redis-server package ships the template unit every instance runs from.
if ! systemctl cat redis-server@.service >/dev/null 2>&1; then
  echo "this redis-server package has no redis-server@.service template unit, which Landfall needs to run one Redis per environment" >&2
  exit 1
fi

# An environment that still lives on the shared Redis has data there. Creating
# an empty instance would silently drop it, so point to migrate instead.
if ! sudo test -f "$CONF" && [ "$FRESH" != true ] && on_shared_redis; then
  echo "This environment's Redis user ($REDIS_USER) is on the shared Redis (port $SHARED_PORT), along with its data." >&2
  echo "Run: landfall redis migrate --env $ENV_NAME   (gives it its own instance and copies the data)" >&2
  echo "or:  landfall redis bootstrap --env $ENV_NAME --fresh   (own instance, starting empty)" >&2
  exit 1
fi

# --- Port: the instance's own, or one picked once and kept in its config ---
used_ports() {
  sudo sh -c 'cat "$1"/redis*.conf 2>/dev/null' _ "$ETC_DIR" | awk '$1 == "port" { print $2 }'
}
port_listening() {
  (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null
}
CURRENT_PORT=$(instance_port)
if [ -n "$CURRENT_PORT" ]; then
  if [ "$PORT_SETTING" != 0 ] && [ "$PORT_SETTING" != "$CURRENT_PORT" ]; then
    echo "Instance $INSTANCE listens on port $CURRENT_PORT, but redis.port is $PORT_SETTING." >&2
    echo "Set redis.port = $CURRENT_PORT (or remove it). Moving an instance to another port is not automatic: its REDIS_URL would change." >&2
    exit 1
  fi
  REDIS_PORT=$CURRENT_PORT
elif [ "$PORT_SETTING" != 0 ]; then
  if used_ports | grep -qx "$PORT_SETTING" || port_listening "$PORT_SETTING"; then
    echo "redis.port $PORT_SETTING is already used by another Redis instance or program on this host; pick another or remove it to have one picked" >&2
    exit 1
  fi
  REDIS_PORT=$PORT_SETTING
else
  for p in $(seq "$PORT_MIN" "$PORT_MAX"); do
    if ! used_ports | grep -qx "$p" && ! port_listening "$p"; then
      REDIS_PORT=$p
      break
    fi
  done
  if [ -z "$REDIS_PORT" ]; then
    echo "no free port between $PORT_MIN and $PORT_MAX for a Redis instance; set redis.port" >&2
    exit 1
  fi
fi

# --- The instance's config, owned by Landfall ---
# The package owns $LOG_DIR (redis:adm); only create it if it is missing.
if ! sudo test -d "$LOG_DIR"; then
  sudo mkdir -p "$LOG_DIR"
  sudo chown redis:redis "$LOG_DIR"
fi
sudo mkdir -p "$DATA_DIR"
sudo chown redis:redis "$DATA_DIR"
sudo chmod 750 "$DATA_DIR"
if ! sudo test -f "$ACL_FILE"; then
  sudo touch "$ACL_FILE"
fi
sudo chown redis:redis "$ACL_FILE"
sudo chmod 640 "$ACL_FILE"

NEW_CONF=$(mktemp)
trap 'rm -f "$NEW_CONF"' EXIT
cat > "$NEW_CONF" <<EOF
# Managed by Landfall (landfall redis bootstrap). Edits are overwritten.
port $REDIS_PORT
$BIND_LINE
protected-mode yes
supervised systemd
daemonize no
pidfile $PID_FILE
logfile $LOG_FILE
dir $DATA_DIR
dbfilename dump.rdb
aclfile $ACL_FILE
databases 16
maxmemory $MAXMEMORY
maxmemory-policy $POLICY
save 3600 1
save 300 100
save 60 10000
appendonly no
EOF
CONF_CHANGED=false
if ! sudo cmp -s "$NEW_CONF" "$CONF"; then
  sudo tee "$CONF" < "$NEW_CONF" >/dev/null
  sudo chown redis:redis "$CONF"
  sudo chmod 640 "$CONF"
  CONF_CHANGED=true
fi

sudo systemctl enable "$UNIT" >/dev/null 2>&1 || true
if [ "$CONF_CHANGED" = true ] && sudo systemctl is-active --quiet "$UNIT"; then
  # Redis saves its data on a clean stop, so a restart keeps the keys.
  sudo systemctl restart "$UNIT"
else
  sudo systemctl start "$UNIT"
fi
if ! wait_for_redis; then
  echo "Redis instance $INSTANCE did not start on port $REDIS_PORT. See: sudo journalctl -u $UNIT, and $LOG_FILE" >&2
  exit 1
fi

# --- Lock the instance's "default" user ---
# A new instance's default user has no password, so any process on the host
# could use it. Passwords go to redis-cli on stdin, never on a command line.
if redis-cli -p "$REDIS_PORT" ping 2>/dev/null | grep -qx PONG; then
  if ! sudo test -s "$REDIS_ADMIN_FILE"; then
    sudo mkdir -p "$(dirname "$REDIS_ADMIN_FILE")"
    sudo chmod 700 "$(dirname "$REDIS_ADMIN_FILE")"
    ( umask 077; openssl rand -hex 32 | tr -d '\n' | sudo tee "$REDIS_ADMIN_FILE" >/dev/null )
    sudo chmod 600 "$REDIS_ADMIN_FILE"
  fi
  if ! printf 'ACL SETUSER default on resetpass >%s ~* %s +@all\n' "$(sudo cat "$REDIS_ADMIN_FILE")" "$CHANNELS" \
      | redis-cli -p "$REDIS_PORT" | grep -qx OK; then
    echo "could not set a password on the default user of instance $INSTANCE" >&2
    exit 1
  fi
  DEFAULT_LOCKED=true
elif ! sudo test -s "$REDIS_ADMIN_FILE"; then
  echo "The default user of instance $INSTANCE has a password Landfall does not know ($REDIS_ADMIN_FILE is missing)." >&2
  echo "Write that password to $REDIS_ADMIN_FILE (root only, mode 600) and run this again." >&2
  exit 1
fi
if ! rcli ping 2>/dev/null | grep -qx PONG; then
  echo "cannot log in to instance $INSTANCE with the admin password in $REDIS_ADMIN_FILE" >&2
  exit 1
fi

# --- The app's user ---
# ACL GETUSER exits 0 for a user that doesn't exist, so list the names instead.
USER_EXISTS=false
if rcli ACL USERS 2>/dev/null | grep -qxF "$REDIS_USER"; then
  USER_EXISTS=true
fi
if [ "$USER_EXISTS" = false ]; then
  CREATED=true
  PASSWORD_ROTATED=true
elif [ "$RESET_PASSWORD" = true ]; then
  PASSWORD_ROTATED=true
fi

# Check what could stop this run before touching the app's password: once it
# changes, the running app's password no longer works.
if ! rcli CONFIG GET aclfile 2>/dev/null | sed -n '2p' | grep -q .; then
  echo "instance $INSTANCE has no aclfile, so its users cannot be saved" >&2
  exit 1
fi

# What the app's user may do: all keys, channels and commands in its own
# instance except the admin ones (ACL, CONFIG, SHUTDOWN, ...), so the app
# cannot change users or the server's configuration. Re-asserted every run.
APP_RULES="~* ${CHANNELS} +@all -@admin ${CLIENT_RULES}"

write_result() {
  ( umask 077
    {
      echo "INSTANCE=${INSTANCE}"
      echo "PORT=${REDIS_PORT}"
      echo "CREATED=${CREATED}"
      echo "PASSWORD_ROTATED=${PASSWORD_ROTATED}"
      echo "DEFAULT_LOCKED=${DEFAULT_LOCKED}"
      if [ -n "$REDIS_PASS" ]; then
        echo "REDIS_URL=redis://${REDIS_USER}:${REDIS_PASS}@127.0.0.1:${REDIS_PORT}/${REDIS_DB}"
      fi
      echo "ACL_SAVED=$1"
    } > "$RESULT" )
}

if [ "$PASSWORD_ROTATED" = true ]; then
  REDIS_PASS=$(openssl rand -base64 32 | tr -dc 'A-Za-z0-9' | head -c 32)
  # Password and permissions in one command, so they change together.
  if ! printf 'ACL SETUSER %s on resetpass >%s %s\n' "$REDIS_USER" "$REDIS_PASS" "$APP_RULES" | rcli | grep -qx OK; then
    echo "could not set the password for Redis user $REDIS_USER" >&2
    exit 1
  fi
  # Record the new URL right away: Redis already uses this password, so it
  # must reach the caller even if a later step fails.
  write_result false
else
  if ! printf 'ACL SETUSER %s on %s\n' "$REDIS_USER" "$APP_RULES" | rcli | grep -qx OK; then
    echo "could not set permissions for Redis user $REDIS_USER" >&2
    exit 1
  fi
fi

if ! rcli ACL SAVE | grep -qx OK; then
  echo "ACL SAVE failed on instance $INSTANCE: its users would be lost when it restarts" >&2
  exit 1
fi

write_result true
`

func buildStatusScript(l Layout, envName, instance, redisUser string, redisDB int) string {
	vars := instanceVars(l, l.Instance(instance))
	vars["ENV_NAME"] = envName
	vars["REDIS_USER"] = redisUser
	vars["REDIS_DB"] = fmt.Sprint(redisDB)
	return "set -euo pipefail\n" + shellVars(vars) + cliSnippet + statusBody
}

const statusBody = `
REDIS_PORT=$(instance_port)
echo "Instance:            $INSTANCE ($UNIT)"
if [ -z "$REDIS_PORT" ]; then
  echo "  not created yet"
  if on_shared_redis; then
    echo "  this environment still uses the shared Redis (port $SHARED_PORT), which every environment on the host can reach"
    echo "  run: landfall redis migrate --env $ENV_NAME"
  else
    echo "  run: landfall redis bootstrap --env $ENV_NAME --save-secret"
  fi
  exit 0
fi
echo "Service:             $(sudo systemctl is-active "$UNIT" 2>/dev/null || true)"
echo "Port:                $REDIS_PORT"
echo "ACL user:            $REDIS_USER"
echo "DB index:            $REDIS_DB"
if ! command -v redis-cli >/dev/null 2>&1; then
  echo "Redis is not installed."
  exit 0
fi
echo "Version:             $(redis-server --version 2>/dev/null | grep -oE 'v=[0-9.]+' || true)"
echo "Ping (as admin):     $(rcli ping 2>&1 || true)"
if redis-cli -p "$REDIS_PORT" ping 2>/dev/null | grep -qx PONG; then
  echo "Default user locked: no (any process on this host can use it; run: landfall redis bootstrap)"
else
  echo "Default user locked: yes"
fi
if rcli ACL USERS 2>/dev/null | grep -qxF "$REDIS_USER"; then
  echo "ACL user exists:     yes"
else
  echo "ACL user exists:     no (run: landfall redis bootstrap --reset-password --save-secret, then deploy)"
fi
MAXMEM=$(rcli CONFIG GET maxmemory 2>/dev/null | sed -n '2p')
POLICY=$(rcli CONFIG GET maxmemory-policy 2>/dev/null | sed -n '2p')
USED=$(rcli INFO memory 2>/dev/null | tr -d '\r' | awk -F: '$1 == "used_memory_human" { print $2 }')
echo "Memory:              ${USED:-?} used, limit ${MAXMEM:-?} bytes (0 = none), policy ${POLICY:-?}"
if on_shared_redis; then
  echo ""
  echo "This environment's old user still exists on the shared Redis (port $SHARED_PORT)."
  echo "Once the app runs on this instance, remove it: redis-cli -p $SHARED_PORT ACL DELUSER $REDIS_USER (as its admin)"
fi
`

// CheckScript is the Redis part of `landfall check`: it reports through the
// check script's pass/fail/warn functions.
func CheckScript(l Layout, instance, redisUser, envName string) string {
	vars := instanceVars(l, l.Instance(instance))
	vars["REDIS_USER"] = redisUser
	vars["ENV_NAME"] = envName
	return shellVars(vars) + cliSnippet + checkBody
}

const checkBody = `
REDIS_PORT=$(instance_port)
if [ -z "$REDIS_PORT" ]; then
  if on_shared_redis; then
    warn redis "on the shared Redis (port $SHARED_PORT): no isolation from other environments on this host (run: landfall redis migrate --env $ENV_NAME)"
  else
    fail redis "no Redis instance for this environment (run: landfall redis bootstrap --env $ENV_NAME --save-secret)"
  fi
elif ! sudo systemctl is-active --quiet "$UNIT"; then
  fail redis "instance $UNIT is not running (sudo journalctl -u $UNIT)"
elif ! rcli ACL USERS 2>/dev/null | grep -qxF "$REDIS_USER"; then
  fail redis "ACL user $REDIS_USER missing in $INSTANCE (run: landfall redis bootstrap --env $ENV_NAME --reset-password --save-secret, then deploy)"
else
  pass redis "own instance $INSTANCE on port $REDIS_PORT, user $REDIS_USER"
  if redis-cli -p "$REDIS_PORT" ping 2>/dev/null | grep -qx PONG; then
    fail redis_default_user "no password: any process on the host can use this instance (run: landfall redis bootstrap --env $ENV_NAME)"
  else
    pass redis_default_user "password protected"
  fi
fi
`
