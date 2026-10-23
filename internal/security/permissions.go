package security

import (
	"fmt"
	"strings"

	"github.com/flaggx/landfall/internal/config"
	"github.com/flaggx/landfall/internal/ssh"
	"github.com/flaggx/landfall/internal/util"
)

func EnforcePermissions(cfg *config.ResolvedConfig, client *ssh.Client) error {
	_, err := client.RunScript("landfall-security-permissions.sh", PermissionsScriptFor(cfg))
	return err
}

// PermissionsScriptFor builds the permissions script for an environment. If
// the app user's name is invalid it falls back to deploy-user-only ownership,
// so a deploy never fails over it; bootstrap reports the error instead.
func PermissionsScriptFor(cfg *config.ResolvedConfig) string {
	appUser, err := cfg.AppUser()
	if err != nil {
		appUser = ""
	}
	return BuildPermissionsScript(cfg.Environment.Path, cfg.Environment.User, appUser)
}

// RuntimeCacheDir is where a Next.js standalone server writes at runtime
// (image optimizer and data cache): the only path the app user may write.
func RuntimeCacheDir(deployPath string) string {
	return strings.TrimRight(deployPath, "/") + "/.next/standalone/.next/cache"
}

// BuildPermissionsScript makes the deploy user own the deploy path. When
// appUser exists on the server, the code is also given to the app user's
// group read-only, and the runtime cache to the app user; until bootstrap has
// created it, the script behaves as before.
func BuildPermissionsScript(deployPath, deployUser, appUser string) string {
	return fmt.Sprintf(`
set -euo pipefail

DEPLOY_PATH=%s
DEPLOY_USER=%s
APP_USER=%s
CACHE_DIR=%s

if [ -d "$DEPLOY_PATH" ]; then
  if [ -n "$APP_USER" ] && id -u "$APP_USER" >/dev/null 2>&1; then
    APP_GROUP=$(id -gn "$APP_USER")
    # The app reads the code through its group but cannot change it. The env
    # file is skipped: systemd reads it as root, so the app never needs to.
    sudo chown -R "$DEPLOY_USER:$APP_GROUP" "$DEPLOY_PATH"
    sudo find "$DEPLOY_PATH" -path "$DEPLOY_PATH/.env.production" -prune -o ! -type l -exec chmod g+rX,g-w {} +
    sudo chmod 750 "$DEPLOY_PATH"
    if [ -d "$DEPLOY_PATH/.next/standalone" ]; then
      sudo mkdir -p "$CACHE_DIR"
      sudo chown -R "$APP_USER:$APP_GROUP" "$CACHE_DIR"
      sudo chmod 750 "$CACHE_DIR"
    fi
  else
    sudo chown -R "$DEPLOY_USER:" "$DEPLOY_PATH"
    sudo chmod 750 "$DEPLOY_PATH"
  fi
fi

if [ -f "$DEPLOY_PATH/.env.production" ]; then
  sudo chown "$DEPLOY_USER:" "$DEPLOY_PATH/.env.production"
  sudo chmod 600 "$DEPLOY_PATH/.env.production"
fi

if [ -d "$HOME/.ssh" ]; then
  chmod 700 "$HOME/.ssh"
  if [ -f "$HOME/.ssh/id_ed25519" ]; then
    chmod 600 "$HOME/.ssh/id_ed25519"
  fi
  if [ -f "$HOME/.ssh/id_ed25519.pub" ]; then
    chmod 644 "$HOME/.ssh/id_ed25519.pub"
  fi
fi
`, util.ShellQuote(deployPath), util.ShellQuote(deployUser), util.ShellQuote(appUser), util.ShellQuote(RuntimeCacheDir(deployPath)))
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
  fail permissions_env_file ".env.production missing (run: landfall deploy)"
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
