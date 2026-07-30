package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveSecretRef(t *testing.T) {
	secrets := SecretsConfig{
		Secrets: map[string]string{
			"prod_db_url": "postgres://localhost/db",
		},
	}

	val, err := resolveSecretRef("{{secret:prod_db_url}}", secrets)
	if err != nil {
		t.Fatal(err)
	}
	if val != "postgres://localhost/db" {
		t.Fatalf("expected secret value, got %q", val)
	}

	plain, err := resolveSecretRef("plain-value", secrets)
	if err != nil {
		t.Fatal(err)
	}
	if plain != "plain-value" {
		t.Fatalf("expected plain value, got %q", plain)
	}

	_, err = resolveSecretRef("{{secret:missing}}", secrets)
	if err == nil {
		t.Fatal("expected error for missing secret")
	}
}

func TestValidateProjectConfig(t *testing.T) {
	err := validateProjectConfig(ProjectConfig{})
	if err == nil {
		t.Fatal("expected validation error for empty config")
	}

	err = validateProjectConfig(ProjectConfig{
		Project: Project{Name: "app", Repo: "git@github.com:a/b.git"},
		Environments: map[string]Environment{
			"prod": {Host: "1.2.3.4", User: "deploy", Path: "/var/www", Branch: "main", Port: 3000},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLoadWithOptionsSkipSecrets(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ProjectConfigName)
	content := `
[project]
name = "app"
repo = "git@github.com:a/b.git"

[environments.prod]
host = "1.2.3.4"
user = "deploy"
path = "/var/www/app"
branch = "main"
port = 3000

[environments.prod.env]
DATABASE_URL = "{{secret:prod_db_url}}"
NODE_ENV = "production"
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadWithOptions("prod", dir, LoadOptions{ResolveSecrets: false})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Environment.Env["DATABASE_URL"]; got != "{{secret:prod_db_url}}" {
		t.Fatalf("expected unresolved secret ref, got %q", got)
	}

	_, err = LoadWithOptions("prod", dir, LoadOptions{ResolveSecrets: true})
	if err == nil {
		t.Fatal("expected error when resolving missing secrets")
	}
}

func TestFindProjectConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ProjectConfigName)
	if err := os.WriteFile(path, []byte("[project]\nname=\"x\"\nrepo=\"y\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	found, err := FindProjectConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if found != path {
		t.Fatalf("expected %s, got %s", path, found)
	}
}
