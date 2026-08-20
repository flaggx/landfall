package security

import (
	"strings"
	"testing"

	"github.com/flaggx/vpsdeploymentautomation/internal/util"
)

func TestBuildPermissionsScript(t *testing.T) {
	script := BuildPermissionsScript("/var/www/app-prod", "deploy")
	for _, want := range []string{
		"chmod 750",
		".env.production",
		"chmod 600",
		"chmod 700",
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

func TestBuildPermissionsCheckScript(t *testing.T) {
	script := BuildPermissionsCheckScript("/var/www/app-prod", "deploy", true)
	if !strings.Contains(script, "permissions_env_file") {
		t.Fatal("expected env file permission check")
	}
	if !strings.Contains(script, util.ShellQuote("true")) {
		t.Fatal("expected expect env flag")
	}
}
