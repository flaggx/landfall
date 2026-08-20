package util

import (
	"maps"
	"testing"
)

func TestParseEnvFile(t *testing.T) {
	raw := `
# comment
FOO=bar
EMPTY=
SPACED = value with spaces
NOEQUALS
BAZ=one=two
`
	got := ParseEnvFile(raw)
	want := map[string]string{
		"FOO":    "bar",
		"EMPTY":  "",
		"SPACED": "value with spaces",
		"BAZ":    "one=two",
	}
	if !maps.Equal(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestParseEnvFileEmpty(t *testing.T) {
	if len(ParseEnvFile("")) != 0 {
		t.Fatal("expected empty map")
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"simple", "'simple'"},
		{"has space", "'has space'"},
		{"it's", "'it'\"'\"'s'"},
		{"", "''"},
	}
	for _, tt := range tests {
		if got := ShellQuote(tt.in); got != tt.want {
			t.Fatalf("ShellQuote(%q)=%q want %q", tt.in, got, tt.want)
		}
	}
}
