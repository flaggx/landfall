package cli

import (
	"github.com/flaggx/vpsdeploymentautomation/internal/check"
	"github.com/spf13/cobra"
)

func newCheckCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Validate VPS setup, security hardening, and health endpoint",
		Long: `Run a full setup validation for an environment:

  - local secrets are configured
  - SSH connectivity
  - security hardening (UFW, auto-updates, fail2ban)
  - bootstrap state (Node.js, git repo, systemd)
  - PostgreSQL and Redis (if configured)
  - app health endpoint returns ok:true

Use after first-time setup or any time you want a pass/fail audit.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			summary, err := check.Run(cfg)
			if err != nil {
				return err
			}
			return check.PrintSummary(summary, cfg.EnvName)
		},
	}
	cmd.Flags().StringVar(&envName, "env", "prod", "Environment name from vpsdeploy.toml")
	return cmd
}
