package ingest

import (
	"context"
	"testing"

	"frznforge/internal/ingest/testsupport"
	"frznforge/internal/model"
)

func TestLoadCommitsFields(t *testing.T) {
	ctx := context.Background()
	repo := testsupport.Create(t, "", "")
	first := repo.WriteAndCommit(map[string]string{"a.txt": "one\ntwo\n"}, "first subject", testsupport.CommitOptions{})
	second := repo.WriteAndCommit(map[string]string{"a.txt": "one\ntwo\nthree\n"},
		"second subject\n\nbody line one\nbody line two\n\n",
		testsupport.CommitOptions{AuthorName: "Other Person", AuthorEmail: "other@example.com"})

	commits, err := LoadCommits(ctx, repo.Dir, []string{second, first, second})
	if err != nil {
		t.Fatalf("LoadCommits: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("want 2 commits, got %d", len(commits))
	}

	c := commits[second]
	if c.Subject != "second subject" {
		t.Errorf("subject = %q", c.Subject)
	}
	// The body is everything after the blank line, trimmed at both ends.
	if c.Body != "body line one\nbody line two" {
		t.Errorf("body = %q", c.Body)
	}
	if c.Author.Name != "Other Person" || c.Author.Email != "other@example.com" {
		t.Errorf("author = %+v", c.Author)
	}
	// The committer keeps the fixture identity even when the author is overridden.
	if c.Committer.Email != "test@example.com" {
		t.Errorf("committer = %+v", c.Committer)
	}
	if len(c.Parents) != 1 || c.Parents[0] != first {
		t.Errorf("parents = %v", c.Parents)
	}
	if c.AuthorDate != "2024-01-01T00:01:00Z" || c.CommitDate != "2024-01-01T00:01:00Z" {
		t.Errorf("dates = %s / %s", c.AuthorDate, c.CommitDate)
	}

	root := commits[first]
	if len(root.Parents) != 0 {
		t.Errorf("root commit has parents: %v", root.Parents)
	}
	// A root commit counts every file as added.
	if len(root.Files) != 1 || root.Files[0].Path != "a.txt" || *root.Files[0].Additions != 2 {
		t.Errorf("root files = %+v", root.Files)
	}
	if root.Stats != (model.CommitStats{FilesChanged: 1, Additions: 2, Deletions: 0}) {
		t.Errorf("root stats = %+v", root.Stats)
	}
	if c.Stats != (model.CommitStats{FilesChanged: 1, Additions: 1, Deletions: 0}) {
		t.Errorf("second stats = %+v", c.Stats)
	}
}

// A rename is three NUL-separated fields where a normal change is one, and the artifact
// records the NEW path. Getting this wrong shifts every following file in the commit.
func TestLoadCommitsRename(t *testing.T) {
	ctx := context.Background()
	repo := testsupport.Create(t, "", "")
	repo.WriteAndCommit(map[string]string{
		"old-name.txt": "content\nlines\n",
		"other.txt":    "other\n",
	}, "first", testsupport.CommitOptions{})
	repo.Git("mv", "old-name.txt", "new-name.txt")
	renamed := repo.Commit("rename it", testsupport.CommitOptions{})

	commits, err := LoadCommits(ctx, repo.Dir, []string{renamed})
	if err != nil {
		t.Fatal(err)
	}
	files := commits[renamed].Files
	if len(files) != 1 {
		t.Fatalf("want one changed file, got %+v", files)
	}
	if files[0].Path != "new-name.txt" {
		t.Errorf("rename recorded as %q, want the new path", files[0].Path)
	}
}

// Binary files report `-`/`-`, which the artifact stores as null and the stats ignore.
func TestLoadCommitsBinary(t *testing.T) {
	ctx := context.Background()
	repo := testsupport.Create(t, "", "")
	repo.WriteBytes(map[string][]byte{"blob.bin": {0x00, 0x01, 0x02, 0x00}})
	repo.Write(map[string]string{"text.txt": "a\nb\n"})
	repo.Add()
	sha := repo.Commit("binary", testsupport.CommitOptions{})

	commits, err := LoadCommits(ctx, repo.Dir, []string{sha})
	if err != nil {
		t.Fatal(err)
	}
	c := commits[sha]
	var bin *model.CommitFileChange
	for i := range c.Files {
		if c.Files[i].Path == "blob.bin" {
			bin = &c.Files[i]
		}
	}
	if bin == nil {
		t.Fatalf("blob.bin missing from %+v", c.Files)
	}
	if bin.Additions != nil || bin.Deletions != nil {
		t.Errorf("binary counts = %v/%v, want null", bin.Additions, bin.Deletions)
	}
	// filesChanged counts it; the line totals do not.
	if c.Stats.FilesChanged != 2 || c.Stats.Additions != 2 {
		t.Errorf("stats = %+v", c.Stats)
	}
}

// Plain `git log` prints no numstat for a merge, so a merge's file list is empty — not
// missing, and not the union of its parents.
func TestLoadCommitsMerge(t *testing.T) {
	ctx := context.Background()
	repo := testsupport.Create(t, "", "")
	repo.WriteAndCommit(map[string]string{"base.txt": "base\n"}, "base", testsupport.CommitOptions{})
	repo.Checkout("side", true)
	repo.WriteAndCommit(map[string]string{"side.txt": "side\n"}, "side", testsupport.CommitOptions{})
	repo.Checkout("main", false)
	repo.WriteAndCommit(map[string]string{"main.txt": "main\n"}, "main", testsupport.CommitOptions{})
	repo.GitWith(map[string]string{
		"GIT_AUTHOR_DATE":    testsupport.At(600),
		"GIT_COMMITTER_DATE": testsupport.At(600),
	}, "merge", "--no-ff", "--no-verify", "-q", "-m", "merge side", "side")
	merge := repo.Head()

	commits, err := LoadCommits(ctx, repo.Dir, []string{merge})
	if err != nil {
		t.Fatal(err)
	}
	c := commits[merge]
	if len(c.Parents) != 2 {
		t.Fatalf("merge parents = %v", c.Parents)
	}
	if len(c.Files) != 0 || c.Stats != (model.CommitStats{}) {
		t.Errorf("merge files/stats = %+v / %+v, want empty", c.Files, c.Stats)
	}
}

func TestLoadCommitsEmptyInput(t *testing.T) {
	ctx := context.Background()
	repo := testsupport.Create(t, "", "")
	commits, err := LoadCommits(ctx, repo.Dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 0 {
		t.Fatalf("want no commits, got %d", len(commits))
	}
}
