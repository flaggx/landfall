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

func TestShellQuoteValue(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"simple", "simple"},
		{"has space", `"has space"`},
		{`say "hi"`, `"say \"hi\""`},
		{"a$b", `"a$b"`},
		{"plain-ok_1", "plain-ok_1"},
	}
	for _, tt := range tests {
		if got := shellQuoteValue(tt.in); got != tt.want {
			t.Fatalf("shellQuoteValue(%q)=%q want %q", tt.in, got, tt.want)
		}
	}
}

func TestFormatEnvFile(t *testing.T) {
	body := formatEnvFile(map[string]string{
		"NODE_ENV":     "production",
		"DATABASE_URL": "postgres://u:p@localhost/db",
		"MESSAGE":      "hello world",
	})
	for _, line := range []string{
		"NODE_ENV=production",
		"DATABASE_URL=postgres://u:p@localhost/db",
		`MESSAGE="hello world"`,
	} {
		if !strings.Contains(body, line+"\n") && !strings.HasSuffix(body, line) {
			// allow either mid-file or trailing without requiring order
			if !strings.Contains(body, line) {
				t.Fatalf("missing line %q in:\n%s", line, body)
			}
		}
	}
	if strings.Count(body, "\n") < 3 {
		t.Fatalf("expected 3 lines, got %q", body)
	}
}
