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
	// Fresh creates the environment's own instance even though it still has
	// a user and data on the shared Redis, starting empty instead of
	// migrating.
	Fresh bool
}

type BootstrapResult struct {
	User             string
	Instance         string
	Database         int
	Port             int
	ConnectionString string
	SecretName       string
	Created          bool
	PasswordRotated  bool
	// DefaultLocked is true when this run put a password on the instance's
	// built-in "default" user (a new instance).
	DefaultLocked bool
}

func sshClient(cfg *config.ResolvedConfig) (*ssh.Client, error) {
	return ssh.New(ssh.Options{
		Host:    cfg.Environment.Host,
		User:    cfg.Environment.User,
		KeyPath: cfg.Global.SSHKeyPath,
	})
}

func paramsFor(cfg *config.ResolvedConfig, resultPath string, reset, fresh bool) (bootstrapParams, error) {
	user, err := cfg.RedisUser()
	if err != nil {
		return bootstrapParams{}, err
	}
	instance, err := cfg.RedisInstance()
	if err != nil {
		return bootstrapParams{}, err
	}
	return bootstrapParams{
		Layout:     DefaultLayout,
		EnvName:    cfg.EnvName,
		Instance:   instance,
		User:       user,
		Port:       cfg.RedisPort(),
		DB:         cfg.RedisDatabase(),
		Maxmemory:  cfg.RedisMaxmemory(),
		Policy:     cfg.RedisMaxmemoryPolicy(),
		ResultPath: resultPath,
		Reset:      reset,
		Fresh:      fresh,
	}, nil
}

// runBootstrap creates or updates the environment's instance and app user and
// returns the script's result values. If the script failed after a new
// password was set, the new URL is saved or printed before the error returns.
func runBootstrap(client *ssh.Client, cfg *config.ResolvedConfig, opts BootstrapOptions) (map[string]string, error) {
	resultPath, err := client.TempPath("landfall-redis.env")
	if err != nil {
		return nil, err
	}
	params, err := paramsFor(cfg, resultPath, opts.ResetPassword, opts.Fresh)
	if err != nil {
		return nil, err
	}

	_, runErr := client.RunScript("landfall-redis-bootstrap.sh", buildBootstrapScript(params))

	raw, readErr := client.ReadFile(resultPath)
	_, _ = client.Run(fmt.Sprintf("rm -f %s", util.ShellQuote(resultPath)))

	if runErr != nil {
		// The script records a new password as soon as Redis uses it. If a
		// later step failed, the app's old password has already stopped
		// working, so the new one must not be lost.
		if readErr == nil {
			if u := util.ParseEnvFile(string(raw))["REDIS_URL"]; u != "" {
				return nil, rescueConnectionString(cfg, opts, u, runErr)
			}
		}
		return nil, runErr
	}
	if readErr != nil {
		return nil, fmt.Errorf("read bootstrap result: %w", readErr)
	}
	return util.ParseEnvFile(string(raw)), nil
}

func Bootstrap(cfg *config.ResolvedConfig, opts BootstrapOptions) (*BootstrapResult, error) {
	client, err := sshClient(cfg)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	fmt.Fprintf(os.Stdout, "Setting up Redis for %s on %s...\n", cfg.EnvName, cfg.SSHAddress())
	vars, err := runBootstrap(client, cfg, opts)
	if err != nil {
		return nil, err
	}
	result := resultFrom(cfg, vars)

	if result.ConnectionString == "" && !result.Created && !result.PasswordRotated {
		printBootstrapResult(result)
		fmt.Fprintln(os.Stdout, "No new connection string generated. Use your existing secret or run with --reset-password.")
		return result, nil
	}
	if result.ConnectionString == "" {
		return nil, fmt.Errorf("redis bootstrap did not return a connection string")
	}

	printBootstrapResult(result)

	if opts.SaveSecret {
		if err := saveSecret(result.SecretName, result.ConnectionString); err != nil {
			return result, err
		}
		fmt.Fprintf(os.Stdout, "Saved secret %q\n", result.SecretName)
	} else {
		fmt.Fprintf(os.Stdout, "\nSave to secrets with:\n  landfall secrets set %s --value %q\n", result.SecretName, result.ConnectionString)
	}
	fmt.Fprintf(os.Stdout, "\nAdd to landfall.toml if not already present:\n")
	fmt.Fprintf(os.Stdout, "[environments.%s.env]\nREDIS_URL = \"{{secret:%s}}\"\n", cfg.EnvName, result.SecretName)
	return result, nil
}

func resultFrom(cfg *config.ResolvedConfig, vars map[string]string) *BootstrapResult {
	user, _ := cfg.RedisUser()
	var port int
	fmt.Sscan(vars["PORT"], &port)
	return &BootstrapResult{
		User:             user,
		Instance:         vars["INSTANCE"],
		Database:         cfg.RedisDatabase(),
		Port:             port,
		ConnectionString: vars["REDIS_URL"],
		SecretName:       cfg.RedisSecretName(),
		Created:          vars["CREATED"] == "true",
		PasswordRotated:  vars["PASSWORD_ROTATED"] == "true",
		DefaultLocked:    vars["DEFAULT_LOCKED"] == "true",
	}
}

func saveSecret(name, value string) error {
	if _, err := config.InitSecretsFile(); err != nil {
		return err
	}
	return config.SetSecret(name, value)
}

// rescueConnectionString handles a bootstrap that changed the app user's
// password in Redis but then failed: it saves or prints the new URL so the
// app can be redeployed with it, and returns an error explaining both.
func rescueConnectionString(cfg *config.ResolvedConfig, opts BootstrapOptions, url string, runErr error) error {
	secret := cfg.RedisSecretName()
	if opts.SaveSecret {
		if err := saveSecret(secret, url); err == nil {
			fmt.Fprintf(os.Stdout, "Saved secret %q with the new Redis password.\n", secret)
			return fmt.Errorf("redis bootstrap failed after changing the password (the new one is saved in %q; redeploy, then fix the error and re-run without --reset-password): %w", secret, runErr)
		}
	}
	fmt.Fprintf(os.Stdout, "\nRedis already uses a new password for this environment. Save it now:\n  landfall secrets set %s --value %q\n", secret, url)
	return fmt.Errorf("redis bootstrap failed after changing the password (save the URL above, redeploy, then fix the error and re-run without --reset-password): %w", runErr)
}

func Status(cfg *config.ResolvedConfig) error {
	user, err := cfg.RedisUser()
	if err != nil {
		return err
	}
	instance, err := cfg.RedisInstance()
	if err != nil {
		return err
	}
	client, err := sshClient(cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	_, err = client.RunScript("landfall-redis-status.sh", buildStatusScript(DefaultLayout, cfg.EnvName, instance, user, cfg.RedisDatabase()))
	return err
}

func printBootstrapResult(result *BootstrapResult) {
	fmt.Fprintln(os.Stdout, "")
	if result.DefaultLocked {
		fmt.Fprintf(os.Stdout, "Created Redis instance %q on port %d, admin password in %s on the server (root only)\n",
			result.Instance, result.Port, DefaultLayout.Instance(result.Instance).AdminFile)
	}
	if result.Created {
		fmt.Fprintf(os.Stdout, "Created Redis ACL user %q (instance %s, port %d, DB %d)\n", result.User, result.Instance, result.Port, result.Database)
	} else if result.PasswordRotated {
		fmt.Fprintf(os.Stdout, "Updated password for Redis ACL user %q (instance %s)\n", result.User, result.Instance)
	} else {
		fmt.Fprintf(os.Stdout, "Redis ACL user %q already exists (instance %s, port %d)\n", result.User, result.Instance, result.Port)
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
