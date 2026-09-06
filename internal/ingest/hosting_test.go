package ingest

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"frznforge/internal/model"
)

// hostedRepo is a repo stripped to what ResolveHosting reads: its slug, its branch names, its
// default branch and the file maps a hosted branch would serve.
func hostedRepo(slug string, defaultBranch string, branches []string, files map[string]model.FileInfo, refFiles map[string]map[string]model.FileInfo) *model.Repo {
	repo := &model.Repo{
		Slug:     slug,
		Files:    files,
		RefTrees: *model.NewRefTreeMap(),
		Warnings: []model.Warning{},
	}
	if defaultBranch != "" {
		repo.DefaultBranch = &defaultBranch
	}
	for _, name := range branches {
		repo.Branches = append(repo.Branches, model.Branch{Name: name})
	}
	for ref, f := range refFiles {
		repo.RefTrees.Set(ref, model.RefTree{Kind: "branch", Name: ref, Files: f})
	}
	return repo
}

func fileSet(paths ...string) map[string]model.FileInfo {
	out := map[string]model.FileInfo{}
	for _, p := range paths {
		out[p] = model.FileInfo{Path: p, Stored: true}
	}
	return out
}

func TestResolveHostedBranch(t *testing.T) {
	branches := []string{"main", "gh-pages", "site"}
	site := "site"
	missing := "nope"
	for _, tc := range []struct {
		names     []string
		requested *string
		want      string
	}{
		{branches, &site, "site"},
		{branches, &missing, ""},
		// The fallback order is gh-pages → main → master, whatever order the branches are in.
		{branches, nil, "gh-pages"},
		{[]string{"main", "master"}, nil, "main"},
		{[]string{"master", "topic"}, nil, "master"},
		{[]string{"topic"}, nil, ""},
		{nil, nil, ""},
	} {
		if got := ResolveHostedBranch(tc.names, tc.requested); got != tc.want {
			t.Errorf("ResolveHostedBranch(%v, %v) = %q, want %q", tc.names, tc.requested, got, tc.want)
		}
	}
}

func TestResolveHostingResolvesAndSorts(t *testing.T) {
	root := t.TempDir()
	cfg := testResolvedConfig(t, root, `{
      "owner": {"name": "Tester", "handle": "tester"},
      "hosting": {"sites": [
        {"repo": "zeta"},
        {"repo": "alpha", "slug": "docs", "branch": "site"}
      ]}
    }`)
	repos := []*model.Repo{
		hostedRepo("alpha", "main", []string{"main", "site"}, fileSet("README.md"),
			map[string]map[string]model.FileInfo{"site": fileSet("index.html")}),
		hostedRepo("zeta", "main", []string{"main", "gh-pages"}, fileSet("README.md"),
			map[string]map[string]model.FileInfo{"gh-pages": fileSet("index.html")}),
	}
	res, err := ResolveHosting(cfg, repos)
	if err != nil {
		t.Fatal(err)
	}
	want := []model.HostedSite{
		{Slug: "docs", Repo: "alpha", Ref: "site"},
		{Slug: "zeta", Repo: "zeta", Ref: "gh-pages"},
	}
	if !reflect.DeepEqual(res.Hosting, want) {
		t.Fatalf("hosting = %+v, want %+v (sorted by slug)", res.Hosting, want)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("warnings = %v", warningCodes(res.Warnings))
	}
	for _, r := range repos {
		if len(r.Warnings) != 0 {
			t.Errorf("%s picked up warnings: %v", r.Slug, warningCodes(r.Warnings))
		}
	}
}

// A hosting entry naming a repo that never reached the artifact is a site-level warning; a
// repo whose branch is missing is warned on the REPO, so it shows on that repo's page.
func TestResolveHostingWarnsRatherThanFails(t *testing.T) {
	root := t.TempDir()
	cfg := testResolvedConfig(t, root, `{
      "owner": {"name": "Tester", "handle": "tester"},
      "hosting": {"sites": [
        {"repo": "ghost"},
        {"repo": "explicit", "branch": "nope"},
        {"repo": "auto"}
      ]}
    }`)
	explicit := hostedRepo("explicit", "main", []string{"main"}, fileSet("a.txt"), nil)
	auto := hostedRepo("auto", "trunk", []string{"trunk"}, fileSet("a.txt"), nil)
	res, err := ResolveHosting(cfg, []*model.Repo{explicit, auto})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hosting) != 0 {
		t.Fatalf("hosting = %+v, want nothing served", res.Hosting)
	}
	if got := warningCodes(res.Warnings); !reflect.DeepEqual(got, []string{"hosting-unknown-repo"}) {
		t.Fatalf("site warnings = %v", got)
	}
	if res.Warnings[0].Repo != nil {
		t.Error("hosting-unknown-repo is site-level")
	}
	for _, repo := range []*model.Repo{explicit, auto} {
		if got := warningCodes(repo.Warnings); !reflect.DeepEqual(got, []string{"hosting-branch-missing"}) {
			t.Fatalf("%s warnings = %v", repo.Slug, got)
		}
		if repo.Warnings[0].Repo == nil || *repo.Warnings[0].Repo != repo.Slug {
			t.Errorf("%s warning is not scoped to it: %v", repo.Slug, repo.Warnings[0].Repo)
		}
	}
	// A named branch and an auto-resolved one need different explanations.
	if !strings.Contains(explicit.Warnings[0].Message, "'nope' does not exist") {
		t.Errorf("explicit message = %q", explicit.Warnings[0].Message)
	}
	if !strings.Contains(auto.Warnings[0].Message, "gh-pages, main, master") {
		t.Errorf("auto message = %q", auto.Warnings[0].Message)
	}
}

func TestResolveHostingCountsUnservableFiles(t *testing.T) {
	root := t.TempDir()
	cfg := testResolvedConfig(t, root, `{
      "owner": {"name": "Tester", "handle": "tester"},
      "hosting": {"sites": [{"repo": "site"}]}
    }`)
	// The hosted branch IS the default branch here, so the count comes from repo.Files.
	repo := hostedRepo("site", "main", []string{"main"},
		fileSet("index.html", "c#.css", "50%.js", "ok.js"), nil)
	res, err := ResolveHosting(cfg, []*model.Repo{repo})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hosting) != 1 || res.Hosting[0].Ref != "main" {
		t.Fatalf("hosting = %+v", res.Hosting)
	}
	if got := warningCodes(repo.Warnings); !reflect.DeepEqual(got, []string{"hosting-file-unservable"}) {
		t.Fatalf("warnings = %v", got)
	}
	if !strings.Contains(repo.Warnings[0].Message, "2 file(s)") {
		t.Errorf("message = %q", repo.Warnings[0].Message)
	}
}

// A hosted slug fighting public/ over who owns /x/… would be a wrong site that reports
// success, so it is a hard error rather than a warning.
func TestResolveHostingRejectsPublicCollision(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "public", "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := testResolvedConfig(t, root, `{
      "owner": {"name": "Tester", "handle": "tester"},
      "hosting": {"sites": [{"repo": "alpha", "slug": "docs"}]}
    }`)
	repo := hostedRepo("alpha", "main", []string{"main", "gh-pages"}, fileSet("a"), nil)
	_, err := ResolveHosting(cfg, []*model.Repo{repo})
	if err == nil {
		t.Fatal("want an error for a hosted slug that collides with public/")
	}
	if !strings.Contains(err.Error(), "public/docs") {
		t.Errorf("error = %v", err)
	}
}
