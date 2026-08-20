package db

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flaggx/vpsdeploymentautomation/internal/config"
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
	script := buildScheduleScript(cfg, "vpsdeploy-db-backup-prod", "/var/backups/vpsdeploy/prod", "my_webapp_prod", 7, 3, false)
	for _, needle := range []string{
		"pg_dump -Fc",
		"OnCalendar=",
		"${UNIT}.timer",
		"RETAIN_DAYS=7",
		":00:00",
		"UNIT='vpsdeploy-db-backup-prod'",
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("missing %q", needle)
		}
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
	cfgDir := filepath.Join(home, ".config", "vpsdeploy")
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
	if prefix != "vpsdeploy/my-webapp/prod" {
		t.Fatalf("prefix: %s", prefix)
	}
	if ak != "ak" || sk != "sk" {
		t.Fatalf("keys: %s %s", ak, sk)
	}
}
