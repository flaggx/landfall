package check

import (
	"testing"

	"github.com/flaggx/landfall/internal/config"
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
