package bootstrap

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/flaggx/landfall/internal/config"
	"github.com/flaggx/landfall/internal/security"
	"github.com/flaggx/landfall/internal/ssh"
)

//go:embed templates/systemd.service.tmpl
var systemdTemplate string

//go:embed templates/Caddyfile.tmpl
var caddyTemplate string

type systemdData struct {
	ServiceName string
	// User is the app user the service runs as, never the deploy user.
	User     string
	WorkDir  string
	Port     int
	CacheDir string
	// PathUnderHome turns off ProtectHome, which would hide the app.
	PathUnderHome bool
}

type caddyData struct {
	Domain string
	Port   int
}

func Run(cfg *config.ResolvedConfig, installCaddy bool) error {
	client, err := ssh.New(ssh.Options{
		Host:    cfg.Environment.Host,
		User:    cfg.Environment.User,
		KeyPath: cfg.Global.SSHKeyPath,
	})
	if err != nil {
		return err
	}
	defer client.Close()

	appUser, err := cfg.AppUser()
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "Bootstrapping %s on %s...\n", cfg.EnvName, cfg.SSHAddress())

	if err := runBaseSetup(client, cfg); err != nil {
		return fmt.Errorf("base setup: %w", err)
	}

	if err := createAppUser(client, appUser); err != nil {
		return fmt.Errorf("app user: %w", err)
	}

	// Before the unit restarts as the app user, so it can read the code.
	if err := security.EnforcePermissions(cfg, client); err != nil {
		return fmt.Errorf("permissions: %w", err)
	}

	if err := installSystemdUnit(client, cfg, appUser); err != nil {
		return fmt.Errorf("systemd: %w", err)
	}

	if installCaddy && cfg.Environment.Domain != "" {
		if err := installCaddyProxy(client, cfg); err != nil {
			return fmt.Errorf("caddy: %w", err)
		}
	}

	if err := setupDeployKey(client, cfg); err != nil {
		return fmt.Errorf("deploy key: %w", err)
	}

	fmt.Fprintf(os.Stdout, "Bootstrap complete for %s.\n", cfg.EnvName)
	printNextSteps(cfg)
	return nil
}

func runBaseSetup(client *ssh.Client, cfg *config.ResolvedConfig) error {
	script := fmt.Sprintf(`
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

if command -v apt-get >/dev/null 2>&1; then
  sudo apt-get update -qq
  sudo apt-get install -y -qq git curl ca-certificates build-essential
fi

# Node.js 22 LTS. Node 20 reached end of life in April 2026.
if ! command -v node >/dev/null 2>&1 || ! node --version | grep -qE '^v22\.'; then
  curl -fsSL https://deb.nodesource.com/setup_22.x | sudo -E bash -
  sudo apt-get install -y -qq nodejs
fi

sudo mkdir -p %s
sudo chown -R %s:%s %s

if [ ! -d %s/.git ]; then
  git clone %s %s || true
fi
`, shellQuote(cfg.Environment.Path),
		cfg.Environment.User, cfg.Environment.User, shellQuote(cfg.Environment.Path),
		shellQuote(cfg.Environment.Path),
		shellQuote(cfg.Project.Project.Repo),
		shellQuote(cfg.Environment.Path))

	_, err := client.RunScript("landfall-bootstrap-base.sh", script)
	return err
}

// createAppUser adds the system user the app runs as: no login shell, no
// home directory, no sudo, no password. Existing users are left alone.
func createAppUser(client *ssh.Client, appUser string) error {
	_, err := client.RunScript("landfall-bootstrap-app-user.sh", appUserScript(appUser))
	return err
}

func appUserScript(appUser string) string {
	return fmt.Sprintf(`
set -euo pipefail
APP_USER=%s
if ! id -u "$APP_USER" >/dev/null 2>&1; then
  sudo useradd --system --user-group --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin "$APP_USER"
  echo "Created app user $APP_USER"
fi
`, shellQuote(appUser))
}

func installSystemdUnit(client *ssh.Client, cfg *config.ResolvedConfig, appUser string) error {
	tmpl, err := template.New("systemd").Parse(systemdTemplate)
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	data := systemdData{
		ServiceName:   cfg.ServiceName(),
		User:          appUser,
		WorkDir:       cfg.Environment.Path,
		Port:          cfg.Environment.Port,
		CacheDir:      security.RuntimeCacheDir(cfg.Environment.Path),
		PathUnderHome: pathUnderHome(cfg.Environment.Path),
	}
	if err := tmpl.Execute(&buf, data); err != nil {
		return err
	}

	remotePath, err := client.TempPath(cfg.ServiceName() + ".service")
	if err != nil {
		return err
	}
	if err := client.UploadFile(remotePath, buf.Bytes(), 0o644); err != nil {
		return err
	}

	script := fmt.Sprintf(`
set -euo pipefail
sudo mv %s /etc/systemd/system/%s.service
sudo systemctl daemon-reload
sudo systemctl enable %s
sudo systemctl restart %s
`, shellQuote(remotePath), cfg.ServiceName(), cfg.ServiceName(), cfg.ServiceName())

	_, err = client.RunScript("landfall-bootstrap-systemd.sh", script)
	return err
}

func installCaddyProxy(client *ssh.Client, cfg *config.ResolvedConfig) error {
	tmpl, err := template.New("caddy").Parse(caddyTemplate)
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	data := caddyData{
		Domain: cfg.Environment.Domain,
		Port:   cfg.Environment.Port,
	}
	if err := tmpl.Execute(&buf, data); err != nil {
		return err
	}

	script := `
set -euo pipefail
if ! command -v caddy >/dev/null 2>&1; then
  sudo apt-get install -y -qq debian-keyring debian-archive-keyring apt-transport-https curl
  curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
  curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | sudo tee /etc/apt/sources.list.d/caddy-stable.list
  sudo apt-get update -qq
  sudo apt-get install -y -qq caddy
fi
`

	if _, err := client.RunScript("landfall-bootstrap-caddy-install.sh", script); err != nil {
		return err
	}

	caddyPath, err := client.TempPath("caddy-" + cfg.ServiceName() + ".conf")
	if err != nil {
		return err
	}
	if err := client.UploadFile(caddyPath, buf.Bytes(), 0o644); err != nil {
		return err
	}

	applyScript := caddyApplyScript("/etc/caddy", caddyPath, cfg.ServiceName())

	_, err = client.RunScript("landfall-bootstrap-caddy-apply.sh", applyScript)
	return err
}

// stockCaddyfile is the Caddyfile the caddy package installs, with comments
// and all whitespace removed: a :80 demo site serving /usr/share/caddy.
const stockCaddyfile = ":80{root*/usr/share/caddyfile_server}"

// caddyApplyScript installs a site file into <caddyDir>/landfall and makes
// sure the main Caddyfile imports that directory, without losing sites the
// server already has:
//
//   - missing, empty or the package's stock demo file (whose :80 site would
//     steal HTTP traffic from the imported vhosts): replaced by the import.
//   - anything else: the import is appended, and the rest is left as it is.
//
// The previous Caddyfile is backed up first. If Caddy rejects the result,
// the backup is restored and nothing is reloaded.
func caddyApplyScript(caddyDir, uploadedSite, serviceName string) string {
	return fmt.Sprintf(`
set -euo pipefail
CADDY_DIR=%s
SITE_SRC=%s
SITE_NAME=%s
STOCK=%s
CADDYFILE="$CADDY_DIR/Caddyfile"

sudo mkdir -p "$CADDY_DIR/landfall"
sudo mv "$SITE_SRC" "$CADDY_DIR/landfall/$SITE_NAME.caddy"

if ! sudo grep -qE '^[[:space:]]*import[[:space:]]+landfall/' "$CADDYFILE" 2>/dev/null; then
  BACKUP=""
  if sudo test -s "$CADDYFILE"; then
    BACKUP="$CADDYFILE.landfall-backup-$(date -u +%%Y%%m%%dT%%H%%M%%SZ)"
    sudo cp -p "$CADDYFILE" "$BACKUP"
    echo "Backed up $CADDYFILE to $BACKUP"
  fi
  NORMALIZED=$(sudo cat "$CADDYFILE" 2>/dev/null | sed -e 's/#.*//' | tr -d '[:space:]' || true)
  if [ -z "$NORMALIZED" ] || [ "$NORMALIZED" = "$STOCK" ]; then
    printf '%%s\n' 'import landfall/*' | sudo tee "$CADDYFILE" >/dev/null
  else
    # Keep the existing sites; a top-level import at the end adds ours.
    printf '\n# Sites managed by landfall\nimport landfall/*\n' | sudo tee -a "$CADDYFILE" >/dev/null
    echo "Added 'import landfall/*' to the existing $CADDYFILE"
  fi
  if ! sudo caddy validate --config "$CADDYFILE" --adapter caddyfile >/dev/null 2>&1; then
    if [ -n "$BACKUP" ]; then
      sudo cp -p "$BACKUP" "$CADDYFILE"
    else
      sudo rm -f "$CADDYFILE"
    fi
    echo "Caddy rejected $CADDYFILE with the landfall import, so it was restored unchanged." >&2
    echo "Add 'import landfall/*' to it yourself, then run: sudo systemctl reload caddy" >&2
    exit 1
  fi
fi
sudo systemctl reload caddy || sudo systemctl restart caddy
`, shellQuote(caddyDir), shellQuote(uploadedSite), shellQuote(serviceName), shellQuote(stockCaddyfile))
}

func setupDeployKey(client *ssh.Client, cfg *config.ResolvedConfig) error {
	script := `
set -euo pipefail
mkdir -p ~/.ssh
chmod 700 ~/.ssh
if [ ! -f ~/.ssh/id_ed25519 ]; then
  ssh-keygen -t ed25519 -N "" -f ~/.ssh/id_ed25519 -C "landfall@$(hostname)"
fi
# Trust GitHub host keys so git clone/fetch over SSH works without prompts.
touch ~/.ssh/known_hosts
chmod 600 ~/.ssh/known_hosts
if ! grep -q '^github.com ' ~/.ssh/known_hosts 2>/dev/null; then
  ssh-keyscan -t ed25519,rsa github.com >> ~/.ssh/known_hosts 2>/dev/null || true
fi
echo "--- Add this deploy key to GitHub (read-only) ---"
cat ~/.ssh/id_ed25519.pub
`

	_, err := client.RunScript("landfall-bootstrap-deploykey.sh", script)
	return err
}

func printNextSteps(cfg *config.ResolvedConfig) {
	fmt.Fprintln(os.Stdout, "")
	fmt.Fprintln(os.Stdout, "Next steps:")
	fmt.Fprintln(os.Stdout, "1. Add the deploy key printed above to your GitHub repo (Settings → Deploy keys)")
	fmt.Fprintf(os.Stdout, "2. Optional: landfall security harden --env %s\n", cfg.EnvName)
	fmt.Fprintf(os.Stdout, "3. Optional: landfall db bootstrap --env %s --save-secret\n", cfg.EnvName)
	fmt.Fprintf(os.Stdout, "4. Optional: landfall redis bootstrap --env %s --save-secret\n", cfg.EnvName)
	fmt.Fprintf(os.Stdout, "5. Run: landfall deploy --env %s\n", cfg.EnvName)
	if cfg.Environment.Domain != "" {
		fmt.Fprintf(os.Stdout, "6. Point DNS for %s to %s\n", cfg.Environment.Domain, cfg.Environment.Host)
	}
}

func pathUnderHome(path string) bool {
	clean := filepath.Clean(path)
	for _, dir := range []string{"/home", "/root", "/run/user"} {
		if clean == dir || strings.HasPrefix(clean, dir+"/") {
			return true
		}
	}
	return false
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
