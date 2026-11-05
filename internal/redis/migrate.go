package redis

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/flaggx/landfall/internal/config"
	"github.com/flaggx/landfall/internal/deploy"
	"github.com/flaggx/landfall/internal/util"
)

type MigrateOptions struct {
	// FromDB is the DB index on the shared Redis to copy from. Nil means: the
	// index in the environment's current REDIS_URL secret, or else the
	// per-environment default.
	FromDB *int
	// AllowSharedDB copies even though another environment on the same host
	// uses the same DB index on the shared Redis (its keys would be copied
	// too).
	AllowSharedDB bool
}

// Migrate moves an environment from the shared Redis to its own instance:
//
//  1. create the instance and its app user (bootstrap, new password),
//  2. stop the app, so nothing writes to the shared Redis during the copy,
//  3. copy the environment's DB with MIGRATE COPY (the source is kept),
//  4. save the new REDIS_URL secret, rewrite the server's .env.production,
//  5. start the app and wait for its health check.
//
// The app is down for steps 2-5, typically seconds. The shared Redis keeps
// the environment's data and user, so the old URL still works for a rollback.
func Migrate(cfg *config.ResolvedConfig, opts MigrateOptions) error {
	secretName := cfg.RedisSecretName()
	if !referencesSecret(cfg, secretName) {
		return fmt.Errorf("environments.%s.env does not use {{secret:%s}}; add REDIS_URL = \"{{secret:%s}}\" so the app picks up the new instance, then run migrate again", cfg.EnvName, secretName, secretName)
	}
	secrets, err := config.LoadSecrets()
	if err != nil {
		return fmt.Errorf("load secrets: %w", err)
	}
	fromDB, err := migrationSourceDB(cfg, opts, secrets.Secrets, DefaultLayout.SharedPort)
	if err != nil {
		return err
	}
	user, err := cfg.RedisUser()
	if err != nil {
		return err
	}
	instance, err := cfg.RedisInstance()
	if err != nil {
		return err
	}

	client, err := sshClient(cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	inst := DefaultLayout.Instance(instance)
	fmt.Fprintf(os.Stdout, "Migrating %s from the shared Redis (port %d, DB %d) to its own instance %s on %s\n",
		cfg.EnvName, DefaultLayout.SharedPort, fromDB, inst.Unit, cfg.SSHAddress())

	if _, err := client.RunScript("landfall-redis-migrate-check.sh", buildMigrateCheckScript(DefaultLayout, cfg.EnvName, instance, user, fromDB)); err != nil {
		return err
	}

	// 1. The instance and a fresh password. Rotating is harmless: the app
	// still uses the shared Redis until step 4.
	fmt.Fprintln(os.Stdout, "→ create instance")
	vars, err := runBootstrap(client, cfg, BootstrapOptions{ResetPassword: true, Fresh: true})
	if err != nil {
		return err
	}
	newURL := vars["REDIS_URL"]
	port, _ := strconv.Atoi(vars["PORT"])
	if newURL == "" || port == 0 {
		return fmt.Errorf("creating the instance did not return a connection string")
	}

	// 2-5, with the app stopped. Whatever fails, try to start the app again.
	service := cfg.ServiceName()
	fmt.Fprintln(os.Stdout, "→ stop app")
	if _, err := client.Run(fmt.Sprintf("sudo systemctl stop %s", util.ShellQuote(service))); err != nil {
		return fmt.Errorf("stop %s: %w", service, err)
	}
	started := false
	defer func() {
		if !started {
			fmt.Fprintf(os.Stderr, "Starting %s again (still on the shared Redis unless the env file was already rewritten)...\n", service)
			_, _ = client.Run(fmt.Sprintf("sudo systemctl start %s", util.ShellQuote(service)))
		}
	}()

	fmt.Fprintln(os.Stdout, "→ copy keys")
	if _, err := client.RunScript("landfall-redis-copy.sh", buildCopyScript(DefaultLayout, instance, fromDB, port, cfg.RedisDatabase())); err != nil {
		return fmt.Errorf("copy keys (the app still uses the shared Redis; nothing was switched): %w", err)
	}

	fmt.Fprintln(os.Stdout, "→ switch REDIS_URL")
	oldURL := secrets.Secrets[secretName]
	if err := switchSecret(secretName, oldURL, newURL, saveSecret); err != nil {
		return fmt.Errorf("%w (the app still uses the shared Redis)\nNew URL: %s", err, newURL)
	}
	rollback := rollbackHint(cfg.EnvName, secretName, oldURL != "")
	resolved, err := config.LoadResolved(cfg.EnvName, cfg.ProjectPath)
	if err != nil {
		return fmt.Errorf("reload config with the new secret: %w\n%s", err, rollback)
	}
	if err := deploy.WriteEnvFile(client, resolved); err != nil {
		return fmt.Errorf("write .env.production: %w\n%s", err, rollback)
	}

	fmt.Fprintln(os.Stdout, "→ start app")
	if _, err := client.Run(fmt.Sprintf("sudo systemctl start %s && sudo systemctl is-active --quiet %s", util.ShellQuote(service), util.ShellQuote(service))); err != nil {
		return fmt.Errorf("start %s on the new instance: %w\n%s", service, err, rollback)
	}
	started = true
	if err := deploy.HealthCheck(client, resolved); err != nil {
		return fmt.Errorf("the app runs on the new instance but its health check failed: %w\n%s", err, rollback)
	}

	fmt.Fprintf(os.Stdout, "\nMigrated %s to its own Redis instance %s (port %d). Saved secret %q.\n", cfg.EnvName, instance, port, secretName)
	fmt.Fprintln(os.Stdout, rollback)
	fmt.Fprintf(os.Stdout, "The shared Redis still holds the old copy and user. Once you are happy, remove them:\n")
	fmt.Fprintf(os.Stdout, "  landfall redis status --env %s   (shows the cleanup command)\n", cfg.EnvName)
	if oldURL != "" {
		fmt.Fprintf(os.Stdout, "  landfall secrets delete %s\n", PreviousSecretName(secretName))
	}
	return nil
}

// PreviousSecretName is where migrate keeps an environment's old REDIS_URL,
// so a rollback to the shared Redis doesn't need its password reset.
func PreviousSecretName(secretName string) string {
	return secretName + "_previous"
}

// switchSecret saves the old URL (if any) as the previous secret, then the
// new one. The new URL is only saved once the old one is kept, so a failure
// never leaves the old URL lost.
func switchSecret(name, oldURL, newURL string, save func(name, value string) error) error {
	if oldURL != "" {
		if err := save(PreviousSecretName(name), oldURL); err != nil {
			return fmt.Errorf("keep the old URL as secret %q: %w", PreviousSecretName(name), err)
		}
	}
	if err := save(name, newURL); err != nil {
		return fmt.Errorf("save secret %q: %w", name, err)
	}
	return nil
}

// rollbackHint tells how to point the app back at the shared Redis.
func rollbackHint(envName, secretName string, kept bool) string {
	if !kept {
		return fmt.Sprintf("To roll back: set %s to the old URL, then run: landfall deploy --env %s", secretName, envName)
	}
	return fmt.Sprintf("The old URL is kept in secret %q. To roll back:\n  landfall secrets set %s --value \"$(landfall secrets get %s --reveal)\" && landfall deploy --env %s",
		PreviousSecretName(secretName), secretName, PreviousSecretName(secretName), envName)
}

// referencesSecret reports whether the environment's env uses the secret, so
// saving a new value actually reaches the app.
func referencesSecret(cfg *config.ResolvedConfig, secret string) bool {
	ref := "{{secret:" + secret + "}}"
	for _, v := range cfg.Environment.Env {
		if strings.Contains(v, ref) {
			return true
		}
	}
	return false
}

// migrationSourceDB is the DB index the environment uses on the shared Redis:
// --from-db, else the index in its current REDIS_URL secret (what the app
// really uses), else the per-environment default. It refuses an index that
// another environment on the same host also uses, since its keys would be
// copied into this environment's instance.
func migrationSourceDB(cfg *config.ResolvedConfig, opts MigrateOptions, secrets map[string]string, sharedPort int) (int, error) {
	// sharedDB is the DB index an environment uses on the shared Redis, and
	// false when its REDIS_URL already points at another port (its own
	// instance), so it has no keys on the shared Redis to collide with.
	sharedDB := func(envName string, env config.Environment) (int, bool) {
		c := &config.ResolvedConfig{EnvName: envName, Environment: env}
		raw := secrets[c.RedisSecretName()]
		if port, ok := urlPort(raw); ok && port != sharedPort {
			return 0, false
		}
		if db, ok := urlDB(raw); ok {
			return db, true
		}
		return c.LegacyRedisDatabase(), true
	}

	from, _ := sharedDB(cfg.EnvName, cfg.Environment)
	if opts.FromDB != nil {
		from = *opts.FromDB
	}
	if opts.AllowSharedDB {
		return from, nil
	}
	for name, env := range cfg.Project.Environments {
		if name == cfg.EnvName || env.Host != cfg.Environment.Host || env.Redis == nil && !usesRedis(env) {
			continue
		}
		if db, onShared := sharedDB(name, env); onShared && db == from {
			return 0, fmt.Errorf("environment %s on the same host also uses DB %d on the shared Redis, so migrating %s would copy its keys too; migrate %s first, or pass --allow-shared-db to copy everything in DB %d", name, from, cfg.EnvName, name, from)
		}
	}
	return from, nil
}

// urlPort returns the port in a redis:// URL, 6379 when it has none.
func urlPort(raw string) (int, bool) {
	if raw == "" {
		return 0, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return 0, false
	}
	if p := u.Port(); p != "" {
		port, err := strconv.Atoi(p)
		if err != nil {
			return 0, false
		}
		return port, true
	}
	return 6379, true
}

func usesRedis(env config.Environment) bool {
	for k, v := range env.Env {
		if strings.EqualFold(k, "REDIS_URL") && strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}

// urlDB returns the DB index in a redis:// URL's path.
func urlDB(raw string) (int, bool) {
	if raw == "" {
		return 0, false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return 0, false
	}
	path := strings.Trim(u.Path, "/")
	if path == "" {
		return 0, true
	}
	db, err := strconv.Atoi(path)
	if err != nil || db < 0 {
		return 0, false
	}
	return db, true
}

// buildMigrateCheckScript stops a migration before anything changes when
// there is nothing to migrate.
func buildMigrateCheckScript(l Layout, envName, instance, redisUser string, fromDB int) string {
	vars := instanceVars(l, l.Instance(instance))
	vars["ENV_NAME"] = envName
	vars["REDIS_USER"] = redisUser
	vars["FROM_DB"] = fmt.Sprint(fromDB)
	return "set -euo pipefail\n" + shellVars(vars) + cliSnippet + `
if ! shared_rcli ping 2>/dev/null | grep -qx PONG; then
  echo "cannot reach the shared Redis on port $SHARED_PORT as its admin, so there is nothing to migrate from" >&2
  echo "For an environment that never used it, run: landfall redis bootstrap --env $ENV_NAME --save-secret" >&2
  exit 1
fi
if ! on_shared_redis; then
  echo "user $REDIS_USER does not exist on the shared Redis (port $SHARED_PORT): nothing to migrate" >&2
  echo "Run: landfall redis bootstrap --env $ENV_NAME --save-secret" >&2
  exit 1
fi
echo "Keys to copy from DB $FROM_DB: $(shared_rcli -n "$FROM_DB" DBSIZE)"
`
}

// buildCopyScript copies one DB from the shared Redis into the instance with
// MIGRATE ... COPY REPLACE: binary-safe, keeps TTLs, and leaves the source
// untouched. Keys go in batches; commands reach redis-cli on stdin so the
// instance's admin password never appears in the process list.
func buildCopyScript(l Layout, instance string, fromDB, toPort, toDB int) string {
	vars := instanceVars(l, l.Instance(instance))
	vars["FROM_DB"] = fmt.Sprint(fromDB)
	vars["DEST_PORT"] = fmt.Sprint(toPort)
	vars["DEST_DB"] = fmt.Sprint(toDB)
	vars["REDIS_USER"] = ""
	return "set -euo pipefail\n" + shellVars(vars) + cliSnippet + copyBody
}

const copyBody = `
REDIS_PORT=$DEST_PORT
src() { shared_rcli -n "$FROM_DB" "$@"; }
dest() { rcli -n "$DEST_DB" "$@"; }

BEFORE=$(src DBSIZE)
DEST_PASS=$(sudo cat "$REDIS_ADMIN_FILE")
export DEST_PASS DEST_PORT DEST_DB
# Keys are escaped for redis-cli's quoting (backslash, double quote).
# (No --count: the redis-cli of Redis 6.0 and 7.0 rejects it for --scan.)
OUT=$(src --scan | awk '
  BEGIN {
    pre = "MIGRATE 127.0.0.1 " ENVIRON["DEST_PORT"] " \"\" " ENVIRON["DEST_DB"] " 10000 COPY REPLACE AUTH2 default \"" ENVIRON["DEST_PASS"] "\" KEYS"
  }
  {
    gsub(/\\/, "\\\\"); gsub(/"/, "\\\"")
    keys = keys " \"" $0 "\""
    if (++n == 100) { print pre keys; keys = ""; n = 0 }
  }
  END { if (n > 0) print pre keys }
' | src)
unset DEST_PASS
if printf '%s\n' "$OUT" | grep -qE '^(ERR|IOERR|NOAUTH|WRONGPASS|NOPERM)'; then
  echo "copy failed:" >&2
  printf '%s\n' "$OUT" | grep -E '^(ERR|IOERR|NOAUTH|WRONGPASS|NOPERM)' | head -5 >&2
  exit 1
fi

AFTER=$(src DBSIZE)
COPIED=$(dest DBSIZE)
echo "Copied $COPIED key(s) (shared DB $FROM_DB had $BEFORE before, $AFTER now)"
# Keys can only disappear from the source while the app is stopped (TTLs), and
# they expire in the copy too, so the copy must hold at least what is left.
if [ "$COPIED" -lt "$AFTER" ]; then
  echo "the instance has fewer keys ($COPIED) than the shared DB ($AFTER): some keys could not be copied (names containing a newline?)" >&2
  exit 1
fi
`
