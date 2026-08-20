package db

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/flaggx/vpsdeploymentautomation/internal/config"
	"github.com/flaggx/vpsdeploymentautomation/internal/util"
)

type BackupOptions struct {
	Upload bool
}

type ScheduleOptions struct {
	Upload bool
	Hour   int // UTC hour 0-23, default 3
}

func Backup(cfg *config.ResolvedConfig, opts BackupOptions) error {
	dbName, err := cfg.PostgresDatabase()
	if err != nil {
		return err
	}

	client, err := dbSSHClient(cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	dir := cfg.PostgresBackupDir()
	retain := cfg.PostgresBackupRetainDays()
	stamp := time.Now().UTC().Format("20060102T150405Z")
	filename := fmt.Sprintf("%s_%s.dump", dbName, stamp)
	remotePath := strings.TrimRight(dir, "/") + "/" + filename

	fmt.Fprintf(os.Stdout, "Backing up %s on %s → %s\n", dbName, cfg.PostgresSSHHost(), remotePath)

	script := fmt.Sprintf(`
set -euo pipefail
DIR=%s
FILE=%s
DB=%s
RETAIN_DAYS=%d

sudo mkdir -p "$DIR"
sudo chown root:root "$DIR"
sudo chmod 750 "$DIR"

sudo -u postgres pg_dump -Fc -d "$DB" -f "/tmp/landfall-pg.dump"
sudo mv /tmp/landfall-pg.dump "$FILE"
sudo chmod 640 "$FILE"

# Prune old dumps
sudo find "$DIR" -type f -name '*.dump' -mtime +"$RETAIN_DAYS" -delete

ls -lh "$FILE"
echo "BACKUP_PATH=$FILE"
`, util.ShellQuote(dir), util.ShellQuote(remotePath), util.ShellQuote(dbName), retain)

	if _, err := client.RunScript("landfall-db-backup.sh", script); err != nil {
		return err
	}

	if opts.Upload {
		if err := uploadBackup(cfg, remotePath); err != nil {
			return err
		}
	} else if cfg.PostgresBackupS3Configured() {
		fmt.Fprintln(os.Stdout, "S3 bucket configured — re-run with --upload to push off-box.")
	}

	fmt.Fprintln(os.Stdout, "Backup complete.")
	return nil
}

func ListBackups(cfg *config.ResolvedConfig) error {
	client, err := dbSSHClient(cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	dir := cfg.PostgresBackupDir()
	script := fmt.Sprintf(`
set -euo pipefail
DIR=%s
echo "Local backups in $DIR:"
if [ -d "$DIR" ]; then
  sudo ls -lh "$DIR"/*.dump 2>/dev/null || echo "  (none)"
else
  echo "  (directory missing — run: landfall db backup)"
fi
`, util.ShellQuote(dir))

	_, err = client.RunScript("landfall-db-backups.sh", script)
	return err
}

func Restore(cfg *config.ResolvedConfig, file string, yes bool) error {
	if strings.TrimSpace(file) == "" {
		return fmt.Errorf("--file is required (path on the DB host, e.g. /var/backups/landfall/prod/foo.dump)")
	}
	if !yes {
		return fmt.Errorf("refusing to restore without --yes (destructive)")
	}

	dbName, err := cfg.PostgresDatabase()
	if err != nil {
		return err
	}

	client, err := dbSSHClient(cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	fmt.Fprintf(os.Stdout, "Restoring %s from %s on %s\n", dbName, file, cfg.PostgresSSHHost())

	script := fmt.Sprintf(`
set -euo pipefail
FILE=%s
DB=%s

if [ ! -f "$FILE" ]; then
  echo "Backup file not found: $FILE" >&2
  exit 1
fi

# Terminate open sessions then restore into existing DB
sudo -u postgres psql -d postgres -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='${DB}' AND pid <> pg_backend_pid();" || true
sudo -u postgres pg_restore --clean --if-exists -d "$DB" "$FILE"
echo "Restore finished."
`, util.ShellQuote(file), util.ShellQuote(dbName))

	_, err = client.RunScript("landfall-db-restore.sh", script)
	return err
}

func Schedule(cfg *config.ResolvedConfig, opts ScheduleOptions) error {
	client, err := dbSSHClient(cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	hour := opts.Hour
	if hour < 0 || hour > 23 {
		hour = 3
	}

	unit := fmt.Sprintf("landfall-db-backup-%s", cfg.EnvName)
	dir := cfg.PostgresBackupDir()
	dbName, err := cfg.PostgresDatabase()
	if err != nil {
		return err
	}
	retain := cfg.PostgresBackupRetainDays()

	script := buildScheduleScript(cfg, unit, dir, dbName, retain, hour, opts.Upload)
	_, err = client.RunScript("landfall-db-schedule.sh", script)
	return err
}

func buildScheduleScript(cfg *config.ResolvedConfig, unit, dir, dbName string, retain, hour int, upload bool) string {
	uploadSnippet := "true"
	if upload && cfg.PostgresBackupS3Configured() {
		endpoint, bucket, prefix, region, accessKey, secretKey, err := s3Settings(cfg)
		if err == nil {
			destPrefix := strings.Trim(prefix, "/")
			uploadSnippet = fmt.Sprintf(`
export AWS_ACCESS_KEY_ID=%s
export AWS_SECRET_ACCESS_KEY=%s
export AWS_DEFAULT_REGION=%s
if ! command -v aws >/dev/null 2>&1; then
  apt-get update -qq && apt-get install -y -qq awscli || true
fi
if command -v aws >/dev/null 2>&1; then
  aws s3 cp "$FILE" "s3://%s/%s/$(basename "$FILE")" --endpoint-url %s
fi
`, util.ShellQuote(accessKey), util.ShellQuote(secretKey), util.ShellQuote(region),
				bucket, destPrefix, util.ShellQuote(endpoint))
		}
	}

	return fmt.Sprintf(`
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
UNIT=%s
DIR=%s
DB=%s
RETAIN_DAYS=%d
HOUR=%d

sudo tee /usr/local/bin/${UNIT}.sh >/dev/null <<EOF
#!/bin/bash
set -euo pipefail
DIR=${DIR}
DB=${DB}
RETAIN_DAYS=${RETAIN_DAYS}
STAMP=\$(date -u +%%Y%%m%%dT%%H%%M%%SZ)
FILE="\$DIR/\${DB}_\${STAMP}.dump"
mkdir -p "\$DIR"
chmod 750 "\$DIR"
sudo -u postgres pg_dump -Fc -d "\$DB" -f /tmp/landfall-pg.dump
mv /tmp/landfall-pg.dump "\$FILE"
chmod 640 "\$FILE"
find "\$DIR" -type f -name '*.dump' -mtime +"\$RETAIN_DAYS" -delete
%s
EOF
sudo chmod 750 /usr/local/bin/${UNIT}.sh

sudo tee /etc/systemd/system/${UNIT}.service >/dev/null <<EOF
[Unit]
Description=landfall PostgreSQL backup (${UNIT})
After=postgresql.service

[Service]
Type=oneshot
ExecStart=/usr/local/bin/${UNIT}.sh
EOF

sudo tee /etc/systemd/system/${UNIT}.timer >/dev/null <<EOF
[Unit]
Description=Daily landfall PostgreSQL backup (${UNIT})

[Timer]
OnCalendar=*-*-* ${HOUR}:00:00
Persistent=true
Unit=${UNIT}.service

[Install]
WantedBy=timers.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now ${UNIT}.timer
sudo systemctl list-timers ${UNIT}.timer --no-pager || true
echo "Scheduled ${UNIT}.timer at ${HOUR}:00 UTC"
`, util.ShellQuote(unit), util.ShellQuote(dir), util.ShellQuote(dbName), retain, hour, uploadSnippet)
}

func uploadBackup(cfg *config.ResolvedConfig, remoteFile string) error {
	endpoint, bucket, prefix, region, accessKey, secretKey, err := s3Settings(cfg)
	if err != nil {
		return err
	}

	client, err := dbSSHClient(cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	destKey := strings.Trim(prefix, "/") + "/" + remoteFile[strings.LastIndex(remoteFile, "/")+1:]
	fmt.Fprintf(os.Stdout, "Uploading to s3://%s/%s (endpoint %s)\n", bucket, destKey, endpoint)

	script := fmt.Sprintf(`
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
FILE=%s
if ! command -v aws >/dev/null 2>&1; then
  sudo apt-get update -qq
  sudo apt-get install -y -qq awscli
fi
export AWS_ACCESS_KEY_ID=%s
export AWS_SECRET_ACCESS_KEY=%s
export AWS_DEFAULT_REGION=%s
aws s3 cp "$FILE" "s3://%s/%s" --endpoint-url %s
`, util.ShellQuote(remoteFile), util.ShellQuote(accessKey), util.ShellQuote(secretKey),
		util.ShellQuote(region), bucket, destKey, util.ShellQuote(endpoint))

	_, err = client.RunScript("landfall-db-backup-upload.sh", script)
	return err
}

func s3Settings(cfg *config.ResolvedConfig) (endpoint, bucket, prefix, region, accessKey, secretKey string, err error) {
	p := cfg.Environment.Postgres
	if p == nil || strings.TrimSpace(p.BackupS3Bucket) == "" {
		return "", "", "", "", "", "", fmt.Errorf("configure [environments.%s.postgres] backup_s3_bucket (and endpoint) for uploads", cfg.EnvName)
	}
	endpoint = strings.TrimSpace(p.BackupS3Endpoint)
	if endpoint == "" {
		endpoint = "https://s3.amazonaws.com"
	}
	bucket = strings.TrimSpace(p.BackupS3Bucket)
	prefix = strings.TrimSpace(p.BackupS3Prefix)
	if prefix == "" {
		prefix = fmt.Sprintf("landfall/%s/%s", cfg.Project.Project.Name, cfg.EnvName)
	}
	region = strings.TrimSpace(p.BackupS3Region)
	if region == "" {
		region = "auto"
	}

	secrets, err := config.LoadSecrets()
	if err != nil {
		return "", "", "", "", "", "", err
	}
	accessKey = secrets.Secrets["backup_s3_access_key"]
	secretKey = secrets.Secrets["backup_s3_secret_key"]
	if accessKey == "" || secretKey == "" {
		return "", "", "", "", "", "", fmt.Errorf("set secrets backup_s3_access_key and backup_s3_secret_key")
	}
	return endpoint, bucket, prefix, region, accessKey, secretKey, nil
}
