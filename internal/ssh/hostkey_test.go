package ssh

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type hostKeys struct {
	ed25519, ecdsa, rsa gossh.Signer
}

func newHostKeys(t *testing.T) hostKeys {
	t.Helper()
	_, edPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rsaPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var k hostKeys
	for dst, priv := range map[*gossh.Signer]any{&k.ed25519: edPriv, &k.ecdsa: ecPriv, &k.rsa: rsaPriv} {
		s, err := gossh.NewSignerFromKey(priv)
		if err != nil {
			t.Fatal(err)
		}
		*dst = s
	}
	return k
}

// startServer runs an SSH server presenting every given host key, like a
// stock Ubuntu sshd (ed25519, ecdsa and rsa), and returns its address.
func startServer(t *testing.T, signers ...gossh.Signer) string {
	t.Helper()
	cfg := &gossh.ServerConfig{NoClientAuth: true}
	for _, s := range signers {
		cfg.AddHostKey(s)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, chans, reqs, err := gossh.NewServerConn(c, cfg)
				if err != nil {
					c.Close()
					return
				}
				go gossh.DiscardRequests(reqs)
				for ch := range chans {
					ch.Reject(gossh.Prohibited, "test server")
				}
			}()
		}
	}()
	return ln.Addr().String()
}

func writeKnownHosts(t *testing.T, addr string, keys ...gossh.PublicKey) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "known_hosts")
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(knownhosts.Line([]string{knownhosts.Normalize(addr)}, k) + "\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func dial(t *testing.T, knownHostsPath, addr string) error {
	t.Helper()
	cb, algos, err := hostKeyConfig(knownHostsPath, addr)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := gossh.Dial("tcp", addr, &gossh.ClientConfig{
		User:              "deploy",
		HostKeyCallback:   cb,
		HostKeyAlgorithms: algos,
	})
	if err == nil {
		conn.Close()
	}
	return err
}

func TestHostKeyRecordedTypeIsNegotiated(t *testing.T) {
	keys := newHostKeys(t)
	addr := startServer(t, keys.ecdsa, keys.ed25519, keys.rsa)

	for name, recorded := range map[string]gossh.PublicKey{
		"ed25519": keys.ed25519.PublicKey(),
		"ecdsa":   keys.ecdsa.PublicKey(),
		"rsa":     keys.rsa.PublicKey(),
	} {
		t.Run(name, func(t *testing.T) {
			// known_hosts only has one of the server's key types, as when
			// OpenSSH recorded the host first. This must not look like a MITM.
			if err := dial(t, writeKnownHosts(t, addr, recorded), addr); err != nil {
				t.Fatalf("connect with only the %s key recorded: %v", name, err)
			}
		})
	}
}

func TestHostKeyChangedSameTypeIsRejected(t *testing.T) {
	keys := newHostKeys(t)
	other := newHostKeys(t)
	addr := startServer(t, keys.ecdsa, keys.ed25519)

	err := dial(t, writeKnownHosts(t, addr, other.ed25519.PublicKey()), addr)
	if err == nil || !strings.Contains(err.Error(), "REMOTE HOST IDENTIFICATION HAS CHANGED") {
		t.Fatalf("expected a changed-host error, got %v", err)
	}
}

func TestHostKeyUnknownHostIsRecorded(t *testing.T) {
	keys := newHostKeys(t)
	addr := startServer(t, keys.ed25519)
	path := writeKnownHosts(t, addr)

	if err := dial(t, path, addr); err != nil {
		t.Fatalf("first connection: %v", err)
	}
	if _, algos, _ := hostKeyConfig(path, addr); !slices.Equal(algos, []string{gossh.KeyAlgoED25519}) {
		t.Fatalf("after recording, algorithms = %v, want [ssh-ed25519]", algos)
	}
	if err := dial(t, path, addr); err != nil {
		t.Fatalf("second connection: %v", err)
	}
}

func TestKnownHostAlgorithms(t *testing.T) {
	keys := newHostKeys(t)
	addr := "203.0.113.10:22"

	if _, algos, _ := hostKeyConfig(writeKnownHosts(t, addr), addr); algos != nil {
		t.Fatalf("unknown host: got %v, want nil (Go defaults)", algos)
	}

	path := writeKnownHosts(t, addr, keys.rsa.PublicKey(), keys.ed25519.PublicKey())
	_, algos, _ := hostKeyConfig(path, addr)
	want := []string{gossh.KeyAlgoED25519, gossh.KeyAlgoRSASHA512, gossh.KeyAlgoRSASHA256, gossh.KeyAlgoRSA}
	if !slices.Equal(algos, want) {
		t.Fatalf("got %v, want %v", algos, want)
	}

	// Entries for other hosts or ports are ignored.
	if _, algos, _ := hostKeyConfig(path, "203.0.113.10:2222"); algos != nil {
		t.Fatalf("other port: got %v, want nil", algos)
	}
}
