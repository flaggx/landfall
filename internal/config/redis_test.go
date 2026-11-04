package config

import (
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestRedisDefaults(t *testing.T) {
	cfg := &ResolvedConfig{
		Project: ProjectConfig{
			Project: Project{Name: "my-app"},
		},
		EnvName: "prod",
	}

	if cfg.RedisPort() != 6379 {
		t.Fatalf("expected port 6379, got %d", cfg.RedisPort())
	}
	if cfg.RedisDatabase() != 0 {
		t.Fatalf("expected db 0 for prod, got %d", cfg.RedisDatabase())
	}

	cfg.EnvName = "dev"
	if cfg.RedisDatabase() != 1 {
		t.Fatalf("expected db 1 for dev, got %d", cfg.RedisDatabase())
	}

	user, err := cfg.RedisUser()
	if err != nil {
		t.Fatal(err)
	}
	if user != "my_app_dev" {
		t.Fatalf("unexpected user %q", user)
	}

	if cfg.RedisSecretName() != "dev_redis_url" {
		t.Fatalf("unexpected secret name %q", cfg.RedisSecretName())
	}
}

func TestRedisCustomConfig(t *testing.T) {
	cfg := &ResolvedConfig{
		EnvName: "prod",
		Environment: Environment{
			Redis: &RedisConfig{
				Port:     6380,
				Database: intPtr(5),
				User:     "cache_user",
			},
		},
	}

	if cfg.RedisPort() != 6380 {
		t.Fatal()
	}
	if cfg.RedisDatabase() != 5 {
		t.Fatal()
	}
	user, err := cfg.RedisUser()
	if err != nil || user != "cache_user" {
		t.Fatalf("user=%q err=%v", user, err)
	}
}

func intPtr(v int) *int { return &v }

// A [redis] table without "database" must keep the per-environment default,
// so dev does not share prod's DB 0. An explicit 0 must still be honoured.
func TestRedisDatabaseFromTOML(t *testing.T) {
	for _, tc := range []struct {
		name, env, table string
		want             int
	}{
		{"dev, table without database", "dev", "port = 6379", 1},
		{"staging, table without database", "staging", `user = "cache"`, 2},
		{"dev, explicit 0", "dev", "database = 0", 0},
		{"prod, explicit 4", "prod", "database = 4", 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var env Environment
			if _, err := toml.Decode("[redis]\n"+tc.table+"\n", &env); err != nil {
				t.Fatal(err)
			}
			cfg := &ResolvedConfig{EnvName: tc.env, Environment: env}
			if got := cfg.RedisDatabase(); got != tc.want {
				t.Fatalf("RedisDatabase() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestValidateRedisDatabase(t *testing.T) {
	env := Environment{
		Host: "1.2.3.4", User: "deploy", Path: "/var/www", Branch: "main", Port: 3000,
		Redis: &RedisConfig{Database: intPtr(-1)},
	}
	if err := validateEnvironment("dev", env); err == nil || !strings.Contains(err.Error(), "redis.database") {
		t.Fatalf("expected a redis.database error, got %v", err)
	}
	env.Redis.Database = intPtr(0)
	if err := validateEnvironment("dev", env); err != nil {
		t.Fatal(err)
	}
}
