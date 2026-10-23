package bootstrap

import (
	"bytes"
	"os/exec"
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
