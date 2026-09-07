package main

// The two lists that have to stay complete.
//
// A subprocess or an outbound request that nothing logs is invisible, and invisible is exactly
// the failure mode the run log exists for: the Windows blob-truncation hang was found because a
// "git start" had no matching "git done", and it could only be found because EVERY git call was
// logged. One new exec.Command written in a hurry is one place a future hang can hide.
//
// So this walks the source and fails when a site appears that nothing accounts for. It reads
// text rather than parsing Go on purpose — the alternative is a dependency, and this project has
// two.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// site is one place the program reaches outside itself.
//
// prefix is the record name logged around it: `<prefix> start` before, `<prefix> done` after.
// loggedIn names the file carrying those records when it is not this one — the postprocess hook
// is CONSTRUCTED in a platform file and STARTED in postprocess.go, and only the second can log a
// duration. An empty prefix is a deliberate exemption and the reason is on the entry.
type site struct {
	file     string
	prefix   string
	loggedIn string
}

// subprocessSites: every non-test file that constructs a process.
var subprocessSites = []site{
	// The git plumbing every extractor runs on. This pairing is what found the hang.
	{file: "internal/ingest/git.go", prefix: "git"},
	// Network git: clone --mirror, remote update, ls-remote.
	{file: "internal/ingest/remote.go", prefix: "git-net"},
	// The user's own postprocess hook: two platform files build the command, one file runs it.
	{file: "internal/build/postprocess_windows.go", prefix: "postprocess", loggedIn: "internal/build/postprocess.go"},
	{file: "internal/build/postprocess_notwindows.go", prefix: "postprocess", loggedIn: "internal/build/postprocess.go"},

	// Exempt: `init --web` opening a browser. It is started and deliberately never waited for —
	// the browser outlives the wizard — so it cannot hold a run up, and internal/wizard is not
	// part of the engine's hot path.
	{file: "internal/wizard/wizard.go"},
}

// fetchSites: every non-test file that issues an HTTP request.
var fetchSites = []site{
	// The provider importers' shared client. Every ingest fetch, retry and paginated page.
	{file: "internal/ingest/http.go", prefix: "http"},
	// `frznforge init` listing an account's repositories.
	{file: "cmd/frznforge/listing.go", prefix: "http"},

	// KNOWN GAP: internal/wizard/providers.go duplicates cmd/frznforge/listing.go's listing walk
	// for `init --web` (see that file's header, which names the duplication) and is NOT logged.
	// It belongs to internal/wizard.
	{file: "internal/wizard/providers.go"},
}

// skippedDirs are module-relative directories this guard does not walk.
var skippedDirs = []string{
	// Fixture git repositories built by tests. Not a file any run of the binary executes.
	"internal/ingest/testsupport",
	// The timings viewer. It reads the files this instrumentation writes and drives none of the
	// work they describe, so a subprocess of its own is a terminal concern, not a build one.
	"cmd/frzndebugger",
}

func TestEverySubprocessIsLoggedBeforeAndAfterItRuns(t *testing.T) {
	assertInstrumented(t, "exec.Command", subprocessSites,
		"a process is started here and nothing records it. Log `<name> start` before it and "+
			"`<name> done` after it, the way internal/ingest/git.go does — a call that never "+
			"returns is only ever findable as a start with no matching done")
}

func TestEveryOutboundRequestIsLoggedBeforeAndAfterItRuns(t *testing.T) {
	assertInstrumented(t, "http.NewRequest", fetchSites,
		"an HTTP request is made here and nothing records it. Log `http start` before it and "+
			"`http done` (or `http failed`) after it, the way internal/ingest/http.go does")
}

// assertInstrumented finds every non-test file containing needle and checks it against sites.
func assertInstrumented(t *testing.T, needle string, sites []site, advice string) {
	t.Helper()
	root := moduleRoot(t)

	known := make(map[string]site, len(sites))
	for _, s := range sites {
		known[s.file] = s
	}
	found := map[string]bool{}

	for _, dir := range []string{"internal", "cmd"} {
		walkGoFiles(t, root, filepath.Join(root, dir), func(rel, body string) {
			if !strings.Contains(body, needle) {
				return
			}
			found[rel] = true
			s, listed := known[rel]
			if !listed {
				t.Errorf("%s contains %s\n  %s\n  Then list it in cmd/frznforge/coverage_guard_test.go.",
					rel, needle, advice)
				return
			}
			if s.prefix == "" {
				return // an exemption, argued at its entry
			}
			owner, ownerBody := rel, body
			if s.loggedIn != "" {
				owner = s.loggedIn
				ownerBody = readSource(t, filepath.Join(root, filepath.FromSlash(owner)))
			}
			for _, want := range []string{`"` + s.prefix + ` start"`, `"` + s.prefix + ` done"`} {
				if !strings.Contains(ownerBody, want) {
					t.Errorf("%s reaches outside the process and %s never logs %s", rel, owner, want)
				}
			}
		})
	}

	// The list must not outlive the code either: an entry for a file that no longer does this is
	// a claim nobody is checking any more.
	for _, s := range sites {
		if !found[s.file] {
			t.Errorf("%s is listed as containing %s and no longer does — drop the entry", s.file, needle)
		}
	}
}

func walkGoFiles(t *testing.T, root, dir string, fn func(rel, body string)) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(mustRel(t, root, path))
		if d.IsDir() {
			for _, skip := range skippedDirs {
				if rel == skip {
					return filepath.SkipDir
				}
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		fn(rel, readSource(t, path))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
}

func readSource(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

func mustRel(t *testing.T, base, target string) string {
	t.Helper()
	rel, err := filepath.Rel(base, target)
	if err != nil {
		t.Fatal(err)
	}
	return rel
}

// moduleRoot walks up from the test's working directory to the directory holding go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("could not find the module root")
	return ""
}
