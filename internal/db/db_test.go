package db

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/flaggx/landfall/internal/config"
)

func testCfg(t *testing.T, remote bool) *config.ResolvedConfig {
	t.Helper()
	cfg := &config.ResolvedConfig{
		Project: config.ProjectConfig{
			Project: config.Project{Name: "my-webapp", Repo: "git@github.com:a/b.git"},
		},
		EnvName: "prod",
		Environment: config.Environment{
			Host:   "203.0.113.10",
			User:   "deploy",
			Path:   "/var/www/app",
			Branch: "main",
			Port:   3000,
			Postgres: &config.PostgresConfig{
				Database: "my_webapp_prod",
				User:     "my_webapp_prod",
			},
		},
	}
	if remote {
		cfg.Environment.Postgres.Host = "203.0.113.20"
		cfg.Environment.Postgres.AppHost = "203.0.113.10"
		cfg.Environment.Postgres.Port = 5432
	}
	return cfg
}

func TestBuildBootstrapScriptCoLocated(t *testing.T) {
	cfg := testCfg(t, false)
	script := buildBootstrapScript(cfg, "my_webapp_prod", "my_webapp_prod", "/tmp/out.env", false)
	for _, needle := range []string{
		"CONNECT_HOST='localhost'",
		"REMOTE='false'",
		"CREATE USER",
		"CREATE DATABASE",
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("missing %q", needle)
		}
	}
	if !strings.Contains(script, "REMOTE='false'") {
		t.Fatal("expected REMOTE=false")
	}
	// listen_addresses may appear only inside the remote branch; ensure remote gate exists.
	if !strings.Contains(script, `if [ "$REMOTE" = "true" ]`) {
		t.Fatal("expected remote conditional")
	}
}

func TestBuildBootstrapScriptRemote(t *testing.T) {
	cfg := testCfg(t, true)
	script := buildBootstrapScript(cfg, "my_webapp_prod", "my_webapp_prod", "/tmp/out.env", true)
	for _, needle := range []string{
		"CONNECT_HOST='203.0.113.20'",
		"REMOTE='true'",
		"APP_HOST='203.0.113.10'",
		"listen_addresses",
		"pg_hba.conf",
		"ufw allow from",
		"RESET_PASSWORD='true'",
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("missing %q", needle)
		}
	}
}

func TestBuildScheduleScript(t *testing.T) {
	cfg := testCfg(t, false)
	script, err := buildScheduleScript(cfg, "landfall-db-backup-prod", "/var/backups/landfall/prod", "my_webapp_prod", 7, 3, false, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{
		"pg_dump -Fc",
		"OnCalendar=",
		"${UNIT}.timer",
		"RETAIN_DAYS=7",
		":00:00",
		"UNIT='landfall-db-backup-prod'",
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("missing %q", needle)
		}
	}
}

func TestScheduledBackupScript(t *testing.T) {
	// A secret full of shell syntax must survive installation untouched.
	upload := "\nexport AWS_SECRET_ACCESS_KEY='k$(touch /tmp/landfall-pwned)`id`'\naws s3 cp \"$FILE\" \"$DEST/$(basename \"$FILE\")\"\n"
	inner := scheduledBackupScript("/var/backups/landfall/prod", "my_webapp_prod", 7, upload, "")
	for _, needle := range []string{
		"DIR='/var/backups/landfall/prod'",
		"DB='my_webapp_prod'",
		`pg_dump -Fc -d "$DB" > "$FILE"`,
		"umask 027",
	} {
		if !strings.Contains(inner, needle) {
			t.Fatalf("missing %q in:\n%s", needle, inner)
		}
	}
	if strings.Contains(inner, "/tmp/landfall-pg.dump") {
		t.Fatalf("backup script must not use a shared temp file:\n%s", inner)
	}

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	check := exec.Command(bash, "-n")
	check.Stdin = strings.NewReader(inner)
	if out, err := check.CombinedOutput(); err != nil {
		t.Fatalf("bash -n failed: %v\n%s", err, out)
	}

	cfg := testCfg(t, false)
	outer, err := buildScheduleScript(cfg, "unit", "/var/backups/landfall/prod", "my_webapp_prod", 7, 3, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(outer, "<<'LANDFALL_BACKUP_SCRIPT'") {
		t.Fatalf("installer must write the backup script through a quoted heredoc:\n%s", outer)
	}

	// The quoted heredoc must reproduce the script byte for byte.
	heredoc := "cat <<'LANDFALL_BACKUP_SCRIPT'\n" + inner + "LANDFALL_BACKUP_SCRIPT\n"
	out, err := exec.Command(bash, "-c", heredoc).Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != inner {
		t.Fatalf("heredoc changed the script:\n--- want\n%s\n--- got\n%s", inner, out)
	}
}

func TestRestoreRequiresYesAndFile(t *testing.T) {
	cfg := testCfg(t, false)
	if err := Restore(cfg, "", false); err == nil {
		t.Fatal("expected error for empty file")
	}
	if err := Restore(cfg, "/tmp/x.dump", false); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("expected --yes error, got %v", err)
	}
}

func TestBootstrapReplicaRequiresHost(t *testing.T) {
	cfg := testCfg(t, false)
	err := BootstrapReplica(cfg, ReplicaOptions{})
	if err == nil || !strings.Contains(err.Error(), "replica-host") {
		t.Fatalf("expected replica-host error, got %v", err)
	}
}

func TestS3SettingsRequiresBucketAndSecrets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfgDir := filepath.Join(home, ".config", "landfall")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "secrets.toml"), []byte("[secrets]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := testCfg(t, false)
	_, _, _, _, _, _, err := s3Settings(cfg)
	if err == nil {
		t.Fatal("expected missing bucket error")
	}

	cfg.Environment.Postgres.BackupS3Bucket = "backups"
	_, _, _, _, _, _, err = s3Settings(cfg)
	if err == nil || !strings.Contains(err.Error(), "backup_s3_access_key") {
		t.Fatalf("expected access key error, got %v", err)
	}

	if err := os.WriteFile(filepath.Join(cfgDir, "secrets.toml"), []byte(`
[secrets]
backup_s3_access_key = "ak"
backup_s3_secret_key = "sk"
`), 0o600); err != nil {
		t.Fatal(err)
	}

	endpoint, bucket, prefix, region, ak, sk, err := s3Settings(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "https://s3.amazonaws.com" || bucket != "backups" || region != "auto" {
		t.Fatalf("defaults: %s %s %s", endpoint, bucket, region)
	}
	if prefix != "landfall/my-webapp/prod" {
		t.Fatalf("prefix: %s", prefix)
	}
	if ak != "ak" || sk != "sk" {
		t.Fatalf("keys: %s %s", ak, sk)
	}
}

// Runs the generated backup script with stub sudo and pg_dump, against a
// local server standing in for Healthchecks.io.
func TestScheduledBackupScriptPings(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not available")
	}

	var mu sync.Mutex
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.URL.Path)
		mu.Unlock()
	}))
	defer srv.Close()

	for _, tc := range []struct {
		name    string
		dump    string
		wantErr bool
		want    string
	}{
		{"backup succeeds", "echo dumpdata", false, "/ping/abc"},
		{"dump fails", "exit 1", true, "/ping/abc/fail"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mu.Lock()
			hits = nil
			mu.Unlock()
			bin := t.TempDir()
			// sudo -u postgres pg_dump ... -> run pg_dump directly.
			writeExec(t, filepath.Join(bin, "sudo"), "#!/bin/sh\n[ \"$1\" = -u ] && shift 2\nexec \"$@\"\n")
			writeExec(t, filepath.Join(bin, "pg_dump"), "#!/bin/sh\n"+tc.dump+"\n")

			dir := filepath.Join(t.TempDir(), "backups")
			script := scheduledBackupScript(dir, "app_prod", 7, "", srv.URL+"/ping/abc")
			cmd := exec.Command(bash, "-c", script)
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v\n%s", err, tc.wantErr, out)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(hits) != 1 || hits[0] != tc.want {
				t.Fatalf("pings = %v, want [%s]", hits, tc.want)
			}
		})
	}
}

func TestScheduledBackupScriptWithoutPing(t *testing.T) {
	inner := scheduledBackupScript("/var/backups/x", "db", 7, "", "")
	if strings.Contains(inner, "PING_URL") || strings.Contains(inner, "curl") {
		t.Fatalf("no ping configured, script must not curl:\n%s", inner)
	}
}

func TestValidatePingURL(t *testing.T) {
	for _, ok := range []string{"", "https://hc-ping.com/abc-123", "http://10.0.0.5:8000/ping/x"} {
		if err := validatePingURL(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"hc-ping.com/abc", "https://x/$(id)", "https://x/a'b", "https://x/a b", "ftp://x"} {
		if err := validatePingURL(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestScheduleUploadWithoutS3Fails(t *testing.T) {
	cfg := testCfg(t, false)
	if _, err := buildScheduleScript(cfg, "unit", "/var/backups/x", "db", 7, 3, true, ""); err == nil {
		t.Fatal("--upload without S3 settings must fail instead of silently skipping the upload")
	}
}

func writeExec(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}
