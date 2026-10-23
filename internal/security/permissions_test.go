package security

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flaggx/landfall/internal/util"
)

func TestBuildPermissionsScript(t *testing.T) {
	script := BuildPermissionsScript("/var/www/app-prod", "deploy", "app-prod")
	for _, want := range []string{
		"chmod 750",
		".env.production",
		"chmod 600",
		"chmod 700",
		"g+rX,g-w",
		util.ShellQuote("app-prod"),
		util.ShellQuote("/var/www/app-prod/.next/standalone/.next/cache"),
		`if [ -f "$HOME/.ssh/id_ed25519" ]; then`,
		`if [ -f "$HOME/.ssh/id_ed25519.pub" ]; then`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("expected script to contain %q", want)
		}
	}
	if strings.Contains(script, `[ -f "$HOME/.ssh/id_ed25519" ] &&`) {
		t.Fatal("should not use && after test that can fail under set -e exit status")
	}
}

func TestRuntimeCacheDir(t *testing.T) {
	if got := RuntimeCacheDir("/var/www/app/"); got != "/var/www/app/.next/standalone/.next/cache" {
		t.Fatal(got)
	}
}

// Runs the script for real against a temp deploy path, with a fake sudo that
// runs commands as the current user, who plays both the deploy and app user.
func TestPermissionsScriptRuns(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	me, err := user.Current()
	if err != nil {
		t.Skip(err)
	}

	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "sudo"), []byte("#!/bin/sh\nexec \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		appUser string
	}{
		{"with app user", me.Username},
		{"app user not created yet", "landfall-test-no-such-user"},
		{"no app user", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			deployPath := filepath.Join(root, "app")
			code := filepath.Join(deployPath, ".next", "standalone", "server.js")
			envFile := filepath.Join(deployPath, ".env.production")
			mustWrite(t, code, 0o664) // umask 002: group-writable, as on Ubuntu
			mustWrite(t, envFile, 0o664)

			cmd := exec.Command(bash, "-c", BuildPermissionsScript(deployPath, me.Username, tc.appUser))
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "HOME="+root)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("script failed: %v\n%s", err, out)
			}

			assertMode(t, deployPath, 0o750)
			assertMode(t, envFile, 0o600)
			cache := RuntimeCacheDir(deployPath)
			if tc.appUser == me.Username {
				assertMode(t, code, 0o644) // group can read, not write
				assertMode(t, cache, 0o750)
			} else if _, err := os.Stat(cache); !os.IsNotExist(err) {
				t.Fatal("cache dir must only be created for an existing app user")
			}
		})
	}
}

func mustWrite(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s: mode %o, want %o", path, got, want)
	}
}

func TestBuildPermissionsCheckScript(t *testing.T) {
	script := BuildPermissionsCheckScript("/var/www/app-prod", "deploy", true)
	if !strings.Contains(script, "permissions_env_file") {
		t.Fatal("expected env file permission check")
	}
	if !strings.Contains(script, util.ShellQuote("true")) {
		t.Fatal("expected expect env flag")
	}
}
