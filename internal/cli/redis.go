package cli

import (
	"github.com/flaggx/landfall/internal/redis"
	"github.com/spf13/cobra"
)

var (
	resetRedisPassword bool
	saveRedisSecret    bool
	freshRedis         bool
	migrateFromDB      int
	allowSharedRedisDB bool
)

func newRedisCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "redis",
		Short: "Manage Redis on the VPS",
		Long: `Install and configure Redis on the VPS. Each environment gets its own
Redis instance (redis-server@<project>-<env>) with its own port, memory limit,
data and ACL user, so environments on one host can't read, flush or crowd out
each other's data.`,
	}

	cmd.AddCommand(newRedisBootstrapCmd())
	cmd.AddCommand(newRedisStatusCmd())
	cmd.AddCommand(newRedisMigrateCmd())

	return cmd
}

func newRedisBootstrapCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Install Redis and create the environment cache user",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigForConnection()
			if err != nil {
				return err
			}
			_, err = redis.Bootstrap(cfg, redis.BootstrapOptions{
				ResetPassword: resetRedisPassword,
				SaveSecret:    saveRedisSecret,
				Fresh:         freshRedis,
			})
			return err
		},
	}
	cmd.Flags().StringVar(&envName, "env", "prod", "Environment name from landfall.toml")
	cmd.Flags().BoolVar(&resetRedisPassword, "reset-password", false, "Generate a new Redis password")
	cmd.Flags().BoolVar(&saveRedisSecret, "save-secret", false, "Save REDIS_URL to local secrets automatically")
	cmd.Flags().BoolVar(&freshRedis, "fresh", false, "Create the environment's own instance empty, even though it still has data on the shared Redis (see: redis migrate)")
	return cmd
}

func newRedisMigrateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Move an environment from the shared Redis to its own instance, with its data",
		Long: `Move an environment from the host-wide Redis that older Landfall versions
shared between environments (port 6379) to its own instance:

  1. create the instance and its user (new password)
  2. stop the app, so nothing writes during the copy
  3. copy the environment's keys (MIGRATE COPY: the shared Redis keeps them)
  4. save the new REDIS_URL secret and rewrite the server's .env.production
  5. start the app and run its health check

The app is down for steps 2-5, usually a few seconds. The old data and user
stay on the shared Redis until you remove them, so you can roll back by
setting the secret back and deploying.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigForConnection()
			if err != nil {
				return err
			}
			opts := redis.MigrateOptions{AllowSharedDB: allowSharedRedisDB}
			if cmd.Flags().Changed("from-db") {
				opts.FromDB = &migrateFromDB
			}
			return redis.Migrate(cfg, opts)
		},
	}
	cmd.Flags().StringVar(&envName, "env", "prod", "Environment name from landfall.toml")
	cmd.Flags().IntVar(&migrateFromDB, "from-db", 0, "DB index on the shared Redis to copy (default: the one in the current REDIS_URL secret)")
	cmd.Flags().BoolVar(&allowSharedRedisDB, "allow-shared-db", false, "Copy even if another environment on the host uses the same DB index (its keys are copied too)")
	return cmd
}

func newRedisStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show Redis status for an environment",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigForConnection()
			if err != nil {
				return err
			}
			return redis.Status(cfg)
		},
	}
	cmd.Flags().StringVar(&envName, "env", "prod", "Environment name from landfall.toml")
	return cmd
}
