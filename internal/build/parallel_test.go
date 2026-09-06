package build_test

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"frznforge/internal/build"
)

// TestSerialAndParallelAgree is the gate that makes rendering repositories concurrently safe to
// ship.
//
// Concurrency cannot change what a page contains — each repo writes paths derived from the
// artifact, and no two repos write the same path — so the two modes must produce byte-identical
// trees. If they ever differ, something is sharing state that should not be, and this says so
// with the file name rather than leaving it to be noticed as a dirty diff months later.
func TestSerialAndParallelAgree(t *testing.T) {
	root := repoRoot(t)
	if _, err := os.Stat(filepath.Join(root, "data", "forge.json")); err != nil {
		t.Skip("no artifact on this machine")
	}
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

	serial := filepath.Join(t.TempDir(), "serial")
	parallel := filepath.Join(t.TempDir(), "parallel")

	if _, err := build.Run(build.Options{Root: root, OutDir: serial, Now: now, Workers: 1}); err != nil {
		t.Fatalf("serial build: %v", err)
	}
	workers := runtime.GOMAXPROCS(0)
	if workers < 2 {
		t.Skip("single-core machine: there is no parallel mode to compare")
	}
	if _, err := build.Run(build.Options{Root: root, OutDir: parallel, Now: now, Workers: workers}); err != nil {
		t.Fatalf("parallel build (%d workers): %v", workers, err)
	}

	a, b := hashTree(t, serial), hashTree(t, parallel)
	var differing []string
	for path, sumA := range a {
		if sumB, ok := b[path]; !ok {
			differing = append(differing, path+" (only serial wrote it)")
		} else if sumA != sumB {
			differing = append(differing, path+" (different bytes)")
		}
	}
	for path := range b {
		if _, ok := a[path]; !ok {
			differing = append(differing, path+" (only parallel wrote it)")
		}
	}
	if len(differing) > 0 {
		// Keep the first differing pair so the failure can be inspected: a byte difference
		// between two renders of the same page is never obvious from its name.
		if dump := os.Getenv("FRZNFORGE_DUMP_DIFF"); dump != "" {
			sort.Strings(differing)
			name := strings.TrimSuffix(strings.TrimSuffix(differing[0], " (different bytes)"), " (only serial wrote it)")
			for _, side := range []struct{ dir, tag string }{{serial, "serial"}, {parallel, "parallel"}} {
				if content, err := os.ReadFile(filepath.Join(side.dir, filepath.FromSlash(name))); err == nil {
					_ = os.WriteFile(filepath.Join(dump, side.tag+".html"), content, 0o644)
				}
			}
			t.Logf("dumped %s to %s", name, dump)
		}
		sort.Strings(differing)
		if len(differing) > 20 {
			differing = append(differing[:20], "…")
		}
		t.Fatalf("serial and %d-worker builds differ in %d files:\n  %s",
			workers, len(differing), strings.Join(differing, "\n  "))
	}
	t.Logf("%d files identical between serial and %d workers", len(a), workers)
}
