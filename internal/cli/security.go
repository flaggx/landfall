package cli

import (
	"github.com/flaggx/vpsdeploymentautomation/internal/security"
	"github.com/spf13/cobra"
)

var (
	securityDisableRootSSH     bool
	securityDisableSSHPassword bool
	securityAutoReboot         bool
	securitySSHPort            int
)

func newSecurityCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "security",
		Short: "Harden and audit VPS security (Ubuntu)",
		Long:  "Apply Ubuntu-focused security hardening: unattended upgrades, UFW, fail2ban, and SSH hardening.",
	}

	cmd.AddCommand(newSecurityHardenCmd())
	cmd.AddCommand(newSecurityStatusCmd())

	return cmd
}

func newSecurityHardenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "harden",
		Short: "Apply Ubuntu security hardening and enable auto-updates",
		Long: `Install and configure security controls on the VPS:

  - unattended-upgrades for automatic security patches
  - UFW firewall (SSH, HTTP, HTTPS)
  - fail2ban for SSH brute-force protection
  - SSH hardening via a reversible drop-in config

Run once per VPS, ideally before going to production.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			return security.Harden(cfg, security.HardenOptions{
				DisableRootSSH:     securityDisableRootSSH,
				DisableSSHPassword: securityDisableSSHPassword,
				AutoReboot:         securityAutoReboot,
				SSHPort:            securitySSHPort,
			})
		},
	}
	cmd.Flags().StringVar(&envName, "env", "prod", "Environment name from vpsdeploy.toml")
	cmd.Flags().BoolVar(&securityDisableRootSSH, "ssh-disable-root", true, "Disable SSH root login")
	cmd.Flags().BoolVar(&securityDisableSSHPassword, "ssh-disable-password", false, "Disable SSH password login (key-only)")
	cmd.Flags().BoolVar(&securityAutoReboot, "auto-reboot", false, "Automatically reboot for kernel security updates")
	cmd.Flags().IntVar(&securitySSHPort, "ssh-port", 22, "SSH port to allow through UFW")
	return cmd
}

func newSecurityStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show security hardening status",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			return security.Status(cfg)
		},
	}
	cmd.Flags().StringVar(&envName, "env", "prod", "Environment name from vpsdeploy.toml")
	return cmd
}
