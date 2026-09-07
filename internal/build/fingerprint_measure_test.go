package build_test

// The measurement behind "the diagnostics are free when off and cheap when on".
//
// It is off by default because it rebuilds the whole site twice and takes minutes on a corpus
// worth measuring. Run it deliberately, against whatever artifact is on disk:
//
//	FRZNFORGE_MEASURE=1 go test ./internal/build/ -run FingerprintAndMeasure -v -count=1
//
// Two numbers come out — the same build with every diagnostic discarded, and with the run log
// and the timings file actually being written — plus one hash per run over every emitted file.
// The hashes must match. That is the whole claim: instrumentation may cost time, and may not
// cost a single byte of the site.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"frznforge/internal/build"
	"frznforge/internal/logging"
	"frznforge/internal/timings"
)

func TestFingerprintAndMeasure(t *testing.T) {
	if os.Getenv("FRZNFORGE_MEASURE") == "" {
		t.Skip("set FRZNFORGE_MEASURE=1 to run the measurement")
	}
	root := repoRoot(t)
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

	type result struct {
		label       string
		elapsed     time.Duration
		routes      int
		fingerprint string
		logBytes    int64
		timeBytes   int64
	}
	var results []result

	measure := func(label string, instrumented bool) {
		var diagDir string
		if instrumented {
			// The real sinks, in a directory of their own: this is what `frznforge build` does on
			// every run, and measuring a capture handler instead would measure the wrong thing.
			diagDir = t.TempDir()
			if err := logging.SetupFile(diagDir, io.Discard); err != nil {
				t.Fatalf("open the run log: %v", err)
			}
			if err := timings.Open(diagDir, io.Discard); err != nil {
				t.Fatalf("open the timings file: %v", err)
			}
		}

		out := filepath.Join(t.TempDir(), "site")
		start := time.Now()
		res, err := build.Run(build.Options{Root: root, OutDir: out, Now: now})
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("%s build: %v", label, err)
		}

		r := result{label: label, elapsed: elapsed, routes: res.Routes, fingerprint: fingerprint(t, out)}
		if instrumented {
			_ = timings.Close()
			_ = logging.Close()
			r.logBytes = sizeOf(logging.LogPath(diagDir))
			r.timeBytes = sizeOf(timings.Path(diagDir))
		}
		results = append(results, r)
	}

	measure("diagnostics off", false)
	measure("diagnostics on", true)

	for _, r := range results {
		t.Logf("%-24s %8s  %d files  run log %d B  timings %d B  %s",
			r.label, r.elapsed.Round(time.Millisecond), r.routes, r.logBytes, r.timeBytes, r.fingerprint)
	}
	for _, r := range results[1:] {
		if r.fingerprint != results[0].fingerprint {
			t.Errorf("%q emitted a different site from %q — a diagnostic changed the output",
				r.label, results[0].label)
		}
	}
}

func sizeOf(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// fingerprint is one hash over every emitted path and its contents.
func fingerprint(t *testing.T, root string) string {
	t.Helper()
	tree := hashTree(t, root)
	paths := make([]string, 0, len(tree))
	for p := range tree {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		fmt.Fprintf(h, "%s %s\n", p, tree[p])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
