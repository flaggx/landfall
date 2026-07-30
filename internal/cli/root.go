package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/flaggx/vpsdeploymentautomation/internal/bootstrap"
	"github.com/flaggx/vpsdeploymentautomation/internal/config"
	"github.com/flaggx/vpsdeploymentautomation/internal/deploy"
	"github.com/spf13/cobra"
)

var (
	projectDir string
	envName    string
	deployRef  string
	followLogs bool
	withCaddy  bool
)

func NewRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "vpsdeploy",
		Short: "Deploy Git repos to a VPS",
	}

	root.PersistentFlags().StringVar(&projectDir, "project-dir", ".", "Directory containing vpsdeploy.toml")

	root.AddCommand(newInitCmd())
	root.AddCommand(newBootstrapCmd())
	root.AddCommand(newDeployCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newLogsCmd())
	root.AddCommand(newSecretsCmd())
	root.AddCommand(newDBCmd())

	return root
}

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create a vpsdeploy.toml in the current project",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInit()
		},
	}
}

func newBootstrapCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "One-time VPS setup for an environment",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			return bootstrap.Run(cfg, withCaddy)
		},
	}
	cmd.Flags().StringVar(&envName, "env", "prod", "Environment name from vpsdeploy.toml")
	cmd.Flags().BoolVar(&withCaddy, "caddy", false, "Install and configure Caddy reverse proxy")
	return cmd
}

func newDeployCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy the latest code to an environment",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			_, err = deploy.Run(cfg, deploy.Options{Ref: deployRef})
			return err
		},
	}
	cmd.Flags().StringVar(&envName, "env", "prod", "Environment name from vpsdeploy.toml")
	cmd.Flags().StringVar(&deployRef, "ref", "", "Git ref to deploy (branch, tag, or commit). Defaults to configured branch.")
	return cmd
}

func newStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show deployment status for an environment",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			return deploy.Status(cfg)
		},
	}
	cmd.Flags().StringVar(&envName, "env", "prod", "Environment name from vpsdeploy.toml")
	return cmd
}

func newLogsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Show systemd logs for an environment",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			return deploy.Logs(cfg, followLogs)
		},
	}
	cmd.Flags().StringVar(&envName, "env", "prod", "Environment name from vpsdeploy.toml")
	cmd.Flags().BoolVarP(&followLogs, "follow", "f", false, "Follow log output")
	return cmd
}

func loadConfig() (*config.ResolvedConfig, error) {
	return config.LoadResolved(envName, projectDir)
}

func runInit() error {
	absDir, err := filepath.Abs(projectDir)
	if err != nil {
		return err
	}

	target := filepath.Join(absDir, config.ProjectConfigName)
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("%s already exists", target)
	}

	reader := bufio.NewReader(os.Stdin)

	name := prompt(reader, "Project name", filepath.Base(absDir))
	repo := prompt(reader, "Git repo (SSH URL)", "git@github.com:you/your-repo.git")
	host := prompt(reader, "VPS host (IP or domain)", "")
	user := prompt(reader, "SSH user", "deploy")

	prodPath := prompt(reader, "Prod deploy path", fmt.Sprintf("/var/www/%s-prod", name))
	prodBranch := prompt(reader, "Prod branch", "main")
	prodPort := promptInt(reader, "Prod port", 3000)
	prodDomain := prompt(reader, "Prod domain (optional, for Caddy)", "")

	devPath := prompt(reader, "Dev deploy path", fmt.Sprintf("/var/www/%s-dev", name))
	devBranch := prompt(reader, "Dev branch", "develop")
	devPort := promptInt(reader, "Dev port", 3001)
	devDomain := prompt(reader, "Dev domain (optional)", "")

	cfg := config.ProjectConfig{
		Project: config.Project{
			Name: name,
			Repo: repo,
		},
		Environments: map[string]config.Environment{
			"prod": {
				Host:        host,
				User:        user,
				Path:        prodPath,
				Branch:      prodBranch,
				Port:        prodPort,
				Domain:      prodDomain,
				HealthCheck: fmt.Sprintf("http://127.0.0.1:%d/api/health", prodPort),
				Env: map[string]string{
					"NODE_ENV":     "production",
					"DATABASE_URL": "{{secret:prod_db_url}}",
				},
			},
			"dev": {
				Host:        host,
				User:        user,
				Path:        devPath,
				Branch:      devBranch,
				Port:        devPort,
				Domain:      devDomain,
				HealthCheck: fmt.Sprintf("http://127.0.0.1:%d/api/health", devPort),
				Env: map[string]string{
					"NODE_ENV":     "development",
					"DATABASE_URL": "{{secret:dev_db_url}}",
				},
			},
		},
	}

	if err := config.WriteProjectConfig(target, cfg); err != nil {
		return err
	}

	globalDir, err := config.EnsureGlobalConfigDir()
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "Created %s\n", target)
	fmt.Fprintf(os.Stdout, "Global config directory: %s\n", globalDir)
	fmt.Fprintln(os.Stdout, "Optional: run `vpsdeploy secrets init` to set up local secrets")
	return nil
}

func prompt(reader *bufio.Reader, label, defaultValue string) string {
	if defaultValue != "" {
		fmt.Fprintf(os.Stdout, "%s [%s]: ", label, defaultValue)
	} else {
		fmt.Fprintf(os.Stdout, "%s: ", label)
	}

	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultValue
	}
	return line
}

func promptInt(reader *bufio.Reader, label string, defaultValue int) int {
	raw := prompt(reader, label, strconv.Itoa(defaultValue))
	value, err := strconv.Atoi(raw)
	if err != nil {
		return defaultValue
	}
	return value
}
