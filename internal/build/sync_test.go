package build_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"frznforge/internal/build"
	"frznforge/internal/config"
	"frznforge/internal/ingest"
	"frznforge/internal/ingest/testsupport"
	"frznforge/internal/model"
	"frznforge/internal/routes"
)

// Artifact ↔ site sync, on a project this test builds itself — the port of
// tests/unit/site-sync.test.ts, tests/unit/phase3-sync.test.ts and tests/unit/phase6-sync.test.ts.
//
// The Go suite already had TestBuildEmitsEveryRoute, which makes the same claim and makes it
// well. What it does not have is the ability to make it anywhere: it reads the DEVELOPER'S
// data/forge.json and calls t.Skip when there is not one. Checkpoint D's acceptance bar is a
// clean clone with no Node and no artifact, and on that machine — and on any CI runner — the
// build's strongest structural test prints "ok" while asserting nothing at all. A test that
// skips is indistinguishable from a test that passes in every summary anyone reads.
//
// So this one owns its inputs end to end: fixture git repositories, a config, the real Go
// ingest, the real build. It runs the whole pipeline the user runs, and it cannot skip.
//
// The three TypeScript files it replaces made three claims, and all three are below:
//   - every repo in the artifact gets a page and appears in the listing (site-sync);
//   - trees, refs, tags and archives map 1:1 onto emitted routes (phase3-sync);
//   - notes and organizations are routable AND searchable (phase6-sync).

// syncFixture builds a project directory holding a config, three git repositories, a notes
// folder and an organization, ingests it, and returns the project root and the artifact.
//
// Deliberately not a table: these repositories are shaped to exercise the multiplier families
// (a branch and a tag beyond the default, so refTrees is non-empty and archives exist) and an
// empty repository, which is the shape that has historically produced routes nothing emits.
func syncFixture(t *testing.T) (root string, data model.ForgeData) {
	t.Helper()

	root = t.TempDir()
	alpha := testsupport.Create(t, "alpha", "main")
	alpha.WriteAndCommit(map[string]string{
		"README.md":  "# Alpha\n\nThe first repository.\n",
		"src/app.ts": "export const v = 1;\n",
		"LICENSE":    "MIT License\n",
	}, "init", testsupport.CommitOptions{})
	alpha.Branch("feat/zip", "main")
	alpha.Checkout("feat/zip", false)
	alpha.WriteAndCommit(map[string]string{"src/b.ts": "export const b = 2;\n"}, "feature work", testsupport.CommitOptions{})
	alpha.Checkout("main", false)
	alpha.Tag("v1.0.0", testsupport.TagOptions{})

	// A second repository so cross-repo ordering is exercised; a slash in a path so the route
	// encoder is too.
	beta := testsupport.Create(t, "beta", "main")
	beta.WriteAndCommit(map[string]string{
		"README.md":            "# Beta\n",
		"docs/guide/intro.md":  "# Intro\n",
		"docs/guide/deep.json": "{}\n",
	}, "init", testsupport.CommitOptions{})

	// An empty repository. It has no tree, no files and no commits, and it is the shape that
	// most often produces a listed route with nothing behind it.
	empty := testsupport.Create(t, "empty", "main")

	notesDir := filepath.Join(root, "notes")
	if err := os.MkdirAll(notesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(notesDir, "a-note.md"), "---\ntitle: A Note\ndate: 2026-08-01\n---\n\nBody of the note.\n")

	orgsDir := filepath.Join(root, "content", "orgs")
	if err := os.MkdirAll(orgsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(orgsDir, "acme.md"), "---\nname: Acme\n---\n\nAbout Acme.\n")

	write(t, filepath.Join(root, "content", "profile.md"), "---\ntitle: Owner\n---\n\nHello.\n")

	// public/ (the owner's files) and web/ (the engine's browser half) are copied into the
	// output verbatim, and they are copied from the PROJECT, not from an embedded copy inside
	// the binary — a site that does not carry web/ builds pages with no stylesheet and no
	// command palette. A fixture without them leaves that whole path untested, and its absence
	// is invisible: the pages still render.
	//
	// Deliberately awkward bytes. The rule is "no transform, no rename, no content hash", and
	// a file of plain ASCII would pass a build that re-encoded or normalised what it copied.
	write(t, filepath.Join(root, "public", "robots.txt"), "User-agent: *\r\nDisallow:\r\n")
	write(t, filepath.Join(root, "web", "css", "global.css"), ":root { --hf-ice: #7fd; } /* … ünïcode */\n")

	cfg := map[string]any{
		"site":  map[string]any{"title": "Sync Fixture", "url": "https://example.com"},
		"owner": map[string]any{"name": "Owner", "handle": "owner"},
		"repos": []any{
			map[string]any{"type": "local", "path": alpha.Dir, "slug": "alpha", "org": "acme"},
			map[string]any{"type": "local", "path": beta.Dir, "slug": "beta"},
			map[string]any{"type": "local", "path": empty.Dir, "slug": "empty"},
		},
		"organizations": []any{map[string]any{"slug": "acme", "name": "Acme"}},
		"notes":         map[string]any{"dir": "notes"},
		// Archives are off by default and are one of the four route families, so this fixture
		// turns them on rather than leaving a family untested.
		"ingest": map[string]any{"archives": true, "outDir": "data"},
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, config.Filename), string(raw))

	resolved, err := config.Load(root)
	if err != nil {
		t.Fatalf("load the config this test just wrote: %v", err)
	}
	opts, err := ingest.ScanOptionsFromConfig(resolved)
	if err != nil {
		t.Fatal(err)
	}

	// Config order, not scan order: Assemble's slug-collision rule depends on it.
	scanned := make([]ingest.ScannedRepo, 0, len(resolved.Sources))
	for _, src := range resolved.Sources {
		res, err := ingest.ScanRepo(context.Background(), ingest.ScanSource{
			AbsPath: src.AbsPath, Slug: src.Slug, Overrides: src.Overrides,
		}, opts)
		if err != nil {
			t.Fatalf("scan %s: %v", src.Slug, err)
		}
		scanned = append(scanned, ingest.ScannedRepo{Result: res, Org: src.Org})
	}

	assembled, err := ingest.Assemble(resolved, scanned)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if err := ingest.WriteArtifact(assembled.Data, assembled.Blobs, assembled.Archives, resolved.OutDir); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	return root, assembled.Data
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSyncOnASelfBuiltProject is the whole claim in one build.
func TestSyncOnASelfBuiltProject(t *testing.T) {
	root, data := syncFixture(t)

	// The fixture has to be worth testing against. A scan that silently produced three empty
	// repos would satisfy every assertion below without exercising anything.
	if len(data.Repos) != 3 {
		t.Fatalf("the fixture ingested %d repos, want 3: %v", len(data.Repos), repoSlugsOf(data))
	}
	if len(data.Notes) != 1 {
		t.Fatalf("the fixture ingested %d notes, want 1", len(data.Notes))
	}
	if len(data.Organizations) != 1 {
		t.Fatalf("the fixture ingested %d orgs, want 1", len(data.Organizations))
	}
	var withRefTrees, withArchives int
	for i := range data.Repos {
		if len(data.Repos[i].RefTrees.Keys()) > 0 {
			withRefTrees++
		}
		if len(data.Repos[i].Archives) > 0 {
			withArchives++
		}
	}
	if withRefTrees == 0 {
		t.Fatal("no repo has a refTree — the multiplier families are untested by this fixture")
	}
	if withArchives == 0 {
		t.Fatal("no repo has an archive — the archive family is untested by this fixture")
	}

	out := filepath.Join(t.TempDir(), "dist")
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	if _, err := build.Run(build.Options{Root: root, OutDir: out, Now: now}); err != nil {
		t.Fatalf("build: %v", err)
	}

	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	router := routes.Router{Base: cfg.Site.Base}

	predicted := map[string]string{} // on-disk relative path -> the route that predicted it
	for _, route := range router.AllRoutes(&data) {
		predicted[decoded(t, filePathFor(router.Base, route))] = route
	}
	predicted[decoded(t, build.RawPath(router.Base, router.SearchIndexURL()))] = router.SearchIndexURL()
	// public/ and web/ are copied verbatim; they are not routes and the router never lists them.
	for _, dir := range []string{"public", "web"} {
		for _, rel := range treeFiles(t, filepath.Join(root, dir)) {
			predicted[rel] = dir + "/ (copied verbatim)"
		}
	}

	emitted := map[string]bool{}
	for _, p := range treeFiles(t, out) {
		emitted[p] = true
	}

	t.Run("every predicted route is emitted", func(t *testing.T) {
		// A family whose loop never runs writes zero files and returns nil; the build prints
		// "built N files" and exits 0. This is the direction that catches it.
		var missing []string
		for p, route := range predicted {
			if !emitted[p] {
				missing = append(missing, route)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			t.Errorf("%d predicted routes were not emitted, e.g.\n  %s", len(missing), strings.Join(head(missing, 10), "\n  "))
		}
	})

	t.Run("every emitted file was predicted", func(t *testing.T) {
		// The reverse: a page nothing links to and the search index does not know about. It is
		// how a stale route survives a rename.
		var stray []string
		for p := range emitted {
			if _, ok := predicted[p]; !ok {
				stray = append(stray, p)
			}
		}
		sort.Strings(stray)
		if len(stray) > 0 {
			t.Errorf("%d emitted files match no route, e.g.\n  %s", len(stray), strings.Join(head(stray, 10), "\n  "))
		}
	})

	t.Run("public and web are copied byte for byte", func(t *testing.T) {
		// "Verbatim" is a promise the build makes to the browser: the bytes on disk are the
		// bytes served. A copier that rewrote line endings, re-encoded UTF-8 or dropped a
		// comment would still produce a working site and would still have broken the promise.
		for _, dir := range []string{"public", "web"} {
			for _, rel := range treeFiles(t, filepath.Join(root, dir)) {
				src, err := os.ReadFile(filepath.Join(root, dir, filepath.FromSlash(rel)))
				if err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(rel)))
				if err != nil {
					t.Errorf("%s was not copied out of %s/: %v", rel, dir, err)
					continue
				}
				if !bytes.Equal(src, got) {
					t.Errorf("%s changed on the way out of %s/\n  on disk: %q\n  emitted: %q", rel, dir, src, got)
				}
			}
		}
	})

	t.Run("every repo gets a page, the empty one included", func(t *testing.T) {
		// site-sync.test.ts's first claim. The empty repository is the interesting member: it
		// has no tree to render, and "no tree" has more than once meant "no page".
		for i := range data.Repos {
			slug := data.Repos[i].Slug
			p := decoded(t, filePathFor(router.Base, router.RepoURL(slug)))
			if !emitted[p] {
				t.Errorf("repo %q has no page at %s", slug, p)
			}
		}
	})

	t.Run("notes and organizations are routable", func(t *testing.T) {
		// phase6-sync.test.ts. These two families arrived in schema v4 and are the ones a
		// route-family port is most likely to forget, because a site with no notes looks fine.
		for _, note := range data.Notes {
			p := decoded(t, filePathFor(router.Base, router.NoteURL(note.Slug)))
			if !emitted[p] {
				t.Errorf("note %q has no page at %s", note.Slug, p)
			}
		}
		for _, org := range data.Organizations {
			p := decoded(t, filePathFor(router.Base, router.OrgURL(org.Slug)))
			if !emitted[p] {
				t.Errorf("org %q has no page at %s", org.Slug, p)
			}
		}
	})

	t.Run("notes and organizations are searchable", func(t *testing.T) {
		// The other half of phase6-sync's claim, and the half that fails quietly: a note with a
		// page but no index entry is unreachable from the command palette, which is how most
		// people navigate this site.
		raw, err := os.ReadFile(filepath.Join(out, decoded(t, build.RawPath(router.Base, router.SearchIndexURL()))))
		if err != nil {
			t.Fatalf("read the search index: %v", err)
		}
		var index struct {
			Docs []struct {
				Kind string `json:"kind"`
				URL  string `json:"url"`
			} `json:"docs"`
		}
		if err := json.Unmarshal(raw, &index); err != nil {
			t.Fatal(err)
		}
		indexed := map[string]bool{}
		for _, d := range index.Docs {
			indexed[d.URL] = true
		}
		for _, note := range data.Notes {
			if !indexed[router.NoteURL(note.Slug)] {
				t.Errorf("note %q has a page but no search entry", note.Slug)
			}
		}
		for _, org := range data.Organizations {
			if !indexed[router.OrgURL(org.Slug)] {
				t.Errorf("org %q has a page but no search entry", org.Slug)
			}
		}
		for i := range data.Repos {
			if !indexed[router.RepoURL(data.Repos[i].Slug)] {
				t.Errorf("repo %q has a page but no search entry", data.Repos[i].Slug)
			}
		}
	})

	t.Run("every search result resolves to a file", func(t *testing.T) {
		// The bug this pins shipped: the palette offered file results whose paths hold `#` or
		// `%`, which get no page, so choosing one 404ed. Asserting the index against the
		// emitted tree is the only check that would have caught it.
		raw, err := os.ReadFile(filepath.Join(out, decoded(t, build.RawPath(router.Base, router.SearchIndexURL()))))
		if err != nil {
			t.Fatal(err)
		}
		var index struct {
			Docs []struct {
				URL string `json:"url"`
			} `json:"docs"`
		}
		if err := json.Unmarshal(raw, &index); err != nil {
			t.Fatal(err)
		}
		if len(index.Docs) == 0 {
			t.Fatal("the search index is empty")
		}
		var dead []string
		for _, d := range index.Docs {
			if !emitted[decoded(t, filePathFor(router.Base, d.URL))] {
				dead = append(dead, d.URL)
			}
		}
		sort.Strings(dead)
		if len(dead) > 0 {
			t.Errorf("%d search results point at nothing, e.g.\n  %s", len(dead), strings.Join(head(dead, 10), "\n  "))
		}
	})
}

func repoSlugsOf(data model.ForgeData) []string {
	out := make([]string, 0, len(data.Repos))
	for i := range data.Repos {
		out = append(out, data.Repos[i].Slug)
	}
	return out
}

// buildRoot is a project the build can be run against, and a name for failure messages.
type buildRoot struct {
	name string
	root string
}

// buildRoots returns every project worth building in this test binary.
//
// It always includes the self-built fixture, and adds the developer's own repository when there
// is an artifact in it. That ordering is the point: the fixture is what makes these tests run at
// all on a clean clone or a CI runner, and the real corpus — 600-odd pages of genuinely awkward
// content — is what makes them worth running when it happens to be there. Before this existed
// the second was the only one, so on every machine without a `npm run ingest` behind it the
// build's determinism and concurrency gates printed "ok" and checked nothing.
func buildRoots(t *testing.T) []buildRoot {
	t.Helper()
	fixture, _ := syncFixture(t)
	roots := []buildRoot{{"fixture", fixture}}

	// The developer's own corpus is added only when asked for, and that is a change of heart
	// worth explaining. It went in so these gates would run against something bigger and nastier
	// than a fixture, and it did its job — until the owner published their whole account and the
	// corpus went from one repository to 73. Each of these tests builds it TWICE, so
	// `go test ./...` started taking 1,117 seconds and failing on the default ten-minute clock:
	// a green suite that nobody can run is worth less than a smaller one that everybody does.
	//
	// The fixture still runs unconditionally, which is the property that matters — these gates
	// must never quietly assert nothing on a clean clone. The big corpus is now the deeper run
	// you ask for by name:
	//
	//	FRZNFORGE_FULL_CORPUS=1 go test ./internal/build/ -timeout 90m
	if os.Getenv("FRZNFORGE_FULL_CORPUS") == "" {
		t.Log("skipping the local corpus; set FRZNFORGE_FULL_CORPUS=1 to include it (slow)")
		return roots
	}
	if real := repoRoot(t); real != "" {
		if _, err := os.Stat(filepath.Join(real, "data", "forge.json")); err == nil {
			roots = append(roots, buildRoot{"this repository", real})
		}
	}
	return roots
}
