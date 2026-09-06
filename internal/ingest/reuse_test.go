package ingest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"frznforge/internal/config"
	"frznforge/internal/ingest/testsupport"
	"frznforge/internal/model"
)

// Cross-run reuse. Every one of the four skips has the same obligation — it must be incapable of
// changing an artifact byte — so the assertions here are mostly "the replay is indistinguishable
// from the real work" and "anything unverifiable falls back to the real work".

/* ---- CLI ------------------------------------------------------------------ */

func TestParseIngestArgs(t *testing.T) {
	if got, err := ParseIngestArgs(nil); err != nil || got.NoCache || got.BackfillMetadata {
		t.Fatalf("no flags: %+v err=%v", got, err)
	}
	if got, err := ParseIngestArgs([]string{"--no-cache"}); err != nil || !got.NoCache {
		t.Fatalf("--no-cache: %+v err=%v", got, err)
	}
	if got, err := ParseIngestArgs([]string{"--backfill-metadata"}); err != nil || !got.BackfillMetadata {
		t.Fatalf("--backfill-metadata: %+v err=%v", got, err)
	}
	if _, err := ParseIngestArgs([]string{"--nope"}); err == nil {
		t.Error("an unrecognised flag must be an error, not a silent no-op")
	}
	// --no-cache reads nothing from the provider cache, so every repo would look like a gap and
	// the run would be a full refetch wearing the wrong name.
	if _, err := ParseIngestArgs([]string{"--no-cache", "--backfill-metadata"}); err == nil {
		t.Error("the two opposite flags must be rejected together")
	}
}

/* ---- the two time-based skips --------------------------------------------- */

func runLogEntryAt(at time.Time, fresh, gitOK, metaOK bool) *RunLogEntry {
	return &RunLogEntry{FetchedAt: FormatRunLogTime(at), Fresh: fresh, GitOk: gitOK, MetaOk: metaOK}
}

func TestWithinFreshWindow(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	if !WithinFreshWindow(runLogEntryAt(now.Add(-time.Minute), true, true, true), now, 2) {
		t.Error("a fully fresh fetch a minute ago is inside a two-minute window")
	}
	// A degraded run is never window-skipped, so a rate limit heals itself run by run.
	if WithinFreshWindow(runLogEntryAt(now.Add(-time.Minute), false, true, true), now, 2) {
		t.Error("a degraded fetch must always be re-attempted")
	}
	if WithinFreshWindow(runLogEntryAt(now.Add(-3*time.Minute), true, true, true), now, 2) {
		t.Error("an old fetch is outside the window")
	}
	// A stamp in the future is a clock that moved backwards; never trust it.
	if WithinFreshWindow(runLogEntryAt(now.Add(time.Minute), true, true, true), now, 2) {
		t.Error("a future stamp must not open the window")
	}
	if WithinFreshWindow(nil, now, 2) {
		t.Error("a source with no record is never in the window")
	}
	if WithinFreshWindow(&RunLogEntry{FetchedAt: "not a date", Fresh: true}, now, 2) {
		t.Error("an unparseable stamp must not open the window")
	}
}

func TestWithinCooldown(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	hour := int64(3600)

	if !WithinCooldown(runLogEntryAt(now.Add(-10*time.Minute), true, true, true), now, &hour) {
		t.Error("a fully successful fetch ten minutes ago is inside an hour's cooldown")
	}
	// Deliberately stricter than the freshness window: a repo whose provider metadata was
	// rate-limited must never be held back for hours, or a long cooldown would freeze the gap in
	// place. The repos in trouble go first and are always retried.
	if WithinCooldown(runLogEntryAt(now.Add(-10*time.Minute), true, true, false), now, &hour) {
		t.Error("a failed metadata half must not be held back by the cooldown")
	}
	if WithinCooldown(runLogEntryAt(now.Add(-10*time.Minute), true, false, true), now, &hour) {
		t.Error("a failed git half must not be held back by the cooldown")
	}
	if WithinCooldown(runLogEntryAt(now.Add(-2*time.Hour), true, true, true), now, &hour) {
		t.Error("an elapsed cooldown must not skip")
	}
	// Off by default.
	if WithinCooldown(runLogEntryAt(now.Add(-time.Second), true, true, true), now, nil) {
		t.Error("a nil cooldown is disabled")
	}
	if WithinCooldown(nil, now, &hour) {
		t.Error("a source with no record is never on cooldown")
	}
}

/* ---- ref parsing ---------------------------------------------------------- */

func TestParseRefLinesReadsBothGitOutputShapes(t *testing.T) {
	// for-each-ref here emits `<ref>\0<sha>`; ls-remote emits `<sha>\t<ref>`. Both are normalised
	// so the two sides of the comparison are directly comparable.
	forEachRef := ParseRefLines("refs/heads/main\x00aaa\nrefs/tags/v1\x00bbb\n")
	lsRemote := ParseRefLines("aaa\trefs/heads/main\nbbb\trefs/tags/v1\n")
	if !RefsEqual(forEachRef, lsRemote) {
		t.Fatalf("for-each-ref %v != ls-remote %v", forEachRef, lsRemote)
	}

	// `^{}` rows are the peeled targets of annotated tags; the tag object id is what both sides
	// already agree on, so keeping them would only add noise.
	peeled := ParseRefLines("aaa\trefs/tags/v1\nccc\trefs/tags/v1^{}\n")
	if len(peeled) != 1 || peeled["refs/tags/v1"] != "aaa" {
		t.Errorf("peeled rows leaked in: %v", peeled)
	}

	// Blank lines and CRLF, which git output carries on Windows.
	crlf := ParseRefLines("aaa\trefs/heads/main\r\n\r\nbbb\trefs/tags/v1\r\n")
	if len(crlf) != 2 || crlf["refs/heads/main"] != "aaa" || crlf["refs/tags/v1"] != "bbb" {
		t.Errorf("CRLF output = %v", crlf)
	}
}

func TestRefsEqualSpotsAnyDifference(t *testing.T) {
	base := map[string]string{"refs/heads/main": "aaa", "refs/tags/v1": "bbb"}
	if !RefsEqual(base, map[string]string{"refs/tags/v1": "bbb", "refs/heads/main": "aaa"}) {
		t.Error("the same refs in a different order are still the same refs")
	}
	// Mirrors fetch per REPOSITORY, so the only honest granularity is "every ref matches".
	for name, other := range map[string]map[string]string{
		"moved":   {"refs/heads/main": "zzz", "refs/tags/v1": "bbb"},
		"added":   {"refs/heads/main": "aaa", "refs/tags/v1": "bbb", "refs/tags/v2": "ccc"},
		"deleted": {"refs/heads/main": "aaa"},
	} {
		if RefsEqual(base, other) {
			t.Errorf("%s ref was not spotted", name)
		}
	}
}

func TestReadRefHeadsReadsAMirror(t *testing.T) {
	ctx := context.Background()
	repo := testsupport.Create(t, "heads", "main")
	head := repo.WriteAndCommit(map[string]string{"a.txt": "one\n"}, "first",
		testsupport.CommitOptions{Date: testsupport.At(0)})
	repo.Tag("v1.0.0", testsupport.TagOptions{Date: testsupport.At(60)})

	heads := ReadRefHeads(ctx, repo.Dir)
	if heads["refs/heads/main"] != head {
		t.Errorf("refs/heads/main = %q, want %q", heads["refs/heads/main"], head)
	}
	if _, ok := heads["refs/tags/v1.0.0"]; !ok {
		t.Errorf("tags missing from the baseline: %v", heads)
	}
	// A path that is not a repository yields no baseline rather than an empty one: a forgotten
	// baseline costs one extra fetch, a wrong one would skip a fetch that was needed.
	if got := ReadRefHeads(ctx, remoteTempDir(t, "not-a-repo")); got != nil {
		t.Errorf("non-repo baseline = %v, want nil", got)
	}
}

/* ---- run log -------------------------------------------------------------- */

func TestRunLogRoundTrip(t *testing.T) {
	cacheDir := remoteTempDir(t, "cache")
	entries := map[string]RunLogEntry{
		filepath.Join(cacheDir, "mirrors", "gitea-x"): {
			FetchedAt: FormatRunLogTime(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)),
			Fresh:     true, GitOk: true, MetaOk: true,
			Heads: map[string]string{"refs/heads/main": "aaa"},
		},
	}
	WriteRunLog(cacheDir, "cfg-hash", entries)

	log := ReadRunLog(cacheDir)
	if log == nil {
		t.Fatal("the run log did not survive a round trip")
	}
	if log.ConfigHash != "cfg-hash" || len(log.Remotes) != 1 {
		t.Fatalf("log = %+v", log)
	}
	for key, want := range entries {
		got := log.Remotes[key]
		if got.FetchedAt != want.FetchedAt || got.Fresh != want.Fresh ||
			got.GitOk != want.GitOk || got.MetaOk != want.MetaOk ||
			!RefsEqual(got.Heads, want.Heads) {
			t.Errorf("entry = %+v, want %+v", got, want)
		}
	}

	// A log written by an older version is discarded wholesale rather than half-read: it is a
	// rebuildable cache, and the cost of discarding it is one un-skipped fetch cycle.
	if err := os.WriteFile(RunLogPathFor(cacheDir),
		[]byte(`{"version":1,"configHash":"cfg-hash","remotes":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ReadRunLog(cacheDir); got != nil {
		t.Errorf("an older run log was read: %+v", got)
	}

	// So is a corrupt one.
	if err := os.WriteFile(RunLogPathFor(cacheDir), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ReadRunLog(cacheDir); got != nil {
		t.Errorf("a corrupt run log was read: %+v", got)
	}
	if got := ReadRunLog(filepath.Join(cacheDir, "nowhere")); got != nil {
		t.Errorf("a missing run log was read: %+v", got)
	}
}

func TestConfigHashIsOrderIndependentButValueSensitive(t *testing.T) {
	a, err := ConfigHashFor(map[string]any{"x": 1, "y": map[string]any{"b": 2, "a": 3}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := ConfigHashFor(map[string]any{"y": map[string]any{"a": 3, "b": 2}, "x": 1})
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Error("the hash depends on key order")
	}
	c, err := ConfigHashFor(map[string]any{"x": 2, "y": map[string]any{"b": 2, "a": 3}})
	if err != nil {
		t.Fatal(err)
	}
	if a == c {
		t.Error("a changed value did not change the hash")
	}
}

func TestStableStringifySortsRecursively(t *testing.T) {
	got, err := StableStringify(map[string]any{
		"b": 1,
		"a": map[string]any{"z": []any{map[string]any{"n": 1, "m": 2}}, "y": 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"a":{"y":2,"z":[{"m":2,"n":1}]},"b":1}`
	if got != want {
		t.Errorf("StableStringify = %s, want %s", got, want)
	}
}

/* ---- scan cache ----------------------------------------------------------- */

// writeArtifactStores mirrors what the artifact writer puts under outDir, which is where a
// replay reads its bytes back from.
func writeArtifactStores(t testing.TB, outDir string, res ScanResult) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(outDir, blobDirname), 0o755); err != nil {
		t.Fatal(err)
	}
	for sha, data := range res.Blobs {
		if err := os.WriteFile(filepath.Join(outDir, blobDirname, sha), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range res.Archives {
		file := filepath.Join(outDir, filepath.FromSlash(a.File))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, a.Data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func serializeRepo(t testing.TB, repo *model.Repo) string {
	t.Helper()
	encoded, err := model.Serialize(model.ForgeData{
		SchemaVersion: model.SchemaVersion,
		Repos:         []model.Repo{*repo},
		Notes:         []model.Note{}, Organizations: []model.Organization{},
		Hosting: []model.HostedSite{}, Warnings: []model.Warning{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// scanCacheFixture is a repo with a blob and a tag archive — everything a replay has to
// rehydrate.
func scanCacheFixture(t testing.TB) (*testsupport.Repo, ScanSource, ScanOptions) {
	t.Helper()
	repo := testsupport.Create(t, "cached", "main")
	repo.WriteAndCommit(map[string]string{"README.md": "# cached\n", "a.txt": "one\n"}, "first",
		testsupport.CommitOptions{Date: testsupport.At(0)})
	repo.Tag("v1.0.0", testsupport.TagOptions{Annotated: true, Date: testsupport.At(60)})

	opts := testScanOptions()
	opts.Archives = true
	return repo, ScanSource{AbsPath: repo.Dir, Slug: "cached"}, opts
}

func TestScanCacheReplaysIdenticalBytes(t *testing.T) {
	ctx := context.Background()
	repo, source, opts := scanCacheFixture(t)
	cacheDir := remoteTempDir(t, "cache")
	outDir := remoteTempDir(t, "out")

	digest, err := ScanInputDigest(ctx, source, opts)
	if err != nil {
		t.Fatal(err)
	}
	if digest == "" {
		t.Fatal("a real repo must produce a digest")
	}
	res, err := ScanRepo(ctx, source, opts)
	if err != nil || res.Skipped {
		t.Fatalf("ScanRepo: %v skipped=%v", err, res.Skipped)
	}
	if len(res.Blobs) == 0 || len(res.Archives) == 0 {
		t.Fatalf("the fixture produced nothing to rehydrate: %d blobs, %d archives",
			len(res.Blobs), len(res.Archives))
	}
	writeArtifactStores(t, outDir, res)

	file := ScanCachePathFor(cacheDir, source.AbsPath)
	WriteScanCache(file, digest, res)

	entry := ReadScanCache(file)
	if entry == nil {
		t.Fatal("the scan cache did not survive a round trip")
	}
	if entry.InputDigest != digest {
		t.Fatalf("digest = %q, want %q", entry.InputDigest, digest)
	}
	replayed, ok := RehydrateScan(entry, outDir)
	if !ok {
		t.Fatal("a complete store must rehydrate")
	}
	if serializeRepo(t, replayed.Repo) != serializeRepo(t, res.Repo) {
		t.Error("a replayed repo serialised to different bytes")
	}
	// A replay must contribute its FULL buffer maps, or the artifact writer's prune would delete
	// a cached repo's files.
	if len(replayed.Blobs) != len(res.Blobs) {
		t.Errorf("replayed %d blobs, want %d", len(replayed.Blobs), len(res.Blobs))
	}
	for sha, want := range res.Blobs {
		if string(replayed.Blobs[sha]) != string(want) {
			t.Errorf("blob %s came back different", sha)
		}
	}
	if len(replayed.Archives) != len(res.Archives) {
		t.Fatalf("replayed %d archives, want %d", len(replayed.Archives), len(res.Archives))
	}
	for i, a := range res.Archives {
		if replayed.Archives[i].File != a.File || string(replayed.Archives[i].Data) != string(a.Data) {
			t.Errorf("archive %s came back different", a.File)
		}
	}

	// A missing blob means re-scan, never a partial replay.
	for sha := range res.Blobs {
		if err := os.Remove(filepath.Join(outDir, blobDirname, sha)); err != nil {
			t.Fatal(err)
		}
		break
	}
	if _, ok := RehydrateScan(entry, outDir); ok {
		t.Error("a missing blob must force a re-scan")
	}
	_ = repo
}

func TestScanCacheRefusesAnArchiveWhoseBytesMoved(t *testing.T) {
	ctx := context.Background()
	_, source, opts := scanCacheFixture(t)
	cacheDir := remoteTempDir(t, "cache")
	outDir := remoteTempDir(t, "out")

	digest, err := ScanInputDigest(ctx, source, opts)
	if err != nil {
		t.Fatal(err)
	}
	res, err := ScanRepo(ctx, source, opts)
	if err != nil || res.Skipped {
		t.Fatalf("ScanRepo: %v skipped=%v", err, res.Skipped)
	}
	writeArtifactStores(t, outDir, res)
	file := ScanCachePathFor(cacheDir, source.AbsPath)
	WriteScanCache(file, digest, res)

	// Archive paths are slug-keyed, not content-addressed: after a slug-collision rename this
	// path can hold the COLLIDING repo's zip. Without the hash check a replay would silently
	// publish that repo's source archive under this repo's URL.
	victim := filepath.Join(outDir, filepath.FromSlash(res.Archives[0].File))
	if err := os.WriteFile(victim, []byte("PK\x03\x04 someone else's zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	entry := ReadScanCache(file)
	if entry == nil {
		t.Fatal("cache entry missing")
	}
	if _, ok := RehydrateScan(entry, outDir); ok {
		t.Error("a replay accepted another repo's archive bytes")
	}
}

func TestScanInputDigestCoversEverythingTheScanReads(t *testing.T) {
	ctx := context.Background()
	repo, source, opts := scanCacheFixture(t)

	base, err := ScanInputDigest(ctx, source, opts)
	if err != nil {
		t.Fatal(err)
	}
	same, err := ScanInputDigest(ctx, source, opts)
	if err != nil {
		t.Fatal(err)
	}
	if base != same {
		t.Fatal("the digest is not stable for unchanged inputs")
	}

	changed := func(name string, mutate func(*ScanSource, *ScanOptions)) {
		t.Helper()
		s, o := source, opts
		mutate(&s, &o)
		got, err := ScanInputDigest(ctx, s, o)
		if err != nil {
			t.Fatal(err)
		}
		if got == base {
			t.Errorf("%s did not invalidate the digest", name)
		}
	}

	// Provider metadata is part of the key ON PURPOSE: descriptions, links and releases change
	// with no ref-head change, and a key without them would replay stale releases into a
	// schema-valid artifact.
	changed("provider metadata", func(s *ScanSource, _ *ScanOptions) {
		s.ProviderMeta = &config.RepoMetaInput{Description: strPtr("new description")}
	})
	changed("releases", func(s *ScanSource, _ *ScanOptions) {
		s.Releases = []model.Release{stubRelease("v9.9.9")}
	})
	changed("overrides", func(s *ScanSource, _ *ScanOptions) {
		s.Overrides = &config.RepoMetaInput{Name: strPtr("renamed")}
	})
	changed("scan options", func(_ *ScanSource, o *ScanOptions) { o.MaxBlobBytes = 1 })
	changed("contributors", func(_ *ScanSource, o *ScanOptions) {
		o.Contributors = BuildContributorIndex([]config.ContributorConfig{
			{Name: "Someone", Emails: []string{"someone@example.com"}},
		})
	})

	// A new commit moves refs/heads/main, which is in the digest.
	repo.WriteAndCommit(map[string]string{"a.txt": "two\n"}, "second",
		testsupport.CommitOptions{Date: testsupport.At(120)})
	moved, err := ScanInputDigest(ctx, source, opts)
	if err != nil {
		t.Fatal(err)
	}
	if moved == base {
		t.Error("a new commit did not invalidate the digest")
	}

	// A path that is not a repository has no digest: the caller runs the real scan, which emits
	// the repo-not-found skip itself.
	empty, err := ScanInputDigest(ctx, ScanSource{AbsPath: remoteTempDir(t, "not-a-repo")}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if empty != "" {
		t.Errorf("digest for a non-repo = %q, want empty", empty)
	}
}

func TestReadScanCacheRejectsWhatCannotBeReplayed(t *testing.T) {
	cacheDir := remoteTempDir(t, "cache")
	file := filepath.Join(cacheDir, "entry.json")

	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Built field by field rather than by find-and-replace: the entry and the repo it wraps both
	// have an `archives` key, and patching the wrong one is how this test silently stops testing.
	entryJSON := func(head, repo, blobShas, archives string) string {
		return `{` + head + `"repo":` + repo + `,"blobShas":` + blobShas + `,"archives":` + archives + `}`
	}
	const head = `"version":1,"inputDigest":"d",`
	valid := entryJSON(head, minimalRepoJSON, "[]", "[]")

	write(valid)
	if ReadScanCache(file) == nil {
		t.Fatal("a well-formed entry must be readable")
	}
	for name, body := range map[string]string{
		"older version":   entryJSON(`"version":0,"inputDigest":"d",`, minimalRepoJSON, "[]", "[]"),
		"no digest":       entryJSON(`"version":1,`, minimalRepoJSON, "[]", "[]"),
		"corrupt json":    "{not json",
		"unnamed archive": entryJSON(head, minimalRepoJSON, "[]", `[{"sha256":"x"}]`),
		"invalid repo slug": entryJSON(head,
			strings.Replace(minimalRepoJSON, `"slug":"cached"`, `"slug":"Not A Slug"`, 1), "[]", "[]"),
		// encoding/json leaves a missing array key as a nil slice, which marshals as `null` where
		// the artifact says `[]` — replaying that would write different bytes for the same commits.
		"missing container": entryJSON(head,
			strings.Replace(minimalRepoJSON, `"tags":[],`, "", 1), "[]", "[]"),
	} {
		write(body)
		if got := ReadScanCache(file); got != nil {
			t.Errorf("%s was accepted: %+v", name, got.Repo)
		}
	}
}

// minimalRepoJSON is the smallest artifact-valid repo record, used to probe the cache reader's
// rejection rules without running a scan.
const minimalRepoJSON = `{
  "slug":"cached","name":"cached","description":null,
  "source":{"type":"local","path":"/tmp/cached"},
  "links":{},"tags":[],"template":false,"license":null,
  "releaseMode":"tags","releases":[],
  "empty":true,"defaultBranch":null,"branches":[],"gitTags":[],
  "commits":{},"commitCount":0,"extraCommits":{},
  "tree":[],"files":{},"refTrees":{},"archives":[],
  "languages":[],"contributors":[],"insights":null,"readme":null,
  "createdAt":null,"updatedAt":null,"warnings":[]
}`

func TestScanCachePathIsKeyedOnTheRepoPath(t *testing.T) {
	cacheDir := filepath.Join("c", "cache")
	a := ScanCachePathFor(cacheDir, filepath.Join("x", "one"))
	b := ScanCachePathFor(cacheDir, filepath.Join("x", "two"))
	if a == b {
		t.Error("two repos collapsed onto one scan-cache file")
	}
	if a != ScanCachePathFor(cacheDir, filepath.Join("x", "one")) {
		t.Error("the same repo must reuse its cache file across runs")
	}
	if filepath.Dir(a) != filepath.Join(cacheDir, scanCacheDirname) {
		t.Errorf("scan caches live in %q", filepath.Dir(a))
	}
}
