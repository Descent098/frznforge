package ingest

import (
	"context"
	"strings"
	"testing"

	"frznforge/internal/ingest/testsupport"
)

func treeFixture(t *testing.T) *testsupport.Repo {
	repo := testsupport.Create(t, "", "")
	repo.WriteAndCommit(map[string]string{
		"README.md":       "# hello\n",
		"src/main.go":     "package main\n\nfunc main() {}\n",
		"src/deep/one.ts": "export const one = 1;\n",
	}, "first", testsupport.CommitOptions{})
	// A second commit that touches only one file, so lastCommit differs per path.
	repo.WriteAndCommit(map[string]string{
		"src/deep/one.ts": "export const one = 2;\n",
	}, "second", testsupport.CommitOptions{})
	return repo
}

func TestListTreeFlattensAllDepths(t *testing.T) {
	ctx := context.Background()
	repo := treeFixture(t)

	entries, err := ListTree(ctx, repo.Dir, repo.Head())
	if err != nil {
		t.Fatalf("ListTree: %v", err)
	}
	var paths, types []string
	for _, e := range entries {
		paths = append(paths, e.Path)
		types = append(types, e.Type)
	}
	want := []string{"README.md", "src", "src/deep", "src/deep/one.ts", "src/main.go"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("tree paths = %v, want %v (sorted, trees included)", paths, want)
	}
	if types[1] != "tree" || types[0] != "blob" {
		t.Errorf("types = %v", types)
	}
	// Trees carry no size; blobs do.
	if entries[1].Size != nil {
		t.Errorf("tree entry has a size: %v", *entries[1].Size)
	}
	if entries[0].Size == nil || *entries[0].Size != int64(len("# hello\n")) {
		t.Errorf("README size = %v", entries[0].Size)
	}
	if entries[3].Name != "one.ts" {
		t.Errorf("name = %q, want the basename", entries[3].Name)
	}

	root, err := ListRootTree(ctx, repo.Dir, repo.Head())
	if err != nil {
		t.Fatal(err)
	}
	if len(root) != 2 || root[0].Path != "README.md" || root[1].Path != "src" {
		t.Fatalf("root listing = %+v", root)
	}
}

// A directory takes the newest commit of anything beneath it, which is what makes the file
// table's "last change" column meaningful for folders.
func TestLastCommitByPathCoversAncestors(t *testing.T) {
	ctx := context.Background()
	repo := treeFixture(t)
	first := repo.Rev("HEAD~1")
	second := repo.Head()

	last, err := LastCommitByPath(ctx, repo.Dir, second, []string{"README.md", "src", "src/deep", "src/deep/one.ts", "src/main.go"})
	if err != nil {
		t.Fatalf("LastCommitByPath: %v", err)
	}
	for path, want := range map[string]string{
		"README.md":       first,
		"src/main.go":     first,
		"src/deep/one.ts": second,
		"src/deep":        second,
		"src":             second,
	} {
		if last[path] != want {
			t.Errorf("lastCommit[%s] = %s, want %s", path, last[path], want)
		}
	}
}

func TestScanTreeClassifiesFiles(t *testing.T) {
	ctx := context.Background()
	repo := testsupport.Create(t, "", "")
	big := strings.Repeat("x", 200)
	repo.WriteBytes(map[string][]byte{
		"small.txt": []byte("hello\n"),
		"tiny.bin":  {0x00, 0x01, 0x02},
		"big.txt":   []byte(big),
		"big.bin":   append([]byte{0x00}, bytesRepeat('y', 199)...),
	})
	repo.Add()
	head := repo.Commit("first", testsupport.CommitOptions{})

	res, err := ScanTree(ctx, repo.Dir, head, 100)
	if err != nil {
		t.Fatalf("ScanTree: %v", err)
	}

	small := res.Files["small.txt"]
	if !small.Stored || small.Binary || small.TooLarge || small.Size != 6 {
		t.Errorf("small.txt = %+v", small)
	}
	if small.Language == nil || *small.Language != "Text" {
		t.Errorf("small.txt language = %v", small.Language)
	}
	if _, ok := res.Blobs[small.Sha]; !ok {
		t.Error("small.txt content was not kept for the blob store")
	}

	// Binary and within the cap: schema v2 stores it anyway, for raw serving and previews.
	tiny := res.Files["tiny.bin"]
	if !tiny.Binary || !tiny.Stored || tiny.TooLarge {
		t.Errorf("tiny.bin = %+v", tiny)
	}

	// Over the cap: never stored, and classified by sniffing only the first 8000 bytes.
	bigText := res.Files["big.txt"]
	if !bigText.TooLarge || bigText.Stored || bigText.Binary {
		t.Errorf("big.txt = %+v", bigText)
	}
	if _, ok := res.Blobs[bigText.Sha]; ok {
		t.Error("an oversized file reached the blob store")
	}
	bigBin := res.Files["big.bin"]
	if !bigBin.TooLarge || bigBin.Stored || !bigBin.Binary {
		t.Errorf("big.bin = %+v", bigBin)
	}

	// Every listed entry carries a lastCommit; with one commit it is that commit.
	for _, e := range res.Tree {
		if e.LastCommit != head {
			t.Errorf("%s lastCommit = %s", e.Path, e.LastCommit)
		}
	}
}
