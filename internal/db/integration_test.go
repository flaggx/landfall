package db

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flaggx/landfall/internal/config"
	"github.com/flaggx/landfall/internal/util"
)

// These tests run the real bootstrap script against the machine's own
// PostgreSQL, with real sudo. They change the server (users, databases,
// pg_hba.conf, listen_addresses), so they only run when
// LANDFALL_PG_INTEGRATION=1, as in CI on a throwaway runner.
func pgIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("LANDFALL_PG_INTEGRATION") != "1" {
		t.Skip("set LANDFALL_PG_INTEGRATION=1 to run against the local PostgreSQL (changes its config)")
	}
}

func pgBootstrap(t *testing.T, cfg *config.ResolvedConfig, db, user string, reset bool) (map[string]string, string, error) {
	t.Helper()
	result := filepath.Join(t.TempDir(), "result.env")
	out, err := exec.Command("bash", "-c", buildBootstrapScript(cfg, db, user, result, reset)).CombinedOutput()
	raw, _ := os.ReadFile(result)
	return util.ParseEnvFile(string(raw)), string(out), err
}

// connect runs a query with the given URL and returns psql's output.
func connect(url string) (string, error) {
	out, err := exec.Command("psql", url, "-tAc", "SELECT current_database()").CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func uniqueName(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano()%1_000_000_000)
}

func TestPostgresBootstrapIntegration(t *testing.T) {
	pgIntegration(t)
	cfg := testCfg(t, false)
	user := uniqueName("lf_shared")
	prodDB, devDB := uniqueName("lf_prod"), uniqueName("lf_dev")

	// New user and database: a working URL.
	first, out, err := pgBootstrap(t, cfg, prodDB, user, false)
	if err != nil {
		t.Fatalf("first bootstrap: %v\n%s", err, out)
	}
	url := first["DATABASE_URL"]
	if got, err := connect(url); err != nil || got != prodDB {
		t.Fatalf("connect %q: %v %s", url, err, got)
	}

	// The same user for a second database (e.g. dev sharing prod's user):
	// the database is created, the password is kept, no error.
	second, out, err := pgBootstrap(t, cfg, devDB, user, false)
	if err != nil {
		t.Fatalf("shared-user bootstrap: %v\n%s", err, out)
	}
	if second["DB_CREATED"] != "true" || second["DATABASE_URL"] != "" {
		t.Fatalf("shared-user result: %v", second)
	}
	devURL := strings.Replace(url, "/"+prodDB, "/"+devDB, 1)
	if got, err := connect(devURL); err != nil || got != devDB {
		t.Fatalf("the existing password must work for the new database: %v %s", err, got)
	}

	// --reset-password rotates it: the new URL works, the old one doesn't.
	third, out, err := pgBootstrap(t, cfg, prodDB, user, true)
	if err != nil {
		t.Fatalf("reset bootstrap: %v\n%s", err, out)
	}
	if got, err := connect(third["DATABASE_URL"]); err != nil || got != prodDB {
		t.Fatalf("new password: %v %s", err, got)
	}
	if _, err := connect(url); err == nil {
		t.Fatal("the old password must stop working")
	}
}

// Two environments on one "dedicated" DB server each get their own pg_hba
// line. 127.0.0.2 stands in for the app VPS: only the line bootstrap adds
// lets a connection from it in.
func TestPostgresRemoteAccessIntegration(t *testing.T) {
	pgIntegration(t)
	cfgFor := func(appHost string) *config.ResolvedConfig {
		cfg := testCfg(t, true)
		cfg.Environment.Postgres.Host = "127.0.0.2"
		cfg.Environment.Postgres.AppHost = appHost
		return cfg
	}
	type env struct{ db, user, url string }
	envs := []*env{{db: uniqueName("lf_rprod"), user: uniqueName("lf_rprod")}, {db: uniqueName("lf_rdev"), user: uniqueName("lf_rdev")}}
	for _, e := range envs {
		vars, out, err := pgBootstrap(t, cfgFor("127.0.0.2"), e.db, e.user, false)
		if err != nil {
			t.Fatalf("bootstrap %s: %v\n%s", e.db, err, out)
		}
		e.url = vars["DATABASE_URL"]
		if !strings.Contains(e.url, "@127.0.0.2:") {
			t.Fatalf("URL must use the DB host: %q", e.url)
		}
	}
	// Before the fix, only the first environment got a pg_hba line.
	for _, e := range envs {
		if got, err := connect(e.url); err != nil || got != e.db {
			t.Fatalf("%s cannot connect from the app host: %v %s", e.db, err, got)
		}
	}
	// Re-running must not duplicate lines.
	if _, out, err := pgBootstrap(t, cfgFor("127.0.0.2"), envs[0].db, envs[0].user, false); err != nil {
		t.Fatalf("re-run: %v\n%s", err, out)
	}
	hba, err := exec.Command("bash", "-c", `sudo cat /etc/postgresql/*/main/pg_hba.conf`).Output()
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(hba), "# landfall-app-access "+envs[0].db+" "+envs[0].user); n != 1 {
		t.Fatalf("want one block for %s, got %d:\n%s", envs[0].db, n, hba)
	}
}
