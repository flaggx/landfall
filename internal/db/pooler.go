package db

import (
	"fmt"
	"os"

	"github.com/flaggx/vpsdeploymentautomation/internal/config"
	"github.com/flaggx/vpsdeploymentautomation/internal/util"
)

type PoolerOptions struct {
	ListenPort int // default 6432
}

// BootstrapPooler installs PgBouncer on the DB host (or co-located app host)
// in front of local Postgres. Apps can set DATABASE_URL host to this machine
// and port 6432 once credentials match.
func BootstrapPooler(cfg *config.ResolvedConfig, opts PoolerOptions) error {
	listenPort := opts.ListenPort
	if listenPort <= 0 {
		listenPort = 6432
	}

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

	fmt.Fprintf(os.Stdout, "Installing PgBouncer on %s (port %d)...\n", cfg.PostgresSSHHost(), listenPort)

	script := fmt.Sprintf(`
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
DB_NAME=%s
DB_USER=%s
LISTEN_PORT=%d
APP_HOST=%s
CONNECT_HOST=%s

sudo apt-get update -qq
sudo apt-get install -y -qq pgbouncer

sudo tee /etc/pgbouncer/pgbouncer.ini >/dev/null <<EOF
[databases]
${DB_NAME} = host=127.0.0.1 port=5432 dbname=${DB_NAME}

[pgbouncer]
listen_addr = 0.0.0.0
listen_port = ${LISTEN_PORT}
auth_type = scram-sha-256
auth_file = /etc/pgbouncer/userlist.txt
admin_users = postgres
pool_mode = transaction
max_client_conn = 200
default_pool_size = 20
ignore_startup_parameters = extra_float_digits
EOF

if [ ! -f /etc/pgbouncer/userlist.txt ]; then
  printf '"%%s" "md5placeholder"\n' "$DB_USER" | sudo tee /etc/pgbouncer/userlist.txt >/dev/null
  sudo chmod 600 /etc/pgbouncer/userlist.txt
fi

if [ -n "$APP_HOST" ] && command -v ufw >/dev/null 2>&1; then
  sudo ufw allow from "$APP_HOST" to any port "$LISTEN_PORT" proto tcp comment "landfall-pgbouncer" || true
fi

sudo systemctl enable pgbouncer
sudo systemctl restart pgbouncer
sudo systemctl is-active pgbouncer

echo "PgBouncer listening on ${LISTEN_PORT}."
echo "Update /etc/pgbouncer/userlist.txt with the app role password, then restart pgbouncer."
echo "Point DATABASE_URL at host=${CONNECT_HOST} port=${LISTEN_PORT}"
`, util.ShellQuote(dbName), util.ShellQuote(dbUser), listenPort,
		util.ShellQuote(cfg.PostgresAppHost()), util.ShellQuote(cfg.PostgresConnectHost()))

	_, err = client.RunScript("landfall-db-pooler.sh", script)
	return err
}
