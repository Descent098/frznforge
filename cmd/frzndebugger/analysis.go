package main

// Everything this program knows how to work out from the two files, with no terminal and no
// HTTP anywhere near it. The TUI and the web view both render what is computed here, which is
// the same reason internal/timings keeps Aggregate: two viewers that do their own arithmetic
// eventually disagree, and then neither can be trusted.

import (
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"frznforge/internal/logging"
	"frznforge/internal/timings"
)

// Data is one directory's worth of diagnostics, already parsed and already scrubbed.
type Data struct {
	Dir         string
	LogPath     string
	TimingsPath string

	// LogErr and TimingsErr are read failures, kept rather than returned: a missing timings file
	// must not stop the log from being shown, and vice versa. Someone reaching for this program
	// is already debugging something else.
	LogErr     error
	TimingsErr error

	Log     []LogRecord
	Records []timings.Record
}

// Load reads both files out of an ingest output directory.
//
// A file that does not exist is not an error, exactly as in timings.ParseFile: "no build has run
// yet" and "the last build wrote nothing" are the same empty answer.
func Load(dir string) Data {
	d := Data{
		Dir:         dir,
		LogPath:     logging.LogPath(dir),
		TimingsPath: timings.Path(dir),
	}
	if raw, err := os.ReadFile(d.LogPath); err == nil {
		d.Log = ParseLog(string(raw))
	} else if !os.IsNotExist(err) {
		d.LogErr = err
	}

	records, err := timings.ParseFile(d.TimingsPath)
	if err != nil {
		d.TimingsErr = err
	}
	d.Records = scrubRecords(records)
	return d
}

// scrubRecords is the timings half of the redaction pass ParseLog does for the log.
//
// The recorder already scrubbed these fields on the way out (timings.Recorder.write), so this is
// the second pass and it exists for the same reason: a file written by an older binary, or one
// mailed over from another machine, has not necessarily met a scrubber at all, and this process
// is the one about to put the bytes on a screen and into an HTTP response.
func scrubRecords(records []timings.Record) []timings.Record {
	for i := range records {
		records[i].Kind = logging.Scrub(records[i].Kind)
		records[i].Name = logging.Scrub(records[i].Name)
		records[i].Err = logging.Scrub(records[i].Err)
		if len(records[i].Counts) == 0 {
			continue
		}
		clean := make(timings.Counts, len(records[i].Counts))
		for k, v := range records[i].Counts {
			if logging.SecretKey(k) {
				continue
			}
			clean[logging.Scrub(k)] = v
		}
		records[i].Counts = clean
	}
	return records
}

/* ---- the unfinished step ------------------------------------------------- */

// The whole reason this program exists is a run that stopped, so start with the steps that never
// came back.
//
// A step writes its record when it FINISHES, so a step that never finished has no line in the
// file at all. It has to be inferred, and the format leaves exactly two fingerprints:
//
//  1. A HOLE IN THE ID SEQUENCE. Ids are decimal and assigned in Start order from 1
//     (Recorder.next), so a run whose steps all finished writes the ids 1..N with nothing
//     missing. A missing id is a step that started and never wrote a line.
//  2. A DANGLING PARENT. A record naming a parent that is not in the file. Strictly this is a
//     subset of (1) — a parent always draws a lower id than its children — but it is the
//     valuable subset, because those children say what the missing step was DOING and when it
//     was last seen alive.
//
// What neither fingerprint can catch: the innermost step of a run killed with nothing else in
// flight. Its id is the highest ever drawn, so it leaves no hole below the maximum and it has no
// children to point at it. Under a parallel build that case is rare — the other workers'
// steps carry the sequence past the stuck one — but it is real, and it is why this reports
// "at least" rather than "exactly".
//
// And the one way it can be WRONG rather than incomplete: timings.trim keeps whole contiguous
// blocks of lines sharing a run id, which is a whole run unless two frznforge processes shared
// one data directory and interleaved their lines. Half a run kept that way begins at some id in
// the middle, and every id below it reads as a hole. It takes concurrent builds AND a file over
// TrimAtBytes to happen at all; maxUnfinishedPerRun is what stops it costing anything worse than
// a wrong number.
type Unfinished struct {
	Run string
	// ID is the record id the step drew from the sequence, and all that is certainly known
	// about it: its kind and name were only ever going to be written at Done.
	ID string
	// Children are the finished steps that named this one as their parent, in file order. Empty
	// when the step was found by the hole in the sequence alone.
	Children []timings.Record
	// Since is the earliest child's start — the latest moment this step is known to have already
	// been running. Zero when nothing named it.
	Since time.Time
	// LastSeen is the latest child's end: the last moment this step is known to have still been
	// working. Zero when nothing named it.
	LastSeen time.Time
	// RunEnd is the end of the last record in the whole run, which is as close to "when the
	// process stopped" as the file gets.
	RunEnd time.Time
}

// AtLeast is a lower bound on how long the step ran: from the first moment it is known to have
// been running to the last record of its run. It is a bound and not a measurement — the step may
// have started well before its first child and may have kept running after the process's last
// line — so every caller labels it as one.
func (u Unfinished) AtLeast() time.Duration {
	if u.Since.IsZero() || !u.RunEnd.After(u.Since) {
		return 0
	}
	return u.RunEnd.Sub(u.Since)
}

// maxUnfinishedPerRun caps what a corrupt id can cost. A single line claiming id=900000000 would
// otherwise make this program report nine hundred million unfinished steps and then run out of
// memory in front of someone who was already having a bad day.
const maxUnfinishedPerRun = 200

// Unfinisheds finds every step that started and never finished, newest run first and then in id
// order. Truncated reports how many were dropped by the cap.
func Unfinisheds(records []timings.Record) (found []Unfinished, truncated int) {
	return UnfinishedsUntil(records, time.Time{})
}

// UnfinishedsUntil is Unfinisheds with a floor on when the newest run actually stopped.
//
// The timings file only knows when the last step FINISHED, and the step that matters here is the
// one that never did. On a killed build the last completed child landed 300 ms in while the run
// log kept writing for another four seconds — so the bound read "at least 304ms" for something
// that had been running 4.1 s, understating by thirteen times the one number the reader opened
// this program to see.
//
// The log's last record is a far better answer to "when did the process stop", so when it is
// later than the last timings record it wins. Older runs keep their own end: a later run's log
// says nothing about when an earlier one stopped.
func UnfinishedsUntil(records []timings.Record, logEnd time.Time) (found []Unfinished, truncated int) {
	order := timings.Runs(records)
	// Runs is oldest first; the run someone opened this program about is the newest.
	for i := len(order) - 1; i >= 0; i-- {
		run := order[i]
		// Newest run only — see the comment above.
		if i == len(order)-1 && logEnd.After(run.End) {
			run.End = logEnd
		}
		got, cut := unfinishedInRun(timings.Filter(records, run.ID), run)
		found = append(found, got...)
		truncated += cut
	}
	return found, truncated
}

func unfinishedInRun(records []timings.Record, run timings.Run) (found []Unfinished, truncated int) {
	present := map[string]bool{}
	var seq []int64
	for _, r := range records {
		if r.ID == "" || present[r.ID] {
			continue
		}
		present[r.ID] = true
		if n, err := strconv.ParseInt(r.ID, 10, 64); err == nil && n > 0 {
			seq = append(seq, n)
		}
	}
	sort.Slice(seq, func(i, j int) bool { return seq[i] < seq[j] })

	children := map[string][]timings.Record{}
	for _, r := range records {
		if r.Parent != "" && r.Parent != r.ID {
			children[r.Parent] = append(children[r.Parent], r)
		}
	}

	// The holes: 1..first-1, then every gap between consecutive ids. Counted first and built
	// second, so one absurd id costs a subtraction rather than an allocation per number — and so
	// the "and N more" the viewer prints is the real total rather than however many fitted.
	var holes int64
	prev := int64(0)
	for _, n := range seq {
		if n > prev+1 {
			holes += n - prev - 1
		}
		prev = n
	}
	if holes > 1<<30 {
		holes = 1 << 30 // int is 32 bits on some builds; a number this size is corruption anyway
	}

	ids := make([]string, 0, min(int(holes), maxUnfinishedPerRun))
	prev = 0
	for _, n := range seq {
		for missing := prev + 1; missing < n && len(ids) < maxUnfinishedPerRun; missing++ {
			ids = append(ids, strconv.FormatInt(missing, 10))
		}
		prev = n
		if len(ids) >= maxUnfinishedPerRun {
			break
		}
	}
	truncated = int(holes) - len(ids)

	// A dangling parent whose id never made it into the sequence — a non-decimal id from a writer
	// this program has not met. Rare, but reporting it costs nothing and hiding it costs the
	// only clue about a step that is definitely missing.
	dangling := make([]string, 0, len(children))
	for parent := range children {
		if !present[parent] {
			dangling = append(dangling, parent)
		}
	}
	// children is a map, so this sort is what stops the extra rows from moving between runs of
	// the viewer over the same file.
	sort.Strings(dangling)
	known := map[string]bool{}
	for _, id := range ids {
		known[id] = true
	}
	for _, id := range dangling {
		if known[id] {
			continue
		}
		if len(ids) >= maxUnfinishedPerRun {
			truncated++
			continue
		}
		ids = append(ids, id)
		known[id] = true
	}

	// Numeric where both sides are numbers, lexical otherwise: "10" must come after "9".
	sort.Slice(ids, func(i, j int) bool { return lessID(ids[i], ids[j]) })

	for _, id := range ids {
		u := Unfinished{Run: run.ID, ID: id, Children: children[id], RunEnd: run.End}
		for _, c := range u.Children {
			at := c.At()
			if at.IsZero() {
				continue
			}
			if u.Since.IsZero() || at.Before(u.Since) {
				u.Since = at
			}
			if end := at.Add(c.Duration()); end.After(u.LastSeen) {
				u.LastSeen = end
			}
		}
		found = append(found, u)
	}
	return found, truncated
}

func lessID(a, b string) bool {
	an, aerr := strconv.ParseInt(a, 10, 64)
	bn, berr := strconv.ParseInt(b, 10, 64)
	if aerr == nil && berr == nil {
		return an < bn
	}
	if aerr == nil {
		return true // numbers before anything else, so the ordinary case reads normally
	}
	if berr == nil {
		return false
	}
	return a < b
}

/* ---- the tree of aggregates ---------------------------------------------- */

// maxTreeDepth is a corrupt-file guard, not a limit anyone will meet: real nesting is a run
// containing repos containing refs, three or four deep. Without it a file whose parent links
// form a cycle — which no writer produces but a truncated disk write could fake — would recurse
// until the stack gave out.
const maxTreeDepth = 64

// Node is one row of the timings table: the aggregate for a (kind, name) pair at one position in
// the parent chain, plus the aggregate of everything those records contained.
//
// This is what makes "open a repo's total to see its refs" work. The top level aggregates the
// steps with no parent; opening one aggregates the children of every record in that group, so
// the number on the parent row and the numbers on the rows under it are computed from the same
// records by the same function.
type Node struct {
	timings.Stat
	// Key identifies this node across a re-sort, so an expanded row stays expanded when the
	// table is re-ordered. It is the path of kind/name pairs from the root.
	Key      string
	Depth    int
	Children []Node
}

// BuildTree groups records into the nesting the file records, and aggregates each level with
// timings.Aggregate.
//
// A record whose parent id is not in the file is a root — the format says so, because a run that
// was interrupted loses the parent and keeps the children, and orphaning them would hide them.
func BuildTree(records []timings.Record) []Node {
	byID := map[string]bool{}
	for _, r := range records {
		if r.ID != "" {
			byID[r.ID] = true
		}
	}
	kids := map[string][]int{}
	var roots []int
	for i, r := range records {
		if r.Parent == "" || r.Parent == r.ID || !byID[r.Parent] {
			roots = append(roots, i)
			continue
		}
		kids[r.Parent] = append(kids[r.Parent], i)
	}
	seen := map[int]bool{}
	nodes := groupLevel(records, roots, kids, seen, "", 0)

	// Anything still unconsumed belongs to a parent cycle — every record in it points at another
	// record in the file, so none of them is a root and the walk above never reached them. No
	// writer produces that, but a torn disk write can fake it, and a viewer that shows an empty
	// screen for a file full of records is worse than one that shows them in an odd place. They
	// are surfaced as extra roots, under their own key prefix so the expanded/collapsed state of
	// a real node cannot collide with theirs.
	for pass := 1; pass <= 8; pass++ {
		var left []int
		for i := range records {
			if !seen[i] {
				left = append(left, i)
			}
		}
		if len(left) == 0 {
			break
		}
		nodes = append(nodes, groupLevel(records, left, kids, seen, "\x1ecycle"+strconv.Itoa(pass), 0)...)
	}
	return nodes
}

type statKey struct{ kind, name string }

func groupLevel(records []timings.Record, idx []int, kids map[string][]int, seen map[int]bool, prefix string, depth int) []Node {
	if depth > maxTreeDepth || len(idx) == 0 {
		return nil
	}
	// seen consumes each record at the first place it appears. Together with the depth cap it is
	// what makes a cyclic parent link terminate instead of hanging the viewer.
	var (
		level []timings.Record
		byKey = map[statKey][]int{}
	)
	for _, i := range idx {
		if seen[i] {
			continue
		}
		seen[i] = true
		level = append(level, records[i])
		k := statKey{records[i].Kind, records[i].Name}
		byKey[k] = append(byKey[k], i)
	}
	if len(level) == 0 {
		return nil
	}

	stats := timings.Aggregate(level)
	out := make([]Node, 0, len(stats))
	for _, s := range stats {
		// byKey is only ever indexed, never ranged: its values were built in file order above, so
		// the child list below is the same on every machine that reads the same file.
		var childIdx []int
		for _, i := range byKey[statKey{s.Kind, s.Name}] {
			childIdx = append(childIdx, kids[records[i].ID]...)
		}
		key := prefix + "\x1f" + s.Kind + "\x00" + s.Name
		out = append(out, Node{
			Stat:     s,
			Key:      key,
			Depth:    depth,
			Children: groupLevel(records, childIdx, kids, seen, key, depth+1),
		})
	}
	return out
}

/* ---- sorting ------------------------------------------------------------- */

// SortKey names a sortable column. The order of the constants is the order the s key cycles in.
type SortKey int

const (
	SortTotal SortKey = iota
	SortMean
	SortCount
	SortBest
	SortWorst
	SortFailed
	SortKind
	SortName
)

// sortKeyNames are the column labels, indexed by SortKey. Also the names --sort would take if it
// ever grows one, which is why they are lowercase words rather than header text.
var sortKeyNames = []string{"total", "mean", "count", "best", "worst", "failed", "kind", "name"}

func (k SortKey) String() string {
	if int(k) < len(sortKeyNames) {
		return sortKeyNames[k]
	}
	return "total"
}

// SortNodes orders a tree in place, every level by the same column.
//
// Ties always break on kind then name, whatever the column, so the table never depends on the
// order records happened to finish in — which under a parallel build is not the same twice.
func SortNodes(nodes []Node, by SortKey, desc bool) {
	sort.SliceStable(nodes, func(i, j int) bool {
		a, b := nodes[i], nodes[j]
		less, equal := compareStat(a.Stat, b.Stat, by)
		if equal {
			if a.Kind != b.Kind {
				return a.Kind < b.Kind
			}
			return a.Name < b.Name
		}
		if desc {
			return !less
		}
		return less
	})
	for i := range nodes {
		SortNodes(nodes[i].Children, by, desc)
	}
}

// SortStats orders a flat aggregate the same way SortNodes orders a tree.
func SortStats(stats []timings.Stat, by SortKey, desc bool) {
	sort.SliceStable(stats, func(i, j int) bool {
		less, equal := compareStat(stats[i], stats[j], by)
		if equal {
			if stats[i].Kind != stats[j].Kind {
				return stats[i].Kind < stats[j].Kind
			}
			return stats[i].Name < stats[j].Name
		}
		if desc {
			return !less
		}
		return less
	})
}

func compareStat(a, b timings.Stat, by SortKey) (less, equal bool) {
	switch by {
	case SortMean:
		return a.Mean < b.Mean, a.Mean == b.Mean
	case SortCount:
		return a.Count < b.Count, a.Count == b.Count
	case SortBest:
		return a.Best < b.Best, a.Best == b.Best
	case SortWorst:
		return a.Worst < b.Worst, a.Worst == b.Worst
	case SortFailed:
		return a.Failed < b.Failed, a.Failed == b.Failed
	case SortKind:
		return a.Kind < b.Kind, a.Kind == b.Kind
	case SortName:
		return a.Name < b.Name, a.Name == b.Name
	default:
		return a.Total < b.Total, a.Total == b.Total
	}
}

/* ---- run scoping --------------------------------------------------------- */

// AllRuns is the --run value, and the run picker's entry, meaning "every run in the file".
const AllRuns = "all"

// Scope narrows records to one run. An empty or unknown id means the newest run, because that is
// what someone opening this program after a bad build wants to see; "all" means every record,
// which is what the file is appended to for.
//
// The chosen id is returned so a caller can say which run it is showing rather than leaving the
// user to guess.
func Scope(records []timings.Record, run string) ([]timings.Record, string) {
	if strings.EqualFold(run, AllRuns) {
		return records, AllRuns
	}
	order := timings.Runs(records)
	if len(order) == 0 {
		return nil, ""
	}
	for _, r := range order {
		if r.ID == run {
			return timings.Filter(records, run), run
		}
	}
	newest := order[len(order)-1].ID
	return timings.Filter(records, newest), newest
}
