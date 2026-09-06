package build_test

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"frznforge/internal/build"
)

// Determinism is a property of this build, not an aspiration: the same artifact must produce the
// same bytes, on any machine, on any day. Two things routinely break it in a Go port and neither
// announces itself —
//
//   - map iteration, which is randomised, so an unsorted range emits a different order per run;
//   - concurrency, once Phase 7 renders repos in parallel.
//
// Both produce a site that looks right and diffs dirty, which is how they survive review. These
// tests are the gate.

// TestBuildIsDeterministic builds the same artifact twice and compares every emitted file.
//
// The clock is injected identically for both runs, because "3 days ago" legitimately changes with
// real time — that is the one input a reproducible build is allowed to vary on, and pinning it
// here is what isolates the failure modes above.
func TestBuildIsDeterministic(t *testing.T) {
	root := repoRoot(t)
	if _, err := os.Stat(filepath.Join(root, "data", "forge.json")); err != nil {
		t.Skip("no artifact on this machine; run `npm run ingest` or `frznforge ingest` first")
	}

	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	first := filepath.Join(t.TempDir(), "one")
	second := filepath.Join(t.TempDir(), "two")

	for _, out := range []string{first, second} {
		if _, err := build.Run(build.Options{Root: root, OutDir: out, Now: now}); err != nil {
			t.Skipf("build is not complete yet: %v", err)
		}
	}

	a, b := hashTree(t, first), hashTree(t, second)
	if len(a) != len(b) {
		t.Fatalf("run one emitted %d files, run two emitted %d", len(a), len(b))
	}
	var differing []string
	for path, sumA := range a {
		sumB, ok := b[path]
		if !ok {
			differing = append(differing, path+" (missing from run two)")
			continue
		}
		if sumA != sumB {
			differing = append(differing, path)
		}
	}
	for path := range b {
		if _, ok := a[path]; !ok {
			differing = append(differing, path+" (missing from run one)")
		}
	}
	if len(differing) > 0 {
		sort.Strings(differing)
		if len(differing) > 20 {
			differing = append(differing[:20], "…")
		}
		t.Fatalf("two builds of the same artifact differ in %d files:\n  %s",
			len(differing), strings.Join(differing, "\n  "))
	}
	t.Logf("%d files, byte-identical across two runs", len(a))
}

// TestFilePath pins the route→file rule. It is the one piece of the output contract that every
// page family depends on and none of them owns.
func TestFilePath(t *testing.T) {
	cases := []struct{ base, route, want string }{
		{"", "/", "index.html"},
		{"", "/repos/", "repos/index.html"},
		{"", "/repos/alpha/", "repos/alpha/index.html"},
		{"", "/404", "404.html"},
		{"", "/search-index.json", "search-index.json"},
		{"", "/repos/a/raw/main/src/x.go", "repos/a/raw/main/src/x.go"},
		{"", "/repos/a/archive/main.zip", "repos/a/archive/main.zip"},
		// Under a sub-path deploy the base is stripped: the file lives at the same place on
		// disk, and the host serves it under the prefix.
		{"/mysite", "/mysite/", "index.html"},
		{"/mysite", "/mysite/repos/alpha/", "repos/alpha/index.html"},
		{"/mysite", "/mysite/404", "404.html"},
	}
	for _, c := range cases {
		if got := build.FilePath(c.base, c.route); got != c.want {
			t.Errorf("FilePath(%q, %q) = %q, want %q", c.base, c.route, got, c.want)
		}
	}
}

// TestRawPath pins the other half of the rule: a bytes route is never renamed.
//
// The extensionless cases are the ones that matter. Sending them through FilePath publishes
// `LICENSE.html`, and every raw link in every file table for that repo 404s.
func TestRawPath(t *testing.T) {
	cases := []struct{ base, route, want string }{
		{"", "/repos/a/raw/main/src/x.go", "repos/a/raw/main/src/x.go"},
		{"", "/repos/a/raw/main/LICENSE", "repos/a/raw/main/LICENSE"},
		{"", "/repos/a/raw/main/docs/TODO", "repos/a/raw/main/docs/TODO"},
		{"", "/repos/a/raw/main/.gitignore", "repos/a/raw/main/.gitignore"},
		{"", "/notes/n/raw/Makefile", "notes/n/raw/Makefile"},
		{"", "/repos/a/archive/main.zip", "repos/a/archive/main.zip"},
		{"", "/search-index.json", "search-index.json"},
		{"/mysite", "/mysite/repos/a/raw/main/LICENSE", "repos/a/raw/main/LICENSE"},
	}
	for _, c := range cases {
		if got := build.RawPath(c.base, c.route); got != c.want {
			t.Errorf("RawPath(%q, %q) = %q, want %q", c.base, c.route, got, c.want)
		}
	}
}

func hashTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(content)
		out[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

// repoRoot walks up from the test's working directory to the module root.
func repoRoot(t *testing.T) string {
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
