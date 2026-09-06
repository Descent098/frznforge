package model

import (
	"strings"
	"testing"
)

// TestRefTreesKeepInsertionOrder pins the divergence a plain Go map hid.
//
// The TypeScript scanner inserts every treed BRANCH first, by name, then every treed TAG, by
// name. A repository with a branch `other` and a tag `aaa` therefore emits ["other","aaa"] — and
// a Go map would emit ["aaa","other"], because encoding/json sorts map keys.
//
// Neither this project's own artifact nor the e2e fixture contains such a repository: in both,
// branches-then-tags happens to also be alphabetical, so both byte-compared clean while the
// divergence waited for the first repo with a `v*` tag and a feature branch — which is most of
// them. This test is that repository.
func TestRefTreesKeepInsertionOrder(t *testing.T) {
	repo := minimalRepo("alpha")
	trees := NewRefTreeMap()
	// Branches first, by name — then tags, by name. Exactly the scanner's order.
	trees.Set("other", RefTree{Kind: "branch", Name: "other", Commit: strings.Repeat("a", 40), Tree: []TreeEntry{}, Files: map[string]FileInfo{}})
	trees.Set("aaa", RefTree{Kind: "tag", Name: "aaa", Commit: strings.Repeat("b", 40), Tree: []TreeEntry{}, Files: map[string]FileInfo{}})
	repo.RefTrees = *trees

	d := EmptyForgeData()
	d.Repos = append(d.Repos, repo)
	out, err := Serialize(d)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)

	iOther, iAaa := strings.Index(got, `"other"`), strings.Index(got, `"aaa"`)
	if iOther < 0 || iAaa < 0 {
		t.Fatalf("both refs should be present:\n%s", got)
	}
	if iOther > iAaa {
		t.Errorf("refTrees emitted in sorted order, not insertion order — the branch must come first")
	}

	// And the order has to survive a decode/encode round trip, which is what `frznforge verify`
	// leans on.
	parsed, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if keys := parsed.Repos[0].RefTrees.Keys(); len(keys) != 2 || keys[0] != "other" || keys[1] != "aaa" {
		t.Errorf("round trip lost the order: %v", keys)
	}
	again, err := Serialize(parsed)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != got {
		t.Error("re-serializing a decoded artifact changed the refTrees order")
	}
}

// TestRefTreeMapDoesNotEscapeHTML — the ordered map does its own encoding, so it has to make the
// same choice Serialize does. A path or a file's content inside a ref tree containing `<` must
// come out raw, not as a unicode escape.
func TestRefTreeMapDoesNotEscapeHTML(t *testing.T) {
	repo := minimalRepo("alpha")
	trees := NewRefTreeMap()
	trees.Set("main", RefTree{
		Kind: "branch", Name: "main", Commit: strings.Repeat("a", 40),
		Tree:  []TreeEntry{{Path: "a<b>.md", Name: "a<b>.md", Type: "blob", Mode: "100644", Sha: strings.Repeat("c", 40), LastCommit: strings.Repeat("d", 40)}},
		Files: map[string]FileInfo{},
	})
	repo.RefTrees = *trees
	d := EmptyForgeData()
	d.Repos = append(d.Repos, repo)

	// Through Serialize, not json.Marshal: the artifact is written by an Encoder with
	// SetEscapeHTML(false), and json.Marshal would re-escape a custom marshaler's output. Testing
	// the wrong one of those would have "found" a bug that only the test had.
	out, err := Serialize(d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "a<b>.md") {
		t.Errorf("a path inside a ref tree was HTML-escaped: %s", truncate(out))
	}
	if strings.Contains(string(out), "u003c") {
		t.Errorf("output contains a unicode escape for a literal less-than: %s", truncate(out))
	}
}

// truncate keeps a failure message readable when the artifact is large.
func truncate(b []byte) string {
	if len(b) > 600 {
		return string(b[:600]) + "…"
	}
	return string(b)
}
