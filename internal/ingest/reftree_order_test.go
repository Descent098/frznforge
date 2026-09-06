package ingest

import (
	"context"
	"sort"
	"testing"

	"frznforge/internal/ingest/testsupport"
)

// TestRefTreeOrderIsBranchesThenTags asserts the scan's refTrees key ORDER directly.
//
// It exists because the Go-vs-TypeScript parity tests cannot see this. They decode both sides
// into the same Go type and re-encode them, so whatever ordering the encoder imposes is imposed
// on both and cancels out — confirmed by reintroducing a sort in model.RefTreeMap and watching
// those tests still pass while this one and TestRefTreesKeepInsertionOrder fail.
//
// The order is branches first (by name), then tags (by name); NOT overall alphabetical. A tag
// that sorts before a branch is what separates the two, and it is ordinary in real repositories:
// any `v*` tag alongside a `zzz-` branch, or the `aaa-first` tag below.
func TestRefTreeOrderIsBranchesThenTags(t *testing.T) {
	repo := testsupport.Create(t, "order", "main")
	repo.WriteAndCommit(map[string]string{"a.txt": "a\n"}, "first", testsupport.CommitOptions{})

	// A tag whose name sorts before every branch, and a branch whose name sorts after it.
	repo.Tag("aaa-first", testsupport.TagOptions{Annotated: true, Message: "first tag\n"})
	repo.Checkout("zeta", true)
	repo.WriteAndCommit(map[string]string{"z.txt": "z\n"}, "zeta work", testsupport.CommitOptions{})
	repo.Checkout("main", false)

	res, err := ScanRepo(context.Background(), ScanSource{AbsPath: repo.Dir}, testScanOptions())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	got := res.Repo.RefTrees.Keys()
	if len(got) < 2 {
		t.Fatalf("expected a branch tree and a tag tree, got %v", got)
	}

	byName := append([]string(nil), got...)
	sort.Strings(byName)
	if equalStrings(got, byName) {
		t.Fatalf("this fixture no longer separates insertion order from alphabetical order (%v) — "+
			"the divergence it exists to catch would be invisible", got)
	}

	// Branches must all precede tags.
	seenTag := false
	for _, name := range got {
		tree, _ := res.Repo.RefTrees.Get(name)
		if tree.Kind == "tag" {
			seenTag = true
			continue
		}
		if seenTag {
			t.Errorf("branch %q comes after a tag; order was %v", name, got)
		}
	}
	t.Logf("refTrees order: %v (alphabetical would be %v)", got, byName)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
