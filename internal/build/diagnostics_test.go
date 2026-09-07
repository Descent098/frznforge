package build_test

// What the render says about itself while it runs, and the one property that says is allowed to
// have: none of it may change a byte of the site.

import (
	"bytes"
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"frznforge/internal/build"
	"frznforge/internal/timings"
)

// capture is a slog handler that keeps every record, formatted the way the text handler would
// print the fields this package asserts on.
//
// The mutex is a POINTER and is shared with every handler WithAttrs derives: the build writes
// records from every worker at once, and a derived handler with a mutex of its own would guard a
// different lock than the slice it appends to.
type capture struct {
	mu    *sync.Mutex
	lines *[]string
	attrs []slog.Attr
}

func (c *capture) Enabled(context.Context, slog.Level) bool { return true }

func (c *capture) Handle(_ context.Context, r slog.Record) error {
	parts := []string{r.Message}
	for _, a := range c.attrs {
		parts = append(parts, a.Key+"="+a.Value.String())
	}
	r.Attrs(func(a slog.Attr) bool {
		parts = append(parts, a.Key+"="+a.Value.String())
		return true
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	*c.lines = append(*c.lines, strings.Join(parts, " "))
	return nil
}

func (c *capture) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &capture{mu: c.mu, lines: c.lines, attrs: append(append([]slog.Attr{}, c.attrs...), attrs...)}
}
func (c *capture) WithGroup(string) slog.Handler { return c }

// captureLogs installs the capture handler for the duration of the test.
func captureLogs(t *testing.T) *[]string {
	t.Helper()
	lines := &[]string{}
	prev := slog.Default()
	slog.SetDefault(slog.New(&capture{mu: &sync.Mutex{}, lines: lines}))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return lines
}

// captureTimings installs a process-wide recorder writing into a buffer.
func captureTimings(t *testing.T) func() []timings.Record {
	t.Helper()
	var buf bytes.Buffer
	timings.Install(timings.NewRecorder(&buf, "test-run"))
	t.Cleanup(func() { timings.Install(nil) })
	return func() []timings.Record {
		records, err := timings.Parse(bytes.NewReader(buf.Bytes()))
		if err != nil {
			t.Fatalf("parse the timings this build wrote: %v", err)
		}
		return records
	}
}

func TestTheRenderNamesEveryFamilyBeforeAndAfterItRunsIt(t *testing.T) {
	// The hour-long hang printed a repository name and then nothing. A family that never returns
	// has to leave a start with no matching done, which is the only thing that says WHERE — so
	// the pairing is asserted rather than assumed.
	root, _ := syncFixture(t)
	lines := captureLogs(t)

	if _, err := build.Run(build.Options{
		Root: root, OutDir: t.TempDir(), Now: fixedBuildTime, Workers: 4,
	}); err != nil {
		t.Fatalf("build: %v", err)
	}

	starts, dones := map[string]int{}, map[string]int{}
	for _, line := range *lines {
		switch {
		case strings.HasPrefix(line, "family start "):
			starts[logFamilyOf(line)]++
		case strings.HasPrefix(line, "family done "):
			dones[logFamilyOf(line)]++
		}
	}
	if len(starts) == 0 {
		t.Fatalf("the render logged no families at all:\n%s", strings.Join(*lines, "\n"))
	}
	for family, n := range starts {
		if dones[family] != n {
			t.Errorf("%s: %d starts, %d dones — a pair is missing", family, n, dones[family])
		}
	}
	// Spot the families that matter: the site-wide ones and the per-repo multiplier.
	for _, want := range []string{
		"build.profile", "build.repos", "build.search-index",
		"build.tree", "build.blob", "build.raw", "build.history",
	} {
		if starts[want] == 0 {
			t.Errorf("no %q family was logged; got %v", want, sortedNames(starts))
		}
	}
}

func TestTheWorkerSemaphoreSaysWhoHoldsItAndHowFullItIs(t *testing.T) {
	// The build that hung parked every worker in `chan send` and produced no evidence at all.
	// Slots taken and returned are logged with the section that took them and the occupancy at
	// the time, so the same failure names itself.
	root, _ := syncFixture(t)
	lines := captureLogs(t)

	if _, err := build.Run(build.Options{
		Root: root, OutDir: t.TempDir(), Now: fixedBuildTime, Workers: 4,
	}); err != nil {
		t.Fatalf("build: %v", err)
	}

	sections, acquired, released := 0, 0, 0
	for _, line := range *lines {
		switch {
		case strings.HasPrefix(line, "parallel section start "):
			sections++
			for _, field := range []string{"section=", "units=", "cap=", "held="} {
				if !strings.Contains(line, field) {
					t.Errorf("a section start is missing %s: %s", field, line)
				}
			}
		case strings.HasPrefix(line, "sem acquired "):
			acquired++
			if !strings.Contains(line, "sem=build.workers") {
				t.Errorf("an acquire does not name the semaphore: %s", line)
			}
		case strings.HasPrefix(line, "sem released "):
			released++
		}
	}
	if sections == 0 {
		t.Fatalf("no parallel section was logged:\n%s", strings.Join(*lines, "\n"))
	}
	if acquired == 0 {
		t.Fatal("no semaphore slot was logged as taken, so none can be logged as stuck")
	}
	if acquired != released {
		t.Errorf("%d slots taken, %d returned — the pair does not balance", acquired, released)
	}
}

func TestTheTimingsFileNamesEveryStepItRecords(t *testing.T) {
	// "step 3" aggregates nothing. Every record has to carry the repo, ref or family it is about,
	// or the file cannot answer "what was slow" for anything but the run as a whole.
	root, _ := syncFixture(t)
	read := captureTimings(t)

	if _, err := build.Run(build.Options{
		Root: root, OutDir: t.TempDir(), Now: fixedBuildTime, Workers: 4,
	}); err != nil {
		t.Fatalf("build: %v", err)
	}
	records := read()
	if len(records) == 0 {
		t.Fatal("the build recorded no timings")
	}

	byKind := map[string][]string{}
	for _, r := range records {
		if r.Kind == "" {
			t.Errorf("a record has no kind: %+v", r)
		}
		if r.Name == "" {
			t.Errorf("a %s record has no name: %+v", r.Kind, r)
		}
		byKind[r.Kind] = append(byKind[r.Kind], r.Name)
	}

	for _, want := range []string{"build.site", "build.repos", "build.repo", "build.ref", "build.blob"} {
		if len(byKind[want]) == 0 {
			t.Errorf("no %q record; kinds recorded: %v", want, sortedNames(countOf(byKind)))
		}
	}
	if !contains(byKind["build.repo"], "alpha") {
		t.Errorf("no per-repo record for alpha; got %v", byKind["build.repo"])
	}
	// Per ref, named repo-first: without the slug every repository's "main" would aggregate into
	// one meaningless row.
	if !contains(byKind["build.ref"], "alpha@main") {
		t.Errorf("no per-ref record for alpha@main; got %v", byKind["build.ref"])
	}
	if !contains(byKind["build.ref"], "alpha@feat/zip") {
		t.Errorf("the second ref of alpha was not measured; got %v", byKind["build.ref"])
	}

	// Page counts, not just durations: a duration alone cannot tell a slow renderer from a big
	// repository.
	pages := int64(0)
	for _, r := range records {
		if r.Kind == "build.ref" {
			pages += r.Counts["pages"]
		}
	}
	if pages == 0 {
		t.Error("the per-ref records carry no page counts")
	}
}

func TestPerRepoPageCountsAreTheSameSeriallyAsInParallel(t *testing.T) {
	// The counters live on the Builder, and only the POOL resets them per unit — the serial path
	// hands every repository the same Builder with every previous repository's total still on it.
	// A count taken as a total instead of a difference is right in one mode and cumulative
	// nonsense in the other, which is invisible until someone reads the file.
	root, _ := syncFixture(t)

	byWorkers := map[int]map[string]int64{}
	for _, workers := range []int{1, 4} {
		read := captureTimings(t)
		if _, err := build.Run(build.Options{
			Root: root, OutDir: t.TempDir(), Now: fixedBuildTime, Workers: workers,
		}); err != nil {
			t.Fatalf("%d-worker build: %v", workers, err)
		}
		counts := map[string]int64{}
		for _, r := range read() {
			if r.Kind == "build.repo" {
				counts[r.Name] = r.Counts["pages"]
			}
		}
		byWorkers[workers] = counts
	}

	for slug, serial := range byWorkers[1] {
		if parallel := byWorkers[4][slug]; serial != parallel {
			t.Errorf("%s: %d pages recorded serially, %d in parallel", slug, serial, parallel)
		}
	}
	if len(byWorkers[1]) == 0 {
		t.Fatal("no per-repo records at all")
	}
}

func TestDiagnosticsChangeNothingAboutTheSite(t *testing.T) {
	// The rule the whole change lives under. A diagnostic that alters a byte of dist/ is a
	// determinism bug wearing a useful name, so the same build is run with the log and the
	// timings recorder fully on and fully off, and the two trees are compared file by file.
	root, _ := syncFixture(t)

	quiet := t.TempDir()
	if _, err := build.Run(build.Options{
		Root: root, OutDir: quiet, Now: fixedBuildTime, Workers: 4,
	}); err != nil {
		t.Fatalf("quiet build: %v", err)
	}

	captureLogs(t)
	captureTimings(t)
	loud := t.TempDir()
	if _, err := build.Run(build.Options{
		Root: root, OutDir: loud, Now: fixedBuildTime, Workers: 4,
	}); err != nil {
		t.Fatalf("instrumented build: %v", err)
	}

	a, b := hashTree(t, quiet), hashTree(t, loud)
	if len(a) != len(b) {
		t.Fatalf("quiet build emitted %d files, instrumented build emitted %d", len(a), len(b))
	}
	var differing []string
	for path, sum := range a {
		if b[path] != sum {
			differing = append(differing, path)
		}
	}
	if len(differing) > 0 {
		sort.Strings(differing)
		t.Fatalf("turning diagnostics on changed %d file(s):\n  %s",
			len(differing), strings.Join(differing, "\n  "))
	}
}

/* ---- helpers -------------------------------------------------------------- */

var fixedBuildTime = time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

// logFamilyOf pulls the family= field out of a captured line.
func logFamilyOf(line string) string {
	for _, field := range strings.Fields(line) {
		if v, ok := strings.CutPrefix(field, "family="); ok {
			return v
		}
	}
	return ""
}

func sortedNames(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func countOf(m map[string][]string) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = len(v)
	}
	return out
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
