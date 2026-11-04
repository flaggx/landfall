package redis

import (
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncodeRedisURL(t *testing.T) {
	got := EncodeRedisURL("my_user", "p@ss:word/x", "127.0.0.1", 6379, 0)
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "redis" {
		t.Fatalf("scheme: %s", u.Scheme)
	}
	if u.User.Username() != "my_user" {
		t.Fatalf("user: %s", u.User.Username())
	}
	pass, _ := u.User.Password()
	if pass != "p@ss:word/x" {
		t.Fatalf("password: %s", pass)
	}
	if u.Host != "127.0.0.1:6379" {
		t.Fatalf("host: %s", u.Host)
	}
	if u.Path != "/0" {
		t.Fatalf("path: %s", u.Path)
	}
}

func testParams() bootstrapParams {
	return bootstrapParams{
		Layout: DefaultLayout, EnvName: "prod", Instance: "my-webapp-prod", User: "app_prod",
		Maxmemory: "0", Policy: "noeviction", ResultPath: "/tmp/out.env", Reset: true,
	}
}

func TestBuildBootstrapScript(t *testing.T) {
	script := buildBootstrapScript(testParams())
	for _, needle := range []string{
		"REDIS_USER='app_prod'",
		"PORT_SETTING='0'",
		"REDIS_DB='0'",
		"RESET_PASSWORD='true'",
		"INSTANCE='my-webapp-prod'",
		"UNIT='redis-server@my-webapp-prod'",
		"CONF='/etc/redis/redis-my-webapp-prod.conf'",
		"ACL_FILE='/var/lib/redis/my-webapp-prod/users.acl'",
		"REDIS_ADMIN_FILE='/etc/landfall/redis-admin-my-webapp-prod'",
		"SHARED_ADMIN_FILE='/etc/landfall/redis-admin-password'",
		"systemctl cat redis-server@.service",
		"maxmemory $MAXMEMORY",
		"maxmemory-policy $POLICY",
		"ACL SETUSER",
		"ACL SAVE",
		"ACL USERS",
		"+@all -@admin",
		"RESULT='/tmp/out.env'",
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("script missing %q\n%s", needle, script)
		}
	}
	// ACL GETUSER exits 0 for a missing user, so it must not be the existence check.
	if strings.Contains(script, "rcli ACL GETUSER") {
		t.Fatalf("script must not use ACL GETUSER to test for a user")
	}
	// The body is fixed text; only the header carries values, all quoted.
	if strings.Contains(bootstrapBody, "%") && strings.Contains(bootstrapBody, "%%") {
		t.Fatal("the script body is not a format string; it must not contain %%")
	}
}

func TestBuildScriptsAreValidBash(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	for name, script := range map[string]string{
		"bootstrap":     buildBootstrapScript(testParams()),
		"status":        buildStatusScript(DefaultLayout, "prod", "my-webapp-prod", "app_prod", 0),
		"check":         "pass() { :; }; fail() { :; }; warn() { :; }\n" + CheckScript(DefaultLayout, "my-webapp-prod", "app_prod", "prod"),
		"migrate check": buildMigrateCheckScript(DefaultLayout, "prod", "my-webapp-prod", "app_prod", 0),
		"copy":          buildCopyScript(DefaultLayout, "my-webapp-prod", 1, 6380, 0),
	} {
		cmd := exec.Command(bash, "-n")
		cmd.Stdin = strings.NewReader(script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: bash -n failed: %v\n%s", name, err, out)
		}
	}
}

// A name with shell syntax can only ever reach the scripts as a quoted value.
func TestScriptsQuoteValues(t *testing.T) {
	p := testParams()
	p.EnvName = "dev$(touch /tmp/pwned)"
	script := buildBootstrapScript(p)
	if !strings.Contains(script, "ENV_NAME='dev$(touch /tmp/pwned)'") {
		t.Fatalf("ENV_NAME must be single-quoted:\n%s", script[:400])
	}
}

// Ubuntu 22.04 ships Redis 6.0, which rejects channel rules (&*) and the "-"
// bind prefix; 6.2+ needs the channel rule.
func TestVersionSnippet(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	const modernBind = "bind 127.0.0.1 -::1"
	const oldClient = "+client|setname +client|getname +client|id"
	for _, tc := range []struct {
		name, versionOutput, channels, bind, client string
	}{
		{"redis 6.0 (jammy)", "Redis server v=6.0.16 sha=00000000:0 malloc=jemalloc-5.2.1 bits=64 build=a3fdef44459b3ad6", "", "bind 127.0.0.1", oldClient},
		{"redis 6.2", "Redis server v=6.2.14 sha=00000000:0 malloc=jemalloc-5.3.0 bits=64 build=1", "&*", modernBind, oldClient},
		{"redis 7.0 (noble)", "Redis server v=7.0.15 sha=00000000:0 malloc=jemalloc-5.3.0 bits=64 build=1", "&*", modernBind, ""},
		{"redis 10", "Redis server v=10.0.1 sha=00000000:0 malloc=libc bits=64 build=1", "&*", modernBind, ""},
		{"unknown version", "", "&*", modernBind, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			stub := "#!/bin/bash\nexit 1\n"
			if tc.versionOutput != "" {
				stub = "#!/bin/bash\necho '" + tc.versionOutput + "'\n"
			}
			// Stub redis-cli too, so the fallback cannot find a real one on PATH.
			for _, name := range []string{"redis-server", "redis-cli"} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte(stub), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command(bash, "-c", "set -euo pipefail\n"+versionSnippet+`printf '%s|%s|%s' "$CHANNELS" "$BIND_LINE" "$CLIENT_RULES"`)
			// Only the stub and coreutils: no real redis-server or redis-cli.
			cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin"}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("snippet failed: %v\n%s", err, out)
			}
			if want := tc.channels + "|" + tc.bind + "|" + tc.client; string(out) != want {
				t.Fatalf("CHANNELS|BIND_LINE|CLIENT_RULES = %q, want %q", out, want)
			}
		})
	}
}

func TestBootstrapScriptChangesPasswordLast(t *testing.T) {
	script := buildBootstrapScript(testParams())
	order := []string{
		"on_shared_redis; then",       // refuses to drop data on the shared Redis
		"maxmemory $MAXMEMORY",        // writes the instance's config...
		`systemctl start "$UNIT"`,     // ...and starts it
		"ACL SETUSER default",         // locks the default user
		"rcli CONFIG GET aclfile",     // checks users can be saved...
		"on resetpass >%s %s",         // ...before the app's password changes
		"write_result false",          // the new URL is recorded straight away
		"rcli ACL SAVE | grep -qx OK", // only then can the save fail
		"write_result true",
	}
	last := -1
	for _, needle := range order {
		i := strings.Index(script, needle)
		if i < 0 {
			t.Fatalf("script missing %q", needle)
		}
		if i < last {
			t.Fatalf("%q is out of order", needle)
		}
		last = i
	}
	rest := strings.Replace(script, versionSnippet, "", 1)
	if strings.Contains(rest, "&*") || strings.Contains(rest, "-::1/") || strings.Contains(rest, "'bind 127.0.0.1 -::1'") {
		t.Fatal("channel rules must come from $CHANNELS, not be hard-coded (Redis 6.0 rejects them)")
	}
}
