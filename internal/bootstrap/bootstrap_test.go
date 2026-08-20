package bootstrap

import (
	"bytes"
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

func TestSystemdTemplateRenders(t *testing.T) {
	tmpl, err := template.New("systemd").Parse(systemdTemplate)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	err = tmpl.Execute(&buf, systemdData{
		ServiceName: "app-prod",
		User:        "deploy",
		WorkDir:     "/var/www/app",
		Port:        3000,
	})
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, needle := range []string{
		"User=deploy",
		"PORT=3000",
		"WorkingDirectory=/var/www/app/.next/standalone",
		"Description=app-prod web application",
	} {
		if !strings.Contains(out, needle) {
			t.Fatalf("missing %q in %s", needle, out)
		}
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
