package deploy

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/flaggx/landfall/internal/config"
	"github.com/flaggx/landfall/internal/security"
	"github.com/flaggx/landfall/internal/ssh"
)

type Options struct {
	Ref string
}

type Result struct {
	Commit    string
	Duration  time.Duration
	EnvName   string
	Service   string
	HealthURL string
}

func Run(cfg *config.ResolvedConfig, opts Options) (*Result, error) {
	start := time.Now()

	client, err := ssh.New(ssh.Options{
		Host:    cfg.Environment.Host,
		User:    cfg.Environment.User,
		KeyPath: cfg.Global.SSHKeyPath,
	})
	if err != nil {
		return nil, err
	}
	defer client.Close()

	serviceName := cfg.ServiceName()
	ref := opts.Ref
	if ref == "" {
		ref = cfg.Environment.Branch
	}

	fmt.Fprintf(os.Stdout, "Deploying %s to %s (%s)...\n", cfg.Project.Project.Name, cfg.EnvName, cfg.SSHAddress())

	steps := []struct {
		name string
		fn   func(*ssh.Client) error
	}{
		{"preflight", func(c *ssh.Client) error { return preflight(c, cfg) }},
		{"sync", func(c *ssh.Client) error { return syncRepo(c, cfg, ref) }},
		{"env", func(c *ssh.Client) error { return writeEnvFile(c, cfg) }},
		{"build", func(c *ssh.Client) error { return buildApp(c, cfg) }},
		{"activate", func(c *ssh.Client) error { return activateRelease(c, cfg) }},
		{"permissions", func(c *ssh.Client) error { return enforcePermissions(c, cfg) }},
		{"restart", func(c *ssh.Client) error { return restartService(c, serviceName) }},
		{"health", func(c *ssh.Client) error { return healthCheck(c, cfg) }},
	}

	for _, step := range steps {
		fmt.Fprintf(os.Stdout, "→ %s\n", step.name)
		if err := step.fn(client); err != nil {
			return nil, fmt.Errorf("%s: %w", step.name, err)
		}
	}

	commit, err := readDeployedCommit(client, cfg.Environment.Path)
	if err != nil {
		return nil, err
	}

	result := &Result{
		Commit:    commit,
		Duration:  time.Since(start),
		EnvName:   cfg.EnvName,
		Service:   serviceName,
		HealthURL: cfg.Environment.HealthCheck,
	}

	fmt.Fprintf(os.Stdout, "Deploy succeeded in %s (commit %s)\n", result.Duration.Round(time.Second), result.Commit)
	return result, nil
}

func preflight(client *ssh.Client, cfg *config.ResolvedConfig) error {
	script := fmt.Sprintf(`
set -euo pipefail
test -d %s || mkdir -p %s
command -v git >/dev/null
command -v npm >/dev/null
node --version
df -h %s | tail -1
`, shellQuote(cfg.Environment.Path), shellQuote(cfg.Environment.Path), shellQuote(cfg.Environment.Path))

	_, err := client.RunScript("landfall-preflight.sh", script)
	return err
}

func syncRepo(client *ssh.Client, cfg *config.ResolvedConfig, ref string) error {
	repo := cfg.Project.Project.Repo
	path := cfg.Environment.Path
	branch := cfg.Environment.Branch

	script := fmt.Sprintf(`
set -euo pipefail
if [ ! -d %s/.git ]; then
  git clone %s %s
fi
cd %s
git remote set-url origin %s
git fetch origin %s
if git show-ref --verify --quiet refs/heads/%s; then
  git checkout %s
else
  git checkout -b %s origin/%s 2>/dev/null || git checkout %s
fi
git reset --hard FETCH_HEAD
`, shellQuote(path), shellQuote(repo), shellQuote(path),
		shellQuote(path), shellQuote(repo), shellQuote(ref),
		shellQuote(ref), shellQuote(ref),
		shellQuote(ref), shellQuote(branch), shellQuote(ref))

	_, err := client.RunScript("landfall-sync.sh", script)
	return err
}

func writeEnvFile(client *ssh.Client, cfg *config.ResolvedConfig) error {
	if len(cfg.Environment.Env) == 0 {
		return nil
	}

	envPath := cfg.Environment.Path + "/.env.production"
	return client.UploadFile(envPath, []byte(formatEnvFile(cfg.Environment.Env)), 0o600)
}

func formatEnvFile(env map[string]string) string {
	var b strings.Builder
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		b.WriteString(fmt.Sprintf("%s=%s\n", key, shellQuoteValue(env[key])))
	}
	return b.String()
}

func enforcePermissions(client *ssh.Client, cfg *config.ResolvedConfig) error {
	script := security.BuildPermissionsScript(cfg.Environment.Path, cfg.Environment.User)
	_, err := client.RunScript("landfall-permissions.sh", script)
	return err
}

func buildApp(client *ssh.Client, cfg *config.ResolvedConfig) error {
	script := fmt.Sprintf(`
set -euo pipefail
cd %s
# Install build tooling (typescript, drizzle-kit, etc.) even when the
# production env file sets NODE_ENV=production.
npm ci --include=dev
export NODE_ENV=production
# Allow Next/TypeScript builds on small VPS plans (will spill to swap if needed).
export NODE_OPTIONS="${NODE_OPTIONS:---max-old-space-size=1536}"
if [ -f .env.production ]; then
  set -a
  . ./.env.production
  set +a
fi
npm run build
`, shellQuote(cfg.Environment.Path))

	_, err := client.RunScript("landfall-build.sh", script)
	return err
}

func activateRelease(client *ssh.Client, cfg *config.ResolvedConfig) error {
	script := fmt.Sprintf(`
set -euo pipefail
cd %s
if [ -d .next/standalone ]; then
  cp -r .next/static .next/standalone/.next/static 2>/dev/null || true
  cp -r public .next/standalone/public 2>/dev/null || true
fi
COMMIT=$(git rev-parse HEAD)
echo "commit=$COMMIT" > .deploy-meta
echo "deployed_at=$(date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ)" >> .deploy-meta
echo "env=%s" >> .deploy-meta
`, shellQuote(cfg.Environment.Path), cfg.EnvName)

	_, err := client.RunScript("landfall-activate.sh", script)
	return err
}

func restartService(client *ssh.Client, serviceName string) error {
	cmd := fmt.Sprintf("sudo systemctl restart %s && sudo systemctl is-active --quiet %s", serviceName, serviceName)
	_, err := client.Run(cmd)
	return err
}

func healthCheck(client *ssh.Client, cfg *config.ResolvedConfig) error {
	if cfg.Environment.HealthCheck == "" {
		return nil
	}

	script := fmt.Sprintf(`
set -euo pipefail
URL=%s
for i in $(seq 1 30); do
  BODY=$(curl -fsS --max-time 5 "$URL" 2>/dev/null || true)
  if [ -n "$BODY" ] && echo "$BODY" | grep -q '"ok"[[:space:]]*:[[:space:]]*true'; then
    exit 0
  fi
  sleep 2
done
echo "health check failed for $URL (expected JSON with ok:true)" >&2
exit 1
`, shellQuote(cfg.Environment.HealthCheck))

	_, err := client.RunScript("landfall-health.sh", script)
	return err
}

func readDeployedCommit(client *ssh.Client, path string) (string, error) {
	cmd := fmt.Sprintf("cd %s && git rev-parse --short HEAD", shellQuote(path))
	stdout := &strings.Builder{}
	stderr := os.Stderr
	_, err := client.RunWithOutput(cmd, stdout, stderr)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(stdout.String()), nil
}

func Status(cfg *config.ResolvedConfig) error {
	client, err := ssh.New(ssh.Options{
		Host:    cfg.Environment.Host,
		User:    cfg.Environment.User,
		KeyPath: cfg.Global.SSHKeyPath,
	})
	if err != nil {
		return err
	}
	defer client.Close()

	serviceName := cfg.ServiceName()
	script := fmt.Sprintf(`
set -euo pipefail
echo "Service: %s"
sudo systemctl is-active %s || true
sudo systemctl show %s --property=ActiveEnterTimestamp --value || true
if [ -f %s/.deploy-meta ]; then
  echo "--- deploy meta ---"
  cat %s/.deploy-meta
fi
if [ -d %s/.git ]; then
  echo "--- git ---"
  cd %s
  git rev-parse --short HEAD
  git log -1 --format="%%s"
fi
`, serviceName, serviceName, serviceName,
		shellQuote(cfg.Environment.Path), shellQuote(cfg.Environment.Path),
		shellQuote(cfg.Environment.Path), shellQuote(cfg.Environment.Path))

	_, err = client.RunScript("landfall-status.sh", script)
	return err
}

func Logs(cfg *config.ResolvedConfig, follow bool) error {
	client, err := ssh.New(ssh.Options{
		Host:    cfg.Environment.Host,
		User:    cfg.Environment.User,
		KeyPath: cfg.Global.SSHKeyPath,
	})
	if err != nil {
		return err
	}
	defer client.Close()

	serviceName := cfg.ServiceName()
	var cmd string
	if follow {
		cmd = fmt.Sprintf("sudo journalctl -u %s -f -n 100", serviceName)
	} else {
		cmd = fmt.Sprintf("sudo journalctl -u %s -n 100 --no-pager", serviceName)
	}

	_, err = client.Run(cmd)
	return err
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func shellQuoteValue(s string) string {
	if strings.ContainsAny(s, " \t\n\"'$\\") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}

// Avoid unused import when building with certain flags
var _ io.Writer = os.Stdout
