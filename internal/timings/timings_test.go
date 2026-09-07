package timings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"frznforge/internal/logging"
)

// clock is a hand-cranked time source. The records this package writes are timestamps and
// durations, so every assertion about their content needs the clock to be an input rather than
// whatever the machine happened to be doing.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock {
	return &clock{t: time.Date(2026, 9, 6, 22, 49, 3, 512_000_000, time.UTC)}
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// recorderOn returns a Recorder writing into buf with a hand-cranked clock.
func recorderOn(buf *bytes.Buffer) (*Recorder, *clock) {
	c := newClock()
	r := NewRecorder(buf, "20260906T224903Z-4812")
	r.now = c.now
	return r, c
}

// The written line IS the documented schema. If this test needs changing, SchemaVersion and the
// package comment need changing with it — a viewer is written against both.
func TestRecordShapeIsTheDocumentedSchema(t *testing.T) {
	var buf bytes.Buffer
	r, c := recorderOn(&buf)

	s := r.Start("git.clone", "kieran/frznforge")
	c.advance(1500 * time.Millisecond)
	s.DoneWith(Counts{"files": 3})

	want := `{"v":1,"run":"20260906T224903Z-4812","id":"1","ts":"2026-09-06T22:49:03.512Z",` +
		`"kind":"git.clone","name":"kieran/frznforge","ms":1500,"counts":{"files":3}}` + "\n"
	if buf.String() != want {
		t.Fatalf("record shape changed\n got %s\nwant %s", buf.String(), want)
	}
}

// Sub-millisecond steps must not all round to zero: a build has thousands of them and an
// aggregate over zeros says nothing.
func TestSubMillisecondResolution(t *testing.T) {
	var buf bytes.Buffer
	r, c := recorderOn(&buf)

	s := r.Start("blob.render", "README.md")
	c.advance(420 * time.Microsecond)
	s.Done()

	var rec Record
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.MS != 0.42 {
		t.Fatalf("ms = %v, want 0.42", rec.MS)
	}
	if rec.Duration() != 420*time.Microsecond {
		t.Fatalf("Duration() = %v, want 420µs", rec.Duration())
	}
}

// Containment is recorded, not inferred: the parent carries span, the child carries the parent
// id, and the parent's line comes last because a span finishes after the steps it contains.
func TestChildLinksToParentAndMarksSpan(t *testing.T) {
	var buf bytes.Buffer
	r, c := recorderOn(&buf)

	span := r.Start("ingest.all", "")
	c.advance(10 * time.Millisecond)
	kid := span.Child("git.clone", "kieran/frznforge")
	c.advance(90 * time.Millisecond)
	kid.Done()
	span.Done()

	recs := mustParse(t, buf.String())
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2", len(recs))
	}
	child, parent := recs[0], recs[1]
	if child.Kind != "git.clone" {
		t.Fatalf("children must be written before their parent; got %q first", child.Kind)
	}
	if child.Parent != parent.ID {
		t.Errorf("child parent = %q, want %q", child.Parent, parent.ID)
	}
	if child.Span {
		t.Error("a childless step must not be marked as a span")
	}
	if !parent.Span {
		t.Error("a step with a child must be marked as a span")
	}
	if parent.MS != 100 || child.MS != 90 {
		t.Errorf("durations: parent %v child %v, want 100 and 90", parent.MS, child.MS)
	}
}

// `defer timings.Start(...).Done()` sits in code that runs whether or not timings were opened.
// Every path through the API has to survive the nil recorder and the nil step.
func TestZeroValuesAreNoOps(t *testing.T) {
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("panicked on a zero value: %v", p)
		}
	}()

	var nilStep *Step
	nilStep.Add("x", 1).Fail(errors.New("boom")).Done()
	nilStep.Child("a", "b").Done()

	var nilRec *Recorder
	nilRec.Start("a", "b").Add("x", 1).Done()
	if nilRec.RunID() != "" {
		t.Error("nil Recorder should have no run id")
	}
	if err := nilRec.Close(); err != nil {
		t.Errorf("nil Recorder Close: %v", err)
	}

	// And the package-level API with nothing installed.
	Install(nil)
	Start("a", "b").Add("x", 1).Done()
	if RunID() != "" {
		t.Error("RunID with nothing installed should be empty")
	}
	if err := Close(); err != nil {
		t.Errorf("Close with nothing installed: %v", err)
	}
}

// `defer s.Done()` alongside an explicit `s.DoneWith(...)` is the shape a caller reaches for
// first. It must produce one record, not two.
func TestDoneIsIdempotent(t *testing.T) {
	var buf bytes.Buffer
	r, _ := recorderOn(&buf)

	s := r.Start("build.repo", "a")
	s.DoneWith(Counts{"pages": 2})
	s.Done()

	if n := len(mustParse(t, buf.String())); n != 1 {
		t.Fatalf("got %d records, want 1", n)
	}
}

func TestFailIsRecorded(t *testing.T) {
	var buf bytes.Buffer
	r, c := recorderOn(&buf)

	s := r.Start("git.fetch", "kieran/frznforge")
	c.advance(30 * time.Second)
	s.Fail(errors.New("dial tcp: i/o timeout")).Done()

	rec := mustParse(t, buf.String())[0]
	if rec.Err != "dial tcp: i/o timeout" {
		t.Errorf("err = %q", rec.Err)
	}
	if rec.MS != 30000 {
		t.Errorf("a failed step must still carry its duration, got %v", rec.MS)
	}
}

// The build finishes hundreds of steps at once. Every record must be a whole line and every id
// must be unique, or a viewer's nesting collapses.
func TestConcurrentStepsProduceWholeUniqueRecords(t *testing.T) {
	var buf syncBuffer
	r := NewRecorder(&buf, "concurrent-run")

	const goroutines, each = 32, 25
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			span := r.Start("build.repo", fmt.Sprintf("repo-%d", g))
			for i := 0; i < each; i++ {
				kid := span.Child("build.page", fmt.Sprintf("page-%d-%d", g, i))
				kid.Add("bytes", int64(i))
				kid.Done()
			}
			span.Done()
		}(g)
	}
	wg.Wait()

	recs := mustParse(t, buf.String())
	if want := goroutines * (each + 1); len(recs) != want {
		t.Fatalf("got %d records, want %d", len(recs), want)
	}
	ids := map[string]bool{}
	for _, rec := range recs {
		if ids[rec.ID] {
			t.Fatalf("duplicate record id %q", rec.ID)
		}
		ids[rec.ID] = true
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Redaction is enforced at this sink too, because the timings file gets pasted into issues just
// like the log does.
func TestSecretsNeverReachTheFile(t *testing.T) {
	const token = "glpat-timingsRedaction01"
	logging.Redact(token)

	var buf bytes.Buffer
	r, _ := recorderOn(&buf)

	s := r.Start("git.clone", "https://oauth2:"+token+"@gitlab.com/o/r.git")
	s.Add("token", 1) // a count key that claims to be a secret
	s.Add("pages", 4)
	s.Fail(errors.New("GET https://gitlab.com/api/v4/x?private_token=" + token + " failed"))
	s.Done()

	out := buf.String()
	if strings.Contains(out, token) {
		t.Fatalf("token reached the timings file:\n%s", out)
	}
	rec := mustParse(t, out)[0]
	if _, ok := rec.Counts["token"]; ok {
		t.Errorf("a count keyed like a secret was written: %v", rec.Counts)
	}
	if rec.Counts["pages"] != 4 {
		t.Errorf("an ordinary count was lost: %v", rec.Counts)
	}
}

/* ---- the file ------------------------------------------------------------ */

// newTimingsDir claims a scratch directory BEFORE registering the teardown that closes the
// file. t.Cleanup is last-in-first-out, and on Windows a directory holding an open handle
// cannot be removed.
func newTimingsDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(func() { _ = Close() })
	return dir
}

// Append, not truncate: comparing a run against the ones before it is the whole point.
func TestOpenAppendsAcrossRuns(t *testing.T) {
	dir := newTimingsDir(t)

	for i := 0; i < 3; i++ {
		if err := Open(dir, nil); err != nil {
			t.Fatalf("Open: %v", err)
		}
		Start("build.all", "").Done()
		if err := Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}

	recs, err := ParseDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 {
		t.Fatalf("got %d records after three runs, want 3", len(recs))
	}
	if filepath.Base(Path(dir)) != "frznforge-timings.jsonl" {
		t.Errorf("file name changed: %s", Path(dir))
	}
}

// Records reach the disk as they are written, not at Close: the run this file describes best is
// the one that never got to close anything.
func TestRecordsAreOnDiskBeforeClose(t *testing.T) {
	dir := newTimingsDir(t)
	if err := Open(dir, nil); err != nil {
		t.Fatalf("Open: %v", err)
	}
	Start("git.clone", "kieran/frznforge").Done()

	recs, err := ParseDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records before Close, want 1", len(recs))
	}
	if recs[0].Run == "" || recs[0].Run != RunID() {
		t.Errorf("run id = %q, want %q", recs[0].Run, RunID())
	}
}

// A timings file that cannot be opened is reported once and the run carries on with no timings.
func TestOpenFailureIsNotFatal(t *testing.T) {
	dir := newTimingsDir(t)
	blocked := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	if err := Open(blocked, &stderr); err == nil {
		t.Fatal("Open returned nil for an unopenable path")
	}
	if !strings.Contains(stderr.String(), "continuing without it") {
		t.Fatalf("failure was not reported: %q", stderr.String())
	}
	Start("build.all", "").Done() // must not panic and must go nowhere
}

// The two diagnostics share one directory with the artifact. This is the wiring an agent will
// write in cmd/frznforge, run here so that a change to either package's file name or its open
// mode shows up as a failure rather than as two files quietly fighting over data/.
func TestBothDiagnosticsCoexistInOneDirectory(t *testing.T) {
	dir := newTimingsDir(t)
	t.Cleanup(func() { _ = logging.Close() })

	if err := logging.SetupFile(dir, nil); err != nil {
		t.Fatalf("SetupFile: %v", err)
	}
	if err := Open(dir, nil); err != nil {
		t.Fatalf("Open: %v", err)
	}
	slog.Debug("ingest start", "repos", 2)
	Start("ingest.all", "").Add("repos", 2).Done()

	for _, name := range []string{"frznforge.log", FileName} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if info.Size() == 0 {
			t.Errorf("%s is empty before Close", name)
		}
	}
	// Nothing else was created: an artifact directory is not a scratch directory.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("the diagnostics wrote %v, want exactly the two files", names)
	}
}

/* ---- helpers ------------------------------------------------------------- */

func mustParse(t *testing.T, s string) []Record {
	t.Helper()
	recs, err := Parse(strings.NewReader(s))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return recs
}
