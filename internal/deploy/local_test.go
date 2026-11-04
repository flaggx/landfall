package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flaggx/landfall/internal/config"
)

func testConfig() *config.ResolvedConfig {
	return &config.ResolvedConfig{
		Global: config.GlobalConfig{SSHKeyPath: "/home/me/.ssh/id_ed25519"},
		Project: config.ProjectConfig{
			Project: config.Project{Name: "my app", Repo: "git@github.com:me/app.git"},
		},
		EnvName: "prod",
		Environment: config.Environment{
			Host:   "203.0.113.10",
			User:   "deploy",
			Path:   "/var/www/app-prod",
			Branch: "main",
			Env: map[string]string{
				"DATABASE_URL": "postgres://u:s3cret@127.0.0.1/db",
				"NODE_ENV":     "production",
			},
		},
	}
}

func TestValidateRef(t *testing.T) {
	for _, ref := range []string{
		"main", "feature/login", "v1.2.3", "HEAD~1", "HEAD^", "46e7403a756c977756c1c3e881786fcdef710261",
		// Valid in git, refused by the old allow-list (deploys used to break).
		"v1.2.0+build.5", "feat/a,b", "_wip", "fix=1", "issue#42", "100%", "wow!", "release/2026.11",
	} {
		if err := validateRef(ref); err != nil {
			t.Errorf("ref %q should be accepted: %v", ref, err)
		}
	}
	for _, ref := range []string{
		"", "@", "-main", "--upload-pack=touch /tmp/x", "main; rm -rf /", "a..b", "$(id)", "`id`", "main branch",
		"main\n", "a:b", "a*", "a[0]", "a\\b", "main.lock", "feature/", "main.", "a//b", ".hidden", "a/.b", "main@{1}",
	} {
		if err := validateRef(ref); err == nil {
			t.Errorf("ref %q should be rejected", ref)
		}
	}
}

func TestValidateLocalBuildTarget(t *testing.T) {
	if err := validateLocalBuildTarget(testConfig()); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	cases := map[string]func(*config.ResolvedConfig){
		"host option":   func(c *config.ResolvedConfig) { c.Environment.Host = "-oProxyCommand=id" },
		"host with cmd": func(c *config.ResolvedConfig) { c.Environment.Host = "example.com;id" },
		"user option":   func(c *config.ResolvedConfig) { c.Environment.User = "-l root" },
		"relative path": func(c *config.ResolvedConfig) { c.Environment.Path = "www/app" },
		"repo option":   func(c *config.ResolvedConfig) { c.Project.Project.Repo = "--upload-pack=id" },
		"quoted key":    func(c *config.ResolvedConfig) { c.Global.SSHKeyPath = "/tmp/k' -oProxyCommand=id '" },
	}
	for name, mutate := range cases {
		cfg := testConfig()
		mutate(cfg)
		if err := validateLocalBuildTarget(cfg); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestComparePlatforms(t *testing.T) {
	if err := comparePlatforms("Linux x86_64", "v20.20.2", "Linux x86_64", "v20.11.0", false); err != nil {
		t.Fatalf("matching platforms rejected: %v", err)
	}
	if err := comparePlatforms("Darwin arm64", "v20.20.2", "Linux x86_64", "v20.20.2", true); err == nil {
		t.Fatal("different OS/CPU must be rejected even with allowNodeMismatch")
	}
	if err := comparePlatforms("Linux x86_64", "v22.1.0", "Linux x86_64", "v20.20.2", false); err == nil {
		t.Fatal("different Node majors must be rejected by default")
	}
	if err := comparePlatforms("Linux x86_64", "v22.1.0", "Linux x86_64", "v20.20.2", true); err != nil {
		t.Fatalf("allowNodeMismatch should permit different Node majors: %v", err)
	}
}

func TestBuildCacheDirIsSanitized(t *testing.T) {
	dir, err := buildCacheDir(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(dir, "/landfall/builds/my_app-prod") {
		t.Fatalf("unexpected cache dir %q", dir)
	}
}

func TestLocalBuildEnv(t *testing.T) {
	cfg := testConfig()
	env := strings.Join(localBuildEnv(cfg), "\n")
	for _, want := range []string{"DATABASE_URL=postgres://u:s3cret@127.0.0.1/db", "NODE_ENV=production", "LANDFALL_BUILD=local"} {
		if !strings.Contains(env, want) {
			t.Errorf("missing %q in %q", want, env)
		}
	}
	if strings.Contains(env, "LANDFALL_RELEASE") {
		t.Error("LANDFALL_RELEASE must only be set when a release_command is configured")
	}

	cfg.Environment.ReleaseCommand = "npx drizzle-kit migrate"
	if !strings.Contains(strings.Join(localBuildEnv(cfg), "\n"), "LANDFALL_RELEASE=1") {
		t.Error("LANDFALL_RELEASE=1 expected with a release_command")
	}
}

func TestRsyncArgs(t *testing.T) {
	cfg := testConfig()
	args := rsyncArgs(cfg, "/home/me/.cache/landfall/builds/app-prod/", "app-prod")
	joined := strings.Join(args, "\n")

	for _, want := range []string{
		"--delete",
		"--safe-links",
		"--protect-args",
		"StrictHostKeyChecking=yes",
		"BatchMode=yes",
		"IdentitiesOnly=yes",
		"-i '/home/me/.ssh/id_ed25519'",
		"/.git",
		"/.env",
		"/.env.*",
		"/.deploy-meta",
		"/.next/standalone/.next/cache",
		"--no-group",
		"--no-owner",
		// Written as root, straight as deploy:<code group> with final modes,
		// so the running app can read every new file at once.
		"--rsync-path=sudo rsync",
		"--chown=deploy:app-prod",
		"--chmod=Du=rwx,Dg=rx,Do=,Fu+rw,Fg+rX,Fg-w,Fo-rwx",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("rsync args missing %q:\n%s", want, joined)
		}
	}

	// Source and destination come last, after "--", with exactly one trailing slash.
	n := len(args)
	if args[n-3] != "--" || args[n-2] != "/home/me/.cache/landfall/builds/app-prod/" || args[n-1] != "deploy@203.0.113.10:/var/www/app-prod/" {
		t.Fatalf("unexpected source/destination: %q", args[n-3:])
	}
	// Secrets travel only through writeEnvFile, never on the rsync command line.
	if strings.Contains(joined, "s3cret") {
		t.Fatal("rsync args must not contain environment values")
	}
}

func TestScriptsAreValidBash(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	cfg := testConfig()
	cfg.Environment.ReleaseCommand = "npx drizzle-kit migrate"

	build := buildScript(cfg)
	if !strings.Contains(build, "export LANDFALL_RELEASE=1") || !strings.Contains(build, "export LANDFALL_BUILD=remote") {
		t.Fatalf("build script missing landfall markers:\n%s", build)
	}
	if !strings.Contains(build, "'/var/www/app-prod/.next/standalone/.next/cache'") {
		t.Fatalf("build script must reclaim the app user's runtime cache:\n%s", build)
	}
	release := releaseScript(cfg)
	if !strings.Contains(release, "npx drizzle-kit migrate") || !strings.Contains(release, ". ./.env.production") {
		t.Fatalf("release script incomplete:\n%s", release)
	}

	for name, script := range map[string]string{"build": build, "release": release} {
		cmd := exec.Command(bash, "-n")
		cmd.Stdin = strings.NewReader(script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s script: bash -n failed: %v\n%s", name, err, out)
		}
	}

	cfg.Environment.ReleaseCommand = ""
	if strings.Contains(buildScript(cfg), "LANDFALL_RELEASE") {
		t.Fatal("LANDFALL_RELEASE must not be exported without a release_command")
	}
}

// End-to-end local build against a real repository. Opt in with
// LANDFALL_E2E_REPO=<git url> (it clones, installs and builds).
func TestBuildLocallyEndToEnd(t *testing.T) {
	repo := os.Getenv("LANDFALL_E2E_REPO")
	if repo == "" {
		t.Skip("set LANDFALL_E2E_REPO to run")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	cfg := testConfig()
	cfg.Project.Project.Repo = repo
	cfg.Environment.ReleaseCommand = "true"
	cfg.Environment.Env = map[string]string{"BETTER_AUTH_SECRET": "e2e-test-only-not-a-real-secret-000000"}
	if ref := os.Getenv("LANDFALL_E2E_REF"); ref != "" {
		cfg.Environment.Branch = ref
	}

	dir, commit, err := buildLocally(cfg, cfg.Environment.Branch)
	if err != nil {
		t.Fatal(err)
	}
	if len(commit) != 40 {
		t.Fatalf("expected a full commit hash, got %q", commit)
	}
	if out, _ := exec.Command("git", "-C", dir, "status", "--porcelain", "--untracked-files=no").Output(); len(out) != 0 {
		t.Fatalf("build modified tracked files:\n%s", out)
	}
	for _, leaked := range []string{".env", ".env.production", ".env.local"} {
		if _, err := os.Stat(dir + "/" + leaked); err == nil {
			t.Fatalf("%s must not exist in the build directory", leaked)
		}
	}
	t.Logf("built %s in %s", commit, dir)
	if keep := os.Getenv("LANDFALL_E2E_KEEP"); keep != "" {
		if err := exec.Command("cp", "-a", dir, keep).Run(); err != nil {
			t.Fatal(err)
		}
	}
}

// The upload's --chmod must turn the 0700 local build into what the
// permissions step expects (750 directories, group-readable files), keeping
// executables runnable by the app's group. Checked with a real local rsync.
func TestUploadChmodWithRsync(t *testing.T) {
	rsync, err := exec.LookPath("rsync")
	if err != nil {
		t.Skip("rsync not available")
	}
	src := filepath.Join(t.TempDir(), "build")
	dst := t.TempDir()
	files := map[string]os.FileMode{"server.js": 0o600, "node_modules/.bin/tool": 0o700}
	for name, mode := range files {
		p := filepath.Join(src, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(src, 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(rsync, "--archive", "--chmod="+uploadChmod, "--", src+"/", dst+"/").CombinedOutput(); err != nil {
		t.Fatalf("rsync: %v\n%s", err, out)
	}
	for path, want := range map[string]os.FileMode{
		"":                       0o750,
		"node_modules":           0o750,
		"server.js":              0o640,
		"node_modules/.bin/tool": 0o750,
	} {
		info, err := os.Stat(filepath.Join(dst, path))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%q: mode %o, want %o", path, got, want)
		}
	}
}
