package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	// AppName is the product / CLI name.
	AppName = "landfall"
	// LegacyAppName is accepted for config paths and filenames from older installs.
	LegacyAppName = "vpsdeploy"

	ProjectConfigName       = "landfall.toml"
	LegacyProjectConfigName = "vpsdeploy.toml"
	GlobalConfigName        = "config.toml"
	SecretsConfigName       = "secrets.toml"
)

var secretRefPattern = regexp.MustCompile(`^\{\{secret:([a-zA-Z0-9_]+)\}\}$`)

type GlobalConfig struct {
	SSHKeyPath string `toml:"ssh_key_path"`
}

type ProjectConfig struct {
	Project      Project                `toml:"project"`
	Environments map[string]Environment `toml:"environments"`
}

type Project struct {
	Name string `toml:"name"`
	Repo string `toml:"repo"`
}

type Environment struct {
	Host        string            `toml:"host"`
	User        string            `toml:"user"`
	Path        string            `toml:"path"`
	Branch      string            `toml:"branch"`
	Port        int               `toml:"port"`
	HealthCheck string            `toml:"health_check"`
	Domain      string            `toml:"domain"`
	Env         map[string]string `toml:"env"`
	Postgres    *PostgresConfig   `toml:"postgres"`
	Redis       *RedisConfig      `toml:"redis"`
}

type SecretsConfig struct {
	Secrets map[string]string `toml:"secrets"`
}

type ResolvedConfig struct {
	Global      GlobalConfig
	Project     ProjectConfig
	Secrets     SecretsConfig
	ProjectPath string
	Environment Environment
	EnvName     string
}

// GlobalConfigDir returns ~/.config/landfall, or the legacy ~/.config/vpsdeploy
// directory when that already exists and landfall does not (existing installs).
func GlobalConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	modern := filepath.Join(home, ".config", AppName)
	legacy := filepath.Join(home, ".config", LegacyAppName)
	if _, err := os.Stat(modern); err == nil {
		return modern, nil
	}
	if _, err := os.Stat(legacy); err == nil {
		return legacy, nil
	}
	return modern, nil
}

func LoadGlobalConfig() (GlobalConfig, error) {
	cfg := GlobalConfig{
		SSHKeyPath: defaultSSHKeyPath(),
	}

	dir, err := GlobalConfigDir()
	if err != nil {
		return cfg, err
	}

	path := filepath.Join(dir, GlobalConfigName)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return cfg, nil
	}

	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return cfg, fmt.Errorf("parse global config: %w", err)
	}

	if cfg.SSHKeyPath == "" {
		cfg.SSHKeyPath = defaultSSHKeyPath()
	}

	return cfg, nil
}

func LoadSecrets() (SecretsConfig, error) {
	cfg := SecretsConfig{
		Secrets: map[string]string{},
	}

	dir, err := GlobalConfigDir()
	if err != nil {
		return cfg, err
	}

	path := filepath.Join(dir, SecretsConfigName)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return cfg, nil
	}

	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return cfg, fmt.Errorf("parse secrets config: %w", err)
	}

	if cfg.Secrets == nil {
		cfg.Secrets = map[string]string{}
	}

	return cfg, nil
}

func FindProjectConfig(startDir string) (string, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", err
	}

	for {
		for _, name := range []string{ProjectConfigName, LegacyProjectConfigName} {
			candidate := filepath.Join(dir, name)
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return "", fmt.Errorf("%s (or %s) not found (searched from %s)", ProjectConfigName, LegacyProjectConfigName, startDir)
}

func LoadProjectConfig(path string) (ProjectConfig, error) {
	var cfg ProjectConfig
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return cfg, fmt.Errorf("parse project config: %w", err)
	}
	if err := validateProjectConfig(cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

type LoadOptions struct {
	ResolveSecrets bool
}

func LoadResolved(envName, projectDir string) (*ResolvedConfig, error) {
	return LoadWithOptions(envName, projectDir, LoadOptions{ResolveSecrets: true})
}

func LoadWithOptions(envName, projectDir string, opts LoadOptions) (*ResolvedConfig, error) {
	global, err := LoadGlobalConfig()
	if err != nil {
		return nil, err
	}

	configPath, err := FindProjectConfig(projectDir)
	if err != nil {
		return nil, err
	}

	project, err := LoadProjectConfig(configPath)
	if err != nil {
		return nil, err
	}

	env, ok := project.Environments[envName]
	if !ok {
		return nil, fmt.Errorf("environment %q not found in %s", envName, configPath)
	}
	if err := validateEnvironment(envName, env); err != nil {
		return nil, err
	}

	secrets := SecretsConfig{Secrets: map[string]string{}}
	if opts.ResolveSecrets {
		secrets, err = LoadSecrets()
		if err != nil {
			return nil, err
		}
		env, err = resolveEnvironmentEnv(env, secrets)
		if err != nil {
			return nil, err
		}
	}

	return &ResolvedConfig{
		Global:      global,
		Project:     project,
		Secrets:     secrets,
		ProjectPath: filepath.Dir(configPath),
		Environment: env,
		EnvName:     envName,
	}, nil
}

func resolveEnvironmentEnv(env Environment, secrets SecretsConfig) (Environment, error) {
	if env.Env == nil {
		return env, nil
	}
	resolved := make(map[string]string, len(env.Env))
	for key, value := range env.Env {
		out, err := resolveSecretRef(value, secrets)
		if err != nil {
			return env, err
		}
		resolved[key] = out
	}
	env.Env = resolved
	return env, nil
}

func resolveSecretRef(value string, secrets SecretsConfig) (string, error) {
	matches := secretRefPattern.FindStringSubmatch(value)
	if matches == nil {
		return value, nil
	}
	name := matches[1]
	secret, ok := secrets.Secrets[name]
	if !ok || secret == "" {
		return "", fmt.Errorf("secret %q is not set (run: landfall secrets set %s)", name, name)
	}
	return secret, nil
}

func validateProjectConfig(cfg ProjectConfig) error {
	if strings.TrimSpace(cfg.Project.Name) == "" {
		return fmt.Errorf("project.name is required")
	}
	if strings.TrimSpace(cfg.Project.Repo) == "" {
		return fmt.Errorf("project.repo is required")
	}
	if len(cfg.Environments) == 0 {
		return fmt.Errorf("at least one environment is required")
	}
	return nil
}

func validateEnvironment(name string, env Environment) error {
	if strings.TrimSpace(env.Host) == "" {
		return fmt.Errorf("environments.%s.host is required", name)
	}
	if strings.TrimSpace(env.User) == "" {
		return fmt.Errorf("environments.%s.user is required", name)
	}
	if strings.TrimSpace(env.Path) == "" {
		return fmt.Errorf("environments.%s.path is required", name)
	}
	if strings.TrimSpace(env.Branch) == "" {
		return fmt.Errorf("environments.%s.branch is required", name)
	}
	if env.Port <= 0 {
		return fmt.Errorf("environments.%s.port must be > 0", name)
	}
	return nil
}

func (c *ResolvedConfig) ServiceName() string {
	return fmt.Sprintf("%s-%s", c.Project.Project.Name, c.EnvName)
}

func (c *ResolvedConfig) SSHAddress() string {
	return fmt.Sprintf("%s@%s", c.Environment.User, c.Environment.Host)
}

func defaultSSHKeyPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ssh", "id_ed25519")
}

func WriteProjectConfig(path string, cfg ProjectConfig) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(cfg)
}

func EnsureGlobalConfigDir() (string, error) {
	dir, err := GlobalConfigDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}
