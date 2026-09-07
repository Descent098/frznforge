package serve

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// The static file server behind `frznforge dev`.
//
// Two things make it worth this much test. It is the same code the Playwright suite runs
// against (cmd/frznforge/dev.go --dir), so a bug in resolve() does not look like a bug in the
// server — it looks like a bug in the site, in whichever spec happens to notice. And it
// replaces tests/e2e/serve.ts, 54 lines of node that had no tests at all, which is how it came
// to disagree with the site's own raw endpoint about the content type of a .ps1 file.

const (
	homeHTML     = "<!doctype html><title>home</title>\n"
	notFoundHTML = "<!doctype html><title>404 - not found</title>\n"
	alphaHTML    = "<!doctype html><title>alpha</title>\n"
	// outsideSecret sits one directory ABOVE the served root. Nothing the server hands back may
	// ever contain it.
	outsideSecret = "SECRET-ABOVE-THE-ROOT\n"
)

// logoPNG is deliberately not text: a NUL, a high byte and a CRLF, so "served verbatim" is a
// claim about bytes and not about a string that survived a round trip by luck.
var logoPNG = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0xFF, 0xFE, 0x00, 'e', 'n', 'd'}

// siteFiles is the miniature build every test serves. A slice, not a map, so it is written in
// the same order on every run — the rule the build itself follows.
var siteFiles = []struct {
	path string // slash-separated, relative to the served root
	body []byte
}{
	{"index.html", []byte(homeHTML)},
	{"404.html", []byte(notFoundHTML)},
	{"repos/alpha/index.html", []byte(alphaHTML)},
	{"repos/alpha/raw/main/notes.ps1", []byte("Write-Host 'hi'\n")},
	{"repos/alpha/raw/main/a b.md", []byte("# a spaced name\n")},
	{"assets/app.mjs", []byte("export const x = 1;\n")},
	{"logo.png", logoPNG},
	{"sub/deep/index.html", []byte("deep\n")},
	// about and about.html both exist so the resolve order is observable: the exact file has to
	// win, or a raw route serving a committed file named `about` would hand back a page.
	{"about", []byte("the exact file\n")},
	{"about.html", []byte("the .html fallback\n")},
	// guide/ and guide.html likewise: a directory index outranks the .html fallback.
	{"guide/index.html", []byte("the directory index\n")},
	{"guide.html", []byte("the .html fallback\n")},
}

func write(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// newSite writes the fixture build to a temp directory and returns the root to serve.
//
// Two decoys sit outside that root, both shaped like the real thing: a file in the parent, and
// a sibling directory whose name starts with the root's ("dist" and "dist-base" really are
// siblings in tests/.tmp/e2e, one built at the site root and one under /mysite). The second is
// the reason the escape check is `full == root || full has root+separator as a prefix` and not
// a plain string prefix — a prefix check calls dist-base part of dist.
func newSite(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	write(t, filepath.Join(base, "outside.txt"), []byte(outsideSecret))
	write(t, filepath.Join(base, "dist-base", "secret.txt"), []byte(outsideSecret))
	root := filepath.Join(base, "dist")
	for _, f := range siteFiles {
		write(t, filepath.Join(root, filepath.FromSlash(f.path)), f.body)
	}
	return root
}

func mustHandler(t *testing.T, root, base string) http.Handler {
	t.Helper()
	h, err := NewHandler(root, base)
	if err != nil {
		t.Fatalf("NewHandler(%q, %q): %v", root, base, err)
	}
	return h
}

// get drives the handler directly rather than through a client, so no URL normalisation happens
// between the test and the code under test — the traversal cases depend on the raw path
// arriving exactly as written.
func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestServesTheBuiltSite(t *testing.T) {
	h := mustHandler(t, newSite(t), "")
	for _, c := range []struct {
		target string
		status int
		ctype  string
		body   string
		why    string
	}{
		{"/", 200, "text/html; charset=utf-8", homeHTML, "the site root is its index.html"},
		{"/index.html", 200, "text/html; charset=utf-8", homeHTML, "and so is the file itself"},
		{"/repos/alpha/", 200, "text/html; charset=utf-8", alphaHTML, "a directory URL is its index"},
		{"/repos/alpha", 200, "text/html; charset=utf-8", alphaHTML, "and it resolves without the trailing slash too, rather than redirecting"},
		{"/logo.png", 200, "image/png", string(logoPNG), "an asset keeps its own type"},
		{"/assets/app.mjs", 200, "text/javascript; charset=utf-8", "export const x = 1;\n", "a module the browser must be willing to execute"},
		{"/repos/alpha/raw/main/notes.ps1", 200, "text/plain; charset=utf-8", "Write-Host 'hi'\n", "the raw route hands back committed bytes a browser will display, not download"},
		{"/repos/alpha/raw/main/a%20b.md", 200, "text/plain; charset=utf-8", "# a spaced name\n", "percent-encoding is decoded once, to the name the build wrote"},
		{"/nope", 404, "text/html; charset=utf-8", notFoundHTML, "a miss gets the site's own 404 page with a 404 status, as a static host would give"},
		{"/nope.png", 404, "text/html; charset=utf-8", notFoundHTML, "including when the URL asked for an image"},
		{"/index.html/", 404, "text/html; charset=utf-8", notFoundHTML, "a trailing slash means a directory index and nothing else"},
		{"/about.html/", 404, "text/html; charset=utf-8", notFoundHTML, "the same rule for a page that does exist"},
		{"/about/", 404, "text/html; charset=utf-8", notFoundHTML, "and the .html fallback does not apply to a directory URL"},
	} {
		rec := get(t, h, c.target)
		if rec.Code != c.status {
			t.Errorf("GET %s = %d, want %d (%s)", c.target, rec.Code, c.status, c.why)
		}
		if got := rec.Header().Get("Content-Type"); got != c.ctype {
			t.Errorf("GET %s Content-Type = %q, want %q", c.target, got, c.ctype)
		}
		if got := rec.Body.String(); got != c.body {
			t.Errorf("GET %s body = %q, want %q (%s)", c.target, got, c.body, c.why)
		}
	}
}

func TestAnEmptyRequestPathIsTheRoot(t *testing.T) {
	// An absolute-form request line ("GET http://host HTTP/1.1", which proxies and some tools
	// send) parses to an empty path. Without the default it would join to the root directory
	// and 404 the home page.
	h := mustHandler(t, newSite(t), "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example.test", nil))
	if rec.Code != 200 || rec.Body.String() != homeHTML {
		t.Errorf("an empty request path gave %d %q, want 200 and the home page", rec.Code, rec.Body.String())
	}
}

func TestResolutionOrder(t *testing.T) {
	// The order is a contract, not an accident: exact file, then directory index, then the
	// .html a page was written under. Each fixture below exists twice on disk so the winner is
	// observable rather than inferred.
	h := mustHandler(t, newSite(t), "")
	for _, c := range []struct{ target, want, why string }{
		{"/about", "the exact file\n", "a committed file named `about` must not be mistaken for about.html"},
		{"/guide", "the directory index\n", "a directory with an index outranks a same-named page"},
		{"/sub/deep", "deep\n", "the index lookup is not limited to the top level"},
	} {
		if got := get(t, h, c.target).Body.String(); got != c.want {
			t.Errorf("GET %s = %q, want %q (%s)", c.target, got, c.want, c.why)
		}
	}
	// The extensionless fallback, which the node server never had: /404 finds 404.html and
	// serves it with a 200, because the file was found — the status follows the lookup, not the
	// name of the file.
	rec := get(t, h, "/404")
	if rec.Code != 200 || rec.Body.String() != notFoundHTML {
		t.Errorf("GET /404 = %d %q, want 200 and 404.html (an extensionless path finds the page file)", rec.Code, rec.Body.String())
	}
}

func TestNoDirectoryListing(t *testing.T) {
	// http.FileServer would answer this with an index of assets/. Nothing in a build is meant
	// to be browsed that way, and a listing leaks the shape of the output directory.
	h := mustHandler(t, newSite(t), "")
	rec := get(t, h, "/assets/")
	if rec.Code != 404 {
		t.Errorf("GET /assets/ = %d, want 404 for a directory with no index.html", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "app.mjs") {
		t.Errorf("GET /assets/ listed the directory:\n%s", rec.Body.String())
	}
}

func TestServedBytesAreVerbatim(t *testing.T) {
	// The whole promise of `frznforge dev`: the bytes on disk are the bytes the browser gets.
	// Nothing is minified, rewritten or re-encoded, and Content-Length is the file's own size —
	// a client that trusts it (curl, a link checker, HTTP/1.0) must not be told a different
	// number from what arrives.
	root := newSite(t)
	h := mustHandler(t, root, "")
	for _, name := range []string{"logo.png", "index.html", "repos/alpha/raw/main/notes.ps1"} {
		want, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		rec := get(t, h, "/"+name)
		if got := rec.Body.Bytes(); string(got) != string(want) {
			t.Errorf("GET /%s returned %d bytes, want the file's %d, byte for byte", name, len(got), len(want))
		}
		if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(want)) {
			t.Errorf("GET /%s Content-Length = %q, want %d", name, got, len(want))
		}
	}
}

func TestCacheControlIsNoStore(t *testing.T) {
	// A rebuild rewrites every file under the same URLs, so a cached copy is a previous build
	// shown as the current one — the exact confusion the notice exists to prevent, and it would
	// be blamed on the site rather than on the server. There is no revalidation story here
	// worth having: this is a dev server, and the files are on the same disk.
	root := newSite(t)
	h := mustHandler(t, root, "")
	for _, target := range []string{"/", "/logo.png", "/assets/app.mjs", "/nope", "/..%5coutside.txt"} {
		if got := get(t, h, target).Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("GET %s Cache-Control = %q, want no-store", target, got)
		}
	}
	// Including the sub-path miss, which is answered before any file is looked at.
	based := mustHandler(t, root, "/mysite")
	if got := get(t, based, "/").Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("a request outside the deploy base had Cache-Control %q, want no-store", got)
	}
}

func TestHeadReturnsTheHeadersWithoutABody(t *testing.T) {
	root := newSite(t)
	h := mustHandler(t, root, "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/logo.png", nil))

	if rec.Code != 200 {
		t.Fatalf("HEAD /logo.png = %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD /logo.png returned %d bytes of body", rec.Body.Len())
	}
	// The headers a HEAD exists to deliver. Dropping Content-Length here is the failure that
	// makes `curl -I` report a zero-length asset that downloads fine.
	got := get(t, h, "/logo.png")
	for _, header := range []string{"Content-Type", "Content-Length", "Cache-Control"} {
		if rec.Header().Get(header) != got.Header().Get(header) {
			t.Errorf("HEAD %s = %q, GET said %q", header, rec.Header().Get(header), got.Header().Get(header))
		}
	}
	if rec.Header().Get("Content-Length") != strconv.Itoa(len(logoPNG)) {
		t.Errorf("HEAD Content-Length = %q, want the file's %d", rec.Header().Get("Content-Length"), len(logoPNG))
	}
}

func TestOnlyGetAndHeadAreAnswered(t *testing.T) {
	// This server has nothing to write to. Answering a POST with a 200 (or with a file) would
	// invite a browser or a probe to treat it as an endpoint; 405 with an Allow header is what
	// tells the caller what does work.
	//
	// The 405 is the one response without Cache-Control, because it is answered before the
	// header is set. Nothing turns on it: a 405 is not heuristically cacheable.
	h := mustHandler(t, newSite(t), "")
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, "/", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s / = %d, want 405", method, rec.Code)
		}
		if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
			t.Errorf("%s / Allow = %q, want %q", method, got, "GET, HEAD")
		}
		if body := rec.Body.String(); !strings.Contains(body, "GET") || !strings.Contains(body, "HEAD") {
			t.Errorf("%s / body = %q, want it to name the methods that work", method, body)
		}
	}
}

func TestDeployBase(t *testing.T) {
	// `site.base` (0.2.0): under a sub-path deploy the site lives ONLY under the prefix, and
	// everything outside it belongs to whatever else the host serves. Serving the root too
	// would hide exactly the bug tests/e2e/base-path.spec.ts exists to catch — a root-absolute
	// link that escaped the build works locally and 404s in production.
	root := newSite(t)
	for _, c := range []struct {
		name, base, target string
		status             int
		ctype              string
		body               string
	}{
		{"the base itself, no trailing slash", "/mysite", "/mysite", 200, "text/html; charset=utf-8", homeHTML},
		{"the base with a trailing slash", "/mysite", "/mysite/", 200, "text/html; charset=utf-8", homeHTML},
		{"a page under the base", "/mysite", "/mysite/repos/alpha/", 200, "text/html; charset=utf-8", alphaHTML},
		{"an asset under the base", "/mysite", "/mysite/logo.png", 200, "image/png", string(logoPNG)},
		{"a miss under the base still gets the site's 404", "/mysite", "/mysite/nope", 404, "text/html; charset=utf-8", notFoundHTML},
		{"the host root", "/mysite", "/", 404, "text/plain; charset=utf-8", "not under the deploy base /mysite\n"},
		{"a leaked root-absolute link", "/mysite", "/repos/alpha/", 404, "text/plain; charset=utf-8", "not under the deploy base /mysite\n"},
		{"a sibling that merely starts with the letters", "/mysite", "/mysiteX/", 404, "text/plain; charset=utf-8", "not under the deploy base /mysite\n"},
		{"a base written without its leading slash", "mysite", "/mysite/", 200, "text/html; charset=utf-8", homeHTML},
		{"a base written with a trailing slash", "/mysite/", "/mysite/", 200, "text/html; charset=utf-8", homeHTML},
		{"a nested base", "/a/b", "/a/b/repos/alpha/", 200, "text/html; charset=utf-8", alphaHTML},
		{"a nested base, one level short", "/a/b", "/a/", 404, "text/plain; charset=utf-8", "not under the deploy base /a/b\n"},
		{"an empty base serves at the root", "", "/", 200, "text/html; charset=utf-8", homeHTML},
		{"a base of / is the same as none", "/", "/", 200, "text/html; charset=utf-8", homeHTML},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := get(t, mustHandler(t, root, c.base), c.target)
			if rec.Code != c.status {
				t.Errorf("base %q, GET %s = %d, want %d", c.base, c.target, rec.Code, c.status)
			}
			if got := rec.Header().Get("Content-Type"); got != c.ctype {
				t.Errorf("base %q, GET %s Content-Type = %q, want %q", c.base, c.target, got, c.ctype)
			}
			if got := rec.Body.String(); got != c.body {
				t.Errorf("base %q, GET %s body = %q, want %q", c.base, c.target, got, c.body)
			}
		})
	}
}

func TestTheBaseMissIsPlainTextNotTheSite404(t *testing.T) {
	// Deliberately not 404.html: under a sub-path deploy this URL is not the site's to answer,
	// and handing back a frznforge page would make a leaked root-absolute link look like a
	// broken page in the site rather than a link pointing outside it.
	rec := get(t, mustHandler(t, newSite(t), "/mysite"), "/repos/")
	if strings.Contains(rec.Body.String(), "<!doctype") {
		t.Errorf("a request outside the base was answered with an HTML page:\n%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "/mysite") {
		t.Errorf("the message does not name the base the request missed: %q", rec.Body.String())
	}
}

func TestUnderBase(t *testing.T) {
	// The split on its own. "/mysiteX" sharing the prefix's letters is why this is not a plain
	// HasPrefix, and it is the case a rewrite would most easily lose.
	for _, c := range []struct {
		upath, base, rest string
		ok                bool
	}{
		{"/mysite", "/mysite", "/", true},
		{"/mysite/", "/mysite", "/", true},
		{"/mysite/repos/", "/mysite", "/repos/", true},
		{"/mysiteX", "/mysite", "", false},
		{"/mysiteX/repos/", "/mysite", "", false},
		{"/", "/mysite", "", false},
		{"/other/mysite/", "/mysite", "", false},
		{"/a/b/x", "/a/b", "/x", true},
		{"/a/bx", "/a/b", "", false},
	} {
		rest, ok := underBase(c.upath, c.base)
		if ok != c.ok || rest != c.rest {
			t.Errorf("underBase(%q, %q) = %q, %v; want %q, %v", c.upath, c.base, rest, ok, c.rest, c.ok)
		}
	}
}

func TestPathTraversalNeverLeavesTheRoot(t *testing.T) {
	// This server is bound to localhost, but it is the only file server in the project and the
	// check costs one string comparison. Every spelling below is one a client can put on the
	// wire without a browser's help: the decoded forms, the percent-encoded ones (r.URL.Path is
	// already decoded, so %2e%2e arrives as ..), and the backslash — which is inert on POSIX
	// but a path separator to filepath.Join on Windows, and is therefore the one a
	// POSIX-shaped check misses.
	h := mustHandler(t, newSite(t), "")
	for _, target := range []string{
		"/../outside.txt",
		"/../../outside.txt",
		"/sub/../../outside.txt",
		"/sub/deep/../../../outside.txt",
		"/%2e%2e/outside.txt",
		"/%2e%2e%2foutside.txt",
		"/%2e%2e%2f%2e%2e%2foutside.txt",
		"/..%5coutside.txt",
		"/..%5c..%5coutside.txt",
		"/sub%2f..%2f..%2foutside.txt",
		"/....//outside.txt",
		// The sibling directory, which is what tests/e2e/serve.ts got wrong: its check was
		// `file.startsWith(root)`, and "…/dist-base/secret.txt" starts with "…/dist".
		"/../dist-base/secret.txt",
		"/..%5cdist-base%5csecret.txt",
	} {
		rec := get(t, h, target)
		if rec.Code == http.StatusOK {
			t.Errorf("GET %s = 200 — the request resolved to something outside dist/", target)
		}
		if strings.Contains(rec.Body.String(), outsideSecret) {
			t.Errorf("GET %s served the file above the served root", target)
		}
	}

	// The counterpart, so the loop above cannot pass by refusing everything: a `..` that stays
	// inside the root is still served.
	if got := get(t, h, "/sub/deep/../deep/").Body.String(); got != "deep\n" {
		t.Errorf("GET /sub/deep/../deep/ = %q, want the page — a contained .. is not an attack", got)
	}
	if got := get(t, h, "/repos/alpha/raw/main/../main/notes.ps1").Body.String(); got != "Write-Host 'hi'\n" {
		t.Errorf("a contained .. inside a raw path was refused: %q", got)
	}

	// And the same through a deploy base, which strips its prefix before any of this runs.
	based := mustHandler(t, newSite(t), "/mysite")
	for _, target := range []string{"/mysite/../outside.txt", "/mysite/%2e%2e%2foutside.txt"} {
		if body := get(t, based, target).Body.String(); strings.Contains(body, outsideSecret) {
			t.Errorf("GET %s escaped through the base prefix", target)
		}
	}
}

func TestWindowsBackslashIsRefusedOutright(t *testing.T) {
	if runtime.GOOS != "windows" {
		// A backslash is an ordinary filename character here, so the request is a miss, not an
		// escape — covered by the loop above. The 403 branch only exists for the platform where
		// filepath.Join would act on it.
		t.Skip("the backslash is only a path separator on Windows")
	}
	rec := get(t, mustHandler(t, newSite(t), ""), "/..%5coutside.txt")
	if rec.Code != http.StatusForbidden {
		t.Errorf("GET /..%%5coutside.txt = %d, want 403 — this is the one path that reaches the escape check", rec.Code)
	}
}

func TestSiteWithNo404Page(t *testing.T) {
	// A directory that is not a frznforge build (or a build that was interrupted). The
	// preflight normally catches it first, so the only requirement here is that a miss still
	// says 404 rather than crashing or serving nothing.
	root := t.TempDir()
	write(t, filepath.Join(root, "index.html"), []byte(homeHTML))
	rec := get(t, mustHandler(t, root, ""), "/nope")
	if rec.Code != 404 {
		t.Errorf("GET /nope = %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q, want plain text when there is no 404.html to serve", got)
	}
	if !strings.Contains(rec.Body.String(), "not found") {
		t.Errorf("body = %q, want it to say something", rec.Body.String())
	}
}

func TestListenServesOverARealSocket(t *testing.T) {
	root := newSite(t)
	srv, err := Listen(Options{Dir: root, Base: "/mysite", Port: 0})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	// Port 0 is what the e2e harness passes, and the printed URL is the only way anything finds
	// the server afterwards. So it has to be complete before Serve blocks: scheme, the port the
	// OS actually chose, the deploy base, and the trailing slash that makes it paste-able.
	if !strings.HasSuffix(srv.URL, "/mysite/") {
		t.Errorf("URL = %q, want it to end with the deploy base and a slash", srv.URL)
	}
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("URL %q does not parse: %v", srv.URL, err)
	}
	if u.Port() == "0" || u.Port() == "" {
		t.Fatalf("URL = %q, want the port the OS chose, not the one that was asked for", srv.URL)
	}
	if srv.Dir != filepath.Clean(root) {
		t.Errorf("Dir = %q, want the absolute served directory %q", srv.Dir, filepath.Clean(root))
	}

	served := make(chan error, 1)
	go func() { served <- srv.Serve() }()

	res, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET %s: %v", srv.URL, err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || string(body) != homeHTML {
		t.Errorf("GET %s = %d %q, want 200 and the home page", srv.URL, res.StatusCode, body)
	}
	if got := res.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control over the wire = %q, want no-store", got)
	}

	// HEAD end to end, where net/http — not the handler — is responsible for suppressing the
	// body. Content-Length still has to be the file's size.
	head, err := http.Head(srv.URL)
	if err != nil {
		t.Fatalf("HEAD %s: %v", srv.URL, err)
	}
	head.Body.Close()
	if head.ContentLength != int64(len(homeHTML)) {
		t.Errorf("HEAD Content-Length = %d, want %d", head.ContentLength, len(homeHTML))
	}

	if err := srv.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	// Ctrl-C is how this command normally ends, so a closed server must not be reported as a
	// failure — `frznforge dev` would exit non-zero every single time.
	if err := <-served; err != nil {
		t.Errorf("Serve after Close = %v, want nil", err)
	}
}

func TestListenSaysWhatToDoWhenThePortIsTaken(t *testing.T) {
	root := newSite(t)
	first, err := Listen(Options{Dir: root, Port: 0})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	served := make(chan error, 1)
	go func() { served <- first.Serve() }()
	defer func() {
		first.Close()
		<-served
	}()

	u, err := url.Parse(first.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}

	_, err = Listen(Options{Dir: root, Port: port})
	if err == nil {
		t.Fatal("a second server bound a port that was already in use")
	}
	// This is what a second `frznforge dev` in another terminal prints. "address already in
	// use" on its own leaves the reader to work out that there is a flag for it.
	msg := err.Error()
	for _, want := range []string{"--port", strconv.Itoa(port)} {
		if !strings.Contains(msg, want) {
			t.Errorf("the busy-port error does not mention %q; it reads:\n%s", want, msg)
		}
	}
}
