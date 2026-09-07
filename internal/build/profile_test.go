package build

import (
	"testing"
	"time"

	"frznforge/internal/config"
	"frznforge/internal/model"
)

// The contribution graph and the recent-activity feed — the port of the `contributions` and
// `activity` blocks of tests/unit/phase34-libs.test.ts.
//
// These two derivations were the last thing in the rewrite covered ONLY by the parity harness,
// and the parity harness is deleted at the end of Phase 9. Parity is also the weakest possible
// evidence for them: both sides render the same profile page from the same artifact, so a
// bucketing rule that is wrong in the same way in both languages compares equal. The rules
// below — dedupe by sha, filter by identity, quartile levels, streaks, the shadowed-push drop —
// are asserted directly against values worked out by hand from the fixture.
//
// The fixture is the one the TypeScript test uses, field for field, so the two suites disagree
// about the same repository rather than about two different ones.

func fixtureSha(n int) string {
	const hex = "0123456789abcdef"
	out := make([]byte, 40)
	for i := range out {
		out[i] = '0'
	}
	i := 39
	for n > 0 {
		out[i] = hex[n%16]
		n /= 16
		i--
	}
	return string(out)
}

func fixtureCommit(n int, date, subject, email string) model.Commit {
	c := model.Commit{
		Sha:        fixtureSha(n),
		Parents:    []string{},
		Author:     model.Person{Name: "Kieran", Email: email},
		AuthorDate: date,
		Committer:  model.Person{Name: "Kieran", Email: email},
		CommitDate: date,
		Subject:    subject,
		Stats:      model.CommitStats{FilesChanged: 1, Additions: 1},
	}
	if n > 1 {
		c.Parents = []string{fixtureSha(n - 1)}
	}
	return c
}

// profileFixture mirrors the `repo` const in phase34-libs.test.ts: three commits on three
// consecutive days, two branches where feat/zip contains all of main's commits, and two tags.
func profileFixture() model.Repo {
	return model.Repo{
		Slug: "alpha",
		Name: "alpha",
		Branches: []model.Branch{
			{Name: "main", Head: fixtureSha(2), Commits: []string{fixtureSha(2), fixtureSha(1)}, LastCommitDate: "2026-08-20T10:00:00Z"},
			{Name: "feat/zip", Head: fixtureSha(3), Commits: []string{fixtureSha(3), fixtureSha(2), fixtureSha(1)}, LastCommitDate: "2026-08-21T10:00:00Z"},
		},
		GitTags: []model.Tag{
			{Name: "v1.0.0", Target: fixtureSha(1), Annotated: true, Date: "2026-08-19T00:00:00Z"},
			{Name: "light", Target: fixtureSha(2), Date: "2026-08-20T10:00:00Z"},
		},
		Commits: map[string]model.Commit{
			fixtureSha(1): fixtureCommit(1, "2026-08-19T09:00:00Z", "init", "kieran@example.com"),
			fixtureSha(2): fixtureCommit(2, "2026-08-20T10:00:00Z", "second", "kieran@example.com"),
			fixtureSha(3): fixtureCommit(3, "2026-08-21T10:00:00Z", "feature work", "kieran@example.com"),
		},
		CommitCount: 3,
	}
}

// profileNow is the TypeScript test's `now`: a Sunday, two days after the newest commit, so all
// three days sit inside the 52-week window with room to spare.
var profileNow = time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

func TestCommitsByDay(t *testing.T) {
	repo := profileFixture()

	t.Run("buckets by authoring day", func(t *testing.T) {
		days := commitsByDay([]model.Repo{repo}, nil)
		for day, want := range map[string]int{"2026-08-19": 1, "2026-08-20": 1, "2026-08-21": 1} {
			if days[day] != want {
				t.Errorf("%s = %d, want %d", day, days[day], want)
			}
		}
		if len(days) != 3 {
			t.Errorf("got %d days, want 3: %v", len(days), days)
		}
	})

	t.Run("an identity filter that matches nobody empties the graph", func(t *testing.T) {
		if days := commitsByDay([]model.Repo{repo}, []string{"other@x.com"}); len(days) != 0 {
			t.Errorf("got %v, want no days", days)
		}
	})

	t.Run("identity matching is case-insensitive", func(t *testing.T) {
		// The owner's email is configured by hand and git records whatever the author typed;
		// the two differing only in case must not empty the profile's headline graph.
		if days := commitsByDay([]model.Repo{repo}, []string{"KIERAN@example.com"}); len(days) != 3 {
			t.Errorf("got %d days, want 3 — the compare is case-sensitive", len(days))
		}
	})

	t.Run("a sha present in two repos is counted once", func(t *testing.T) {
		// Two scanned repos sharing history (a fork, a vendored subtree) must not double the
		// contribution count, unlike the deliberately un-deduped KPI in commitsSince.
		second := profileFixture()
		second.Slug = "beta"
		days := commitsByDay([]model.Repo{repo, second}, nil)
		if days["2026-08-19"] != 1 {
			t.Errorf("2026-08-19 = %d, want 1 — the same sha was counted twice", days["2026-08-19"])
		}
	})
}

func TestBuildContribGraph(t *testing.T) {
	heat := config.HeatConfig{Hot: 7, Warm: 30, Neutral: 180, Cool: 365}
	g := buildContribGraph([]model.Repo{profileFixture()}, nil, profileNow, heat)

	if len(g.Weeks) != 52 {
		t.Errorf("weeks = %d, want 52", len(g.Weeks))
	}
	if g.Total != 3 {
		t.Errorf("total = %d, want 3", g.Total)
	}
	if g.LongestStreak != 3 {
		t.Errorf("longest streak = %d, want 3 (the 19th, 20th and 21st)", g.LongestStreak)
	}
	if g.BusiestDay == nil || g.BusiestDay.Count != 1 {
		t.Errorf("busiest day = %+v, want a one-commit day", g.BusiestDay)
	}
	if len(g.Months) <= 8 {
		t.Errorf("months = %d, want more than 8 across a year", len(g.Months))
	}

	var filled []*contribCell
	for _, week := range g.Weeks {
		for _, cell := range week {
			if cell != nil && cell.Count > 0 {
				filled = append(filled, cell)
			}
		}
	}
	if len(filled) != 3 {
		t.Fatalf("%d cells have commits, want 3", len(filled))
	}
	for _, c := range filled {
		// Three equal counts means every quartile threshold is 1, so all three land in the
		// same non-zero level. A level of 0 here would mean the quartile maths divided by the
		// window's length rather than indexing into it.
		if c.Level < 1 {
			t.Errorf("%s: level %d, want a non-zero level", c.Day, c.Level)
		}
		if c.Heat != "hot" {
			t.Errorf("%s: heat %q, want hot — all three days are inside the 7-day window", c.Day, c.Heat)
		}
		if c.Family != "c-hot" {
			t.Errorf("%s: family %q, want c-hot", c.Day, c.Family)
		}
	}
}

// TestContribGraphEndsOnSaturday pins the grid's alignment, which the TypeScript test only
// implies. Getting it wrong shifts every cell by a day: the counts stay right and the picture
// is wrong, which no total-based assertion would catch.
func TestContribGraphEndsOnSaturday(t *testing.T) {
	heat := config.HeatConfig{Hot: 7, Warm: 30, Neutral: 180, Cool: 365}
	// A Wednesday, mid-week, so the current column is genuinely part-filled.
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	g := buildContribGraph([]model.Repo{profileFixture()}, nil, now, heat)

	first := g.Weeks[0][0]
	if first == nil {
		t.Fatal("the graph opens on a nil cell")
	}
	start, err := time.Parse("2006-01-02", first.Day)
	if err != nil {
		t.Fatal(err)
	}
	if start.Weekday() != time.Sunday {
		t.Errorf("the grid opens on a %s, want Sunday", start.Weekday())
	}

	// Today is Wednesday, so Thursday, Friday and Saturday of the last column are the future
	// and must be nil rather than zero-count cells — an empty square and a square that says
	// "no commits" are different claims.
	last := g.Weeks[51]
	for i, cell := range last {
		if i <= int(now.Weekday()) && cell == nil {
			t.Errorf("last week day %d is nil, but it is not in the future", i)
		}
		if i > int(now.Weekday()) && cell != nil {
			t.Errorf("last week day %d is %+v, but it has not happened yet", i, cell)
		}
	}
}

func TestBuildActivity(t *testing.T) {
	repo := profileFixture()

	t.Run("groups pushes per repo, branch and day, newest first", func(t *testing.T) {
		events := buildActivity([]model.Repo{repo}, 10)
		if len(events) == 0 {
			t.Fatal("no events")
		}
		first := events[0]
		if first.Type != "push" || first.Branch != "feat/zip" || first.Count != 1 || first.Subject != "feature work" {
			t.Errorf("newest event = %+v, want the feat/zip push of 'feature work'", first)
		}
		for i := 1; i < len(events); i++ {
			if events[i-1].Date < events[i].Date {
				t.Errorf("events are not newest-first at %d: %q then %q", i, events[i-1].Date, events[i].Date)
			}
		}
	})

	t.Run("tags appear as their own events", func(t *testing.T) {
		var tags []string
		for _, e := range buildActivity([]model.Repo{repo}, 10) {
			if e.Type == "tag" {
				tags = append(tags, e.Tag)
			}
		}
		if len(tags) != 2 {
			t.Fatalf("tag events = %v, want both v1.0.0 and light", tags)
		}
		want := map[string]bool{"v1.0.0": true, "light": true}
		for _, name := range tags {
			if !want[name] {
				t.Errorf("unexpected tag event %q", name)
			}
		}
	})

	t.Run("a push shadowed by another branch is shown once", func(t *testing.T) {
		// main and feat/zip both contain the 20th's commit. Without the dedupe the feed reads
		// as two separate pushes of the same work, which is what a merged feature branch looks
		// like on every repository that has ever been merged.
		n := 0
		for _, e := range buildActivity([]model.Repo{repo}, 10) {
			if e.Type == "push" && e.Date[:10] == "2026-08-20" {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%d pushes on 2026-08-20, want 1", n)
		}
	})

	t.Run("caps at the limit", func(t *testing.T) {
		if got := buildActivity([]model.Repo{repo}, 2); len(got) != 2 {
			t.Errorf("got %d events, want the cap of 2", len(got))
		}
	})

	t.Run("a branch commit missing from the commit map is skipped, not panicked on", func(t *testing.T) {
		// The history knobs (maxCommits, maxCommitAgeDays) leave branch lists holding shas the
		// artifact never stored. Reading one out of the map without checking is a nil-value
		// commit with an empty date, which would sort to the end of the feed as a blank row.
		trimmed := profileFixture()
		delete(trimmed.Commits, fixtureSha(1))
		for _, e := range buildActivity([]model.Repo{trimmed}, 10) {
			if e.Type == "push" && e.Date == "" {
				t.Errorf("a dropped commit produced a dateless event: %+v", e)
			}
		}
	})
}
