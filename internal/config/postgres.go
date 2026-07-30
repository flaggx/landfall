package config

import (
	"fmt"
	"regexp"
	"strings"
)

type PostgresConfig struct {
	Database string `toml:"database"`
	User     string `toml:"user"`
}

var postgresIdentifierPattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,62}$`)

func (c *ResolvedConfig) PostgresDatabase() (string, error) {
	if c.Environment.Postgres != nil && c.Environment.Postgres.Database != "" {
		return validatePostgresIdentifier(c.Environment.Postgres.Database, "database")
	}
	return defaultPostgresIdentifier(c.Project.Project.Name, c.EnvName), nil
}

func (c *ResolvedConfig) PostgresUser() (string, error) {
	if c.Environment.Postgres != nil && c.Environment.Postgres.User != "" {
		return validatePostgresIdentifier(c.Environment.Postgres.User, "user")
	}
	return defaultPostgresIdentifier(c.Project.Project.Name, c.EnvName), nil
}

func (c *ResolvedConfig) PostgresSecretName() string {
	return fmt.Sprintf("%s_db_url", c.EnvName)
}

func defaultPostgresIdentifier(projectName, envName string) string {
	base := sanitizePostgresIdentifier(projectName) + "_" + sanitizePostgresIdentifier(envName)
	if len(base) > 63 {
		base = base[:63]
	}
	if base == "" {
		base = "app_" + sanitizePostgresIdentifier(envName)
	}
	return base
}

func sanitizePostgresIdentifier(value string) string {
	value = strings.ToLower(value)
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if r == '-' || r == '.' {
			b.WriteRune('_')
		}
	}
	result := b.String()
	result = strings.Trim(result, "_")
	if result == "" {
		return "app"
	}
	if result[0] >= '0' && result[0] <= '9' {
		return "app_" + result
	}
	return result
}

func validatePostgresIdentifier(value, field string) (string, error) {
	if !postgresIdentifierPattern.MatchString(value) {
		return "", fmt.Errorf("postgres %s %q is invalid (letters, numbers, underscores; must start with a letter)", field, value)
	}
	return value, nil
}
