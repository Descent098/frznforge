package main

// The listing printed when there is no usable terminal.
//
// A viewer that cannot run has to print the data rather than fail: someone reaching for this
// program is already debugging something else, and "your console does not support VT sequences"
// is not an answer to the question they asked. So every path that cannot draw a screen — a
// redirected stdout, a console that refuses raw mode, --plain, a CI job — ends up here, and it
// shows the same three sections in the same order the screen does.
//
// It is one long listing on purpose. Paging belongs to the user's pager, which is better at it
// than anything written here would be, and a listing that ends in a prompt is a listing that
// cannot be redirected into a file.

import (
	"fmt"
	"io"
	"strconv"

	"frznforge/internal/timings"
)

// WritePlain prints the whole of what Data has to say, filtered as the flags asked.
func WritePlain(w io.Writer, data Data, filter LogFilter, run string, width int) error {
	if width < 40 {
		width = defaultCols
	}
	out := &plainWriter{w: w}

	out.line("frzndebugger  " + data.Dir)
	out.line("  log:     " + describeFile(data.LogPath, len(data.Log), "records", data.LogErr))
	out.line("  timings: " + describeFile(data.TimingsPath, len(data.Records), "steps", data.TimingsErr))
	out.line("")

	scoped, chosen := Scope(data.Records, run)
	unfinished, truncated := UnfinishedsUntil(scoped, LastLogTime(data.Log, chosen))

	summary, _ := UnfinishedSummary(unfinished, truncated)
	out.line("== " + summary)
	for _, row := range UnfinishedRows(unfinished, truncated, width)[1:] {
		out.line(row.Text)
	}
	out.line("")

	runs := timings.Runs(data.Records)
	out.line("== timings")
	out.line("  " + RunLine(runs, chosen))
	if len(runs) > 1 && chosen != AllRuns {
		out.line("  " + strconv.Itoa(len(runs)) + " runs in the file; --run=<id> or --run=all to widen")
	}
	if len(scoped) > 0 {
		tree := BuildTree(scoped)
		SortNodes(tree, SortTotal, true)
		header := TimingsHeader(SortTotal, true, width)
		out.line("  " + header.Text)
		// Fully expanded: on a screen the tree is opened one node at a time because the screen is
		// small, and neither of those reasons applies to a file.
		for _, row := range TimingsRows(tree, allOpen(tree), width) {
			out.line("  " + row.Text)
		}
	}
	out.line("")

	records := FilterLog(data.Log, filter)
	label := "== log (" + strconv.Itoa(len(records)) + " of " + strconv.Itoa(len(data.Log)) + " records"
	if filter.HasLevel {
		label += ", level>=" + filter.MinLevel.String()
	}
	if filter.Text != "" {
		label += ", matching " + strconv.Quote(filter.Text)
	}
	out.line(label + ")")
	for _, row := range LogRows(records, width) {
		out.line("  " + row.Text)
	}
	return out.err
}

// allOpen expands every node of a tree, for the listing that has no keyboard to open them with.
func allOpen(nodes []Node) map[string]bool {
	open := map[string]bool{}
	var walk func([]Node)
	walk = func(ns []Node) {
		for _, n := range ns {
			open[n.Key] = true
			walk(n.Children)
		}
	}
	walk(nodes)
	return open
}

func describeFile(path string, count int, noun string, err error) string {
	switch {
	case err != nil:
		return path + " -- could not be read: " + err.Error()
	case count == 0:
		return path + " -- not there, or empty"
	default:
		return path + " (" + strconv.Itoa(count) + " " + noun + ")"
	}
}

// plainWriter keeps the first write error and stops, so a closed pipe — `| head` is the normal
// way to read this — ends quietly instead of printing an error per remaining line.
type plainWriter struct {
	w   io.Writer
	err error
}

func (p *plainWriter) line(text string) {
	if p.err != nil {
		return
	}
	_, p.err = fmt.Fprintln(p.w, text)
}
