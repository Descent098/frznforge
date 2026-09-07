package wizard

// The wizard server, ported from tests/unit/web-init.test.ts.
//
// The real net/http server is bound on port 0 and driven over real HTTP, because the whole
// point of this half of the package is the request handling: the session key, the Host/Origin
// pinning, and the fact that a provider token can never come back out. A stubbed
// http.RoundTripper stands in for the provider, so nothing here touches a socket it did not
// open itself.
//
// One property is asserted on EVERY request rather than in a test of its own, because it is the
// one that must never lapse anywhere: no response may contain the sentinel token, headers
// included. newResponse checks it before a test can look at the answer, so an endpoint added
// later is covered by the first test that calls it.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"frznforge/internal/ingest"
)

// sentinelToken is a value that must never appear in anything the server sends to the browser.
const sentinelToken = "ghp_SENTINEL_never_leaves_the_process_0123456789"

// fixtureConfig is the config nearly every test edits: comment-heavy, awkwardly formatted, and
// valid. Its comments are what the splices have to survive, so they are load-bearing fixtures
// rather than decoration.
const fixtureConfig = `// frznforge site configuration.
{
  "site": {
    "title": "My Forge" // shown in the sidebar
  },

  "owner": { "name": "Kieran Wood", "handle": "kieran" },

  /*
   * Repositories to ingest.
   *   { "type": "local", "path": "../useful" },
   */
  "repos": [
    // Self-host demo: the frznforge repo itself.
    { "type": "local", "path": ".", "slug": "frznforge" },
    { "type": "github", "owner": "me", "repo": "old" }
  ],

  "organizations": [
    { "slug": "cc", "name": "Canadian Coding" }
  ],

  "hosting": {
    "sites": [
      { "repo": "frznforge", "branch": "gh-pages" }
    ]
  },

  "ingest": {
    "maxBlobBytes"  :  524288, // half a meg, and the spacing is the author's
    "outDir": "./data" // keep this comment
  }
}
`

// brokenConfig parses as JSON but fails the schema — the deterministic way to exercise every
// "the config does not load" path.
const brokenConfig = `{
  "owner": { "name": "", "handle": "Not A Slug!" }
}
`

// githubPage is the provider listing the stub answers with: one plain repo, one fork, one
// archived private repo, deliberately out of alphabetical order so the sort is doing work.
const githubPage = `[
  { "name": "ezcv", "full_name": "me/ezcv", "owner": { "login": "me" }, "description": "Static CV generator" },
  { "name": "sdu", "full_name": "me/sdu", "owner": { "login": "me" }, "description": "Disk usage", "fork": true },
  { "name": "attic", "full_name": "me/attic", "owner": { "login": "me" }, "description": null, "archived": true, "private": true }
]`

/* ------------------------------------------------------------------ the provider stub */

// providerCall is one outbound request, recorded so a test can see exactly which host was
// offered the token.
type providerCall struct {
	url          string
	auth         string
	privateToken string
}

// stubProvider answers an account listing from memory and 404s everything else. It is a
// RoundTripper rather than a test server so the wizard can be asked for
// `https://api.github.com` by name — the host is what the token-trust rules turn on, so a test
// that had to rewrite it would not be testing them.
type stubProvider struct {
	mu    sync.Mutex
	calls []providerCall
	page  string
}

func (p *stubProvider) RoundTrip(req *http.Request) (*http.Response, error) {
	p.mu.Lock()
	p.calls = append(p.calls, providerCall{
		url:          req.URL.String(),
		auth:         req.Header.Get("authorization"),
		privateToken: req.Header.Get("private-token"),
	})
	p.mu.Unlock()

	body, status := `{"message":"Not Found"}`, http.StatusNotFound
	if strings.Contains(req.URL.Path, "/users/me/repos") || strings.Contains(req.URL.Path, "/users/me/projects") {
		body, status = p.page, http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

func (p *stubProvider) seen() []providerCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]providerCall(nil), p.calls...)
}

/* ------------------------------------------------------------------ the harness */

type wizardOptions struct {
	// config is the config file's contents; "" writes no config file at all.
	config string
	// env replaces the default single-GitHub-token environment.
	env ingest.Env
	// provider and host are the terminal's own --provider/--host flags.
	provider string
	host     string
	// page replaces the provider listing the stub answers with.
	page string
}

// wizardRun is one running wizard over its own temp directory.
type wizardRun struct {
	t      *testing.T
	dir    string
	config string
	stub   *stubProvider
	out    *bytes.Buffer

	session *Session
	origin  string
	key     string
	client  *http.Client

	served  chan error
	stopped bool
	err     error
}

// backupInstant is the fixed clock every session runs on, so a backup's name is the name the
// test asserts rather than whatever second the suite happened to reach.
var backupInstant = time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)

func startWizard(t *testing.T, o wizardOptions) *wizardRun {
	t.Helper()
	dir := t.TempDir()
	configPath := ""
	if o.config != "" {
		configPath = filepath.Join(dir, "frznforge.config.jsonc")
		if err := os.WriteFile(configPath, []byte(o.config), 0o644); err != nil {
			t.Fatalf("write the fixture config: %v", err)
		}
	}
	env := o.env
	if env == nil {
		env = ingest.Env{"FRZNFORGE_GITHUB_TOKEN": sentinelToken}
	}
	page := o.page
	if page == "" {
		page = githubPage
	}
	stub := &stubProvider{page: page}
	out := &bytes.Buffer{}

	session, err := Listen(Options{
		Root: dir, Port: 0, NoOpen: true, Out: out,
		Provider: o.provider, Host: o.host,
		Env:    env,
		Client: &http.Client{Transport: stub},
		Now:    func() time.Time { return backupInstant },
	})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	parsed, err := url.Parse(session.URL)
	if err != nil {
		t.Fatalf("the wizard printed a URL that does not parse (%q): %v", session.URL, err)
	}

	r := &wizardRun{
		t: t, dir: dir, config: configPath, stub: stub, out: out,
		session: session,
		origin:  "http://" + parsed.Host,
		key:     parsed.Query().Get("s"),
		// Keep-alives stay ON: /api/done's `Connection: close` only means anything against a
		// client that would otherwise have reused the socket.
		client: &http.Client{Transport: &http.Transport{}, Timeout: 20 * time.Second},
		served: make(chan error, 1),
	}
	if r.key == "" {
		t.Fatalf("the printed URL carries no session key: %q", session.URL)
	}
	go func() { r.served <- session.Serve() }()

	t.Cleanup(func() {
		r.client.CloseIdleConnections()
		if r.stopped {
			return
		}
		_ = session.Close()
		r.wait()
	})
	return r
}

// wait blocks until Serve returns, and reports what it returned.
func (r *wizardRun) wait() error {
	r.t.Helper()
	if r.stopped {
		return r.err
	}
	r.stopped = true
	select {
	case r.err = <-r.served:
	case <-time.After(15 * time.Second):
		r.t.Fatal("the wizard did not stop within 15s")
	}
	return r.err
}

/* ------------------------------------------------------------------ requests */

type response struct {
	t      *testing.T
	status int
	header http.Header
	body   string
	// closed is the client's reading of `Connection: close`. net/http strips that header from
	// Response.Header as it processes it — a hop-by-hop header never reaches the caller — so
	// Response.Close is the only place the answer's instruction survives.
	closed bool
}

// newResponse reads a response and, before a test can look at it, asserts that nothing in it —
// header or body — carries the provider token. Doing it here rather than per-test means every
// endpoint the suite touches is covered, including the ones added later.
func newResponse(t *testing.T, res *http.Response) *response {
	t.Helper()
	raw, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		t.Fatalf("read the response body: %v", err)
	}
	out := &response{t: t, status: res.StatusCode, header: res.Header, body: string(raw), closed: res.Close}
	if strings.Contains(out.full(), sentinelToken) {
		t.Fatalf("the provider token reached the browser:\n%s", out.full())
	}
	return out
}

// full is everything a response could hide a secret in: every header, plus the body.
func (r *response) full() string {
	var b strings.Builder
	for name, values := range r.header {
		for _, v := range values {
			fmt.Fprintf(&b, "%s: %s\n", name, v)
		}
	}
	b.WriteString("\n")
	b.WriteString(r.body)
	return b.String()
}

// json decodes the body, failing with it when it is not the JSON object the page expects.
func (r *response) json() map[string]any {
	r.t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(r.body), &out); err != nil {
		r.t.Fatalf("the answer is not a JSON object (%v):\n%s", err, r.body)
	}
	return out
}

func (r *response) wantStatus(want int, what string) *response {
	r.t.Helper()
	if r.status != want {
		r.t.Fatalf("%s: status %d, want %d — body was:\n%s", what, r.status, want, r.body)
	}
	return r
}

func (r *wizardRun) do(req *http.Request) *response {
	r.t.Helper()
	res, err := r.client.Do(req)
	if err != nil {
		r.t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	return newResponse(r.t, res)
}

func (r *wizardRun) getWithKey(path, key string) *response {
	r.t.Helper()
	req, err := http.NewRequest(http.MethodGet, r.origin+path+"?s="+url.QueryEscape(key), nil)
	if err != nil {
		r.t.Fatal(err)
	}
	return r.do(req)
}

func (r *wizardRun) get(path string) *response { return r.getWithKey(path, r.key) }

func (r *wizardRun) post(path string, body any) *response {
	r.t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		r.t.Fatalf("encode the request body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, r.origin+path+"?s="+url.QueryEscape(r.key), bytes.NewReader(encoded))
	if err != nil {
		r.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	return r.do(req)
}

// finish presses Done or Cancel and waits for the server to stop, asserting it stopped cleanly.
func (r *wizardRun) finish(path string) map[string]any {
	r.t.Helper()
	body := r.post(path, map[string]any{}).wantStatus(http.StatusOK, path).json()
	if err := r.wait(); err != nil {
		r.t.Fatalf("%s: the session ended with an error: %v", path, err)
	}
	return body
}

// configText is the config file as it stands on disk right now.
func (r *wizardRun) configText() string {
	r.t.Helper()
	raw, err := os.ReadFile(r.config)
	if err != nil {
		r.t.Fatalf("read back the config: %v", err)
	}
	return string(raw)
}

// backups lists the .bak files in a directory, which is how "one backup per file per session"
// is checked.
func (r *wizardRun) backups(dir string) []string {
	r.t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		r.t.Fatalf("read %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".bak") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

// at walks a decoded answer by key path, naming the step it got lost on.
func at(t *testing.T, body map[string]any, path ...string) any {
	t.Helper()
	var value any = body
	for i, key := range path {
		obj, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("%s is not an object, so %s cannot be read", strings.Join(path[:i], "."), strings.Join(path, "."))
		}
		value, ok = obj[key]
		if !ok {
			t.Fatalf("the answer has no %s", strings.Join(path[:i+1], "."))
		}
	}
	return value
}

func wantEqual(t *testing.T, got, want any, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %#v, want %#v", what, got, want)
	}
}

/* ------------------------------------------------------------------ the guards, in isolation */

func TestHostAllowed(t *testing.T) {
	// The DNS-rebinding guard: a name that resolves to 127.0.0.1 still arrives carrying ITS own
	// Host header, so only the loopback names of the port we bound are accepted.
	cases := []struct {
		header string
		want   bool
	}{
		{"127.0.0.1:4321", true},
		{"localhost:4321", true},
		{"[::1]:4321", true},
		{"127.0.0.1:9999", false},
		{"evil.example", false},
		{"evil.example:4321", false},
		{"", false},
	}
	for _, c := range cases {
		if got := hostAllowed(c.header, 4321); got != c.want {
			t.Errorf("hostAllowed(%q, 4321) = %v, want %v", c.header, got, c.want)
		}
	}
}

func TestOriginAllowed(t *testing.T) {
	// A typed-in URL sends no Origin at all, so absent has to pass; a foreign one never does.
	cases := []struct {
		header string
		want   bool
	}{
		{"", true},
		{"null", true},
		{"http://127.0.0.1:4321", true},
		{"http://localhost:4321", true},
		{"https://evil.example", false},
		{"http://127.0.0.1:4322", false},
	}
	for _, c := range cases {
		if got := originAllowed(c.header, 4321); got != c.want {
			t.Errorf("originAllowed(%q, 4321) = %v, want %v", c.header, got, c.want)
		}
	}
}

func TestHostTrustedForToken(t *testing.T) {
	// The decision is pinned to the HOST, and to the provider that host was authorised for.
	// Without the provider half, `--provider=gitea --host=https://intranet` would let the page
	// switch to GitHub and post the GitHub token to that same intranet box.
	cases := []struct {
		what                 string
		provider, host       string
		cliProvider, cliHost string
		want                 bool
	}{
		{"the provider's own host, trailing slash and all", "github", "https://api.github.com/", "", "", true},
		{"the provider's own host, differently cased", "github", "https://API.GitHub.com", "", "", true},
		{"a lookalike domain", "github", "https://api.github.com.evil.example", "", "", false},
		{"a self-hosted provider with no --host", "gitea", "https://git.example", "", "", false},
		{"the --host the user named, for that provider", "gitea", "https://git.example", "gitea", "https://git.example", true},
		{"the --host the user named, for a DIFFERENT provider", "github", "https://git.example", "gitea", "https://git.example", false},
	}
	for _, c := range cases {
		if got := hostTrustedForToken(c.provider, c.host, c.cliProvider, c.cliHost); got != c.want {
			t.Errorf("%s: hostTrustedForToken(%q, %q, %q, %q) = %v, want %v",
				c.what, c.provider, c.host, c.cliProvider, c.cliHost, got, c.want)
		}
	}
}

/* ------------------------------------------------------------------ the session key */

func TestServesThePageToTheURLItPrinted(t *testing.T) {
	r := startWizard(t, wizardOptions{})
	res := r.get("/").wantStatus(http.StatusOK, "GET /")
	if ct := res.header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	for _, want := range []string{"frznforge init", "Load repositories"} {
		if !strings.Contains(res.body, want) {
			t.Errorf("the served page does not contain %q", want)
		}
	}
	// A page that can reach nothing but its own server.
	csp := res.header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "connect-src 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("Content-Security-Policy %q is missing %q", csp, want)
		}
	}
	// The standing headers, on every answer.
	wantEqual(t, res.header.Get("Cache-Control"), "no-store", "Cache-Control")
	wantEqual(t, res.header.Get("X-Content-Type-Options"), "nosniff", "X-Content-Type-Options")
	wantEqual(t, res.header.Get("Referrer-Policy"), "no-referrer", "Referrer-Policy")

	r.finish("/api/cancel")
}

// everyEndpoint is every path the server dispatches. The session-key guard runs before routing,
// so all of them are listed here rather than the two a spot check would cover.
var everyEndpoint = []string{
	"/", "/index.html",
	"/api/context", "/api/config", "/api/profile",
	"/api/repos", "/api/preview", "/api/write", "/api/config/write",
	"/api/profile/preview", "/api/profile/write", "/api/upload",
	"/api/done", "/api/cancel",
}

func TestRefusesEveryEndpointWithoutTheSessionKey(t *testing.T) {
	// Loopback is reachable by a fetch() from any site the user has open. The key is the only
	// thing that makes that fail, so it gates the page as well as the API — and, because the
	// guard runs before routing, a keyless /api/done cannot stop the server either.
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	for _, target := range everyEndpoint {
		method := http.MethodPost
		if target == "/" || target == "/index.html" {
			method = http.MethodGet
		}
		req, err := http.NewRequest(method, r.origin+target, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		none := r.do(req).wantStatus(http.StatusForbidden, "no key at "+target)
		if ct := none.header.Get("Content-Type"); !strings.Contains(ct, "text/plain") {
			t.Errorf("%s without a key: Content-Type = %q, want text/plain", target, ct)
		}
		if !strings.Contains(none.body, "session key") {
			t.Errorf("%s without a key: the refusal does not say what is missing: %q", target, none.body)
		}
		r.getWithKey(target, "not-the-key").wantStatus(http.StatusForbidden, "a wrong key at "+target)
	}
	// A wrong key of exactly the right length must fail too — the compare is constant-time, not
	// a length check that returns early.
	r.getWithKey("/api/context", strings.Repeat("x", len(r.key))).
		wantStatus(http.StatusForbidden, "a wrong key of the right length")

	// Nothing above got through: the session is still up and has written nothing.
	r.get("/api/context").wantStatus(http.StatusOK, "the session after fourteen refused requests")
	wantFile(t, r.configText(), fixtureConfig, "the config after fourteen refused requests")
	wantEqual(t, r.finish("/api/cancel")["writes"], float64(0), "writes")
}

func TestRefusesAForeignHostHeaderEvenWithAValidKey(t *testing.T) {
	r := startWizard(t, wizardOptions{})
	req, err := http.NewRequest(http.MethodGet, r.origin+"/?s="+r.key, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "evil.example"
	res := r.do(req).wantStatus(http.StatusForbidden, "a rebound DNS name")
	if !strings.Contains(res.body, "127.0.0.1") {
		t.Errorf("the refusal does not say where the server does answer: %q", res.body)
	}

	// localhost is the same interface under another name, and has to keep working.
	ok, err := http.NewRequest(http.MethodGet, r.origin+"/?s="+r.key, nil)
	if err != nil {
		t.Fatal(err)
	}
	ok.Host = "localhost:" + fmt.Sprint(r.session.port)
	r.do(ok).wantStatus(http.StatusOK, "Host: localhost")

	r.finish("/api/cancel")
}

func TestRefusesAForeignOriginOnTheAPIEvenWithAValidKey(t *testing.T) {
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	body, _ := json.Marshal(map[string]any{"provider": "github", "account": "me"})
	req, err := http.NewRequest(http.MethodPost, r.origin+"/api/repos?s="+r.key, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")
	res := r.do(req).wantStatus(http.StatusForbidden, "a cross-origin POST")
	if !strings.Contains(res.body, "cross-origin") {
		t.Errorf("the refusal does not say why: %q", res.body)
	}
	if calls := r.stub.seen(); len(calls) != 0 {
		t.Errorf("a refused request still reached the provider: %+v", calls)
	}

	r.finish("/api/cancel")
}

func TestRefusesUnknownPathsAndWrongMethods(t *testing.T) {
	r := startWizard(t, wizardOptions{})
	res := r.post("/api/nope", map[string]any{}).wantStatus(http.StatusNotFound, "an unknown endpoint")
	if !strings.Contains(res.body, "/api/nope") {
		t.Errorf("the 404 does not name the path that was asked for: %q", res.body)
	}
	r.get("/api/write").wantStatus(http.StatusMethodNotAllowed, "GET on a POST-only endpoint")
	r.finish("/api/cancel")
}

/* ------------------------------------------------------------------ /api/context */

func TestContextNamesTheTokenVariableButNeverCarriesTheToken(t *testing.T) {
	r := startWizard(t, wizardOptions{})
	body := r.get("/api/context").wantStatus(http.StatusOK, "GET /api/context").json()

	order, _ := body["order"].([]any)
	if len(order) != 4 || order[0] != "github" || order[1] != "gitlab" || order[2] != "gitea" || order[3] != "forgejo" {
		t.Errorf("order = %v, want the four providers in display order", order)
	}
	github := at(t, body, "providers", "github", "token").(map[string]any)
	names, _ := github["names"].([]any)
	if len(names) != 2 || names[0] != "FRZNFORGE_GITHUB_TOKEN" || names[1] != "GITHUB_TOKEN" {
		t.Errorf("github token names = %v, want both variables in order", names)
	}
	wantEqual(t, github["from"], "FRZNFORGE_GITHUB_TOKEN", "github token.from")
	wantEqual(t, github["found"], true, "github token.found")
	if _, present := github["token"]; present {
		t.Error("the token report carries the token itself")
	}
	wantEqual(t, at(t, body, "providers", "gitlab", "token", "found"), false, "gitlab token.found")
	wantEqual(t, at(t, body, "providers", "gitea", "hostRequired"), true, "gitea hostRequired")
	wantEqual(t, at(t, body, "providers", "github", "defaultHost"), "https://api.github.com", "github defaultHost")
	// Nothing in the temp directory yet, and null rather than "" so the page's `if (!configPath)`
	// sees one kind of nothing.
	wantEqual(t, body["configPath"], nil, "configPath")
	wantEqual(t, body["configName"], nil, "configName")
	wantEqual(t, at(t, body, "defaults", "provider"), "github", "defaults.provider")
	wantEqual(t, at(t, body, "defaults", "releases"), "provider", "defaults.releases")

	r.finish("/api/cancel")
}

func TestContextReportsTheResolvedConfigPath(t *testing.T) {
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	body := r.get("/api/context").json()
	wantEqual(t, body["configPath"], r.config, "configPath")
	wantEqual(t, body["configName"], "frznforge.config.jsonc", "configName")
	r.finish("/api/cancel")
}

func TestContextOffersTheTerminalsOwnFlagsAsStartingValues(t *testing.T) {
	r := startWizard(t, wizardOptions{provider: "gitea", host: "https://git.example"})
	body := r.get("/api/context").json()
	wantEqual(t, at(t, body, "defaults", "provider"), "gitea", "defaults.provider")
	wantEqual(t, at(t, body, "defaults", "host"), "https://git.example", "defaults.host")
	r.finish("/api/cancel")
}

/* ------------------------------------------------------------------ /api/repos */

func TestReposReturnsTheListingSortedWithTheFlagsTheTableNeeds(t *testing.T) {
	r := startWizard(t, wizardOptions{})
	body := r.post("/api/repos", map[string]any{
		"provider": "github", "host": "https://api.github.com", "account": "me",
	}).wantStatus(http.StatusOK, "POST /api/repos").json()

	wantEqual(t, body["hostLabel"], "api.github.com", "hostLabel")
	repos, _ := body["repos"].([]any)
	if len(repos) != 3 {
		t.Fatalf("repos = %v, want the three the provider listed", repos)
	}
	var names []string
	for _, repo := range repos {
		names = append(names, repo.(map[string]any)["fullName"].(string))
	}
	// Sorted by full name, so the page's table is stable between loads.
	if strings.Join(names, ",") != "me/attic,me/ezcv,me/sdu" {
		t.Errorf("repos = %v, want them sorted by full name", names)
	}
	attic := repos[0].(map[string]any)
	wantEqual(t, attic["name"], "attic", "repos[0].name")
	wantEqual(t, attic["archived"], true, "repos[0].archived")
	wantEqual(t, attic["private"], true, "repos[0].private")
	wantEqual(t, attic["fork"], false, "repos[0].fork")
	sdu := repos[2].(map[string]any)
	wantEqual(t, sdu["fork"], true, "repos[2].fork")
	wantEqual(t, sdu["description"], "Disk usage", "repos[2].description")
	// GitHub list items answer all three questions, so every quick filter is usable.
	for _, filter := range []string{"fork", "archived", "private"} {
		wantEqual(t, at(t, body, "flags", filter), true, "flags."+filter)
	}

	r.finish("/api/cancel")
}

func TestReposTurnsAProviderFailureIntoA502(t *testing.T) {
	r := startWizard(t, wizardOptions{})
	res := r.post("/api/repos", map[string]any{
		"provider": "github", "host": "https://api.github.com", "account": "nobody",
	}).wantStatus(http.StatusBadGateway, "an account the provider does not have")
	if !strings.Contains(res.body, "404") {
		t.Errorf("the failure does not say what the provider answered: %q", res.body)
	}
	r.finish("/api/cancel")
}

func TestReposRejectsAnAccountThatCouldNotBeAPathSegment(t *testing.T) {
	r := startWizard(t, wizardOptions{})
	for _, account := range []string{"../../etc/passwd\n", "", "me/../..", "me name"} {
		r.post("/api/repos", map[string]any{"provider": "github", "account": account}).
			wantStatus(http.StatusBadRequest, fmt.Sprintf("account %q", account))
	}
	if calls := r.stub.seen(); len(calls) != 0 {
		t.Errorf("a refused account still reached the provider: %+v", calls)
	}
	r.finish("/api/cancel")
}

/* ------------------------------------------------------------------ /api/preview */

func TestPreviewRendersExactlyTheSnippetAWriteWouldSplice(t *testing.T) {
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	r.post("/api/repos", map[string]any{"provider": "github", "host": "https://api.github.com", "account": "me"}).
		wantStatus(http.StatusOK, "load the listing")
	body := r.post("/api/preview", map[string]any{
		"provider": "github", "host": "https://api.github.com", "account": "me",
		"releases": "provider", "select": []string{"me/ezcv"},
	}).wantStatus(http.StatusOK, "POST /api/preview").json()

	want := "  \"repos\": [\n    { \"type\": \"github\", \"owner\": \"me\", \"repo\": \"ezcv\", \"releases\": \"provider\" },\n  ],"
	wantEqual(t, body["snippet"], want, "snippet")
	wantEqual(t, body["added"], float64(1), "added")
	wantEqual(t, body["changed"], true, "changed")
	// A preview writes nothing.
	wantEqual(t, r.configText(), fixtureConfig, "the config after a preview")

	r.finish("/api/cancel")
}

func TestPreviewRefusesASelectionWhoseListingWasNeverLoaded(t *testing.T) {
	// A selection is resolved against the listing THIS session loaded. Resolving it against a
	// different one would quietly pick different repositories than the user clicked.
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	res := r.post("/api/preview", map[string]any{
		"provider": "github", "host": "https://api.github.com", "account": "me",
		"releases": "provider", "select": []string{"me/ezcv"},
	}).wantStatus(http.StatusBadRequest, "a selection with no listing behind it")
	if !strings.Contains(res.body, "load it again") {
		t.Errorf("the refusal does not tell the page what to do: %q", res.body)
	}
	r.finish("/api/cancel")
}

/* ------------------------------------------------------------------ /api/write */

func TestWriteSplicesEntriesInKeepsABackupAndIsIdempotent(t *testing.T) {
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	r.post("/api/repos", map[string]any{"provider": "github", "host": "https://api.github.com", "account": "me"}).
		wantStatus(http.StatusOK, "load the listing")
	body := r.post("/api/write", map[string]any{
		"provider": "github", "host": "https://api.github.com", "account": "me",
		"releases": "provider", "select": []string{"me/ezcv", "me/sdu"},
	}).wantStatus(http.StatusOK, "POST /api/write").json()
	wantEqual(t, body["added"], float64(2), "added")
	wantEqual(t, body["configPath"], r.config, "configPath")

	// The whole file, byte for byte: two lines added inside the array and nothing else moved.
	written := r.configText()
	want := strings.Replace(fixtureConfig,
		"    { \"type\": \"github\", \"owner\": \"me\", \"repo\": \"old\" }\n",
		"    { \"type\": \"github\", \"owner\": \"me\", \"repo\": \"old\" },\n"+
			"    { \"type\": \"github\", \"owner\": \"me\", \"repo\": \"ezcv\", \"releases\": \"provider\" },\n"+
			"    { \"type\": \"github\", \"owner\": \"me\", \"repo\": \"sdu\", \"releases\": \"provider\" },\n", 1)
	wantFile(t, written, want, "the config after a picker write")

	// The backup is the file exactly as it was, next to it, named for the session's clock.
	backup, _ := body["backup"].(string)
	if got := filepath.Dir(backup); got != r.dir {
		t.Errorf("the backup landed in %s, want %s", got, r.dir)
	}
	wantEqual(t, filepath.Base(backup), "frznforge.config.jsonc.20240115T103000Z.bak", "the backup's name")
	raw, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("read the backup: %v", err)
	}
	wantFile(t, string(raw), fixtureConfig, "the backup's contents")

	// Re-splicing the same entries adds nothing: the write is idempotent.
	again, ok := insertRepos(written, []RepoEntry{
		{Type: "github", Owner: "me", Repo: "ezcv", Releases: "provider"},
		{Type: "github", Owner: "me", Repo: "sdu", Releases: "provider"},
	})
	if !ok {
		t.Fatal("the written file no longer has a repos array to splice into")
	}
	if again.changed {
		t.Error("re-splicing the same entries changed the file")
	}
	wantFile(t, again.text, written, "a second splice of the same entries")

	wantEqual(t, r.finish("/api/done")["writes"], float64(1), "writes")
}

func TestWriteAcceptsPreBuiltEntriesAsWellAsASelection(t *testing.T) {
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	r.post("/api/write", map[string]any{"entries": []any{map[string]any{
		"type": "forgejo", "host": "https://codeberg.org", "owner": "me", "repo": "tool", "releases": "tags",
	}}}).wantStatus(http.StatusOK, "POST /api/write with entries")
	wantContains(t, r.configText(),
		`{ "type": "forgejo", "host": "https://codeberg.org", "owner": "me", "repo": "tool", "releases": "tags" },`,
		"the written entry")
	r.finish("/api/done")
}

func TestWriteNeverWritesAPathTheBrowserChose(t *testing.T) {
	// The browser never names a file: every path written is the one the terminal resolved.
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	decoy := filepath.Join(r.dir, "decoy.jsonc")
	if err := os.WriteFile(decoy, []byte(fixtureConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	body := r.post("/api/write", map[string]any{
		"configPath": decoy,
		"file":       decoy,
		"entries":    []any{map[string]any{"type": "github", "owner": "me", "repo": "ezcv"}},
	}).wantStatus(http.StatusOK, "POST /api/write naming another file").json()
	wantEqual(t, body["configPath"], r.config, "configPath")

	raw, err := os.ReadFile(decoy)
	if err != nil {
		t.Fatal(err)
	}
	wantFile(t, string(raw), fixtureConfig, "the file the browser named")
	wantContains(t, r.configText(), `"repo": "ezcv"`, "the file the terminal resolved")
	r.finish("/api/done")
}

func TestWriteRejectsAnEntryWithCharactersThatHaveNoBusinessInAConfig(t *testing.T) {
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	res := r.post("/api/write", map[string]any{"entries": []any{map[string]any{
		"type": "github", "owner": `me", "evil": "`, "repo": "x",
	}}}).wantStatus(http.StatusBadRequest, "an owner that would break out of its string literal")
	if !strings.Contains(res.body, "invalid owner") {
		t.Errorf("the refusal does not name the field: %q", res.body)
	}
	wantFile(t, r.configText(), fixtureConfig, "the config after a refused entry")
	r.finish("/api/cancel")
}

func TestWriteRefusesAHostCarryingALineTerminator(t *testing.T) {
	// A URL parser strips the newline before parsing but the original text keeps it, and it is
	// the original text that would land inside a string literal in the file.
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	res := r.post("/api/write", map[string]any{"entries": []any{map[string]any{
		"type": "github", "host": "https://a.com/x\ny", "owner": "o1", "repo": "r1",
	}}}).wantStatus(http.StatusBadRequest, "a host carrying a newline")
	if !strings.Contains(res.body, "not a usable host") {
		t.Errorf("the refusal does not say what is wrong with the host: %q", res.body)
	}
	wantFile(t, r.configText(), fixtureConfig, "the config after a refused host")
	r.finish("/api/cancel")
}

func TestWriteRefusesAHostileRepoNameFromTheProvider(t *testing.T) {
	// A listing is remote input. The select path builds entries from it, so a name carrying a
	// line break and a fabricated key must be refused where the entry is built.
	hostile := `[{ "name": "evil\n    \"path\": \"/etc/passwd\", \"slug\": \"pwned",
	              "full_name": "me/evil", "owner": { "login": "me" } }]`
	r := startWizard(t, wizardOptions{config: fixtureConfig, page: hostile})
	r.post("/api/repos", map[string]any{"provider": "github", "host": "https://api.github.com", "account": "me"}).
		wantStatus(http.StatusOK, "load the hostile listing")
	res := r.post("/api/write", map[string]any{
		"provider": "github", "host": "https://api.github.com", "account": "me",
		"releases": "provider", "select": []string{"me/evil"},
	})
	if res.status < 400 {
		t.Errorf("a hostile repository name was accepted (status %d): %s", res.status, res.body)
	}
	if !strings.Contains(res.body, "cannot go in a config file") {
		t.Errorf("the refusal does not say why: %q", res.body)
	}
	wantFile(t, r.configText(), fixtureConfig, "the config after a hostile listing")
	r.finish("/api/cancel")
}

func TestWriteAddsNothingOnASecondRunOverTheSameConfig(t *testing.T) {
	// Two sessions, the same entry: running the picker again must add nothing and take no
	// backup, because nothing changed.
	first := startWizard(t, wizardOptions{config: fixtureConfig})
	entries := []any{map[string]any{"type": "github", "owner": "me", "repo": "ezcv", "releases": "provider"}}
	first.post("/api/write", map[string]any{"entries": entries}).wantStatus(http.StatusOK, "the first write")
	afterFirst := first.configText()
	first.finish("/api/done")

	// The second session runs over the file the first one left behind, in the same directory.
	body := rerunOver(t, first.dir)
	wantEqual(t, body["added"], float64(0), "added")
	wantEqual(t, body["skipped"], float64(1), "skipped")
	wantEqual(t, body["changed"], false, "changed")
	wantEqual(t, body["backup"], nil, "backup")

	raw, err := os.ReadFile(filepath.Join(first.dir, "frznforge.config.jsonc"))
	if err != nil {
		t.Fatal(err)
	}
	wantFile(t, string(raw), afterFirst, "the config after a second run")
	if n := strings.Count(string(raw), `"repo": "ezcv"`); n != 1 {
		t.Errorf(`"repo": "ezcv" appears %d times, want 1`, n)
	}
}

// rerunOver starts a second wizard over an existing directory and writes the same entry again.
// It exists because startWizard always makes a fresh temp directory, and the point of this test
// is a second session over the FIRST one's output.
func rerunOver(t *testing.T, dir string) map[string]any {
	t.Helper()
	session, err := Listen(Options{
		Root: dir, Port: 0, NoOpen: true, Out: &bytes.Buffer{},
		Env: ingest.Env{}, Client: &http.Client{Transport: &stubProvider{page: githubPage}},
		Now: func() time.Time { return backupInstant },
	})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	served := make(chan error, 1)
	go func() { served <- session.Serve() }()

	parsed, err := url.Parse(session.URL)
	if err != nil {
		t.Fatal(err)
	}
	run := &wizardRun{
		t: t, dir: dir, config: filepath.Join(dir, "frznforge.config.jsonc"),
		session: session, origin: "http://" + parsed.Host, key: parsed.Query().Get("s"),
		client: &http.Client{Transport: &http.Transport{}, Timeout: 20 * time.Second},
		served: served,
	}
	t.Cleanup(func() { run.client.CloseIdleConnections() })
	body := run.post("/api/write", map[string]any{"entries": []any{
		map[string]any{"type": "github", "owner": "me", "repo": "ezcv", "releases": "provider"},
	}}).wantStatus(http.StatusOK, "the second write").json()
	run.finish("/api/done")
	return body
}

func TestASessionSurvivesAWriteAndOnlyDoneStopsIt(t *testing.T) {
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	r.post("/api/write", map[string]any{"entries": []any{
		map[string]any{"type": "github", "owner": "me", "repo": "ezcv"},
	}}).wantStatus(http.StatusOK, "the write")
	// A write no longer ends the run: the session is a multi-write editor.
	r.get("/api/context").wantStatus(http.StatusOK, "a request after a write")

	done := r.post("/api/done", map[string]any{}).wantStatus(http.StatusOK, "POST /api/done")
	wantEqual(t, done.json()["done"], true, "done")
	wantEqual(t, done.json()["writes"], float64(1), "writes")
	// The socket is closed with the answer: the server is about to go away, and a browser
	// holding a keep-alive to it would get a connection error instead of its reply.
	if !done.closed {
		t.Error("the answer did not ask the client to close the connection")
	}

	if err := r.wait(); err != nil {
		t.Fatalf("the session ended with an error: %v", err)
	}
	if !strings.Contains(r.out.String(), "Done — 1 write") {
		t.Errorf("the summary does not report the write:\n%s", r.out.String())
	}
	wantServerGone(t, r)
}

// wantServerGone proves the listener really is closed, not merely idle.
func wantServerGone(t *testing.T, r *wizardRun) {
	t.Helper()
	client := &http.Client{
		Transport: &http.Transport{DisableKeepAlives: true},
		Timeout:   5 * time.Second,
	}
	res, err := client.Get(r.origin + "/api/context?s=" + r.key)
	if err == nil {
		res.Body.Close()
		t.Fatalf("the server is still answering after the session ended (status %d)", res.StatusCode)
	}
}

/* ------------------------------------------------------------------ cancel */

func TestCancelStopsTheSessionAndTouchesNothing(t *testing.T) {
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	r.post("/api/repos", map[string]any{"provider": "github", "host": "https://api.github.com", "account": "me"}).
		wantStatus(http.StatusOK, "load the listing")
	body := r.finish("/api/cancel")
	wantEqual(t, body["cancelled"], true, "cancelled")
	wantEqual(t, body["writes"], float64(0), "writes")

	wantFile(t, r.configText(), fixtureConfig, "the config after a cancel")
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "frznforge.config.jsonc" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the directory holds %v, want only the config — a cancel writes no backup", names)
	}
	if !strings.Contains(r.out.String(), "nothing was written") {
		t.Errorf("the summary does not say nothing was written:\n%s", r.out.String())
	}
	wantServerGone(t, r)
}

/* ------------------------------------------------------------------ the provider token */

func TestTheTokenAppearsInNoAnswerFromAnyEndpoint(t *testing.T) {
	// newResponse checks every single response, so this test's job is to make sure every
	// endpoint — including its failure answers, which carry filesystem and provider messages —
	// is actually visited.
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	r.get("/")
	r.get("/api/context")
	r.get("/api/config")
	r.get("/api/profile")
	r.getWithKey("/api/context", "wrong")
	r.post("/api/repos", map[string]any{"provider": "github", "account": "me"})
	r.post("/api/repos", map[string]any{"provider": "github", "account": "nobody"})
	r.post("/api/preview", map[string]any{"provider": "github", "account": "me", "select": []string{"me/ezcv"}})
	r.post("/api/write", map[string]any{"entries": []any{map[string]any{"type": "github", "owner": "me", "repo": "ezcv"}}})
	r.post("/api/config/write", map[string]any{"operations": []any{
		map[string]any{"op": "set", "path": "site.title", "value": "X"},
	}})
	r.post("/api/profile/preview", map[string]any{"body": "# hi"})
	r.post("/api/profile/write", map[string]any{"body": "# hi"})
	r.post("/api/upload", map[string]any{"target": map[string]any{"kind": "owner"}, "data": "not base64 at all"})
	r.post("/api/nope", map[string]any{})
	r.finish("/api/done")

	// The URL the wizard printed carries a session key, not the provider token.
	if strings.Contains(r.out.String(), sentinelToken) {
		t.Errorf("the terminal output carries the provider token:\n%s", r.out.String())
	}
}

func TestTheTokenIsNeverSentToAHostTheBrowserInvented(t *testing.T) {
	// The exfiltration path a response-body check cannot see: the token going OUT, to a host
	// the page typed into "Custom API host".
	r := startWizard(t, wizardOptions{})
	body := r.post("/api/repos", map[string]any{
		"provider": "github", "host": "http://127.0.0.1:8799", "account": "me",
	}).wantStatus(http.StatusOK, "a listing from an unauthorised host").json()

	calls := r.stub.seen()
	if len(calls) == 0 {
		t.Fatal("no request was made at all, so nothing was proven")
	}
	for _, call := range calls {
		if !strings.Contains(call.url, "127.0.0.1:8799") {
			t.Errorf("a request went somewhere else entirely: %s", call.url)
		}
		if call.auth != "" || call.privateToken != "" {
			t.Errorf("credentials were sent to a host the terminal did not authorise: %+v", call)
		}
	}
	wantEqual(t, body["tokenSent"], false, "tokenSent")
	warnings, _ := body["warnings"].([]any)
	joined := fmt.Sprint(warnings...)
	if !strings.Contains(joined, "was not sent to it") {
		t.Errorf("the page is not told the listing is anonymous: %v", warnings)
	}

	r.finish("/api/cancel")
}

func TestTheTokenIsSentToTheProvidersOwnAPIHost(t *testing.T) {
	r := startWizard(t, wizardOptions{})
	body := r.post("/api/repos", map[string]any{
		"provider": "github", "host": "https://api.github.com", "account": "me",
	}).wantStatus(http.StatusOK, "a listing from the provider's own host").json()
	wantEqual(t, body["tokenSent"], true, "tokenSent")

	calls := r.stub.seen()
	if len(calls) == 0 {
		t.Fatal("no request was made at all")
	}
	wantEqual(t, calls[0].auth, "Bearer "+sentinelToken, "the Authorization header")
	r.finish("/api/cancel")
}

func TestTheTokenGoesToTheNamedHostButOnlyForThatProvider(t *testing.T) {
	// Binding --host to --provider is what stops the page switching provider and posting a
	// different token to the same intranet box.
	r := startWizard(t, wizardOptions{
		provider: "github", host: "https://ghe.corp/api/v3",
		env: ingest.Env{"FRZNFORGE_GITHUB_TOKEN": sentinelToken, "FRZNFORGE_GITEA_TOKEN": sentinelToken},
	})
	r.post("/api/repos", map[string]any{
		"provider": "github", "host": "https://ghe.corp/api/v3", "account": "me",
	}).wantStatus(http.StatusOK, "github at the authorised host")
	calls := r.stub.seen()
	wantEqual(t, calls[len(calls)-1].auth, "Bearer "+sentinelToken, "github at --host")

	r.post("/api/repos", map[string]any{
		"provider": "gitea", "host": "https://ghe.corp/api/v3", "account": "me",
	}).wantStatus(http.StatusOK, "gitea at the same host")
	calls = r.stub.seen()
	if last := calls[len(calls)-1]; last.auth != "" || last.privateToken != "" {
		t.Errorf("the gitea token was sent to a host authorised for github: %+v", last)
	}

	r.finish("/api/cancel")
}

/* ------------------------------------------------------------------ concurrency */

func TestConcurrentWritesAllLandUnderOneBackup(t *testing.T) {
	// Nothing binds a session to one tab: the URL is printed, and a browser may already have
	// opened one. Six unserialised read-modify-writes each answered "added 1" while only the
	// last entry survived. Every write must see the one before it, and the session keeps ONE
	// .bak of the pre-wizard file however many writes follow.
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	const writers = 6
	var wg sync.WaitGroup
	statuses := make([]int, writers)
	added := make([]any, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			res := r.post("/api/write", map[string]any{"entries": []any{
				map[string]any{"type": "github", "owner": "me", "repo": fmt.Sprintf("repo%d", n)},
			}})
			statuses[n] = res.status
			added[n] = res.json()["added"]
		}(i)
	}
	wg.Wait()

	written := r.configText()
	for i := 0; i < writers; i++ {
		if statuses[i] != http.StatusOK {
			t.Errorf("writer %d: status %d, want 200", i, statuses[i])
		}
		if added[i] != float64(1) {
			t.Errorf("writer %d: added = %v, want 1", i, added[i])
		}
		if !strings.Contains(written, fmt.Sprintf(`"repo": "repo%d"`, i)) {
			t.Errorf("writer %d's entry is not in the file — a later write overwrote it", i)
		}
	}

	backups := r.backups(r.dir)
	if len(backups) != 1 {
		t.Fatalf("%d backups, want exactly one for the session", len(backups))
	}
	raw, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	wantFile(t, string(raw), fixtureConfig, "the one backup")
	wantEqual(t, r.finish("/api/done")["writes"], float64(writers), "writes")
}

/* ------------------------------------------------------------------ request bodies */

func TestRefusesABodyBiggerThanTheCeiling(t *testing.T) {
	// The real bodies are a few kilobytes. Anything past the ceiling is refused outright rather
	// than buffered.
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	huge := map[string]any{"body": strings.Repeat("x", maxBodyBytes+1024)}
	r.post("/api/profile/write", huge).wantStatus(http.StatusBadRequest, "an over-sized body")
	r.post("/api/write", "not an object").wantStatus(http.StatusBadRequest, "a body that is not an object")
	wantFile(t, r.configText(), fixtureConfig, "the config after two refused bodies")
	r.finish("/api/cancel")
}
