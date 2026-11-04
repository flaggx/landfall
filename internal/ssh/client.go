package ssh

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type Client struct {
	conn    *gossh.Client
	host    string
	user    string
	keyPath string
}

type Options struct {
	Host    string
	User    string
	KeyPath string
	Port    int
	Timeout time.Duration
}

func New(opts Options) (*Client, error) {
	if opts.Port == 0 {
		opts.Port = 22
	}
	if opts.Timeout == 0 {
		opts.Timeout = 15 * time.Second
	}
	if opts.KeyPath == "" {
		return nil, fmt.Errorf("ssh key path is required")
	}

	key, err := os.ReadFile(opts.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("read ssh key %s: %w", opts.KeyPath, err)
	}

	signer, err := gossh.ParsePrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("parse ssh key: %w", err)
	}

	addr := net.JoinHostPort(opts.Host, fmt.Sprintf("%d", opts.Port))

	knownHostsPath, err := ensureKnownHostsFile()
	if err != nil {
		return nil, err
	}
	hostKeyCallback, hostKeyAlgorithms, err := hostKeyConfig(knownHostsPath, addr)
	if err != nil {
		return nil, err
	}

	config := &gossh.ClientConfig{
		User: opts.User,
		Auth: []gossh.AuthMethod{
			gossh.PublicKeys(signer),
		},
		HostKeyCallback:   hostKeyCallback,
		HostKeyAlgorithms: hostKeyAlgorithms,
		Timeout:           opts.Timeout,
	}

	conn, err := gossh.Dial("tcp", addr, config)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s: %w", addr, err)
	}

	return &Client{
		conn:    conn,
		host:    opts.Host,
		user:    opts.User,
		keyPath: opts.KeyPath,
	}, nil
}

// ensureKnownHostsFile returns the path of ~/.ssh/known_hosts, creating the
// directory and an empty file if they do not exist yet.
func ensureKnownHostsFile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}

	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", sshDir, err)
	}

	knownHostsPath := filepath.Join(sshDir, "known_hosts")
	f, err := os.OpenFile(knownHostsPath, os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return "", fmt.Errorf("create %s: %w", knownHostsPath, err)
	}
	f.Close()
	return knownHostsPath, nil
}

// hostKeyConfig verifies host keys against knownHostsPath, recording keys for
// hosts seen for the first time and rejecting any host whose key no longer
// matches a previously recorded entry (equivalent to
// `ssh -o StrictHostKeyChecking=accept-new`).
//
// It also returns the host key algorithms to negotiate with addr. When
// known_hosts already has keys for the host, only their types are offered, so
// the server presents a key we can compare against. Without this, Go may pick
// e.g. ECDSA while known_hosts only holds the host's ed25519 key, and a
// perfectly valid server would look like a changed host. A nil list (unknown
// host) keeps Go's defaults.
func hostKeyConfig(knownHostsPath, addr string) (gossh.HostKeyCallback, []string, error) {
	verify, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return nil, nil, fmt.Errorf("load %s: %w", knownHostsPath, err)
	}

	return func(hostname string, remote net.Addr, key gossh.PublicKey) error {
		err := verify(hostname, remote, key)
		if err == nil {
			return nil
		}

		var keyErr *knownhosts.KeyError
		if !errors.As(err, &keyErr) {
			return err
		}
		if len(keyErr.Want) > 0 {
			return fmt.Errorf("REMOTE HOST IDENTIFICATION HAS CHANGED for %s — refusing to connect (possible MITM attack). If this is expected (e.g. VPS was rebuilt), remove the old entry from %s: %w", hostname, knownHostsPath, err)
		}

		// Unknown host: trust on first use, recording the key for future connections.
		line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key)
		f, err := os.OpenFile(knownHostsPath, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("record host key for %s: %w", hostname, err)
		}
		defer f.Close()
		if _, err := f.WriteString(line + "\n"); err != nil {
			return fmt.Errorf("record host key for %s: %w", hostname, err)
		}

		fmt.Fprintf(os.Stderr, "Permanently added '%s' (%s) to the list of known hosts.\n", hostname, key.Type())
		return nil
	}, knownHostAlgorithms(verify, addr), nil
}

// hostKeyAlgorithmPreference orders the plain host key algorithms we offer
// when restricting negotiation to the types recorded in known_hosts.
var hostKeyAlgorithmPreference = []string{
	gossh.KeyAlgoED25519,
	gossh.KeyAlgoECDSA256,
	gossh.KeyAlgoECDSA384,
	gossh.KeyAlgoECDSA521,
	gossh.KeyAlgoRSASHA512,
	gossh.KeyAlgoRSASHA256,
	gossh.KeyAlgoRSA,
}

// knownHostAlgorithms returns the host key algorithms matching the key types
// known_hosts records for addr, or nil if the host is not recorded.
func knownHostAlgorithms(verify gossh.HostKeyCallback, addr string) []string {
	// Look the host up with a key that cannot match any entry; the resulting
	// KeyError lists every recorded key for the host.
	probe, err := gossh.NewPublicKey(ed25519.PublicKey(make([]byte, ed25519.PublicKeySize)))
	if err != nil {
		return nil
	}
	var keyErr *knownhosts.KeyError
	if !errors.As(verify(addr, &net.TCPAddr{}, probe), &keyErr) || len(keyErr.Want) == 0 {
		return nil
	}

	known := map[string]bool{}
	for _, k := range keyErr.Want {
		typ := k.Key.Type()
		if typ == gossh.KeyAlgoRSA {
			// An RSA host key can be presented with any of the RSA signature algorithms.
			known[gossh.KeyAlgoRSASHA512] = true
			known[gossh.KeyAlgoRSASHA256] = true
		}
		known[typ] = true
	}

	var algos []string
	for _, algo := range hostKeyAlgorithmPreference {
		if known[algo] {
			algos = append(algos, algo)
			delete(known, algo)
		}
	}
	// Any other recorded types (e.g. security-key host keys), in a stable order.
	var rest []string
	for algo := range known {
		rest = append(rest, algo)
	}
	sort.Strings(rest)
	return append(algos, rest...)
}

func (c *Client) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

func (c *Client) Run(command string) (int, error) {
	return c.RunWithOutput(command, os.Stdout, os.Stderr)
}

func (c *Client) RunWithOutput(command string, stdout, stderr io.Writer) (int, error) {
	session, err := c.conn.NewSession()
	if err != nil {
		return -1, fmt.Errorf("create ssh session: %w", err)
	}
	defer session.Close()

	session.Stdout = stdout
	session.Stderr = stderr

	err = session.Run(command)
	if err == nil {
		return 0, nil
	}

	exitErr, ok := err.(*gossh.ExitError)
	if ok {
		return exitErr.ExitStatus(), fmt.Errorf("command failed with exit code %d", exitErr.ExitStatus())
	}

	return -1, err
}

func (c *Client) RunScript(name, script string) (int, error) {
	remotePath := path.Join("/tmp", name)
	if err := c.UploadFile(remotePath, []byte(script), 0o700); err != nil {
		return -1, err
	}

	cmd := fmt.Sprintf("bash %s; exit_code=$?; rm -f %s; exit $exit_code", remotePath, remotePath)
	return c.Run(cmd)
}

func (c *Client) UploadFile(remotePath string, content []byte, mode os.FileMode) error {
	session, err := c.conn.NewSession()
	if err != nil {
		return fmt.Errorf("create ssh session: %w", err)
	}
	defer session.Close()

	if mode == 0 {
		mode = 0o644
	}

	// StdinPipe must be set up before Start; calling it from a goroutine
	// raced Start and could leave a nil writer (and a panic).
	stdin, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("open scp stdin: %w", err)
	}
	var stderr bytes.Buffer
	session.Stderr = &stderr

	// Remote paths are always POSIX, whatever OS landfall runs on.
	cmd := fmt.Sprintf("scp -t %s", shellQuote(path.Dir(remotePath)))
	if err := session.Start(cmd); err != nil {
		return fmt.Errorf("start scp upload: %w", err)
	}

	// The header carries the final mode, so a new file is never created more
	// permissive than requested (scp applies it before any data is written).
	writeErr := func() error {
		defer stdin.Close()
		if _, err := fmt.Fprintf(stdin, "C%04o %d %s\n", mode.Perm(), len(content), path.Base(remotePath)); err != nil {
			return err
		}
		if _, err := stdin.Write(content); err != nil {
			return err
		}
		_, err := stdin.Write([]byte{0})
		return err
	}()

	if err := session.Wait(); err != nil {
		return fmt.Errorf("upload file %s: %w %s", remotePath, err, strings.TrimSpace(stderr.String()))
	}
	if writeErr != nil {
		return fmt.Errorf("upload file %s: %w", remotePath, writeErr)
	}

	chmodSession, err := c.conn.NewSession()
	if err != nil {
		return fmt.Errorf("create chmod session: %w", err)
	}
	defer chmodSession.Close()

	// Still chmod: scp leaves the mode of a file that already existed alone.
	chmodCmd := fmt.Sprintf("chmod %o %s", mode.Perm(), shellQuote(remotePath))
	if err := chmodSession.Run(chmodCmd); err != nil {
		return fmt.Errorf("chmod remote file: %w", err)
	}

	return nil
}

func (c *Client) ReadFile(remotePath string) ([]byte, error) {
	session, err := c.conn.NewSession()
	if err != nil {
		return nil, fmt.Errorf("create ssh session: %w", err)
	}
	defer session.Close()

	var stdout bytes.Buffer
	session.Stdout = &stdout

	cmd := fmt.Sprintf("cat %s", shellQuote(remotePath))
	if err := session.Run(cmd); err != nil {
		return nil, fmt.Errorf("read remote file: %w", err)
	}

	return stdout.Bytes(), nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
