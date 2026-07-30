package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

func SecretsPath() (string, error) {
	dir, err := GlobalConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, SecretsConfigName), nil
}

func SaveSecrets(cfg SecretsConfig) error {
	if cfg.Secrets == nil {
		cfg.Secrets = map[string]string{}
	}

	dir, err := EnsureGlobalConfigDir()
	if err != nil {
		return err
	}

	path := filepath.Join(dir, SecretsConfigName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("write secrets: %w", err)
	}
	defer f.Close()

	enc := toml.NewEncoder(f)
	if err := enc.Encode(cfg); err != nil {
		return fmt.Errorf("encode secrets: %w", err)
	}

	return os.Chmod(path, 0o600)
}

func SetSecret(key, value string) error {
	if err := validateSecretKey(key); err != nil {
		return err
	}
	if value == "" {
		return fmt.Errorf("secret value cannot be empty")
	}

	cfg, err := LoadSecrets()
	if err != nil {
		return err
	}

	cfg.Secrets[key] = value
	return SaveSecrets(cfg)
}

func DeleteSecret(key string) error {
	if err := validateSecretKey(key); err != nil {
		return err
	}

	cfg, err := LoadSecrets()
	if err != nil {
		return err
	}

	if _, ok := cfg.Secrets[key]; !ok {
		return fmt.Errorf("secret %q not found", key)
	}

	delete(cfg.Secrets, key)
	return SaveSecrets(cfg)
}

func GetSecret(key string) (string, error) {
	if err := validateSecretKey(key); err != nil {
		return "", err
	}

	cfg, err := LoadSecrets()
	if err != nil {
		return "", err
	}

	value, ok := cfg.Secrets[key]
	if !ok {
		return "", fmt.Errorf("secret %q not found", key)
	}

	return value, nil
}

func ListSecretKeys() ([]string, error) {
	cfg, err := LoadSecrets()
	if err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(cfg.Secrets))
	for key := range cfg.Secrets {
		keys = append(keys, key)
	}
	return keys, nil
}

func InitSecretsFile() (string, error) {
	dir, err := EnsureGlobalConfigDir()
	if err != nil {
		return "", err
	}

	path := filepath.Join(dir, SecretsConfigName)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}

	cfg := SecretsConfig{Secrets: map[string]string{}}
	if err := SaveSecrets(cfg); err != nil {
		return "", err
	}

	return path, nil
}

func CollectSecretRefs(cfg ProjectConfig) []string {
	seen := map[string]struct{}{}
	var refs []string

	for _, env := range cfg.Environments {
		for _, value := range env.Env {
			matches := secretRefPattern.FindStringSubmatch(value)
			if matches == nil {
				continue
			}
			key := matches[1]
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			refs = append(refs, key)
		}
	}

	return refs
}

func CheckSecretsForProject(projectDir string) ([]string, []string, error) {
	configPath, err := FindProjectConfig(projectDir)
	if err != nil {
		return nil, nil, err
	}

	projectCfg, err := LoadProjectConfig(configPath)
	if err != nil {
		return nil, nil, err
	}

	required := CollectSecretRefs(projectCfg)
	secretsCfg, err := LoadSecrets()
	if err != nil {
		return required, nil, err
	}

	var missing []string
	for _, key := range required {
		if _, ok := secretsCfg.Secrets[key]; !ok {
			missing = append(missing, key)
		}
	}

	return required, missing, nil
}

func validateSecretKey(key string) error {
	if key == "" {
		return fmt.Errorf("secret key cannot be empty")
	}
	if !secretRefPattern.MatchString("{{secret:" + key + "}}") {
		return fmt.Errorf("secret key %q is invalid (use letters, numbers, and underscores)", key)
	}
	return nil
}
