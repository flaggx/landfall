package ssh

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"

	gossh "golang.org/x/crypto/ssh"
)

type upload struct {
	dir, header string
	content     []byte
}

// scpServer is an SSH server that implements just enough of the remote side
// for UploadFile: `scp -t <dir>` (the sink protocol) and `chmod`.
type scpServer struct {
	mu       sync.Mutex
	uploads  []upload
	chmods   []string
	commands []string // everything else: mktemp, bash, rm
	mktemps  int
}

func (s *scpServer) start(t *testing.T) *Client {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &gossh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)

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
			go s.serveConn(c, cfg)
		}
	}()

	conn, err := gossh.Dial("tcp", ln.Addr().String(), &gossh.ClientConfig{
		User:            "deploy",
		HostKeyCallback: gossh.FixedHostKey(signer.PublicKey()),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return &Client{conn: conn}
}

func (s *scpServer) serveConn(c net.Conn, cfg *gossh.ServerConfig) {
	_, chans, reqs, err := gossh.NewServerConn(c, cfg)
	if err != nil {
		c.Close()
		return
	}
	go gossh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			nc.Reject(gossh.UnknownChannelType, "only sessions")
			continue
		}
		ch, chReqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go s.serveSession(ch, chReqs)
	}
}

func (s *scpServer) serveSession(ch gossh.Channel, reqs <-chan *gossh.Request) {
	defer ch.Close()
	for req := range reqs {
		if req.Type != "exec" {
			req.Reply(false, nil)
			continue
		}
		var payload struct{ Command string }
		if err := gossh.Unmarshal(req.Payload, &payload); err != nil {
			req.Reply(false, nil)
			return
		}
		req.Reply(true, nil)
		status := s.run(payload.Command, ch)
		ch.SendRequest("exit-status", false, gossh.Marshal(struct{ Status uint32 }{status}))
		return
	}
}

func (s *scpServer) run(command string, ch gossh.Channel) uint32 {
	switch {
	case strings.HasPrefix(command, "scp -t "):
		dir := strings.Trim(strings.TrimPrefix(command, "scp -t "), "'")
		r := bufio.NewReader(ch)
		header, err := r.ReadString('\n')
		if err != nil {
			return 1
		}
		var mode, size int
		var name string
		if _, err := fmt.Sscanf(header, "C%o %d %s", &mode, &size, &name); err != nil {
			return 1
		}
		content := make([]byte, size)
		if _, err := io.ReadFull(r, content); err != nil {
			return 1
		}
		if b, err := r.ReadByte(); err != nil || b != 0 {
			return 1
		}
		s.mu.Lock()
		s.uploads = append(s.uploads, upload{dir: dir, header: strings.TrimSpace(header), content: content})
		s.mu.Unlock()
		return 0
	case strings.HasPrefix(command, "chmod "):
		s.mu.Lock()
		s.chmods = append(s.chmods, command)
		s.mu.Unlock()
		return 0
	case command == "mktemp -d /tmp/landfall.XXXXXXXXXX":
		s.mu.Lock()
		s.mktemps++
		n := s.mktemps
		s.mu.Unlock()
		fmt.Fprintf(ch, "/tmp/landfall.Ab3dE6gH%02d\n", n)
		return 0
	case strings.HasPrefix(command, "bash "), strings.HasPrefix(command, "rm -rf -- "):
		s.mu.Lock()
		s.commands = append(s.commands, command)
		s.mu.Unlock()
		return 0
	}
	return 127
}

func TestUploadFile(t *testing.T) {
	srv := &scpServer{}
	client := srv.start(t)

	content := []byte("DATABASE_URL='postgres://x'\n")
	if err := client.UploadFile("/var/www/my app/.env.production", content, 0o600); err != nil {
		t.Fatal(err)
	}

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.uploads) != 1 {
		t.Fatalf("uploads = %v", srv.uploads)
	}
	u := srv.uploads[0]
	if u.dir != "/var/www/my app" {
		t.Fatalf("scp target dir = %q (must be quoted for paths with spaces)", u.dir)
	}
	// The header carries the requested mode, so the file is never created
	// world-readable, even before the chmod.
	if want := fmt.Sprintf("C0600 %d .env.production", len(content)); u.header != want {
		t.Fatalf("scp header = %q, want %q", u.header, want)
	}
	if string(u.content) != string(content) {
		t.Fatalf("content = %q", u.content)
	}
	if len(srv.chmods) != 1 || srv.chmods[0] != "chmod 600 '/var/www/my app/.env.production'" {
		t.Fatalf("chmods = %v", srv.chmods)
	}
}

// Before the fix StdinPipe ran in a goroutine that raced session.Start: when
// Start won, StdinPipe failed, the nil writer panicked, or the upload hung.
// Run many uploads concurrently so `go test -race` can see any such race.
func TestUploadFileConcurrent(t *testing.T) {
	srv := &scpServer{}
	client := srv.start(t)

	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- client.UploadFile(fmt.Sprintf("/tmp/landfall-%d.sh", i), []byte(strings.Repeat("x", 64*1024)), 0o700)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.uploads) != n {
		t.Fatalf("got %d uploads, want %d", len(srv.uploads), n)
	}
	for _, u := range srv.uploads {
		if len(u.content) != 64*1024 || !strings.HasPrefix(u.header, "C0700 65536 ") {
			t.Fatalf("bad upload %q (%d bytes)", u.header, len(u.content))
		}
	}
}

// Scripts go into one private directory per session (mktemp -d), never a
// fixed path in /tmp that another user could create or symlink first, and
// the directory is removed when the session closes.
func TestRunScriptUsesPrivateTempDir(t *testing.T) {
	srv := &scpServer{}
	client := srv.start(t)

	for _, name := range []string{"landfall-a.sh", "landfall-b.sh"} {
		if _, err := client.RunScript(name, "echo hi\n"); err != nil {
			t.Fatal(err)
		}
	}
	result, err := client.TempPath("landfall-db.env")
	if err != nil {
		t.Fatal(err)
	}
	const dir = "/tmp/landfall.Ab3dE6gH01"
	if result != dir+"/landfall-db.env" {
		t.Fatalf("TempPath = %q", result)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.mktemps != 1 {
		t.Fatalf("want one temp dir per session, mktemp ran %d times", srv.mktemps)
	}
	for _, u := range srv.uploads {
		if u.dir != dir {
			t.Fatalf("script uploaded to %q, want the session's temp dir %q", u.dir, dir)
		}
	}
	want := []string{
		"bash '" + dir + "/landfall-a.sh'; exit_code=$?; rm -f '" + dir + "/landfall-a.sh'; exit $exit_code",
		"bash '" + dir + "/landfall-b.sh'; exit_code=$?; rm -f '" + dir + "/landfall-b.sh'; exit $exit_code",
		"rm -rf -- '" + dir + "'",
	}
	if strings.Join(srv.commands, "\n") != strings.Join(want, "\n") {
		t.Fatalf("commands:\n%s\nwant:\n%s", strings.Join(srv.commands, "\n"), strings.Join(want, "\n"))
	}
}

// A temp dir path that doesn't look like mktemp's is refused rather than
// used in later commands.
func TestTempDirRejectsUnexpectedPath(t *testing.T) {
	for _, dir := range []string{"/tmp/landfall.Ab3dE6gH01", "/tmp/landfall.x; rm -rf ~", "/tmp/other.Ab3dE6gH01", ""} {
		want := dir == "/tmp/landfall.Ab3dE6gH01"
		if got := tempDirPattern.MatchString(dir); got != want {
			t.Errorf("%q: match %v, want %v", dir, got, want)
		}
	}
}
