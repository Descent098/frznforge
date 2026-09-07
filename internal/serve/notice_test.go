package serve

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The preflight and the notice, ported from tests/unit/dev-script.test.ts.
//
// The bug both exist to replace: `astro preview` on a project that had never been built failed
// with a sentence about a missing directory and said nothing about what to do, and `frznforge
// dev` over an empty dist/ would answer every request "not found" for the same reason. So what
// is asserted here is not the exit code — it is that the text names the command that fixes it.
// A preflight that fails without instructions is the failure it was written to prevent.

// testPaths is the shape cmd/frznforge/dev.go assembles: a project root, dist/ under it, and
// the artifact wherever ingest.outDir put it.
func testPaths(t *testing.T) Paths {
	t.Helper()
	root := t.TempDir()
	return Paths{
		Root:         root,
		Dir:          filepath.Join(root, "dist"),
		ArtifactFile: filepath.Join(root, "data", "forge.json"),
	}
}

// only answers true for exactly these absolute paths — the Go shape of dev-script.test.ts's
// `only(...)`. Preflight takes exists as a parameter precisely so its messages can be checked
// without building a site to look at.
func only(present ...string) func(string) bool {
	return func(p string) bool { return slices.Contains(present, p) }
}

func TestPreflightPassesWhenBothHalvesAreThere(t *testing.T) {
	p := testPaths(t)
	got := Preflight(p, only(p.ArtifactFile, p.Dir, filepath.Join(p.Dir, "index.html")))
	if !got.OK {
		t.Fatalf("Preflight = %+v, want OK on a built project", got)
	}
	if len(got.Missing) != 0 || len(got.Lines) != 0 {
		t.Errorf("a passing preflight printed %v / %v, want silence", got.Missing, got.Lines)
	}
}

func TestPreflightReportsAMissingDist(t *testing.T) {
	p := testPaths(t)
	got := Preflight(p, only(p.ArtifactFile))
	if got.OK {
		t.Fatal("Preflight passed with no dist/ — the server would then 404 every request without saying why")
	}
	if !slices.Equal(got.Missing, []string{"dist"}) {
		t.Errorf("Missing = %v, want [dist] — project-relative, since that is what a reader pastes into a shell", got.Missing)
	}
	text := strings.Join(got.Lines, "\n")
	for _, want := range []string{"missing: dist", "frznforge build", "frznforge ingest", "Then `frznforge dev` again"} {
		if !strings.Contains(text, want) {
			t.Errorf("the message does not contain %q; it reads:\n%s", want, text)
		}
	}
	// The artifact is present, so naming it would send the reader to re-run an ingest that has
	// already happened.
	if strings.Contains(text, "missing: data/forge.json") {
		t.Errorf("the message blames the artifact, which exists:\n%s", text)
	}
}

func TestPreflightReportsAMissingArtifactEvenWithDistPresent(t *testing.T) {
	p := testPaths(t)
	got := Preflight(p, only(p.Dir, filepath.Join(p.Dir, "index.html")))
	if got.OK {
		t.Fatal("Preflight passed with no artifact")
	}
	if !slices.Equal(got.Missing, []string{"data/forge.json"}) {
		t.Errorf("Missing = %v, want [data/forge.json]", got.Missing)
	}
	if text := strings.Join(got.Lines, "\n"); !strings.Contains(text, "frznforge ingest") {
		t.Errorf("a missing artifact must name the command that produces one:\n%s", text)
	}
}

func TestPreflightListsBothArtifactFirst(t *testing.T) {
	p := testPaths(t)
	got := Preflight(p, only())
	// Order is the whole point of the assertion: the artifact is the input to the build that
	// fills dist/, so fixing it in the other order means running the build twice.
	if !slices.Equal(got.Missing, []string{"data/forge.json", "dist"}) {
		t.Errorf("Missing = %v, want [data/forge.json dist] in that order", got.Missing)
	}
}

func TestPreflightCatchesAnInterruptedBuild(t *testing.T) {
	// New in the Go port: scripts/dev.ts only checked that dist/ existed, so a build killed
	// halfway left a directory that passed the guard and then served nothing but 404s. An
	// index.html is the cheapest proof that a build finished.
	p := testPaths(t)
	got := Preflight(p, only(p.ArtifactFile, p.Dir))
	if got.OK {
		t.Fatal("Preflight passed on a dist/ with no index.html — that is an interrupted build, and it fails as puzzlingly as no dist/ at all")
	}
	if !slices.Equal(got.Missing, []string{"dist/index.html"}) {
		t.Errorf("Missing = %v, want [dist/index.html]", got.Missing)
	}
}

func TestPreflightSkipsTheArtifactForAnExplicitDir(t *testing.T) {
	// `frznforge dev --dir=...` is "serve these files" — the e2e harness pointing at a fixture
	// build, or someone else's output. There is no project artifact behind it, and demanding a
	// data/forge.json that was never meant to exist would make the harness unusable.
	p := testPaths(t)
	p.ArtifactFile = ""
	if got := Preflight(p, only(p.Dir, filepath.Join(p.Dir, "index.html"))); !got.OK {
		t.Fatalf("Preflight = %+v, want OK: a served directory with an index.html is all --dir promises", got)
	}
	got := Preflight(p, only())
	if !slices.Equal(got.Missing, []string{"dist"}) {
		t.Errorf("Missing = %v, want [dist] only", got.Missing)
	}
	if text := strings.Join(got.Lines, "\n"); strings.Contains(text, "forge.json") {
		t.Errorf("the message names an artifact path that was never configured:\n%s", text)
	}
}

func TestPreflightReportsPathsRelativeAndForwardSlashed(t *testing.T) {
	p := testPaths(t)
	// ingest.outDir can point anywhere, including outside the project. There is nothing to be
	// relative to then, so the absolute path is shown — still forward-slashed, still something
	// the reader can act on. (Two t.TempDir calls are siblings, so this really is outside.)
	elsewhere := filepath.Join(t.TempDir(), "data", "forge.json")
	p.ArtifactFile = elsewhere
	got := Preflight(p, only(p.Dir, filepath.Join(p.Dir, "index.html")))
	if len(got.Missing) != 1 {
		t.Fatalf("Missing = %v, want just the artifact", got.Missing)
	}
	if got.Missing[0] != filepath.ToSlash(elsewhere) {
		t.Errorf("Missing[0] = %q, want the absolute path forward-slashed (%q)", got.Missing[0], filepath.ToSlash(elsewhere))
	}
	if strings.Contains(got.Missing[0], `\`) {
		t.Errorf("Missing[0] = %q still carries backslashes; a Windows path in the message should read the same as everywhere else", got.Missing[0])
	}
}

func TestPreflightAgainstTheRealFilesystem(t *testing.T) {
	// Everything above injects `exists`; this one wires the real Exists in, so a change to
	// either half cannot pass by agreeing with a stub.
	root := t.TempDir()
	p := Paths{Root: root, Dir: filepath.Join(root, "dist"), ArtifactFile: filepath.Join(root, "data", "forge.json")}
	if got := Preflight(p, Exists); got.OK {
		t.Fatal("an empty project passed the preflight")
	}
	write(t, p.ArtifactFile, []byte(`{"schemaVersion":1}`))
	write(t, filepath.Join(p.Dir, "index.html"), []byte("<!doctype html>"))
	if got := Preflight(p, Exists); !got.OK {
		t.Fatalf("Preflight = %+v, want OK once both files are on disk", got)
	}
	if err := os.Remove(filepath.Join(p.Dir, "index.html")); err != nil {
		t.Fatal(err)
	}
	if got := Preflight(p, Exists); got.OK {
		t.Fatal("a dist/ emptied of its index.html passed the preflight")
	}
}

func TestNoticeSaysNothingIsRebuilt(t *testing.T) {
	// The stale-dist surprise is the most common confusion this tool produces: edit a template,
	// reload, see the old page. Eight lines up front are cheaper than the debugging session.
	text := strings.Join(Notice(testPaths(t)), "\n")
	for _, want := range []string{
		"most recent `frznforge build`",
		"Nothing is rebuilt here",
		"no file is watched",
		"frznforge build",  // re-render what is already ingested
		"frznforge ingest", // ...and the command that refreshes the artifact first
		"dist/",
		"data/forge.json",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the notice does not contain %q; it reads:\n%s", want, text)
		}
	}
}

func TestNoticeNamesTheConfiguredArtifact(t *testing.T) {
	// ingest.outDir is configurable, so a hard-coded data/ in the notice would point the reader
	// at a file that does not exist on their project.
	p := testPaths(t)
	p.ArtifactFile = filepath.Join(p.Root, "artifact", "forge.json")
	text := strings.Join(Notice(p), "\n")
	if !strings.Contains(text, "artifact/forge.json") {
		t.Errorf("the notice does not name the configured artifact:\n%s", text)
	}
	if strings.Contains(text, "data/forge.json") {
		t.Errorf("the notice hard-codes data/forge.json:\n%s", text)
	}
}

func TestNoticeWithoutAProjectArtifact(t *testing.T) {
	// Under --dir there is no artifact to have an opinion about, and inventing a path would be
	// worse than saying nothing specific.
	p := testPaths(t)
	p.ArtifactFile = ""
	text := strings.Join(Notice(p), "\n")
	if strings.Contains(text, "forge.json") {
		t.Errorf("the notice invented an artifact path:\n%s", text)
	}
	if !strings.Contains(text, "the artifact") {
		t.Errorf("the notice should still say where the pages came from:\n%s", text)
	}
}

func TestNoticeDroppedTheNodeEraCommands(t *testing.T) {
	// scripts/dev.ts pointed the reader at `npm run build` and offered `npm run astro dev` as
	// the HMR escape hatch. Phase 9 deletes both. A notice that still names them sends the
	// reader to a command that no longer exists, which is worse than no notice at all.
	text := strings.Join(Notice(testPaths(t)), "\n")
	for _, gone := range []string{"npm run", "astro", "preview"} {
		if strings.Contains(text, gone) {
			t.Errorf("the notice still mentions %q from the Node toolchain:\n%s", gone, text)
		}
	}
}

func TestNoticeForwardSlashesEveryPath(t *testing.T) {
	// filepath.Join builds these with backslashes on Windows; a notice that prints
	// `dist\index.html` reads like a different project from the docs and the config.
	if text := strings.Join(Notice(testPaths(t)), "\n"); strings.Contains(text, `\`) {
		t.Errorf("the notice contains a backslash path:\n%s", text)
	}
}
