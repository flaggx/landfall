package check

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/flaggx/landfall/internal/config"
	"github.com/flaggx/landfall/internal/security"
	"github.com/flaggx/landfall/internal/ssh"
	"github.com/flaggx/landfall/internal/util"
)

type Result struct {
	Name    string
	Status  Status
	Detail  string
	Skipped bool
}

type Status string

const (
	StatusPass Status = "PASS"
	StatusFail Status = "FAIL"
	StatusWarn Status = "WARN"
	StatusSkip Status = "SKIP"
)

type Summary struct {
	Results []Result
}

func (s Summary) AllPassed() bool {
	for _, r := range s.Results {
		if r.Skipped {
			continue
		}
		if r.Status == StatusFail {
			return false
		}
	}
	return true
}

func Run(cfg *config.ResolvedConfig) (Summary, error) {
	summary := Summary{}

	required, missing, err := config.CheckSecretsForProject(cfg.ProjectPath)
	if err != nil {
		return summary, err
	}
	if len(required) == 0 {
		summary.Results = append(summary.Results, Result{
			Name:   "local secrets",
			Status: StatusPass,
			Detail: "no secrets required",
		})
	} else if len(missing) > 0 {
		summary.Results = append(summary.Results, Result{
			Name:   "local secrets",
			Status: StatusFail,
			Detail: fmt.Sprintf("missing: %s", strings.Join(missing, ", ")),
		})
	} else {
		summary.Results = append(summary.Results, Result{
			Name:   "local secrets",
			Status: StatusPass,
			Detail: fmt.Sprintf("%d secret(s) configured", len(required)),
		})
	}

	client, err := ssh.New(ssh.Options{
		Host:    cfg.Environment.Host,
		User:    cfg.Environment.User,
		KeyPath: cfg.Global.SSHKeyPath,
	})
	if err != nil {
		summary.Results = append(summary.Results, Result{
			Name:   "ssh connection",
			Status: StatusFail,
			Detail: err.Error(),
		})
		return summary, nil
	}
	defer client.Close()

	summary.Results = append(summary.Results, Result{
		Name:   "ssh connection",
		Status: StatusPass,
		Detail: cfg.SSHAddress(),
	})

	remoteResults, err := runRemoteChecks(client, cfg)
	if err != nil {
		return summary, err
	}
	summary.Results = append(summary.Results, remoteResults...)

	return summary, nil
}

func runRemoteChecks(client *ssh.Client, cfg *config.ResolvedConfig) ([]Result, error) {
	dbName, _ := cfg.PostgresDatabase()
	redisUser, _ := cfg.RedisUser()
	redisPort := cfg.RedisPort()
	redisDB := cfg.RedisDatabase()
	expectDB := envExpectsPostgres(cfg)
	expectRedis := envExpectsRedis(cfg)
	healthURL := cfg.Environment.HealthCheck
	serviceName := cfg.ServiceName()
	deployPath := cfg.Environment.Path

	expectEnvFile := len(cfg.Environment.Env) > 0
	permissionsChecks := security.BuildPermissionsCheckScript(deployPath, cfg.Environment.User, expectEnvFile)

	resultPath := fmt.Sprintf("/tmp/landfall-check-%s.env", cfg.EnvName)
	script := fmt.Sprintf(`
set -euo pipefail
RESULT=%s
: > "$RESULT"

pass() { echo "$1=PASS|$2" >> "$RESULT"; }
fail() { echo "$1=FAIL|$2" >> "$RESULT"; }
warn() { echo "$1=WARN|$2" >> "$RESULT"; }
skip() { echo "$1=SKIP|$2" >> "$RESULT"; }

%s

# Security
if command -v ufw >/dev/null 2>&1 && sudo ufw status 2>/dev/null | grep -q "Status: active"; then
  pass security_ufw "active"
else
  fail security_ufw "not active (run: landfall security harden)"
fi

if dpkg -s unattended-upgrades >/dev/null 2>&1 && systemctl is-active unattended-upgrades >/dev/null 2>&1; then
  pass security_auto_updates "enabled"
else
  fail security_auto_updates "not configured (run: landfall security harden)"
fi

if command -v fail2ban-client >/dev/null 2>&1 && sudo systemctl is-active fail2ban >/dev/null 2>&1; then
  pass security_fail2ban "active"
else
  fail security_fail2ban "not active (run: landfall security harden)"
fi

if [ -f /etc/ssh/sshd_config.d/99-landfall.conf ] || [ -f /etc/ssh/sshd_config.d/99-vpsdeploy.conf ]; then
  pass security_ssh "hardening drop-in present"
else
  warn security_ssh "drop-in missing (run: landfall security harden)"
fi

# Bootstrap / runtime
if command -v node >/dev/null 2>&1; then
  pass node "$(node --version)"
else
  fail node "not installed (run: landfall bootstrap)"
fi

if [ -d %s ]; then
  pass deploy_path "exists"
else
  fail deploy_path "missing (run: landfall bootstrap)"
fi

if [ -d %s/.git ]; then
  pass git_repo "cloned"
else
  fail git_repo "not cloned (run: landfall bootstrap or deploy)"
fi

if systemctl list-unit-files %s.service >/dev/null 2>&1; then
  if sudo systemctl is-active --quiet %s; then
    pass systemd "service active"
  else
    fail systemd "service not active (run: landfall deploy)"
  fi
else
  fail systemd "unit missing (run: landfall bootstrap)"
fi

# PostgreSQL
if [ "%s" = "true" ]; then
  if command -v psql >/dev/null 2>&1 && sudo systemctl is-active postgresql >/dev/null 2>&1; then
    if sudo -u postgres psql -tAc "SELECT 1 FROM pg_database WHERE datname='%s'" | grep -q 1; then
      pass postgres "database %s exists"
    else
      fail postgres "database missing (run: landfall db bootstrap)"
    fi
  else
    fail postgres "not running (run: landfall db bootstrap)"
  fi
else
  skip postgres "not configured"
fi

# Redis
if [ "%s" = "true" ]; then
  if command -v redis-cli >/dev/null 2>&1 && sudo systemctl is-active redis-server >/dev/null 2>&1 || sudo systemctl is-active redis >/dev/null 2>&1; then
    if redis-cli -p %d ACL GETUSER %s >/dev/null 2>&1; then
      pass redis "ACL user %s (db %d)"
    else
      fail redis "ACL user missing (run: landfall redis bootstrap)"
    fi
  else
    fail redis "not running (run: landfall redis bootstrap)"
  fi
else
  skip redis "not configured"
fi

# App health endpoint
if [ -n "%s" ]; then
  BODY=$(curl -fsS "%s" 2>/dev/null || true)
  if [ -z "$BODY" ]; then
    fail health_endpoint "no response from %s"
  elif echo "$BODY" | grep -q '"ok"[[:space:]]*:[[:space:]]*true'; then
    pass health_endpoint "ok=true"
    echo "health_body=$BODY" >> "$RESULT"
  else
    fail health_endpoint "expected JSON with ok:true from %s"
    echo "health_body=$BODY" >> "$RESULT"
  fi
else
  skip health_endpoint "health_check not configured in landfall.toml"
fi

chmod 600 "$RESULT"
`, util.ShellQuote(resultPath), permissionsChecks,
		util.ShellQuote(deployPath), util.ShellQuote(deployPath),
		serviceName, serviceName,
		boolString(expectDB), dbName, dbName,
		boolString(expectRedis), redisPort, redisUser, redisUser, redisDB,
		healthURL, healthURL, healthURL, healthURL)

	if _, err := client.RunScript("landfall-check.sh", script); err != nil {
		return nil, err
	}

	raw, err := client.ReadFile(resultPath)
	if err != nil {
		return nil, fmt.Errorf("read check results: %w", err)
	}
	_, _ = client.Run(fmt.Sprintf("rm -f %s", util.ShellQuote(resultPath)))

	return parseRemoteResults(string(raw)), nil
}

func parseRemoteResults(raw string) []Result {
	var results []Result
	var healthBody string

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "health_body=") {
			healthBody = strings.TrimPrefix(line, "health_body=")
			continue
		}

		name, rest, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		statusText, detail, ok := strings.Cut(rest, "|")
		if !ok {
			continue
		}

		result := Result{
			Name:   strings.ReplaceAll(name, "_", " "),
			Status: Status(statusText),
			Detail: detail,
		}
		if result.Status == StatusSkip {
			result.Skipped = true
		}
		results = append(results, result)
	}

	if healthBody != "" {
		results = append(results, parseHealthBodyChecks(healthBody)...)
	}

	return results
}

func parseHealthBodyChecks(body string) []Result {
	var payload struct {
		OK     bool              `json:"ok"`
		Checks map[string]string `json:"checks"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return nil
	}

	if len(payload.Checks) == 0 {
		return nil
	}

	var results []Result
	for name, value := range payload.Checks {
		status := StatusPass
		if value != "ok" {
			status = StatusFail
		}
		results = append(results, Result{
			Name:   "health: " + name,
			Status: status,
			Detail: value,
		})
	}
	return results
}

func envExpectsPostgres(cfg *config.ResolvedConfig) bool {
	if cfg.Environment.Postgres != nil {
		return true
	}
	for key, value := range cfg.Environment.Env {
		if strings.EqualFold(key, "DATABASE_URL") && strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func envExpectsRedis(cfg *config.ResolvedConfig) bool {
	if cfg.Environment.Redis != nil {
		return true
	}
	for key, value := range cfg.Environment.Env {
		if strings.EqualFold(key, "REDIS_URL") && strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func PrintSummary(summary Summary, envName string) error {
	fmt.Fprintf(os.Stdout, "Setup check for %s\n\n", envName)
	fmt.Fprintf(os.Stdout, "%-22s %-6s %s\n", "CHECK", "STATUS", "DETAIL")
	fmt.Fprintf(os.Stdout, "%s\n", strings.Repeat("-", 72))

	failures := 0
	for _, r := range summary.Results {
		fmt.Fprintf(os.Stdout, "%-22s %-6s %s\n", r.Name, r.Status, r.Detail)
		if r.Status == StatusFail {
			failures++
		}
	}

	fmt.Fprintln(os.Stdout)
	if failures > 0 {
		fmt.Fprintf(os.Stdout, "%d check(s) failed.\n", failures)
		return fmt.Errorf("setup check failed")
	}
	if summary.AllPassed() {
		fmt.Fprintln(os.Stdout, "All checks passed.")
	}
	return nil
}
