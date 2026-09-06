package ingest

import (
	"context"
	"testing"

	"frznforge/internal/ingest/testsupport"
)

// twoBranchRepo: main with two commits, feature/x branched off with one more, one annotated
// and one lightweight tag. The branch name with a slash is deliberate — it is what refSlug and
// the archive filenames have to survive.
func twoBranchRepo(t *testing.T) *testsupport.Repo {
	repo := testsupport.Create(t, "", "")
	repo.WriteAndCommit(map[string]string{"a.txt": "a\n"}, "first", testsupport.CommitOptions{})
	repo.Tag("v1.0.0", testsupport.TagOptions{Annotated: true, Message: "release one\n"})
	repo.WriteAndCommit(map[string]string{"b.txt": "b\n"}, "second", testsupport.CommitOptions{})
	repo.Tag("light", testsupport.TagOptions{})
	repo.Checkout("feature/x", true)
	repo.WriteAndCommit(map[string]string{"c.txt": "c\n"}, "third", testsupport.CommitOptions{})
	repo.Checkout("main", false)
	return repo
}

func TestListBranchRefs(t *testing.T) {
	ctx := context.Background()
	repo := twoBranchRepo(t)

	refs, err := ListBranchRefs(ctx, repo.Dir)
	if err != nil {
		t.Fatalf("ListBranchRefs: %v", err)
	}
	if len(refs) != 2 || refs[0].Name != "feature/x" || refs[1].Name != "main" {
		t.Fatalf("branches not sorted by name: %+v", refs)
	}
	if refs[1].Head != repo.Rev("main") {
		t.Errorf("main head = %s, want %s", refs[1].Head, repo.Rev("main"))
	}
	if refs[1].HeadDate != "2024-01-01T00:02:00Z" {
		t.Errorf("main head date = %s", refs[1].HeadDate)
	}
}

func TestDetectDefaultBranchUsesHEAD(t *testing.T) {
	ctx := context.Background()
	repo := twoBranchRepo(t)
	refs, err := ListBranchRefs(ctx, repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	def, err := DetectDefaultBranch(ctx, repo.Dir, refs)
	if err != nil {
		t.Fatal(err)
	}
	if def.Name != "main" || len(def.Warnings) != 0 {
		t.Fatalf("default branch = %q with %d warnings, want main with none", def.Name, len(def.Warnings))
	}
}

// HEAD pointing at a branch with no commits is the case a fresh `git init -b trunk` leaves
// behind: the fallback has to pick a real branch AND say so.
func TestDetectDefaultBranchUnbornHEAD(t *testing.T) {
	ctx := context.Background()
	repo := twoBranchRepo(t)
	repo.SetHead("not-yet")

	refs, err := ListBranchRefs(ctx, repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	def, err := DetectDefaultBranch(ctx, repo.Dir, refs)
	if err != nil {
		t.Fatal(err)
	}
	if def.Name != "main" {
		t.Fatalf("fallback picked %q, want main", def.Name)
	}
	if len(def.Warnings) != 1 || def.Warnings[0].Code != "default-branch-fallback" {
		t.Fatalf("want one default-branch-fallback warning, got %+v", def.Warnings)
	}
	if def.Warnings[0].Message != "HEAD points at 'not-yet' which has no commits; using 'main' as the default branch" {
		t.Errorf("warning message: %q", def.Warnings[0].Message)
	}
}

func TestDetectDefaultBranchEmptyRepo(t *testing.T) {
	ctx := context.Background()
	repo := testsupport.Create(t, "", "")
	def, err := DetectDefaultBranch(ctx, repo.Dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if def.Name != "" || len(def.Warnings) != 0 {
		t.Fatalf("empty repo: name=%q warnings=%+v", def.Name, def.Warnings)
	}
}

func TestLoadBranchesCap(t *testing.T) {
	ctx := context.Background()
	repo := twoBranchRepo(t)
	refs, err := ListBranchRefs(ctx, repo.Dir)
	if err != nil {
		t.Fatal(err)
	}

	all, err := LoadBranches(ctx, repo.Dir, refs, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Branches) != 2 {
		t.Fatalf("want 2 branches, got %d", len(all.Branches))
	}
	if len(all.Branches[0].Commits) != 3 || len(all.Branches[1].Commits) != 2 {
		t.Fatalf("commit lists: feature/x=%d main=%d", len(all.Branches[0].Commits), len(all.Branches[1].Commits))
	}
	if len(all.Shas) != 3 {
		t.Fatalf("sha union = %d, want 3", len(all.Shas))
	}
	if len(all.Warnings) != 0 {
		t.Fatalf("uncapped load warned: %+v", all.Warnings)
	}

	one := int64(1)
	capped, err := LoadBranches(ctx, repo.Dir, refs, &one, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range capped.Branches {
		if len(b.Commits) != 1 || b.Commits[0] != b.Head {
			t.Fatalf("branch %s capped to %v, want just its head", b.Name, b.Commits)
		}
	}
	if len(capped.Warnings) != 1 || capped.Warnings[0].Code != "commits-capped" {
		t.Fatalf("want one commits-capped warning, got %+v", capped.Warnings)
	}
}

// The age window is anchored to the repo's newest commit, never to the clock — so a fixture
// dated 2024 still ages out correctly in any year.
func TestLoadBranchesAgeWindow(t *testing.T) {
	ctx := context.Background()
	repo := testsupport.Create(t, "", "")
	repo.WriteAndCommit(map[string]string{"old.txt": "old\n"}, "old", testsupport.CommitOptions{Date: "2020-01-01T00:00:00Z"})
	repo.WriteAndCommit(map[string]string{"new.txt": "new\n"}, "new", testsupport.CommitOptions{Date: "2024-01-01T00:00:00Z"})

	refs, err := ListBranchRefs(ctx, repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	days := int64(30)
	res, err := LoadBranches(ctx, repo.Dir, refs, nil, &days)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Branches[0].Commits) != 1 {
		t.Fatalf("want only the in-window commit, got %v", res.Branches[0].Commits)
	}
	if len(res.Warnings) != 1 || res.Warnings[0].Code != "commits-aged-out" {
		t.Fatalf("want one commits-aged-out warning, got %+v", res.Warnings)
	}
}

func TestLoadTags(t *testing.T) {
	ctx := context.Background()
	repo := twoBranchRepo(t)
	tags, err := LoadTags(ctx, repo.Dir)
	if err != nil {
		t.Fatalf("LoadTags: %v", err)
	}
	if len(tags) != 2 || tags[0].Name != "light" || tags[1].Name != "v1.0.0" {
		t.Fatalf("tags not sorted by name: %+v", tags)
	}

	light := tags[0]
	if light.Annotated || light.Message != nil || light.Tagger != nil {
		t.Errorf("lightweight tag carries annotation data: %+v", light)
	}
	if light.Target != repo.Rev("main") {
		t.Errorf("lightweight target = %s", light.Target)
	}

	ann := tags[1]
	if !ann.Annotated || ann.Message == nil || *ann.Message != "release one" {
		t.Errorf("annotated message = %v, want trimmed \"release one\"", ann.Message)
	}
	if ann.Tagger == nil || ann.Tagger.Email != "test@example.com" {
		t.Errorf("tagger = %+v", ann.Tagger)
	}
	if ann.Target == repo.Rev("v1.0.0") {
		// rev-parse on an annotated tag gives the tag object; the artifact records the commit.
		t.Errorf("target %s is the tag object, not the peeled commit", ann.Target)
	}
	if ann.Target != repo.Rev("v1.0.0^{commit}") {
		t.Errorf("target = %s, want the peeled commit", ann.Target)
	}
}
