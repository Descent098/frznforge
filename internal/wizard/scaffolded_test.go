package wizard

import (
	"strings"
	"testing"

	"frznforge/internal/scaffold"
)

// The wizard against the config the scaffolder actually writes.
//
// Every other test in this package builds its own fixture, which is right for pinning a
// behaviour but wrong for this one question: what happens on the very first save of a brand new
// site. `frznforge new` then `frznforge init --web` then "Add sources" is the opening sequence
// of this whole product, and it ran into a case none of the hand-written fixtures had — a
// `"repos": [ … ]` holding five commented-out examples and no value at all.
//
// insertRepos treated that as an empty array and replaced the body, deleting the examples and
// the explanation around them. The identical function in cmd/frznforge/entries.go had the fix
// and a test; this copy had neither, because the two splice engines are forked. The fixture
// here is `scaffold.Files()` itself rather than a copy of it, so the test keeps asking the real
// question if the scaffolded config is ever reworded.
func TestInsertReposKeepsTheScaffoldedExamples(t *testing.T) {
	source := scaffoldedConfig(t)

	before := strings.Count(source, "// {")
	if before == 0 {
		t.Fatalf("the scaffolded config has no commented-out example entries any more — this test\n"+
			"is now vacuous and needs rewriting against whatever replaced them:\n%s", source)
	}

	res, ok := insertRepos(source, []RepoEntry{
		{Type: "github", Owner: "me", Repo: "thing", Slug: "thing"},
	})
	if !ok {
		t.Fatal("insertRepos refused the scaffolded config outright")
	}
	if !res.changed {
		t.Fatal("insertRepos reported no change after adding an entry")
	}

	if after := strings.Count(res.text, "// {"); after != before {
		t.Errorf("the scaffolded config had %d commented-out examples and has %d after one save;\n"+
			"a new site loses the only documentation it has for the block it is growing\n\n%s",
			before, after, res.text)
	}
	if !strings.Contains(res.text, `"repo": "thing"`) {
		t.Errorf("the new entry is missing from the result:\n%s", res.text)
	}

	// Every line that was there is still there, in order. Counting the examples alone would pass
	// a rewrite that kept the five `// {` lines and dropped the prose between them.
	for _, line := range strings.Split(source, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.Contains(res.text, line) {
			t.Errorf("a line of the scaffolded config did not survive the save: %q", line)
		}
	}
}

// scaffoldedConfig returns the config file frznforge's own scaffolder writes.
func scaffoldedConfig(t *testing.T) string {
	t.Helper()
	for _, f := range scaffold.Files() {
		if strings.HasSuffix(f.Path, ".jsonc") {
			return f.Contents
		}
	}
	t.Fatal("scaffold.Files() writes no .jsonc config — the scaffolder moved and this test cannot find its subject")
	return ""
}
