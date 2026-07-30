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

func TestValidatePostgresIdentifier(t *testing.T) {
	if _, err := validatePostgresIdentifier("1bad", "user"); err == nil {
		t.Fatal("expected error for invalid identifier")
	}
	if _, err := validatePostgresIdentifier("good_user", "user"); err != nil {
		t.Fatal(err)
	}
}
