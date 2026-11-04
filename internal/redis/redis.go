package redis

import (
	"fmt"
	"net/url"
	"os"

	"github.com/flaggx/landfall/internal/config"
	"github.com/flaggx/landfall/internal/ssh"
	"github.com/flaggx/landfall/internal/util"
)

type BootstrapOptions struct {
	ResetPassword bool
	SaveSecret    bool
}

type BootstrapResult struct {
	User             string
	Database         int
	Port             int
	ConnectionString string
	SecretName       string
	Created          bool
	PasswordRotated  bool
	// DefaultLocked is true when this run put a password on Redis's built-in
	// "default" user.
	DefaultLocked bool
}

func Bootstrap(cfg *config.ResolvedConfig, opts BootstrapOptions) (*BootstrapResult, error) {
	redisUser, err := cfg.RedisUser()
	if err != nil {
		return nil, err
	}
	redisPort := cfg.RedisPort()
	redisDB := cfg.RedisDatabase()

	client, err := ssh.New(ssh.Options{
		Host:    cfg.Environment.Host,
		User:    cfg.Environment.User,
		KeyPath: cfg.Global.SSHKeyPath,
	})
	if err != nil {
		return nil, err
	}
	defer client.Close()

	fmt.Fprintf(os.Stdout, "Setting up Redis for %s on %s...\n", cfg.EnvName, cfg.SSHAddress())

	resultPath, err := client.TempPath("landfall-redis.env")
	if err != nil {
		return nil, err
	}
	script := buildBootstrapScript(DefaultPaths, redisUser, redisPort, redisDB, resultPath, opts.ResetPassword)

	_, runErr := client.RunScript("landfall-redis-bootstrap.sh", script)

	raw, readErr := client.ReadFile(resultPath)
	_, _ = client.Run(fmt.Sprintf("rm -f %s", util.ShellQuote(resultPath)))

	if runErr != nil {
		// The script records a new password as soon as Redis uses it. If a
		// later step failed, the app's old password has already stopped
		// working, so the new one must not be lost.
		if readErr == nil {
			if url := util.ParseEnvFile(string(raw))["REDIS_URL"]; url != "" {
				return nil, rescueConnectionString(cfg, opts, url, runErr)
			}
		}
		return nil, runErr
	}
	if readErr != nil {
		return nil, fmt.Errorf("read bootstrap result: %w", readErr)
	}

	vars := util.ParseEnvFile(string(raw))
	connectionString := vars["REDIS_URL"]

	result := &BootstrapResult{
		User:             redisUser,
		Database:         redisDB,
		Port:             redisPort,
		ConnectionString: connectionString,
		SecretName:       cfg.RedisSecretName(),
		Created:          vars["CREATED"] == "true",
		PasswordRotated:  vars["PASSWORD_ROTATED"] == "true",
		DefaultLocked:    vars["DEFAULT_LOCKED"] == "true",
	}

	if connectionString == "" && !result.Created && !result.PasswordRotated {
		printBootstrapResult(result)
		fmt.Fprintln(os.Stdout, "No new connection string generated. Use your existing secret or run with --reset-password.")
		return result, nil
	}

	if connectionString == "" {
		return nil, fmt.Errorf("redis bootstrap did not return a connection string")
	}

	printBootstrapResult(result)

	if opts.SaveSecret {
		if _, err := config.InitSecretsFile(); err != nil {
			return result, err
		}
		if err := config.SetSecret(result.SecretName, result.ConnectionString); err != nil {
			return result, err
		}
		fmt.Fprintf(os.Stdout, "Saved secret %q\n", result.SecretName)
	} else if result.ConnectionString != "" && (result.Created || result.PasswordRotated) {
		fmt.Fprintf(os.Stdout, "\nSave to secrets with:\n  landfall secrets set %s --value %q\n", result.SecretName, result.ConnectionString)
	}

	if result.Created || result.PasswordRotated {
		fmt.Fprintf(os.Stdout, "\nAdd to landfall.toml if not already present:\n")
		fmt.Fprintf(os.Stdout, "[environments.%s.env]\nREDIS_URL = \"{{secret:%s}}\"\n", cfg.EnvName, result.SecretName)
	}

	return result, nil
}

// rescueConnectionString handles a bootstrap that changed the app user's
// password in Redis but then failed: it saves or prints the new URL so the
// app can be redeployed with it, and returns an error explaining both.
func rescueConnectionString(cfg *config.ResolvedConfig, opts BootstrapOptions, url string, runErr error) error {
	secret := cfg.RedisSecretName()
	if opts.SaveSecret {
		if _, err := config.InitSecretsFile(); err == nil {
			if err := config.SetSecret(secret, url); err == nil {
				fmt.Fprintf(os.Stdout, "Saved secret %q with the new Redis password.\n", secret)
				return fmt.Errorf("redis bootstrap failed after changing the password (the new one is saved in %q; redeploy, then fix the error and re-run without --reset-password): %w", secret, runErr)
			}
		}
	}
	fmt.Fprintf(os.Stdout, "\nRedis already uses a new password for this environment. Save it now:\n  landfall secrets set %s --value %q\n", secret, url)
	return fmt.Errorf("redis bootstrap failed after changing the password (save the URL above, redeploy, then fix the error and re-run without --reset-password): %w", runErr)
}

func Status(cfg *config.ResolvedConfig) error {
	redisUser, err := cfg.RedisUser()
	if err != nil {
		return err
	}
	redisPort := cfg.RedisPort()
	redisDB := cfg.RedisDatabase()

	client, err := ssh.New(ssh.Options{
		Host:    cfg.Environment.Host,
		User:    cfg.Environment.User,
		KeyPath: cfg.Global.SSHKeyPath,
	})
	if err != nil {
		return err
	}
	defer client.Close()

	script := buildStatusScript(DefaultPaths, redisUser, redisPort, redisDB)

	_, err = client.RunScript("landfall-redis-status.sh", script)
	return err
}

func printBootstrapResult(result *BootstrapResult) {
	fmt.Fprintln(os.Stdout, "")
	if result.DefaultLocked {
		fmt.Fprintf(os.Stdout, "Set a password on Redis's default user (stored on the server in %s, root only)\n", DefaultPaths.AdminFile)
	}
	if result.Created {
		fmt.Fprintf(os.Stdout, "Created Redis ACL user %q (DB index %d, port %d)\n", result.User, result.Database, result.Port)
	} else if result.PasswordRotated {
		fmt.Fprintf(os.Stdout, "Updated password for Redis ACL user %q (DB index %d)\n", result.User, result.Database)
	} else {
		fmt.Fprintf(os.Stdout, "Redis ACL user %q already exists (DB index %d)\n", result.User, result.Database)
		fmt.Fprintln(os.Stdout, "Use --reset-password to rotate the password, or keep your existing secret.")
	}
}

// EncodeRedisURL builds a properly escaped redis URL (used in tests).
func EncodeRedisURL(user, password, host string, port, db int) string {
	u := &url.URL{
		Scheme: "redis",
		User:   url.UserPassword(user, password),
		Host:   fmt.Sprintf("%s:%d", host, port),
		Path:   fmt.Sprintf("/%d", db),
	}
	return u.String()
}
