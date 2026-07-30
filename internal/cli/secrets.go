package cli

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/flaggx/vpsdeploymentautomation/internal/config"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newSecretsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Manage local deployment secrets",
		Long:  "Secrets are stored locally at ~/.config/vpsdeploy/secrets.toml and injected at deploy time.",
	}

	cmd.AddCommand(newSecretsInitCmd())
	cmd.AddCommand(newSecretsListCmd())
	cmd.AddCommand(newSecretsSetCmd())
	cmd.AddCommand(newSecretsGetCmd())
	cmd.AddCommand(newSecretsDeleteCmd())
	cmd.AddCommand(newSecretsCheckCmd())

	return cmd
}

func newSecretsInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create the local secrets file",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := config.InitSecretsFile()
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "Secrets file ready at %s\n", path)
			fmt.Fprintln(os.Stdout, "Add secrets with: vpsdeploy secrets set <name>")
			return nil
		},
	}
}

func newSecretsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List stored secret names",
		RunE: func(cmd *cobra.Command, args []string) error {
			keys, err := config.ListSecretKeys()
			if err != nil {
				return err
			}

			if len(keys) == 0 {
				path, _ := config.SecretsPath()
				fmt.Fprintf(os.Stdout, "No secrets stored yet (%s)\n", path)
				return nil
			}

			sort.Strings(keys)
			for _, key := range keys {
				fmt.Fprintln(os.Stdout, key)
			}
			return nil
		},
	}
}

func newSecretsSetCmd() *cobra.Command {
	var value string

	cmd := &cobra.Command{
		Use:   "set <name>",
		Short: "Set or update a secret",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]

			secretValue := value
			if secretValue == "" {
				var err error
				secretValue, err = promptSecret("Enter secret value: ")
				if err != nil {
					return err
				}
			}

			if _, err := config.InitSecretsFile(); err != nil {
				return err
			}

			if err := config.SetSecret(key, secretValue); err != nil {
				return err
			}

			fmt.Fprintf(os.Stdout, "Set secret %q\n", key)
			return nil
		},
	}

	cmd.Flags().StringVar(&value, "value", "", "Secret value (if omitted, prompts securely)")
	return cmd
}

func newSecretsGetCmd() *cobra.Command {
	var reveal bool

	cmd := &cobra.Command{
		Use:   "get <name>",
		Short: "Show a secret value",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			value, err := config.GetSecret(args[0])
			if err != nil {
				return err
			}

			if reveal {
				fmt.Fprintln(os.Stdout, value)
			} else {
				fmt.Fprintf(os.Stdout, "%s = %s\n", args[0], maskSecret(value))
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&reveal, "reveal", false, "Print the full secret value")
	return cmd
}

func newSecretsDeleteCmd() *cobra.Command {
	var yes bool

	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a secret",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]

			if !yes {
				fmt.Fprintf(os.Stdout, "Delete secret %q? [y/N]: ", key)
				reader := bufio.NewReader(os.Stdin)
				line, _ := reader.ReadString('\n')
				answer := strings.ToLower(strings.TrimSpace(line))
				if answer != "y" && answer != "yes" {
					fmt.Fprintln(os.Stdout, "Cancelled.")
					return nil
				}
			}

			if err := config.DeleteSecret(key); err != nil {
				return err
			}

			fmt.Fprintf(os.Stdout, "Deleted secret %q\n", key)
			return nil
		},
	}

	cmd.Flags().BoolVar(&yes, "yes", false, "Skip confirmation prompt")
	return cmd
}

func newSecretsCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Verify secrets required by vpsdeploy.toml are set",
		RunE: func(cmd *cobra.Command, args []string) error {
			required, missing, err := config.CheckSecretsForProject(projectDir)
			if err != nil {
				return err
			}

			if len(required) == 0 {
				fmt.Fprintln(os.Stdout, "No secret references found in vpsdeploy.toml")
				return nil
			}

			fmt.Fprintf(os.Stdout, "Required secrets (%d):\n", len(required))
			for _, key := range required {
				status := "ok"
				for _, m := range missing {
					if m == key {
						status = "MISSING"
						break
					}
				}
				fmt.Fprintf(os.Stdout, "  %s  %s\n", status, key)
			}

			if len(missing) > 0 {
				fmt.Fprintln(os.Stdout, "")
				fmt.Fprintln(os.Stdout, "Set missing secrets with:")
				for _, key := range missing {
					fmt.Fprintf(os.Stdout, "  vpsdeploy secrets set %s\n", key)
				}
				return fmt.Errorf("%d required secret(s) missing", len(missing))
			}

			fmt.Fprintln(os.Stdout, "All required secrets are set.")
			return nil
		},
	}
}

func promptSecret(label string) (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stdout, label)
		bytes, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stdout)
		if err != nil {
			return "", fmt.Errorf("read secret: %w", err)
		}
		value := strings.TrimSpace(string(bytes))
		if value == "" {
			return "", fmt.Errorf("secret value cannot be empty")
		}
		return value, nil
	}

	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return "", fmt.Errorf("secret value cannot be empty")
	}
	value := strings.TrimSpace(scanner.Text())
	if value == "" {
		return "", fmt.Errorf("secret value cannot be empty")
	}
	return value, nil
}

func maskSecret(value string) string {
	if value == "" {
		return "(empty)"
	}
	if len(value) <= 4 {
		return strings.Repeat("*", len(value))
	}
	return value[:2] + strings.Repeat("*", len(value)-4) + value[len(value)-2:]
}
