package ingest

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"frznforge/internal/ingest/testsupport"
	"frznforge/internal/model"
)

// dated builds the only three fields the insights pass reads off a commit.
func dated(sha, email, authorDate, commitDate string) model.Commit {
	return model.Commit{
		Sha:        sha,
		Author:     model.Person{Name: "Dev", Email: email},
		AuthorDate: authorDate,
		CommitDate: commitDate,
	}
}

func TestMonthArithmetic(t *testing.T) {
	if got := monthOf("2024-03-15T12:00:00Z"); got != "2024-03" {
		t.Errorf("monthOf = %q", got)
	}
	// A short or malformed value is handed back rather than panicking on a slice.
	if got := monthOf("2024"); got != "2024" {
		t.Errorf("monthOf(short) = %q", got)
	}
	for _, month := range []string{"0001-01", "1970-12", "2024-01", "2024-12", "9999-12"} {
		if got := monthFromIndex(monthIndex(month)); got != month {
			t.Errorf("monthFromIndex(monthIndex(%q)) = %q", month, got)
		}
	}
	// Consecutive months are consecutive indices, which is what lets gaps be filled by counting.
	if monthIndex("2024-12")+1 != monthIndex("2025-01") {
		t.Error("December and January are not adjacent")
	}
}

// A file with no trailing newline still ends in a line — the rule the blob viewer uses, and
// the reason this is not a plain newline count. Getting it wrong made a blob page print
// "2 lines" while insights counted it as 1.
func TestCountLines(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int64
	}{
		{"", 0},
		{"one\n", 1},
		{"one", 1},
		{"one\ntwo\n", 2},
		{"one\ntwo", 2},
		{"\n", 1},
		{"\n\n", 2},
		{"a\r\nb\r\n", 2},
	} {
		if got := countLines([]byte(c.in)); got != c.want {
			t.Errorf("countLines(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestPickEvenlyKeepsBothEnds(t *testing.T) {
	items := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	got := pickEvenly(items, 4)
	if !reflect.DeepEqual(got, []int{0, 3, 6, 9}) {
		t.Errorf("pickEvenly(10, 4) = %v, want [0 3 6 9]", got)
	}
	if len(pickEvenly(items, 100)) != 10 {
		t.Error("a cap above the length should keep everything")
	}
	if len(pickEvenly(items, 10)) != 10 {
		t.Error("a cap equal to the length should keep everything")
	}
	// One checkpoint cannot hold both ends; the newest wins, since it matches the current tree.
	if one := pickEvenly(items, 1); len(one) != 1 || one[0] != 9 {
		t.Errorf("pickEvenly(10, 1) = %v", one)
	}
	if len(pickEvenly([]int{}, 5)) != 0 {
		t.Error("an empty list should stay empty")
	}
}

// The picks are computed by rounding, so they depend on the list length alone. Whatever the
// shape, the result must keep both ends and never repeat or reorder an index — the property
// that makes the code-size series monotone in history.
func TestPickEvenlyIsMonotoneAndKeepsTheEnds(t *testing.T) {
	for n := 1; n <= 40; n++ {
		items := make([]int, n)
		for i := range items {
			items[i] = i
		}
		for max := 1; max <= n+2; max++ {
			got := pickEvenly(items, max)
			if len(got) == 0 {
				t.Fatalf("n=%d max=%d: empty", n, max)
			}
			if got[len(got)-1] != n-1 {
				t.Errorf("n=%d max=%d: last pick %d, want the newest (%d)", n, max, got[len(got)-1], n-1)
			}
			if max > 1 && got[0] != 0 {
				t.Errorf("n=%d max=%d: first pick %d, want the oldest", n, max, got[0])
			}
			if len(got) > max {
				t.Errorf("n=%d max=%d: %d picks", n, max, len(got))
			}
			for i := 1; i < len(got); i++ {
				if got[i] <= got[i-1] {
					t.Errorf("n=%d max=%d: picks are not strictly increasing: %v", n, max, got)
					break
				}
			}
		}
	}
}

// Months inside the span with no commits are emitted as zeros rather than omitted, so a chart
// drawn straight from the array shows a quiet period as quiet.
func TestBucketCommitsFillsQuietMonths(t *testing.T) {
	commits := map[string]model.Commit{
		"c1": dated("c1", "a@example.com", "2024-01-05T00:00:00Z", "2024-01-05T00:00:00Z"),
		"c2": dated("c2", "A@Example.com", "2024-01-20T00:00:00Z", "2024-01-20T00:00:00Z"),
		"c3": dated("c3", "b@example.com", "2024-01-25T00:00:00Z", "2024-01-25T00:00:00Z"),
		"c4": dated("c4", "a@example.com", "2024-04-01T00:00:00Z", "2024-04-01T00:00:00Z"),
	}
	got := bucketCommits(commits, []string{"c4", "c3", "c2", "c1", "missing"})
	want := []model.CommitPoint{
		// Three commits by two distinct authors — the two spellings of a@example.com fold.
		{Month: "2024-01", Commits: 3, Contributors: 2},
		{Month: "2024-02", Commits: 0, Contributors: 0},
		{Month: "2024-03", Commits: 0, Contributors: 0},
		{Month: "2024-04", Commits: 1, Contributors: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("bucketCommits =\n %+v\nwant\n %+v", got, want)
	}
	// Never nil: the artifact says [].
	if empty := bucketCommits(commits, nil); empty == nil || len(empty) != 0 {
		t.Errorf("no branch commits = %+v, want an empty slice", empty)
	}
}

// The two series read two different clocks. "Who wrote code when" is the author date, even
// when a rebase landed the tree months later.
func TestBucketCommitsUsesAuthorDate(t *testing.T) {
	commits := map[string]model.Commit{
		"c1": dated("c1", "a@example.com", "2024-01-05T00:00:00Z", "2024-09-05T00:00:00Z"),
	}
	got := bucketCommits(commits, []string{"c1"})
	if len(got) != 1 || got[0].Month != "2024-01" {
		t.Errorf("bucketCommits = %+v, want a single 2024-01 point", got)
	}
}

// A tree is the state of the branch at the point its commit was APPLIED, so the checkpoints
// bucket by commit date. Bucketing a replayed commit by its author date would plot a tree
// state in a month where it never existed.
func TestMonthlyCheckpointsUseCommitDate(t *testing.T) {
	commits := map[string]model.Commit{
		"c1": dated("c1", "a@example.com", "2023-01-05T00:00:00Z", "2024-06-05T00:00:00Z"),
	}
	got := monthlyCheckpoints(commits, []string{"c1"})
	if len(got) != 1 || got[0].Month != "2024-06" || got[0].Sha != "c1" {
		t.Errorf("monthlyCheckpoints = %+v, want one 2024-06 checkpoint", got)
	}
}

// Within a month the commit closest to the branch head wins: branchCommits is newest first,
// so that is the lowest index.
func TestMonthlyCheckpointsPickTheCommitNearestTheHead(t *testing.T) {
	commits := map[string]model.Commit{
		"newest": dated("newest", "a@example.com", "2024-03-20T00:00:00Z", "2024-03-20T00:00:00Z"),
		"middle": dated("middle", "a@example.com", "2024-03-10T00:00:00Z", "2024-03-10T00:00:00Z"),
		"oldest": dated("oldest", "a@example.com", "2024-03-01T00:00:00Z", "2024-03-01T00:00:00Z"),
	}
	got := monthlyCheckpoints(commits, []string{"newest", "middle", "oldest"})
	if len(got) != 1 || got[0].Sha != "newest" {
		t.Errorf("monthlyCheckpoints = %+v, want the head-most commit of the month", got)
	}
}

// The series must never run backwards in time. A month whose pick sits CLOSER to the head
// than a later month's pick is dropped rather than plotted out of order, and so is any month
// newer than the head's own — nothing on a branch is newer than its head.
func TestMonthlyCheckpointsStayInHistoryOrder(t *testing.T) {
	commits := map[string]model.Commit{
		// A rebase left c2's commit date older than c1's, but c2 is nearer the head.
		"c3": dated("c3", "a@example.com", "2024-03-01T00:00:00Z", "2024-03-01T00:00:00Z"),
		"c2": dated("c2", "a@example.com", "2024-01-01T00:00:00Z", "2024-01-01T00:00:00Z"),
		"c1": dated("c1", "a@example.com", "2024-02-01T00:00:00Z", "2024-02-01T00:00:00Z"),
	}
	got := monthlyCheckpoints(commits, []string{"c3", "c2", "c1"})
	if len(got) != 2 || got[0].Month != "2024-02" || got[1].Month != "2024-03" {
		t.Fatalf("monthlyCheckpoints = %+v, want 2024-02 then 2024-03", got)
	}

	// A commit dated after the head is not a later checkpoint; it is dropped.
	future := map[string]model.Commit{
		"head": dated("head", "a@example.com", "2024-01-01T00:00:00Z", "2024-01-01T00:00:00Z"),
		"old":  dated("old", "a@example.com", "2024-05-01T00:00:00Z", "2024-05-01T00:00:00Z"),
	}
	got = monthlyCheckpoints(future, []string{"head", "old"})
	if len(got) != 1 || got[0].Month != "2024-01" {
		t.Errorf("monthlyCheckpoints = %+v, want only the head's month", got)
	}
}

/* ---- the measured half, against a real repository ------------------------- */

// insightsFixture is three monthly commits with a gap, a vendored path and a binary blob.
func insightsFixture(t *testing.T) (*testsupport.Repo, ComputeInsightsArgs, map[string]int64) {
	t.Helper()
	repo := testsupport.Create(t, "insights", "main")

	sizes := map[string]int64{}
	record := func(name, content string) { sizes[name] = int64(len(content)) }

	aGo := "package a\n"
	record("a.go", aGo)
	c1 := repo.WriteAndCommit(map[string]string{"a.go": aGo}, "first", testsupport.CommitOptions{Date: "2024-01-10T00:00:00Z"})

	bGo := "package b\nfunc B() {}\n"
	record("b.go", bGo)
	// node_modules is vendored: listed by ls-tree, never counted.
	c2 := repo.WriteAndCommit(map[string]string{
		"b.go":                      bGo,
		"node_modules/dep/index.js": "module.exports = 1\n",
	}, "second", testsupport.CommitOptions{Date: "2024-02-10T00:00:00Z"})

	cTxt := "one\ntwo" // no trailing newline: still two lines
	record("c.txt", cTxt)
	repo.Write(map[string]string{"c.txt": cTxt})
	// A NUL in the first 8000 bytes makes it binary, so its bytes are excluded too.
	repo.WriteBytes(map[string][]byte{"logo.bin": {0x00, 0x01, 0x02, 0x03}})
	repo.Add()
	c3 := repo.Commit("third", testsupport.CommitOptions{Date: "2024-04-10T00:00:00Z"})

	args := ComputeInsightsArgs{
		Commits: map[string]model.Commit{
			c1: dated(c1, "dev@example.com", "2024-01-10T00:00:00Z", "2024-01-10T00:00:00Z"),
			c2: dated(c2, "dev@example.com", "2024-02-10T00:00:00Z", "2024-02-10T00:00:00Z"),
			c3: dated(c3, "dev@example.com", "2024-04-10T00:00:00Z", "2024-04-10T00:00:00Z"),
		},
		BranchCommits: []string{c3, c2, c1},
		Head:          c3,
		Options:       DefaultInsightsOptions,
	}
	return repo, args, sizes
}

func TestComputeInsights(t *testing.T) {
	repo, args, size := insightsFixture(t)
	res, err := ComputeInsights(context.Background(), repo.Dir, args)
	if err != nil {
		t.Fatalf("ComputeInsights: %v", err)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("warnings = %+v, want none", res.Warnings)
	}
	in := res.Insights
	if in == nil {
		t.Fatal("insights = nil")
	}

	wantCommits := []model.CommitPoint{
		{Month: "2024-01", Commits: 1, Contributors: 1},
		{Month: "2024-02", Commits: 1, Contributors: 1},
		{Month: "2024-03", Commits: 0, Contributors: 0},
		{Month: "2024-04", Commits: 1, Contributors: 1},
	}
	if !reflect.DeepEqual(in.Commits, wantCommits) {
		t.Errorf("commits series =\n %+v\nwant\n %+v", in.Commits, wantCommits)
	}

	// Vendored and binary blobs are excluded from bytes; prose and unknown languages are NOT,
	// because this series measures the tracked tree rather than the language split.
	lines := func(n int64) *int64 { return &n }
	wantCodeSize := []model.CodeSizePoint{
		{Month: "2024-01", Bytes: size["a.go"], Lines: lines(1)},
		{Month: "2024-02", Bytes: size["a.go"] + size["b.go"], Lines: lines(3)},
		{Month: "2024-04", Bytes: size["a.go"] + size["b.go"] + size["c.txt"], Lines: lines(5)},
	}
	if !reflect.DeepEqual(in.CodeSize, wantCodeSize) {
		t.Errorf("codeSize =\n %+v\nwant\n %+v", codeSizeString(in.CodeSize), codeSizeString(wantCodeSize))
	}
	if in.Sampled || in.SampleCount != 3 || in.Approximate {
		t.Errorf("sampled=%v count=%d approximate=%v, want false/3/false", in.Sampled, in.SampleCount, in.Approximate)
	}
}

// Thinning the checkpoints is what `sampled` reports — and the zero-filled quiet month in the
// commits series never had a tree of its own, so it does not count toward the total.
func TestComputeInsightsSamples(t *testing.T) {
	repo, args, _ := insightsFixture(t)
	args.Options.Samples = 2
	res, err := ComputeInsights(context.Background(), repo.Dir, args)
	if err != nil {
		t.Fatalf("ComputeInsights: %v", err)
	}
	in := res.Insights
	if !in.Sampled || in.SampleCount != 2 || len(in.CodeSize) != 2 {
		t.Fatalf("sampled=%v count=%d points=%d", in.Sampled, in.SampleCount, len(in.CodeSize))
	}
	// Both ends survive the thinning: the oldest checkpoint and the current tree.
	if in.CodeSize[0].Month != "2024-01" || in.CodeSize[1].Month != "2024-04" {
		t.Errorf("kept %q and %q, want the two ends", in.CodeSize[0].Month, in.CodeSize[1].Month)
	}
	// A samples value below one is clamped rather than emptying the series.
	args.Options.Samples = 0
	res, err = ComputeInsights(context.Background(), repo.Dir, args)
	if err != nil {
		t.Fatalf("ComputeInsights: %v", err)
	}
	if len(res.Insights.CodeSize) != 1 || res.Insights.CodeSize[0].Month != "2024-04" {
		t.Errorf("samples=0 gave %+v, want the newest checkpoint alone", res.Insights.CodeSize)
	}
}

// Past the byte budget a checkpoint reports null lines and a byte total that may include
// binaries, and says so in a warning. The wording is compared verbatim: it is artifact bytes.
func TestComputeInsightsApproximate(t *testing.T) {
	repo := testsupport.Create(t, "approx", "main")
	months := []string{"2024-01", "2024-02", "2024-03", "2024-04", "2024-05", "2024-06"}
	commits := map[string]model.Commit{}
	shas := []string{}
	for i, month := range months {
		date := month + "-10T00:00:00Z"
		name := fmt.Sprintf("f%d.txt", i)
		sha := repo.WriteAndCommit(map[string]string{name: "aaaaaaaa\n"}, "c"+month, testsupport.CommitOptions{Date: date})
		commits[sha] = dated(sha, "dev@example.com", date, date)
		shas = append([]string{sha}, shas...) // newest first
	}
	args := ComputeInsightsArgs{
		Commits:       commits,
		BranchCommits: shas,
		Head:          shas[0],
		// Smaller than any one blob, so nothing is ever read.
		Options: InsightsOptions{Enabled: true, Samples: 24, MaxBytesPerSample: 4},
	}
	res, err := ComputeInsights(context.Background(), repo.Dir, args)
	if err != nil {
		t.Fatalf("ComputeInsights: %v", err)
	}
	if !res.Insights.Approximate {
		t.Error("approximate = false")
	}
	for _, p := range res.Insights.CodeSize {
		if p.Lines != nil {
			t.Errorf("%s has a line count of %d; nothing was read", p.Month, *p.Lines)
		}
	}
	// Unread blobs still contribute their ls-tree size, so the series keeps its shape.
	if last := res.Insights.CodeSize[len(res.Insights.CodeSize)-1]; last.Bytes != 6*9 {
		t.Errorf("final bytes = %d, want %d", last.Bytes, 6*9)
	}
	if len(res.Warnings) != 1 {
		t.Fatalf("warnings = %+v, want one", res.Warnings)
	}
	w := res.Warnings[0]
	if w.Code != "insights-approximate" {
		t.Errorf("code = %q", w.Code)
	}
	// Repo-scoped warnings are stamped with the slug by scanRepo, not here.
	if w.Repo != nil {
		t.Errorf("repo = %q, want nil", *w.Repo)
	}
	want := "6 of 6 code-size checkpoints (2024-01, 2024-02, 2024-03, 2024-04, 2024-05 and 1 more) " +
		"hold more content than ingest.insights.maxBytesPerSample (4 bytes), so their line counts " +
		"were skipped and their byte totals may include binary files (the blobs past the budget " +
		"were never read, so they could not be classified)"
	if w.Message != want {
		t.Errorf("message =\n %q\nwant\n %q", w.Message, want)
	}
}

func TestComputeInsightsReturnsNothingWhenItShould(t *testing.T) {
	repo, args, _ := insightsFixture(t)
	cases := map[string]func(a *ComputeInsightsArgs){
		"disabled":       func(a *ComputeInsightsArgs) { a.Options.Enabled = false },
		"no branch":      func(a *ComputeInsightsArgs) { a.BranchCommits = nil },
		"no head":        func(a *ComputeInsightsArgs) { a.Head = "" },
		"unknown commit": func(a *ComputeInsightsArgs) { a.Commits = map[string]model.Commit{} },
	}
	for name, mutate := range cases {
		local := args
		mutate(&local)
		res, err := ComputeInsights(context.Background(), repo.Dir, local)
		if err != nil {
			t.Fatalf("%s: ComputeInsights: %v", name, err)
		}
		if res.Insights != nil {
			t.Errorf("%s: insights = %+v, want nil", name, res.Insights)
		}
		// Never nil: the caller appends these to the repo's warning list.
		if res.Warnings == nil {
			t.Errorf("%s: warnings = nil, want an empty slice", name)
		}
	}
}

// Two runs over the same repository at the same commits must produce the same series — the
// inputs are maps, and every checkpoint choice has to come from the commit list rather than a
// clock or the filesystem.
func TestComputeInsightsIsDeterministic(t *testing.T) {
	repo, args, _ := insightsFixture(t)
	first, err := ComputeInsights(context.Background(), repo.Dir, args)
	if err != nil {
		t.Fatalf("ComputeInsights: %v", err)
	}
	for i := 0; i < 5; i++ {
		got, err := ComputeInsights(context.Background(), repo.Dir, args)
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d differs:\n got %+v\nwant %+v", i, got.Insights, first.Insights)
		}
	}
}

func codeSizeString(points []model.CodeSizePoint) string {
	out := ""
	for _, p := range points {
		lines := "null"
		if p.Lines != nil {
			lines = fmt.Sprint(*p.Lines)
		}
		out += fmt.Sprintf("{%s bytes=%d lines=%s} ", p.Month, p.Bytes, lines)
	}
	return out
}
