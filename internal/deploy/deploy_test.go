package deploy

import (
	"strings"
	"testing"
)

func TestShellQuote(t *testing.T) {
	if got := shellQuote("a'b"); got != "'a'\"'\"'b'" {
		t.Fatalf("got %q", got)
	}
}

func TestFormatEnvFile(t *testing.T) {
	body := formatEnvFile(map[string]string{
		"NODE_ENV":     "production",
		"DATABASE_URL": "postgres://u:p@localhost/db",
		"MESSAGE":      "hello world",
	})
	for _, line := range []string{
		"NODE_ENV='production'",
		"DATABASE_URL='postgres://u:p@localhost/db'",
		"MESSAGE='hello world'",
	} {
		if !strings.Contains(body, line) {
			t.Fatalf("missing line %q in:\n%s", line, body)
		}
	}
	if strings.Count(body, "\n") < 3 {
		t.Fatalf("expected 3 lines, got %q", body)
	}
}

// buildApp sources .env.production with bash (`. ./.env.production`), so
// values must be single-quoted: a value left unquoted or double-quoted would
// let bash expand "$vars", `backticks`, and "\" escapes embedded in a secret
// instead of treating it as a literal string.
func TestFormatEnvFileNeutralizesShellMetacharacters(t *testing.T) {
	value := "p$(whoami)`w`\\d"
	body := formatEnvFile(map[string]string{"SECRET": value})
	want := "SECRET='" + value + "'\n"
	if body != want {
		t.Fatalf("got %q want %q", body, want)
	}
}
