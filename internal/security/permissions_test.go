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
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("expected script to contain %q", want)
		}
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
