package ssh

import "testing"

func TestScpHeaderUsesRequestedMode(t *testing.T) {
	if got := scpHeader(0o600, 12, "/srv/app/.env.production"); got != "C0600 12 .env.production\n" {
		t.Fatalf("got %q", got)
	}
	if got := scpHeader(0o700, 3, "/tmp/landfall-build.sh"); got != "C0700 3 landfall-build.sh\n" {
		t.Fatalf("got %q", got)
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("a b"); got != "'a b'" {
		t.Fatalf("got %q", got)
	}
	if got := shellQuote("a'b"); got != "'a'\"'\"'b'" {
		t.Fatalf("got %q", got)
	}
}
