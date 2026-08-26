package ssh

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
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

	hostKeyCallback, err := trustOnFirstUseCallback()
	if err != nil {
		return nil, err
	}

	config := &gossh.ClientConfig{
		User: opts.User,
		Auth: []gossh.AuthMethod{
			gossh.PublicKeys(signer),
		},
		HostKeyCallback: hostKeyCallback,
		Timeout:         opts.Timeout,
	}

	addr := net.JoinHostPort(opts.Host, fmt.Sprintf("%d", opts.Port))
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

// trustOnFirstUseCallback verifies host keys against ~/.ssh/known_hosts,
// recording keys for hosts seen for the first time and rejecting any host
// whose key no longer matches a previously recorded entry (equivalent to
// `ssh -o StrictHostKeyChecking=accept-new`).
func trustOnFirstUseCallback() (gossh.HostKeyCallback, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}

	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", sshDir, err)
	}

	knownHostsPath := filepath.Join(sshDir, "known_hosts")
	f, err := os.OpenFile(knownHostsPath, os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", knownHostsPath, err)
	}
	f.Close()

	verify, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", knownHostsPath, err)
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
	}, nil
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
	remotePath := filepath.Join("/tmp", name)
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

	go func() {
		w, _ := session.StdinPipe()
		defer w.Close()
		fmt.Fprintf(w, "C0644 %d %s\n", len(content), filepath.Base(remotePath))
		w.Write(content)
		fmt.Fprint(w, "\x00")
	}()

	cmd := fmt.Sprintf("scp -t %s", filepath.Dir(remotePath))
	if err := session.Start(cmd); err != nil {
		return fmt.Errorf("start scp upload: %w", err)
	}

	if err := session.Wait(); err != nil {
		return fmt.Errorf("upload file: %w", err)
	}

	chmodSession, err := c.conn.NewSession()
	if err != nil {
		return fmt.Errorf("create chmod session: %w", err)
	}
	defer chmodSession.Close()

	chmodCmd := fmt.Sprintf("chmod %o %s", mode, remotePath)
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
