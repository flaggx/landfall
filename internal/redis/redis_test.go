package redis

import (
	"net/url"
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
	script := buildBootstrapScript("app_prod", 6379, 0, "/tmp/out.env", true)
	for _, needle := range []string{
		"REDIS_USER='app_prod'",
		"REDIS_PORT=6379",
		"REDIS_DB=0",
		"RESET_PASSWORD='true'",
		"ACL SETUSER",
		"ACL SAVE",
		"RESULT='/tmp/out.env'",
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("script missing %q\n%s", needle, script)
		}
	}
}
