package redis

import (
	"strings"
	"testing"

	"github.com/flaggx/landfall/internal/config"
)

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
