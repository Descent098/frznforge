package main

import (
	"strings"
	"testing"
	"time"

	"frznforge/internal/timings"
)

func TestFormatDurationPicksTheUnitAndKeepsTheColumnWidth(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0"},
		{500 * time.Nanosecond, "500ns"},
		{1500 * time.Nanosecond, "1.50us"},
		{12300 * time.Nanosecond, "12.3us"},
		{1234 * time.Microsecond, "1.23ms"},
		{123456 * time.Microsecond, "123ms"},
		{1500 * time.Millisecond, "1.50s"},
		{90 * time.Second, "1m30.0s"},
	}
	for _, c := range cases {
		if got := FormatDuration(c.in); got != c.want {
			t.Errorf("FormatDuration(%v) = %q, want %q", c.in, got, c.want)
		}
	}
	// Nothing outside ASCII: a Windows console on the wrong code page turns a micro sign into
	// mojibake, and a debugging tool that looks broken is one more thing to debug.
	for _, c := range cases {
		for _, r := range FormatDuration(c.in) {
			if r > 127 {
				t.Fatalf("FormatDuration(%v) = %q contains a non-ASCII rune", c.in, FormatDuration(c.in))
			}
		}
	}
}

func TestFormatCountsSortsItsKeys(t *testing.T) {
	counts := timings.Counts{"pages": 812, "bytes": 5, "archives": 2}
	for i := 0; i < 20; i++ {
		if got := FormatCounts(counts); got != "archives=2 bytes=5 pages=812" {
			t.Fatalf("FormatCounts = %q; ranging the map would give a different line each time", got)
		}
	}
	if FormatCounts(nil) != "" {
		t.Error("no counts is an empty string, not a stray separator")
	}
}

func TestClipAndPadCountRunesNotBytes(t *testing.T) {
	if got := clip("kieran/frznforge", 10); got != "kieran/..." {
		t.Errorf("clip = %q, want a marked cut", got)
	}
	if got := clip("naïve-repo", 5); got != "na..." {
		t.Errorf("clip on a multi-byte string = %q", got)
	}
	if got := pad("ab", 5); got != "ab   " {
		t.Errorf("pad = %q", got)
	}
	if got := padLeft("ab", 5); got != "   ab" {
		t.Errorf("padLeft = %q", got)
	}
	if got := len([]rune(pad("naïve", 8))); got != 8 {
		t.Errorf("pad measured %d columns for a string with a multi-byte rune", got)
	}
}

func TestUnfinishedSummarySaysBothThings(t *testing.T) {
	text, style := UnfinishedSummary(nil, 0)
	if style != StyleDim || !strings.Contains(text, "no unfinished steps") {
		t.Errorf("clean run summary = %q/%v", text, style)
	}
	text, style = UnfinishedSummary([]Unfinished{{ID: "3"}}, 0)
	if style != StyleError || !strings.Contains(text, "1 UNFINISHED step") {
		t.Errorf("dirty run summary = %q/%v", text, style)
	}
	text, _ = UnfinishedSummary([]Unfinished{{ID: "3"}}, 4)
	if !strings.Contains(text, "5 UNFINISHED steps") {
		t.Errorf("summary must count the ones it could not list too, got %q", text)
	}
}

func TestUnfinishedRowsNameTheChildrenThatSurvived(t *testing.T) {
	found, truncated := Unfinisheds(records(t, killedRun))
	rows := UnfinishedRows(found, truncated, 120)
	joined := rowText(rows)
	for _, want := range []string{"UNFINISHED", "#3", "at least", "git.fetch kieran/beta", "ERR timed out"} {
		if !strings.Contains(joined, want) {
			t.Errorf("rows do not mention %q:\n%s", want, joined)
		}
	}
}

func TestUnfinishedRowsExplainACleanRun(t *testing.T) {
	joined := rowText(UnfinishedRows(nil, 0, 120))
	if !strings.Contains(joined, "no unfinished steps") || !strings.Contains(joined, "hole in the id sequence") {
		t.Errorf("a clean run should say what was looked for:\n%s", joined)
	}
}

func TestTimingsRowsOpenOnlyWhatIsExpanded(t *testing.T) {
	tree := BuildTree(records(t, completeRun))
	SortNodes(tree, SortTotal, true)

	closed := TimingsRows(tree, map[string]bool{}, 120)
	if len(closed) != 1 {
		t.Fatalf("closed tree = %d rows, want the one root:\n%s", len(closed), rowText(closed))
	}
	if !strings.HasPrefix(strings.TrimSpace(closed[0].Text), "+ build") {
		t.Errorf("a closed node with children is marked +, got %q", closed[0].Text)
	}
	if !closed[0].HasChildren || closed[0].Open {
		t.Errorf("row flags = %+v", closed[0])
	}

	opened := TimingsRows(tree, map[string]bool{tree[0].Key: true}, 120)
	if len(opened) != 3 {
		t.Fatalf("opened tree = %d rows, want root plus two repos:\n%s", len(opened), rowText(opened))
	}
	if !strings.Contains(rowText(opened), "ingest.repo kieran/alpha") {
		t.Errorf("children missing:\n%s", rowText(opened))
	}
}

func TestTimingsHeaderMarksTheSortedColumn(t *testing.T) {
	if got := TimingsHeader(SortTotal, true, 120).Text; !strings.Contains(got, "totalv") {
		t.Errorf("header = %q, want the total column marked descending", got)
	}
	if got := TimingsHeader(SortName, false, 120).Text; !strings.Contains(got, "step^") {
		t.Errorf("header = %q, want the name column marked ascending", got)
	}
}

func TestTimingsRowsStayInsideANarrowWindow(t *testing.T) {
	tree := BuildTree(records(t, completeRun))
	for _, width := range []int{40, 60, 80, 200} {
		for _, row := range TimingsRows(tree, allOpen(tree), width) {
			if n := len([]rune(row.Text)); n > width {
				t.Errorf("width %d produced a %d-column row: %q", width, n, row.Text)
			}
		}
	}
}

func TestLogRowsMarkTheLoudOnes(t *testing.T) {
	rows := LogRows(ParseLog(sampleLog), 200)
	if len(rows) != 5 {
		t.Fatalf("got %d rows", len(rows))
	}
	if rows[2].Style != StyleWarn {
		t.Errorf("WARN row style = %v", rows[2].Style)
	}
	if rows[3].Style != StyleError {
		t.Errorf("ERROR row style = %v", rows[3].Style)
	}
	if rows[4].Style != StyleWarn || !strings.Contains(rows[4].Text, "???") {
		t.Errorf("an unparsed line is marked and shown, got %q/%v", rows[4].Text, rows[4].Style)
	}
	if !strings.Contains(rows[0].Text, "args=rev-parse HEAD") {
		t.Errorf("attributes belong on the row: %q", rows[0].Text)
	}
}

func TestRunLineDescribesTheChosenRun(t *testing.T) {
	runs := timings.Runs(records(t, completeRun+killedRun))
	got := RunLine(runs, "20260906T224903Z-4812")
	for _, want := range []string{"2 of 2", "wall", "4 steps", "1 failed"} {
		if !strings.Contains(got, want) {
			t.Errorf("RunLine = %q, missing %q", got, want)
		}
	}
	if got := RunLine(runs, AllRuns); !strings.Contains(got, "all (2 in file)") {
		t.Errorf("all-runs line = %q", got)
	}
	if got := RunLine(nil, ""); !strings.Contains(got, "no records") {
		t.Errorf("empty line = %q", got)
	}
}

func rowText(rows []Row) string {
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(r.Text)
		b.WriteByte('\n')
	}
	return b.String()
}
