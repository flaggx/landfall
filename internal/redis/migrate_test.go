package redis

import (
	"errors"
	"strings"
	"testing"

	"github.com/flaggx/landfall/internal/config"
)

func TestSwitchSecretKeepsOldURLFirst(t *testing.T) {
	var saved []string
	store := map[string]string{}
	save := func(name, value string) error {
		saved = append(saved, name)
		store[name] = value
		return nil
	}
	if err := switchSecret("prod_redis_url", "redis://old@127.0.0.1:6379/0", "redis://new@127.0.0.1:6381/0", save); err != nil {
		t.Fatal(err)
	}
	if strings.Join(saved, ",") != "prod_redis_url_previous,prod_redis_url" {
		t.Fatalf("save order: %v", saved)
	}
	if store["prod_redis_url_previous"] != "redis://old@127.0.0.1:6379/0" || store["prod_redis_url"] != "redis://new@127.0.0.1:6381/0" {
		t.Fatalf("stored: %v", store)
	}
}

func TestSwitchSecretFailsBeforeSwitchingWhenOldURLCannotBeKept(t *testing.T) {
	var saved []string
	save := func(name, value string) error {
		saved = append(saved, name)
		if strings.HasSuffix(name, "_previous") {
			return errors.New("disk full")
		}
		return nil
	}
	err := switchSecret("prod_redis_url", "redis://old@127.0.0.1:6379/0", "redis://new@127.0.0.1:6381/0", save)
	if err == nil || !strings.Contains(err.Error(), "prod_redis_url_previous") {
		t.Fatalf("err = %v", err)
	}
	if len(saved) != 1 {
		t.Fatalf("the new URL was saved although the old one wasn't kept: %v", saved)
	}
}

func TestSwitchSecretWithoutOldURL(t *testing.T) {
	var saved []string
	save := func(name, value string) error { saved = append(saved, name); return nil }
	if err := switchSecret("dev_redis_url", "", "redis://new@127.0.0.1:6380/0", save); err != nil {
		t.Fatal(err)
	}
	if strings.Join(saved, ",") != "dev_redis_url" {
		t.Fatalf("save order: %v", saved)
	}
}

func TestRollbackHint(t *testing.T) {
	got := rollbackHint("prod", "prod_redis_url", true)
	want := `landfall secrets set prod_redis_url --value "$(landfall secrets get prod_redis_url_previous --reveal)" && landfall deploy --env prod`
	if !strings.Contains(got, want) {
		t.Fatalf("hint:\n%s\nwant it to contain:\n%s", got, want)
	}
	if got := rollbackHint("dev", "dev_redis_url", false); !strings.Contains(got, "set dev_redis_url to the old URL") {
		t.Fatalf("hint without a kept URL: %s", got)
	}
}

func TestMigrationSourceDB(t *testing.T) {
	const host = "203.0.113.10"
	env := func() config.Environment {
		return config.Environment{Host: host, Env: map[string]string{"REDIS_URL": "{{secret:x}}"}}
	}
	project := config.ProjectConfig{
		Project:      config.Project{Name: "app"},
		Environments: map[string]config.Environment{"prod": env(), "staging": env()},
	}
	resolved := &config.ResolvedConfig{Project: project, EnvName: "prod", Environment: project.Environments["prod"]}
	db := func(n int) *int { return &n }

	tests := []struct {
		name    string
		secrets map[string]string
		opts    MigrateOptions
		want    int
		wantErr string
	}{
		{
			name: "other environment on its own instance with the same index",
			secrets: map[string]string{
				"prod_redis_url":    "redis://app_prod:p@127.0.0.1:6379/0",
				"staging_redis_url": "redis://app_staging:p@127.0.0.1:6380/0",
			},
			want: 0,
		},
		{
			name: "other environment on another DB of the shared Redis",
			secrets: map[string]string{
				"prod_redis_url":    "redis://app_prod:p@127.0.0.1:6379/0",
				"staging_redis_url": "redis://app_staging:p@127.0.0.1:6379/2",
			},
			want: 0,
		},
		{
			name: "other environment on the same DB of the shared Redis",
			secrets: map[string]string{
				"prod_redis_url":    "redis://app_prod:p@127.0.0.1:6379/0",
				"staging_redis_url": "redis://app_staging:p@127.0.0.1:6379/0",
			},
			wantErr: "environment staging on the same host also uses DB 0",
		},
		{
			name: "a URL without a port is on the shared Redis",
			secrets: map[string]string{
				"prod_redis_url":    "redis://app_prod:p@127.0.0.1/0",
				"staging_redis_url": "redis://app_staging:p@127.0.0.1/0",
			},
			wantErr: "also uses DB 0",
		},
		{
			name: "no secrets: legacy per-environment defaults (prod 0, staging 2)",
			want: 0,
		},
		{
			name: "--allow-shared-db skips the check",
			secrets: map[string]string{
				"prod_redis_url":    "redis://app_prod:p@127.0.0.1:6379/0",
				"staging_redis_url": "redis://app_staging:p@127.0.0.1:6379/0",
			},
			opts: MigrateOptions{AllowSharedDB: true},
			want: 0,
		},
		{
			name: "--from-db overrides the secret",
			secrets: map[string]string{
				"prod_redis_url":    "redis://app_prod:p@127.0.0.1:6379/0",
				"staging_redis_url": "redis://app_staging:p@127.0.0.1:6380/0",
			},
			opts: MigrateOptions{FromDB: db(5)},
			want: 5,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := migrationSourceDB(resolved, tt.opts, tt.secrets, 6379)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got DB %d, want %d", got, tt.want)
			}
		})
	}
}
