package redis

import (
	"net/url"
	"os/exec"
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

func TestBuildBootstrapScript(t *testing.T) {
	script := buildBootstrapScript(DefaultPaths, "app_prod", 6379, 0, "/tmp/out.env", true)
	for _, needle := range []string{
		"REDIS_USER='app_prod'",
		"REDIS_PORT=6379",
		"REDIS_DB=0",
		"RESET_PASSWORD='true'",
		"ACL SETUSER",
		"ACL SAVE",
		"ACL USERS",
		"aclfile $ACL_FILE",
		"REDIS_ADMIN_FILE='/etc/landfall/redis-admin-password'",
		"+@all -@admin",
		`sudo test -f "$REDIS_CONF"`,
		"RESULT='/tmp/out.env'",
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("script missing %q\n%s", needle, script)
		}
	}
	// ACL GETUSER exits 0 for a missing user, so it must not be the existence check.
	if strings.Contains(script, `"$REDIS_PORT" ACL GETUSER`) {
		t.Fatalf("script must not use ACL GETUSER to test for a user\n%s", script)
	}
}

func TestBuildBootstrapScriptIsValidBash(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	script := buildBootstrapScript(DefaultPaths, "app_prod", 6379, 0, "/tmp/out.env", false)
	cmd := exec.Command(bash, "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bash -n failed: %v\n%s", err, out)
	}
}
