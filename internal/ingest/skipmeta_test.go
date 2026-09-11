package ingest

// ingest.skipMetaRefetches: serve a repo's cached provider metadata record instead of
// re-requesting it, for as long as that record is a completed provider answer.
//
// The flag trades an unbounded amount of metadata staleness for API quota, so the tests it has to
// pass are the ones that pin what it may NOT trade away:
//
//   - the artifact is identical whether the metadata was fetched or served (the whole safety
//     argument — a skipped fetch and a fetch returning the same values differ in no byte);
//   - it raises no warning, because warnings ARE artifact bytes and one here would make
//     forge.json depend on how warm this machine's cache happens to be;
//   - git still fetches, so a repo's new commits still appear;
//   - releases still fetch, so a new release still publishes;
//   - a hollow cache entry does not satisfy it, or "I asked once and got nothing" would become
//     "never ask again";
//   - it does not downgrade the run log's metaOk, which would silently disable
//     ingest.reuse.cooldownSeconds for every repo forever.
//
// The tests split at the seam the flag does: PrepareRemote decides whether to CALL the importer,
// and Ingest decides whether the flag applies at all. No test here touches the network — the
// "remote" is a local fixture repo cloned by path.

import (
	"bytes"
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

/* ---- helpers -------------------------------------------------------------- */

// namedProviderMeta is stubProviderMeta plus the one field the shared stub deliberately leaves
// nil. providerMetaUsable requires a name, so a cache seeded from the bare stub is (correctly)
// not usable — every test that wants the skip to fire warms the cache with this instead.
func namedProviderMeta() ImportedRepoMeta {
	meta := stubProviderMeta()
	meta.Name = strPtr("widget")
	return meta
}

// namedStub is an importer whose FetchMeta answers the way a real one does: with a name, a web
// URL and a clone URL always set.
func namedStub(releases ...model.Release) *stubImporter {
	meta := namedProviderMeta()
	return &stubImporter{meta: &meta, releases: releases}
}

// seedFixtureRepo is one fixture origin plus the root/cache/resolved-source triple every test
// below starts from.
func seedFixtureRepo(t testing.TB) (*testsupport.Repo, string, string, config.ResolvedSource) {
	t.Helper()
	origin := testsupport.Create(t, "widget", "main")
	origin.WriteAndCommit(map[string]string{"a.txt": "one\n"}, "first",
		testsupport.CommitOptions{Date: testsupport.At(0)})
	root := remoteTempDir(t, "root")
	cacheDir := remoteTempDir(t, "cache")
	return origin, root, cacheDir, resolvedRemote(cacheDir, giteaRemoteSource())
}

// skipMetaIngestConfig is the smallest resolved config Ingest reads for one remote source.
// ingest.reuse is left OFF: the freshness window would skip the second run of every test here
// before the flag was ever consulted, and the flag deliberately does not depend on the run log —
// which is itself the reason it lives beside `fetch` rather than inside `reuse`.
func skipMetaIngestConfig(root, cacheDir, outDir string, source config.ResolvedSource) *config.Resolved {
	cfg := &config.Resolved{Root: root, OutDir: outDir, CacheDir: cacheDir}
	cfg.Ingest.Fetch = "auto"
	cfg.Ingest.OutDir = outDir
	cfg.Ingest.CacheDir = cacheDir
	cfg.Ingest.MaxBlobBytes = 512 * 1024
	cfg.Ingest.TagTrees = 5
	cfg.Ingest.Concurrency = 1
	cfg.Sources = []config.ResolvedSource{source}
	return cfg
}

/* ---- the guard in PrepareRemote ------------------------------------------- */

func TestSkipMetaRefetchesServesCachedMetadataAndStillFetchesGit(t *testing.T) {
	ctx := context.Background()
	origin, root, cacheDir, resolved := seedFixtureRepo(t)

	first, err := PrepareRemote(ctx, resolved, remoteTestConfig(root, cacheDir, "auto"), PrepareRemoteDeps{
		Env:            Env{},
		CreateImporter: alwaysStub(namedStub(stubRelease("v1.0.0"))),
		EnsureMirror:   localMirror(origin.Dir),
	}, PrepareRemoteOptions{})
	if err != nil {
		t.Fatalf("warm run: %v", err)
	}

	// The mirror seam is wrapped rather than replaced: this is the half --backfill-metadata skips
	// and this flag must not, so the test counts the calls instead of failing on them.
	mirrored := 0
	stub := namedStub(stubRelease("v1.0.0"))
	second, err := PrepareRemote(ctx, resolved, remoteTestConfig(root, cacheDir, "auto"), PrepareRemoteDeps{
		Env:            Env{},
		CreateImporter: alwaysStub(stub),
		EnsureMirror: func(ctx context.Context, src config.RepoSourceConfig, cachePath string, opts EnsureMirrorOptions) EnsureMirrorResult {
			mirrored++
			return localMirror(origin.Dir)(ctx, src, cachePath, opts)
		},
	}, PrepareRemoteOptions{SkipMetaRefetches: true})
	if err != nil {
		t.Fatalf("skipped run: %v", err)
	}

	wantSequence(t, "importer calls", stub.recorded(), []string{"releases"})
	if mirrored != 1 || second.Action != MirrorFetched {
		t.Errorf("git was not fetched: %d mirror call(s), action %s", mirrored, second.Action)
	}
	// Silent by design: a warning is artifact bytes, and one here would make the published file
	// depend on how warm this machine's cache is.
	if len(second.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", warningCodes(second.Warnings))
	}
	if !sameMetaLayer(first.ProviderMeta, second.ProviderMeta) {
		t.Errorf("provider layer drifted:\n fetched = %+v\n served  = %+v", first.ProviderMeta, second.ProviderMeta)
	}
	wantStr(t, "source.cloneUrl", second.ScanSource.Source.CloneURL, "https://gitea.example.com/acme/widget.git")

	// The metadata half is recorded as SUCCESSFUL, and that is load-bearing rather than tidy.
	//
	// The first version of this recorded nil — "not attempted" — by analogy with git in backfill
	// mode, so the caller would carry the previous run's answer forward. But adding the config key
	// changes the config hash, which discards the run log, so the FIRST flag-on run has nothing to
	// carry: MetaOk stayed false, the skip kept firing, and nothing ever set it back. Cooldown
	// refuses to skip a repo whose metadata half last failed, so ingest.reuse.cooldownSeconds died
	// permanently the day the flag was turned on. TestSkipMetaSurvivesTheUpgradePath covers that.
	//
	// True is also simply correct: this skip fires only after providerMetaUsable has confirmed the
	// cached record is complete, so "we have good metadata" is something the run knows, unlike
	// backfill's silence about the mirror.
	status := second.FetchStatus
	if status == nil || status.Meta == nil || !*status.Meta || status.MetaFresh || !status.MetaSkipped {
		t.Errorf("fetch status = %+v, want Meta true / MetaFresh false / MetaSkipped true", status)
	}
	if status == nil || status.Git == nil || !*status.Git {
		t.Errorf("the git half must still be recorded as fetched: %+v", status)
	}
}

func TestSkipMetaRefetchesFetchesWhenTheCacheIsHollow(t *testing.T) {
	ctx := context.Background()

	// Each body is a .meta.json that EXISTS but was never written by a successful FetchMeta: a
	// hand-seeded file, a truncated write, a foreign one. Suppressing the call on any of them
	// would freeze the repo's metadata at "nothing" forever.
	cases := map[string]string{
		"no meta at all":  `{"version":1,"meta":null,"releases":[]}`,
		"an empty record": `{"version":1,"meta":{},"releases":[]}`,
		"no web url": `{"version":1,"meta":{"name":"widget","webUrl":"",
			"cloneUrl":"https://gitea.example.com/acme/widget.git"},"releases":[]}`,
		"an unusable clone url": `{"version":1,"meta":{"name":"widget",
			"webUrl":"https://gitea.example.com/acme/widget","cloneUrl":"ftp://x"},"releases":[]}`,
		"a blank name": `{"version":1,"meta":{"name":"","webUrl":"https://gitea.example.com/acme/widget",
			"cloneUrl":"https://gitea.example.com/acme/widget.git"},"releases":[]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			origin, root, cacheDir, resolved := seedFixtureRepo(t)
			cacheFile := ProviderCachePathFor(resolved.AbsPath)
			if err := os.MkdirAll(filepath.Dir(cacheFile), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cacheFile, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}

			stub := namedStub()
			if _, err := PrepareRemote(ctx, resolved, remoteTestConfig(root, cacheDir, "auto"), PrepareRemoteDeps{
				Env: Env{}, CreateImporter: alwaysStub(stub), EnsureMirror: localMirror(origin.Dir),
			}, PrepareRemoteOptions{SkipMetaRefetches: true}); err != nil {
				t.Fatalf("PrepareRemote: %v", err)
			}
			wantSequence(t, "importer calls", stub.recorded(), []string{"meta", "releases"})
		})
	}
}

func TestSkipMetaRefetchesNeverStarvesARepoWithNoCache(t *testing.T) {
	// The pathology the flag must not create: a repo whose metadata has never arrived is exactly
	// the one that needs the request. It also pairs with orderMissesFirst, which puts these repos
	// at the head of the queue precisely so a limited quota reaches them.
	ctx := context.Background()
	origin, root, cacheDir, resolved := seedFixtureRepo(t)

	stub := namedStub(stubRelease("v1.0.0"))
	prepared, err := PrepareRemote(ctx, resolved, remoteTestConfig(root, cacheDir, "auto"), PrepareRemoteDeps{
		Env: Env{}, CreateImporter: alwaysStub(stub), EnsureMirror: localMirror(origin.Dir),
	}, PrepareRemoteOptions{SkipMetaRefetches: true})
	if err != nil {
		t.Fatalf("PrepareRemote: %v", err)
	}
	wantSequence(t, "importer calls", stub.recorded(), []string{"meta", "releases"})
	if prepared.FetchStatus == nil || prepared.FetchStatus.MetaSkipped {
		t.Errorf("a repo with nothing cached must not report a skip: %+v", prepared.FetchStatus)
	}
	if prepared.ProviderMeta == nil || prepared.ProviderMeta.Name == nil {
		t.Errorf("the fetched metadata did not reach the layer: %+v", prepared.ProviderMeta)
	}
}

func TestSkipMetaRefetchesStillFetchesReleases(t *testing.T) {
	// The flag's name-honesty test. A changed description is cosmetic; a new release is content a
	// visitor came for, and the per-source lever for release cost is already `"releases": "tags"`.
	ctx := context.Background()
	origin, root, cacheDir, resolved := seedFixtureRepo(t)

	if _, err := PrepareRemote(ctx, resolved, remoteTestConfig(root, cacheDir, "auto"), PrepareRemoteDeps{
		Env:            Env{},
		CreateImporter: alwaysStub(namedStub(stubRelease("v1.0.0"))),
		EnsureMirror:   localMirror(origin.Dir),
	}, PrepareRemoteOptions{}); err != nil {
		t.Fatalf("warm run: %v", err)
	}

	stub := namedStub(stubRelease("v2.0.0"), stubRelease("v1.0.0"))
	prepared, err := PrepareRemote(ctx, resolved, remoteTestConfig(root, cacheDir, "auto"), PrepareRemoteDeps{
		Env: Env{}, CreateImporter: alwaysStub(stub), EnsureMirror: localMirror(origin.Dir),
	}, PrepareRemoteOptions{SkipMetaRefetches: true})
	if err != nil {
		t.Fatalf("skipped run: %v", err)
	}
	wantSequence(t, "importer calls", stub.recorded(), []string{"releases"})
	wantSequence(t, "releases", tags(prepared.Releases), []string{"v2.0.0", "v1.0.0"})
}

func TestSkipMetaRefetchesDoesNotSilenceTheReleasesHalf(t *testing.T) {
	// The skip suppresses one request; it must not suppress what the OTHER half has to say. A
	// releases failure that fell back to cache is still a degraded run and still warns.
	ctx := context.Background()
	origin, root, cacheDir, resolved := seedFixtureRepo(t)

	if _, err := PrepareRemote(ctx, resolved, remoteTestConfig(root, cacheDir, "auto"), PrepareRemoteDeps{
		Env:            Env{},
		CreateImporter: alwaysStub(namedStub(stubRelease("v1.0.0"))),
		EnsureMirror:   localMirror(origin.Dir),
	}, PrepareRemoteOptions{}); err != nil {
		t.Fatalf("warm run: %v", err)
	}

	stub := namedStub()
	stub.releasesErr = &ImporterError{Kind: KindNetwork, Message: "GET /releases failed"}
	prepared, err := PrepareRemote(ctx, resolved, remoteTestConfig(root, cacheDir, "auto"), PrepareRemoteDeps{
		Env: Env{}, CreateImporter: alwaysStub(stub), EnsureMirror: localMirror(origin.Dir),
	}, PrepareRemoteOptions{SkipMetaRefetches: true})
	if err != nil {
		t.Fatalf("degraded run: %v", err)
	}
	wantSequence(t, "warnings", warningCodes(prepared.Warnings),
		[]string{"remote-fetch-failed", "remote-cache-stale"})
	// "releases", not "metadata and releases": the metadata was not stale, it was not asked for.
	if got := prepared.Warnings[1].Message; got != "cached provider releases were used" {
		t.Errorf("stale message = %q", got)
	}
	// A releases call that was made and failed is a false, not the nil that means "not attempted".
	if prepared.FetchStatus == nil || prepared.FetchStatus.Meta == nil || *prepared.FetchStatus.Meta {
		t.Errorf("a failed releases half must record metaOk false: %+v", prepared.FetchStatus)
	}
}

func TestFetchNeverIgnoresSkipMetaRefetches(t *testing.T) {
	// ingest.fetch: "never" owns its own stale-cache warning, and the caller gates the skip to
	// "auto" so this branch is unreachable with it on. Pinned anyway: a skip that suppressed this
	// message would make an offline build look like a healthy one.
	ctx := context.Background()
	origin, root, cacheDir, resolved := seedFixtureRepo(t)

	if _, err := PrepareRemote(ctx, resolved, remoteTestConfig(root, cacheDir, "auto"), PrepareRemoteDeps{
		Env:            Env{},
		CreateImporter: alwaysStub(namedStub(stubRelease("v1.0.0"))),
		EnsureMirror:   localMirror(origin.Dir),
	}, PrepareRemoteOptions{}); err != nil {
		t.Fatalf("warm run: %v", err)
	}

	stub := namedStub()
	prepared, err := PrepareRemote(ctx, resolved, remoteTestConfig(root, cacheDir, "never"), PrepareRemoteDeps{
		Env: Env{}, CreateImporter: alwaysStub(stub), EnsureMirror: localMirror(origin.Dir),
	}, PrepareRemoteOptions{SkipMetaRefetches: true})
	if err != nil {
		t.Fatalf("offline run: %v", err)
	}
	if got := stub.recorded(); len(got) != 0 {
		t.Errorf("an offline run called the API: %v", got)
	}
	wantSequence(t, "warnings", warningCodes(prepared.Warnings), []string{"remote-cache-stale"})
	if !strings.Contains(prepared.Warnings[0].Message, "came from the cache") {
		t.Errorf("message = %q", prepared.Warnings[0].Message)
	}
}

/* ---- the whole pipeline --------------------------------------------------- */

func TestSkipMetaRefetchesWritesTheSameArtifactAsAFetch(t *testing.T) {
	// The claim the feature rests on: a served record and a fetch that returned those same values
	// produce the same bytes. They flow through the identical metaLayer → validateLayer →
	// providerURLs → newScanSource path, so this is a property of the code rather than a
	// coincidence — which is exactly why it is worth pinning as bytes.
	ctx := context.Background()
	origin, root, cacheDir, resolved := seedFixtureRepo(t)

	run := func(outDir string, skip bool, stub *stubImporter) []byte {
		t.Helper()
		cfg := skipMetaIngestConfig(root, cacheDir, outDir, resolved)
		cfg.Ingest.SkipMetaRefetches = skip
		res, err := Ingest(ctx, cfg, Hooks{}, Options{Remote: PrepareRemoteDeps{
			Env: Env{}, CreateImporter: alwaysStub(stub), EnsureMirror: localMirror(origin.Dir),
		}})
		if err != nil {
			t.Fatalf("ingest (skip=%v): %v", skip, err)
		}
		if err := WriteArtifact(res.Data, res.Blobs, res.Archives, outDir); err != nil {
			t.Fatalf("write artifact (skip=%v): %v", skip, err)
		}
		raw, err := os.ReadFile(filepath.Join(outDir, "forge.json"))
		if err != nil {
			t.Fatalf("read artifact (skip=%v): %v", skip, err)
		}
		return raw
	}

	fetched := run(remoteTempDir(t, "out-fetched"), false, namedStub(stubRelease("v1.0.0")))

	// Rigged to fail if called. The recorded() check below is the direct assertion; the byte
	// comparison would catch it too, because a failed FetchMeta raises remote-fetch-failed and a
	// warning is part of the artifact.
	refuses := namedStub(stubRelease("v1.0.0"))
	refuses.metaErr = &ImporterError{
		Kind:    KindNetwork,
		Message: "FetchMeta must not be called while ingest.skipMetaRefetches is on",
	}
	served := run(remoteTempDir(t, "out-served"), true, refuses)

	wantSequence(t, "importer calls", refuses.recorded(), []string{"releases"})
	if !bytes.Equal(fetched, served) {
		t.Errorf("the artifact differs between a fetch and a served cache\n fetched: %d bytes\n served:  %d bytes",
			len(fetched), len(served))
	}
}

func TestSkipMetaRefetchesStillPublishesNewCommits(t *testing.T) {
	// Unlike --backfill-metadata, this flag never takes the early-return replay branch and never
	// forces the mirror's fetch mode, so git is untouched by it. Asserted with a real commit
	// rather than with a mirror-call count, because what a user notices is the missing commit.
	ctx := context.Background()
	origin, root, cacheDir, resolved := seedFixtureRepo(t)
	outDir := remoteTempDir(t, "out")

	cfg := skipMetaIngestConfig(root, cacheDir, outDir, resolved)
	cfg.Ingest.SkipMetaRefetches = true
	deps := func(stub *stubImporter) PrepareRemoteDeps {
		return PrepareRemoteDeps{
			Env: Env{}, CreateImporter: alwaysStub(stub), EnsureMirror: localMirror(origin.Dir),
		}
	}

	// Nothing is cached yet, so this run pays for the metadata — the flag never starves a gap.
	warm := namedStub()
	if _, err := Ingest(ctx, cfg, Hooks{}, Options{Remote: deps(warm)}); err != nil {
		t.Fatalf("warm run: %v", err)
	}
	wantSequence(t, "warm importer calls", warm.recorded(), []string{"meta", "releases"})

	second := origin.WriteAndCommit(map[string]string{"b.txt": "two\n"}, "second",
		testsupport.CommitOptions{Date: testsupport.At(1)})

	stub := namedStub()
	res, err := Ingest(ctx, cfg, Hooks{}, Options{Remote: deps(stub)})
	if err != nil {
		t.Fatalf("skipped run: %v", err)
	}
	wantSequence(t, "importer calls", stub.recorded(), []string{"releases"})
	if len(res.Data.Repos) != 1 {
		t.Fatalf("repos = %d, want 1", len(res.Data.Repos))
	}
	repo := res.Data.Repos[0]
	found := false
	for _, c := range repo.Commits {
		if c.Sha == second {
			found = true
		}
	}
	if !found {
		t.Errorf("the commit made between the two runs is missing; the mirror stopped updating (%d commits)",
			len(repo.Commits))
	}
}

func TestNoCacheAndFetchAlwaysOverrideSkipMetaRefetches(t *testing.T) {
	// Two independent overrides, asserted where the gate is actually decided. --no-cache is the
	// belt-and-braces one: it nils the cached record inside PrepareRemote AND rewrites fetch to
	// "always", which fails the mode gate here.
	ctx := context.Background()
	origin, root, cacheDir, resolved := seedFixtureRepo(t)

	warm := func(outDir string) *config.Resolved {
		t.Helper()
		cfg := skipMetaIngestConfig(root, cacheDir, outDir, resolved)
		cfg.Ingest.SkipMetaRefetches = true
		if _, err := Ingest(ctx, cfg, Hooks{}, Options{Remote: PrepareRemoteDeps{
			Env: Env{}, CreateImporter: alwaysStub(namedStub()), EnsureMirror: localMirror(origin.Dir),
		}}); err != nil {
			t.Fatalf("warm run: %v", err)
		}
		return cfg
	}

	cases := []struct {
		name    string
		fetch   string
		options Options
	}{
		{name: "--no-cache", fetch: "auto", options: Options{NoCache: true}},
		{name: "--refresh-meta", fetch: "auto", options: Options{RefreshMeta: true}},
		{name: `fetch: "always"`, fetch: "always"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := warm(remoteTempDir(t, "out"))
			cfg.Ingest.Fetch = tc.fetch
			stub := namedStub()
			options := tc.options
			options.Remote = PrepareRemoteDeps{
				Env: Env{}, CreateImporter: alwaysStub(stub), EnsureMirror: localMirror(origin.Dir),
			}
			if _, err := Ingest(ctx, cfg, Hooks{}, options); err != nil {
				t.Fatalf("ingest: %v", err)
			}
			wantSequence(t, "importer calls", stub.recorded(), []string{"meta", "releases"})
		})
	}
}

func TestSkipMetaRefetchesKeepsTheCooldownAndAgesItsOwnStamp(t *testing.T) {
	// The regression FetchStatus.Meta is a *bool to prevent, plus the stamp the console reads.
	//
	// WithinCooldown refuses to skip a repo whose last fetch was not fully successful. If the
	// skipped metadata half were recorded as a plain false rather than as "not attempted", every
	// repo would lose ingest.reuse.cooldownSeconds permanently the day this flag was switched on.
	ctx := context.Background()
	origin, root, cacheDir, resolved := seedFixtureRepo(t)
	outDir := remoteTempDir(t, "out")

	hour := int64(3600)
	cfg := skipMetaIngestConfig(root, cacheDir, outDir, resolved)
	cfg.Ingest.SkipMetaRefetches = true
	cfg.Ingest.Reuse = config.ReuseConfig{Enabled: true, MaxAgeMinutes: 2, CooldownSeconds: &hour}

	// The flag is on from the FIRST run: it is part of the hashed config, so turning it on between
	// runs would invalidate the run log and there would be no previous entry to carry forward.
	start := time.Date(2026, 6, 14, 9, 0, 0, 0, time.UTC)
	at := start
	run := func(stub *stubImporter) Result {
		t.Helper()
		res, err := Ingest(ctx, cfg, Hooks{}, Options{Remote: PrepareRemoteDeps{
			Env: Env{}, CreateImporter: alwaysStub(stub), EnsureMirror: localMirror(origin.Dir),
			Now: func() time.Time { return at },
		}})
		if err != nil {
			t.Fatalf("ingest at %s: %v", at, err)
		}
		return res
	}
	entry := func() RunLogEntry {
		t.Helper()
		log := ReadRunLog(cacheDir)
		if log == nil {
			t.Fatal("no run log was written")
		}
		got, ok := log.Remotes[resolved.AbsPath]
		if !ok {
			t.Fatalf("no run log entry for %s", resolved.AbsPath)
		}
		return got
	}

	// Nothing cached: a real fetch, recording both halves and stamping the metadata one.
	run(namedStub())
	first := entry()
	if !first.MetaOk || first.MetaFetchedAt != FormatRunLogTime(start) {
		t.Fatalf("the first run recorded %+v", first)
	}

	// Two hours later — past the freshness window and past the cooldown, so the run really runs.
	at = start.Add(2 * time.Hour)
	stub := namedStub()
	res := run(stub)
	wantSequence(t, "importer calls", stub.recorded(), []string{"releases"})
	if len(res.Remotes) != 1 || !res.Remotes[0].MetaSkipped {
		t.Fatalf("the skip was not reported to the console: %+v", res.Remotes)
	}
	if got := res.Remotes[0].MetaFetchedAt; got != FormatRunLogTime(start) {
		t.Errorf("reported metaFetchedAt = %q, want the first run's stamp", got)
	}
	second := entry()
	if !second.MetaOk {
		t.Error("a skipped metadata half was recorded as a FAILED one; the cooldown is now dead for this repo")
	}
	if second.FetchedAt != FormatRunLogTime(at) {
		t.Errorf("fetchedAt = %q, want the second run's instant — git did run", second.FetchedAt)
	}
	if second.MetaFetchedAt != FormatRunLogTime(start) {
		t.Errorf("metaFetchedAt = %q, want the first run's stamp carried forward", second.MetaFetchedAt)
	}

	// Ten minutes after that: inside the hour's cooldown, which only fires because metaOk survived.
	at = at.Add(10 * time.Minute)
	third := run(namedStub())
	if len(third.Remotes) != 1 || third.Remotes[0].Action != MirrorReused || !third.Remotes[0].Cooldown {
		t.Errorf("the cooldown did not fire: %+v", third.Remotes)
	}
}
