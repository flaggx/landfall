package security

import (
	"strings"
	"testing"
)

func TestBuildHardenScriptDefaults(t *testing.T) {
	script := buildHardenScript(HardenOptions{
		SSHPort:            22,
		DisableRootSSH:     true,
		DisableSSHPassword: false,
		AutoReboot:         false,
	})
	for _, needle := range []string{
		"unattended-upgrades",
		"ufw",
		"fail2ban",
		"PermitRootLogin no",
		"PasswordAuthentication yes",
		"99-vpsdeploy.conf",
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("missing %q", needle)
		}
	}
}

func TestBuildHardenScriptKeyOnlyAndReboot(t *testing.T) {
	script := buildHardenScript(HardenOptions{
		SSHPort:            2222,
		DisableRootSSH:     true,
		DisableSSHPassword: true,
		AutoReboot:         true,
		ExtraPorts:         []int{8080},
	})
	for _, needle := range []string{
		"PasswordAuthentication no",
		"Automatic-Reboot",
		"2222/tcp",
		"8080/tcp",
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("missing %q", needle)
		}
	}
}

func TestBuildPermissionsCheckScriptWithoutEnv(t *testing.T) {
	script := BuildPermissionsCheckScript("/var/www/app", "deploy", false)
	if !strings.Contains(script, "EXPECT_ENV='false'") && !strings.Contains(script, "EXPECT_ENV=false") {
		t.Fatalf("missing EXPECT_ENV false:\n%s", script)
	}
}
