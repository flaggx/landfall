package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flaggx/landfall/internal/config"
)

// newOrigin creates a bare repo with two commits on main and a tag, and a
// clone of it whose origin is that repo. It returns the clone and both
// commits' SHAs.
func newOrigin(t *testing.T) (clone, first, second string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	origin := filepath.Join(root, "origin.git")
	work := filepath.Join(root, "work")
	git(root, "init", "-q", "--bare", "-b", "main", origin)
	git(root, "init", "-q", "-b", "main", work)
	git(work, "commit", "-q", "--allow-empty", "-m", "one")
	first = git(work, "rev-parse", "HEAD")
	git(work, "commit", "-q", "--allow-empty", "-m", "two")
	second = git(work, "rev-parse", "HEAD")
	git(work, "tag", "v1.2.0+build.5")
	git(work, "push", "-q", origin, "main", "--tags")
	clone = filepath.Join(root, "clone")
	git(root, "clone", "-q", origin, clone)
	return clone, first, second
}

func TestFetchRefSnippet(t *testing.T) {
	clone, first, second := newOrigin(t)
	for _, tc := range []struct {
		ref, want string
		wantErr   bool
	}{
		{"main", second, false},
		{"v1.2.0+build.5", second, false},
		{first, first, false},     // full SHA
		{first[:7], first, false}, // short SHA: fetched by name fails, resolved locally
		{"deadbee", "", true},     // not on origin
		{"no-such-branch", "", true},
	} {
		t.Run(tc.ref, func(t *testing.T) {
			cmd := exec.Command("bash", "-c", "set -euo pipefail\nREF='"+tc.ref+"'\n"+fetchRefSnippet+"\necho \"TARGET=$TARGET\"")
			cmd.Dir = clone
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v\n%s", err, tc.wantErr, out)
			}
			if !tc.wantErr && !strings.Contains(string(out), "TARGET="+tc.want) {
				t.Fatalf("want TARGET=%s\n%s", tc.want, out)
			}
		})
	}
}

func TestFetchRefLocal(t *testing.T) {
	clone, first, _ := newOrigin(t)
	got, err := fetchRef(clone, first[:8])
	if err != nil || got != first {
		t.Fatalf("short SHA: got %q, %v; want %s", got, err, first)
	}
	if _, err := fetchRef(clone, "deadbee"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown short SHA: err = %v", err)
	}
}

func TestNodeEnvWarning(t *testing.T) {
	cfg := &config.ResolvedConfig{EnvName: "dev"}
	for value, wantWarning := range map[string]bool{"development": true, "test": true, "production": false} {
		cfg.Environment.Env = map[string]string{"NODE_ENV": value}
		if got := nodeEnvWarning(cfg); (got != "") != wantWarning {
			t.Fatalf("NODE_ENV=%s: warning %q", value, got)
		}
	}
	cfg.Environment.Env = map[string]string{"APP_ENV": "development"}
	if got := nodeEnvWarning(cfg); got != "" {
		t.Fatalf("no NODE_ENV: warning %q", got)
	}
}
