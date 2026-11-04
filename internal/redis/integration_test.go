package redis

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flaggx/landfall/internal/util"
)

// These tests run the real bootstrap, status, check and migration scripts
// against real Redis servers. Opt in with LANDFALL_REDIS_BIN_DIR=<dir
// containing redis-server and redis-cli>. sudo, systemctl, apt-get and chown
// are stubs, and every path lives in the test's temp directory, so nothing
// else on the machine is touched. The systemctl stub plays the package's
// redis-server@<name> template unit (and redis-server for the shared Redis).

type redisSandbox struct {
	t      *testing.T
	dir    string
	bin    string
	layout Layout
}

func newRedisSandbox(t *testing.T) *redisSandbox {
	t.Helper()
	binDir := os.Getenv("LANDFALL_REDIS_BIN_DIR")
	if binDir == "" {
		t.Skip("set LANDFALL_REDIS_BIN_DIR to run against a real Redis")
	}
	dir := t.TempDir()
	s := &redisSandbox{t: t, dir: dir, bin: filepath.Join(dir, "bin")}
	s.layout = Layout{
		EtcDir:     filepath.Join(dir, "etc"),
		LibDir:     filepath.Join(dir, "lib"),
		LogDir:     filepath.Join(dir, "log"),
		RunDir:     filepath.Join(dir, "run"),
		AdminDir:   filepath.Join(dir, "landfall"),
		SharedPort: freePort(t),
	}
	for _, d := range []string{s.bin, s.layout.EtcDir, s.layout.LibDir, s.layout.LogDir, s.layout.RunDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"redis-server", "redis-cli"} {
		if err := os.Symlink(filepath.Join(binDir, name), filepath.Join(s.bin, name)); err != nil {
			t.Fatal(err)
		}
	}

	s.write(filepath.Join(s.bin, "sudo"), `#!/bin/bash
while [ $# -gt 0 ]; do
  case "$1" in
    -u) shift 2 ;;
    -n|-E) shift ;;
    *) break ;;
  esac
done
exec "$@"
`)
	s.write(filepath.Join(s.bin, "apt-get"), "#!/bin/bash\nexit 0\n")
	s.write(filepath.Join(s.bin, "chown"), "#!/bin/bash\nexit 0\n")
	s.write(filepath.Join(s.bin, "systemctl"), fmt.Sprintf(`#!/bin/bash
ETC=%s
CMD=$1; shift
[ "${1:-}" = --quiet ] && shift
UNIT=${1:-}
case "$CMD" in
  cat) [ "$UNIT" = redis-server@.service ]; exit ;;
  enable|disable|daemon-reload) exit 0 ;;
esac
case "$UNIT" in
  redis-server@*) CONF="$ETC/redis-${UNIT#redis-server@}.conf" ;;
  redis-server) CONF="$ETC/redis.conf" ;;
  *) exit 1 ;;
esac
PIDFILE=$(awk '$1 == "pidfile" { print $2 }' "$CONF" 2>/dev/null)
running() { [ -n "$PIDFILE" ] && [ -f "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; }
stop() {
  if running; then
    PID=$(cat "$PIDFILE"); kill "$PID"
    while kill -0 "$PID" 2>/dev/null; do sleep 0.05; done
  fi
}
start() { mkdir -p "$(dirname "$PIDFILE")"; redis-server "$CONF" --daemonize yes --supervised no; }
case "$CMD" in
  start) running || start ;;
  restart) stop; start ;;
  stop) stop ;;
  is-active) running && echo active ;;
  *) exit 1 ;;
esac
`, util.ShellQuote(s.layout.EtcDir)))

	t.Cleanup(func() {
		confs, _ := filepath.Glob(filepath.Join(s.layout.EtcDir, "redis*.conf"))
		for _, c := range confs {
			unit := "redis-server" // redis.conf: the shared Redis
			if base := filepath.Base(c); strings.HasPrefix(base, "redis-") {
				unit = "redis-server@" + strings.TrimSuffix(strings.TrimPrefix(base, "redis-"), ".conf")
			}
			_, _ = s.sh("systemctl stop " + unit)
		}
	})
	return s
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func (s *redisSandbox) write(path, content string) {
	s.t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
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

func (s *redisSandbox) params(env, user string, port int) bootstrapParams {
	return bootstrapParams{
		Layout: s.layout, EnvName: env, Instance: "app-" + env, User: user, Port: port,
		Maxmemory: "64mb", Policy: "noeviction", ResultPath: filepath.Join(s.dir, "result-"+env+".env"),
	}
}

// bootstrapRaw runs the bootstrap script and returns the result file's
// values (if any), the script output and its error.
func (s *redisSandbox) bootstrapRaw(p bootstrapParams) (map[string]string, string, error) {
	s.t.Helper()
	out, err := s.sh(buildBootstrapScript(p))
	raw, readErr := os.ReadFile(p.ResultPath)
	if readErr != nil {
		return nil, out, err
	}
	return util.ParseEnvFile(string(raw)), out, err
}

func (s *redisSandbox) bootstrap(p bootstrapParams) map[string]string {
	s.t.Helper()
	vars, out, err := s.bootstrapRaw(p)
	if err != nil {
		s.t.Fatalf("bootstrap %s failed: %v\n%s", p.Instance, err, out)
	}
	return vars
}

// cli runs redis-cli against port. "-u <redis URL>" is turned into
// --user/--pass/-p/-n, because redis-cli 6.0 ignores the username in a URL
// and would try the password against the default user.
func (s *redisSandbox) cli(port int, args ...string) string {
	s.t.Helper()
	full := []string{"--no-auth-warning", "-p", fmt.Sprint(port)}
	for i := 0; i < len(args); i++ {
		if args[i] == "-u" && i+1 < len(args) {
			u, err := url.Parse(args[i+1])
			if err != nil {
				s.t.Fatalf("bad redis URL %q: %v", args[i+1], err)
			}
			pass, _ := u.User.Password()
			full = append(full, "--user", u.User.Username(), "--pass", pass, "-p", u.Port())
			if db := strings.TrimPrefix(u.Path, "/"); db != "" {
				full = append(full, "-n", db)
			}
			i++
			continue
		}
		full = append(full, args[i])
	}
	out, _ := exec.Command(filepath.Join(s.bin, "redis-cli"), full...).CombinedOutput()
	return strings.TrimSpace(string(out))
}

// admin runs redis-cli as the instance's admin user.
func (s *redisSandbox) admin(instance string, port int, args ...string) string {
	s.t.Helper()
	pass, err := os.ReadFile(s.layout.Instance(instance).AdminFile)
	if err != nil {
		s.t.Fatal(err)
	}
	return s.cli(port, append([]string{"-a", string(pass)}, args...)...)
}

func (s *redisSandbox) pid(instance string) string {
	raw, _ := os.ReadFile(s.layout.Instance(instance).PidFile)
	return strings.TrimSpace(string(raw))
}

func (s *redisSandbox) version() (major, minor int) {
	s.t.Helper()
	out, err := exec.Command(filepath.Join(s.bin, "redis-server"), "--version").Output()
	if err != nil {
		s.t.Fatal(err)
	}
	i := strings.Index(string(out), "v=")
	if i < 0 {
		s.t.Fatalf("cannot find the version in %q", out)
	}
	if _, err := fmt.Sscanf(string(out[i+2:]), "%d.%d", &major, &minor); err != nil {
		s.t.Fatalf("cannot parse %q: %v", out, err)
	}
	return major, minor
}

// appRules are the ACL rules an older Landfall gave an app user on the
// shared Redis: channel rules (&*) only exist from Redis 6.2.
func (s *redisSandbox) appRules() []string {
	if major, minor := s.version(); major > 6 || (major == 6 && minor >= 2) {
		return []string{"~*", "&*", "+@all"}
	}
	return []string{"~*", "+@all"}
}

// startShared starts a stand-in for the old host-wide Redis, with an open
// default user as older Landfall versions left it.
func (s *redisSandbox) startShared() {
	s.t.Helper()
	conf := fmt.Sprintf("port %d\nbind 127.0.0.1\ndir %s\npidfile %s\nlogfile %s\nsave \"\"\n",
		s.layout.SharedPort, s.dir, filepath.Join(s.layout.RunDir, "shared.pid"), filepath.Join(s.layout.LogDir, "shared.log"))
	s.write(filepath.Join(s.layout.EtcDir, "redis.conf"), conf)
	if out, err := s.sh("systemctl start redis-server"); err != nil {
		s.t.Fatalf("start shared: %v\n%s", err, out)
	}
	waitFor(s.t, func() bool { return s.cli(s.layout.SharedPort, "ping") == "PONG" })
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if ok() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timed out waiting")
}

func TestInstanceBootstrapAgainstRealRedis(t *testing.T) {
	s := newRedisSandbox(t)
	port := freePort(t)
	p := s.params("prod", "app_prod", port)

	first := s.bootstrap(p)
	url := first["REDIS_URL"]
	if first["CREATED"] != "true" || first["DEFAULT_LOCKED"] != "true" || first["PORT"] != fmt.Sprint(port) || !strings.HasPrefix(url, "redis://app_prod:") {
		t.Fatalf("first run: %v", first)
	}

	// The instance's config is Landfall's and names its own files.
	inst := s.layout.Instance("app-prod")
	conf, err := os.ReadFile(inst.Conf)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{fmt.Sprintf("port %d", port), "aclfile " + inst.ACLFile, "dir " + inst.DataDir, "maxmemory 64mb", "maxmemory-policy noeviction"} {
		if !strings.Contains(string(conf), want) {
			t.Fatalf("config missing %q:\n%s", want, conf)
		}
	}

	if got := s.cli(port, "ping"); !strings.Contains(got, "NOAUTH") {
		t.Fatalf("the default user must need a password, got %q", got)
	}
	if got := s.cli(port, "-u", url, "set", "k", "v"); got != "OK" {
		t.Fatalf("app user cannot write: %q", got)
	}
	// What client libraries send while connecting must still be allowed.
	if got := s.cli(port, "-u", url, "CLIENT", "SETNAME", "landfall-test"); got != "OK" {
		t.Fatalf("app user cannot run CLIENT SETNAME: %q", got)
	}
	for _, adminCmd := range [][]string{{"ACL", "LIST"}, {"CONFIG", "GET", "maxmemory"}, {"CLIENT", "KILL", "ID", "999999"}} {
		if got := s.cli(port, append([]string{"-u", url}, adminCmd...)...); !strings.Contains(got, "NOPERM") {
			t.Fatalf("app user must not run %v, got %q", adminCmd, got)
		}
	}
	if got := s.admin("app-prod", port, "CONFIG", "GET", "maxmemory"); !strings.Contains(got, fmt.Sprint(64*1024*1024)) {
		t.Fatalf("maxmemory not applied: %q", got)
	}

	// Users and data survive a restart.
	if out, err := s.sh("systemctl restart " + inst.Unit); err != nil {
		t.Fatalf("restart: %v\n%s", err, out)
	}
	waitFor(t, func() bool { return s.cli(port, "-u", url, "ping") == "PONG" })
	if got := s.cli(port, "-u", url, "get", "k"); got != "v" {
		t.Fatalf("data lost on restart: %q", got)
	}

	// A second run changes nothing and doesn't restart the instance.
	pid := s.pid("app-prod")
	second := s.bootstrap(p)
	if second["CREATED"] != "false" || second["PASSWORD_ROTATED"] != "false" || second["REDIS_URL"] != "" {
		t.Fatalf("second run should be a no-op: %v", second)
	}
	if s.pid("app-prod") != pid {
		t.Fatal("a no-op run restarted the instance")
	}

	// A memory change rewrites the config and restarts it; data is kept.
	p.Maxmemory = "32mb"
	s.bootstrap(p)
	if got := s.admin("app-prod", port, "CONFIG", "GET", "maxmemory"); !strings.Contains(got, fmt.Sprint(32*1024*1024)) {
		t.Fatalf("new maxmemory not applied: %q", got)
	}
	if got := s.cli(port, "-u", url, "get", "k"); got != "v" {
		t.Fatalf("data lost on a config change: %q", got)
	}

	// --reset-password rotates.
	p.Reset = true
	third := s.bootstrap(p)
	if third["PASSWORD_ROTATED"] != "true" || third["REDIS_URL"] == url {
		t.Fatalf("reset: %v", third)
	}
	if got := s.cli(port, "-u", third["REDIS_URL"], "ping"); got != "PONG" {
		t.Fatalf("new password: %q", got)
	}
	if got := s.cli(port, "-u", url, "ping"); got == "PONG" {
		t.Fatal("the old password must stop working")
	}

	// A different redis.port than the instance's is refused, not applied.
	p.Reset = false
	p.Port = freePort(t)
	if _, out, err := s.bootstrapRaw(p); err == nil || !strings.Contains(out, fmt.Sprintf("listens on port %d", port)) {
		t.Fatalf("expected a port mismatch error, got err=%v\n%s", err, out)
	}
}

// The point of per-environment instances: environments can't reach each
// other's data, flush it, or use up its memory.
func TestEnvironmentsAreIsolated(t *testing.T) {
	s := newRedisSandbox(t)
	prodPort, devPort := freePort(t), freePort(t)
	prod := s.bootstrap(s.params("prod", "app_prod", prodPort))["REDIS_URL"]
	devParams := s.params("dev", "app_dev", devPort)
	devParams.Maxmemory = "2mb"
	dev := s.bootstrap(devParams)["REDIS_URL"]

	if got := s.cli(prodPort, "-u", prod, "set", "order:1", "paid"); got != "OK" {
		t.Fatalf("prod write: %q", got)
	}
	// Dev's credentials don't work on prod's instance at all.
	devOnProd := strings.Replace(dev, fmt.Sprintf(":%d/", devPort), fmt.Sprintf(":%d/", prodPort), 1)
	if got := s.cli(prodPort, "-u", devOnProd, "get", "order:1"); got == "paid" {
		t.Fatal("dev's user could read prod's data")
	}
	// Dev can flush its own instance, nothing else.
	if got := s.cli(devPort, "-u", dev, "FLUSHALL"); got != "OK" {
		t.Fatalf("dev FLUSHALL: %q", got)
	}
	if got := s.cli(prodPort, "-u", prod, "get", "order:1"); got != "paid" {
		t.Fatalf("prod's data must survive dev's FLUSHALL, got %q", got)
	}
	// Dev filling its memory fails dev's writes (noeviction), not prod's.
	// 100 KB: Linux caps a single command-line argument at 128 KB.
	big := strings.Repeat("x", 100*1024)
	var full bool
	for i := 0; i < 100 && !full; i++ {
		full = strings.Contains(s.cli(devPort, "-u", dev, "set", fmt.Sprintf("big:%d", i), big), "OOM")
	}
	if !full {
		t.Fatal("dev's 2mb limit was never reached")
	}
	if got := s.cli(prodPort, "-u", prod, "set", "order:2", "paid"); got != "OK" {
		t.Fatalf("prod must still accept writes when dev is full, got %q", got)
	}
}

func TestBootstrapPicksAndKeepsPort(t *testing.T) {
	s := newRedisSandbox(t)
	// A range whose first port is taken by an unrelated program.
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	min := busy.Addr().(*net.TCPAddr).Port

	p := s.params("dev", "app_dev", 0)
	p.PortMin, p.PortMax = min, min+50
	first := s.bootstrap(p)
	picked := first["PORT"]
	if picked == "" || picked == fmt.Sprint(min) {
		t.Fatalf("must skip the busy port %d, picked %q", min, picked)
	}
	// Kept on the next run, even though it is now "in use" (by itself).
	if again := s.bootstrap(p); again["PORT"] != picked {
		t.Fatalf("port changed from %s to %s", picked, again["PORT"])
	}
	// A second instance gets a different port.
	q := s.params("staging", "app_staging", 0)
	q.PortMin, q.PortMax = min, min+50
	if other := s.bootstrap(q)["PORT"]; other == picked || other == fmt.Sprint(min) {
		t.Fatalf("second instance picked %s (first has %s, %d is busy)", other, picked, min)
	}
}

func TestBootstrapRefusesEnvironmentOnSharedRedis(t *testing.T) {
	s := newRedisSandbox(t)
	s.startShared()
	if got := s.cli(s.layout.SharedPort, append([]string{"ACL", "SETUSER", "app_dev", "on", ">old"}, s.appRules()...)...); got != "OK" {
		t.Fatalf("setup: %q", got)
	}

	p := s.params("dev", "app_dev", freePort(t))
	_, out, err := s.bootstrapRaw(p)
	if err == nil || !strings.Contains(out, "redis migrate --env dev") {
		t.Fatalf("bootstrap must point to migrate, got err=%v\n%s", err, out)
	}
	if _, err := os.Stat(s.layout.Instance("app-dev").Conf); !os.IsNotExist(err) {
		t.Fatal("no instance may be created when the environment has data on the shared Redis")
	}

	// The check reports the environment as not isolated.
	result := filepath.Join(s.dir, "check")
	check := "RESULT=" + util.ShellQuote(result) + "\n" +
		`pass() { echo "$1=PASS|$2" >> "$RESULT"; }; fail() { echo "$1=FAIL|$2" >> "$RESULT"; }; warn() { echo "$1=WARN|$2" >> "$RESULT"; }` + "\n" +
		CheckScript(s.layout, "app-dev", "app_dev", "dev")
	if out, err := s.sh(check); err != nil {
		t.Fatalf("check: %v\n%s", err, out)
	}
	if raw, _ := os.ReadFile(result); !strings.Contains(string(raw), "redis=WARN|on the shared Redis") {
		t.Fatalf("check should warn about the shared Redis:\n%s", raw)
	}

	// --fresh creates it anyway, empty.
	p.Fresh = true
	if vars := s.bootstrap(p); vars["REDIS_URL"] == "" {
		t.Fatalf("--fresh: %v", vars)
	}
}

// Migration: the environment's DB on the shared Redis is copied into its
// own instance (binary-safe, TTLs kept, odd key names), other environments'
// DBs are not, and the source is left as it was.
func TestMigrateCopiesOnlyItsDatabase(t *testing.T) {
	s := newRedisSandbox(t)
	s.startShared()
	shared := s.layout.SharedPort
	if got := s.cli(shared, append([]string{"ACL", "SETUSER", "app_dev", "on", ">old"}, s.appRules()...)...); got != "OK" {
		t.Fatalf("setup: %q", got)
	}
	for _, cmd := range [][]string{
		{"-n", "0", "set", "prod:secret", "prod-only"}, // prod's DB: must not be copied
		{"-n", "1", "set", "session:1", "alice"},
		{"-n", "1", "hset", "user:1", "name", "Bob", "role", "admin"},
		{"-n", "1", "rpush", "queue", "a", "b", "c"},
		{"-n", "1", "set", "ttl:key", "v", "EX", "600"},
		{"-n", "1", "set", `odd "key" with\back slash`, "1"},
	} {
		if got := s.cli(shared, cmd...); got != "OK" && got != "2" && got != "3" {
			t.Fatalf("setup %v: %q", cmd, got)
		}
	}

	if out, err := s.sh(buildMigrateCheckScript(s.layout, "dev", "app-dev", "app_dev", 1)); err != nil || !strings.Contains(out, "Keys to copy from DB 1: 5") {
		t.Fatalf("migrate check: %v\n%s", err, out)
	}

	p := s.params("dev", "app_dev", freePort(t))
	p.Fresh, p.Reset = true, true
	vars := s.bootstrap(p)
	port := p.Port
	url := vars["REDIS_URL"]

	out, err := s.sh(buildCopyScript(s.layout, "app-dev", 1, port, 0))
	if err != nil || !strings.Contains(out, "Copied 5 key(s)") {
		t.Fatalf("copy: %v\n%s", err, out)
	}
	for args, want := range map[string]string{
		"get session:1":      "alice",
		"hget user:1 role":   "admin",
		"lrange queue 0 -1":  "a\nb\nc",
		"exists prod:secret": "0",
	} {
		if got := s.cli(port, append([]string{"-u", url}, strings.Fields(args)...)...); got != want {
			t.Errorf("%s: got %q, want %q", args, got, want)
		}
	}
	if got := s.cli(port, "-u", url, "get", `odd "key" with\back slash`); got != "1" {
		t.Errorf("odd key name: got %q", got)
	}
	if ttl := s.cli(port, "-u", url, "ttl", "ttl:key"); ttl == "-1" || ttl == "-2" {
		t.Errorf("TTL must be kept, got %s", ttl)
	}
	// The source keeps everything, for a rollback.
	if got := s.cli(shared, "-n", "1", "dbsize"); got != "5" {
		t.Errorf("the shared DB must be left as it was, has %s keys", got)
	}
	// Copying again (a re-run after a failure) replaces, doesn't fail.
	if out, err := s.sh(buildCopyScript(s.layout, "app-dev", 1, port, 0)); err != nil {
		t.Fatalf("second copy: %v\n%s", err, out)
	}
}

func TestMigrateCheckWithNothingToMigrate(t *testing.T) {
	s := newRedisSandbox(t)
	s.startShared()
	out, err := s.sh(buildMigrateCheckScript(s.layout, "dev", "app-dev", "app_dev", 1))
	if err == nil || !strings.Contains(out, "nothing to migrate") {
		t.Fatalf("expected nothing to migrate, got err=%v\n%s", err, out)
	}
}
