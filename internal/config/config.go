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
	ProjectConfigName = "vpsdeploy.toml"
	GlobalConfigName  = "config.toml"
	SecretsConfigName = "secrets.toml"
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

func GlobalConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "vpsdeploy"), nil
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
		candidate := filepath.Join(dir, ProjectConfigName)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return "", fmt.Errorf("%s not found (searched from %s)", ProjectConfigName, startDir)
}

func LoadProjectConfig(path string) (ProjectConfig, error) {
	var cfg ProjectConfig
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return cfg, fmt.Errorf("parse project config: %w", err)
	}
	return cfg, nil
}

type LoadOptions struct {
	// ResolveSecrets replaces {{secret:...}} refs in environment env vars.
	// Set false for connection-only commands (harden, bootstrap, db/redis setup)
	// that run before secrets exist.
	ResolveSecrets bool
}

func LoadResolved(envName, projectDir string) (*ResolvedConfig, error) {
	return LoadWithOptions(envName, projectDir, LoadOptions{ResolveSecrets: true})
}

func LoadWithOptions(envName, projectDir string, opts LoadOptions) (*ResolvedConfig, error) {
	configPath, err := FindProjectConfig(projectDir)
	if err != nil {
		return nil, err
	}

	projectCfg, err := LoadProjectConfig(configPath)
	if err != nil {
		return nil, err
	}

	if err := validateProjectConfig(projectCfg); err != nil {
		return nil, err
	}

	env, ok := projectCfg.Environments[envName]
	if !ok {
		return nil, fmt.Errorf("environment %q not found in %s", envName, configPath)
	}

	if err := validateEnvironment(envName, env); err != nil {
		return nil, err
	}

	globalCfg, err := LoadGlobalConfig()
	if err != nil {
		return nil, err
	}

	secretsCfg, err := LoadSecrets()
	if err != nil {
		return nil, err
	}

	resolvedEnv := env
	if opts.ResolveSecrets {
		resolvedEnv, err = resolveEnvironmentEnv(env, secretsCfg)
		if err != nil {
			return nil, err
		}
	} else {
		// Copy env map so callers cannot mutate the parsed project config.
		resolvedEnv.Env = make(map[string]string, len(env.Env))
		for k, v := range env.Env {
			resolvedEnv.Env[k] = v
		}
	}

	return &ResolvedConfig{
		Global:      globalCfg,
		Project:     projectCfg,
		Secrets:     secretsCfg,
		ProjectPath: filepath.Dir(configPath),
		Environment: resolvedEnv,
		EnvName:     envName,
	}, nil
}

func resolveEnvironmentEnv(env Environment, secrets SecretsConfig) (Environment, error) {
	resolved := env
	resolved.Env = make(map[string]string, len(env.Env))

	for key, value := range env.Env {
		resolvedValue, err := resolveSecretRef(value, secrets)
		if err != nil {
			return Environment{}, fmt.Errorf("env %s: %w", key, err)
		}
		resolved.Env[key] = resolvedValue
	}

	return resolved, nil
}

func resolveSecretRef(value string, secrets SecretsConfig) (string, error) {
	matches := secretRefPattern.FindStringSubmatch(value)
	if matches == nil {
		return value, nil
	}

	secretKey := matches[1]
	secretValue, ok := secrets.Secrets[secretKey]
	if !ok {
		return "", fmt.Errorf("secret %q not found in %s", secretKey, SecretsConfigName)
	}

	return secretValue, nil
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

	enc := toml.NewEncoder(f)
	return enc.Encode(cfg)
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
