package deploy

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/flaggx/landfall/internal/config"
	"github.com/flaggx/landfall/internal/ssh"
)

// Names that end up as arguments to git, rsync and ssh on the local machine.
// They are passed without a shell, but a value starting with "-" would still
// be read as an option, so reject anything outside a conservative set.
var (
	safeRefPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/@^~-]*$`)
	safeHostPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	unsafeNameChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)
)

// uploadExcludes are never sent to the server and never deleted there.
// Secrets reach the server only through writeEnvFile.
var uploadExcludes = []string{
	"/.git",
	"/.env",
	"/.env.*",
	"/.deploy-meta",
	"/.next/cache",
	// The running app's cache, owned by the app user (see security.RuntimeCacheDir).
	"/.next/standalone/.next/cache",
}

func validateRef(ref string) error {
	if !safeRefPattern.MatchString(ref) || strings.Contains(ref, "..") {
		return fmt.Errorf("invalid git ref %q", ref)
	}
	return nil
}

func validateLocalBuildTarget(cfg *config.ResolvedConfig) error {
	if !safeHostPattern.MatchString(cfg.Environment.Host) {
		return fmt.Errorf("local builds need a plain hostname or IPv4 address, got host %q", cfg.Environment.Host)
	}
	if !safeHostPattern.MatchString(cfg.Environment.User) {
		return fmt.Errorf("invalid ssh user %q", cfg.Environment.User)
	}
	if !filepath.IsAbs(cfg.Environment.Path) {
		return fmt.Errorf("deploy path must be absolute, got %q", cfg.Environment.Path)
	}
	if strings.HasPrefix(cfg.Project.Project.Repo, "-") {
		return fmt.Errorf("invalid repo %q", cfg.Project.Project.Repo)
	}
	if strings.ContainsAny(cfg.Global.SSHKeyPath, "'\"\\\n") {
		return fmt.Errorf("ssh key path %q contains characters local builds cannot pass to rsync", cfg.Global.SSHKeyPath)
	}
	return nil
}

// localPreflight checks that this machine can produce a build the server can
// run: same OS and CPU (native modules) and the same Node.js major version.
func localPreflight(client *ssh.Client, cfg *config.ResolvedConfig, allowNodeMismatch bool) error {
	if err := validateLocalBuildTarget(cfg); err != nil {
		return err
	}
	for _, tool := range []string{"git", "node", "npm", "rsync", "ssh", "uname"} {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("local builds need %q on this machine: %w", tool, err)
		}
	}

	remote := &bytes.Buffer{}
	if _, err := client.RunWithOutput("uname -sm && node --version", remote, os.Stderr); err != nil {
		return fmt.Errorf("read server platform: %w", err)
	}
	remoteLines := strings.Split(strings.TrimSpace(remote.String()), "\n")
	if len(remoteLines) != 2 {
		return fmt.Errorf("unexpected server platform output %q", remote.String())
	}

	localPlatform, err := localOutput("", "uname", "-sm")
	if err != nil {
		return err
	}
	localNode, err := localOutput("", "node", "--version")
	if err != nil {
		return err
	}

	return comparePlatforms(localPlatform, localNode, strings.TrimSpace(remoteLines[0]), strings.TrimSpace(remoteLines[1]), allowNodeMismatch)
}

func comparePlatforms(localPlatform, localNode, remotePlatform, remoteNode string, allowNodeMismatch bool) error {
	if localPlatform != remotePlatform {
		return fmt.Errorf("local builds need the same OS and CPU as the server: this machine is %q, the server is %q (use --build remote)", localPlatform, remotePlatform)
	}
	if nodeMajor(localNode) != nodeMajor(remoteNode) {
		if !allowNodeMismatch {
			return fmt.Errorf("Node.js major versions differ: this machine has %s, the server has %s. Install the server's version locally, or pass --allow-node-mismatch if the app has no natively compiled dependencies", localNode, remoteNode)
		}
		fmt.Fprintf(os.Stderr, "warning: building with Node.js %s for a server running %s\n", localNode, remoteNode)
	}
	return nil
}

func nodeMajor(version string) string {
	return strings.SplitN(strings.TrimPrefix(strings.TrimSpace(version), "v"), ".", 2)[0]
}

// buildCacheDir is a private, per-environment clone used only for builds, so
// nothing from the developer's working tree (uncommitted changes, local env
// files) can end up on the server.
func buildCacheDir(cfg *config.ResolvedConfig) (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	name := unsafeNameChars.ReplaceAllString(cfg.Project.Project.Name+"-"+cfg.EnvName, "_")
	return filepath.Join(base, config.AppName, "builds", name), nil
}

// buildLocally checks out ref into the build cache, builds it, and returns
// the build directory and the commit that was built.
func buildLocally(cfg *config.ResolvedConfig, ref string) (dir, commit string, err error) {
	dir, err = buildCacheDir(cfg)
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return "", "", err
	}

	repo := cfg.Project.Project.Repo
	if _, statErr := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(statErr) {
		if err := runLocal("", nil, "git", "clone", "--no-checkout", "--", repo, dir); err != nil {
			return "", "", err
		}
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", "", err
	}
	if err := runLocal(dir, nil, "git", "remote", "set-url", "origin", "--", repo); err != nil {
		return "", "", err
	}
	if err := runLocal(dir, nil, "git", "fetch", "origin", "--", ref); err != nil {
		return "", "", err
	}
	commit, err = localOutput(dir, "git", "rev-parse", "FETCH_HEAD^{commit}")
	if err != nil {
		return "", "", err
	}
	if err := runLocal(dir, nil, "git", "checkout", "--force", "--detach", commit); err != nil {
		return "", "", err
	}
	// Start from a pristine tree so stale output from an earlier build can't be uploaded.
	if err := runLocal(dir, nil, "git", "clean", "-fdx"); err != nil {
		return "", "", err
	}

	// Install dependencies without the app's secrets in the environment;
	// only the build itself receives them.
	if err := runLocal(dir, nil, "npm", "ci", "--include=dev"); err != nil {
		return "", "", err
	}
	if err := runLocal(dir, localBuildEnv(cfg), "npm", "run", "build"); err != nil {
		return "", "", err
	}
	return dir, commit, nil
}

// localBuildEnv passes the environment's variables to the build through the
// process environment, so secrets are never written to disk on this machine.
func localBuildEnv(cfg *config.ResolvedConfig) []string {
	keys := make([]string, 0, len(cfg.Environment.Env))
	for key := range cfg.Environment.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	env := make([]string, 0, len(keys)+3)
	for _, key := range keys {
		env = append(env, key+"="+cfg.Environment.Env[key])
	}
	env = append(env, "NODE_ENV=production", "LANDFALL_BUILD=local")
	if cfg.Environment.ReleaseCommand != "" {
		env = append(env, "LANDFALL_RELEASE=1")
	}
	return env
}

// verifyServerCommit makes sure the server's checkout is the commit that was
// built, in case the ref moved between the local build and the sync.
func verifyServerCommit(client *ssh.Client, cfg *config.ResolvedConfig, built string) error {
	out := &bytes.Buffer{}
	cmd := fmt.Sprintf("cd %s && git rev-parse HEAD", shellQuote(cfg.Environment.Path))
	if _, err := client.RunWithOutput(cmd, out, os.Stderr); err != nil {
		return err
	}
	if server := strings.TrimSpace(out.String()); server != built {
		return fmt.Errorf("the server checked out %s but %s was built locally (the ref moved during the deploy); run the deploy again", server, built)
	}
	return nil
}

// uploadBuild mirrors the build directory onto the server's deploy path.
func uploadBuild(cfg *config.ResolvedConfig, dir string) error {
	return runLocal("", nil, "rsync", rsyncArgs(cfg, dir)...)
}

func rsyncArgs(cfg *config.ResolvedConfig, dir string) []string {
	// StrictHostKeyChecking=yes: the Go SSH client has already verified (or
	// recorded) the host key in known_hosts, so rsync must not accept a new one.
	sshCommand := fmt.Sprintf("ssh -i '%s' -o BatchMode=yes -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes", cfg.Global.SSHKeyPath)

	args := []string{
		"--archive",
		// Leave group ownership to the permissions step, which gives the code
		// to the app user's group; local group IDs mean nothing on the server.
		"--no-group",
		"--compress",
		"--delete",
		// Don't let a symlink in the build point outside the deploy path.
		"--safe-links",
		// Send the remote path as-is instead of through the remote shell.
		"--protect-args",
		"--rsh", sshCommand,
	}
	for _, pattern := range uploadExcludes {
		args = append(args, "--exclude", pattern)
	}
	destination := fmt.Sprintf("%s@%s:%s/", cfg.Environment.User, cfg.Environment.Host, strings.TrimRight(cfg.Environment.Path, "/"))
	return append(args, "--", strings.TrimRight(dir, "/")+"/", destination)
}

func runLocal(dir string, extraEnv []string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if extraEnv != nil {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, firstArg(args), err)
	}
	return nil
}

func localOutput(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w", name, firstArg(args), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}
