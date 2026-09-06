package ingest

import (
	"reflect"
	"testing"

	"frznforge/internal/config"
	"frznforge/internal/model"
)

func authored(sha, name, email, date string) model.Commit {
	return model.Commit{Sha: sha, Author: model.Person{Name: name, Email: email}, AuthorDate: date}
}

func commitsBySha(cs ...model.Commit) map[string]model.Commit {
	m := make(map[string]model.Commit, len(cs))
	for _, c := range cs {
		m[c.Sha] = c
	}
	return m
}

func TestBuildContributorIndex(t *testing.T) {
	first := config.ContributorConfig{Name: "First", Emails: []string{"  One@Example.COM ", "two@example.com"}}
	second := config.ContributorConfig{Name: "Second", Emails: []string{"TWO@example.com", "three@example.com"}}
	blank := config.ContributorConfig{Name: "Blank", Emails: []string{"   ", ""}}
	index := BuildContributorIndex([]config.ContributorConfig{first, second, blank})

	// Addresses are folded the same way git author emails are, so surrounding space and case
	// in the config cannot stop a match.
	if got := index["one@example.com"].Name; got != "First" {
		t.Errorf("one@example.com → %q, want First", got)
	}
	// The FIRST entry to claim an address keeps it, so the index never depends on which entry
	// the runtime happened to visit first.
	if got := index["two@example.com"].Name; got != "First" {
		t.Errorf("two@example.com → %q, want First (the first claimant)", got)
	}
	if got := index["three@example.com"].Name; got != "Second" {
		t.Errorf("three@example.com → %q, want Second", got)
	}
	// A blank address indexes nothing rather than claiming the empty key, which every commit
	// with no author email would then match.
	if _, ok := index[""]; ok {
		t.Error("a blank configured address claimed the empty key")
	}
	if len(index) != 3 {
		t.Errorf("index has %d entries, want 3", len(index))
	}
}

func TestContributorsFromCommits(t *testing.T) {
	commits := commitsBySha(
		authored("a1", "Alice", "Alice@Example.com", "2024-01-01T00:00:00Z"),
		authored("a2", "Alice A.", " alice@example.com ", "2024-03-01T00:00:00Z"),
		authored("a3", "Alice", "ALICE@EXAMPLE.COM", "2024-02-01T00:00:00Z"),
		authored("b1", "Bob", "bob@example.com", "2024-02-15T00:00:00Z"),
	)
	got := ContributorsFromCommits(commits, nil)
	if len(got) != 2 {
		t.Fatalf("got %d contributors, want 2: %+v", len(got), got)
	}
	// Grouped case-insensitively, and the stored address is the folded one.
	alice := got[0]
	if alice.Email != "alice@example.com" || alice.Commits != 3 {
		t.Errorf("alice = %+v", alice)
	}
	// The display name is the one on the most recent commit, not the first or the commonest.
	if alice.Name != "Alice A." {
		t.Errorf("alice name = %q, want the most recent one", alice.Name)
	}
	if alice.FirstCommit != "2024-01-01T00:00:00Z" || alice.LastCommit != "2024-03-01T00:00:00Z" {
		t.Errorf("alice dates = %s … %s", alice.FirstCommit, alice.LastCommit)
	}
	// Nothing is decorated without a config entry; these are `null` in the artifact.
	if alice.Avatar != nil || alice.Description != nil || alice.URL != nil {
		t.Errorf("undecorated contributor carries decoration: %+v", alice)
	}
	if got[1].Name != "Bob" || got[1].Commits != 1 {
		t.Errorf("bob = %+v", got[1])
	}
}

// Two commits by one person at the very same instant under two different names: the sha
// decides, so the artifact does not depend on which one git listed first.
func TestContributorNameTieIsBrokenBySha(t *testing.T) {
	same := "2024-01-01T00:00:00Z"
	got := ContributorsFromCommits(commitsBySha(
		authored("aaa", "Earlier Sha", "x@example.com", same),
		authored("zzz", "Later Sha", "x@example.com", same),
	), nil)
	if len(got) != 1 || got[0].Name != "Later Sha" {
		t.Errorf("name tie = %+v, want the higher sha to win", got)
	}
}

func TestContributorsSortOrder(t *testing.T) {
	got := ContributorsFromCommits(commitsBySha(
		authored("z1", "Zed", "z@example.com", "2024-01-01T00:00:00Z"),
		authored("z2", "Zed", "z@example.com", "2024-01-02T00:00:00Z"),
		authored("z3", "Zed", "z@example.com", "2024-01-03T00:00:00Z"),
		authored("n2", "Ann", "b@example.com", "2024-01-04T00:00:00Z"),
		authored("n1", "Ann", "a@example.com", "2024-01-05T00:00:00Z"),
		authored("b1", "Bob", "c@example.com", "2024-01-06T00:00:00Z"),
	), nil)
	// Commits desc first, then name, then email — every step a plain code-point compare.
	want := [][2]string{{"Zed", "z@example.com"}, {"Ann", "a@example.com"}, {"Ann", "b@example.com"}, {"Bob", "c@example.com"}}
	if len(got) != len(want) {
		t.Fatalf("got %d contributors, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Name != w[0] || got[i].Email != w[1] {
			t.Errorf("position %d = %s <%s>, want %s <%s>", i, got[i].Name, got[i].Email, w[0], w[1])
		}
	}
}

func TestContributorsMergeConfiguredAddresses(t *testing.T) {
	commits := commitsBySha(
		authored("a", "Old Name", "One@Example.com", "2024-01-01T00:00:00Z"),
		authored("b", "New Name", "two@example.com", "2024-02-01T00:00:00Z"),
		authored("c", "Someone", "three@example.com", "2024-03-01T00:00:00Z"),
	)
	index := BuildContributorIndex([]config.ContributorConfig{{
		Name:        "Configured Person",
		Emails:      []string{"one@example.com", "two@example.com"},
		Avatar:      "avatars/p.png",
		Description: "does the work",
		URL:         "https://example.com/p",
	}})

	got := ContributorsFromCommits(commits, index)
	if len(got) != 2 {
		t.Fatalf("want two contributors, got %+v", got)
	}
	merged := got[0]
	if merged.Commits != 2 {
		t.Errorf("the two addresses did not merge: %+v", merged)
	}
	if merged.Name != "Configured Person" {
		t.Errorf("name = %q, want the configured one", merged.Name)
	}
	// Keyed by the FIRST configured address, and case-folded.
	if merged.Email != "one@example.com" {
		t.Errorf("email = %q", merged.Email)
	}
	if merged.FirstCommit != "2024-01-01T00:00:00Z" || merged.LastCommit != "2024-02-01T00:00:00Z" {
		t.Errorf("dates were not widened: %+v", merged)
	}
	// The avatar reaches the artifact as the public/-relative path the site serves, which is
	// what the TypeScript's PublicPath transform produced at config-parse time.
	if merged.Avatar == nil || *merged.Avatar != "/avatars/p.png" {
		t.Errorf("avatar = %v", merged.Avatar)
	}
	if merged.Description == nil || *merged.Description != "does the work" {
		t.Errorf("description = %v", merged.Description)
	}
	if merged.URL == nil || *merged.URL != "https://example.com/p" {
		t.Errorf("url = %v", merged.URL)
	}
	if got[1].Name != "Someone" || got[1].Avatar != nil {
		t.Errorf("undecorated contributor = %+v", got[1])
	}
}

// The merged count is what the sort orders by, which is why the merge happens during the walk
// rather than as a post-pass: two addresses at one commit each outrank a three-commit author
// only once they are one person.
func TestConfiguredMergeChangesTheRanking(t *testing.T) {
	commits := commitsBySha(
		authored("s1", "Solo", "solo@example.com", "2024-01-01T00:00:00Z"),
		authored("s2", "Solo", "solo@example.com", "2024-01-02T00:00:00Z"),
		authored("m1", "Merged", "work@example.com", "2024-01-03T00:00:00Z"),
		authored("m2", "Merged", "home@example.com", "2024-01-04T00:00:00Z"),
		authored("m3", "Merged", "laptop@example.com", "2024-01-05T00:00:00Z"),
	)
	entry := config.ContributorConfig{
		Name:   "Merged Person",
		Emails: []string{"work@example.com", "home@example.com", "laptop@example.com"},
	}
	if got := ContributorsFromCommits(commits, nil); got[0].Name != "Solo" {
		t.Errorf("unmerged, the two-commit author should lead: %+v", got)
	}
	got := ContributorsFromCommits(commits, BuildContributorIndex([]config.ContributorConfig{entry}))
	if len(got) != 2 {
		t.Fatalf("want two contributors, got %+v", got)
	}
	if got[0].Name != "Merged Person" || got[0].Commits != 3 {
		t.Errorf("merged person = %+v, want 3 commits and the lead", got[0])
	}
	if got[0].FirstCommit != "2024-01-03T00:00:00Z" || got[0].LastCommit != "2024-01-05T00:00:00Z" {
		t.Errorf("merged dates = %s … %s", got[0].FirstCommit, got[0].LastCommit)
	}
}

// The commits argument is a map, and Go randomises map iteration. Everything the walk touches
// has to be order-independent or explicitly ordered.
func TestContributorsFromCommitsIsDeterministic(t *testing.T) {
	commits := map[string]model.Commit{}
	for _, c := range []model.Commit{
		authored("a1", "Ann", "ann@example.com", "2024-01-01T00:00:00Z"),
		authored("a2", "Ann", "ANN@example.com", "2024-01-02T00:00:00Z"),
		authored("b1", "Bob", "bob@example.com", "2024-01-03T00:00:00Z"),
		authored("b2", "Bobby", "bob@example.com", "2024-01-04T00:00:00Z"),
		authored("c1", "Cy", "cy@example.com", "2024-01-05T00:00:00Z"),
		authored("d1", "Dee", "dee@example.com", "2024-01-05T00:00:00Z"),
		authored("e1", "Eve", "eve@example.com", "2024-01-05T00:00:00Z"),
	} {
		commits[c.Sha] = c
	}
	index := BuildContributorIndex([]config.ContributorConfig{
		{Name: "Cee Dee", Emails: []string{"cy@example.com", "dee@example.com"}},
	})
	first := ContributorsFromCommits(commits, index)
	for i := 0; i < 30; i++ {
		if got := ContributorsFromCommits(commits, index); !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d differs:\n got %+v\nwant %+v", i, got, first)
		}
	}
}

func TestUnmatchedContributors(t *testing.T) {
	entries := []config.ContributorConfig{
		{Name: "Matched", Emails: []string{"nope@example.com", "  Yes@Example.COM "}},
		{Name: "Missing", Emails: []string{"absent@example.com"}},
		{Name: "Also Missing", Emails: []string{"gone@example.com", "vanished@example.com"}},
	}
	seen := map[string]bool{"yes@example.com": true, "other@example.com": true}
	got := UnmatchedContributors(entries, seen)
	// Config order, so the warning list is reproducible.
	if len(got) != 2 || got[0].Name != "Missing" || got[1].Name != "Also Missing" {
		t.Fatalf("unmatched = %+v, want Missing then Also Missing", got)
	}
	// Never nil: the caller ranges over it and the empty case is the common one.
	if all := UnmatchedContributors(nil, seen); all == nil || len(all) != 0 {
		t.Errorf("no entries = %+v, want an empty slice", all)
	}
	if none := UnmatchedContributors(entries, map[string]bool{}); len(none) != 3 {
		t.Errorf("nothing seen = %+v, want every entry", none)
	}
}
