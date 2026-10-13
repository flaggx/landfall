package redis

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flaggx/landfall/internal/util"
)

// These tests run the real bootstrap and status scripts against a throwaway
// Redis server. Opt in with LANDFALL_REDIS_BIN_DIR=<dir containing
// redis-server and redis-cli>. sudo, systemctl, apt-get and chown are
// replaced by stubs so nothing outside the test's temp directory is touched.

type redisSandbox struct {
	t     *testing.T
	dir   string
	bin   string
	port  int
	paths Paths
}

func newRedisSandbox(t *testing.T) *redisSandbox {
	t.Helper()
	binDir := os.Getenv("LANDFALL_REDIS_BIN_DIR")
	if binDir == "" {
		t.Skip("set LANDFALL_REDIS_BIN_DIR to run against a real Redis")
	}

	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"redis-server", "redis-cli"} {
		if err := os.Symlink(filepath.Join(binDir, name), filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	s := &redisSandbox{
		t:    t,
		dir:  dir,
		bin:  bin,
		port: port,
		paths: Paths{
			Conf:      filepath.Join(dir, "redis.conf"),
			ACLFile:   filepath.Join(dir, "users.acl"),
			AdminFile: filepath.Join(dir, "landfall", "redis-admin-password"),
		},
	}

	conf := fmt.Sprintf("port %d\nbind 127.0.0.1 -::1\ndir %s\ndaemonize yes\npidfile %s\nlogfile %s\nsave \"\"\n",
		port, dir, s.pidFile(), filepath.Join(dir, "redis.log"))
	s.write(s.paths.Conf, conf, 0o644)

	s.write(filepath.Join(bin, "sudo"), `#!/bin/bash
while [ $# -gt 0 ]; do
  case "$1" in
    -u) shift 2 ;;
    -n|-E) shift ;;
    *) break ;;
  esac
done
exec "$@"
`, 0o755)
	s.write(filepath.Join(bin, "apt-get"), "#!/bin/bash\nexit 0\n", 0o755)
	s.write(filepath.Join(bin, "chown"), "#!/bin/bash\nexit 0\n", 0o755)
	s.write(filepath.Join(bin, "systemctl"), fmt.Sprintf(`#!/bin/bash
PIDFILE=%s
CONF=%s
running() { [ -f "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; }
stop() {
  if running; then
    PID=$(cat "$PIDFILE")
    kill "$PID"
    while kill -0 "$PID" 2>/dev/null; do sleep 0.1; done
  fi
}
[ "${2:-}" = "redis-server" ] || exit 1
case "$1" in
  restart) stop; redis-server "$CONF" ;;
  start) running || redis-server "$CONF" ;;
  stop) stop ;;
  is-active) running && echo active ;;
  *) exit 0 ;;
esac
`, util.ShellQuote(s.pidFile()), util.ShellQuote(s.paths.Conf)), 0o755)

	t.Cleanup(func() { _, _ = s.sh("systemctl stop redis-server") })
	return s
}

func (s *redisSandbox) pidFile() string { return filepath.Join(s.dir, "redis.pid") }

func (s *redisSandbox) write(path, content string, mode os.FileMode) {
	s.t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		s.t.Fatal(err)
	}
}

// sh runs a script with the stubs first on PATH.
func (s *redisSandbox) sh(script string) (string, error) {
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+s.bin+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// bootstrap runs the bootstrap script and returns the result file's values.
func (s *redisSandbox) bootstrap(reset bool) map[string]string {
	s.t.Helper()
	result := filepath.Join(s.dir, "result.env")
	_ = os.Remove(result)
	out, err := s.sh(buildBootstrapScript(s.paths, "app_prod", s.port, 0, result, reset))
	if err != nil {
		s.t.Fatalf("bootstrap failed: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(result)
	if err != nil {
		s.t.Fatal(err)
	}
	return util.ParseEnvFile(string(raw))
}

// cli runs redis-cli with the given connection arguments and returns its output.
func (s *redisSandbox) cli(args ...string) string {
	s.t.Helper()
	full := append([]string{"--no-auth-warning", "-p", fmt.Sprint(s.port)}, args...)
	cmd := exec.Command(filepath.Join(s.bin, "redis-cli"), full...)
	out, _ := cmd.CombinedOutput()
	return strings.TrimSpace(string(out))
}

func (s *redisSandbox) pid() string {
	raw, _ := os.ReadFile(s.pidFile())
	return strings.TrimSpace(string(raw))
}

func (s *redisSandbox) confCount(prefix string) int {
	raw, _ := os.ReadFile(s.paths.Conf)
	count := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, prefix) {
			count++
		}
	}
	return count
}

func TestBootstrapAgainstRealRedis(t *testing.T) {
	s := newRedisSandbox(t)

	// --- first run on a fresh Redis ---
	first := s.bootstrap(false)
	if first["CREATED"] != "true" || first["DEFAULT_LOCKED"] != "true" {
		t.Fatalf("first run: unexpected result %v", first)
	}
	url := first["REDIS_URL"]
	if !strings.HasPrefix(url, "redis://app_prod:") {
		t.Fatalf("first run: missing REDIS_URL in %v", first)
	}

	if got := s.cli("ping"); !strings.Contains(got, "NOAUTH") {
		t.Fatalf("default user must need a password now, got %q", got)
	}
	if got := s.cli("-u", url, "ping"); got != "PONG" {
		t.Fatalf("app user cannot log in: %q", got)
	}
	if got := s.cli("-u", url, "set", "k", "v"); got != "OK" {
		t.Fatalf("app user cannot write: %q", got)
	}
	// What client libraries send while connecting must still be allowed.
	if got := s.cli("-u", url, "CLIENT", "SETINFO", "lib-name", "landfall-test"); got != "OK" {
		t.Fatalf("app user cannot run CLIENT SETINFO: %q", got)
	}
	for _, admin := range [][]string{{"ACL", "LIST"}, {"CONFIG", "GET", "aclfile"}, {"ACL", "SETUSER", "default", "nopass"}} {
		if got := s.cli(append([]string{"-u", url}, admin...)...); !strings.Contains(got, "NOPERM") {
			t.Fatalf("app user must not be allowed to run %v, got %q", admin, got)
		}
	}

	info, err := os.Stat(s.paths.AdminFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("admin password file mode is %v, want 0600", info.Mode().Perm())
	}
	if s.confCount("aclfile ") != 1 {
		t.Fatalf("expected exactly one aclfile line in redis.conf")
	}
	if s.confCount("bind 127.0.0.1 -::1") != 1 {
		t.Fatalf("a loopback-only bind line must be left as it was")
	}

	// --- users survive a Redis restart ---
	if out, err := s.sh("systemctl restart redis-server"); err != nil {
		t.Fatalf("restart: %v\n%s", err, out)
	}
	waitForRedis(t, s)
	if got := s.cli("-u", url, "ping"); got != "PONG" {
		t.Fatalf("app user lost after restart: %q", got)
	}
	if got := s.cli("ping"); !strings.Contains(got, "NOAUTH") {
		t.Fatalf("default user lost its password after restart: %q", got)
	}

	// --- second run changes nothing and does not restart Redis ---
	pidBefore := s.pid()
	second := s.bootstrap(false)
	if second["CREATED"] != "false" || second["PASSWORD_ROTATED"] != "false" || second["DEFAULT_LOCKED"] != "false" || second["REDIS_URL"] != "" {
		t.Fatalf("second run should be a no-op, got %v", second)
	}
	if s.pid() != pidBefore {
		t.Fatal("second run restarted Redis")
	}
	if s.confCount("aclfile ") != 1 {
		t.Fatal("second run added another aclfile line")
	}
	if got := s.cli("-u", url, "ping"); got != "PONG" {
		t.Fatalf("existing password stopped working after a no-op run: %q", got)
	}

	// --- reset rotates the app password ---
	third := s.bootstrap(true)
	newURL := third["REDIS_URL"]
	if third["PASSWORD_ROTATED"] != "true" || newURL == "" || newURL == url {
		t.Fatalf("reset should produce a new URL, got %v", third)
	}
	if got := s.cli("-u", newURL, "ping"); got != "PONG" {
		t.Fatalf("new password does not work: %q", got)
	}
	if got := s.cli("-u", url, "ping"); !strings.Contains(got, "WRONGPASS") {
		t.Fatalf("old password must stop working, got %q", got)
	}

	// --- status reports the hardened state ---
	status, err := s.sh(buildStatusScript(s.paths, "app_prod", s.port, 0))
	if err != nil {
		t.Fatalf("status failed: %v\n%s", err, status)
	}
	for _, want := range []string{
		"Ping (as admin):\nPONG",
		"Default user password-protected:\n  yes",
		"ACL user exists:\n  yes",
		"ACL users persisted (aclfile):\n  yes",
	} {
		if !strings.Contains(status, want) {
			t.Fatalf("status missing %q:\n%s", want, status)
		}
	}

	// --- a locked default user without the admin file is reported, not guessed ---
	if err := os.Remove(s.paths.AdminFile); err != nil {
		t.Fatal(err)
	}
	out, err := s.sh(buildBootstrapScript(s.paths, "app_prod", s.port, 0, filepath.Join(s.dir, "result.env"), false))
	if err == nil || !strings.Contains(out, "does not know") {
		t.Fatalf("expected a clear failure when the admin password is unknown, got err=%v\n%s", err, out)
	}
}

// An existing server whose app user was created by an older Landfall (open
// default user, no aclfile, user only in memory) must be upgraded in place
// without changing the app's password.
func TestBootstrapUpgradesExistingRedis(t *testing.T) {
	s := newRedisSandbox(t)
	if out, err := s.sh("systemctl start redis-server"); err != nil {
		t.Fatalf("start: %v\n%s", err, out)
	}
	waitForRedis(t, s)

	// What a pre-fix bootstrap left behind once the aclfile is configured:
	// simulate by adding the aclfile first, then the user with full rights.
	if err := os.WriteFile(s.paths.ACLFile, nil, 0o640); err != nil {
		t.Fatal(err)
	}
	conf, _ := os.ReadFile(s.paths.Conf)
	s.write(s.paths.Conf, string(conf)+"aclfile "+s.paths.ACLFile+"\n", 0o644)
	if out, err := s.sh("systemctl restart redis-server"); err != nil {
		t.Fatalf("restart: %v\n%s", err, out)
	}
	waitForRedis(t, s)
	if got := s.cli("ACL", "SETUSER", "app_prod", "on", ">oldpassword", "~*", "&*", "+@all"); got != "OK" {
		t.Fatalf("setup: %q", got)
	}
	oldURL := fmt.Sprintf("redis://app_prod:oldpassword@127.0.0.1:%d/0", s.port)

	pidBefore := s.pid()
	result := s.bootstrap(false)
	if result["CREATED"] != "false" || result["PASSWORD_ROTATED"] != "false" || result["DEFAULT_LOCKED"] != "true" {
		t.Fatalf("unexpected result %v", result)
	}
	if s.pid() != pidBefore {
		t.Fatal("upgrade restarted Redis although the config was already correct")
	}
	if got := s.cli("-u", oldURL, "ping"); got != "PONG" {
		t.Fatalf("the app's existing password must keep working: %q", got)
	}
	if got := s.cli("-u", oldURL, "ACL", "LIST"); !strings.Contains(got, "NOPERM") {
		t.Fatalf("the app user should have lost admin commands, got %q", got)
	}
	if got := s.cli("ping"); !strings.Contains(got, "NOAUTH") {
		t.Fatalf("default user must need a password now, got %q", got)
	}
}

func waitForRedis(t *testing.T, s *redisSandbox) {
	t.Helper()
	out, err := s.sh(fmt.Sprintf(`for _ in $(seq 1 40); do redis-cli -p %d ping 2>&1 | grep -qE 'PONG|NOAUTH' && exit 0; sleep 0.25; done; exit 1`, s.port))
	if err != nil {
		t.Fatalf("redis did not come up: %s", out)
	}
}
