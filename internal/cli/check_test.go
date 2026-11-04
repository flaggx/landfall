package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A missing secret must show up as a failed "local secrets" row in the
// report, not stop `landfall check` before it prints anything. Secrets of
// other environments do not count.
func TestCheckReportsMissingSecrets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home) // no SSH key and no secrets file
	project := t.TempDir()
	toml := `[project]
name = "my-webapp"
repo = "git@github.com:a/b.git"

[environments.prod]
host = "203.0.113.10"
user = "deploy"
path = "/var/www/app"
branch = "main"
port = 3000

[environments.prod.env]
REDIS_URL = "{{secret:prod_redis_url}}"

[environments.dev]
host = "203.0.113.11"
user = "deploy"
path = "/var/www/app-dev"
branch = "dev"
port = 3001

[environments.dev.env]
DATABASE_URL = "{{secret:dev_db_url}}"
`
	if err := os.WriteFile(filepath.Join(project, "landfall.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := captureStdout(t, func() error {
		root := NewRoot()
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		root.SetArgs([]string{"check", "--env", "prod", "--project-dir", project})
		return root.Execute()
	})
	if err == nil || strings.Contains(err.Error(), "is not set") {
		t.Fatalf("want the report's own failure, got err=%v\n%s", err, out)
	}
	if !strings.Contains(out, "missing: prod_redis_url") {
		t.Fatalf("report must list the missing secret:\n%s", out)
	}
	if strings.Contains(out, "dev_db_url") {
		t.Fatalf("another environment's secret must not be required:\n%s", out)
	}
}

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	runErr := fn()
	os.Stdout = orig
	w.Close()
	return <-done, runErr
}
