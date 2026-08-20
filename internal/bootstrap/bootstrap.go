package bootstrap

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"strings"
	"text/template"

	"github.com/flaggx/landfall/internal/config"
	"github.com/flaggx/landfall/internal/ssh"
)

//go:embed templates/systemd.service.tmpl
var systemdTemplate string

//go:embed templates/Caddyfile.tmpl
var caddyTemplate string

type systemdData struct {
	ServiceName string
	User        string
	WorkDir     string
	Port        int
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

	fmt.Fprintf(os.Stdout, "Bootstrapping %s on %s...\n", cfg.EnvName, cfg.SSHAddress())

	if err := runBaseSetup(client, cfg); err != nil {
		return fmt.Errorf("base setup: %w", err)
	}

	if err := installSystemdUnit(client, cfg); err != nil {
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

if ! command -v node >/dev/null 2>&1 || ! node --version | grep -qE '^v20\.'; then
  curl -fsSL https://deb.nodesource.com/setup_20.x | sudo -E bash -
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

func installSystemdUnit(client *ssh.Client, cfg *config.ResolvedConfig) error {
	tmpl, err := template.New("systemd").Parse(systemdTemplate)
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	data := systemdData{
		ServiceName: cfg.ServiceName(),
		User:        cfg.Environment.User,
		WorkDir:     cfg.Environment.Path,
		Port:        cfg.Environment.Port,
	}
	if err := tmpl.Execute(&buf, data); err != nil {
		return err
	}

	remotePath := fmt.Sprintf("/tmp/%s.service", cfg.ServiceName())
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

	caddyPath := fmt.Sprintf("/tmp/caddy-%s.conf", cfg.ServiceName())
	if err := client.UploadFile(caddyPath, buf.Bytes(), 0o644); err != nil {
		return err
	}

	applyScript := fmt.Sprintf(`
set -euo pipefail
sudo mkdir -p /etc/caddy/landfall
sudo mv %s /etc/caddy/landfall/%s.caddy
CADDYFILE="/etc/caddy/Caddyfile"
# Replace the stock :80 demo site so it does not steal HTTP traffic from
# imported vhosts (needed for ACME HTTP-01 and reverse proxy routing).
if grep -q 'root \* /usr/share/caddy' "$CADDYFILE" 2>/dev/null || ! grep -q 'import landfall' "$CADDYFILE" 2>/dev/null; then
  printf '%%s\n' 'import landfall/*' | sudo tee "$CADDYFILE" >/dev/null
fi
sudo systemctl reload caddy || sudo systemctl restart caddy
`, shellQuote(caddyPath), cfg.ServiceName())

	_, err = client.RunScript("landfall-bootstrap-caddy-apply.sh", applyScript)
	return err
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

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
