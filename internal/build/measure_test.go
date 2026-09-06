package build_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"frznforge/internal/build"
)

// TestMeasureWorkers times the same build at several worker counts.
//
// Off by default — it is a measurement, not an assertion, and it rebuilds the whole site once
// per row. Run it deliberately:
//
//	FRZNFORGE_MEASURE=1 go test ./internal/build/ -run MeasureWorkers -v -count=1
//
// FRZNFORGE_OUT_DIR selects the artifact, so the same command measures the self-site or the
// multi-repo fixture corpus. The self-site has ONE repository, so it is the wrong thing to
// measure here by construction: parallelism across repos cannot help a corpus of one, and a
// reading that shows no gain there is the expected result rather than a disappointing one.
func TestMeasureWorkers(t *testing.T) {
	if os.Getenv("FRZNFORGE_MEASURE") == "" {
		t.Skip("set FRZNFORGE_MEASURE=1 to run the measurement")
	}
	root := repoRoot(t)
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	max := runtime.GOMAXPROCS(0)

	for _, workers := range []int{1, 2, 4, 8, max} {
		if workers > max {
			continue
		}
		out := filepath.Join(t.TempDir(), "w")
		start := time.Now()
		res, err := build.Run(build.Options{Root: root, OutDir: out, Now: now, Workers: workers})
		if err != nil {
			t.Fatalf("%d workers: %v", workers, err)
		}
		t.Logf("workers=%-3d %6s  %d files", workers, time.Since(start).Round(time.Millisecond), res.Routes)
	}
}
