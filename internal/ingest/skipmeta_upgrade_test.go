package ingest

import (
	"context"
	"testing"
	"time"
)

// The upgrade path: turning ingest.skipMetaRefetches on must not disable the cooldown forever.
//
// The failure this pins is a two-step one, which is why no single-run test found it.
//
//  1. Adding the config key changes what ConfigHashFor hashes, so the first run after the change
//     discards the run log. That run has no previous entry to carry anything forward from.
//  2. The skip fires, and the first version of the feature recorded the metadata half as "not
//     attempted" — nil — expecting the caller to carry the previous answer forward. There was
//     none, so MetaOk took its zero value: false.
//
// From then on nothing could recover it. The skip keeps firing on every later run (the provider
// cache is still complete), so the status is never anything but "not attempted", so MetaOk is
// carried forward as false for good. WithinCooldown refuses to skip a repo whose metadata half
// last failed, so ingest.reuse.cooldownSeconds — a feature the user did not touch — silently
// stopped working the day they turned this one on.
//
// The fix is that a skip records SUCCESS, and that is honest rather than convenient: this skip
// fires only after providerMetaUsable has confirmed the cached record is complete, so the run
// knows the metadata is good. Backfill's nil is a different claim — it skips git while knowing
// nothing about the mirror.
//
// The FetchStatus below comes from a real PrepareRemote call rather than being written by hand.
// Asserting the merge against a status this test invented would only prove the test agrees with
// itself; the bug lived in what PrepareRemote reports.
func TestSkipMetaSurvivesTheUpgradePath(t *testing.T) {
	ctx := context.Background()
	origin, root, cacheDir, resolved := seedFixtureRepo(t)

	// A warm run, so there is a complete provider cache for the skip to find.
	if _, err := PrepareRemote(ctx, resolved, remoteTestConfig(root, cacheDir, "auto"), PrepareRemoteDeps{
		Env:            Env{},
		CreateImporter: alwaysStub(namedStub(stubRelease("v1.0.0"))),
		EnsureMirror:   localMirror(origin.Dir),
	}, PrepareRemoteOptions{}); err != nil {
		t.Fatalf("warm run: %v", err)
	}

	skipped := func() *FetchStatus {
		res, err := PrepareRemote(ctx, resolved, remoteTestConfig(root, cacheDir, "auto"), PrepareRemoteDeps{
			Env:            Env{},
			CreateImporter: alwaysStub(namedStub(stubRelease("v1.0.0"))),
			EnsureMirror:   localMirror(origin.Dir),
		}, PrepareRemoteOptions{SkipMetaRefetches: true})
		if err != nil {
			t.Fatalf("skipped run: %v", err)
		}
		if res.FetchStatus == nil || !res.FetchStatus.MetaSkipped {
			t.Fatalf("the skip did not fire, so this test is not exercising anything: %+v", res.FetchStatus)
		}
		return res.FetchStatus
	}

	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	cooldown := int64(3600)

	// Step 1: the run log was just discarded by the config-hash change, so this run starts with
	// nothing. Whatever it writes is what every later run inherits, because the skip keeps firing
	// and never produces a fresh answer to overwrite it with.
	first := mergeRunLogEntry(skipped(), nil, now)
	if !first.MetaOk {
		t.Fatalf("the first flag-on run recorded metaOk=false with no previous entry to carry.\n" +
			"Every later run inherits that false, because the skip keeps firing and never produces\n" +
			"a fresh answer — so ingest.reuse.cooldownSeconds is disabled for good. A skip fires\n" +
			"only when the cached record is complete, so it has to record success.")
	}
	if !first.GitOk {
		t.Error("git ran and succeeded; the run log should say so")
	}

	// Step 2: a later run inherits it, and the cooldown still works.
	second := mergeRunLogEntry(skipped(), &first, now.Add(10*time.Minute))
	if !second.MetaOk {
		t.Error("the second run lost metaOk, so the cooldown is disabled from here on")
	}
	if !WithinCooldown(&second, now.Add(20*time.Minute), &cooldown) {
		t.Errorf("WithinCooldown said no for an entry fetched ten minutes ago under a one-hour "+
			"cooldown: %+v", second)
	}

	// And the honest half: nothing was fetched, so no metadata timestamp is invented.
	if second.MetaFetchedAt != "" {
		t.Errorf("metaFetchedAt = %q, but FetchMeta never ran on either run", second.MetaFetchedAt)
	}
}

// mergeRunLogEntry is the run log merge from Ingest, applied to one repo.
//
// Kept in step with internal/ingest/ingest.go by hand, which is a duplication worth its cost: the
// bug was in what a nil previous entry does to these three fields, and reaching that through a
// whole ingest would bury the lines under a fixture.
func mergeRunLogEntry(status *FetchStatus, prev *RunLogEntry, now time.Time) RunLogEntry {
	entry := RunLogEntry{FetchedAt: now.UTC().Format(time.RFC3339)}
	hasPrev := prev != nil

	switch {
	case status != nil && status.Git != nil:
		entry.GitOk = *status.Git
	case hasPrev:
		entry.GitOk = prev.GitOk
	}
	switch {
	case status != nil && status.Meta != nil:
		entry.MetaOk = *status.Meta
	case hasPrev:
		entry.MetaOk = prev.MetaOk
	}
	switch {
	case status != nil && status.MetaFresh:
		entry.MetaFetchedAt = entry.FetchedAt
	case hasPrev:
		entry.MetaFetchedAt = prev.MetaFetchedAt
	}
	return entry
}
