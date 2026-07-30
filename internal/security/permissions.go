package security

import (
	"fmt"

	"github.com/flaggx/vpsdeploymentautomation/internal/config"
	"github.com/flaggx/vpsdeploymentautomation/internal/ssh"
	"github.com/flaggx/vpsdeploymentautomation/internal/util"
)

func EnforcePermissions(cfg *config.ResolvedConfig, client *ssh.Client) error {
	script := BuildPermissionsScript(cfg.Environment.Path, cfg.Environment.User)
	_, err := client.RunScript("vpsdeploy-security-permissions.sh", script)
	return err
}

func BuildPermissionsScript(deployPath, deployUser string) string {
	return fmt.Sprintf(`
set -euo pipefail

DEPLOY_PATH=%s
DEPLOY_USER=%s

if [ -d "$DEPLOY_PATH" ]; then
  sudo chown -R "$DEPLOY_USER:$DEPLOY_USER" "$DEPLOY_PATH"
  sudo chmod 750 "$DEPLOY_PATH"
fi

if [ -f "$DEPLOY_PATH/.env.production" ]; then
  sudo chown "$DEPLOY_USER:$DEPLOY_USER" "$DEPLOY_PATH/.env.production"
  sudo chmod 600 "$DEPLOY_PATH/.env.production"
fi

if [ -d "$HOME/.ssh" ]; then
  chmod 700 "$HOME/.ssh"
  [ -f "$HOME/.ssh/id_ed25519" ] && chmod 600 "$HOME/.ssh/id_ed25519"
  [ -f "$HOME/.ssh/id_ed25519.pub" ] && chmod 644 "$HOME/.ssh/id_ed25519.pub"
fi
`, util.ShellQuote(deployPath), util.ShellQuote(deployUser))
}

func BuildPermissionsCheckScript(deployPath, deployUser string, expectEnvFile bool) string {
	return fmt.Sprintf(`
DEPLOY_PATH=%s
DEPLOY_USER=%s
EXPECT_ENV=%s

if [ -d "$DEPLOY_PATH" ]; then
  PATH_OWNER=$(stat -c '%%U' "$DEPLOY_PATH")
  PATH_MODE=$(stat -c '%%a' "$DEPLOY_PATH")
  if [ "$PATH_OWNER" = "$DEPLOY_USER" ] && [ "$PATH_MODE" = "750" ]; then
    pass permissions_deploy_path "owner=$PATH_OWNER mode=$PATH_MODE"
  else
    fail permissions_deploy_path "expected owner=$DEPLOY_USER mode=750, got owner=$PATH_OWNER mode=$PATH_MODE"
  fi
else
  skip permissions_deploy_path "deploy path missing"
fi

if [ -f "$DEPLOY_PATH/.env.production" ]; then
  ENV_OWNER=$(stat -c '%%U' "$DEPLOY_PATH/.env.production")
  ENV_MODE=$(stat -c '%%a' "$DEPLOY_PATH/.env.production")
  if [ "$ENV_OWNER" = "$DEPLOY_USER" ] && [ "$ENV_MODE" = "600" ]; then
    pass permissions_env_file ".env.production owner=$ENV_OWNER mode=$ENV_MODE"
  else
    fail permissions_env_file "expected owner=$DEPLOY_USER mode=600, got owner=$ENV_OWNER mode=$ENV_MODE"
  fi
elif [ "$EXPECT_ENV" = "true" ]; then
  fail permissions_env_file ".env.production missing (run: vpsdeploy deploy)"
else
  skip permissions_env_file "not configured"
fi

if [ -d "$HOME/.ssh" ]; then
  SSH_MODE=$(stat -c '%%a' "$HOME/.ssh")
  if [ "$SSH_MODE" = "700" ]; then
    pass permissions_ssh_dir "~/.ssh mode=$SSH_MODE"
  else
    fail permissions_ssh_dir "expected mode=700, got mode=$SSH_MODE"
  fi
else
  warn permissions_ssh_dir "~/.ssh missing"
fi
`, util.ShellQuote(deployPath), util.ShellQuote(deployUser), util.ShellQuote(boolString(expectEnvFile)))
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
