package security

import (
	"strings"
	"testing"
)

func TestBuildExtraPortRules(t *testing.T) {
	rules := buildExtraPortRules(22, nil)
	for _, want := range []string{"ufw allow 22/tcp", "ufw allow 80/tcp", "ufw allow 443/tcp"} {
		if !strings.Contains(rules, want) {
			t.Fatalf("missing %q in rules:\n%s", want, rules)
		}
	}
}

func TestBuildExtraPortRulesCustomSSH(t *testing.T) {
	rules := buildExtraPortRules(2222, []int{8080})
	for _, want := range []string{"ufw allow 2222/tcp", "ufw allow 8080/tcp"} {
		if !strings.Contains(rules, want) {
			t.Fatalf("missing %q in rules:\n%s", want, rules)
		}
	}
}
