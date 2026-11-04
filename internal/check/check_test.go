package check

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flaggx/landfall/internal/config"
	"github.com/flaggx/landfall/internal/util"
)

func TestParseRemoteResults(t *testing.T) {
	raw := `security_ufw=PASS|active
security_auto_updates=PASS|enabled
postgres=SKIP|not configured
health_endpoint=PASS|ok=true
health_body={"ok":true,"checks":{"app":"ok","database":"ok"}}`

	results := parseRemoteResults(raw)
	if len(results) < 4 {
		t.Fatalf("expected at least 4 results, got %d", len(results))
	}

	foundHealthApp := false
	for _, r := range results {
		if r.Name == "health: app" && r.Status == StatusPass {
			foundHealthApp = true
		}
	}
	if !foundHealthApp {
		t.Fatal("expected parsed health check detail")
	}
}

func TestEnvExpectsPostgres(t *testing.T) {
	cfg := &config.ResolvedConfig{
		Environment: config.Environment{
			Env: map[string]string{
				"DATABASE_URL": "{{secret:prod_db_url}}",
			},
		},
	}
	if !envExpectsPostgres(cfg) {
		t.Fatal("expected postgres to be required")
	}
}

func TestEnvExpectsRedis(t *testing.T) {
	cfg := &config.ResolvedConfig{
		Environment: config.Environment{
			Env: map[string]string{
				"REDIS_URL": "{{secret:prod_redis_url}}",
			},
		},
	}
	if !envExpectsRedis(cfg) {
		t.Fatal("expected redis to be required")
	}
}

func TestSummaryAllPassed(t *testing.T) {
	summary := Summary{
		Results: []Result{
			{Name: "ssh", Status: StatusPass},
			{Name: "postgres", Status: StatusSkip, Skipped: true},
		},
	}
	if !summary.AllPassed() {
		t.Fatal("expected all passed")
	}

	summary.Results = append(summary.Results, Result{Name: "ufw", Status: StatusFail})
	if summary.AllPassed() {
		t.Fatal("expected failure")
	}
}

// The ISR check counts Next.js's failed cache writes in the service's log
// (through a stub journalctl) and reports them as a warning.
func TestISRCacheCheckScript(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	for _, tc := range []struct {
		name, log  string
		wantStatus Status
		wantDetail string
	}{
		{"no ISR failures", "Nov 04 app[1]: ready on port 3000\n", StatusPass, "no failed ISR cache writes"},
		{"failed writes", "Nov 04 app[1]: Failed to update prerender cache for /blog/post Error: EROFS: read-only file system, open '/var/www/app/.next/standalone/.next/server/app/blog/post.html'\n" +
			"Nov 04 app[1]: Failed to update prerender cache for /about [Error: EROFS: read-only file system]\n", StatusWarn, "2 failed ISR cache write(s)"},
		{"no journal access", "", StatusPass, "no failed ISR cache writes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			logFile := filepath.Join(t.TempDir(), "journal")
			if err := os.WriteFile(logFile, []byte(tc.log), 0o644); err != nil {
				t.Fatal(err)
			}
			for name, body := range map[string]string{
				"sudo":       `exec "$@"`,
				"journalctl": `[ "$2" = 'my-webapp-prod.service' ] || exit 1; cat ` + util.ShellQuote(logFile),
			} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			result := filepath.Join(t.TempDir(), "result")
			script := "set -euo pipefail\nRESULT=" + util.ShellQuote(result) + "\n" +
				`pass() { echo "$1=PASS|$2" >> "$RESULT"; }` + "\n" +
				`warn() { echo "$1=WARN|$2" >> "$RESULT"; }` + "\n" +
				isrCacheCheckScript("my-webapp-prod")
			cmd := exec.Command(bash, "-c", script)
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("script failed: %v\n%s", err, out)
			}
			raw, _ := os.ReadFile(result)
			results := parseRemoteResults(string(raw))
			if len(results) != 1 {
				t.Fatalf("results = %+v", results)
			}
			r := results[0]
			if r.Name != "isr cache" || r.Status != tc.wantStatus || !strings.Contains(r.Detail, tc.wantDetail) {
				t.Fatalf("got %+v, want %s with %q", r, tc.wantStatus, tc.wantDetail)
			}
		})
	}
}

// With a dedicated DB server, check tests that its port is reachable from
// the app server instead of running psql where PostgreSQL isn't installed.
// 127.0.0.2 stands in for the DB host.
func TestPostgresCheckRemote(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	ln, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Skipf("cannot listen on 127.0.0.2: %v", err)
	}
	defer ln.Close()
	open := ln.Addr().(*net.TCPAddr).Port
	closed, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Fatal(err)
	}
	closedPort := closed.Addr().(*net.TCPAddr).Port
	closed.Close()

	for _, tc := range []struct {
		port int
		want Status
	}{{open, StatusPass}, {closedPort, StatusFail}} {
		cfg := &config.ResolvedConfig{Environment: config.Environment{
			Host:     "203.0.113.10",
			Postgres: &config.PostgresConfig{Host: "127.0.0.2", Port: tc.port},
		}}
		result := filepath.Join(t.TempDir(), "result")
		script := "set -euo pipefail\nRESULT=" + util.ShellQuote(result) + "\n" +
			`pass() { echo "$1=PASS|$2" >> "$RESULT"; }` + "\n" +
			`fail() { echo "$1=FAIL|$2" >> "$RESULT"; }` + "\n" +
			postgresCheckScript(cfg, true, "app_prod")
		if strings.Contains(script, "psql") {
			t.Fatal("the remote check must not run psql on the app server")
		}
		if out, err := exec.Command(bash, "-c", script).CombinedOutput(); err != nil {
			t.Fatalf("script failed: %v\n%s", err, out)
		}
		raw, _ := os.ReadFile(result)
		results := parseRemoteResults(string(raw))
		if len(results) != 1 || results[0].Status != tc.want || !strings.Contains(results[0].Detail, "127.0.0.2:") {
			t.Fatalf("port %d: got %+v, want %s", tc.port, results, tc.want)
		}
	}
}
