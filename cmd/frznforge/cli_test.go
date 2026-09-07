package main

// Port of tests/unit/cli.test.ts and tests/unit/build-script.test.ts.
//
// Nothing in this package's tests touches the network, the real environment, the real clock or a
// terminal. Every provider call goes through an httptest server reached via Io.Client, every
// token comes from an ingest.Env literal, every backup timestamp comes from Io.Now, and Io.IsTTY
// is false unless a test is deliberately driving the interactive picker from a string. A test
// that hangs here would be a test that found a prompt the flags were supposed to have answered —
// which is the bug the whole non-TTY branch exists to prevent.
//
// This file holds only the shared fixtures; the assertions live beside the code they cover:
// flags_test.go, select_test.go, entries_test.go, listing_test.go, init_test.go, build_test.go.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"frznforge/internal/ingest"
)

// fixedNow is the clock every test that writes a file runs on. Its formatted form
// (20260823T195100Z) is asserted by name in the backup tests, so changing it changes those.
var fixedNow = time.Date(2026, 8, 23, 19, 51, 0, 0, time.UTC)

// fixtureConfig is a hand-written config of the shape `init` has to survive: comments above the
// file, a block comment inside it, a real entry in `repos`, and a same-line comment further
// down. Every one of those is something a splice can destroy, and several tests assert that the
// bytes outside the array come through unchanged.
const fixtureConfig = `// frznforge site configuration.
// Read at build time — changing it requires a rebuild.
{
  "owner": { "name": "Kieran Wood", "handle": "kieran", "profile": "./content/profile.md" },

  /*
   * Repositories to ingest.
   * Example:
   *   { "type": "local", "path": "../useful" },
   */
  "repos": [
    // Self-host demo: the frznforge repo itself.
    { "type": "local", "path": ".", "slug": "frznforge" },
  ],

  "ingest": {
    "outDir": "./data" // keep this comment
  }
}
`

/* ---- the Io seam ---------------------------------------------------------- */

// testIo is an Io whose streams are buffers: no terminal, no environment, no wall clock.
type testIo struct {
	*Io
	out bytes.Buffer
	err bytes.Buffer
}

// newTestIo builds an Io rooted at cwd. Env is a non-nil empty map on purpose — nil would read
// the developer's real environment, and a machine with GITHUB_TOKEN set would then take a
// different branch through every token test.
func newTestIo(cwd string) *testIo {
	t := &testIo{}
	t.Io = &Io{
		Out:   &t.out,
		Err:   &t.err,
		Cwd:   cwd,
		Env:   ingest.Env{},
		Now:   func() time.Time { return fixedNow },
		In:    strings.NewReader(""),
		IsTTY: false,
	}
	return t
}

func (t *testIo) stdout() string { return t.out.String() }
func (t *testIo) stderr() string { return t.err.String() }

// everything is stdout and stderr together — what a person watching the run actually sees, and
// the only honest thing to assert a secret's absence against.
func (t *testIo) everything() string { return t.out.String() + "\n" + t.err.String() }

/* ---- a provider that never leaves the process ----------------------------- */

// fakeProvider serves listing pages from memory. Paths are matched exactly (`/users/me/repos`),
// so a test that mistypes an endpoint gets the 404 branch rather than a silent pass.
//
// Every body is shorter than a page, so paginate stops after the first request; the one test
// that needs a full page installs its own handler.
func fakeProvider(t *testing.T, pages map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := pages[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		w.Header().Set("content-type", "application/json")
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Errorf("encode %s: %v", r.URL.Path, err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// listingItem is one repository as a GitHub/Gitea/Forgejo listing returns it. A map rather than
// a struct so a test can leave `fork` and `archived` out entirely, which is a different input
// from setting them false.
func listingItem(owner, name string, extra map[string]any) map[string]any {
	item := map[string]any{
		"name":      name,
		"full_name": owner + "/" + name,
		"owner":     map[string]any{"login": owner},
	}
	for k, v := range extra {
		item[k] = v
	}
	return item
}

/* ---- listing fixtures for the pure selection tests ------------------------ */

// testRepo is one listing entry with every flag REPORTED as false — the shape GitHub, Gitea and
// Forgejo return. A repo built here can therefore be filtered; the GitLab shape, whose Fork and
// Archived are nil because the listing never said, is written out longhand where it is needed so
// that the difference between "false" and "unknown" stays visible in the test that turns on it.
func testRepo(fullName string, mutate ...func(*remoteRepo)) remoteRepo {
	owner, name, _ := strings.Cut(fullName, "/")
	fork, archived := false, false
	r := remoteRepo{Name: name, FullName: fullName, Owner: owner, Fork: &fork, Archived: &archived}
	for _, m := range mutate {
		m(&r)
	}
	return r
}

func isFork(r *remoteRepo)     { yes := true; r.Fork = &yes }
func isArchived(r *remoteRepo) { yes := true; r.Archived = &yes }
func isPrivate(r *remoteRepo)  { r.Private = true }

/* ---- filesystem helpers --------------------------------------------------- */

// writeFixtureConfig drops fixtureConfig into a fresh temp directory and returns both.
func writeFixtureConfig(t *testing.T) (dir, file string) {
	t.Helper()
	dir = t.TempDir()
	file = filepath.Join(dir, "frznforge.config.jsonc")
	if err := os.WriteFile(file, []byte(fixtureConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, file
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// mustContain fails with the whole text, not just the missing needle: a CLI message is judged as
// a whole, and a diff of one substring tells a reader nothing about what was printed instead.
func mustContain(t *testing.T, text, want, why string) {
	t.Helper()
	if !strings.Contains(text, want) {
		t.Errorf("%s\n  missing: %q\n  in:\n%s", why, want, text)
	}
}

func mustNotContain(t *testing.T, text, unwanted, why string) {
	t.Helper()
	if strings.Contains(text, unwanted) {
		t.Errorf("%s\n  present: %q\n  in:\n%s", why, unwanted, text)
	}
}

func names(repos []remoteRepo) []string {
	out := make([]string, len(repos))
	for i, r := range repos {
		out[i] = r.FullName
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
