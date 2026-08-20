package db

import (
	"fmt"
	"os"
	"strings"

	"github.com/flaggx/vpsdeploymentautomation/internal/config"
	"github.com/flaggx/vpsdeploymentautomation/internal/ssh"
	"github.com/flaggx/vpsdeploymentautomation/internal/util"
)

type ReplicaOptions struct {
	ReplicaHost string
	SaveSecret  bool
}

// BootstrapReplica configures streaming replication from the environment primary
// onto --replica-host (a second VPS with SSH access using the same deploy key).
func BootstrapReplica(cfg *config.ResolvedConfig, opts ReplicaOptions) error {
	if strings.TrimSpace(opts.ReplicaHost) == "" {
		return fmt.Errorf("--replica-host is required")
	}

	dbName, err := cfg.PostgresDatabase()
	if err != nil {
		return err
	}
	dbUser, err := cfg.PostgresUser()
	if err != nil {
		return err
	}

	primaryHost := cfg.PostgresSSHHost()
	replUser := "vpsdeploy_repl"
	port := cfg.PostgresPort()

	primary, err := dbSSHClient(cfg)
	if err != nil {
		return err
	}
	defer primary.Close()

	fmt.Fprintf(os.Stdout, "Configuring replication user on primary %s...\n", primaryHost)

	resultPath := fmt.Sprintf("/tmp/vpsdeploy-repl-%s.env", cfg.EnvName)
	primScript := fmt.Sprintf(`
set -euo pipefail
RESULT=%s
REPL_USER=%s
REPLICA_HOST=%s
DB_PORT=%d

PASS=$(openssl rand -base64 32 | tr -dc 'A-Za-z0-9' | head -c 32)
if ! sudo -u postgres psql -tAc "SELECT 1 FROM pg_roles WHERE rolname='${REPL_USER}'" | grep -q 1; then
  sudo -u postgres psql -c "CREATE ROLE \"${REPL_USER}\" WITH REPLICATION LOGIN PASSWORD '${PASS}';"
else
  sudo -u postgres psql -c "ALTER ROLE \"${REPL_USER}\" WITH REPLICATION LOGIN PASSWORD '${PASS}';"
fi

PG_CONF_DIR=$(ls -d /etc/postgresql/*/main 2>/dev/null | head -1)
if [ -z "$PG_CONF_DIR" ]; then
  echo "PostgreSQL config dir not found" >&2
  exit 1
fi
sudo sed -i "s/^#\\?listen_addresses.*/listen_addresses = '*'/" "$PG_CONF_DIR/postgresql.conf"
grep -q '^wal_level' "$PG_CONF_DIR/postgresql.conf" && sudo sed -i "s/^#\\?wal_level.*/wal_level = replica/" "$PG_CONF_DIR/postgresql.conf" || echo "wal_level = replica" | sudo tee -a "$PG_CONF_DIR/postgresql.conf" >/dev/null
grep -q '^max_wal_senders' "$PG_CONF_DIR/postgresql.conf" && sudo sed -i "s/^#\\?max_wal_senders.*/max_wal_senders = 10/" "$PG_CONF_DIR/postgresql.conf" || echo "max_wal_senders = 10" | sudo tee -a "$PG_CONF_DIR/postgresql.conf" >/dev/null
grep -q '^max_replication_slots' "$PG_CONF_DIR/postgresql.conf" && sudo sed -i "s/^#\\?max_replication_slots.*/max_replication_slots = 10/" "$PG_CONF_DIR/postgresql.conf" || echo "max_replication_slots = 10" | sudo tee -a "$PG_CONF_DIR/postgresql.conf" >/dev/null

HBA="$PG_CONF_DIR/pg_hba.conf"
MARKER="# vpsdeploy-replication"
if ! grep -qF "$MARKER" "$HBA"; then
  echo "$MARKER" | sudo tee -a "$HBA" >/dev/null
  echo "host    replication    ${REPL_USER}    ${REPLICA_HOST}/32    scram-sha-256" | sudo tee -a "$HBA" >/dev/null
fi
if command -v ufw >/dev/null 2>&1; then
  sudo ufw allow from "$REPLICA_HOST" to any port "$DB_PORT" proto tcp comment "vpsdeploy-replication" || true
fi
sudo systemctl restart postgresql
echo "REPL_PASS=$PASS" > "$RESULT"
chmod 600 "$RESULT"
`, util.ShellQuote(resultPath), util.ShellQuote(replUser), util.ShellQuote(opts.ReplicaHost), port)

	if _, err := primary.RunScript("vpsdeploy-db-repl-primary.sh", primScript); err != nil {
		return err
	}
	raw, err := primary.ReadFile(resultPath)
	if err != nil {
		return err
	}
	_, _ = primary.Run(fmt.Sprintf("rm -f %s", util.ShellQuote(resultPath)))
	replPass := util.ParseEnvFile(string(raw))["REPL_PASS"]
	if replPass == "" {
		return fmt.Errorf("primary did not return replication password")
	}

	replica, err := ssh.New(ssh.Options{
		Host:    opts.ReplicaHost,
		User:    cfg.PostgresSSHUser(),
		KeyPath: cfg.Global.SSHKeyPath,
	})
	if err != nil {
		return err
	}
	defer replica.Close()

	fmt.Fprintf(os.Stdout, "Bootstrapping standby on %s from primary %s...\n", opts.ReplicaHost, primaryHost)

	standbyScript := fmt.Sprintf(`
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
PRIMARY=%s
REPL_USER=%s
REPL_PASS=%s
DB_PORT=%d

if command -v apt-get >/dev/null 2>&1; then
  if ! command -v psql >/dev/null 2>&1; then
    sudo apt-get update -qq
    sudo apt-get install -y -qq postgresql postgresql-contrib
  fi
fi

sudo systemctl stop postgresql || true
PGDATA=$(ls -d /var/lib/postgresql/*/main 2>/dev/null | head -1)
if [ -z "$PGDATA" ]; then
  echo "Could not find PGDATA" >&2
  exit 1
fi
sudo rm -rf "${PGDATA}.bak" || true
if [ -d "$PGDATA" ]; then
  sudo mv "$PGDATA" "${PGDATA}.bak"
fi
sudo -u postgres env PGPASSWORD=%s pg_basebackup -h "$PRIMARY" -p "$DB_PORT" -U "$REPL_USER" -D "$PGDATA" -Fp -Xs -P -R
sudo systemctl start postgresql
sleep 2
if sudo -u postgres psql -tAc "SELECT pg_is_in_recovery();" | grep -q t; then
  echo "Standby is in recovery (OK)"
else
  echo "WARNING: standby not in recovery — check logs" >&2
fi
`, util.ShellQuote(primaryHost), util.ShellQuote(replUser), util.ShellQuote(replPass), port, util.ShellQuote(replPass))

	if _, err := replica.RunScript("vpsdeploy-db-repl-standby.sh", standbyScript); err != nil {
		return err
	}

	fmt.Fprintln(os.Stdout, "")
	fmt.Fprintln(os.Stdout, "Replica bootstrap complete.")
	fmt.Fprintf(os.Stdout, "Point read traffic at: postgresql://%s:<app-password>@%s:%d/%s\n", dbUser, opts.ReplicaHost, port, dbName)
	fmt.Fprintf(os.Stdout, "Optional secret name: %s\n", cfg.PostgresReadSecretName())
	if opts.SaveSecret {
		fmt.Fprintln(os.Stdout, "Could not auto-save read URL (app DB password lives in your existing secret). Copy prod_db_url and change the host to the replica.")
	}
	return nil
}
