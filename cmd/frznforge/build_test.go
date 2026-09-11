package main

// `frznforge build`'s command line and its one refusal — the port of tests/unit/build-script.ts.
//
// The 0.3.0 wrapper existed because `npm run ingest && astro build` could not be steered: in a
// chained npm script every `--` argument lands on the last command. The flags survived the
// rewrite; the routing did not, because there is no second process to route to any more. That
// difference is asserted below rather than assumed.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"frznforge/internal/config"
	"frznforge/internal/model"
)

/* ---- argument parsing ----------------------------------------------------- */

func TestParseBuildArgsDefaultsToIngestThenRender(t *testing.T) {
	// A bare `frznforge build` is the whole build: scan, then render. Anything else would make
	// the documented one-command workflow wrong.
	args, err := parseBuildArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if args.NoIngest || args.Ingest.NoCache || args.Ingest.BackfillMetadata || args.Ingest.RefreshMeta {
		t.Errorf("a bare build is not a plain build: %+v", args)
	}
	if args.Root != "." || args.Build.Root != "." || args.OutDirSet {
		t.Errorf("root/out defaults = %q/%q/%v", args.Root, args.Build.Root, args.OutDirSet)
	}
}

func TestParseBuildArgsTakesItsOwnFlagsAndForwardsTheIngestOnes(t *testing.T) {
	cases := []struct {
		argv  []string
		check func(buildArgs) string
	}{
		{[]string{"--no-ingest"}, func(a buildArgs) string {
			if !a.NoIngest {
				return "--no-ingest was not taken"
			}
			return ""
		}},
		{[]string{"--no-cache"}, func(a buildArgs) string {
			if !a.Ingest.NoCache {
				return "--no-cache did not reach the ingest half"
			}
			return ""
		}},
		{[]string{"--backfill-metadata"}, func(a buildArgs) string {
			if !a.Ingest.BackfillMetadata {
				return "--backfill-metadata did not reach the ingest half"
			}
			return ""
		}},
		{[]string{"--refresh-meta"}, func(a buildArgs) string {
			if !a.Ingest.RefreshMeta {
				return "--refresh-meta did not reach the ingest half"
			}
			return ""
		}},
		{[]string{"--serial"}, func(a buildArgs) string {
			if a.Build.Workers != 1 {
				return "--serial did not pin the renderer to one worker"
			}
			return ""
		}},
		{[]string{"--workers=4"}, func(a buildArgs) string {
			if a.Build.Workers != 4 {
				return "--workers was not read"
			}
			return ""
		}},
		{[]string{"-v"}, func(a buildArgs) string {
			if !a.Build.Verbose {
				return "-v did not reach the renderer"
			}
			return ""
		}},
		{[]string{"--root=proj", "--out=site"}, func(a buildArgs) string {
			if a.Root != "proj" || a.Build.Root != "proj" || a.Build.OutDir != "site" || !a.OutDirSet {
				return "--root/--out were not both applied"
			}
			return ""
		}},
		{[]string{"--postprocess=echo hi"}, func(a buildArgs) string {
			if a.Build.Postprocess != "echo hi" {
				return "--postprocess was not read whole (a command has spaces in it)"
			}
			return ""
		}},
	}
	for _, c := range cases {
		args, err := parseBuildArgs(c.argv)
		if err != nil {
			t.Errorf("%v: %v", c.argv, err)
			continue
		}
		if problem := c.check(args); problem != "" {
			t.Errorf("%v: %s", c.argv, problem)
		}
	}
}

func TestParseBuildArgsRejectsAnUnknownFlagRatherThanForwardingIt(t *testing.T) {
	// The 0.3.0 wrapper passed anything it did not recognise to `astro build`, which reported its
	// own bad input. There is no downstream any more, so a forwarded flag would silently do
	// nothing — the user would watch a build ignore the option they came to use.
	_, err := parseBuildArgs([]string{"--silent"})
	if err == nil {
		t.Fatal("--silent was accepted")
	}
	mustContain(t, err.Error(), `build: unknown flag "--silent"`, "the flag is not named")
	mustContain(t, err.Error(), "frznforge build", "the refusal does not carry the usage")
}

func TestParseBuildArgsRefusesNoIngestTogetherWithAnIngestFlag(t *testing.T) {
	// Asking for an ingest behaviour AND for no ingest means one of the two was a mistake.
	// Silently dropping either would be guessing which, and the guess is invisible.
	for _, flag := range []string{"--no-cache", "--backfill-metadata", "--refresh-meta"} {
		_, err := parseBuildArgs([]string{"--no-ingest", flag})
		if err == nil {
			t.Errorf("--no-ingest %s was accepted", flag)
			continue
		}
		mustContain(t, err.Error(), "--no-ingest cannot be combined with "+flag, "the conflict does not name both flags")
		mustContain(t, err.Error(), "Drop one", "the refusal does not say what to do")
	}
	// Both at once are quoted back together rather than one at a time.
	_, err := parseBuildArgs([]string{"--no-ingest", "--no-cache", "--backfill-metadata"})
	if err == nil {
		t.Fatal("all three together were accepted")
	}
}

func TestParseBuildArgsRefusesTheTwoIngestFlagsThatAreOpposites(t *testing.T) {
	// --no-cache reads nothing from the provider cache, so every repo would look like a gap and
	// a "backfill" would be a full refetch wearing the wrong name — and spend the whole quota.
	// --refresh-meta conflicts with the backfill for the mirror-image reason: it asks for every
	// repo's metadata to be re-requested, which is the spend the backfill exists to avoid.
	for _, argv := range [][]string{
		{"--no-cache", "--backfill-metadata"},
		{"--refresh-meta", "--backfill-metadata"},
	} {
		err := mustFailBuildArgs(t, argv)
		if err == nil {
			continue
		}
		mustContain(t, err.Error(), "opposites", "the refusal does not say why they conflict")
	}
	// --no-cache and --refresh-meta are redundant rather than contradictory: --no-cache already
	// re-fetches everything, so asking for fresh metadata alongside it is not a mistake.
	if _, err := parseBuildArgs([]string{"--no-cache", "--refresh-meta"}); err != nil {
		t.Errorf("--no-cache --refresh-meta was refused: %v", err)
	}
}

// mustFailBuildArgs is the "this combination is refused" half of the two tests above.
func mustFailBuildArgs(t *testing.T, argv []string) error {
	t.Helper()
	_, err := parseBuildArgs(argv)
	if err == nil {
		t.Errorf("%v was accepted", argv)
	}
	return err
}

func TestParseBuildArgsAllowsEachFlagOnItsOwn(t *testing.T) {
	// The counterpart to the two refusals above: neither may become a general ban on the flag.
	for _, argv := range [][]string{
		{"--no-ingest"}, {"--no-cache"}, {"--backfill-metadata"}, {"--refresh-meta"},
		{"--no-ingest", "-v"}, nil,
	} {
		if _, err := parseBuildArgs(argv); err != nil {
			t.Errorf("%v: %v", argv, err)
		}
	}
}

func TestParseBuildArgsRejectsAWorkerCountThatIsNotOne(t *testing.T) {
	// 0 or a negative would deadlock the pool or fall back to the default without saying so.
	for _, bad := range []string{"--workers=0", "--workers=-2", "--workers=many", "--workers="} {
		if _, err := parseBuildArgs([]string{bad}); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
}

/* ---- --no-ingest ---------------------------------------------------------- */

func TestNoIngestRefusesWhenThereIsNoArtifactToRender(t *testing.T) {
	// The renderer treats a missing artifact as its own error, but that message is about a file
	// rather than about the flag that made it matter — and the fix, "run it once without
	// --no-ingest", would be nowhere on screen. Without the check the user replaces a good site
	// with an empty one and finds out from a visitor.
	root := t.TempDir()
	writeMinimalConfig(t, root)

	tio := newTestIo(root)
	err := run([]string{"build", "--no-ingest", "--root=" + root}, tio.Io)
	if err == nil {
		t.Fatal("--no-ingest rendered with no artifact")
	}
	text := err.Error()
	// Project-relative and forward-slashed, so it can be pasted into a shell as it stands.
	mustContain(t, text, "missing: data/forge.json", "the missing file is not named in a pasteable form")
	mustContain(t, text, "frznforge build", "the refusal does not name the command that fixes it")
	mustContain(t, text, "frznforge ingest", "the refusal does not offer the scan-only command")
	if _, statErr := os.Stat(filepath.Join(root, "dist")); !os.IsNotExist(statErr) {
		t.Error("a refused build still created dist/")
	}
}

func TestNoIngestRendersTheArtifactOnDiskWithoutScanningAnything(t *testing.T) {
	// The flag's purpose: iterate on templates and styles against the artifact you already have.
	// It must reach the renderer, say plainly that nothing was fetched, and produce a site.
	root := t.TempDir()
	writeMinimalConfig(t, root)
	writeEmptyArtifact(t, root)

	tio := newTestIo(root)
	if err := run([]string{"build", "--no-ingest", "--root=" + root}, tio.Io); err != nil {
		t.Fatalf("build --no-ingest: %v", err)
	}

	out := tio.stdout()
	mustContain(t, out, "--no-ingest: rendering data/forge.json as it stands; no repo is fetched or scanned.",
		"the run does not say the scan was skipped")
	// The scan announcement belongs to the other branch. Printing it here would tell a reader
	// their repositories were refreshed when nothing touched the network.
	mustNotContain(t, out, "scanning first", "a --no-ingest run claimed to scan")
	mustContain(t, out, "built ", "the run does not report what it produced")

	for _, page := range []string{"index.html", filepath.Join("repos", "index.html"), "404.html"} {
		if _, err := os.Stat(filepath.Join(root, "dist", page)); err != nil {
			t.Errorf("no %s in dist/: %v", page, err)
		}
	}
	// The artifact is an input, never an output: --no-ingest must not have rewritten it.
	if got := readFile(t, filepath.Join(root, "data", "forge.json")); got != emptyArtifactJSON(t) {
		t.Error("--no-ingest rewrote the artifact it was told only to render")
	}
}

func TestBuildSaysItIsAboutToScanWhenIngestIsNotSkipped(t *testing.T) {
	// `frznforge build` scans before it renders — the expensive half and the surprising one, so
	// the line that names the flag which skips it has to be where the time is being spent.
	root := t.TempDir()
	writeMinimalConfig(t, root)

	tio := newTestIo(root)
	if err := run([]string{"build", "--root=" + root}, tio.Io); err != nil {
		t.Fatalf("build: %v", err)
	}
	mustContain(t, tio.stdout(), "scanning first — pass --no-ingest", "a scanning build does not say so")
	// A config with no repositories still produces an artifact, and the render that follows finds
	// it — which is what proves the two halves were run in the right order by one process.
	if _, err := os.Stat(filepath.Join(root, "data", "forge.json")); err != nil {
		t.Errorf("the scan wrote no artifact: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "dist", "index.html")); err != nil {
		t.Errorf("the render produced no home page: %v", err)
	}
}

/* ---- fixtures ------------------------------------------------------------- */

// writeMinimalConfig is the smallest config the real loader accepts: everything else defaults,
// including ingest.outDir → <root>/data, which is the path the refusal message has to name.
func writeMinimalConfig(t *testing.T, root string) {
	t.Helper()
	const src = "{\n  \"owner\": { \"name\": \"Test Owner\", \"handle\": \"test\" },\n  \"repos\": []\n}\n"
	if err := os.WriteFile(filepath.Join(root, config.Filename), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeEmptyArtifact puts a valid, repository-free forge.json where the config says the ingest
// would have left one.
func writeEmptyArtifact(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, "data")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "forge.json"), []byte(emptyArtifactJSON(t)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func emptyArtifactJSON(t *testing.T) string {
	t.Helper()
	raw, err := model.Serialize(model.EmptyForgeData())
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// relForwardSlashed is what the refusal prints; asserted here so a Windows path separator cannot
// creep into a message the user is expected to paste.
func TestRelIsProjectRelativeAndForwardSlashed(t *testing.T) {
	root := filepath.Join("C:", "proj")
	if got := rel(root, filepath.Join(root, "data", "forge.json")); got != "data/forge.json" {
		t.Errorf("rel = %q", got)
	}
	// A target outside the root has no relative form worth showing, so the absolute path is
	// printed instead of a chain of `../..`.
	outside := filepath.Join("D:", "elsewhere", "forge.json")
	if got := rel(root, outside); !strings.HasSuffix(got, "elsewhere/forge.json") {
		t.Errorf("rel outside the root = %q", got)
	}
}
