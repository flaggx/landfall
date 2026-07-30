package config

import "fmt"

type RedisConfig struct {
	Port     int    `toml:"port"`
	Database int    `toml:"database"`
	User     string `toml:"user"`
}

func (c *ResolvedConfig) RedisPort() int {
	if c.Environment.Redis != nil && c.Environment.Redis.Port > 0 {
		return c.Environment.Redis.Port
	}
	return 6379
}

func (c *ResolvedConfig) RedisDatabase() int {
	if c.Environment.Redis != nil && c.Environment.Redis.Database >= 0 {
		return c.Environment.Redis.Database
	}
	return defaultRedisDatabase(c.EnvName)
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
