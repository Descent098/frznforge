// Package timings records how long each step of a run took, into
// <outDir>/frznforge-timings.jsonl.
//
// It is deliberately NOT part of internal/logging. The log answers "what happened" and only the
// last run's copy is worth keeping; timings answer "what was slow", and that question is only
// answerable by comparing a run against the ones before it. One file truncates, the other
// appends; putting both concerns in one file would have forced one of those two to be wrong.
//
// # The file format
//
// JSON Lines: one JSON object per line, appended, flushed per record. JSON Lines rather than a
// JSON array because a run that is killed — the run whose timings you most want — leaves a file
// that is still parseable up to its last complete line. An array would leave a syntax error.
//
// Every record has this shape (whitespace added here only):
//
//	{"v":1,"run":"20260906T224903Z-4812","id":"7","parent":"3","span":true,
//	 "ts":"2026-09-06T22:49:03.512Z","kind":"repo.build","name":"kieran/frznforge",
//	 "ms":1234.567,"counts":{"pages":812},"err":"clone failed"}
//
//	v       int     Schema version, currently 1. A reader that meets a higher version should
//	                keep the record and render the fields it knows rather than drop the line.
//	run     string  Run id — identical on every record one process writes, and sortable, so
//	                grouping by it separates this build from the previous one.
//	id      string  Record id, unique within the run, decimal, assigned in Start order.
//	parent  string  The id of the containing step. Absent on a top-level step.
//	span    bool    True when this step contained at least one child. Absent when false.
//	ts      string  RFC 3339 in UTC with milliseconds. The moment the step STARTED, not the
//	                moment the line was written — end time is ts + ms.
//	kind    string  What sort of step: "git.clone", "ingest.repo", "build.family". Dotted,
//	                lowercase, coarse enough to aggregate over.
//	name    string  The subject: a repo slug, a ref, a page family. May be absent.
//	ms      number  Wall-clock duration in milliseconds, fractional to microsecond resolution.
//	counts  object  Optional string → integer counts (pages, bytes, files). Absent when empty.
//	err     string  Optional. Present when the step recorded a failure; a step that failed
//	                still gets a record, because "slow" is usually "timed out".
//
// # What a reader must tolerate
//
//   - A parent is written AFTER its children, because a span finishes last. Read the whole file
//     before nesting anything.
//   - A parent may be missing entirely when a run was interrupted. A record whose parent id is
//     not in the file is a root.
//   - The last line may be truncated. Parse drops an incomplete final line without complaining.
//   - Two frznforge processes sharing one data directory interleave whole lines but never split
//     one, because each record is a single O_APPEND write.
//   - Records within a run appear in completion order, which under a parallel build is not
//     deterministic. Nothing here is an input to the site: this file never reaches dist/ or
//     forge.json, and the build's output does not depend on whether it was written at all.
//
// # Reading the clock
//
// The house rule is that nothing reads the clock, because two machines must emit identical
// bytes. This package is the exception that proves it: measuring elapsed time is the entire
// point, and the exception is safe precisely because the output is a diagnostic file outside
// the artifact. No other package should copy the pattern.
package timings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"frznforge/internal/logging"
)

// SchemaVersion is the "v" written on every record. Bump it when a field changes meaning, not
// when one is added: a reader is required to ignore fields it does not know.
const SchemaVersion = 1

// FileName is the timings file inside the ingest output directory.
const FileName = "frznforge-timings.jsonl"

// Path is where Open writes for a given ingest output directory.
func Path(outDir string) string { return filepath.Join(outDir, FileName) }

// Counts is the optional per-step tally: pages rendered, bytes written, refs walked.
type Counts map[string]int64

// Record is one line of the file. The json tags ARE the schema documented above; changing one
// is a schema change.
type Record struct {
	Version int     `json:"v"`
	Run     string  `json:"run"`
	ID      string  `json:"id"`
	Parent  string  `json:"parent,omitempty"`
	Span    bool    `json:"span,omitempty"`
	TS      string  `json:"ts"`
	Kind    string  `json:"kind"`
	Name    string  `json:"name,omitempty"`
	MS      float64 `json:"ms"`
	Counts  Counts  `json:"counts,omitempty"`
	Err     string  `json:"err,omitempty"`
}

// At is the moment the step started, or the zero time when ts is missing or malformed.
func (r Record) At() time.Time {
	t, err := time.Parse(time.RFC3339, r.TS)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// Duration is the step's wall-clock duration.
//
// Rounded, not truncated: ms is a decimal that does not land exactly on a binary float, so
// 1234.567 would otherwise come back a nanosecond short of what was measured and an aggregate
// over thousands of records would drift.
func (r Record) Duration() time.Duration {
	return time.Duration(math.Round(r.MS * float64(time.Millisecond)))
}

/* ---- the recorder ------------------------------------------------------- */

// Recorder is one process's sink. Exported so a test — or a caller that wants timings somewhere
// other than a file — can hold its own instead of the process-wide one.
type Recorder struct {
	runID string
	now   func() time.Time

	// mu covers the write, so a record is one line and two goroutines finishing at the same
	// moment cannot interleave halves of theirs. The build finishes hundreds of steps
	// concurrently; this lock is held for the length of one Write and nothing else.
	mu sync.Mutex
	w  io.Writer
	c  io.Closer

	next atomic.Int64
}

// NewRecorder returns a Recorder writing one line per record to w.
//
// It does not own w unless w is also an io.Closer, in which case Close closes it. A nil w
// yields a Recorder whose steps are no-ops, which is what makes "timings not configured" the
// same code path as "timings configured".
func NewRecorder(w io.Writer, runID string) *Recorder {
	if w == nil {
		return nil
	}
	r := &Recorder{runID: runID, w: w, now: time.Now}
	r.c, _ = w.(io.Closer)
	return r
}

// RunID is the id stamped on every record this Recorder writes.
func (r *Recorder) RunID() string {
	if r == nil {
		return ""
	}
	return r.runID
}

// Close closes the underlying writer if it owns one. Safe on a nil Recorder.
func (r *Recorder) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.c
	r.w, r.c = nil, nil
	if c == nil {
		return nil
	}
	return c.Close()
}

// write emits one record. Everything that could carry a credential goes through the same
// scrubber the log uses — a repo name can be a URL, and an error string very often is one.
// Redaction belongs at the sink here for the same reason it does there: a call site that has to
// remember is a call site that will forget.
func (r *Recorder) write(rec Record) {
	rec.Kind = logging.Scrub(rec.Kind)
	rec.Name = logging.Scrub(rec.Name)
	rec.Err = logging.Scrub(rec.Err)
	if len(rec.Counts) > 0 {
		clean := make(Counts, len(rec.Counts))
		for k, v := range rec.Counts {
			// A count is an integer and cannot itself be a secret, but a key named "token" would
			// still be a claim about one, and this file gets pasted into issues too.
			if logging.SecretKey(k) {
				continue
			}
			clean[logging.Scrub(k)] = v
		}
		rec.Counts = clean
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// Escaping off: encoding/json escapes the angle brackets and ampersand by default, which
	// turns a URL in an error message into a wall of unicode escapes for no benefit. This file
	// is read by a person and never embedded in HTML.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(rec); err != nil {
		return // a record that cannot be encoded is not worth failing a build over
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.w == nil {
		return
	}
	// One Write per record, on a handle opened O_APPEND: that is what keeps the line whole and
	// what makes the tail survive a killed process. There is no user-space buffer to lose.
	_, _ = r.w.Write(buf.Bytes())
}

/* ---- steps -------------------------------------------------------------- */

// Step is one measured interval. The zero value and a nil pointer are both safe no-ops, so a
// caller never has to ask whether timings are configured.
type Step struct {
	rec    *Recorder
	id     string
	parent string
	kind   string
	name   string
	ts     time.Time

	mu     sync.Mutex
	counts Counts
	err    string

	span atomic.Bool
	done atomic.Bool
}

// off is returned when there is no recorder. It is shared rather than allocated per call
// because `defer timings.Start(k, n).Done()` appears in hot loops and must cost nothing when
// timings are off; every method below returns early on rec == nil, so nothing mutates it.
var off = &Step{}

// Start begins a top-level step on r. Idiomatic use is one line:
//
//	defer r.Start("git.clone", slug).Done()
//
// When counts are only known at the end, keep the handle and use Add or DoneWith.
func (r *Recorder) Start(kind, name string) *Step {
	if r == nil {
		return off
	}
	return &Step{
		rec:  r,
		id:   strconv.FormatInt(r.next.Add(1), 10),
		kind: kind,
		name: name,
		ts:   r.now(),
	}
}

// ID is this step's record id, or "" when timings are off. A caller that cannot carry the
// *Step itself across a boundary can carry this and rebuild the link.
func (s *Step) ID() string {
	if s == nil || s.rec == nil {
		return ""
	}
	return s.id
}

// Child begins a step contained by s, and marks s as a span.
//
// Containment is recorded rather than inferred: a viewer nests by parent id instead of guessing
// from overlapping timestamps, which under a parallel build would nest the wrong things.
//
// Call it before the parent's Done. A child started afterwards still records and still points
// at the right parent, but the parent's line is already on disk without its span flag, so a
// viewer sees a nested step under a parent that claims to have none.
//
// Safe from several goroutines: the parallel build starts a repo's page steps from whichever
// worker drew them.
func (s *Step) Child(kind, name string) *Step {
	if s == nil || s.rec == nil {
		return off
	}
	s.span.Store(true)
	child := s.rec.Start(kind, name)
	child.parent = s.id
	return child
}

// Add accumulates a count on an in-flight step, so a caller can still `defer s.Done()` and fill
// the numbers in as it goes. Safe from several goroutines.
func (s *Step) Add(key string, n int64) *Step {
	if s == nil || s.rec == nil {
		return s
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.counts == nil {
		s.counts = Counts{}
	}
	s.counts[key] += n
	return s
}

// Fail attaches an error to the step. The step is still recorded when it finishes: a failure
// that took thirty seconds is exactly the thing this file exists to show.
func (s *Step) Fail(err error) *Step {
	if s == nil || s.rec == nil || err == nil {
		return s
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err.Error()
	return s
}

// Done ends the step and writes its record. A second call does nothing, so
// `defer s.Done()` alongside an explicit `s.DoneWith(...)` is safe rather than a double line.
func (s *Step) Done() { s.finish(nil) }

// DoneWith ends the step, merging counts in first:
//
//	s.DoneWith(timings.Counts{"pages": n, "bytes": b})
func (s *Step) DoneWith(counts Counts) { s.finish(counts) }

func (s *Step) finish(extra Counts) {
	if s == nil || s.rec == nil {
		return
	}
	if !s.done.CompareAndSwap(false, true) {
		return
	}
	elapsed := s.rec.now().Sub(s.ts)

	s.mu.Lock()
	for k, v := range extra {
		if s.counts == nil {
			s.counts = Counts{}
		}
		s.counts[k] += v
	}
	counts, errText := s.counts, s.err
	s.mu.Unlock()

	s.rec.write(Record{
		Version: SchemaVersion,
		Run:     s.rec.runID,
		ID:      s.id,
		Parent:  s.parent,
		Span:    s.span.Load(),
		TS:      s.ts.UTC().Format("2006-01-02T15:04:05.000Z"),
		Kind:    s.kind,
		Name:    s.name,
		MS:      float64(elapsed.Microseconds()) / 1000,
		Counts:  counts,
		Err:     errText,
	})
}

/* ---- the process-wide recorder ------------------------------------------ */

var std atomic.Pointer[Recorder]

// Open installs the process-wide recorder, appending to <outDir>/frznforge-timings.jsonl.
//
// A failure is reported once to errOut (os.Stderr when nil) and returned; the returned error is
// for tests. Production callers ignore it — timings are diagnostics, and a data directory that
// cannot be written must not be the reason a build refuses to run. Every Start after a failed
// Open is a no-op.
//
// Calling it twice closes the first file. The last call wins.
func Open(outDir string, errOut io.Writer) error {
	if errOut == nil {
		errOut = os.Stderr
	}
	path := Path(outDir)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return report(errOut, path, err)
	}
	// Trim before taking the append handle: the trim rewrites the file, and doing that under a
	// handle that is already appending is how you lose the run you are about to record.
	//
	// A trim that fails costs nothing but a file that stays large, so it goes in the run log
	// rather than on the user's terminal — a warning about housekeeping for a diagnostic file
	// is exactly the noise that teaches people to ignore warnings.
	if err := trim(path); err != nil {
		slog.Debug("timings: could not trim the history", "path", path, "err", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return report(errOut, path, err)
	}
	Install(NewRecorder(f, NewRunID(time.Now(), os.Getpid())))
	return nil
}

func report(errOut io.Writer, path string, err error) error {
	wrapped := fmt.Errorf("could not open the timings log %s: %w", path, err)
	fmt.Fprintln(errOut, "warning: "+logging.Scrub(wrapped.Error())+" (continuing without it)")
	return wrapped
}

// Install replaces the process-wide recorder, closing the one it replaces.
func Install(r *Recorder) {
	if old := std.Swap(r); old != nil && old != r {
		_ = old.Close()
	}
}

// Close closes the process-wide recorder. Safe when none was opened, and safe twice.
func Close() error { return std.Swap(nil).Close() }

// RunID is the current run's id, or "" when timings are not configured.
func RunID() string { return std.Load().RunID() }

// Start begins a top-level step on the process-wide recorder:
//
//	defer timings.Start("git.clone", slug).Done()
func Start(kind, name string) *Step { return std.Load().Start(kind, name) }

// NewRunID builds a run id: a compact sortable UTC timestamp plus the pid.
//
// Sortable so a viewer can order runs without parsing every record's ts, and pid-suffixed so
// two frznforge processes started in the same second do not merge into one run.
func NewRunID(now time.Time, pid int) string {
	return now.UTC().Format("20060102T150405Z") + "-" + strconv.Itoa(pid)
}

/* ---- growth cap --------------------------------------------------------- */

// The file appends forever unless something stops it, and "forever" here is a few hundred bytes
// per step times thousands of steps per build. Left alone it would eventually be the slowest
// part of the build it is measuring.
//
// The cap: when the file exceeds TrimAtBytes at open time, the newest whole runs that fit in
// KeepBytes — and at most KeepRuns of them — are kept and the rest is dropped. WHOLE runs,
// because half a run reads as a fast run and would poison the aggregate; the newest, because
// comparing against last week is not what anyone does with this. The trim happens once per
// process, at open, so it never competes with the build for I/O.
const (
	TrimAtBytes = 4 << 20 // 4 MiB: large enough that a normal user never triggers it
	KeepBytes   = 2 << 20 // trim down to half, so the next trim is many runs away
	KeepRuns    = 25
)

func trim(path string) error {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Size() <= TrimAtBytes {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	kept := tailRuns(data, KeepBytes, KeepRuns)
	if len(kept) == len(data) {
		return nil
	}
	return os.WriteFile(path, kept, 0o644)
}

// tailRuns returns the suffix of data holding the newest whole runs within the budgets. A run
// is a contiguous block of lines sharing a run id; an incomplete final line is discarded.
func tailRuns(data []byte, maxBytes int, maxRuns int) []byte {
	end := bytes.LastIndexByte(data, '\n')
	if end < 0 {
		return nil
	}
	body := data[:end+1]

	// The offset of the first line of each contiguous block of records sharing a run id, oldest
	// first. A line that does not parse reads as run "" and therefore starts a block of its own,
	// which is the conservative answer: it can be dropped without taking a real run with it.
	var starts []int
	prevRun := ""
	for off := 0; off < len(body); {
		nl := bytes.IndexByte(body[off:], '\n')
		var head struct {
			Run string `json:"run"`
		}
		_ = json.Unmarshal(body[off:off+nl], &head)
		if head.Run != prevRun {
			starts = append(starts, off)
			prevRun = head.Run
		}
		off += nl + 1
	}
	if len(starts) == 0 {
		return body
	}

	cut := starts[len(starts)-1] // always keep the newest run, whatever it costs
	runs := 1
	for i := len(starts) - 2; i >= 0; i-- {
		if runs >= maxRuns || len(body)-starts[i] > maxBytes {
			break
		}
		cut = starts[i]
		runs++
	}
	return body[cut:]
}

/* ---- aggregation -------------------------------------------------------- */

// Stat is the aggregate for one (kind, name) pair.
type Stat struct {
	Kind   string
	Name   string
	Count  int           // records seen
	Failed int           // of those, how many carried an error
	Runs   int           // distinct run ids that contributed
	Total  time.Duration // sum of durations
	Mean   time.Duration // Total / Count, truncated to the nanosecond
	Best   time.Duration // fastest
	Worst  time.Duration // slowest
	Counts Counts        // counts summed across every record
}

// Aggregate summarises records by (kind, name).
//
// It lives here rather than in a viewer so the TUI and the web view cannot disagree about what
// "worst" means. Worst is the single slowest occurrence, not a percentile: with three runs of a
// step there is no percentile worth computing, and the outlier is the thing being hunted.
//
// The result is sorted by Total descending — the answer to "what was slow" reads off the top —
// with ties broken by kind then name so the order never depends on map iteration.
func Aggregate(records []Record) []Stat {
	type key struct{ kind, name string }
	acc := map[key]*Stat{}
	runs := map[key]map[string]bool{}

	for _, r := range records {
		k := key{r.Kind, r.Name}
		s := acc[k]
		if s == nil {
			s = &Stat{Kind: r.Kind, Name: r.Name, Best: -1}
			acc[k] = s
			runs[k] = map[string]bool{}
		}
		d := r.Duration()
		s.Count++
		s.Total += d
		if r.Err != "" {
			s.Failed++
		}
		if s.Best < 0 || d < s.Best {
			s.Best = d
		}
		if d > s.Worst {
			s.Worst = d
		}
		for ck, cv := range r.Counts {
			if s.Counts == nil {
				s.Counts = Counts{}
			}
			s.Counts[ck] += cv
		}
		if r.Run != "" {
			runs[k][r.Run] = true
		}
	}

	out := make([]Stat, 0, len(acc))
	for k, s := range acc {
		if s.Best < 0 {
			s.Best = 0
		}
		s.Mean = s.Total / time.Duration(s.Count)
		s.Runs = len(runs[k])
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Total != out[j].Total {
			return out[i].Total > out[j].Total
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Run is one process's worth of records, for a viewer that compares this build against the last.
type Run struct {
	ID      string
	Start   time.Time     // earliest record start
	End     time.Time     // latest record end (its start plus its duration)
	Wall    time.Duration // End - Start: how long the run took, not the sum of its parts
	Records int
	Failed  int
}

// Runs groups records by run id, oldest first. Ordering is by start time with the id breaking
// ties, so two runs that began in the same millisecond still come out in a stable order.
func Runs(records []Record) []Run {
	byID := map[string]*Run{}
	for _, r := range records {
		run := byID[r.Run]
		if run == nil {
			run = &Run{ID: r.Run}
			byID[r.Run] = run
		}
		run.Records++
		if r.Err != "" {
			run.Failed++
		}
		at := r.At()
		if at.IsZero() {
			continue
		}
		if run.Start.IsZero() || at.Before(run.Start) {
			run.Start = at
		}
		if end := at.Add(r.Duration()); end.After(run.End) {
			run.End = end
		}
	}
	out := make([]Run, 0, len(byID))
	for _, r := range byID {
		if !r.Start.IsZero() && r.End.After(r.Start) {
			r.Wall = r.End.Sub(r.Start)
		}
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Start.Equal(out[j].Start) {
			return out[i].Start.Before(out[j].Start)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Filter returns the records belonging to one run, in file order.
func Filter(records []Record, runID string) []Record {
	out := make([]Record, 0, len(records))
	for _, r := range records {
		if r.Run == runID {
			out = append(out, r)
		}
	}
	return out
}
