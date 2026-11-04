package templates

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Runs cache-handler.js.example under Node: what Next.js needs from a
// handler (Buffer/Map round trips, tags from ctx and the x-next-cache-tags
// header, revalidateTag, expiry), and that entries survive a restart.
func TestCacheHandlerTemplate(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available")
	}
	src, err := os.ReadFile("cache-handler.js.example")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	handler := filepath.Join(dir, "cache-handler.js")
	if err := os.WriteFile(handler, src, 0o644); err != nil {
		t.Fatal(err)
	}
	cacheDir := filepath.Join(dir, "cache", "landfall-isr")

	run := func(t *testing.T, script string, extraEnv ...string) string {
		t.Helper()
		full := "const assert = require('node:assert/strict');\n" +
			"const Handler = require(" + jsString(handler) + ");\n" +
			"(async () => {\n" + script + "\n})().catch((e) => { console.error(e); process.exit(1) });\n"
		cmd := exec.Command(node, "-e", full)
		cmd.Env = append(os.Environ(), append([]string{"LANDFALL_ISR_CACHE_DIR=" + cacheDir, "NEXT_PHASE="}, extraEnv...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("node: %v\n%s", err, out)
		}
		return string(out)
	}

	// A key shaped like Next.js 16's scoped response keys: slashes, a hash.
	const pageKey = "/route-cache/APP_PAGE/" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" + "/$/blog/post"

	t.Run("round trip and restart", func(t *testing.T) {
		run(t, `
const h = new Handler({});
assert.equal(await h.get('`+pageKey+`'), null);
await h.set('`+pageKey+`', {
  kind: 'APP_PAGE',
  html: '<p>hi</p>',
  rscData: Buffer.from('rsc-bytes'),
  segmentData: new Map([['/blog', Buffer.from('seg')]]),
  headers: { 'x-next-cache-tags': '_N_T_/blog/post,posts' },
  status: 200,
}, { cacheControl: { revalidate: 60, expire: 3600 } });
`)
		// A new process stands in for a restart: the entry is read from disk.
		run(t, `
const got = await new Handler({}).get('`+pageKey+`');
assert.ok(got, 'entry must survive a restart');
assert.equal(got.value.html, '<p>hi</p>');
assert.ok(Buffer.isBuffer(got.value.rscData));
assert.equal(got.value.rscData.toString(), 'rsc-bytes');
assert.ok(got.value.segmentData instanceof Map);
assert.equal(got.value.segmentData.get('/blog').toString(), 'seg');
assert.deepEqual(got.tags.sort(), ['_N_T_/blog/post', 'posts']);
assert.equal(typeof got.lastModified, 'number');
`)
	})

	t.Run("revalidateTag", func(t *testing.T) {
		run(t, `
const h = new Handler({});
await h.set('fetch-key', { kind: 'FETCH', data: { body: 'x' } }, { tags: ['products'] });
assert.ok(await h.get('fetch-key'));
await new Promise((r) => setTimeout(r, 5));
await h.revalidateTag(['products']);
assert.equal(await h.get('fetch-key'), null, 'a revalidated tag must invalidate the entry');
`)
		// The revalidation is persisted too.
		run(t, `assert.equal(await new Handler({}).get('fetch-key'), null);`)
	})

	t.Run("revalidatePath tag from the header", func(t *testing.T) {
		run(t, `
const h = new Handler({});
await h.set('page2', { kind: 'APP_PAGE', html: 'a', headers: { 'x-next-cache-tags': '_N_T_/about' } }, {});
await new Promise((r) => setTimeout(r, 5));
await h.revalidateTag('_N_T_/about');
assert.equal(await h.get('page2'), null);
`)
	})

	t.Run("expiry", func(t *testing.T) {
		run(t, `
const h = new Handler({});
await h.set('short', { kind: 'FETCH', data: {} }, { cacheControl: { expire: 0.001 } });
await new Promise((r) => setTimeout(r, 20));
assert.equal(await h.get('short'), null);
`)
	})

	t.Run("nothing is written during next build", func(t *testing.T) {
		buildDir := filepath.Join(dir, "build-cache")
		run(t, `
const h = new Handler({});
await h.set('k', { kind: 'FETCH', data: {} }, {});
assert.equal(await h.get('k'), null);
`, "NEXT_PHASE=phase-production-build", "LANDFALL_ISR_CACHE_DIR="+buildDir)
		if _, err := os.Stat(buildDir); !os.IsNotExist(err) {
			t.Fatal("the handler must not write during next build")
		}
	})

	t.Run("an unwritable cache never fails the request", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root can write to a read-only directory")
		}
		ro := filepath.Join(dir, "readonly")
		if err := os.MkdirAll(ro, 0o555); err != nil {
			t.Fatal(err)
		}
		out := run(t, `
const h = new Handler({});
await h.set('k', { kind: 'FETCH', data: {} }, {});
assert.equal(await h.get('k'), null);
`, "LANDFALL_ISR_CACHE_DIR="+filepath.Join(ro, "isr"))
		if !strings.Contains(out, "could not write a cache entry") {
			t.Fatalf("expected one warning, got:\n%s", out)
		}
	})
}

func jsString(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}
