package config

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncodeDatabaseURL(t *testing.T) {
	cfg := &ResolvedConfig{}
	got := cfg.EncodeDatabaseURL("user", "p@ss:word", "db.example", 5432, "app_prod")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "postgresql" {
		t.Fatalf("scheme %s", u.Scheme)
	}
	pass, _ := u.User.Password()
	if pass != "p@ss:word" {
		t.Fatalf("pass %s", pass)
	}
	if u.Host != "db.example:5432" || u.Path != "/app_prod" {
		t.Fatalf("host/path %s %s", u.Host, u.Path)
	}
}

func TestValidateEnvironmentTable(t *testing.T) {
	base := Environment{
		Host: "1.2.3.4", User: "deploy", Path: "/var/www", Branch: "main", Port: 3000,
	}
	if err := validateEnvironment("prod", base); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		mut  func(*Environment)
		frag string
	}{
		{func(e *Environment) { e.Host = "" }, "host"},
		{func(e *Environment) { e.User = "" }, "user"},
		{func(e *Environment) { e.Path = "" }, "path"},
		{func(e *Environment) { e.Branch = "" }, "branch"},
		{func(e *Environment) { e.Port = 0 }, "port"},
	}
	for _, tt := range cases {
		env := base
		tt.mut(&env)
		err := validateEnvironment("prod", env)
		if err == nil || !strings.Contains(err.Error(), tt.frag) {
			t.Fatalf("frag %s: err=%v", tt.frag, err)
		}
	}
}

func TestSanitizePostgresIdentifier(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"My-App", "my_app"},
		{"123abc", "app_123abc"},
		{"...", "app"},
		{"ok_name", "ok_name"},
	}
	for _, tt := range tests {
		if got := sanitizePostgresIdentifier(tt.in); got != tt.want {
			t.Fatalf("sanitize(%q)=%q want %q", tt.in, got, tt.want)
		}
	}
}

func TestDefaultPostgresIdentifierTruncates(t *testing.T) {
	long := strings.Repeat("a", 80)
	got := defaultPostgresIdentifier(long, "prod")
	if len(got) > 63 {
		t.Fatalf("len %d: %s", len(got), got)
	}
}

func TestServiceNameAndSSHAddress(t *testing.T) {
	cfg := &ResolvedConfig{
		Project:     ProjectConfig{Project: Project{Name: "shop"}},
		EnvName:     "prod",
		Environment: Environment{Host: "203.0.113.10", User: "deploy"},
	}
	if cfg.ServiceName() != "shop-prod" {
		t.Fatal(cfg.ServiceName())
	}
	if cfg.SSHAddress() != "deploy@203.0.113.10" {
		t.Fatal(cfg.SSHAddress())
	}
}

func TestFindProjectConfigWalksUp(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(root, ProjectConfigName)
	if err := os.WriteFile(cfgPath, []byte("[project]\nname=\"x\"\nrepo=\"y\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	found, err := FindProjectConfig(nested)
	if err != nil {
		t.Fatal(err)
	}
	if found != cfgPath {
		t.Fatalf("got %s want %s", found, cfgPath)
	}
}

func TestFindProjectConfigMissing(t *testing.T) {
	dir := t.TempDir()
	if _, err := FindProjectConfig(dir); err == nil {
		t.Fatal("expected not found")
	}
}

func TestWriteAndLoadProjectConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ProjectConfigName)
	cfg := ProjectConfig{
		Project: Project{Name: "app", Repo: "git@github.com:a/b.git"},
		Environments: map[string]Environment{
			"prod": {Host: "1.2.3.4", User: "deploy", Path: "/var/www", Branch: "main", Port: 3000},
		},
	}
	if err := WriteProjectConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadProjectConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Project.Name != "app" || loaded.Environments["prod"].Port != 3000 {
		t.Fatalf("%#v", loaded)
	}
}

func TestPostgresBackupHelpers(t *testing.T) {
	cfg := &ResolvedConfig{
		Project: ProjectConfig{Project: Project{Name: "app"}},
		EnvName: "staging",
		Environment: Environment{
			Postgres: &PostgresConfig{
				BackupDir:        "/custom/backups",
				BackupRetain:     14,
				BackupS3Bucket:   "b",
				BackupS3Endpoint: "https://example.com",
			},
		},
	}
	if cfg.PostgresBackupDir() != "/custom/backups" {
		t.Fatal(cfg.PostgresBackupDir())
	}
	if cfg.PostgresBackupRetainDays() != 14 {
		t.Fatal(cfg.PostgresBackupRetainDays())
	}
	if !cfg.PostgresBackupS3Configured() {
		t.Fatal("expected s3 configured")
	}
}

func TestResolveSecretRefNotAPlaceholder(t *testing.T) {
	secrets := SecretsConfig{Secrets: map[string]string{"x": "1"}}
	got, err := resolveSecretRef("{{notasecret}}", secrets)
	if err != nil {
		t.Fatal(err)
	}
	if got != "{{notasecret}}" {
		t.Fatalf("got %q", got)
	}
}
