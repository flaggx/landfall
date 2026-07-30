package db

import (
	"fmt"
	"os"

	"github.com/flaggx/vpsdeploymentautomation/internal/config"
	"github.com/flaggx/vpsdeploymentautomation/internal/ssh"
	"github.com/flaggx/vpsdeploymentautomation/internal/util"
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

func Bootstrap(cfg *config.ResolvedConfig, opts BootstrapOptions) (*BootstrapResult, error) {
	dbName, err := cfg.PostgresDatabase()
	if err != nil {
		return nil, err
	}
	dbUser, err := cfg.PostgresUser()
	if err != nil {
		return nil, err
	}

	client, err := ssh.New(ssh.Options{
		Host:    cfg.Environment.Host,
		User:    cfg.Environment.User,
		KeyPath: cfg.Global.SSHKeyPath,
	})
	if err != nil {
		return nil, err
	}
	defer client.Close()

	fmt.Fprintf(os.Stdout, "Setting up PostgreSQL for %s on %s...\n", cfg.EnvName, cfg.SSHAddress())

	resultPath := fmt.Sprintf("/tmp/vpsdeploy-db-%s-%s.env", cfg.Project.Project.Name, cfg.EnvName)
	script := buildBootstrapScript(dbName, dbUser, resultPath, opts.ResetPassword)

	if _, err := client.RunScript("vpsdeploy-db-bootstrap.sh", script); err != nil {
		return nil, err
	}

	raw, err := client.ReadFile(resultPath)
	if err != nil {
		return nil, fmt.Errorf("read bootstrap result: %w", err)
	}
	_, _ = client.Run(fmt.Sprintf("rm -f %s", util.ShellQuote(resultPath)))

	vars := util.ParseEnvFile(string(raw))
	connectionString := vars["DATABASE_URL"]

	result := &BootstrapResult{
		Database:         dbName,
		User:             dbUser,
		ConnectionString: connectionString,
		SecretName:       cfg.PostgresSecretName(),
		Created:          vars["CREATED"] == "true",
		PasswordRotated:  vars["PASSWORD_ROTATED"] == "true",
	}

	if connectionString == "" && !result.Created && !result.PasswordRotated {
		printBootstrapResult(result)
		fmt.Fprintln(os.Stdout, "No new connection string generated. Use your existing secret or run with --reset-password.")
		return result, nil
	}

	if connectionString == "" {
		return nil, fmt.Errorf("database bootstrap did not return a connection string")
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
		fmt.Fprintf(os.Stdout, "\nSave to secrets with:\n  vpsdeploy secrets set %s --value %q\n", result.SecretName, result.ConnectionString)
	}

	if result.Created || result.PasswordRotated {
		fmt.Fprintf(os.Stdout, "\nAdd to vpsdeploy.toml if not already present:\n")
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
echo "PostgreSQL service:"
sudo systemctl is-active postgresql || true
echo ""
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
`, dbName, dbUser, dbName, dbUser)

	_, err = client.RunScript("vpsdeploy-db-status.sh", script)
	return err
}

func buildBootstrapScript(dbName, dbUser, resultPath string, resetPassword bool) string {
	resetFlag := "false"
	if resetPassword {
		resetFlag = "true"
	}

	return fmt.Sprintf(`
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

RESULT=%s
DB_NAME=%s
DB_USER=%s
RESET_PASSWORD=%s

CREATED=false
PASSWORD_ROTATED=false
DB_PASS=""

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
  sudo -u postgres psql -c "CREATE USER \"${DB_USER}\" WITH PASSWORD '${DB_PASS}';"
  CREATED=true
  PASSWORD_ROTATED=true
elif [ "$RESET_PASSWORD" = true ]; then
  DB_PASS=$(openssl rand -base64 32 | tr -dc 'A-Za-z0-9' | head -c 32)
  sudo -u postgres psql -c "ALTER USER \"${DB_USER}\" WITH PASSWORD '${DB_PASS}';"
  PASSWORD_ROTATED=true
fi

if [ "$DB_EXISTS" = false ]; then
  sudo -u postgres psql -c "CREATE DATABASE \"${DB_NAME}\" OWNER \"${DB_USER}\";"
  CREATED=true
fi

sudo -u postgres psql -c "GRANT ALL PRIVILEGES ON DATABASE \"${DB_NAME}\" TO \"${DB_USER}\";"

{
  echo "CREATED=${CREATED}"
  echo "PASSWORD_ROTATED=${PASSWORD_ROTATED}"
  if [ -n "$DB_PASS" ]; then
    echo "DATABASE_URL=postgresql://${DB_USER}:${DB_PASS}@localhost:5432/${DB_NAME}"
  fi
} > "$RESULT"
chmod 600 "$RESULT"
`, util.ShellQuote(resultPath), util.ShellQuote(dbName), util.ShellQuote(dbUser), util.ShellQuote(resetFlag))
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
