package config

import "testing"

func TestDefaultPostgresIdentifier(t *testing.T) {
	name := defaultPostgresIdentifier("my-webapp", "prod")
	if name != "my_webapp_prod" {
		t.Fatalf("unexpected identifier %q", name)
	}
}

func TestPostgresNamesFromConfig(t *testing.T) {
	cfg := &ResolvedConfig{
		Project: ProjectConfig{
			Project: Project{Name: "my-app"},
		},
		EnvName: "prod",
		Environment: Environment{
			Postgres: &PostgresConfig{
				Database: "custom_db",
				User:     "custom_user",
			},
		},
	}

	db, err := cfg.PostgresDatabase()
	if err != nil || db != "custom_db" {
		t.Fatalf("database: %q err=%v", db, err)
	}

	user, err := cfg.PostgresUser()
	if err != nil || user != "custom_user" {
		t.Fatalf("user: %q err=%v", user, err)
	}

	if cfg.PostgresSecretName() != "prod_db_url" {
		t.Fatalf("unexpected secret name %q", cfg.PostgresSecretName())
	}
}

func TestPostgresRemoteHelpers(t *testing.T) {
	cfg := &ResolvedConfig{
		Project: ProjectConfig{Project: Project{Name: "my-app"}},
		EnvName: "prod",
		Environment: Environment{
			Host: "203.0.113.10",
			User: "deploy",
			Postgres: &PostgresConfig{
				Host:    "203.0.113.20",
				Port:    5432,
				AppHost: "203.0.113.10",
			},
		},
	}

	if !cfg.PostgresIsRemote() {
		t.Fatal("expected remote")
	}
	if cfg.PostgresConnectHost() != "203.0.113.20" {
		t.Fatalf("connect host: %s", cfg.PostgresConnectHost())
	}
	if cfg.PostgresSSHHost() != "203.0.113.20" {
		t.Fatalf("ssh host: %s", cfg.PostgresSSHHost())
	}
	if cfg.PostgresAppHost() != "203.0.113.10" {
		t.Fatalf("app host: %s", cfg.PostgresAppHost())
	}
	if cfg.PostgresBackupDir() != "/var/backups/vpsdeploy/prod" {
		t.Fatalf("backup dir: %s", cfg.PostgresBackupDir())
	}
	if cfg.PostgresReadSecretName() != "prod_db_read_url" {
		t.Fatalf("read secret: %s", cfg.PostgresReadSecretName())
	}
}

func TestPostgresCoLocatedDefaults(t *testing.T) {
	cfg := &ResolvedConfig{
		EnvName:     "prod",
		Environment: Environment{Host: "203.0.113.10", User: "deploy"},
	}
	if cfg.PostgresIsRemote() {
		t.Fatal("expected co-located")
	}
	if cfg.PostgresConnectHost() != "localhost" {
		t.Fatal(cfg.PostgresConnectHost())
	}
	if cfg.PostgresSSHHost() != "203.0.113.10" {
		t.Fatal(cfg.PostgresSSHHost())
	}
}
