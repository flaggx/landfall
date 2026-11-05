package redis

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flaggx/landfall/internal/util"
)

// These tests run bootstrap and migration with real sudo and systemd, using
// the redis-server package's own redis-server@ template unit and the real
// paths (/etc/redis, /var/lib/redis, ...), against the package's default
// instance on 6379 as the shared Redis. They change the machine, so they
// only run when LANDFALL_REDIS_SYSTEMD=1, as in CI on a throwaway runner.
func systemdTest(t *testing.T) {
	t.Helper()
	if os.Getenv("LANDFALL_REDIS_SYSTEMD") != "1" {
		t.Skip("set LANDFALL_REDIS_SYSTEMD=1 to run against the real redis-server@ unit (changes this machine)")
	}
}

func rootSh(t *testing.T, script string) (string, error) {
	t.Helper()
	out, err := exec.Command("bash", "-c", script).CombinedOutput()
	return string(out), err
}

// realInstance returns a unique instance name and removes the instance when
// the test ends.
func realInstance(t *testing.T, prefix string) string {
	t.Helper()
	name := fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()%1_000_000)
	inst := DefaultLayout.Instance(name)
	t.Cleanup(func() {
		_, _ = rootSh(t, fmt.Sprintf("sudo systemctl disable --now %s; sudo rm -rf %s %s %s %s",
			util.ShellQuote(inst.Unit), util.ShellQuote(inst.Conf), util.ShellQuote(inst.DataDir),
			util.ShellQuote(inst.AdminFile), util.ShellQuote(inst.LogFile)))
	})
	return name
}

func realCLI(args ...string) string {
	out, _ := exec.Command("redis-cli", append([]string{"--no-auth-warning"}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out))
}

// urlArgs turns a redis:// URL into redis-cli arguments (6.0's redis-cli
// ignores the username in a URL).
func urlArgs(t *testing.T, raw string) []string {
	t.Helper()
	vars := strings.TrimPrefix(raw, "redis://")
	cred, hostPath, _ := strings.Cut(vars, "@")
	user, pass, _ := strings.Cut(cred, ":")
	hostPort, db, _ := strings.Cut(hostPath, "/")
	_, port, _ := strings.Cut(hostPort, ":")
	return []string{"--user", user, "--pass", pass, "-p", port, "-n", db}
}

func TestRealSystemdInstance(t *testing.T) {
	systemdTest(t)
	name := realInstance(t, "lf-ci")
	result := filepath.Join(t.TempDir(), "result.env")
	p := bootstrapParams{
		Layout: DefaultLayout, EnvName: "ci", Instance: name, User: "app_ci",
		Maxmemory: "32mb", Policy: "noeviction", ResultPath: result,
	}
	out, err := rootSh(t, buildBootstrapScript(p))
	if err != nil {
		t.Fatalf("bootstrap: %v\n%s", err, out)
	}
	raw, _ := os.ReadFile(result)
	vars := util.ParseEnvFile(string(raw))
	app := urlArgs(t, vars["REDIS_URL"])
	if got := realCLI(append(app, "set", "k", "v")...); got != "OK" {
		t.Fatalf("app user on the real instance: %q (bootstrap output:\n%s)", got, out)
	}

	// The package's unit really runs it, under its sandbox, as redis.
	unit := DefaultLayout.Instance(name).Unit
	if out, err := rootSh(t, "systemctl is-active "+unit+" && systemctl show -p User --value "+unit); err != nil || !strings.Contains(out, "redis") {
		t.Fatalf("unit state: %v\n%s", err, out)
	}

	// Restart through systemd keeps users and data (RDB on clean stop).
	if out, err := rootSh(t, "sudo systemctl restart "+unit); err != nil {
		t.Fatalf("restart: %v\n%s", err, out)
	}
	ok := false
	for i := 0; i < 50 && !ok; i++ {
		ok = realCLI(append(app, "get", "k")...) == "v"
		time.Sleep(100 * time.Millisecond)
	}
	if !ok {
		t.Fatalf("data or user lost after a systemd restart: %q", realCLI(append(app, "get", "k")...))
	}

	// The status script runs against it.
	if out, err := rootSh(t, buildStatusScript(DefaultLayout, "ci", name, "app_ci", 0)); err != nil || !strings.Contains(out, "Default user locked: yes") {
		t.Fatalf("status: %v\n%s", err, out)
	}
}

// Migration from the package's default instance (the shared Redis on 6379,
// open default user as older Landfall left it) into a real instance.
func TestRealSystemdMigration(t *testing.T) {
	systemdTest(t)
	if got := realCLI("-p", "6379", "ping"); got != "PONG" {
		t.Skipf("the default redis-server instance on 6379 is not open: %q", got)
	}
	name := realInstance(t, "lf-ci-mig")
	user := "app_" + strings.ReplaceAll(name, "-", "_")
	db := "7"
	t.Cleanup(func() {
		realCLI("-p", "6379", "ACL", "DELUSER", user)
		realCLI("-p", "6379", "-n", db, "FLUSHDB")
	})
	rules := []string{"~*", "+@all"}
	if !strings.Contains(realCLI("-p", "6379", "INFO", "server"), "redis_version:6.0") {
		rules = []string{"~*", "&*", "+@all"}
	}
	if got := realCLI(append([]string{"-p", "6379", "ACL", "SETUSER", user, "on", ">old"}, rules...)...); got != "OK" {
		t.Fatalf("setup user: %q", got)
	}
	realCLI("-p", "6379", "-n", db, "set", "session:1", "alice")
	realCLI("-p", "6379", "-n", db, "set", "ttl:1", "v", "EX", "600")

	if out, err := rootSh(t, buildMigrateCheckScript(DefaultLayout, "ci", name, user, 7)); err != nil || !strings.Contains(out, "Keys to copy from DB 7: 2") {
		t.Fatalf("migrate check: %v\n%s", err, out)
	}

	// Without --fresh/migrate, bootstrap refuses to strand the data.
	result := filepath.Join(t.TempDir(), "result.env")
	p := bootstrapParams{Layout: DefaultLayout, EnvName: "ci", Instance: name, User: user, Maxmemory: "32mb", Policy: "noeviction", ResultPath: result}
	if out, err := rootSh(t, buildBootstrapScript(p)); err == nil || !strings.Contains(out, "redis migrate") {
		t.Fatalf("bootstrap must refuse: %v\n%s", err, out)
	}

	p.Fresh, p.Reset = true, true
	if out, err := rootSh(t, buildBootstrapScript(p)); err != nil {
		t.Fatalf("bootstrap: %v\n%s", err, out)
	}
	raw, _ := os.ReadFile(result)
	vars := util.ParseEnvFile(string(raw))
	var port int
	fmt.Sscan(vars["PORT"], &port)
	if out, err := rootSh(t, buildCopyScript(DefaultLayout, name, 7, port, 0)); err != nil || !strings.Contains(out, "Copied 2 key(s)") {
		t.Fatalf("copy: %v\n%s", err, out)
	}
	app := urlArgs(t, vars["REDIS_URL"])
	if got := realCLI(append(app, "get", "session:1")...); got != "alice" {
		t.Fatalf("migrated key: %q", got)
	}
	if got := realCLI("-p", "6379", "-n", db, "get", "session:1"); got != "alice" {
		t.Fatalf("the shared Redis must keep its copy: %q", got)
	}
}
