package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestMaskSecret(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", "(empty)"},
		{"ab", "**"},
		{"abcd", "****"},
		{"abcdef", "ab**ef"},
		{"re_abcdefghijklmnopqrstuvwxyz", "re*************************yz"},
	}
	for _, tt := range tests {
		if got := maskSecret(tt.in); got != tt.want {
			t.Fatalf("maskSecret(%q)=%q want %q", tt.in, got, tt.want)
		}
	}
}

func TestRootHelpListsCoreCommands(t *testing.T) {
	root := NewRoot()
	buf := new(bytes.Buffer)
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, cmd := range []string{"deploy", "bootstrap", "secrets", "db", "redis", "check", "security"} {
		if !strings.Contains(out, cmd) {
			t.Fatalf("help missing %q:\n%s", cmd, out)
		}
	}
}

func TestDBHelpListsBackupCommands(t *testing.T) {
	root := NewRoot()
	buf := new(bytes.Buffer)
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"db", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, cmd := range []string{"backup", "restore", "schedule", "replica", "pooler"} {
		if !strings.Contains(out, cmd) {
			t.Fatalf("db help missing %q:\n%s", cmd, out)
		}
	}
}
