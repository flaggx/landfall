package db

import (
	"fmt"
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
	Database         string
	User             string
	ConnectionString string
	SecretName       string
	Created          bool
	PasswordRotated  bool
}

func dbSSHClient(cfg *config.ResolvedConfig) (*ssh.Client, error) {
	return ssh.New(ssh.Options{
		Host:    cfg.PostgresSSHHost(),
		User:    cfg.PostgresSSHUser(),
		KeyPath: cfg.Global.SSHKeyPath,
	})
}

func Bootstrap(cfg *config.ResolvedConfig, opts BootstrapOptions) (*BootstrapResult, error) {
	dbName, err := cfg.PostgresDatabase()
	if err != nil {
		return nil, err
	}
	dbUser, err := cfg.PostgresUser()
	if err != nil {
		return nil, err
	}

	client, err := dbSSHClient(cfg)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	fmt.Fprintf(os.Stdout, "Setting up PostgreSQL for %s on %s...\n", cfg.EnvName, cfg.PostgresSSHHost())

	resultPath := fmt.Sprintf("/tmp/landfall-db-%s-%s.env", cfg.Project.Project.Name, cfg.EnvName)
	script := buildBootstrapScript(cfg, dbName, dbUser, resultPath, opts.ResetPassword)

	_, runErr := client.RunScript("landfall-db-bootstrap.sh", script)

	raw, readErr := client.ReadFile(resultPath)
	_, _ = client.Run(fmt.Sprintf("rm -f %s", util.ShellQuote(resultPath)))

	if runErr != nil {
		// The script records a new password as soon as PostgreSQL uses it. If
		// a later step failed, the old password may already be gone.
		if readErr == nil {
			if url := util.ParseEnvFile(string(raw))["DATABASE_URL"]; url != "" {
				return nil, rescueConnectionString(cfg, opts, url, runErr)
			}
		}
		return nil, runErr
	}
	if readErr != nil {
		return nil, fmt.Errorf("read bootstrap result: %w", readErr)
	}

	vars := util.ParseEnvFile(string(raw))
	connectionString := vars["DATABASE_URL"]

	result := &BootstrapResult{
		Database:         dbName,
		User:             dbUser,
		ConnectionString: connectionString,
		SecretName:       cfg.PostgresSecretName(),
		Created:          vars["DB_CREATED"] == "true" || vars["USER_CREATED"] == "true",
		PasswordRotated:  vars["PASSWORD_ROTATED"] == "true",
	}

	if connectionString == "" {
		printBootstrapResult(result)
		if vars["DB_CREATED"] == "true" {
			// The user already existed (e.g. shared with another environment),
			// so its password was kept and is not known here.
			fmt.Fprintf(os.Stdout, "User %q already existed, so its password was not changed. Use it in this environment's DATABASE_URL:\n  postgresql://%s:<password>@%s:%d/%s\n", dbUser, dbUser, cfg.PostgresConnectHost(), cfg.PostgresPort(), dbName)
			fmt.Fprintln(os.Stdout, "Or re-run with --reset-password for a new one; that changes it for every database this user can reach.")
		} else {
			fmt.Fprintln(os.Stdout, "No new connection string generated. Use your existing secret or run with --reset-password.")
		}
		return result, nil
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
		fmt.Fprintf(os.Stdout, "[environments.%s.env]\nDATABASE_URL = \"{{secret:%s}}\"\n", cfg.EnvName, result.SecretName)
	}

	return result, nil
}

func Status(cfg *config.ResolvedConfig) error {
	dbName, err := cfg.PostgresDatabase()
	if err != nil {
		return err
	}
	dbUser, err := cfg.PostgresUser()
	if err != nil {
		return err
	}

	client, err := dbSSHClient(cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	script := fmt.Sprintf(`
set -euo pipefail
echo "PostgreSQL service:"
sudo systemctl is-active postgresql || true
echo ""
echo "SSH target:          %s"
echo "Connect host:        %s"
echo "Port:                %d"
echo "Configured database: %s"
echo "Configured user:     %s"
echo ""
if command -v psql >/dev/null 2>&1; then
  echo "Installed version:"
  psql --version
  echo ""
  echo "Database exists:"
  sudo -u postgres psql -tAc "SELECT 1 FROM pg_database WHERE datname='%s'" | grep -q 1 && echo "  yes" || echo "  no"
  echo "User exists:"
  sudo -u postgres psql -tAc "SELECT 1 FROM pg_roles WHERE rolname='%s'" | grep -q 1 && echo "  yes" || echo "  no"
else
  echo "PostgreSQL is not installed."
fi
`, util.ShellQuote(cfg.PostgresSSHHost()), util.ShellQuote(cfg.PostgresConnectHost()), cfg.PostgresPort(),
		dbName, dbUser, dbName, dbUser)

	_, err = client.RunScript("landfall-db-status.sh", script)
	return err
}

// pgHbaSnippet makes HBA contain exactly one remote-access line for
// DB_NAME/DB_USER from APP_HOST, under a marker naming that database and user,
// so several environments can share one DB server and a changed app_host
// replaces the old line. Blocks written by older versions (a bare
// "# landfall-app-access" marker) are replaced when they belong to this
// database and user. Sets HBA_CHANGED=true when the file changed.
const pgHbaSnippet = `
MARKER="# landfall-app-access ${DB_NAME} ${DB_USER}"
HBA_LINE="host    ${DB_NAME}    ${DB_USER}    ${APP_HOST}/32    scram-sha-256"
HBA_NEW=$(mktemp)
sudo cat "$HBA" | awk -v m="$MARKER" -v legacy="# landfall-app-access" -v db="$DB_NAME" -v u="$DB_USER" '
  drop { drop = 0; next }
  $0 == m { drop = 1; next }
  $0 == legacy {
    if ((getline nxt) > 0) {
      split(nxt, f, " ")
      if (f[1] == "host" && f[2] == db && f[3] == u) next
      print; print nxt; next
    }
  }
  { print }
' > "$HBA_NEW"
printf '%s\n%s\n' "$MARKER" "$HBA_LINE" >> "$HBA_NEW"
HBA_CHANGED=false
if ! sudo cmp -s "$HBA_NEW" "$HBA"; then
  sudo cp "$HBA" "$HBA.landfall-backup"
  sudo tee "$HBA" < "$HBA_NEW" >/dev/null
  HBA_CHANGED=true
fi
rm -f "$HBA_NEW"
`

func buildBootstrapScript(cfg *config.ResolvedConfig, dbName, dbUser, resultPath string, resetPassword bool) string {
	resetFlag := "false"
	if resetPassword {
		resetFlag = "true"
	}

	connectHost := cfg.PostgresConnectHost()
	port := cfg.PostgresPort()
	appHost := cfg.PostgresAppHost()
	remote := "false"
	if cfg.PostgresIsRemote() {
		remote = "true"
	}

	return fmt.Sprintf(`
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

RESULT=%s
DB_NAME=%s
DB_USER=%s
RESET_PASSWORD=%s
CONNECT_HOST=%s
DB_PORT=%d
REMOTE=%s
APP_HOST=%s

USER_CREATED=false
DB_CREATED=false
PASSWORD_ROTATED=false
DB_PASS=""
# Never let a caller read a result left over from an earlier run.
rm -f "$RESULT"

# The result is written as soon as a password changes, and again at the end,
# so a new password reaches the caller even if a later step fails.
write_result() {
  ( umask 077
    {
      echo "USER_CREATED=${USER_CREATED}"
      echo "DB_CREATED=${DB_CREATED}"
      echo "PASSWORD_ROTATED=${PASSWORD_ROTATED}"
      if [ -n "$DB_PASS" ]; then
        echo "DATABASE_URL=postgresql://${DB_USER}:${DB_PASS}@${CONNECT_HOST}:${DB_PORT}/${DB_NAME}"
      fi
      echo "COMPLETE=$1"
    } > "$RESULT" )
}

if command -v apt-get >/dev/null 2>&1; then
  if ! command -v psql >/dev/null 2>&1; then
    sudo apt-get update -qq
    sudo apt-get install -y -qq postgresql postgresql-contrib
  fi
  sudo systemctl enable postgresql
  sudo systemctl start postgresql
else
  echo "apt-get not found; install PostgreSQL manually" >&2
  exit 1
fi

if [ "$REMOTE" = "true" ] && [ -z "$APP_HOST" ]; then
  echo "postgres.app_host (or environment host) is required when postgres.host is set" >&2
  exit 1
fi

USER_EXISTS=false
DB_EXISTS=false
if sudo -u postgres psql -tAc "SELECT 1 FROM pg_roles WHERE rolname='${DB_USER}'" | grep -q 1; then
  USER_EXISTS=true
fi
if sudo -u postgres psql -tAc "SELECT 1 FROM pg_database WHERE datname='${DB_NAME}'" | grep -q 1; then
  DB_EXISTS=true
fi

if [ "$USER_EXISTS" = false ]; then
  DB_PASS=$(openssl rand -base64 32 | tr -dc 'A-Za-z0-9' | head -c 32)
  sudo -u postgres psql -v ON_ERROR_STOP=1 -c "CREATE USER \"${DB_USER}\" WITH PASSWORD '${DB_PASS}';"
  USER_CREATED=true
  PASSWORD_ROTATED=true
  write_result false
fi

# A user that already exists (e.g. shared by several environments) keeps its
# password; the new database is simply owned by it.
if [ "$DB_EXISTS" = false ]; then
  sudo -u postgres psql -v ON_ERROR_STOP=1 -c "CREATE DATABASE \"${DB_NAME}\" OWNER \"${DB_USER}\";"
  DB_CREATED=true
fi

sudo -u postgres psql -v ON_ERROR_STOP=1 -c "GRANT ALL PRIVILEGES ON DATABASE \"${DB_NAME}\" TO \"${DB_USER}\";"

# Allow remote app connections when postgres.host is a dedicated DB VPS.
# Restart only for listen_addresses; pg_hba.conf changes need just a reload,
# so a shared DB server doesn't drop every environment's connections.
if [ "$REMOTE" = "true" ]; then
  PG_MAJOR=$(sudo -u postgres psql -tAc "SHOW server_version_num" | head -c 2)
  PG_CONF_DIR="/etc/postgresql/${PG_MAJOR}/main"
  if [ ! -d "$PG_CONF_DIR" ]; then
    PG_CONF_DIR=$(ls -d /etc/postgresql/*/main 2>/dev/null | head -1 || true)
  fi
  if [ -z "$PG_CONF_DIR" ] || [ ! -d "$PG_CONF_DIR" ]; then
    echo "Could not locate PostgreSQL config directory" >&2
    exit 1
  fi
  PG_CONF="$PG_CONF_DIR/postgresql.conf"
  NEEDS_RESTART=false
  if ! sudo grep -qE "^listen_addresses[[:space:]]*=[[:space:]]*'\*'" "$PG_CONF"; then
    sudo sed -i "s/^#\?listen_addresses.*/listen_addresses = '*'/" "$PG_CONF"
    NEEDS_RESTART=true
  fi
  HBA="$PG_CONF_DIR/pg_hba.conf"
%s
  if command -v ufw >/dev/null 2>&1; then
    sudo ufw allow from "$APP_HOST" to any port "$DB_PORT" proto tcp comment "landfall-postgres" || true
  fi
  if [ "$NEEDS_RESTART" = true ]; then
    sudo systemctl restart postgresql
  elif [ "$HBA_CHANGED" = true ]; then
    sudo systemctl reload postgresql
  fi
fi

# Rotate an existing user's password last: once it changes, apps using the
# old one can no longer connect, so nothing may fail after this but the
# final result write.
if [ "$USER_EXISTS" = true ] && [ "$RESET_PASSWORD" = true ]; then
  DB_PASS=$(openssl rand -base64 32 | tr -dc 'A-Za-z0-9' | head -c 32)
  sudo -u postgres psql -v ON_ERROR_STOP=1 -c "ALTER USER \"${DB_USER}\" WITH PASSWORD '${DB_PASS}';"
  PASSWORD_ROTATED=true
  write_result false
fi

write_result true
`, util.ShellQuote(resultPath), util.ShellQuote(dbName), util.ShellQuote(dbUser), util.ShellQuote(resetFlag),
		util.ShellQuote(connectHost), port, util.ShellQuote(remote), util.ShellQuote(appHost), pgHbaSnippet)
}

// rescueConnectionString handles a bootstrap that changed the user's password
// but then failed: it saves or prints the new URL and returns an error
// explaining both.
func rescueConnectionString(cfg *config.ResolvedConfig, opts BootstrapOptions, url string, runErr error) error {
	secret := cfg.PostgresSecretName()
	if opts.SaveSecret {
		if _, err := config.InitSecretsFile(); err == nil {
			if err := config.SetSecret(secret, url); err == nil {
				fmt.Fprintf(os.Stdout, "Saved secret %q with the new PostgreSQL password.\n", secret)
				return fmt.Errorf("database bootstrap failed after setting the password (the new one is saved in %q; fix the error and re-run without --reset-password): %w", secret, runErr)
			}
		}
	}
	fmt.Fprintf(os.Stdout, "\nPostgreSQL already uses a new password for this user. Save it now:\n  landfall secrets set %s --value %q\n", secret, url)
	return fmt.Errorf("database bootstrap failed after setting the password (save the URL above, then fix the error and re-run without --reset-password): %w", runErr)
}

func printBootstrapResult(result *BootstrapResult) {
	fmt.Fprintln(os.Stdout, "")
	if result.Created {
		fmt.Fprintf(os.Stdout, "Created PostgreSQL database %q (user %q)\n", result.Database, result.User)
	} else if result.PasswordRotated {
		fmt.Fprintf(os.Stdout, "Updated password for PostgreSQL user %q (database %q)\n", result.User, result.Database)
	} else {
		fmt.Fprintf(os.Stdout, "PostgreSQL database %q already exists (user %q)\n", result.Database, result.User)
		fmt.Fprintln(os.Stdout, "Use --reset-password to rotate the password, or keep your existing secret.")
	}
}
