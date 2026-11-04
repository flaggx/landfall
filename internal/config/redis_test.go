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

	// Each environment has its own instance: the port is picked by bootstrap
	// (0 here), the DB is 0, and prod has no memory limit.
	if cfg.RedisPort() != 0 {
		t.Fatalf("expected port 0 (picked on the server), got %d", cfg.RedisPort())
	}
	if cfg.RedisDatabase() != 0 {
		t.Fatalf("expected db 0, got %d", cfg.RedisDatabase())
	}
	if cfg.RedisMaxmemory() != "0" || cfg.RedisMaxmemoryPolicy() != "noeviction" {
		t.Fatalf("prod: maxmemory %q policy %q", cfg.RedisMaxmemory(), cfg.RedisMaxmemoryPolicy())
	}
	if name, err := cfg.RedisInstance(); err != nil || name != "my-app-prod" {
		t.Fatalf("instance %q, %v", name, err)
	}

	cfg.EnvName = "dev"
	if cfg.RedisDatabase() != 0 || cfg.LegacyRedisDatabase() != 1 {
		t.Fatalf("dev: db %d, legacy shared db %d", cfg.RedisDatabase(), cfg.LegacyRedisDatabase())
	}
	if cfg.RedisMaxmemory() != "128mb" {
		t.Fatalf("dev: maxmemory %q, want 128mb", cfg.RedisMaxmemory())
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

// Where an environment's data sits on the old shared instance: a [redis]
// table without "database" means the per-environment index, so dev did not
// share prod's DB 0. An explicit value is honoured.
func TestLegacyRedisDatabaseFromTOML(t *testing.T) {
	for _, tc := range []struct {
		name, env, table string
		want             int
	}{
		{"dev, table without database", "dev", "maxmemory = \"64mb\"", 1},
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
			if got := cfg.LegacyRedisDatabase(); got != tc.want {
				t.Fatalf("LegacyRedisDatabase() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestRedisInstanceName(t *testing.T) {
	for _, tc := range []struct {
		project, env, explicit, want string
		wantErr                      bool
	}{
		{"my-webapp", "prod", "", "my-webapp-prod", false},
		{"My App", "dev", "", "my-app-dev", false}, // spaces and case made safe
		{"shop.example", "dev", "", "shop-example-dev", false},
		{"x", "dev", "custom_name", "custom_name", false},
		{"x", "dev", "Bad Name", "", true},
		{"!!!", "", "", "", true},
	} {
		cfg := &ResolvedConfig{Project: ProjectConfig{Project: Project{Name: tc.project}}, EnvName: tc.env}
		if tc.explicit != "" {
			cfg.Environment.Redis = &RedisConfig{Instance: tc.explicit}
		}
		got, err := cfg.RedisInstance()
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("%q/%q/%q: got %q, %v; want %q (error %v)", tc.project, tc.env, tc.explicit, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestValidateRedisConfig(t *testing.T) {
	for _, tc := range []struct {
		r    RedisConfig
		frag string
	}{
		{RedisConfig{Port: 6379}, "shared Redis"},
		{RedisConfig{Port: 70000}, "port"},
		{RedisConfig{Maxmemory: "lots"}, "maxmemory"},
		{RedisConfig{MaxmemoryPolicy: "lru"}, "maxmemory_policy"},
		{RedisConfig{Instance: "a b"}, "instance"},
	} {
		err := validateRedisConfig("dev", &tc.r)
		if err == nil || !strings.Contains(err.Error(), tc.frag) {
			t.Errorf("%+v: err = %v, want it to mention %q", tc.r, err, tc.frag)
		}
	}
	for _, ok := range []RedisConfig{{}, {Port: 6390, Maxmemory: "1GB", MaxmemoryPolicy: "allkeys-lru", Instance: "shop-dev"}, {Maxmemory: "0"}} {
		if err := validateRedisConfig("dev", &ok); err != nil {
			t.Errorf("%+v: %v", ok, err)
		}
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
