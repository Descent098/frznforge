package ingest

// The e2e suite's hermeticity, pinned in the language that owns it.
//
// tests/e2e/global-setup.ts builds its fixture artifact by pre-seeding a mirror and a provider
// response cache and then running `frznforge ingest --backfill-metadata`. It does that because
// the backfill replay branch in PrepareRemote returns before the importer is constructed and
// before EnsureMirror is called, so the run touches neither HTTP nor the network half of git —
// through production code, with no test seam opened in the binary.
//
// That property is INCIDENTAL to what --backfill-metadata is documented for, which is spending
// API quota only on repos that have none of their metadata yet. Nothing in the flag's own
// description promises "makes no network calls at all", so a reasonable future change to it
// could take the property away and the only symptom would be a mystery diff in a footer tooltip
// three files into a Playwright run. This test is the tripwire: it asserts the two seams
// PrepareRemote takes for exactly this purpose are never reached, and that the replay is a clean
// one — no remote-* warning, and the cached answers actually reaching the artifact.
//
// TestPrepareRemoteBackfillSpendsNothingOnGit covers the same branch one level down, with a stub
// importer. This one runs the whole Ingest the way the CLI does, because that is what the
// harness runs.

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"frznforge/internal/config"
	"frznforge/internal/ingest/testsupport"
	"frznforge/internal/model"
)

// refusingDoer fails any HTTP request and records that it was asked. Returning an error rather
// than calling t.Fatal keeps it safe to use from the ingest's worker goroutines.
type refusingDoer struct {
	mu    sync.Mutex
	calls []string
}

func (d *refusingDoer) Do(req *http.Request) (*http.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, req.Method+" "+req.URL.String())
	return nil, errors.New("the e2e fixture must not reach the network")
}

func (d *refusingDoer) recorded() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.calls...)
}

func TestBackfillReplayTouchesNothing(t *testing.T) {
	ctx := context.Background()

	// The "provider" is a local repo, cloned exactly the way the e2e harness clones it.
	origin := testsupport.Create(t, "widget", "main")
	origin.WriteAndCommit(map[string]string{"a.txt": "one\n"}, "first", testsupport.CommitOptions{Date: testsupport.At(0)})

	root := remoteTempDir(t, "root")
	cacheDir := remoteTempDir(t, "cache")
	source := giteaRemoteSource()
	resolved := resolvedRemote(cacheDir, source)

	// Seed the two files a successful previous run would have left behind: the bare mirror, and
	// the provider response cache beside it. This is the harness's step 4, in Go.
	if got := EnsureMirror(ctx, source, resolved.AbsPath, EnsureMirrorOptions{
		Fetch: "auto", CloneURL: filepath.ToSlash(origin.Dir),
	}); got.Action != MirrorCloned {
		t.Fatalf("seeding the mirror: action = %s, error = %v", got.Action, got.Error)
	}
	meta := stubProviderMeta()
	writeProviderCache(ProviderCachePathFor(resolved.AbsPath), providerCache{
		Version:  providerCacheVersion,
		Meta:     &meta,
		Releases: []model.Release{stubRelease("v1.0.0")},
	})

	cfg := &config.Resolved{Root: root, OutDir: root, CacheDir: cacheDir}
	cfg.Ingest.Fetch = "auto"
	cfg.Ingest.OutDir = root
	cfg.Ingest.CacheDir = cacheDir
	cfg.Ingest.MaxBlobBytes = 512 * 1024
	cfg.Ingest.TagTrees = 5
	// The default, and the one the fixture runs under: reuse on means the run log is read and
	// written, which is the state the replay branch has to be correct in.
	cfg.Ingest.Reuse.Enabled = true
	cfg.Ingest.Reuse.MaxAgeMinutes = 2
	cfg.Sources = []config.ResolvedSource{resolved}

	// CreateImporter is deliberately NOT stubbed. If the replay branch stopped firing, the real
	// registry would build a real Gitea importer over this Doer and the refusal below would be
	// the failure — which is a truer test of "no network" than a stub that answers politely.
	doer := &refusingDoer{}
	res, err := Ingest(ctx, cfg, Hooks{}, Options{
		BackfillMetadata: true,
		Remote:           PrepareRemoteDeps{Env: Env{}, HTTP: doer, Git: refuseGit(t)},
	})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}

	if got := doer.recorded(); len(got) != 0 {
		t.Errorf("the backfill replay made HTTP requests: %v", got)
	}
	if len(res.Remotes) != 1 || res.Remotes[0].Action != MirrorReused {
		t.Errorf("remotes = %+v, want one MirrorReused", res.Remotes)
	}
	// refuseGit fails the mirror rather than the test, so a call would surface here as a
	// remote-fetch-failed warning; the loop below is what catches it either way.
	for _, w := range res.Data.Warnings {
		if strings.HasPrefix(w.Code, "remote-") {
			t.Errorf("the backfill replay warned: [%s] %s", w.Code, w.Message)
		}
	}

	// A replay that produced nothing would satisfy every assertion above, so check that the
	// cached answers actually reached the artifact.
	if len(res.Data.Repos) != 1 {
		t.Fatalf("repos = %d, want 1", len(res.Data.Repos))
	}
	repo := res.Data.Repos[0]
	if repo.Description == nil || *repo.Description != *meta.Description {
		t.Errorf("description = %v, want the cached provider value %q", repo.Description, *meta.Description)
	}
	if len(repo.Releases) != 1 || repo.Releases[0].Tag != "v1.0.0" {
		t.Errorf("releases = %+v, want the one cached release", repo.Releases)
	}
}
