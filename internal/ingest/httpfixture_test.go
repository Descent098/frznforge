package ingest

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// Shared test plumbing for everything that would otherwise talk to a forge.
//
// NO TEST IN THIS PACKAGE MAY REACH THE NETWORK. Two seams make that structural rather than a
// promise: provider HTTP goes through an injected Doer that serves the recorded responses in
// tests/fixtures/http and errors loudly on an unmatched URL, and provider GIT goes through
// `git clone --mirror <local fixture repo>`, which behaves exactly like an https clone. Nothing
// here resolves a hostname.

// httpFixtureRoute is one canned response, keyed in httpFixtureDoer by a URL substring.
type httpFixtureRoute struct {
	// Pattern is matched as a substring of the request URL. Declaration order decides, so the
	// most specific pattern must come first ("/releases" before "/repos/owner/name").
	Pattern string
	// Status defaults to 200.
	Status int
	// Body is returned verbatim; empty means an empty body.
	Body string
	// Headers are merged over `content-type: application/json`.
	Headers map[string]string
}

// httpFixtureCall is one request the doer saw.
type httpFixtureCall struct {
	URL     string
	Headers http.Header
}

// httpFixtureDoer serves recorded fixtures instead of hitting the network, and records what was
// asked for so a test can assert on headers and pagination.
type httpFixtureDoer struct {
	mu     sync.Mutex
	routes []httpFixtureRoute
	calls  []httpFixtureCall
}

func newHTTPFixtureDoer(routes ...httpFixtureRoute) *httpFixtureDoer {
	return &httpFixtureDoer{routes: routes}
}

func (d *httpFixtureDoer) Do(req *http.Request) (*http.Response, error) {
	url := req.URL.String()
	d.mu.Lock()
	d.calls = append(d.calls, httpFixtureCall{URL: url, Headers: req.Header.Clone()})
	routes := d.routes
	d.mu.Unlock()

	for _, route := range routes {
		if !strings.Contains(url, route.Pattern) {
			continue
		}
		return fixtureResponse(req, route), nil
	}
	// An unmatched URL fails loudly and names itself, so a test that forgets a route cannot
	// silently fall through to something that looks like a provider outage.
	patterns := make([]string, 0, len(routes))
	for _, r := range routes {
		patterns = append(patterns, r.Pattern)
	}
	return nil, &noFixtureError{url: url, patterns: patterns}
}

func (d *httpFixtureDoer) recorded() []httpFixtureCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]httpFixtureCall, len(d.calls))
	copy(out, d.calls)
	return out
}

type noFixtureError struct {
	url      string
	patterns []string
}

func (e *noFixtureError) Error() string {
	known := "(none)"
	if len(e.patterns) > 0 {
		known = strings.Join(e.patterns, ", ")
	}
	return "no HTTP fixture matches " + e.url + "\n  known patterns: " + known
}

func fixtureResponse(req *http.Request, route httpFixtureRoute) *http.Response {
	status := route.Status
	if status == 0 {
		status = http.StatusOK
	}
	header := http.Header{}
	header.Set("Content-Type", "application/json; charset=utf-8")
	for k, v := range route.Headers {
		header.Set(k, v)
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(route.Body)),
		Request:    req,
	}
}

// scriptedDoer replays a list of responses in order, repeating the last one once the script runs
// out. It is the "what did the client actually send" harness for retry and rate-limit cases.
type scriptedDoer struct {
	mu     sync.Mutex
	script []httpFixtureRoute
	calls  []string
	// Err, when set for a step, is returned instead of a response — the transport-failure case.
	errs map[int]error
}

func newScriptedDoer(steps ...httpFixtureRoute) *scriptedDoer {
	return &scriptedDoer{script: steps, errs: map[int]error{}}
}

func (d *scriptedDoer) Do(req *http.Request) (*http.Response, error) {
	d.mu.Lock()
	index := len(d.calls)
	d.calls = append(d.calls, req.URL.String())
	if err, ok := d.errs[index]; ok {
		d.mu.Unlock()
		return nil, err
	}
	step := d.script[smallerInt(index, len(d.script)-1)]
	d.mu.Unlock()
	if step.Body == "" {
		step.Body = `{"ok":true}`
	}
	return fixtureResponse(req, step), nil
}

func (d *scriptedDoer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.calls)
}

// erroringDoer models a transport that never connects.
type erroringDoer struct {
	mu    sync.Mutex
	err   error
	calls int
}

func (d *erroringDoer) Do(*http.Request) (*http.Response, error) {
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()
	return nil, d.err
}

func (d *erroringDoer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

// httpFixturePath is tests/fixtures/http, located from this source file so the tests do not
// depend on the working directory.
func httpFixturePath(t testing.TB, rel string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test source file")
	}
	root := filepath.Join(filepath.Dir(thisFile), "..", "..")
	return filepath.Join(append([]string{root, "tests", "fixtures", "http"}, strings.Split(rel, "/")...)...)
}

// loadHTTPFixture reads a recorded provider response, e.g. "github/repo.json".
func loadHTTPFixture(t testing.TB, rel string) string {
	t.Helper()
	data, err := os.ReadFile(httpFixturePath(t, rel))
	if err != nil {
		t.Fatalf("read HTTP fixture %s: %v", rel, err)
	}
	return string(data)
}

// resetSharedBackoff clears the process-wide rate-limit gate.
//
// The gate is process-wide by design (an ingest run wants every repo on one forge to share a
// timer). In a test process that means a rate-limited case would block api.github.com for every
// case after it, so each case that uses the default gate starts from a clean one.
func resetSharedBackoff(t testing.TB) {
	t.Helper()
	SharedBackoff.Reset()
	t.Cleanup(SharedBackoff.Reset)
}

// smallerInt is spelled out rather than using the builtin `min`, which a package-level function
// of that name would shadow for every other file in the test build.
func smallerInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
