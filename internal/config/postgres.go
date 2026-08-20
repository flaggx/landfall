package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

type PostgresConfig struct {
	Database string `toml:"database"`
	User     string `toml:"user"`

	// Host is the Postgres address apps connect to. Empty means co-located
	// (DATABASE_URL uses localhost). Set this for a dedicated DB VPS.
	Host string `toml:"host"`
	Port int    `toml:"port"`

	// AppHost is the app VPS IP/hostname allowed in pg_hba and UFW when Host is set.
	AppHost string `toml:"app_host"`

	// SSHUser overrides Environment.User when connecting to the DB host.
	SSHUser string `toml:"ssh_user"`

	BackupDir        string `toml:"backup_dir"`
	BackupRetain     int    `toml:"backup_retain"` // days to keep local dumps
	BackupS3Endpoint string `toml:"backup_s3_endpoint"`
	BackupS3Bucket   string `toml:"backup_s3_bucket"`
	BackupS3Prefix   string `toml:"backup_s3_prefix"`
	BackupS3Region   string `toml:"backup_s3_region"`
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

func (c *ResolvedConfig) PostgresReadSecretName() string {
	return fmt.Sprintf("%s_db_read_url", c.EnvName)
}

// PostgresPort returns the configured port or 5432.
func (c *ResolvedConfig) PostgresPort() int {
	if c.Environment.Postgres != nil && c.Environment.Postgres.Port > 0 {
		return c.Environment.Postgres.Port
	}
	return 5432
}

// PostgresConnectHost is the host embedded in DATABASE_URL.
// Empty / co-located → localhost.
func (c *ResolvedConfig) PostgresConnectHost() string {
	if c.Environment.Postgres != nil && strings.TrimSpace(c.Environment.Postgres.Host) != "" {
		return strings.TrimSpace(c.Environment.Postgres.Host)
	}
	return "localhost"
}

// PostgresIsRemote is true when apps should not use localhost.
func (c *ResolvedConfig) PostgresIsRemote() bool {
	h := c.PostgresConnectHost()
	return h != "localhost" && h != "127.0.0.1"
}

// PostgresSSHHost is where db bootstrap/backup/status SSH sessions go.
func (c *ResolvedConfig) PostgresSSHHost() string {
	if c.PostgresIsRemote() {
		return c.PostgresConnectHost()
	}
	return c.Environment.Host
}

// PostgresSSHUser is the SSH user for DB operations.
func (c *ResolvedConfig) PostgresSSHUser() string {
	if c.Environment.Postgres != nil && strings.TrimSpace(c.Environment.Postgres.SSHUser) != "" {
		return strings.TrimSpace(c.Environment.Postgres.SSHUser)
	}
	return c.Environment.User
}

// PostgresAppHost is the app IP allowed to reach a dedicated DB (pg_hba + UFW).
func (c *ResolvedConfig) PostgresAppHost() string {
	if c.Environment.Postgres != nil && strings.TrimSpace(c.Environment.Postgres.AppHost) != "" {
		return strings.TrimSpace(c.Environment.Postgres.AppHost)
	}
	// Default: the environment app host when DB is remote.
	if c.PostgresIsRemote() {
		return c.Environment.Host
	}
	return ""
}

func (c *ResolvedConfig) PostgresBackupDir() string {
	if c.Environment.Postgres != nil && strings.TrimSpace(c.Environment.Postgres.BackupDir) != "" {
		return strings.TrimSpace(c.Environment.Postgres.BackupDir)
	}
	return fmt.Sprintf("/var/backups/vpsdeploy/%s", c.EnvName)
}

func (c *ResolvedConfig) PostgresBackupRetainDays() int {
	if c.Environment.Postgres != nil && c.Environment.Postgres.BackupRetain > 0 {
		return c.Environment.Postgres.BackupRetain
	}
	return 7
}

func (c *ResolvedConfig) PostgresBackupS3Configured() bool {
	p := c.Environment.Postgres
	return p != nil && strings.TrimSpace(p.BackupS3Bucket) != ""
}

func (c *ResolvedConfig) EncodeDatabaseURL(user, password, host string, port int, database string) string {
	u := &url.URL{
		Scheme: "postgresql",
		User:   url.UserPassword(user, password),
		Host:   fmt.Sprintf("%s:%d", host, port),
		Path:   "/" + database,
	}
	return u.String()
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
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
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
