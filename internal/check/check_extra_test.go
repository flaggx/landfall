package check

import (
	"testing"

	"github.com/flaggx/landfall/internal/config"
)

func TestParseHealthBodyChecksFailing(t *testing.T) {
	results := parseHealthBodyChecks(`{"ok":false,"checks":{"app":"ok","database":"error"}}`)
	byName := map[string]Result{}
	for _, r := range results {
		byName[r.Name] = r
	}
	if byName["health: app"].Status != StatusPass {
		t.Fatal("app should pass")
	}
	if byName["health: database"].Status != StatusFail {
		t.Fatal("database should fail")
	}
}

func TestParseHealthBodyChecksInvalidJSON(t *testing.T) {
	if results := parseHealthBodyChecks("not-json"); len(results) != 0 {
		t.Fatalf("expected empty, got %#v", results)
	}
}

func TestParseRemoteResultsMalformedLinesIgnored(t *testing.T) {
	raw := `
not_a_result
security_ufw=PASS|active
broken=nopipe
`
	results := parseRemoteResults(raw)
	if len(results) != 1 || results[0].Name != "security ufw" {
		t.Fatalf("got %#v", results)
	}
}

func TestEnvExpectsPostgresFromStruct(t *testing.T) {
	cfg := &config.ResolvedConfig{
		Environment: config.Environment{
			Postgres: &config.PostgresConfig{Database: "x"},
		},
	}
	if !envExpectsPostgres(cfg) {
		t.Fatal("expected true from postgres struct")
	}
}

func TestEnvExpectsRedisFromStruct(t *testing.T) {
	cfg := &config.ResolvedConfig{
		Environment: config.Environment{
			Redis: &config.RedisConfig{User: "x"},
		},
	}
	if !envExpectsRedis(cfg) {
		t.Fatal("expected true from redis struct")
	}
}

func TestEnvExpectsPostgresEmpty(t *testing.T) {
	cfg := &config.ResolvedConfig{Environment: config.Environment{Env: map[string]string{}}}
	if envExpectsPostgres(cfg) {
		t.Fatal("expected false")
	}
}
