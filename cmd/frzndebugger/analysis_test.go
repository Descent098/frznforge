package main

import (
	"strconv"
	"strings"
	"testing"

	"frznforge/internal/timings"
)

func records(t *testing.T, jsonl string) []timings.Record {
	t.Helper()
	recs, err := timings.Parse(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return recs
}

func TestUnfinishedFindsTheHoleInTheIdSequence(t *testing.T) {
	found, truncated := Unfinisheds(records(t, killedRun))
	if truncated != 0 {
		t.Errorf("truncated = %d, want 0", truncated)
	}
	if len(found) != 1 {
		t.Fatalf("got %d unfinished, want the one missing id: %+v", len(found), found)
	}
	u := found[0]
	if u.ID != "3" {
		t.Errorf("id = %q, want 3 -- 1, 2, 4 and 5 are on disk", u.ID)
	}
	if len(u.Children) != 2 {
		t.Fatalf("got %d children, want the two steps that named it as their parent", len(u.Children))
	}
	if u.Since.IsZero() || u.LastSeen.IsZero() {
		t.Fatal("with children present, both bounds are known")
	}
	// Earliest child starts at .300 and the run's last record ends 5s after .000, so the bound is
	// from .300 to 5.000.
	if got := u.AtLeast().Milliseconds(); got != 4700 {
		t.Errorf("AtLeast = %dms, want 4700 (first sign of life to the run's last record)", got)
	}
}

func TestUnfinishedReportsNothingForACleanRun(t *testing.T) {
	found, truncated := Unfinisheds(records(t, completeRun))
	if len(found) != 0 || truncated != 0 {
		t.Fatalf("a run whose ids are 1..3 with no holes has no unfinished steps: %+v", found)
	}
}

func TestUnfinishedOrdersNewestRunFirst(t *testing.T) {
	found, _ := Unfinisheds(records(t, completeRun+killedRun))
	if len(found) != 1 || found[0].Run != "20260906T224903Z-4812" {
		t.Fatalf("got %+v, want the newest run's missing step", found)
	}
}

func TestUnfinishedSurvivesATornTail(t *testing.T) {
	// The half-written last line must not become a record, and must not invent a hole either.
	found, _ := Unfinisheds(records(t, killedRun+tornTail))
	if len(found) != 1 || found[0].ID != "3" {
		t.Fatalf("got %+v, want only the genuine hole", found)
	}
}

func TestUnfinishedCapsAnAbsurdId(t *testing.T) {
	line := `{"v":1,"run":"r","id":"1","ts":"2026-01-01T00:00:00.000Z","kind":"a","ms":1}` + "\n" +
		`{"v":1,"run":"r","id":"900000000","ts":"2026-01-01T00:00:00.000Z","kind":"b","ms":1}` + "\n"
	found, truncated := Unfinisheds(records(t, line))
	if len(found) != maxUnfinishedPerRun {
		t.Fatalf("got %d, want the cap of %d", len(found), maxUnfinishedPerRun)
	}
	if truncated != 900000000-2-maxUnfinishedPerRun {
		t.Errorf("truncated = %d; the count must be the real total, not however many fitted", truncated)
	}
}

func TestUnfinishedReportsADanglingParentWithNoSequenceHole(t *testing.T) {
	// A parent id that is not a decimal number cannot show up as a hole, so the dangling-parent
	// signal is the only thing that finds it.
	line := `{"v":1,"run":"r","id":"1","parent":"zz","ts":"2026-01-01T00:00:00.000Z","kind":"a","ms":10}` + "\n"
	found, _ := Unfinisheds(records(t, line))
	if len(found) != 1 || found[0].ID != "zz" {
		t.Fatalf("got %+v, want the dangling parent zz", found)
	}
}

func TestBuildTreeNestsByParentId(t *testing.T) {
	tree := BuildTree(records(t, completeRun))
	if len(tree) != 1 {
		t.Fatalf("got %d roots, want the one step with no parent: %+v", len(tree), tree)
	}
	if tree[0].Kind != "build" || tree[0].Count != 1 {
		t.Fatalf("root = %+v", tree[0].Stat)
	}
	// Aggregation is per (kind, name), so two repos are two rows, sorted with the slowest first.
	if len(tree[0].Children) != 2 {
		t.Fatalf("got %d child groups, want one per repo", len(tree[0].Children))
	}
	if tree[0].Children[0].Name != "kieran/beta" || tree[0].Children[0].Total.Milliseconds() != 120 {
		t.Errorf("first child = %+v, want the slower repo first", tree[0].Children[0].Stat)
	}
}

func TestBuildTreeAggregatesRepeatsOfTheSameStep(t *testing.T) {
	// Two records with the same kind AND name are one row: this is the "a repo's total opens to
	// show its refs" case, where a hundred ref steps must not be a hundred rows.
	jsonl := `{"v":1,"run":"r","id":"1","span":true,"ts":"2026-01-01T00:00:00.000Z","kind":"repo","name":"alpha","ms":300}
{"v":1,"run":"r","id":"2","parent":"1","ts":"2026-01-01T00:00:00.000Z","kind":"git.ref","name":"main","ms":100}
{"v":1,"run":"r","id":"3","parent":"1","ts":"2026-01-01T00:00:00.100Z","kind":"git.ref","name":"main","ms":50}
`
	tree := BuildTree(records(t, jsonl))
	if len(tree) != 1 || len(tree[0].Children) != 1 {
		t.Fatalf("tree = %s", flatten(tree))
	}
	ref := tree[0].Children[0]
	if ref.Count != 2 || ref.Total.Milliseconds() != 150 || ref.Best.Milliseconds() != 50 || ref.Worst.Milliseconds() != 100 {
		t.Errorf("ref = %+v, want 2 records totalling 150ms with best 50 and worst 100", ref.Stat)
	}
}

func TestBuildTreeRootsARecordWhoseParentIsMissing(t *testing.T) {
	tree := BuildTree(records(t, killedRun))
	kinds := map[string]bool{}
	for _, n := range tree {
		kinds[n.Kind] = true
	}
	// build (no parent) plus git.fetch and git.log, whose parent 3 was never written.
	if !kinds["build"] || !kinds["git.fetch"] || !kinds["git.log"] {
		t.Fatalf("roots = %v; an orphan must surface as a root rather than disappear", kinds)
	}
}

func TestBuildTreeTerminatesOnACycle(t *testing.T) {
	// No writer produces this; a torn disk write could fake it, and a viewer that hangs on a
	// broken file is worse than one that shows it oddly.
	line := `{"v":1,"run":"r","id":"1","parent":"2","ts":"2026-01-01T00:00:00.000Z","kind":"a","ms":1}` + "\n" +
		`{"v":1,"run":"r","id":"2","parent":"1","ts":"2026-01-01T00:00:00.000Z","kind":"b","ms":1}` + "\n"
	done := make(chan []Node, 1)
	go func() { done <- BuildTree(records(t, line)) }()
	select {
	case tree := <-done:
		if len(tree) == 0 {
			t.Fatal("a cycle must still produce rows, not an empty screen")
		}
	case <-timeoutAfter():
		t.Fatal("BuildTree did not terminate on a cyclic parent link")
	}
}

func TestBuildTreeIsStableAcrossRuns(t *testing.T) {
	first := BuildTree(records(t, completeRun+killedRun))
	for i := 0; i < 20; i++ {
		again := BuildTree(records(t, completeRun+killedRun))
		if flatten(first) != flatten(again) {
			t.Fatalf("tree order changed between runs over the same file:\n%s\n%s", flatten(first), flatten(again))
		}
	}
}

func flatten(nodes []Node) string {
	var b strings.Builder
	var walk func([]Node, int)
	walk = func(ns []Node, depth int) {
		for _, n := range ns {
			b.WriteString(strings.Repeat(" ", depth) + n.Kind + "/" + n.Name + "=" + n.Total.String() + "\n")
			walk(n.Children, depth+1)
		}
	}
	walk(nodes, 0)
	return b.String()
}

func TestSortNodesBreaksTiesOnKindThenName(t *testing.T) {
	nodes := []Node{
		{Stat: timings.Stat{Kind: "b", Name: "x", Total: 5}},
		{Stat: timings.Stat{Kind: "a", Name: "z", Total: 5}},
		{Stat: timings.Stat{Kind: "a", Name: "y", Total: 5}},
	}
	SortNodes(nodes, SortTotal, true)
	got := nodes[0].Kind + nodes[0].Name + nodes[1].Kind + nodes[1].Name + nodes[2].Kind + nodes[2].Name
	if got != "ayazbx" {
		t.Errorf("order = %s, want ay az bx -- equal totals must not depend on input order", got)
	}
}

func TestSortNodesByEachColumn(t *testing.T) {
	base := []Node{
		{Stat: timings.Stat{Kind: "a", Total: 10, Mean: 1, Count: 9, Best: 4, Worst: 8, Failed: 0}},
		{Stat: timings.Stat{Kind: "b", Total: 20, Mean: 3, Count: 2, Best: 1, Worst: 30, Failed: 5}},
	}
	for _, c := range []struct {
		by    SortKey
		first string
	}{
		{SortTotal, "b"}, {SortMean, "b"}, {SortCount, "a"},
		{SortBest, "a"}, {SortWorst, "b"}, {SortFailed, "b"},
	} {
		nodes := append([]Node(nil), base...)
		SortNodes(nodes, c.by, true)
		if nodes[0].Kind != c.first {
			t.Errorf("sorted by %s descending, first = %q, want %q", c.by, nodes[0].Kind, c.first)
		}
	}
	nodes := append([]Node(nil), base...)
	SortNodes(nodes, SortKind, false)
	if nodes[0].Kind != "a" {
		t.Errorf("kind ascending should start at a, got %q", nodes[0].Kind)
	}
}

func TestScopeDefaultsToTheNewestRun(t *testing.T) {
	all := records(t, completeRun+killedRun)
	scoped, chosen := Scope(all, "")
	if chosen != "20260906T224903Z-4812" {
		t.Fatalf("chose %q, want the newest run", chosen)
	}
	if len(scoped) != 4 {
		t.Errorf("got %d records, want the newest run's 4", len(scoped))
	}

	if _, chosen := Scope(all, "20260905T101010Z-100"); chosen != "20260905T101010Z-100" {
		t.Errorf("an explicit id must be honoured, got %q", chosen)
	}
	if scoped, chosen := Scope(all, AllRuns); chosen != AllRuns || len(scoped) != len(all) {
		t.Errorf("all = %d records under %q, want %d", len(scoped), chosen, len(all))
	}
	// An unknown id -- including anything path-shaped a browser might send -- falls back to the
	// newest run and never reaches the filesystem.
	if _, chosen := Scope(all, "../../etc/passwd"); chosen != "20260906T224903Z-4812" {
		t.Errorf("unknown run id fell through to %q", chosen)
	}
}

func TestLoadReadsBothFilesAndToleratesNeither(t *testing.T) {
	dir := fixtureDir(t)
	data := Load(dir)
	if data.LogErr != nil || data.TimingsErr != nil {
		t.Fatalf("errors: %v %v", data.LogErr, data.TimingsErr)
	}
	if len(data.Log) != 5 || len(data.Records) != 7 {
		t.Errorf("got %d log records and %d timing records, want 5 and 7", len(data.Log), len(data.Records))
	}

	empty := Load(t.TempDir())
	if empty.LogErr != nil || empty.TimingsErr != nil {
		t.Fatalf("a directory with neither file is ordinary, not an error: %v %v", empty.LogErr, empty.TimingsErr)
	}
	if len(empty.Log) != 0 || len(empty.Records) != 0 {
		t.Error("an empty directory yields no records")
	}
}

func TestLoadDropsASecretLookingCountKey(t *testing.T) {
	dir := t.TempDir()
	write(t, dir+"/frznforge-timings.jsonl",
		`{"v":1,"run":"r","id":"1","ts":"2026-01-01T00:00:00.000Z","kind":"a","ms":1,"counts":{"pages":2,"api_token":9}}`+"\n")
	data := Load(dir)
	if _, ok := data.Records[0].Counts["api_token"]; ok {
		t.Error("a count key that names a secret must not survive into the viewer")
	}
	if data.Records[0].Counts["pages"] != 2 {
		t.Error("an ordinary count must survive untouched")
	}
}

func TestUnfinishedIdOrderIsNumeric(t *testing.T) {
	if !lessID("9", "10") {
		t.Error("9 must sort before 10")
	}
	if !lessID("2", "zz") {
		t.Error("a number sorts before a non-number so the ordinary case reads normally")
	}
	if lessID("10", "9") {
		t.Error("10 must not sort before 9")
	}
	if _, err := strconv.Atoi("zz"); err == nil {
		t.Fatal("the fixture for the non-numeric branch stopped being non-numeric")
	}
}
