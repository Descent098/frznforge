package ingest

import (
	"context"
	"os"
	"sort"
	"strings"
	"testing"

	"frznforge/internal/ingest/testsupport"
	"frznforge/internal/model"
)

func testScanOptions() ScanOptions {
	return ScanOptions{
		MaxBlobBytes: 512 * 1024,
		TagTrees:     25,
		BranchTrees:  10,
		Archives:     false,
		Insights:     DefaultInsightsOptions,
	}
}

func warningCodes(warnings []model.Warning) []string {
	codes := make([]string, 0, len(warnings))
	for _, w := range warnings {
		codes = append(codes, w.Code)
	}
	return codes
}

// A path that is not a repository is a warning and a skip, never a failure: one mistyped
// config entry must not take the whole forge down.
func TestScanRepoNotAGitRepo(t *testing.T) {
	dir, err := os.MkdirTemp("", "frznforge-notrepo-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	res, err := ScanRepo(context.Background(), ScanSource{AbsPath: dir, Slug: "missing"}, testScanOptions())
	if err != nil {
		t.Fatalf("ScanRepo: %v", err)
	}
	if !res.Skipped || res.Repo != nil {
		t.Fatalf("want a skip, got repo=%v", res.Repo)
	}
	if res.Warning == nil || res.Warning.Code != "repo-not-found" {
		t.Fatalf("warning = %+v", res.Warning)
	}
	// The skip warning is site-level: no repo reached the artifact to attach it to.
	if res.Warning.Repo != nil {
		t.Errorf("skip warning is scoped to a repo: %v", *res.Warning.Repo)
	}
}

func TestScanRepoEmpty(t *testing.T) {
	repo := testsupport.Create(t, "empty-repo", "main")
	res, err := ScanRepo(context.Background(), ScanSource{AbsPath: repo.Dir}, testScanOptions())
	if err != nil {
		t.Fatalf("ScanRepo: %v", err)
	}
	r := res.Repo
	if !r.Empty || r.DefaultBranch != nil || r.CreatedAt != nil || r.UpdatedAt != nil {
		t.Errorf("empty repo: empty=%v default=%v created=%v", r.Empty, r.DefaultBranch, r.CreatedAt)
	}
	if r.Insights != nil {
		t.Errorf("empty repo has insights: %+v", r.Insights)
	}
	// Every collection is present and empty rather than absent — a nil slice would serialise
	// as null and break the artifact contract.
	if r.Branches == nil || r.GitTags == nil || r.Tree == nil || r.Files == nil ||
		r.Archives == nil || r.Languages == nil || r.Contributors == nil || r.Releases == nil || r.Tags == nil {
		t.Errorf("empty repo has a nil collection: %+v", r)
	}
	if codes := warningCodes(r.Warnings); len(codes) != 1 || codes[0] != "repo-empty" {
		t.Errorf("warnings = %v, want exactly repo-empty", codes)
	}
	if r.Warnings[0].Repo == nil || *r.Warnings[0].Repo != "empty-repo" {
		t.Errorf("warning is not stamped with the slug: %+v", r.Warnings[0])
	}
}

// The two caps and the unservable-path check all report, and each says what to change.
func TestScanRepoCapWarnings(t *testing.T) {
	repo := testsupport.Create(t, "capped-repo", "main")
	repo.WriteAndCommit(map[string]string{"a.txt": "a\n", "bad#name.txt": "sharp\n"}, "first", testsupport.CommitOptions{})
	repo.Tag("v1", testsupport.TagOptions{Annotated: true})
	repo.Tag("v2", testsupport.TagOptions{Annotated: true})
	repo.Checkout("one", true)
	repo.WriteAndCommit(map[string]string{"one.txt": "1\n"}, "one", testsupport.CommitOptions{})
	repo.Checkout("main", false)
	repo.Checkout("two", true)
	repo.WriteAndCommit(map[string]string{"two.txt": "2\n"}, "two", testsupport.CommitOptions{})
	repo.Checkout("main", false)

	opts := testScanOptions()
	opts.BranchTrees = 1
	opts.TagTrees = 1

	res, err := ScanRepo(context.Background(), ScanSource{AbsPath: repo.Dir}, opts)
	if err != nil {
		t.Fatalf("ScanRepo: %v", err)
	}
	codes := warningCodes(res.Repo.Warnings)
	sort.Strings(codes)
	// The unservable path is reported once per BROWSABLE REF that carries it — the default
	// branch, the one branch that kept a tree, and the one tag that kept a tree — because the
	// missing page is per ref, not per repository.
	want := []string{"branch-trees-capped", "repo-path-unservable", "repo-path-unservable",
		"repo-path-unservable", "tag-trees-capped"}
	if strings.Join(codes, ",") != strings.Join(want, ",") {
		t.Fatalf("warnings = %v, want %v", codes, want)
	}
	for _, w := range res.Repo.Warnings {
		switch w.Code {
		case "branch-trees-capped":
			if w.Message != "2 of 3 branches have browsable trees (ingest.branchTrees = 1)" {
				t.Errorf("branch cap message: %q", w.Message)
			}
		case "tag-trees-capped":
			if w.Message != "1 of 2 tags have browsable trees (ingest.tagTrees = 1)" {
				t.Errorf("tag cap message: %q", w.Message)
			}
		case "repo-path-unservable":
			if !strings.Contains(w.Message, "bad#name.txt") {
				t.Errorf("unservable message: %q", w.Message)
			}
		}
	}
	// One non-default branch got a tree; the default branch's own tree is not a refTree.
	if res.Repo.RefTrees.Len() != 2 {
		t.Errorf("refTrees = %d entries, want the capped branch plus the capped tag", res.Repo.RefTrees.Len())
	}
}

// A hosted branch is exempt from the branch cap and is scanned with the bigger hosted cap —
// a dormant gh-pages is exactly the branch the cap would otherwise drop, and an unstored file
// is a silent 404 on the hosted site.
func TestScanRepoHostedBranchIsExemptFromTheCap(t *testing.T) {
	repo := testsupport.Create(t, "hosted-repo", "main")
	repo.WriteAndCommit(map[string]string{"a.txt": "a\n"}, "first", testsupport.CommitOptions{})
	repo.Checkout("gh-pages", true)
	repo.WriteAndCommit(map[string]string{"bundle.js": strings.Repeat("z", 300)}, "site",
		testsupport.CommitOptions{Date: "2023-01-01T00:00:00Z"})
	repo.Checkout("main", false)
	repo.Checkout("recent", true)
	repo.WriteAndCommit(map[string]string{"recent.txt": "r\n"}, "recent", testsupport.CommitOptions{})
	repo.Checkout("main", false)

	opts := testScanOptions()
	opts.BranchTrees = 1 // only "recent" fits; gh-pages is older
	opts.MaxBlobBytes = 128
	opts.HostedMaxFileBytes = 4096

	res, err := ScanRepo(context.Background(), ScanSource{AbsPath: repo.Dir, HostedRequests: []*string{nil}}, opts)
	if err != nil {
		t.Fatalf("ScanRepo: %v", err)
	}
	pages, ok := res.Repo.RefTrees.Get("gh-pages")
	if !ok {
		t.Fatalf("gh-pages has no tree; refTrees = %v", res.Repo.RefTrees.Keys())
	}
	bundle := pages.Files["bundle.js"]
	if bundle.TooLarge || !bundle.Stored {
		t.Errorf("bundle.js on the hosted branch was not stored: %+v", bundle)
	}
	if _, ok := res.Blobs[bundle.Sha]; !ok {
		t.Error("the hosted branch's bundle never reached the blob store")
	}
}

// A tag pointing at a commit no branch reaches still has to name that commit, so it lands in
// extraCommits — which stays disjoint from commits, because everything derived from `commits`
// (counts, contributors, dates) must keep meaning "the kept history".
func TestScanRepoExtraCommits(t *testing.T) {
	repo := testsupport.Create(t, "orphan-repo", "main")
	repo.WriteAndCommit(map[string]string{"a.txt": "a\n"}, "first", testsupport.CommitOptions{})
	repo.Checkout("gone", true)
	orphan := repo.WriteAndCommit(map[string]string{"b.txt": "b\n"}, "orphan", testsupport.CommitOptions{})
	repo.Tag("v9", testsupport.TagOptions{Annotated: true})
	repo.Checkout("main", false)
	repo.Git("branch", "-D", "gone")

	res, err := ScanRepo(context.Background(), ScanSource{AbsPath: repo.Dir}, testScanOptions())
	if err != nil {
		t.Fatalf("ScanRepo: %v", err)
	}
	if _, ok := res.Repo.ExtraCommits[orphan]; !ok {
		t.Fatalf("the orphaned tag target is missing from extraCommits: %v", keysOfCommits(res.Repo.ExtraCommits))
	}
	if _, ok := res.Repo.Commits[orphan]; ok {
		t.Error("the orphaned commit leaked into commits, which would inflate every aggregate")
	}
	if res.Repo.CommitCount != 1 {
		t.Errorf("commitCount = %d, want the branch history only", res.Repo.CommitCount)
	}
	if res.Repo.CommitFor(orphan) == nil {
		t.Error("CommitFor cannot reach the display-support commit")
	}
}

// Broken metadata is a warning and nothing more. The MESSAGE differs from the TypeScript's —
// it quotes the JSON parser's own words, and Go's parser does not phrase things the way
// V8 does — so only the code and the fallback behaviour are contract here.
func TestScanRepoInvalidMeta(t *testing.T) {
	repo := testsupport.Create(t, "meta-repo", "main")
	repo.WriteAndCommit(map[string]string{".frznforge.json": "{ not json"}, "bad meta", testsupport.CommitOptions{})

	res, err := ScanRepo(context.Background(), ScanSource{AbsPath: repo.Dir}, testScanOptions())
	if err != nil {
		t.Fatalf("ScanRepo: %v", err)
	}
	if codes := warningCodes(res.Repo.Warnings); len(codes) != 1 || codes[0] != "repo-meta-invalid" {
		t.Fatalf("warnings = %v", codes)
	}
	// The derived default survives: the repo is still named after its directory.
	if res.Repo.Name != "meta-repo" {
		t.Errorf("name = %q, want the directory basename", res.Repo.Name)
	}
}

// Scanning the same repository twice must produce the same bytes. Go randomises map iteration
// on every run, so a sort this package forgot shows up here as a flake rather than as a
// mysterious diff in someone's artifact months later.
func TestScanRepoIsDeterministic(t *testing.T) {
	repo := testsupport.Create(t, "stable-repo", "main")
	repo.WriteAndCommit(map[string]string{
		"README.md":   "# stable\n",
		"src/a.go":    "package a\n",
		"src/b.ts":    "export const b = 1;\n",
		"docs/one.md": "one\n",
	}, "first", testsupport.CommitOptions{})
	repo.Tag("v1", testsupport.TagOptions{Annotated: true})
	repo.Checkout("other", true)
	repo.WriteAndCommit(map[string]string{"src/c.py": "c = 1\n"}, "second",
		testsupport.CommitOptions{AuthorName: "Second Person", AuthorEmail: "second@example.com"})
	repo.Checkout("main", false)

	var first string
	for i := 0; i < 3; i++ {
		res, err := ScanRepo(context.Background(), ScanSource{AbsPath: repo.Dir}, testScanOptions())
		if err != nil {
			t.Fatalf("scan %d: %v", i, err)
		}
		got := encodeLikeArtifact(t, res.Repo)
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			t.Fatalf("scan %d differs from the first\n%s", i, firstDifference(first, got))
		}
	}
}

func keysOfCommits(m map[string]model.Commit) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
