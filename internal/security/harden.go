package security

import (
	"fmt"
	"os"
	"strings"

	"github.com/flaggx/landfall/internal/config"
	"github.com/flaggx/landfall/internal/ssh"
)

type HardenOptions struct {
	DisableRootSSH     bool
	DisableSSHPassword bool
	AutoReboot         bool
	SSHPort            int
	ExtraPorts         []int
}

func Harden(cfg *config.ResolvedConfig, opts HardenOptions) error {
	if opts.SSHPort <= 0 {
		opts.SSHPort = 22
	}

	client, err := ssh.New(ssh.Options{
		Host:    cfg.Environment.Host,
		User:    cfg.Environment.User,
		KeyPath: cfg.Global.SSHKeyPath,
	})
	if err != nil {
		return err
	}
	defer client.Close()

	fmt.Fprintf(os.Stdout, "Hardening Ubuntu VPS for %s (%s)...\n", cfg.EnvName, cfg.SSHAddress())
	fmt.Fprintln(os.Stdout, "This enables unattended security updates, UFW firewall, fail2ban, and SSH hardening.")

	script := buildHardenScript(opts)
	if _, err := client.RunScript("landfall-security-harden.sh", script); err != nil {
		return err
	}

	if err := EnforcePermissions(cfg, client); err != nil {
		return fmt.Errorf("secret file permissions: %w", err)
	}

	fmt.Fprintln(os.Stdout, "")
	fmt.Fprintln(os.Stdout, "Security hardening complete.")
	printPostHardenNotes(opts)
	return nil
}

func Status(cfg *config.ResolvedConfig) error {
	client, err := ssh.New(ssh.Options{
		Host:    cfg.Environment.Host,
		User:    cfg.Environment.User,
		KeyPath: cfg.Global.SSHKeyPath,
	})
	if err != nil {
		return err
	}
	defer client.Close()

	script := `
set -euo pipefail

echo "=== OS ==="
if [ -f /etc/os-release ]; then
  . /etc/os-release
  echo "$PRETTY_NAME"
else
  echo "unknown"
fi
echo ""

echo "=== Unattended upgrades ==="
if dpkg -s unattended-upgrades >/dev/null 2>&1; then
  echo "installed: yes"
  systemctl is-active unattended-upgrades 2>/dev/null || true
  if [ -f /etc/apt/apt.conf.d/20auto-upgrades ]; then
    grep -E 'APT::Periodic' /etc/apt/apt.conf.d/20auto-upgrades || true
  fi
else
  echo "installed: no"
fi
echo ""

echo "=== UFW firewall ==="
if command -v ufw >/dev/null 2>&1; then
  sudo ufw status verbose || true
else
  echo "not installed"
fi
echo ""

echo "=== fail2ban ==="
if command -v fail2ban-client >/dev/null 2>&1; then
  sudo systemctl is-active fail2ban || true
  sudo fail2ban-client status sshd 2>/dev/null || echo "sshd jail not active"
else
  echo "not installed"
fi
echo ""

echo "=== SSH hardening ==="
if [ -f /etc/ssh/sshd_config.d/99-landfall.conf ]; then
  cat /etc/ssh/sshd_config.d/99-landfall.conf
elif [ -f /etc/ssh/sshd_config.d/99-vpsdeploy.conf ]; then
  cat /etc/ssh/sshd_config.d/99-vpsdeploy.conf
else
  echo "landfall SSH drop-in not found"
  sudo sshd -T 2>/dev/null | grep -E '^(permitrootlogin|passwordauthentication|port) ' || true
fi
`

	_, err = client.RunScript("landfall-security-status.sh", script)
	return err
}

func buildHardenScript(opts HardenOptions) string {
	disableRoot := "no"
	if !opts.DisableRootSSH {
		disableRoot = "prohibit-password"
	}

	disablePassword := "yes"
	if opts.DisableSSHPassword {
		disablePassword = "no"
	}

	autoReboot := "false"
	autoRebootTime := ""
	if opts.AutoReboot {
		autoReboot = "true"
		autoRebootTime = `Unattended-Upgrade::Automatic-Reboot-Time "03:30";`
	}

	extraPortRules := buildExtraPortRules(opts.SSHPort, opts.ExtraPorts)

	return fmt.Sprintf(`
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

if ! command -v apt-get >/dev/null 2>&1; then
  echo "This command supports Ubuntu/Debian only." >&2
  exit 1
fi

if [ -f /etc/os-release ]; then
  . /etc/os-release
  if [ "${ID:-}" != "ubuntu" ] && [ "${ID:-}" != "debian" ]; then
    echo "Warning: expected Ubuntu/Debian, found ${ID:-unknown}. Continuing anyway." >&2
  fi
fi

sudo apt-get update -qq
sudo apt-get install -y -qq ufw fail2ban unattended-upgrades apt-listchanges

# --- Unattended security upgrades ---
sudo tee /etc/apt/apt.conf.d/20auto-upgrades >/dev/null <<'EOF'
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Download-Upgradeable-Packages "1";
APT::Periodic::Unattended-Upgrade "1";
APT::Periodic::AutocleanInterval "7";
EOF

if [ -f /etc/apt/apt.conf.d/50unattended-upgrades ]; then
  sudo sed -i 's|^//\s*"${distro_id}:${distro_codename}-security";|"${distro_id}:${distro_codename}-security";|' /etc/apt/apt.conf.d/50unattended-upgrades || true
fi

sudo tee /etc/apt/apt.conf.d/52landfall-unattended-upgrades >/dev/null <<EOF
Unattended-Upgrade::Automatic-Reboot "%s";
%s
Unattended-Upgrade::Remove-Unused-Dependencies "true";
Unattended-Upgrade::Remove-New-Unused-Dependencies "true";
EOF

sudo systemctl enable unattended-upgrades
sudo systemctl restart unattended-upgrades

# --- UFW firewall ---
sudo ufw default deny incoming
sudo ufw default allow outgoing
%s
sudo ufw --force enable

# --- fail2ban ---
sudo tee /etc/fail2ban/jail.d/landfall-sshd.local >/dev/null <<'EOF'
[sshd]
enabled = true
port = ssh
filter = sshd
logpath = /var/log/auth.log
maxretry = 5
findtime = 10m
bantime = 1h
EOF

sudo systemctl enable fail2ban
sudo systemctl restart fail2ban

# --- SSH hardening (drop-in, reversible) ---
sudo tee /etc/ssh/sshd_config.d/99-landfall.conf >/dev/null <<'EOF'
# Managed by landfall security harden
PermitRootLogin %s
PasswordAuthentication %s
KbdInteractiveAuthentication no
X11Forwarding no
AllowTcpForwarding no
EOF

if ! sudo sshd -t; then
  echo "SSH config test failed; removing landfall drop-in" >&2
  sudo rm -f /etc/ssh/sshd_config.d/99-landfall.conf
  exit 1
fi

sudo systemctl reload ssh 2>/dev/null || sudo systemctl reload sshd

echo "Hardening applied successfully."
`, autoReboot, autoRebootTime, extraPortRules, disableRoot, disablePassword)
}

func buildExtraPortRules(sshPort int, extraPorts []int) string {
	allowed := map[int]struct{}{
		22:  {},
		80:  {},
		443: {},
	}
	if sshPort != 22 {
		allowed[sshPort] = struct{}{}
	}
	for _, port := range extraPorts {
		allowed[port] = struct{}{}
	}

	var ports []int
	for port := range allowed {
		ports = append(ports, port)
	}
	sortPorts(ports)

	var b strings.Builder
	for _, port := range ports {
		fmt.Fprintf(&b, "sudo ufw allow %d/tcp\n", port)
	}
	return b.String()
}

func sortPorts(ports []int) {
	for i := 0; i < len(ports); i++ {
		for j := i + 1; j < len(ports); j++ {
			if ports[j] < ports[i] {
				ports[i], ports[j] = ports[j], ports[i]
			}
		}
	}
}

func printPostHardenNotes(opts HardenOptions) {
	fmt.Fprintln(os.Stdout, "")
	fmt.Fprintln(os.Stdout, "Applied:")
	fmt.Fprintln(os.Stdout, "  - Unattended security upgrades (auto-update)")
	fmt.Fprintln(os.Stdout, "  - UFW firewall (SSH, HTTP, HTTPS)")
	fmt.Fprintln(os.Stdout, "  - fail2ban SSH protection")
	fmt.Fprintln(os.Stdout, "  - SSH hardening via /etc/ssh/sshd_config.d/99-landfall.conf")
	fmt.Fprintln(os.Stdout, "  - Secret file permissions (deploy dir 750, .env.production 600)")

	if opts.DisableSSHPassword {
		fmt.Fprintln(os.Stdout, "")
		fmt.Fprintln(os.Stdout, "SSH password login is disabled. Ensure your SSH key works before closing this session.")
	}

	if opts.AutoReboot {
		fmt.Fprintln(os.Stdout, "")
		fmt.Fprintln(os.Stdout, "Automatic reboot is enabled for kernel security updates (03:30 server time).")
	}

	fmt.Fprintln(os.Stdout, "")
	fmt.Fprintf(os.Stdout, "Check status: landfall security status --env <env>\n")
}
