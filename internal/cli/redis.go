package cli

import (
	"github.com/flaggx/vpsdeploymentautomation/internal/redis"
	"github.com/spf13/cobra"
)

var (
	resetRedisPassword bool
	saveRedisSecret    bool
)

func newRedisCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "redis",
		Short: "Manage Redis on the VPS",
		Long:  "Install and configure Redis on the VPS with a dedicated ACL user and database index per environment.",
	}

	cmd.AddCommand(newRedisBootstrapCmd())
	cmd.AddCommand(newRedisStatusCmd())

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
			})
			return err
		},
	}
	cmd.Flags().StringVar(&envName, "env", "prod", "Environment name from vpsdeploy.toml")
	cmd.Flags().BoolVar(&resetRedisPassword, "reset-password", false, "Generate a new Redis password")
	cmd.Flags().BoolVar(&saveRedisSecret, "save-secret", false, "Save REDIS_URL to local secrets automatically")
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
	cmd.Flags().StringVar(&envName, "env", "prod", "Environment name from vpsdeploy.toml")
	return cmd
}
