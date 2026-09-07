package main

// The screens, as values.
//
// Every view is a pure function from data and a width to a slice of styled rows. Nothing here
// touches a terminal, which is what lets the whole of the interface be tested by a program with
// no console attached — and what lets the same rows be written to a pipe when raw mode is not
// available.
//
// Everything below is ASCII. No box drawing, no micro sign, no arrows: a Windows console whose
// output code page is not 65001 renders those as mojibake, and a debugging tool that looks
// broken is one more thing to debug. The web view is where the typography lives.

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"frznforge/internal/timings"
)

// Style is what a row means, not what colour it is. The terminal layer picks the escape codes
// and the web view picks the CSS class, so neither has an opinion here.
type Style uint8

const (
	StylePlain Style = iota
	StyleDim
	StyleHead
	StyleAccent
	StyleWarn
	StyleError
)

// Row is one line of a view.
type Row struct {
	Text  string
	Style Style
	// Key identifies a timings node across a re-sort. Empty on a row that is not a tree node.
	Key string
	// HasChildren marks a node that can be opened; Open says whether it currently is.
	HasChildren bool
	Open        bool
	// Selectable is false for headers and separators, so moving the cursor skips them.
	Selectable bool
}

/* ---- formatting ---------------------------------------------------------- */

// FormatDuration renders a duration in the largest unit that keeps three significant figures.
//
// "us" rather than the micro sign for the reason in the file comment. Fixed decimals rather than
// trimmed ones because these numbers sit in a column and a ragged right edge is harder to scan
// than a trailing zero.
func FormatDuration(d time.Duration) string {
	switch {
	case d <= 0:
		return "0"
	case d < time.Microsecond:
		return strconv.FormatInt(int64(d), 10) + "ns"
	case d < time.Millisecond:
		return sigFigs(float64(d)/float64(time.Microsecond)) + "us"
	case d < time.Second:
		return sigFigs(float64(d)/float64(time.Millisecond)) + "ms"
	case d < time.Minute:
		return sigFigs(float64(d)/float64(time.Second)) + "s"
	default:
		minutes := int64(d / time.Minute)
		rest := float64(d%time.Minute) / float64(time.Second)
		return strconv.FormatInt(minutes, 10) + "m" + sigFigs(rest) + "s"
	}
}

func sigFigs(v float64) string {
	switch {
	case v < 10:
		return strconv.FormatFloat(v, 'f', 2, 64)
	case v < 100:
		return strconv.FormatFloat(v, 'f', 1, 64)
	default:
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
}

// FormatCounts renders a step's tallies with the keys sorted, because ranging the map would give
// a different line every time the same file was opened.
func FormatCounts(counts timings.Counts) string {
	if len(counts) == 0 {
		return ""
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + strconv.FormatInt(counts[k], 10)
	}
	return strings.Join(parts, " ")
}

// FormatTime is the timestamp shown against a record: the time of day, which is what someone
// comparing two lines in the same run actually reads. The date is in the log file itself.
func FormatTime(t time.Time) string {
	if t.IsZero() {
		return "--:--:--"
	}
	return t.UTC().Format("15:04:05.000")
}

// clip cuts a string to n columns, rune-aware so a multi-byte character is never cut in half,
// and marks the cut so nobody mistakes a truncated repository name for the whole one.
func clip(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	if n <= 3 {
		return string(runes[:n])
	}
	return string(runes[:n-3]) + "..."
}

// pad right-pads to n columns; padLeft right-aligns. Both count runes, not bytes.
func pad(s string, n int) string {
	s = clip(s, n)
	if d := n - len([]rune(s)); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

func padLeft(s string, n int) string {
	s = clip(s, n)
	if d := n - len([]rune(s)); d > 0 {
		return strings.Repeat(" ", d) + s
	}
	return s
}

/* ---- the unfinished view ------------------------------------------------- */

// UnfinishedSummary is the one line that says whether anything is wrong. It is the first thing
// on the timings screen and the first thing in the plain listing, because it is the reason
// someone opened this program.
func UnfinishedSummary(list []Unfinished, truncated int) (string, Style) {
	total := len(list) + truncated
	if total == 0 {
		return "no unfinished steps: every step that started also finished", StyleDim
	}
	noun := "steps"
	if total == 1 {
		noun = "step"
	}
	return strconv.Itoa(total) + " UNFINISHED " + noun + " -- started and never reported back", StyleError
}

// UnfinishedRows is the detail: one block per step that never came back, newest run first.
func UnfinishedRows(list []Unfinished, truncated int, width int) []Row {
	summary, style := UnfinishedSummary(list, truncated)
	rows := []Row{{Text: summary, Style: style}}
	if len(list) == 0 {
		rows = append(rows, Row{Text: "", Style: StylePlain})
		rows = append(rows, Row{
			Text:  "A step writes its record when it finishes, so a step still running when the",
			Style: StyleDim,
		})
		rows = append(rows, Row{
			Text:  "process stopped leaves a hole in the id sequence. There are no holes here.",
			Style: StyleDim,
		})
		return rows
	}

	run := ""
	for _, u := range list {
		if u.Run != run {
			run = u.Run
			rows = append(rows, Row{Text: "", Style: StylePlain})
			rows = append(rows, Row{Text: "run " + run, Style: StyleHead})
		}
		head := "  #" + u.ID + "  "
		if u.Since.IsZero() {
			// Found by the hole in the sequence alone: the step drew an id and wrote nothing, and
			// nothing else named it as a parent. There is genuinely nothing more to say about it.
			head += "started, never finished; nothing named it as a parent"
			rows = append(rows, Row{Text: clip(head, width), Style: StyleError, Selectable: true})
			continue
		}
		head += "running by " + FormatTime(u.Since) + ", still working at " + FormatTime(u.LastSeen) +
			" -- at least " + FormatDuration(u.AtLeast())
		rows = append(rows, Row{Text: clip(head, width), Style: StyleError, Selectable: true})
		rows = append(rows, Row{
			Text:  clip("      contained "+strconv.Itoa(len(u.Children))+" finished steps:", width),
			Style: StyleDim,
		})
		for i, c := range u.Children {
			if i >= 8 {
				rows = append(rows, Row{
					Text:  clip("        ... and "+strconv.Itoa(len(u.Children)-8)+" more", width),
					Style: StyleDim,
				})
				break
			}
			line := "        " + FormatTime(c.At()) + "  " + padLeft(FormatDuration(c.Duration()), 9) +
				"  " + c.Kind
			if c.Name != "" {
				line += " " + c.Name
			}
			style := StylePlain
			if c.Err != "" {
				line += "  ERR " + c.Err
				style = StyleWarn
			}
			rows = append(rows, Row{Text: clip(line, width), Style: style})
		}
	}
	if truncated > 0 {
		rows = append(rows, Row{Text: "", Style: StylePlain})
		rows = append(rows, Row{
			Text:  "... and " + strconv.Itoa(truncated) + " more not listed (the file claims more missing ids than this can be)",
			Style: StyleWarn,
		})
	}
	return rows
}

/* ---- the timings view ---------------------------------------------------- */

// The fixed-width right-hand columns. The name column takes whatever is left, which is what
// makes the table usable in an 80-column window and a 200-column one.
const (
	colCount  = 6
	colTotal  = 10
	colMean   = 10
	colBest   = 10
	colWorst  = 10
	colFailed = 5
	// wideFixed and narrowFixed are the two layouts, each including the single space between
	// columns. Narrow drops best, worst and the failure count rather than clipping the numbers
	// off the right-hand edge: a truncated total is worse than a missing best.
	wideFixed   = colCount + colTotal + colMean + colBest + colWorst + colFailed + 6
	narrowFixed = colCount + colTotal + colMean + 3
	// minName is the narrowest a step's name may get before the whole row is simply clipped.
	minName = 10
)

// timingsLayout picks the column set for a width and reports how much the name column gets.
func timingsLayout(width int) (nameWidth int, wide bool) {
	if n := width - wideFixed; n >= minName {
		return n, true
	}
	if n := width - narrowFixed; n >= minName {
		return n, false
	}
	return minName, false
}

// TimingsHeader is the column header, marked with the column currently sorted on.
func TimingsHeader(by SortKey, desc bool, width int) Row {
	mark := func(k SortKey, label string) string {
		if k != by {
			return label
		}
		if desc {
			return label + "v"
		}
		return label + "^"
	}
	nameWidth, wide := timingsLayout(width)
	text := pad(mark(SortName, "step"), nameWidth) + " " +
		padLeft(mark(SortCount, "n"), colCount) + " " +
		padLeft(mark(SortTotal, "total"), colTotal) + " " +
		padLeft(mark(SortMean, "mean"), colMean)
	if wide {
		text += " " + padLeft(mark(SortBest, "best"), colBest) +
			" " + padLeft(mark(SortWorst, "worst"), colWorst) +
			" " + padLeft(mark(SortFailed, "fail"), colFailed)
	}
	return Row{Text: clip(text, width), Style: StyleHead}
}

// TimingsRows flattens the tree into the rows currently visible, respecting which nodes are open.
func TimingsRows(nodes []Node, open map[string]bool, width int) []Row {
	nameWidth, wide := timingsLayout(width)
	var rows []Row
	var walk func(ns []Node, depth int)
	walk = func(ns []Node, depth int) {
		for _, n := range ns {
			marker := "  "
			if len(n.Children) > 0 {
				marker = "+ "
				if open[n.Key] {
					marker = "- "
				}
			}
			label := strings.Repeat("  ", depth) + marker + n.Kind
			if n.Name != "" {
				label += " " + n.Name
			}
			style := StylePlain
			if n.Failed > 0 {
				style = StyleWarn
			}
			text := pad(label, nameWidth) + " " +
				padLeft(strconv.Itoa(n.Count), colCount) + " " +
				padLeft(FormatDuration(n.Total), colTotal) + " " +
				padLeft(FormatDuration(n.Mean), colMean)
			if wide {
				text += " " + padLeft(FormatDuration(n.Best), colBest) +
					" " + padLeft(FormatDuration(n.Worst), colWorst) +
					" " + padLeft(failedCell(n.Failed), colFailed)
			}
			rows = append(rows, Row{
				Text:        clip(text, width),
				Style:       style,
				Key:         n.Key,
				HasChildren: len(n.Children) > 0,
				Open:        open[n.Key],
				Selectable:  true,
			})
			if open[n.Key] {
				walk(n.Children, depth+1)
			}
		}
	}
	walk(nodes, 0)
	return rows
}

func failedCell(n int) string {
	if n == 0 {
		return "."
	}
	return strconv.Itoa(n)
}

// NodeDetail is the expanded description of one selected row: what it counted, and how many runs
// contributed. Shown under the table rather than in it, because these are the numbers you want
// once and the table is the thing you scan.
func NodeDetail(n Node) string {
	parts := []string{
		n.Kind + " " + n.Name,
		strconv.Itoa(n.Count) + " records over " + strconv.Itoa(n.Runs) + " runs",
	}
	if n.Failed > 0 {
		parts = append(parts, strconv.Itoa(n.Failed)+" failed")
	}
	if c := FormatCounts(n.Counts); c != "" {
		parts = append(parts, c)
	}
	return strings.Join(parts, "  |  ")
}

/* ---- the log view -------------------------------------------------------- */

// LogRows renders filtered log records. One row per record: a log line that wraps is a log line
// you cannot scan, and the raw file is right there for the whole text.
func LogRows(records []LogRecord, width int) []Row {
	rows := make([]Row, 0, len(records))
	for _, r := range records {
		if r.Unparsed {
			rows = append(rows, Row{Text: clip("  ???  "+r.Raw, width), Style: StyleWarn, Selectable: true})
			continue
		}
		style := StylePlain
		switch {
		case r.Error():
			style = StyleError
		case r.Warning():
			style = StyleWarn
		case r.LevelValue < 0:
			style = StyleDim
		}
		text := padLeft(shortTime(r.Time), 12) + " " + pad(shortLevel(r.Level), 5) + " " + r.Msg
		for _, a := range r.Attrs {
			text += " " + a.Key + "=" + a.Value
		}
		rows = append(rows, Row{Text: clip(text, width), Style: style, Selectable: true})
	}
	return rows
}

// shortTime keeps the time of day off a full "2026-09-06T22:49:03.512-07:00" stamp. The date and
// the offset are in the file; on screen they cost a third of the line and say nothing that
// distinguishes one record from the next.
func shortTime(stamp string) string {
	if i := strings.IndexByte(stamp, 'T'); i >= 0 && len(stamp) > i+1 {
		stamp = stamp[i+1:]
	}
	// Trim the UTC offset, which is a fixed suffix on every record of the same file.
	if i := strings.LastIndexAny(stamp, "+-"); i > 0 {
		stamp = stamp[:i]
	}
	return strings.TrimSuffix(stamp, "Z")
}

// shortLevel trims to the five columns the header allows. "ERROR" fits; "ERROR+2" is clipped to
// "ERROR", which is the part that decides how alarmed to be.
func shortLevel(level string) string {
	if len(level) > 5 {
		return level[:5]
	}
	return level
}

/* ---- runs ---------------------------------------------------------------- */

// RunLine describes the selected run for the header bar.
func RunLine(runs []timings.Run, selected string) string {
	if selected == AllRuns {
		return "run: all (" + strconv.Itoa(len(runs)) + " in file)"
	}
	for i, r := range runs {
		if r.ID != selected {
			continue
		}
		line := "run: " + r.ID + " (" + strconv.Itoa(i+1) + " of " + strconv.Itoa(len(runs)) + ")" +
			"  " + FormatDuration(r.Wall) + " wall, " + strconv.Itoa(r.Records) + " steps"
		if r.Failed > 0 {
			line += ", " + strconv.Itoa(r.Failed) + " failed"
		}
		return line
	}
	return "run: none -- the timings file has no records"
}
