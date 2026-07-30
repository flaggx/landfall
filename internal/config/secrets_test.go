package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSecretsCRUD(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	path, err := InitSecretsFile()
	if err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected secrets file mode 0600, got %o", info.Mode().Perm())
	}

	if err := SetSecret("prod_db_url", "postgres://localhost/db"); err != nil {
		t.Fatal(err)
	}

	value, err := GetSecret("prod_db_url")
	if err != nil {
		t.Fatal(err)
	}
	if value != "postgres://localhost/db" {
		t.Fatalf("unexpected value %q", value)
	}

	keys, err := ListSecretKeys()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0] != "prod_db_url" {
		t.Fatalf("unexpected keys: %v", keys)
	}

	if err := DeleteSecret("prod_db_url"); err != nil {
		t.Fatal(err)
	}

	_, err = GetSecret("prod_db_url")
	if err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestCollectSecretRefs(t *testing.T) {
	cfg := ProjectConfig{
		Environments: map[string]Environment{
			"prod": {
				Env: map[string]string{
					"DATABASE_URL": "{{secret:prod_db_url}}",
					"API_KEY":      "{{secret:prod_api_key}}",
					"NODE_ENV":     "production",
				},
			},
			"dev": {
				Env: map[string]string{
					"DATABASE_URL": "{{secret:dev_db_url}}",
				},
			},
		},
	}

	refs := CollectSecretRefs(cfg)
	if len(refs) != 3 {
		t.Fatalf("expected 3 refs, got %d: %v", len(refs), refs)
	}
}

func TestCheckSecretsForProject(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	t.Setenv("HOME", home)

	configPath := filepath.Join(project, ProjectConfigName)
	content := `
[project]
name = "app"
repo = "git@github.com:you/app.git"

[environments.prod]
host = "1.2.3.4"
user = "deploy"
path = "/var/www/app"
branch = "main"
port = 3000

[environments.prod.env]
DATABASE_URL = "{{secret:prod_db_url}}"
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	required, missing, err := CheckSecretsForProject(project)
	if err != nil {
		t.Fatal(err)
	}
	if len(required) != 1 || required[0] != "prod_db_url" {
		t.Fatalf("unexpected required: %v", required)
	}
	if len(missing) != 1 {
		t.Fatalf("expected one missing secret, got %v", missing)
	}

	if err := SetSecret("prod_db_url", "postgres://localhost/db"); err != nil {
		t.Fatal(err)
	}

	_, missing, err = CheckSecretsForProject(project)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Fatalf("expected no missing secrets, got %v", missing)
	}
}

func TestValidateSecretKey(t *testing.T) {
	if err := validateSecretKey(""); err == nil {
		t.Fatal("expected error for empty key")
	}
	if err := validateSecretKey("prod_db_url"); err != nil {
		t.Fatal(err)
	}
	if err := validateSecretKey("bad-key"); err == nil {
		t.Fatal("expected error for invalid key")
	}
}
