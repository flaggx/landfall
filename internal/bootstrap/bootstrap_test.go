package bootstrap

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/flaggx/landfall/internal/config"
)

func TestShellQuote(t *testing.T) {
	if got := shellQuote(`it's`); got != `'it'"'"'s'` {
		t.Fatalf("got %q", got)
	}
}

func renderSystemd(t *testing.T, data systemdData) string {
	t.Helper()
	tmpl, err := template.New("systemd").Parse(systemdTemplate)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestSystemdTemplateRenders(t *testing.T) {
	out := renderSystemd(t, systemdData{
		ServiceName: "app-prod",
		User:        "app-prod",
		WorkDir:     "/var/www/app",
		Port:        3000,
		CacheDir:    "/var/www/app/.next/standalone/.next/cache",
	})
	for _, needle := range []string{
		"User=app-prod",
		"PORT=3000",
		"WorkingDirectory=/var/www/app/.next/standalone",
		"Description=app-prod web application",
		"NoNewPrivileges=true",
		"ProtectSystem=strict",
		"ReadWritePaths=-/var/www/app/.next/standalone/.next/cache",
		"PrivateTmp=true",
		"ProtectHome=true",
		"RestrictSUIDSGID=true",
		// os.networkInterfaces() needs a netlink socket.
		"RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK",
		// The ISR cache handler template writes inside the writable cache dir.
		"Environment=LANDFALL_ISR_CACHE_DIR=/var/www/app/.next/standalone/.next/cache/landfall-isr",
	} {
		if !strings.Contains(out, needle) {
			t.Fatalf("missing %q in %s", needle, out)
		}
	}
	// V8's JIT needs writable+executable memory.
	if strings.Contains(out, "MemoryDenyWriteExecute") {
		t.Fatal("MemoryDenyWriteExecute breaks Node.js")
	}
}

func TestSystemdTemplateUnderHome(t *testing.T) {
	out := renderSystemd(t, systemdData{
		ServiceName:   "app-prod",
		User:          "app-prod",
		WorkDir:       "/home/deploy/app",
		Port:          3000,
		CacheDir:      "/home/deploy/app/.next/standalone/.next/cache",
		PathUnderHome: true,
	})
	if strings.Contains(out, "ProtectHome") {
		t.Fatalf("ProtectHome would hide an app under /home:\n%s", out)
	}
}

func TestPathUnderHome(t *testing.T) {
	for path, want := range map[string]bool{
		"/var/www/app":       false,
		"/home/deploy/app":   true,
		"/home":              true,
		"/root/app":          true,
		"/homestead/app":     false,
		"/srv/../home/x":     true,
		"/run/user/1000/app": true,
	} {
		if got := pathUnderHome(path); got != want {
			t.Errorf("pathUnderHome(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestAppUserScript(t *testing.T) {
	script := appUserScript("app-prod")
	for _, want := range []string{"useradd --system", "--shell /usr/sbin/nologin", "--no-create-home", "'app-prod'"} {
		if !strings.Contains(script, want) {
			t.Fatalf("missing %q in %s", want, script)
		}
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	cmd := exec.Command(bash, "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, out)
	}
}

func TestCaddyTemplateRenders(t *testing.T) {
	tmpl, err := template.New("caddy").Parse(caddyTemplate)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, caddyData{Domain: "app.example.com", Port: 3000}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "app.example.com") || !strings.Contains(out, "3000") {
		t.Fatalf("unexpected caddyfile: %s", out)
	}
}

func TestServiceNameConvention(t *testing.T) {
	cfg := &config.ResolvedConfig{
		Project: config.ProjectConfig{Project: config.Project{Name: "shop"}},
		EnvName: "prod",
	}
	if cfg.ServiceName() != "shop-prod" {
		t.Fatal(cfg.ServiceName())
	}
}

// The Caddyfile the caddy package installs on Debian/Ubuntu.
const stockCaddyfileContent = `# The Caddyfile is an easy way to configure your Caddy web server.
#
# Unless the file starts with a global options block, the first
# uncommented line is always the address of your site.
#
# To use your own domain name (with automatic HTTPS), first make
# sure your domain's A/AAAA DNS records are properly pointed to
# this machine's public IP, then replace ":80" below with your
# domain name.

:80 {
	# Set this path to your site's directory.
	root * /usr/share/caddy

	# Enable the static file server.
	file_server

	# Another common task is to set up a reverse proxy:
	# reverse_proxy localhost:8080

	# Or serve a PHP site through php-fpm:
	# php_fastcgi localhost:9000
}

# Refer to the Caddy docs for more information:
# https://caddyserver.com/docs/caddyfile
`

const customCaddyfileContent = `blog.example.com {
	root * /srv/blog
	file_server
}
`

// Runs the apply script against a temp /etc/caddy with stub sudo, systemctl
// and caddy. An existing Caddyfile's sites must never be thrown away.
func TestCaddyApplyScript(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	for _, tc := range []struct {
		name, before  string
		validateFails bool
		wantErr       bool
		want          func(t *testing.T, after string)
	}{
		{name: "no Caddyfile", before: "", want: func(t *testing.T, after string) {
			if after != "import landfall/*\n" {
				t.Fatalf("got %q", after)
			}
		}},
		{name: "stock demo file", before: stockCaddyfileContent, want: func(t *testing.T, after string) {
			if after != "import landfall/*\n" {
				t.Fatalf("the stock :80 site must be replaced, got %q", after)
			}
		}},
		{name: "existing sites", before: customCaddyfileContent, want: func(t *testing.T, after string) {
			if !strings.HasPrefix(after, customCaddyfileContent) || !strings.HasSuffix(after, "import landfall/*\n") {
				t.Fatalf("existing sites must be kept and the import appended, got %q", after)
			}
		}},
		{name: "already imports landfall", before: customCaddyfileContent + "import landfall/*\n", want: func(t *testing.T, after string) {
			if after != customCaddyfileContent+"import landfall/*\n" {
				t.Fatalf("must be left alone, got %q", after)
			}
		}},
		{name: "caddy rejects the result", before: customCaddyfileContent, validateFails: true, wantErr: true, want: func(t *testing.T, after string) {
			if after != customCaddyfileContent {
				t.Fatalf("must be restored unchanged, got %q", after)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caddyDir := filepath.Join(t.TempDir(), "caddy")
			if err := os.MkdirAll(caddyDir, 0o755); err != nil {
				t.Fatal(err)
			}
			caddyfile := filepath.Join(caddyDir, "Caddyfile")
			if tc.before != "" {
				if err := os.WriteFile(caddyfile, []byte(tc.before), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			site := filepath.Join(t.TempDir(), "site.conf")
			if err := os.WriteFile(site, []byte("app.example.com {\n\treverse_proxy 127.0.0.1:3000\n}\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			bin := t.TempDir()
			validate := "exit 0"
			if tc.validateFails {
				validate = "exit 1"
			}
			for name, body := range map[string]string{
				"sudo":      "exec \"$@\"",
				"systemctl": "exit 0",
				"caddy":     validate,
			} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			}

			cmd := exec.Command(bash, "-c", caddyApplyScript(caddyDir, site, "my-webapp-prod"))
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v\n%s", err, tc.wantErr, out)
			}

			after, _ := os.ReadFile(caddyfile)
			tc.want(t, string(after))
			if _, err := os.Stat(filepath.Join(caddyDir, "landfall", "my-webapp-prod.caddy")); err != nil {
				t.Fatalf("site file not installed: %v", err)
			}

			// Whenever an existing Caddyfile was changed, a backup must hold the original.
			backups, _ := filepath.Glob(caddyfile + ".landfall-backup-*")
			changed := tc.before != "" && !strings.Contains(tc.before, "import landfall")
			if changed {
				if len(backups) != 1 {
					t.Fatalf("want one backup, got %v", backups)
				}
				if b, _ := os.ReadFile(backups[0]); string(b) != tc.before {
					t.Fatalf("backup content = %q", b)
				}
			} else if len(backups) != 0 {
				t.Fatalf("no backup expected, got %v", backups)
			}
		})
	}
}
