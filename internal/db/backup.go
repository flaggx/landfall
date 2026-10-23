package db

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/flaggx/landfall/internal/config"
	"github.com/flaggx/landfall/internal/util"
)

type BackupOptions struct {
	Upload bool
}

type ScheduleOptions struct {
	Upload bool
	Hour   int // UTC hour 0-23, default 3
	// PingURL is an optional dead-man's-switch URL (e.g. Healthchecks.io):
	// requested after each successful backup, and at PingURL/fail when one fails.
	PingURL string
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

# Stream the dump straight into the root-only backup directory; a temp file
# in /tmp would be readable by every user on the host while it is written.
sudo -u postgres pg_dump -Fc -d "$DB" | sudo tee "$FILE" >/dev/null
sudo chmod 640 "$FILE"
sudo test -s "$FILE"

# Prune old dumps
sudo find "$DIR" -type f -name '*.dump' -mtime +"$RETAIN_DAYS" -delete

sudo ls -lh "$FILE"
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
  sudo sh -c 'ls -lh "$1"/*.dump' _ "$DIR" 2>/dev/null || echo "  (none)"
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

if ! sudo test -f "$FILE"; then
  echo "Backup file not found: $FILE" >&2
  exit 1
fi

# Terminate open sessions then restore into existing DB
sudo -u postgres psql -d postgres -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='${DB}' AND pid <> pg_backend_pid();" || true
# The backup directory is root-only, so feed the dump to pg_restore on stdin.
sudo cat "$FILE" | sudo -u postgres pg_restore --clean --if-exists -d "$DB"
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
	if err := validatePingURL(opts.PingURL); err != nil {
		return err
	}

	script, err := buildScheduleScript(cfg, unit, dir, dbName, retain, hour, opts.Upload, opts.PingURL)
	if err != nil {
		return err
	}
	_, err = client.RunScript("landfall-db-schedule.sh", script)
	return err
}

// validatePingURL accepts only plain http(s) URLs, since the URL ends up in a
// root-run shell script.
func validatePingURL(pingURL string) error {
	if pingURL == "" {
		return nil
	}
	if !strings.HasPrefix(pingURL, "https://") && !strings.HasPrefix(pingURL, "http://") {
		return fmt.Errorf("--ping-url must be an http(s) URL, got %q", pingURL)
	}
	if strings.ContainsAny(pingURL, " '\"\\`$\n") {
		return fmt.Errorf("--ping-url contains characters that are not allowed: %q", pingURL)
	}
	return nil
}

func buildScheduleScript(cfg *config.ResolvedConfig, unit, dir, dbName string, retain, hour int, upload bool, pingURL string) (string, error) {
	uploadSnippet := ""
	if upload {
		if !cfg.PostgresBackupS3Configured() {
			return "", fmt.Errorf("--upload needs S3 settings for this environment (see the README, \"Backups\")")
		}
		endpoint, bucket, prefix, region, accessKey, secretKey, err := s3Settings(cfg)
		if err != nil {
			return "", err
		}
		{
			dest := fmt.Sprintf("s3://%s/%s", bucket, strings.Trim(prefix, "/"))
			uploadSnippet = fmt.Sprintf(`
export AWS_ACCESS_KEY_ID=%s
export AWS_SECRET_ACCESS_KEY=%s
export AWS_DEFAULT_REGION=%s
DEST=%s
if ! command -v aws >/dev/null 2>&1; then
  apt-get update -qq && apt-get install -y -qq awscli || true
fi
if ! command -v aws >/dev/null 2>&1; then
  echo "aws CLI is missing, so the dump was not uploaded" >&2
  exit 1
fi
aws s3 cp "$FILE" "$DEST/$(basename "$FILE")" --endpoint-url %s
`, util.ShellQuote(accessKey), util.ShellQuote(secretKey), util.ShellQuote(region),
				util.ShellQuote(dest), util.ShellQuote(endpoint))
		}
	}

	// The backup script is written through a quoted heredoc, so nothing in it
	// (including the S3 keys) is expanded or executed while it is installed.
	return fmt.Sprintf(`
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
UNIT=%s
HOUR=%d

sudo tee /usr/local/bin/${UNIT}.sh >/dev/null <<'LANDFALL_BACKUP_SCRIPT'
%sLANDFALL_BACKUP_SCRIPT
sudo chmod 750 /usr/local/bin/${UNIT}.sh

sudo tee /etc/systemd/system/${UNIT}.service >/dev/null <<UNIT_FILE
[Unit]
Description=landfall PostgreSQL backup (${UNIT})
After=postgresql.service

[Service]
Type=oneshot
ExecStart=/usr/local/bin/${UNIT}.sh
UNIT_FILE

sudo tee /etc/systemd/system/${UNIT}.timer >/dev/null <<UNIT_FILE
[Unit]
Description=Daily landfall PostgreSQL backup (${UNIT})

[Timer]
OnCalendar=*-*-* ${HOUR}:00:00
Persistent=true
Unit=${UNIT}.service

[Install]
WantedBy=timers.target
UNIT_FILE

sudo systemctl daemon-reload
sudo systemctl enable --now ${UNIT}.timer
sudo systemctl list-timers ${UNIT}.timer --no-pager || true
echo "Scheduled ${UNIT}.timer at ${HOUR}:00 UTC"
`, util.ShellQuote(unit), hour, scheduledBackupScript(dir, dbName, retain, uploadSnippet, pingURL)), nil
}

// scheduledBackupScript is the script the systemd timer runs as root.
func scheduledBackupScript(dir, dbName string, retain int, uploadSnippet, pingURL string) string {
	pingStart, pingEnd := "", ""
	if pingURL != "" {
		// Any failure (set -e) pings /fail; the success ping is the last line,
		// so it only goes out once the dump (and upload) really happened.
		pingStart = fmt.Sprintf("PING_URL=%s\ntrap 'curl -fsS -m 10 --retry 3 \"$PING_URL/fail\" >/dev/null 2>&1 || true' ERR\n", util.ShellQuote(pingURL))
		pingEnd = "curl -fsS -m 10 --retry 3 \"$PING_URL\" >/dev/null 2>&1 || true\n"
	}
	return fmt.Sprintf(`#!/bin/bash
set -euo pipefail
%sDIR=%s
DB=%s
RETAIN_DAYS=%d
STAMP=$(date -u +%%Y%%m%%dT%%H%%M%%SZ)
FILE="$DIR/${DB}_${STAMP}.dump"
mkdir -p "$DIR"
chmod 750 "$DIR"
# Write the dump straight into the root-only directory (no temp file in /tmp).
umask 027
sudo -u postgres pg_dump -Fc -d "$DB" > "$FILE"
chmod 640 "$FILE"
test -s "$FILE"
find "$DIR" -type f -name '*.dump' -mtime +"$RETAIN_DAYS" -delete
%s%s`, pingStart, util.ShellQuote(dir), util.ShellQuote(dbName), retain, uploadSnippet, pingEnd)
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
sudo cat "$FILE" | aws s3 cp - "s3://%s/%s" --endpoint-url %s
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
