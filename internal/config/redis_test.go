package config

import "testing"

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
				Database: 5,
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
