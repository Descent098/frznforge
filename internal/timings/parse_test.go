package timings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The file this package is most useful for is the one a killed process left behind. Its last
// line is half written, and everything before it still has to parse.
func TestParseTolerantOfATornTail(t *testing.T) {
	good := `{"v":1,"run":"r1","id":"1","ts":"2026-09-06T22:49:03.512Z","kind":"git.clone","name":"a","ms":10}`
	in := good + "\n" + good + "\n" + `{"v":1,"run":"r1","id":"3","ts":"2026-09-`

	recs, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d records, want the 2 complete ones", len(recs))
	}
}

// One unparseable line must not cost the ten thousand after it.
func TestParseSkipsGarbageAndKeepsReading(t *testing.T) {
	line := func(id string) string {
		return `{"v":1,"run":"r1","id":"` + id + `","ts":"2026-09-06T22:49:03.512Z","kind":"k","ms":1}`
	}
	in := line("1") + "\n" + "this is not json\n" + "\n" + line("2") + "\n"

	recs, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2", len(recs))
	}
	if recs[1].ID != "2" {
		t.Fatalf("reading stopped at the bad line; last id is %q", recs[1].ID)
	}
}

// "No build has run yet" is an ordinary answer, not an error.
func TestParseFileMissingIsEmptyNotAnError(t *testing.T) {
	recs, err := ParseFile(filepath.Join(t.TempDir(), "nothing-here.jsonl"))
	if err != nil {
		t.Fatalf("ParseFile on a missing file: %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("got %d records from a missing file", len(recs))
	}
}

func recAt(run, kind, name string, ms float64, sec int, err string, pages int64) Record {
	r := Record{
		Version: SchemaVersion,
		Run:     run,
		ID:      "x",
		TS:      time.Date(2026, 9, 6, 22, 49, sec, 0, time.UTC).Format("2006-01-02T15:04:05.000Z"),
		Kind:    kind,
		Name:    name,
		MS:      ms,
		Err:     err,
	}
	if pages > 0 {
		r.Counts = Counts{"pages": pages}
	}
	return r
}

// The numbers here are worked out by hand; if the code disagrees, the code is wrong.
//
//	build.repo/a  100ms (r1, 1 page) + 300ms (r1, 2 pages, failed) + 200ms (r2, 3 pages)
//	              = 3 records, 600ms total, 200ms mean, 100ms best, 300ms worst, 6 pages,
//	                1 failure, 2 runs
//	git.clone/b    50ms (r1) = 1 record, 50ms everywhere
//	page.render/c  1ms + 1ms + 2ms = 4ms total over 3, mean 1.333333ms truncated to the ns
func TestAggregateMaths(t *testing.T) {
	recs := []Record{
		recAt("r1", "build.repo", "a", 100, 1, "", 1),
		recAt("r1", "build.repo", "a", 300, 2, "clone failed", 2),
		recAt("r2", "build.repo", "a", 200, 3, "", 3),
		recAt("r1", "git.clone", "b", 50, 4, "", 0),
		recAt("r1", "page.render", "c", 1, 5, "", 0),
		recAt("r1", "page.render", "c", 1, 6, "", 0),
		recAt("r1", "page.render", "c", 2, 7, "", 0),
	}

	got := Aggregate(recs)
	if len(got) != 3 {
		t.Fatalf("got %d stats, want 3", len(got))
	}
	// Sorted by total descending: 600ms, 50ms, 4ms.
	if got[0].Name != "a" || got[1].Name != "b" || got[2].Name != "c" {
		t.Fatalf("wrong order: %s, %s, %s", got[0].Name, got[1].Name, got[2].Name)
	}

	a := got[0]
	checks := []struct {
		what string
		got  any
		want any
	}{
		{"count", a.Count, 3},
		{"failed", a.Failed, 1},
		{"runs", a.Runs, 2},
		{"total", a.Total, 600 * time.Millisecond},
		{"mean", a.Mean, 200 * time.Millisecond},
		{"best", a.Best, 100 * time.Millisecond},
		{"worst", a.Worst, 300 * time.Millisecond},
		{"pages", a.Counts["pages"], int64(6)},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("build.repo/a %s = %v, want %v", c.what, c.got, c.want)
		}
	}

	if b := got[1]; b.Best != 50*time.Millisecond || b.Worst != 50*time.Millisecond || b.Mean != 50*time.Millisecond {
		t.Errorf("a single record must be its own best, worst and mean: %+v", b)
	}
	if c := got[2]; c.Total != 4*time.Millisecond || c.Mean != 1333333*time.Nanosecond {
		t.Errorf("page.render/c total %v mean %v, want 4ms and 1.333333ms", c.Total, c.Mean)
	}
}

func TestAggregateEmpty(t *testing.T) {
	if got := Aggregate(nil); len(got) != 0 {
		t.Fatalf("got %d stats from no records", len(got))
	}
}

// A run's wall time is the span from its first step to its last, not the sum of its parts: the
// parts overlap, because the build is parallel.
func TestRunsWallTimeIsTheSpanNotTheSum(t *testing.T) {
	recs := []Record{
		recAt("r2", "build.all", "", 1000, 10, "", 0),
		recAt("r2", "build.repo", "a", 250, 10, "", 0),
		recAt("r2", "build.repo", "b", 250, 10, "boom", 0),
		recAt("r1", "build.all", "", 500, 5, "", 0),
	}

	runs := Runs(recs)
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want 2", len(runs))
	}
	if runs[0].ID != "r1" || runs[1].ID != "r2" {
		t.Fatalf("runs are not oldest first: %s then %s", runs[0].ID, runs[1].ID)
	}
	r2 := runs[1]
	if r2.Records != 3 || r2.Failed != 1 {
		t.Errorf("r2 records %d failed %d, want 3 and 1", r2.Records, r2.Failed)
	}
	if r2.Wall != time.Second {
		t.Errorf("r2 wall %v, want 1s (the longest step, not the 1.5s sum)", r2.Wall)
	}

	if got := Filter(recs, "r2"); len(got) != 3 {
		t.Errorf("Filter returned %d records for r2, want 3", len(got))
	}
}

/* ---- the growth cap ------------------------------------------------------ */

// runLines builds n lines belonging to run id.
func runLines(id string, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(`{"v":1,"run":"` + id + `","id":"1","ts":"2026-09-06T22:49:03.512Z","kind":"k","ms":1}` + "\n")
	}
	return b.String()
}

// The cap drops WHOLE runs. Half a run reads as a fast run and would poison every aggregate
// computed over the file afterwards.
func TestTailRunsKeepsWholeNewestRuns(t *testing.T) {
	data := []byte(runLines("r1", 3) + runLines("r2", 2) + runLines("r3", 1))

	t.Run("bounded by run count", func(t *testing.T) {
		got := string(tailRuns(data, 1<<20, 2))
		want := runLines("r2", 2) + runLines("r3", 1)
		if got != want {
			t.Fatalf("got %q\nwant %q", got, want)
		}
	})

	t.Run("bounded by bytes", func(t *testing.T) {
		budget := len(runLines("r2", 2) + runLines("r3", 1))
		got := string(tailRuns(data, budget, 100))
		want := runLines("r2", 2) + runLines("r3", 1)
		if got != want {
			t.Fatalf("got %q\nwant %q", got, want)
		}
	})

	t.Run("the newest run survives any budget", func(t *testing.T) {
		got := string(tailRuns(data, 1, 100))
		if want := runLines("r3", 1); got != want {
			t.Fatalf("got %q\nwant %q", got, want)
		}
	})

	t.Run("a torn final line is dropped", func(t *testing.T) {
		torn := append([]byte(runLines("r3", 1)), []byte(`{"v":1,"run":"r3`)...)
		if got := string(tailRuns(torn, 1<<20, 100)); got != runLines("r3", 1) {
			t.Fatalf("torn tail survived: %q", got)
		}
	})
}

// Below the threshold the file is left exactly alone: trimming is a rare event, not something
// every build pays for.
func TestTrimLeavesSmallFilesAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	want := runLines("r1", 5)
	if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := trim(path); err != nil {
		t.Fatalf("trim: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("trim rewrote a small file")
	}
}

// End to end with the real constants, because TrimAtBytes and KeepBytes only mean anything in
// relation to each other: a KeepBytes above TrimAtBytes would silently never trim.
func TestTrimRewritesAnOversizeFile(t *testing.T) {
	dir := t.TempDir()
	path := Path(dir)

	// Enough runs, each comfortably over KeepBytes, that the trim has to drop most of them.
	perRun := (KeepBytes / len(runLines("r000", 1))) + 100
	var b strings.Builder
	for i := 0; b.Len() <= TrimAtBytes; i++ {
		b.WriteString(runLines(runID(i), perRun))
	}
	whole := b.String()
	if err := os.WriteFile(path, []byte(whole), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := trim(path); err != nil {
		t.Fatalf("trim: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) >= len(whole) {
		t.Fatalf("trim did not shrink the file: %d bytes of %d", len(got), len(whole))
	}
	if !strings.HasSuffix(whole, string(got)) {
		t.Fatal("trim kept something other than the tail of the file")
	}
	recs, err := ParseDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) == 0 {
		t.Fatal("trim left nothing to parse")
	}
	// Whole runs only: every run left in the file must still have all of its records.
	for _, r := range Runs(recs) {
		if r.Records != perRun {
			t.Fatalf("run %s kept %d of %d records — a partial run survived", r.ID, r.Records, perRun)
		}
	}
}

func runID(i int) string { return "r" + string(rune('a'+i%26)) + string(rune('a'+i/26)) }

func TestTrimOnAMissingFileIsFine(t *testing.T) {
	if err := trim(filepath.Join(t.TempDir(), "nothing.jsonl")); err != nil {
		t.Fatalf("trim on a missing file: %v", err)
	}
}
