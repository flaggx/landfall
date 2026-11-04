package db

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flaggx/landfall/internal/util"
)

func writeStubs(t *testing.T, stubs map[string]string) string {
	t.Helper()
	bin := t.TempDir()
	for name, body := range stubs {
		writeExec(t, filepath.Join(bin, name), "#!/bin/bash\n"+body+"\n")
	}
	return bin
}

// Applies pgHbaSnippet to a pg_hba.conf and returns the new file and whether
// the snippet reported a change.
func applyHba(t *testing.T, hba, db, user, appHost string) (string, bool) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	file := filepath.Join(t.TempDir(), "pg_hba.conf")
	if err := os.WriteFile(file, []byte(hba), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := writeStubs(t, map[string]string{"sudo": `exec "$@"`})
	script := "set -euo pipefail\n" +
		"DB_NAME=" + util.ShellQuote(db) + "\nDB_USER=" + util.ShellQuote(user) + "\nAPP_HOST=" + util.ShellQuote(appHost) +
		"\nHBA=" + util.ShellQuote(file) + "\n" + pgHbaSnippet + `echo "HBA_CHANGED=$HBA_CHANGED"`
	cmd := exec.Command(bash, "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("snippet failed: %v\n%s", err, out)
	}
	raw, _ := os.ReadFile(file)
	return string(raw), strings.Contains(string(out), "HBA_CHANGED=true")
}

const stockHba = "local   all   postgres   peer\nhost    all   all   127.0.0.1/32   scram-sha-256\n"

func hbaLine(db, user, host string) string {
	return "host    " + db + "    " + user + "    " + host + "/32    scram-sha-256"
}

func TestPgHbaSnippet(t *testing.T) {
	// First environment: one block added, the rest kept.
	prod, changed := applyHba(t, stockHba, "app_prod", "app_prod", "203.0.113.10")
	if !changed || !strings.HasPrefix(prod, stockHba) || !strings.Contains(prod, hbaLine("app_prod", "app_prod", "203.0.113.10")) {
		t.Fatalf("first env:\n%s", prod)
	}

	// Second environment on the same DB server gets its own line too.
	both, _ := applyHba(t, prod, "app_dev", "app_dev", "203.0.113.11")
	for _, want := range []string{hbaLine("app_prod", "app_prod", "203.0.113.10"), hbaLine("app_dev", "app_dev", "203.0.113.11")} {
		if !strings.Contains(both, want) {
			t.Fatalf("missing %q:\n%s", want, both)
		}
	}

	// Re-running is a no-op (no reload).
	again, changed := applyHba(t, both, "app_dev", "app_dev", "203.0.113.11")
	if changed || again != both {
		t.Fatalf("re-run changed the file:\n%s", again)
	}

	// A changed app_host replaces that environment's line only.
	moved, changed := applyHba(t, both, "app_prod", "app_prod", "203.0.113.99")
	if !changed || strings.Contains(moved, "203.0.113.10/32") || !strings.Contains(moved, hbaLine("app_prod", "app_prod", "203.0.113.99")) || !strings.Contains(moved, hbaLine("app_dev", "app_dev", "203.0.113.11")) {
		t.Fatalf("app_host change:\n%s", moved)
	}
	if n := strings.Count(moved, "# landfall-app-access app_prod app_prod"); n != 1 {
		t.Fatalf("want one prod marker, got %d:\n%s", n, moved)
	}
}

// Files written by older versions used one bare marker for the whole host.
func TestPgHbaSnippetLegacyMarker(t *testing.T) {
	legacy := stockHba + "# landfall-app-access\n" + hbaLine("app_prod", "app_prod", "203.0.113.10") + "\n"

	// The legacy block belongs to prod: it is replaced, not duplicated.
	prod, _ := applyHba(t, legacy, "app_prod", "app_prod", "203.0.113.10")
	if strings.Contains(prod, "# landfall-app-access\n") || strings.Count(prod, hbaLine("app_prod", "app_prod", "203.0.113.10")) != 1 {
		t.Fatalf("legacy prod block:\n%s", prod)
	}

	// Another environment leaves the legacy block (prod's) alone and adds its own.
	dev, _ := applyHba(t, legacy, "app_dev", "app_dev", "203.0.113.11")
	if !strings.HasPrefix(dev, legacy) || !strings.Contains(dev, hbaLine("app_dev", "app_dev", "203.0.113.11")) {
		t.Fatalf("legacy block for another env:\n%s", dev)
	}
}

// Runs the bootstrap script (local DB) with stub sudo/psql/systemctl. psql
// answers the existence queries from $STUB_USER_EXISTS/$STUB_DB_EXISTS, logs every
// command, and fails any command matching $FAIL_ON.
func runBootstrap(t *testing.T, reset bool, env ...string) (map[string]string, string, []string, error) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl not available")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "psql.log")
	bin := writeStubs(t, map[string]string{
		"sudo":      `[ "$1" = -u ] && shift 2; exec "$@"`,
		"apt-get":   "exit 0",
		"systemctl": "exit 0",
		"psql": `cmd="${*: -1}"
echo "$cmd" >> ` + util.ShellQuote(log) + `
if [ -n "${FAIL_ON:-}" ] && [[ "$cmd" == *"$FAIL_ON"* ]]; then echo "ERROR: simulated" >&2; exit 1; fi
case "$cmd" in
  *"SHOW port"*) echo " ${STUB_PORT:-5432}" ;;
  *pg_roles*) [ "${STUB_USER_EXISTS:-}" = 1 ] && echo 1 ;;
  *pg_database*) [ "${STUB_DB_EXISTS:-}" = 1 ] && echo 1 ;;
esac
exit 0`,
	})
	result := filepath.Join(dir, "result.env")
	cfg := testCfg(t, false)
	script := buildBootstrapScript(cfg, "app_prod", "shared_user", result, reset)
	cmd := exec.Command(bash, "-c", script)
	cmd.Env = append(append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH")), env...)
	out, runErr := cmd.CombinedOutput()
	raw, _ := os.ReadFile(result)
	logRaw, _ := os.ReadFile(log)
	var cmds []string
	for _, l := range strings.Split(strings.TrimSpace(string(logRaw)), "\n") {
		if l != "" && !strings.HasPrefix(l, "SELECT") {
			cmds = append(cmds, l)
		}
	}
	return util.ParseEnvFile(string(raw)), string(out), cmds, runErr
}

func TestBootstrapSharedUserNewDatabase(t *testing.T) {
	vars, out, cmds, err := runBootstrap(t, false, "STUB_USER_EXISTS=1", "STUB_DB_EXISTS=0")
	if err != nil {
		t.Fatalf("bootstrap failed: %v\n%s", err, out)
	}
	if vars["DB_CREATED"] != "true" || vars["USER_CREATED"] != "false" || vars["DATABASE_URL"] != "" || vars["COMPLETE"] != "true" {
		t.Fatalf("result = %v", vars)
	}
	for _, c := range cmds {
		if strings.Contains(c, "ALTER USER") || strings.Contains(c, "CREATE USER") {
			t.Fatalf("an existing user's password must be left alone, ran %q", c)
		}
	}
}

func TestBootstrapResetRotatesLast(t *testing.T) {
	// A failure before the rotation leaves the password unchanged.
	_, out, cmds, err := runBootstrap(t, true, "STUB_USER_EXISTS=1", "STUB_DB_EXISTS=0", "FAIL_ON=GRANT")
	if err == nil {
		t.Fatalf("expected the simulated failure\n%s", out)
	}
	for _, c := range cmds {
		if strings.Contains(c, "ALTER USER") {
			t.Fatalf("password changed although an earlier step failed: %v", cmds)
		}
	}

	// Success: the rotation is the last psql command and the URL is returned.
	vars, out, cmds, err := runBootstrap(t, true, "STUB_USER_EXISTS=1", "STUB_DB_EXISTS=1")
	if err != nil {
		t.Fatalf("bootstrap failed: %v\n%s", err, out)
	}
	if last := cmds[len(cmds)-1]; !strings.Contains(last, "ALTER USER") {
		t.Fatalf("rotation must come last, commands: %v", cmds)
	}
	if !strings.HasPrefix(vars["DATABASE_URL"], "postgresql://shared_user:") || vars["COMPLETE"] != "true" {
		t.Fatalf("result = %v", vars)
	}
}

// A new user's password is recorded before CREATE DATABASE, which can fail.
func TestBootstrapKeepsNewUserPasswordOnFailure(t *testing.T) {
	vars, out, _, err := runBootstrap(t, false, "STUB_USER_EXISTS=0", "STUB_DB_EXISTS=0", "FAIL_ON=CREATE DATABASE")
	if err == nil {
		t.Fatalf("expected the simulated failure\n%s", out)
	}
	if !strings.HasPrefix(vars["DATABASE_URL"], "postgresql://shared_user:") || vars["COMPLETE"] != "false" {
		t.Fatalf("the new user's URL must be recorded, got %v", vars)
	}
}

// A server on another port than postgres.port is reported before anything
// changes, instead of failing later with a confusing connection error.
func TestBootstrapPortMismatch(t *testing.T) {
	_, out, cmds, err := runBootstrap(t, false, "STUB_PORT=5433")
	if err == nil || !strings.Contains(out, "listens on port 5433, but postgres.port is 5432") {
		t.Fatalf("expected a port mismatch error, got err=%v\n%s", err, out)
	}
	for _, c := range cmds {
		if strings.Contains(c, "CREATE") || strings.Contains(c, "ALTER") || strings.Contains(c, "GRANT") {
			t.Fatalf("nothing may change on a port mismatch, ran %q", c)
		}
	}
}
