package ingest

// The acceptance bar for the whole port, automated: for the same repositories at the same
// commits, `frznforge ingest` must write a byte-identical forge.json to `npm run ingest`, the
// same blob set, and the same archive bytes.
//
// Every other parity test in this package compares one module's output. This one compares the
// artifact — which is the only comparison that can catch the failures a module test cannot see:
// a pool that hands the assembly its results in a different order, a warning list assembled in
// a different sequence, a slug collision resolved in favour of the other repo, an archive
// written under a path the record does not name.
//
// Both engines read ONE config file (strict JSON, which is also valid JSONC) and differ only in
// their output directory, so a divergence is never "the two were configured differently".

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"frznforge/internal/config"
	"frznforge/internal/model"
)

/* ---- the corpus ----------------------------------------------------------- */

// remoteFixture is one stand-in for a provider: a local git repo the mirror is cloned from,
// plus the answers the importer would have returned. Marshalled into the TypeScript driver's
// request and used directly by the Go side, so both engines import the same bytes.
type remoteFixture struct {
	Origin   string           `json:"origin"`
	Meta     ImportedRepoMeta `json:"meta"`
	Releases []model.Release  `json:"releases"`
}

// corpus is a built fixture site: a project root holding repos, provider origins, notes and the
// config both engines read.
type corpus struct {
	root       string
	configPath string
	remotes    map[string]remoteFixture
}

// remoteKeyFor is the fixture key for a configured remote source. It matches the function of
// the same name in testdata/ts-ingest.ts; the two must agree or one engine imports nothing.
func remoteKeyFor(src config.RepoSourceConfig) string {
	if src.Type == "local" {
		return ""
	}
	if src.Type == "gitlab" {
		return src.Project
	}
	return src.Owner + "/" + src.Repo
}

// identityCorpus builds a site that exercises the parts of the pipeline a single-repo scan
// cannot reach: a slug collision (which renames a repo AND moves its archive), a source path
// that is not a repository, an empty repository, provider imports with and without releases,
// notes, organizations claimed from both directions, hosting, and a configured contributor
// matching nobody. Every one of those produces a warning or an ordering decision made in
// ingest.go or assemble.go rather than in the scanner.
func identityCorpus(t *testing.T) corpus {
	t.Helper()
	root := tempCorpusDir(t)

	// An empty global git config for every git process in this test — the ingests' own reads
	// included, since they inherit this environment. A developer's `core.quotepath` or
	// `log.date` must not be able to change what either engine parses.
	empty := filepath.Join(root, "gitconfig-empty")
	writeCorpusFile(t, empty, "")
	t.Setenv("GIT_CONFIG_GLOBAL", empty)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")

	repos := filepath.Join(root, "repos")
	origins := filepath.Join(root, "origins")

	// alpha — the ordinary repo: committed metadata, a licence, several languages, awkward
	// committed filenames, a binary, an over-cap file, tags (two annotated, one lightweight), a
	// second branch and a gh-pages branch the hosting config serves.
	alpha := initRepo(t, repos, "alpha")
	writeCorpusFile(t, filepath.Join(alpha, "README.md"),
		"# Alpha\n\nA **fixture** repo.\n\n- bullet one\n- bullet two\n")
	writeCorpusFile(t, filepath.Join(alpha, ".frznforge.json"),
		`{"description":"Alpha fixture: a static site generator.","tags":["ssg","astro"],`+
			`"links":{"homepage":"https://example.com/alpha","upstream":"https://github.com/example/alpha"}}`+"\n")
	writeCorpusFile(t, filepath.Join(alpha, "LICENSE"),
		"MIT License\n\nCopyright (c) 2024 Fixture\n\nPermission is hereby granted, free of charge...")
	writeCorpusFile(t, filepath.Join(alpha, "src/index.ts"),
		strings.Repeat("export const answer: number = 42;\n", 20))
	writeCorpusFile(t, filepath.Join(alpha, "src/lib/index.ts"), "export {};\n")
	writeCorpusFile(t, filepath.Join(alpha, "src/style.css"), strings.Repeat("body { margin: 0; }\n", 5))
	writeCorpusFile(t, filepath.Join(alpha, "docs/guide.md"), "# Guide\n\nSome **bold** fixture text.\n")
	// URL-hostile committed names: a space must survive percent-encoding, and '#' and '%'
	// cannot be served statically at all, so they must be listed-but-unlinked.
	writeCorpusFile(t, filepath.Join(alpha, "docs/read me.md"), "# Read me\n\nA name with a space.\n")
	writeCorpusFile(t, filepath.Join(alpha, "docs/50% off.txt"), "percent in the name\n")
	writeCorpusFile(t, filepath.Join(alpha, "docs/c#-tips.md"), "# C# tips\n\nHash in the name.\n")
	// Non-ASCII path (core.quotepath) and CRLF content, built from runes so this file stays ASCII.
	writeCorpusFile(t, filepath.Join(alpha, "docs/caf"+string(rune(0xe9))+".md"), "cafe notes\n")
	writeCorpusFile(t, filepath.Join(alpha, "crlf.txt"), "one\r\ntwo\r\n")
	// Over the 4096-byte blob cap set in the config: listed, content not stored.
	writeCorpusFile(t, filepath.Join(alpha, "big.txt"), strings.Repeat("x", 9000)+"\n")
	writeCorpusBytes(t, filepath.Join(alpha, "assets/dot.png"),
		[]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52})
	commitAll(t, alpha, "initial commit", "2024-01-01T00:00:00Z", "")
	gitAt(t, alpha, "2024-01-15T00:00:00Z", "checkout", "-q", "-b", "feature/extra")
	writeCorpusFile(t, filepath.Join(alpha, "src/extra.ts"), "export const extra = true;\n")
	commitAll(t, alpha, "add extra feature file", "2024-01-15T00:00:00Z",
		"Bo Maintainer <bo@example.com>")
	gitAt(t, alpha, "2024-01-15T00:00:00Z", "checkout", "-q", "main")
	writeCorpusFile(t, filepath.Join(alpha, "src/index.ts"),
		strings.Repeat("export const answer: number = 43;\n", 20))
	commitAll(t, alpha, "bump the answer", "2024-02-01T00:00:00Z", "")
	gitAt(t, alpha, "2024-02-01T00:00:00Z", "tag", "-a", "v1.0.0", "-m", "First release")
	gitAt(t, alpha, "2024-03-01T00:00:00Z", "tag", "-a", "--cleanup=verbatim", "v1.1.0",
		"-m", "Second release\n\n## Highlights\n\n- adds a *guide*\n- new `extra` module\n")
	gitAt(t, alpha, "2024-03-02T00:00:00Z", "tag", "light")
	// gh-pages: a tiny built site, served by the hosting block below.
	gitAt(t, alpha, "2024-03-05T00:00:00Z", "checkout", "-q", "-b", "gh-pages")
	gitAt(t, alpha, "2024-03-05T00:00:00Z", "rm", "-r", "-q", "src", "docs", "assets",
		"README.md", ".frznforge.json", "LICENSE", "crlf.txt", "big.txt")
	writeCorpusFile(t, filepath.Join(alpha, "index.html"),
		`<!doctype html><meta charset="utf-8"><title>alpha site</title><h1>built by alpha</h1>`)
	writeCorpusFile(t, filepath.Join(alpha, "style.css"), "h1 { color: rebeccapurple; }\n")
	commitAll(t, alpha, "publish the site", "2024-03-05T00:00:00Z", "")
	gitAt(t, alpha, "2024-03-05T00:00:00Z", "checkout", "-q", "main")
	// Uncommitted noise: ingest reads git, never the working tree, so none of this may appear.
	writeCorpusFile(t, filepath.Join(alpha, "UNTRACKED-SECRET.txt"), "should never be published")
	writeCorpusFile(t, filepath.Join(alpha, "README.md"), "# MODIFIED BUT NOT COMMITTED\n")

	// bravo — the only fixture with a LONG history, so the insights series has to thin its
	// checkpoints (samples: 6 below is under the active-month count) and the age cap has
	// something to cut. Two authors, months deliberately skipped so quiet months are filled.
	bravo := initRepo(t, repos, "bravo")
	writeCorpusFile(t, filepath.Join(bravo, "README.md"), "# Bravo template\n\nClone me.\n")
	writeCorpusFile(t, filepath.Join(bravo, ".frznforge.json"),
		`{"description":"Bravo fixture: a Go CLI template.","tags":["cli","go"],"template":true}`+"\n")
	writeCorpusFile(t, filepath.Join(bravo, "main.go"), strings.Repeat("package main\n\nfunc main() {}\n", 10))
	commitAll(t, bravo, "scaffold", "2023-06-01T00:00:00Z", "")
	months := []struct{ year, month, count int }{
		{2023, 7, 1}, {2023, 8, 3}, {2023, 9, 2}, {2023, 10, 4},
		{2024, 1, 2}, {2024, 2, 5}, {2024, 3, 1},
		{2024, 5, 3}, {2024, 6, 2}, {2024, 7, 4}, {2024, 8, 1},
	}
	body := strings.Repeat("package main\n\nfunc main() {}\n", 10)
	n := 0
	for _, m := range months {
		for k := 0; k < m.count; k++ {
			n++
			date := fmt.Sprintf("%04d-%02d-%02dT09:0%d:00Z", m.year, m.month, 2+k*4, k)
			body += fmt.Sprintf("\nfunc step%d() int { return %d }\n", n, n)
			writeCorpusFile(t, filepath.Join(bravo, "main.go"), body)
			author := ""
			if k == 1 {
				author = "Bo Maintainer <bo@example.com>"
			}
			commitAll(t, bravo, fmt.Sprintf("step %d", n), date, author)
		}
	}

	// empty — initialised, never committed. It also claims an organization nobody configured,
	// which is the repo-unknown-org warning.
	initRepo(t, repos, "empty")

	// dup — a second repo configured under alpha's slug, so the collision path runs: the loser
	// is renamed to alpha-2, its own warnings are restamped, and its archive moves with it.
	dup := initRepo(t, repos, "dup")
	writeCorpusFile(t, filepath.Join(dup, "main.py"), "print('dup')\n")
	commitAll(t, dup, "the colliding repo", "2024-04-01T00:00:00Z", "")
	gitAt(t, dup, "2024-04-01T00:00:00Z", "tag", "-a", "v0.1.0", "-m", "Only release")

	// charlie — a GitHub-hosted repo whose releases come from the provider API.
	charlie := initRepo(t, origins, "charlie")
	writeCorpusFile(t, filepath.Join(charlie, "README.md"), "# Charlie\n\nMirrored from a provider.\n")
	writeCorpusFile(t, filepath.Join(charlie, "src/main.ts"), strings.Repeat("export const charlie = true;\n", 12))
	commitAll(t, charlie, "import charlie", "2024-03-10T00:00:00Z", "")
	gitAt(t, charlie, "2024-04-01T12:00:00Z", "tag", "-a", "v2.1.0", "-m", "Sunrise")

	// delta — a provider repo that has published nothing and has no annotated tags, so releases
	// cannot fall back to git either: the provider-flavoured empty state.
	delta := initRepo(t, origins, "delta")
	writeCorpusFile(t, filepath.Join(delta, "README.md"), "# Delta\n\nNothing released yet.\n")
	writeCorpusFile(t, filepath.Join(delta, "app.ts"), strings.Repeat("export const delta = 0;\n", 8))
	commitAll(t, delta, "first push", "2024-03-20T00:00:00Z", "")

	// Notes: a plain folder, not a git repo. Two entries slugify onto one value, which is the
	// note-slug-collision warning; the folder notes cover both metadata sources.
	notes := filepath.Join(root, "content", "notes")
	writeCorpusFile(t, filepath.Join(notes, "first-note.md"),
		"---\ntitle: First note\ndate: 2024-02-02\ntags: [fixture, notes]\nsummary: A note with full frontmatter.\n---\n\n# First note\n\nBody text.\n")
	writeCorpusFile(t, filepath.Join(notes, "Hello World.md"), "# Hello World\n\nNo frontmatter here.\n")
	writeCorpusFile(t, filepath.Join(notes, "hello-world.md"), "# Hello world again\n\nThe collision.\n")
	writeCorpusFile(t, filepath.Join(notes, "bundle", "index.md"),
		"---\ntitle: Bundled note\ndate: 2024-03-03\n---\n\nA folder note with a sidecar.\n")
	writeCorpusFile(t, filepath.Join(notes, "bundle", "data.csv"), "a,b\n1,2\n")
	writeCorpusFile(t, filepath.Join(notes, "readme-note", "README.md"), "# Readme note\n\nMetadata from README.\n")

	cfg := map[string]any{
		"site":  map[string]any{"title": "Identity fixture"},
		"owner": map[string]any{"name": "Kieran Wood", "handle": "kieran"},
		"repos": []any{
			map[string]any{"type": "local", "path": "./repos/alpha", "org": "canadian-coding"},
			map[string]any{"type": "local", "path": "./repos/bravo"},
			map[string]any{"type": "local", "path": "./repos/empty", "org": "nope"},
			// Same slug as alpha, later in config order, so it is the one renamed. `releases`
			// on a LOCAL source is deliberately ignored by both engines — the scanner's own
			// default wins — so this pins that a port does not start honouring it on one side.
			map[string]any{"type": "local", "path": "./repos/dup", "slug": "alpha", "releases": "provider"},
			// Not a repository at all: a warning, never a failure.
			map[string]any{"type": "local", "path": "./repos/missing"},
			map[string]any{"type": "github", "owner": "fixture", "repo": "charlie"},
			map[string]any{"type": "gitea", "host": "https://gitea.example.com", "owner": "fixture", "repo": "delta"},
		},
		"organizations": []any{
			map[string]any{
				"slug": "canadian-coding", "name": "Canadian Coding",
				"description": "Small, sturdy, source-available tools.",
				// bravo is claimed from this side; alpha claims the org from the other side; the
				// third name matches nothing, which is the org-unknown-repo warning.
				"repos":  []any{"bravo", "not-a-repo"},
				"avatar": "logo.png",
			},
		},
		"contributors": []any{
			map[string]any{
				"name": "Fixture Author (configured)", "emails": []any{"test@example.com", "also@example.invalid"},
				"avatar": "logo.png", "description": "the fixture commit author", "url": "https://example.com/fixture",
			},
			// Matches nobody: the contributor-unknown-email warning.
			map[string]any{"name": "Nobody At All", "emails": []any{"nobody@example.invalid"}},
		},
		"hosting": map[string]any{"sites": []any{
			map[string]any{"repo": "alpha", "slug": "alpha-site"},
			// Names a repo that is not ingested: the hosting-unknown-repo warning.
			map[string]any{"repo": "ghost", "slug": "ghost-site"},
		}},
		"notes": map[string]any{"dir": "./content/notes"},
		"ingest": map[string]any{
			// Small enough that alpha's big.txt is listed but not stored.
			"maxBlobBytes": 4096,
			// bravo spans 428 days, so this cuts its oldest commits: commits-aged-out.
			"maxCommitAgeDays": 400,
			// Under the tag and branch counts above, so both caps report.
			"tagTrees":    2,
			"branchTrees": 1,
			"concurrency": 3,
			// Under bravo's active-month count, so insights thin their checkpoints.
			"insights": map[string]any{"samples": 6},
		},
	}
	configPath := filepath.Join(root, "config.json")
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeCorpusBytes(t, configPath, append(raw, '\n'))

	return corpus{
		root:       root,
		configPath: configPath,
		remotes: map[string]remoteFixture{
			"fixture/charlie": {
				Origin: charlie,
				Meta: ImportedRepoMeta{
					Name: strPtr("charlie"), Description: strPtr("Charlie fixture: metadata imported from a provider API."),
					Homepage: strPtr("https://example.com/charlie"), Topics: []string{"imported", "provider"},
					License: strPtr("Apache-2.0"), DefaultBranch: strPtr("main"),
					WebURL: "https://github.com/fixture/charlie", CloneURL: "https://github.com/fixture/charlie.git",
					IssuesURL: strPtr("https://github.com/fixture/charlie/issues"),
				},
				Releases: []model.Release{{
					Tag: "v2.1.0", Name: "Sunrise",
					Body:        "## What's new\n\n- imported over the provider API\n- ships an uploaded asset\n",
					URL:         strPtr("https://github.com/fixture/charlie/releases/tag/v2.1.0"),
					PublishedAt: "2024-04-01T12:00:00Z", Author: strPtr("Fixture Releaser"),
					Assets: []model.ReleaseAsset{{
						Name: "charlie-2.1.0-linux-x64.tar.gz",
						URL:  "https://github.com/fixture/charlie/releases/download/v2.1.0/charlie-2.1.0-linux-x64.tar.gz",
						Size: 1048576, ContentType: strPtr("application/gzip"),
					}},
				}},
			},
			"fixture/delta": {
				Origin: delta,
				Meta: ImportedRepoMeta{
					Name: strPtr("delta"), Description: strPtr("Delta fixture: a Gitea repo with no releases."),
					Topics: []string{"imported"}, DefaultBranch: strPtr("main"),
					WebURL: "https://gitea.example.com/fixture/delta", CloneURL: "https://gitea.example.com/fixture/delta.git",
				},
				Releases: []model.Release{},
			},
		},
	}
}

/* ---- the tests ------------------------------------------------------------ */

// TestIngestIsDeterministic pins the property the caches are allowed to change nothing about:
// a second run over the same corpus — this time replaying the scan cache and the provider cache
// the first run wrote — must produce the same artifact.
func TestIngestIsDeterministic(t *testing.T) {
	// No requireTsx here any more, and that is the point. This test never used tsx — it took the
	// module root from the helper and threw it away — but the helper skipped when tsx was absent,
	// so a determinism gate quietly stopped running the moment Phase 9 removed the dependency.
	c := identityCorpus(t)

	cache := filepath.Join(c.root, "cache-repeat")
	first := filepath.Join(c.root, "out-first")
	second := filepath.Join(c.root, "out-second")
	runGoIngest(t, c, first, cache)
	// Same output directory contents matter to the replay (rehydration reads blob bytes back
	// from outDir), so seed the second run's directory from the first before re-ingesting.
	copyTree(t, first, second)
	runGoIngest(t, c, second, cache)
	compareArtifacts(t, first, second)
}

/* ---- running the two engines ---------------------------------------------- */

// runGoIngest runs the Go pipeline over the corpus with the same two seams the TypeScript
// driver replaces: canned importer answers, and the production mirror driver pointed at a local
// origin directory. Everything else is production code.
func runGoIngest(t *testing.T, c corpus, outDir, cacheDir string) {
	t.Helper()
	runGoIngestWith(t, c, outDir, cacheDir, Options{})
}

// runGoIngestWith is the same, plus the per-run flags.
func runGoIngestWith(t *testing.T, c corpus, outDir, cacheDir string, options Options) {
	t.Helper()
	deps := PrepareRemoteDeps{
		// Env{} — not nil — so no token is ever resolved from the developer's environment.
		Env: Env{},
		CreateImporter: func(src config.RepoSourceConfig, _ ImporterContext) Importer {
			fixture, ok := c.remotes[remoteKeyFor(src)]
			if !ok {
				return nil
			}
			return fixtureImporter{provider: src.Type, fixture: fixture}
		},
		EnsureMirror: func(ctx context.Context, src config.RepoSourceConfig, cachePath string, opts EnsureMirrorOptions) EnsureMirrorResult {
			fixture, ok := c.remotes[remoteKeyFor(src)]
			if !ok {
				return EnsureMirrorResult{Path: cachePath, Action: MirrorMissing,
					Error: fmt.Errorf("no remote fixture registered for %s", remoteKeyFor(src))}
			}
			// Forward slashes: git accepts them on Windows and they keep the arg quoting simple.
			opts.CloneURL = filepath.ToSlash(fixture.Origin)
			return EnsureMirror(ctx, src, cachePath, opts)
		},
	}
	options.Remote = deps
	runGoIngestAt(t, c.root, c.configPath, outDir, cacheDir, options)
}

// runGoIngestAt is the Go engine over one config file. outDir and cacheDir are overridden on the
// PARSED config, before resolution — a remote source's mirror path is derived from the resolved
// cacheDir, so setting it afterwards would leave every mirror in the wrong place. The TypeScript
// driver overrides them in exactly the same place.
func runGoIngestAt(t *testing.T, root, configPath, outDir, cacheDir string, options Options) {
	t.Helper()
	if configPath == root {
		configPath = filepath.Join(root, config.Filename)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.ParseBytes(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", configPath, err)
	}
	cfg.Ingest.OutDir = outDir
	cfg.Ingest.CacheDir = cacheDir
	resolved, err := config.Resolve(cfg, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsureOutDir(resolved.OutDir); err != nil {
		t.Fatal(err)
	}
	res, err := Ingest(context.Background(), resolved, Hooks{}, options)
	if err != nil {
		t.Fatalf("go ingest: %v", err)
	}
	if err := WriteArtifact(res.Data, res.Blobs, res.Archives, resolved.OutDir); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
}

// fixtureImporter hands back the fixture's canned answers, standing in for a REST API.
type fixtureImporter struct {
	provider string
	fixture  remoteFixture
}

func (s fixtureImporter) Provider() string { return s.provider }

func (s fixtureImporter) FetchMeta(context.Context) (ImportedRepoMeta, error) {
	return s.fixture.Meta, nil
}

func (s fixtureImporter) FetchReleases(context.Context) (ImportedReleases, error) {
	return ImportedReleases{Releases: s.fixture.Releases}, nil
}

/* ---- comparison ----------------------------------------------------------- */

// compareArtifacts is the whole assertion: the artifact byte for byte, then the blob store, then
// the archive store. All three are compared rather than just forge.json, because a repo whose
// bytes were written under a different name renders as a 404 with a perfectly valid artifact.
func compareArtifacts(t *testing.T, wantDir, gotDir string) {
	t.Helper()
	want, err := os.ReadFile(filepath.Join(wantDir, ArtifactFilename))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(gotDir, ArtifactFilename))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("forge.json differs (%d bytes vs %d)\n%s",
			len(want), len(got), firstDifference(string(want), string(got)))
	}
	compareTree(t, filepath.Join(wantDir, BlobDirname), filepath.Join(gotDir, BlobDirname), "blobs")
	compareTree(t, filepath.Join(wantDir, ArchiveDirname), filepath.Join(gotDir, ArchiveDirname), "archives")
}

// compareTree asserts two directories hold the same relative paths with the same bytes.
func compareTree(t *testing.T, wantDir, gotDir, label string) {
	t.Helper()
	want := readTree(t, wantDir)
	got := readTree(t, gotDir)
	wantNames, gotNames := sortedNames(want), sortedNames(got)
	if strings.Join(wantNames, "\n") != strings.Join(gotNames, "\n") {
		t.Errorf("%s: different file sets\n  only in the TypeScript output: %v\n  only in the Go output: %v",
			label, missing(want, got), missing(got, want))
		return
	}
	if len(wantNames) == 0 {
		t.Errorf("%s: both outputs are empty, so this comparison proves nothing", label)
		return
	}
	for _, name := range wantNames {
		if !bytes.Equal(want[name], got[name]) {
			t.Errorf("%s/%s differs: %d bytes in the TypeScript output, %d in the Go output",
				label, name, len(want[name]), len(got[name]))
		}
	}
}

func readTree(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("read %s: %v", dir, err)
	}
	return out
}

func sortedNames(m map[string][]byte) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

func missing(a, b map[string][]byte) []string {
	out := []string{}
	for _, name := range sortedNames(a) {
		if _, ok := b[name]; !ok {
			out = append(out, name)
		}
	}
	return out
}

func artifactSlugs(data model.ForgeData) string {
	slugs := make([]string, len(data.Repos))
	for i, r := range data.Repos {
		slugs[i] = r.Slug
	}
	return strings.Join(slugs, ", ")
}

/* ---- fixture plumbing ------------------------------------------------------ */

// tempCorpusDir is a temp directory removed with the read-only bit cleared first: git writes
// loose objects read-only, and t.TempDir's plain RemoveAll refuses to delete those on Windows.
func tempCorpusDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "frznforge-identity-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = filepath.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
			if err == nil {
				_ = os.Chmod(path, 0o700)
			}
			return nil
		})
		_ = os.RemoveAll(dir)
	})
	return dir
}

func initRepo(t *testing.T, base, name string) string {
	t.Helper()
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitAt(t, dir, "2024-01-01T00:00:00Z", "init", "-q", "-b", "main")
	gitAt(t, dir, "2024-01-01T00:00:00Z", "config", "user.name", "Fixture Author")
	gitAt(t, dir, "2024-01-01T00:00:00Z", "config", "user.email", "test@example.com")
	gitAt(t, dir, "2024-01-01T00:00:00Z", "config", "commit.gpgsign", "false")
	gitAt(t, dir, "2024-01-01T00:00:00Z", "config", "tag.gpgsign", "false")
	// Without this a Windows checkout rewrites line endings on the way into the index, and the
	// fixture's blobs — and so its shas — stop matching the ones built elsewhere.
	gitAt(t, dir, "2024-01-01T00:00:00Z", "config", "core.autocrlf", "false")
	return dir
}

func commitAll(t *testing.T, dir, message, date, author string) {
	t.Helper()
	gitAt(t, dir, date, "add", "-A")
	args := []string{"commit", "-q", "-m", message}
	if author != "" {
		args = append(args, "--author="+author)
	}
	gitAt(t, dir, date, args...)
}

func gitAt(t *testing.T, dir, date string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = envWith(map[string]string{
		"GIT_AUTHOR_NAME":     "Fixture Author",
		"GIT_AUTHOR_EMAIL":    "test@example.com",
		"GIT_COMMITTER_NAME":  "Fixture Author",
		"GIT_COMMITTER_EMAIL": "test@example.com",
		"GIT_AUTHOR_DATE":     date,
		"GIT_COMMITTER_DATE":  date,
	})
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

// envWith is the process environment plus overrides, with any earlier entry for an overridden
// name removed first: Windows environment blocks are case-insensitive, and a duplicate name is
// exactly the kind of thing that makes one engine read a different directory than the other.
func envWith(overrides map[string]string) []string {
	drop := map[string]bool{
		"FRZNFORGE_OUT_DIR": true, "FRZNFORGE_CACHE_DIR": true, "FRZNFORGE_BASE": true,
	}
	for k := range overrides {
		drop[strings.ToUpper(k)] = true
	}
	out := []string{}
	for _, kv := range os.Environ() {
		name, _, ok := strings.Cut(kv, "=")
		if ok && drop[strings.ToUpper(name)] {
			continue
		}
		out = append(out, kv)
	}
	for k, v := range overrides {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

func writeCorpusFile(t *testing.T, path, content string) {
	t.Helper()
	writeCorpusBytes(t, path, []byte(content))
}

func writeCorpusBytes(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copy %s to %s: %v", src, dst, err)
	}
}

// TestIngestCancelledRunIsAnError pins the counterpart of the ordering rule: a run that stops
// early must FAIL, not publish. A cancelled pool leaves the tail of its results slice at the
// zero value, which reads as "skipped with no warning" — an artifact quietly missing repos, and
// a build that reports success while replacing a good site with a smaller one.
func TestIngestCancelledRunIsAnError(t *testing.T) {
	dir := tempCorpusDir(t)
	empty := filepath.Join(dir, "gitconfig-empty")
	writeCorpusFile(t, empty, "")
	t.Setenv("GIT_CONFIG_GLOBAL", empty)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	repo := initRepo(t, dir, "solo")
	writeCorpusFile(t, filepath.Join(repo, "README.md"), "# Solo\n")
	commitAll(t, repo, "only commit", "2024-01-01T00:00:00Z", "")

	writeCorpusFile(t, filepath.Join(dir, "config.json"), `{
  "site": {"title": "Cancelled"},
  "owner": {"name": "Tester", "handle": "tester"},
  "repos": [{"type": "local", "path": "./solo"}]
}`)
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.ParseBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Ingest.OutDir = filepath.Join(dir, "out")
	cfg.Ingest.CacheDir = filepath.Join(dir, "cache")
	resolved, err := config.Resolve(cfg, dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Ingest(ctx, resolved, Hooks{}, Options{}); err == nil {
		t.Fatal("a cancelled ingest returned no error; it would have written an artifact missing every unscanned repo")
	}
}

// firstDifference reports the first differing line of two documents with a little context.
//
// Kept when the parity tests went: it was written to explain a Go-versus-TypeScript diff, and it
// now explains a run-versus-run one. The labels are deliberately not "TS"/"GO" any more — both
// sides of every comparison left in this package are the same engine, twice.
func firstDifference(want, got string) string {
	wantLines := strings.Split(want, "\n")
	gotLines := strings.Split(got, "\n")
	for i := 0; i < len(wantLines) || i < len(gotLines); i++ {
		w, g := "", ""
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if w == g {
			continue
		}
		var b strings.Builder
		for j := max(0, i-6); j < i; j++ {
			b.WriteString("    " + wantLines[j] + "\n")
		}
		b.WriteString("1st " + w + "\n")
		b.WriteString("2nd " + g + "\n")
		return b.String()
	}
	return "(no line differs; the documents differ only in trailing bytes)"
}

// encodeLikeArtifact serialises a Repo exactly as model.Serialize serialises the artifact around
// it, so a difference here is a difference in the file that ships.
func encodeLikeArtifact(t *testing.T, repo *model.Repo) string {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(repo); err != nil {
		t.Fatalf("encode repo: %v", err)
	}
	return buf.String()
}

// projectRoot is the module root, for a test that needs a real file from the repository.
func projectRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// mitLicense is a real licence body, kept from the deleted parity tests: the detector matches
// on text, so a paraphrase would not exercise it.
const mitLicense = `MIT License

Copyright (c) 2024 Test User

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction.
`
