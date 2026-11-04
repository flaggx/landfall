package security

import (
	"os"
	"os/exec"
	"path/filepath"
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
		"99-landfall.conf",
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

// The fail2ban jail must read SSH logins from /var/log/auth.log where it
// exists (Ubuntu) and from the journal otherwise (Debian 12, no rsyslog).
// Runs the jail part of the harden script with stub sudo; the auth.log path
// is swapped for a temp file so both cases can be tested.
func TestFail2banJailSource(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	script := buildHardenScript(HardenOptions{SSHPort: 22, DisableRootSSH: true})
	start := strings.Index(script, "# --- fail2ban ---")
	end := strings.Index(script, "sudo systemctl enable fail2ban")
	if start < 0 || end < start {
		t.Fatal("fail2ban section not found")
	}
	section := script[start:end]

	for _, tc := range []struct {
		name       string
		authLog    bool
		want       string
		wantPkgLog bool
	}{
		{"ubuntu with auth.log", true, "logpath = ", false},
		{"debian 12 journal only", false, "backend = systemd", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			authLog := filepath.Join(dir, "auth.log")
			if tc.authLog {
				if err := os.WriteFile(authLog, nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			jail := filepath.Join(dir, "jail.local")
			aptLog := filepath.Join(dir, "apt.log")
			bin := t.TempDir()
			stub := "#!/bin/bash\n" +
				"if [ \"$1\" = tee ]; then exec tee " + jail + "; fi\n" +
				"if [ \"$1\" = apt-get ]; then echo \"$*\" >> " + aptLog + "; exit 0; fi\n" +
				"exec \"$@\"\n"
			if err := os.WriteFile(filepath.Join(bin, "sudo"), []byte(stub), 0o755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(bash, "-c", "set -euo pipefail\n"+strings.ReplaceAll(section, "/var/log/auth.log", authLog))
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("section failed: %v\n%s", err, out)
			}
			raw, _ := os.ReadFile(jail)
			if !strings.Contains(string(raw), tc.want) || !strings.Contains(string(raw), "[sshd]") || strings.Contains(string(raw), "F2B_SOURCE") {
				t.Fatalf("jail:\n%s", raw)
			}
			apt, _ := os.ReadFile(aptLog)
			if got := strings.Contains(string(apt), "python3-systemd"); got != tc.wantPkgLog {
				t.Fatalf("python3-systemd installed = %v, want %v (%s)", got, tc.wantPkgLog, apt)
			}
		})
	}
}
