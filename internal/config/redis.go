package config

import (
	"fmt"
	"regexp"
	"strings"
)

type RedisConfig struct {
	// Port of the environment's own Redis instance. 0 (omitted) means
	// bootstrap picks a free port and records it in the instance's config.
	Port int `toml:"port"`
	// Database is a pointer so an omitted value can be told apart from an
	// explicit 0. Each environment has its own instance, so the default is 0.
	Database *int   `toml:"database"`
	User     string `toml:"user"`
	// Instance names the environment's Redis instance (redis-server@<name>).
	// Defaults to the service name, <project>-<env>.
	Instance string `toml:"instance"`
	// Maxmemory caps the instance's memory, in Redis's syntax ("128mb",
	// "1gb", "0" for no limit). Defaults to 128mb, or no limit for prod.
	Maxmemory string `toml:"maxmemory"`
	// MaxmemoryPolicy is what Redis does at the limit. Defaults to
	// noeviction: writes fail instead of keys silently disappearing, which
	// queues (e.g. BullMQ) require. allkeys-lru suits a pure cache.
	MaxmemoryPolicy string `toml:"maxmemory_policy"`
}

const (
	// LegacySharedRedisPort is the host-wide Redis that Landfall used for every
	// environment before each environment got its own instance.
	LegacySharedRedisPort = 6379
	// RedisInstancePortMin and RedisInstancePortMax bound the ports bootstrap
	// picks from for an instance without an explicit port.
	RedisInstancePortMin = 6380
	RedisInstancePortMax = 6479

	defaultRedisMaxmemory       = "128mb"
	defaultRedisMaxmemoryPolicy = "noeviction"
)

var (
	redisInstancePattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	redisInstanceUnsafe    = regexp.MustCompile(`[^a-z0-9_-]+`)
	redisMaxmemoryPattern  = regexp.MustCompile(`^(?i)[0-9]+(b|k|kb|m|mb|g|gb)?$`)
	redisMaxmemoryPolicies = map[string]bool{
		"noeviction": true, "allkeys-lru": true, "allkeys-lfu": true, "allkeys-random": true,
		"volatile-lru": true, "volatile-lfu": true, "volatile-random": true, "volatile-ttl": true,
	}
)

// RedisPort is the configured port of the environment's own instance, or 0
// when bootstrap should pick one.
func (c *ResolvedConfig) RedisPort() int {
	if c.Environment.Redis != nil && c.Environment.Redis.Port > 0 {
		return c.Environment.Redis.Port
	}
	return 0
}

// RedisDatabase is the DB index in the environment's own instance.
func (c *ResolvedConfig) RedisDatabase() int {
	if c.Environment.Redis != nil && c.Environment.Redis.Database != nil {
		return *c.Environment.Redis.Database
	}
	return 0
}

// LegacyRedisDatabase is the DB index the environment used on the shared
// instance, where environments were told apart by index. Used to find the
// data to migrate.
func (c *ResolvedConfig) LegacyRedisDatabase() int {
	if c.Environment.Redis != nil && c.Environment.Redis.Database != nil {
		return *c.Environment.Redis.Database
	}
	return defaultRedisDatabase(c.EnvName)
}

// RedisInstance names the environment's Redis instance: redis.instance, or
// the service name made safe for a systemd unit and file names.
func (c *ResolvedConfig) RedisInstance() (string, error) {
	if c.Environment.Redis != nil && c.Environment.Redis.Instance != "" {
		name := c.Environment.Redis.Instance
		if !redisInstancePattern.MatchString(name) {
			return "", fmt.Errorf("environments.%s.redis.instance %q must be lowercase letters, digits, _ and -, at most 64 characters", c.EnvName, name)
		}
		return name, nil
	}
	name := strings.Trim(redisInstanceUnsafe.ReplaceAllString(strings.ToLower(c.ServiceName()), "-"), "-_")
	if len(name) > 64 {
		name = strings.TrimRight(name[:64], "-_")
	}
	if !redisInstancePattern.MatchString(name) {
		return "", fmt.Errorf("cannot derive a Redis instance name from %q; set environments.%s.redis.instance", c.ServiceName(), c.EnvName)
	}
	return name, nil
}

// RedisMaxmemory is the instance's memory limit in Redis's syntax.
func (c *ResolvedConfig) RedisMaxmemory() string {
	if c.Environment.Redis != nil && c.Environment.Redis.Maxmemory != "" {
		return strings.ToLower(c.Environment.Redis.Maxmemory)
	}
	if isProdEnv(c.EnvName) {
		return "0"
	}
	return defaultRedisMaxmemory
}

// RedisMaxmemoryPolicy is what the instance does when it reaches maxmemory.
func (c *ResolvedConfig) RedisMaxmemoryPolicy() string {
	if c.Environment.Redis != nil && c.Environment.Redis.MaxmemoryPolicy != "" {
		return c.Environment.Redis.MaxmemoryPolicy
	}
	return defaultRedisMaxmemoryPolicy
}

func (c *ResolvedConfig) RedisUser() (string, error) {
	if c.Environment.Redis != nil && c.Environment.Redis.User != "" {
		return validatePostgresIdentifier(c.Environment.Redis.User, "user")
	}
	return defaultPostgresIdentifier(c.Project.Project.Name, c.EnvName), nil
}

func (c *ResolvedConfig) RedisSecretName() string {
	return fmt.Sprintf("%s_redis_url", c.EnvName)
}

func validateRedisConfig(name string, r *RedisConfig) error {
	if r == nil {
		return nil
	}
	if r.Database != nil && *r.Database < 0 {
		return fmt.Errorf("environments.%s.redis.database must be >= 0", name)
	}
	if r.Port < 0 || r.Port > 65535 {
		return fmt.Errorf("environments.%s.redis.port must be between 1 and 65535", name)
	}
	if r.Port == LegacySharedRedisPort {
		return fmt.Errorf("environments.%s.redis.port %d is the shared Redis that every environment used before; each environment now has its own instance, so pick another port or omit it (see: landfall redis migrate)", name, r.Port)
	}
	if r.Instance != "" && !redisInstancePattern.MatchString(r.Instance) {
		return fmt.Errorf("environments.%s.redis.instance %q must be lowercase letters, digits, _ and -, at most 64 characters", name, r.Instance)
	}
	if r.Maxmemory != "" && !redisMaxmemoryPattern.MatchString(r.Maxmemory) {
		return fmt.Errorf("environments.%s.redis.maxmemory %q must be a size like 128mb, 1gb or 0", name, r.Maxmemory)
	}
	if r.MaxmemoryPolicy != "" && !redisMaxmemoryPolicies[r.MaxmemoryPolicy] {
		return fmt.Errorf("environments.%s.redis.maxmemory_policy %q is not a Redis eviction policy (e.g. noeviction, allkeys-lru)", name, r.MaxmemoryPolicy)
	}
	return nil
}

func isProdEnv(envName string) bool {
	return envName == "prod" || envName == "production"
}

func defaultRedisDatabase(envName string) int {
	switch envName {
	case "prod", "production":
		return 0
	case "dev", "development":
		return 1
	case "staging", "stage":
		return 2
	default:
		return 3
	}
}
