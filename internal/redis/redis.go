package redis

import (
	"fmt"
	"net/url"
	"os"

	"github.com/flaggx/landfall/internal/config"
	"github.com/flaggx/landfall/internal/ssh"
	"github.com/flaggx/landfall/internal/util"
)

type BootstrapOptions struct {
	ResetPassword bool
	SaveSecret    bool
}

type BootstrapResult struct {
	User             string
	Database         int
	Port             int
	ConnectionString string
	SecretName       string
	Created          bool
	PasswordRotated  bool
}

func Bootstrap(cfg *config.ResolvedConfig, opts BootstrapOptions) (*BootstrapResult, error) {
	redisUser, err := cfg.RedisUser()
	if err != nil {
		return nil, err
	}
	redisPort := cfg.RedisPort()
	redisDB := cfg.RedisDatabase()

	client, err := ssh.New(ssh.Options{
		Host:    cfg.Environment.Host,
		User:    cfg.Environment.User,
		KeyPath: cfg.Global.SSHKeyPath,
	})
	if err != nil {
		return nil, err
	}
	defer client.Close()

	fmt.Fprintf(os.Stdout, "Setting up Redis for %s on %s...\n", cfg.EnvName, cfg.SSHAddress())

	resultPath := fmt.Sprintf("/tmp/landfall-redis-%s-%s.env", cfg.Project.Project.Name, cfg.EnvName)
	script := buildBootstrapScript(redisUser, redisPort, redisDB, resultPath, opts.ResetPassword)

	if _, err := client.RunScript("landfall-redis-bootstrap.sh", script); err != nil {
		return nil, err
	}

	raw, err := client.ReadFile(resultPath)
	if err != nil {
		return nil, fmt.Errorf("read bootstrap result: %w", err)
	}
	_, _ = client.Run(fmt.Sprintf("rm -f %s", util.ShellQuote(resultPath)))

	vars := util.ParseEnvFile(string(raw))
	connectionString := vars["REDIS_URL"]

	result := &BootstrapResult{
		User:             redisUser,
		Database:         redisDB,
		Port:             redisPort,
		ConnectionString: connectionString,
		SecretName:       cfg.RedisSecretName(),
		Created:          vars["CREATED"] == "true",
		PasswordRotated:  vars["PASSWORD_ROTATED"] == "true",
	}

	if connectionString == "" && !result.Created && !result.PasswordRotated {
		printBootstrapResult(result)
		fmt.Fprintln(os.Stdout, "No new connection string generated. Use your existing secret or run with --reset-password.")
		return result, nil
	}

	if connectionString == "" {
		return nil, fmt.Errorf("redis bootstrap did not return a connection string")
	}

	printBootstrapResult(result)

	if opts.SaveSecret {
		if _, err := config.InitSecretsFile(); err != nil {
			return result, err
		}
		if err := config.SetSecret(result.SecretName, result.ConnectionString); err != nil {
			return result, err
		}
		fmt.Fprintf(os.Stdout, "Saved secret %q\n", result.SecretName)
	} else if result.ConnectionString != "" && (result.Created || result.PasswordRotated) {
		fmt.Fprintf(os.Stdout, "\nSave to secrets with:\n  landfall secrets set %s --value %q\n", result.SecretName, result.ConnectionString)
	}

	if result.Created || result.PasswordRotated {
		fmt.Fprintf(os.Stdout, "\nAdd to landfall.toml if not already present:\n")
		fmt.Fprintf(os.Stdout, "[environments.%s.env]\nREDIS_URL = \"{{secret:%s}}\"\n", cfg.EnvName, result.SecretName)
	}

	return result, nil
}

func Status(cfg *config.ResolvedConfig) error {
	redisUser, err := cfg.RedisUser()
	if err != nil {
		return err
	}
	redisPort := cfg.RedisPort()
	redisDB := cfg.RedisDatabase()

	client, err := ssh.New(ssh.Options{
		Host:    cfg.Environment.Host,
		User:    cfg.Environment.User,
		KeyPath: cfg.Global.SSHKeyPath,
	})
	if err != nil {
		return err
	}
	defer client.Close()

	script := fmt.Sprintf(`
set -euo pipefail
echo "Redis service:"
sudo systemctl is-active redis-server 2>/dev/null || sudo systemctl is-active redis 2>/dev/null || echo "  not running"
echo ""
echo "Configured ACL user: %s"
echo "Configured DB index: %d"
echo "Configured port:     %d"
echo ""
if command -v redis-cli >/dev/null 2>&1; then
  echo "Installed version:"
  redis-server --version || redis-cli --version
  echo ""
  echo "Ping:"
  redis-cli -p %d ping || true
  echo ""
  echo "ACL user exists:"
  redis-cli -p %d ACL USERS 2>/dev/null | grep -qxF %s && echo "  yes" || echo "  no (run: landfall redis bootstrap --reset-password --save-secret, then deploy)"
  echo ""
  echo "ACL users persisted (aclfile):"
  redis-cli -p %d CONFIG GET aclfile 2>/dev/null | sed -n '2p' | grep -q . && echo "  yes" || echo "  no (users are lost when Redis restarts; run: landfall redis bootstrap)"
else
  echo "Redis is not installed."
fi
`, redisUser, redisDB, redisPort, redisPort, redisPort, util.ShellQuote(redisUser), redisPort)

	_, err = client.RunScript("landfall-redis-status.sh", script)
	return err
}

func buildBootstrapScript(redisUser string, redisPort, redisDB int, resultPath string, resetPassword bool) string {
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
REDIS_CONF="/etc/redis/redis.conf"
ACL_FILE="/var/lib/redis/users.acl"
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
    for _ in $(seq 1 20); do
      redis-cli -p "$REDIS_PORT" ping >/dev/null 2>&1 && break
      sleep 0.5
    done
  fi
else
  echo "Redis config not found at $REDIS_CONF; cannot set up an aclfile" >&2
  exit 1
fi

# ACL GETUSER exits 0 for a user that doesn't exist, so list the names instead.
USER_EXISTS=false
if redis-cli -p "$REDIS_PORT" ACL USERS 2>/dev/null | grep -qxF "$REDIS_USER"; then
  USER_EXISTS=true
fi

if [ "$USER_EXISTS" = false ]; then
  REDIS_PASS=$(openssl rand -base64 32 | tr -dc 'A-Za-z0-9' | head -c 32)
  redis-cli -p "$REDIS_PORT" ACL SETUSER "$REDIS_USER" on ">$REDIS_PASS" ~* '&*' +@all >/dev/null
  CREATED=true
  PASSWORD_ROTATED=true
elif [ "$RESET_PASSWORD" = true ]; then
  REDIS_PASS=$(openssl rand -base64 32 | tr -dc 'A-Za-z0-9' | head -c 32)
  # Re-assert on + key/channel/command perms; resetpass alone can leave the user disabled/empty.
  redis-cli -p "$REDIS_PORT" ACL SETUSER "$REDIS_USER" on resetpass ">$REDIS_PASS" ~* '&*' +@all >/dev/null
  PASSWORD_ROTATED=true
fi

if [ "$CREATED" = true ] || [ "$PASSWORD_ROTATED" = true ]; then
  if ! redis-cli -p "$REDIS_PORT" ACL SAVE | grep -qx OK; then
    echo "ACL SAVE failed: the Redis user will be lost when Redis restarts (is aclfile set in $REDIS_CONF?)" >&2
    exit 1
  fi
fi

{
  echo "CREATED=${CREATED}"
  echo "PASSWORD_ROTATED=${PASSWORD_ROTATED}"
  if [ -n "$REDIS_PASS" ]; then
    echo "REDIS_URL=redis://${REDIS_USER}:${REDIS_PASS}@127.0.0.1:${REDIS_PORT}/${REDIS_DB}"
  fi
} > "$RESULT"
chmod 600 "$RESULT"
`, util.ShellQuote(resultPath), util.ShellQuote(redisUser), redisPort, redisDB, util.ShellQuote(resetFlag))
}

func printBootstrapResult(result *BootstrapResult) {
	fmt.Fprintln(os.Stdout, "")
	if result.Created {
		fmt.Fprintf(os.Stdout, "Created Redis ACL user %q (DB index %d, port %d)\n", result.User, result.Database, result.Port)
	} else if result.PasswordRotated {
		fmt.Fprintf(os.Stdout, "Updated password for Redis ACL user %q (DB index %d)\n", result.User, result.Database)
	} else {
		fmt.Fprintf(os.Stdout, "Redis ACL user %q already exists (DB index %d)\n", result.User, result.Database)
		fmt.Fprintln(os.Stdout, "Use --reset-password to rotate the password, or keep your existing secret.")
	}
}

// EncodeRedisURL builds a properly escaped redis URL (used in tests).
func EncodeRedisURL(user, password, host string, port, db int) string {
	u := &url.URL{
		Scheme: "redis",
		User:   url.UserPassword(user, password),
		Host:   fmt.Sprintf("%s:%d", host, port),
		Path:   fmt.Sprintf("/%d", db),
	}
	return u.String()
}
