package ingest

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"frznforge/internal/config"
	"frznforge/internal/model"
)

/* ---- helpers -------------------------------------------------------------- */

// scannedFixture is a repo record shaped the way ScanRepo leaves one, with just enough filled
// in for the assembly to have something to order, rename and warn about.
func scannedFixture(slug, path string, warnings ...model.Warning) ScannedRepo {
	stamped := make([]model.Warning, len(warnings))
	for i, w := range warnings {
		s := slug
		w.Repo = &s
		stamped[i] = w
	}
	local := path
	repo := &model.Repo{
		Slug:         slug,
		Name:         slug,
		Source:       model.RepoSource{Type: "local", Path: &local},
		Tags:         []string{},
		Releases:     []model.Release{},
		Branches:     []model.Branch{{Name: "main", Head: "abc", Commits: []string{}, LastCommitDate: "2026-01-01T00:00:00Z"}},
		GitTags:      []model.Tag{},
		Commits:      map[string]model.Commit{},
		ExtraCommits: map[string]model.Commit{},
		Tree:         []model.TreeEntry{},
		Files:        map[string]model.FileInfo{},
		RefTrees:     *model.NewRefTreeMap(),
		Archives:     []model.Archive{},
		Languages:    []model.LanguageStat{},
		Contributors: []model.Contributor{},
		Warnings:     stamped,
	}
	main := "main"
	repo.DefaultBranch = &main
	return ScannedRepo{Result: ScanResult{Repo: repo, Blobs: map[string][]byte{}}}
}

// withArchive attaches one archive to a fixture, on both the artifact record and the byte
// carrier — the two the collision rename has to keep in step.
func withArchive(entry ScannedRepo, refSlug string, data []byte) ScannedRepo {
	file := "archives/" + entry.Result.Repo.Slug + "/" + refSlug + ".zip"
	entry.Result.Repo.Archives = append(entry.Result.Repo.Archives, model.Archive{
		Ref: refSlug, Kind: "branch", Commit: "abc", File: file, Bytes: int64(len(data)),
	})
	entry.Result.Archives = append(entry.Result.Archives, ArchiveData{File: file, Data: data})
	return entry
}

func repoSlugs(repos []model.Repo) []string {
	out := make([]string, len(repos))
	for i, r := range repos {
		out[i] = r.Slug
	}
	return out
}

/* ---- assembly ------------------------------------------------------------- */

func TestAssembleSortsAndRenamesCollidingSlugs(t *testing.T) {
	cfg := bareConfig(t)
	first := withArchive(scannedFixture("beta", "/repos/beta"), "main", []byte("first"))
	second := withArchive(scannedFixture("beta", "/repos/beta-clone",
		model.Warning{Code: "repo-empty", Message: "no commits"}), "main", []byte("second"))
	third := scannedFixture("alpha", "/repos/alpha")

	res, err := Assemble(cfg, []ScannedRepo{first, second, third})
	if err != nil {
		t.Fatal(err)
	}
	if got := repoSlugs(res.Data.Repos); !reflect.DeepEqual(got, []string{"alpha", "beta", "beta-2"}) {
		t.Fatalf("repos = %v, want sorted by slug with the second 'beta' renamed", got)
	}
	// The winner is whoever reached the assembly first, not whoever sorts first.
	if res.Data.Repos[1].Source.Label() != "/repos/beta" {
		t.Errorf("the wrong repo kept the bare slug: %s", res.Data.Repos[1].Source.Label())
	}

	renamed := res.Data.Repos[2]
	for _, w := range renamed.Warnings {
		if w.Repo == nil || *w.Repo != "beta-2" {
			t.Errorf("a warning still carries the pre-rename slug: %+v", w)
		}
	}
	// Archive paths embed the slug, on the record and on the bytes alike — otherwise the file
	// on disk and the path in forge.json disagree.
	if renamed.Archives[0].File != "archives/beta-2/main.zip" {
		t.Errorf("archive record = %q", renamed.Archives[0].File)
	}
	if string(res.Archives["archives/beta-2/main.zip"]) != "second" {
		t.Errorf("archive bytes are not under the renamed path: %v", sortedArchiveKeys(res.Archives))
	}
	if _, stale := res.Archives["archives/beta/main.zip"]; !stale {
		t.Error("the repo that kept its slug should still own archives/beta/main.zip")
	}

	// The slug-collision warning names the NEW slug and comes from the site-level list.
	if res.Data.Warnings[0].Code != "slug-collision" {
		t.Fatalf("first warning = %+v, want the site-level collision", res.Data.Warnings[0])
	}
	if res.Data.Warnings[0].Repo == nil || *res.Data.Warnings[0].Repo != "beta-2" {
		t.Errorf("collision warning repo = %v", res.Data.Warnings[0].Repo)
	}
	if !strings.Contains(res.Data.Warnings[0].Message, "/repos/beta-clone") {
		t.Errorf("the message does not identify which repo moved: %q", res.Data.Warnings[0].Message)
	}
}

// A third repo taking the same slug gets -3, and the suffix search skips a name already taken.
func TestAssembleCollisionSuffixesCountUp(t *testing.T) {
	cfg := bareConfig(t)
	res, err := Assemble(cfg, []ScannedRepo{
		scannedFixture("beta", "/a"),
		scannedFixture("beta-2", "/b"), // already occupies the first suffix
		scannedFixture("beta", "/c"),
		scannedFixture("beta", "/d"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := repoSlugs(res.Data.Repos); !reflect.DeepEqual(got, []string{"beta", "beta-2", "beta-3", "beta-4"}) {
		t.Fatalf("repos = %v", got)
	}
}

// A source that was not a repository never becomes a Repo; its warning goes to the site-level
// list, in the order the caller handed the results over.
func TestAssembleKeepsSkippedSourcesOutOfTheArtifact(t *testing.T) {
	cfg := bareConfig(t)
	slug := "missing"
	skipped := ScannedRepo{Result: ScanResult{
		Skipped: true,
		Warning: &model.Warning{Code: "repo-not-found", Message: "/nope is not a git repository"},
	}}
	remote := ScannedRepo{
		Result:         ScanResult{Skipped: true, Warning: &model.Warning{Code: "repo-not-found", Repo: &slug, Message: "no mirror"}},
		RemoteWarnings: []model.Warning{{Code: "remote-fetch-failed", Repo: &slug, Message: "offline"}},
	}
	res, err := Assemble(cfg, []ScannedRepo{skipped, remote, scannedFixture("alpha", "/a")})
	if err != nil {
		t.Fatal(err)
	}
	if got := repoSlugs(res.Data.Repos); !reflect.DeepEqual(got, []string{"alpha"}) {
		t.Fatalf("repos = %v", got)
	}
	want := []string{"repo-not-found", "remote-fetch-failed", "repo-not-found"}
	if got := warningCodes(res.Data.Warnings); !reflect.DeepEqual(got, want) {
		t.Fatalf("warnings = %v, want %v", got, want)
	}
}

// Import warnings ride on the repo so they show on its page and get renamed with it — and they
// come FIRST, because an import problem explains everything under it.
func TestAssembleMovesRemoteWarningsOntoTheRepo(t *testing.T) {
	cfg := bareConfig(t)
	slug := "beta"
	entry := scannedFixture("beta", "/b", model.Warning{Code: "repo-empty", Message: "no commits"})
	entry.RemoteWarnings = []model.Warning{{Code: "remote-cache-stale", Repo: &slug, Message: "served from cache"}}
	// A second repo takes the slug first, so the rename has to restamp the remote warning too.
	res, err := Assemble(cfg, []ScannedRepo{scannedFixture("beta", "/a"), entry})
	if err != nil {
		t.Fatal(err)
	}
	renamed := res.Data.Repos[1]
	if got := warningCodes(renamed.Warnings); !reflect.DeepEqual(got, []string{"remote-cache-stale", "repo-empty"}) {
		t.Fatalf("repo warnings = %v", got)
	}
	for _, w := range renamed.Warnings {
		if w.Repo == nil || *w.Repo != "beta-2" {
			t.Errorf("warning not restamped: %+v", w)
		}
	}
}

// The site-level warning list has one fixed order, and it is part of the artifact's bytes.
func TestAssembleWarningOrder(t *testing.T) {
	root := t.TempDir()
	notesDir := filepath.Join(root, "notes")
	if err := os.MkdirAll(notesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one.md", "one.markdown"} {
		if err := os.WriteFile(filepath.Join(notesDir, name), []byte("# One\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := testResolvedConfig(t, root, `{
      "owner": {"name": "Tester", "handle": "tester"},
      "notes": {"dir": "./notes"},
      "organizations": [{"slug": "tools", "name": "Tools", "repos": ["ghost"]}],
      "contributors": [{"name": "Nobody", "emails": ["nobody@example.com"]}],
      "hosting": {"sites": [{"repo": "phantom"}]}
    }`)
	res, err := Assemble(cfg, []ScannedRepo{
		scannedFixture("alpha", "/a"),
		scannedFixture("alpha", "/b", model.Warning{Code: "repo-empty", Message: "no commits"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"slug-collision",            // site-level
		"note-slug-collision",       // notes
		"org-unknown-repo",          // organizations
		"contributor-unknown-email", // contributors
		"hosting-unknown-repo",      // hosting
		"repo-empty",                // then per repo, in slug order
	}
	if got := warningCodes(res.Data.Warnings); !reflect.DeepEqual(got, want) {
		t.Fatalf("warnings = %v\nwant %v", got, want)
	}
	if !strings.Contains(res.Data.Warnings[3].Message, "'nobody@example.com'") {
		t.Errorf("contributor warning = %q", res.Data.Warnings[3].Message)
	}
}

// Notes are merged into the blob store LAST, which is exactly why their keys are
// domain-separated from git object ids.
func TestAssembleMergesNoteBlobs(t *testing.T) {
	root := t.TempDir()
	notesDir := filepath.Join(root, "notes")
	if err := os.MkdirAll(notesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := []byte("# A note\n")
	if err := os.WriteFile(filepath.Join(notesDir, "a.md"), src, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := testResolvedConfig(t, root,
		`{"owner": {"name": "Tester", "handle": "tester"}, "notes": {"dir": "./notes"}}`)

	entry := scannedFixture("alpha", "/a")
	entry.Result.Blobs["deadbeef"] = []byte("repo file")
	res, err := Assemble(cfg, []ScannedRepo{entry})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Data.Notes) != 1 || res.Data.Notes[0].Slug != "a" {
		t.Fatalf("notes = %+v", res.Data.Notes)
	}
	if string(res.Blobs["deadbeef"]) != "repo file" {
		t.Error("a repo blob went missing")
	}
	if string(res.Blobs[noteShaOf(src)]) != string(src) {
		t.Error("the note blob is not in the shared store under its own key")
	}
}

// Organization membership and hosting both name the FINAL slug, which is why they are resolved
// after the collision pass rather than beside it.
func TestAssembleResolvesOrgsAndHostingAgainstFinalSlugs(t *testing.T) {
	root := t.TempDir()
	cfg := testResolvedConfig(t, root, `{
      "owner": {"name": "Tester", "handle": "tester"},
      "organizations": [{"slug": "tools", "name": "Tools", "repos": ["beta-2"]}],
      "hosting": {"sites": [{"repo": "beta-2", "slug": "site"}]}
    }`)
	loser := scannedFixture("beta", "/b")
	loser.Org = "tools"
	loser.Result.Repo.Branches = append(loser.Result.Repo.Branches, model.Branch{Name: "gh-pages"})
	loser.Result.Repo.RefTrees.Set("gh-pages", model.RefTree{
		Kind: "branch", Name: "gh-pages", Commit: "abc",
		Tree: []model.TreeEntry{}, Files: map[string]model.FileInfo{},
	})

	res, err := Assemble(cfg, []ScannedRepo{scannedFixture("beta", "/a"), loser})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Data.Organizations[0].Repos; !reflect.DeepEqual(got, []string{"beta-2"}) {
		t.Errorf("org members = %v, want the post-rename slug", got)
	}
	want := []model.HostedSite{{Slug: "site", Repo: "beta-2", Ref: "gh-pages"}}
	if !reflect.DeepEqual(res.Data.Hosting, want) {
		t.Errorf("hosting = %+v, want %+v", res.Data.Hosting, want)
	}
}

// Whatever order the scan pool finished in, the artifact is the same — except for the one
// thing the caller is responsible for: which repo keeps a contested slug.
func TestAssembleEmptyInputIsAValidArtifact(t *testing.T) {
	res, err := Assemble(bareConfig(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := model.Validate(&res.Data); err != nil {
		t.Fatalf("the empty artifact does not validate: %v", err)
	}
	raw, err := model.Serialize(res.Data)
	if err != nil {
		t.Fatal(err)
	}
	// Empty slices, never nulls: a nil slice marshals as `null` and the artifact says `[]`.
	for _, key := range []string{`"repos": []`, `"notes": []`, `"organizations": []`, `"hosting": []`, `"warnings": []`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("serialized artifact is missing %s:\n%s", key, raw)
		}
	}
}

func TestPreScanSlug(t *testing.T) {
	dir := t.TempDir()
	bare := filepath.Join(dir, "My Repo.git")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		src  config.ResolvedSource
		want string
	}{
		{
			"local directory basename, .git dropped",
			config.ResolvedSource{RepoSourceConfig: config.RepoSourceConfig{Type: "local"}, AbsPath: bare},
			"my-repo",
		},
		{
			"an explicit slug wins",
			config.ResolvedSource{RepoSourceConfig: config.RepoSourceConfig{Type: "local", Slug: "chosen"}, AbsPath: bare},
			"chosen",
		},
		{
			// A remote source uses its configured name, never the mirror directory — that
			// carries a cache-key digest no user ever typed.
			"github repo name, not the mirror path",
			config.ResolvedSource{
				RepoSourceConfig: config.RepoSourceConfig{Type: "github", Owner: "o", Repo: "Some_Repo"},
				AbsPath:          filepath.Join(dir, "mirrors", "github-api.github.com-o-some-repo"),
			},
			"some-repo",
		},
		{
			"gitlab uses the last segment of the namespaced project path",
			config.ResolvedSource{
				RepoSourceConfig: config.RepoSourceConfig{Type: "gitlab", Project: "group/sub/My Proj"},
				AbsPath:          filepath.Join(dir, "mirrors", "x"),
			},
			"my-proj",
		},
	} {
		if got := PreScanSlug(tc.src); got != tc.want {
			t.Errorf("%s: PreScanSlug = %q, want %q", tc.name, got, tc.want)
		}
	}
}

/* ---- writing -------------------------------------------------------------- */

func sortedArchiveKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// blobs/ and archives/ must MIRROR their maps: new files written, changed files rewritten,
// stale files deleted. A blob left behind is content the site still serves after the repo
// stopped tracking it.
func TestWriteArtifactMirrorsAndPrunes(t *testing.T) {
	out := t.TempDir()
	data := model.EmptyForgeData()

	stale := filepath.Join(out, BlobDirname, "staleblob")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	staleArchive := filepath.Join(out, ArchiveDirname, "gone", "main.zip")
	if err := os.MkdirAll(filepath.Dir(staleArchive), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staleArchive, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	blobs := map[string][]byte{"aaaa": []byte("kept")}
	archives := map[string][]byte{"archives/alpha/main.zip": []byte("zipbytes")}
	if err := WriteArtifact(data, blobs, archives, out); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("a stale blob survived the mirror pass")
	}
	if _, err := os.Stat(filepath.Join(out, ArchiveDirname, "gone")); !os.IsNotExist(err) {
		t.Error("an emptied per-repo archive directory survived")
	}
	got, err := os.ReadFile(filepath.Join(out, BlobDirname, "aaaa"))
	if err != nil || string(got) != "kept" {
		t.Errorf("blob = %q, %v", got, err)
	}
	got, err = os.ReadFile(filepath.Join(out, ArchiveDirname, "alpha", "main.zip"))
	if err != nil || string(got) != "zipbytes" {
		t.Errorf("archive = %q, %v", got, err)
	}
	// The artifact on disk is exactly what model.Serialize produces.
	want, err := model.Serialize(data)
	if err != nil {
		t.Fatal(err)
	}
	onDisk, err := os.ReadFile(filepath.Join(out, ArtifactFilename))
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != string(want) {
		t.Errorf("forge.json is not the serialized artifact:\n%q\n%q", onDisk, want)
	}
}

// An archive key outside archives/ would write a file wherever it pointed, so it is refused.
func TestWriteArtifactRejectsArchivePathsOutsideTheStore(t *testing.T) {
	err := WriteArtifact(model.EmptyForgeData(), nil, map[string][]byte{"elsewhere/x.zip": {}}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "outside archives/") {
		t.Fatalf("err = %v", err)
	}
}

// A structurally invalid artifact is an ingest bug, not a repo-state problem: it stops the run
// instead of being written and read back as a broken site.
func TestWriteArtifactValidatesBeforeWriting(t *testing.T) {
	out := t.TempDir()
	bad := model.EmptyForgeData()
	bad.Warnings = append(bad.Warnings, model.Warning{Code: "not-a-real-code", Message: "x"})
	if err := WriteArtifact(bad, nil, nil, out); err == nil {
		t.Fatal("want an error for an artifact that does not validate")
	}
	if _, err := os.Stat(filepath.Join(out, ArtifactFilename)); !os.IsNotExist(err) {
		t.Error("an invalid artifact was written to disk anyway")
	}
}
