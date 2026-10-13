package redis

import (
	"fmt"

	"github.com/flaggx/landfall/internal/util"
)

// Paths are the files the Redis scripts read and write on the server.
type Paths struct {
	// Conf is the Redis configuration file.
	Conf string
	// ACLFile is where Redis persists ACL users.
	ACLFile string
	// AdminFile holds the password of Redis's built-in "default" user. Only
	// root can read it; Landfall's own commands read it through sudo.
	AdminFile string
}

// DefaultPaths are the locations on an Ubuntu VPS.
var DefaultPaths = Paths{
	Conf:      "/etc/redis/redis.conf",
	ACLFile:   "/var/lib/redis/users.acl",
	AdminFile: "/etc/landfall/redis-admin-password",
}

// CLIPrelude defines the shell function rcli, which runs redis-cli as the
// admin ("default") user. It expects REDIS_PORT to be set. The password is
// passed through the environment, never on a command line.
func CLIPrelude(paths Paths) string {
	return fmt.Sprintf(`
REDIS_ADMIN_FILE=%s
rcli() {
  if sudo test -s "$REDIS_ADMIN_FILE"; then
    REDISCLI_AUTH="$(sudo cat "$REDIS_ADMIN_FILE")" redis-cli --no-auth-warning -p "$REDIS_PORT" "$@"
  else
    redis-cli -p "$REDIS_PORT" "$@"
  fi
}
`, util.ShellQuote(paths.AdminFile))
}

func buildBootstrapScript(paths Paths, redisUser string, redisPort, redisDB int, resultPath string, resetPassword bool) string {
	resetFlag := "false"
	if resetPassword {
		resetFlag = "true"
	}

	return fmt.Sprintf(`
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

RESULT=%s
REDIS_USER=%s
REDIS_PORT=%d
REDIS_DB=%d
RESET_PASSWORD=%s
REDIS_CONF=%s
ACL_FILE=%s
%s
CREATED=false
PASSWORD_ROTATED=false
REDIS_PASS=""

if command -v apt-get >/dev/null 2>&1; then
  if ! command -v redis-cli >/dev/null 2>&1; then
    sudo apt-get update -qq
    sudo apt-get install -y -qq redis-server
  fi
else
  echo "apt-get not found; install Redis manually" >&2
  exit 1
fi

sudo systemctl enable redis-server 2>/dev/null || sudo systemctl enable redis 2>/dev/null || true
sudo systemctl start redis-server 2>/dev/null || sudo systemctl start redis 2>/dev/null

# Ensure Redis only listens locally and keeps ACL users in a file. Without an
# aclfile, ACL SAVE fails and users created below vanish when Redis restarts.
# /etc/redis is readable only by the redis user, so test with sudo.
if sudo test -f "$REDIS_CONF"; then
  CONF_CHANGED=false
  # Leave a bind line alone if it already lists only loopback addresses
  # (the package default is "bind 127.0.0.1 -::1").
  if ! sudo grep -qE '^bind( -?(127\.0\.0\.1|::1))+[[:space:]]*$' "$REDIS_CONF"; then
    if sudo grep -q '^bind ' "$REDIS_CONF"; then
      sudo sed -i 's/^bind .*/bind 127.0.0.1 -::1/' "$REDIS_CONF"
    else
      echo 'bind 127.0.0.1 -::1' | sudo tee -a "$REDIS_CONF" >/dev/null
    fi
    CONF_CHANGED=true
  fi
  if sudo grep -q '^user ' "$REDIS_CONF"; then
    # Redis refuses to start with both inline users and an aclfile.
    echo "redis.conf defines users inline; leaving aclfile unset" >&2
  elif ! sudo grep -q '^aclfile ' "$REDIS_CONF"; then
    sudo touch "$ACL_FILE"
    sudo chown redis:redis "$ACL_FILE"
    sudo chmod 640 "$ACL_FILE"
    echo "aclfile $ACL_FILE" | sudo tee -a "$REDIS_CONF" >/dev/null
    CONF_CHANGED=true
  fi
  # Restarting drops any ACL users that were never saved, so only restart
  # when the config actually changed.
  if [ "$CONF_CHANGED" = true ]; then
    sudo systemctl restart redis-server 2>/dev/null || sudo systemctl restart redis 2>/dev/null
  fi
else
  echo "Redis config not found at $REDIS_CONF; cannot set up an aclfile" >&2
  exit 1
fi

# Wait for Redis to answer at all (an auth error counts: it is up).
for _ in $(seq 1 40); do
  if redis-cli -p "$REDIS_PORT" ping 2>&1 | grep -qE 'PONG|NOAUTH'; then
    break
  fi
  sleep 0.5
done

# Put a password on the built-in "default" user. Out of the box it has none,
# so any process on the host could read, change or flush every environment's
# data. Passwords are sent to redis-cli on stdin so they never show up in the
# process list.
if redis-cli -p "$REDIS_PORT" ping 2>/dev/null | grep -qx PONG; then
  if ! sudo test -s "$REDIS_ADMIN_FILE"; then
    sudo mkdir -p "$(dirname "$REDIS_ADMIN_FILE")"
    sudo chmod 700 "$(dirname "$REDIS_ADMIN_FILE")"
    ( umask 077; openssl rand -hex 32 | tr -d '\n' | sudo tee "$REDIS_ADMIN_FILE" >/dev/null )
    sudo chmod 600 "$REDIS_ADMIN_FILE"
  fi
  if ! printf 'ACL SETUSER default on resetpass >%%s ~* &* +@all\n' "$(sudo cat "$REDIS_ADMIN_FILE")" \
      | redis-cli -p "$REDIS_PORT" | grep -qx OK; then
    echo "could not set a password on Redis's default user" >&2
    exit 1
  fi
  DEFAULT_LOCKED=true
elif ! sudo test -s "$REDIS_ADMIN_FILE"; then
  echo "Redis's default user has a password Landfall does not know ($REDIS_ADMIN_FILE is missing)." >&2
  echo "Write that password to $REDIS_ADMIN_FILE (root only, mode 600) and run this again." >&2
  exit 1
fi

if ! rcli ping 2>/dev/null | grep -qx PONG; then
  echo "cannot log in to Redis with the admin password in $REDIS_ADMIN_FILE" >&2
  exit 1
fi

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

if [ "$PASSWORD_ROTATED" = true ]; then
  REDIS_PASS=$(openssl rand -base64 32 | tr -dc 'A-Za-z0-9' | head -c 32)
  if ! printf 'ACL SETUSER %%s on resetpass >%%s\n' "$REDIS_USER" "$REDIS_PASS" | rcli | grep -qx OK; then
    echo "could not set the password for Redis user $REDIS_USER" >&2
    exit 1
  fi
fi

# (Re)assert what the app's user may do on every run: all keys, channels and
# commands except the admin ones (ACL, CONFIG, SHUTDOWN, ...), so the app
# cannot change users or the server's configuration.
if ! rcli ACL SETUSER "$REDIS_USER" on '~*' '&*' +@all -@admin | grep -qx OK; then
  echo "could not set permissions for Redis user $REDIS_USER" >&2
  exit 1
fi

if ! rcli ACL SAVE | grep -qx OK; then
  echo "ACL SAVE failed: Redis users will be lost when Redis restarts (is aclfile set in $REDIS_CONF?)" >&2
  exit 1
fi

{
  echo "CREATED=${CREATED}"
  echo "PASSWORD_ROTATED=${PASSWORD_ROTATED}"
  echo "DEFAULT_LOCKED=${DEFAULT_LOCKED:-false}"
  if [ -n "$REDIS_PASS" ]; then
    echo "REDIS_URL=redis://${REDIS_USER}:${REDIS_PASS}@127.0.0.1:${REDIS_PORT}/${REDIS_DB}"
  fi
} > "$RESULT"
chmod 600 "$RESULT"
`, util.ShellQuote(resultPath), util.ShellQuote(redisUser), redisPort, redisDB, util.ShellQuote(resetFlag),
		util.ShellQuote(paths.Conf), util.ShellQuote(paths.ACLFile), CLIPrelude(paths))
}

func buildStatusScript(paths Paths, redisUser string, redisPort, redisDB int) string {
	return fmt.Sprintf(`
set -euo pipefail
REDIS_PORT=%d
REDIS_USER=%s
%s
echo "Redis service:"
sudo systemctl is-active redis-server 2>/dev/null || sudo systemctl is-active redis 2>/dev/null || echo "  not running"
echo ""
echo "Configured ACL user: $REDIS_USER"
echo "Configured DB index: %d"
echo "Configured port:     $REDIS_PORT"
echo ""
if command -v redis-cli >/dev/null 2>&1; then
  echo "Installed version:"
  redis-server --version || redis-cli --version
  echo ""
  echo "Ping (as admin):"
  rcli ping || true
  echo ""
  echo "Default user password-protected:"
  if redis-cli -p "$REDIS_PORT" ping 2>/dev/null | grep -qx PONG; then
    echo "  no (any process on this host can use Redis; run: landfall redis bootstrap)"
  else
    echo "  yes"
  fi
  echo ""
  echo "ACL user exists:"
  rcli ACL USERS 2>/dev/null | grep -qxF "$REDIS_USER" && echo "  yes" || echo "  no (run: landfall redis bootstrap --reset-password --save-secret, then deploy)"
  echo ""
  echo "ACL users persisted (aclfile):"
  rcli CONFIG GET aclfile 2>/dev/null | sed -n '2p' | grep -q . && echo "  yes" || echo "  no (users are lost when Redis restarts; run: landfall redis bootstrap)"
else
  echo "Redis is not installed."
fi
`, redisPort, util.ShellQuote(redisUser), CLIPrelude(paths), redisDB)
}
