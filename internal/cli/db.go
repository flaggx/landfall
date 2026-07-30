package cli

import (
	"github.com/flaggx/vpsdeploymentautomation/internal/db"
	"github.com/spf13/cobra"
)

var (
	resetDBPassword bool
	saveDBSecret    bool
)

func newDBCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "db",
		Short: "Manage PostgreSQL on the VPS",
		Long:  "Install and configure PostgreSQL on the VPS with a dedicated database per environment.",
	}

	cmd.AddCommand(newDBBootstrapCmd())
	cmd.AddCommand(newDBStatusCmd())

	return cmd
}

func newDBBootstrapCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Install PostgreSQL and create the environment database",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			_, err = db.Bootstrap(cfg, db.BootstrapOptions{
				ResetPassword: resetDBPassword,
				SaveSecret:    saveDBSecret,
			})
			return err
		},
	}
	cmd.Flags().StringVar(&envName, "env", "prod", "Environment name from vpsdeploy.toml")
	cmd.Flags().BoolVar(&resetDBPassword, "reset-password", false, "Generate a new database password")
	cmd.Flags().BoolVar(&saveDBSecret, "save-secret", false, "Save DATABASE_URL to local secrets automatically")
	return cmd
}

func newDBStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show PostgreSQL status for an environment",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			return db.Status(cfg)
		},
	}
	cmd.Flags().StringVar(&envName, "env", "prod", "Environment name from vpsdeploy.toml")
	return cmd
}
