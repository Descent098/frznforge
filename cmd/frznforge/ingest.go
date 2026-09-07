package main

// `frznforge ingest` — the Go half of what `scripts/ingest.ts` does: read the config, scan every
// configured repository, assemble the artifact and write it (plus its blob and archive stores)
// to ingest.outDir.
//
// The acceptance bar for this command is byte identity: for the same repositories at the same
// commits it must produce exactly the forge.json that `npm run ingest` produces. That is why
// nothing here consults the clock, the locale or any environment beyond the documented
// FRZNFORGE_* overrides — every ordering decision that reaches the file is made in
// internal/ingest, and this file only prints.
//
// Exit code is 0 even when warnings are emitted: an empty repo, an unreachable forge, a missing
// path are all reported but never fail the build. It is 1 only for a hard failure (bad config,
// unwritable outDir, git missing) or for ingest.failOnDegraded — and even then the artifact has
// already been written, because a partial artifact plus a red build is easier to debug than
// neither.
//
// runIngest is split out from the command because `frznforge build` runs the same scan in
// process. scripts/build.ts had to spawn `tsx scripts/ingest.ts` and forward signals to it; a
// function call needs neither, and the two paths cannot print different things.

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"frznforge/internal/config"
	"frznforge/internal/ingest"
	"frznforge/internal/model"
	"frznforge/internal/timings"
)

// stepUnder opens a step under parent, or a top-level one when the run has none — the CLI's copy
// of the same two-line rule internal/build and internal/ingest each need, because Child on a nil
// *Step is the no-op for "timings are off" and not for "there is no parent".
func stepUnder(parent *timings.Step, kind, name string) *timings.Step {
	if parent == nil {
		return timings.Start(kind, name)
	}
	return parent.Child(kind, name)
}

// ingestCmd runs the pipeline over the config in the current directory.
func ingestCmd(argv []string, io *Io, step *timings.Step) error {
	root := "."
	outDir := ""
	// --root and --out are this command's own; everything else goes to the ported flag parser,
	// which owns the two documented ingest flags and the rule that they are opposites.
	rest := make([]string, 0, len(argv))
	for _, a := range argv {
		switch {
		case strings.HasPrefix(a, "--out="):
			outDir = strings.TrimPrefix(a, "--out=")
		case strings.HasPrefix(a, "--root="):
			root = strings.TrimPrefix(a, "--root=")
		default:
			rest = append(rest, a)
		}
	}
	args, err := ingest.ParseIngestArgs(rest)
	if err != nil {
		return err
	}
	return runIngest(root, outDir, args, io, step)
}

// runIngest scans the repositories configured under root and writes the artifact.
//
// outDir overrides ingest.outDir when it is non-empty; `frznforge build` passes "" because it
// has no artifact-directory flag of its own.
//
// step is the run's timings step, so `build` records its scan under the same run as its render.
func runIngest(root, outDir string, args ingest.IngestArgs, io *Io, step *timings.Step) error {
	cfg, err := config.Load(root)
	if err != nil {
		return err
	}
	if outDir != "" {
		cfg.OutDir = outDir
	}
	if err := ingest.EnsureOutDir(cfg.OutDir); err != nil {
		return err
	}

	if args.BackfillMetadata {
		io.log("  --backfill-metadata: only repos with no cached provider metadata will be fetched, " +
			"and git is not touched at all.")
	}
	if args.NoCache {
		// Ingest forces a full fetch and reads no provider/scan caches for this run; fresh
		// results are still recorded (under the real config's hash) for the next ordinary run.
		io.log("  --no-cache: fetching everything; provider/scan caches ignored for this run")
	}

	io.logf("frznforge ingest → %s", cfg.OutDir)
	if len(cfg.Sources) == 0 {
		io.log("  (no repos configured — writing an empty artifact)")
	}
	remoteCount := 0
	for _, src := range cfg.Sources {
		if src.IsRemote() {
			remoteCount++
		}
	}
	if remoteCount > 0 {
		fetch := cfg.Ingest.Fetch
		if args.NoCache {
			fetch = "always [--no-cache]"
		}
		io.logf("  %d remote source(s) — cache %s (fetch: %s)", remoteCount, cfg.CacheDir, fetch)
	}

	started := time.Now()
	res, err := ingest.Ingest(context.Background(), cfg, ingest.Hooks{
		OnRepoStart: func(slug string) { io.logf("  ▸ %s", slug) },
		OnRemote: func(s ingest.RemoteStatus) {
			if s.Cooldown {
				io.logf("    ⚠️ %s: this repo is on cooldown", s.Slug)
				return
			}
			io.logf("    ⇄ %s (%s: %s)", s.Slug, s.Provider, s.Action)
		},
		OnRepoDone: func(repo *model.Repo) {
			empty := ""
			if repo.Empty {
				empty = " (empty)"
			}
			io.logf("    ✓ %s: %d commits, %d branches, %d tags, %d files%s",
				repo.Slug, repo.CommitCount, len(repo.Branches), len(repo.GitTags), len(repo.Files), empty)
		},
	}, ingest.Options{NoCache: args.NoCache, BackfillMetadata: args.BackfillMetadata, Step: step})
	if err != nil {
		return err
	}

	// Timed separately from the scan: writing 25 MB of JSON plus the blob and archive mirrors is
	// a disk cost with nothing to do with git or the network, and folding it into the scan would
	// blame the wrong half when a slow disk is the answer.
	write := stepUnder(step, "ingest.write", "artifact")
	writeErr := ingest.WriteArtifact(res.Data, res.Blobs, res.Archives, cfg.OutDir)
	write.Fail(writeErr).DoneWith(timings.Counts{
		"blobs": int64(len(res.Blobs)), "archives": int64(len(res.Archives)),
	})
	if err := writeErr; err != nil {
		// WriteArtifact validates before it writes, so this is an ingest bug rather than a
		// warning — and a bare validation message says nothing about which repo produced the bad
		// value, so name them.
		slugs := make([]string, len(res.Data.Repos))
		for i, r := range res.Data.Repos {
			slugs[i] = r.Slug
		}
		list := strings.Join(slugs, ", ")
		if list == "" {
			list = "(none)"
		}
		return fmt.Errorf("the artifact failed validation and was not written.\n  repos in this run: %s\n  %w", list, err)
	}

	for _, w := range res.Data.Warnings {
		scope := ""
		if w.Repo != nil {
			scope = " " + *w.Repo + ":"
		}
		io.errf("  ⚠ [%s]%s %s", w.Code, scope, w.Message)
	}

	// Repos that ended the run on cached-or-missing provider data. Read twice below.
	degraded := ingest.DegradedRepos(res.Data)

	// In backfill mode, say plainly which repos actually used the quota and which replayed, so a
	// run that filled nothing is not mistaken for a run that had nothing to fill.
	if args.BackfillMetadata {
		var replayed, stillMissing, filled []string
		for _, r := range res.Remotes {
			if r.Action == ingest.MirrorReused {
				replayed = append(replayed, r.Slug)
				continue
			}
			switch {
			case slices.Contains(degraded, r.Slug):
				stillMissing = append(stillMissing, r.Slug)
			case !r.Skipped:
				filled = append(filled, r.Slug)
			}
		}
		io.logf("  backfill: %d filled, %d still missing, %d already had metadata (no network)",
			len(filled), len(stillMissing), len(replayed))
		if len(filled) > 0 {
			io.logf("    ✓ filled: %s", strings.Join(filled, ", "))
		}
		if len(stillMissing) > 0 {
			io.logf("    ⚠️ still missing: %s", strings.Join(stillMissing, ", "))
			io.log("      Run it again later — the quota resets, and each run only spends it on these.")
		}
	}

	// Remote trouble is a warning, never an error — say so plainly so a stale build is obvious.
	//
	// In backfill mode "cached" is not trouble: git is deliberately never fetched, so every repo
	// reports it. Saying "13 served from cache" about the 13 repos whose metadata was just
	// successfully fetched is exactly backwards, so that line is left to the backfill summary.
	var cached, skipped []string
	for _, r := range res.Remotes {
		if r.Action == ingest.MirrorCached && !args.BackfillMetadata {
			cached = append(cached, r.Slug)
		}
		if r.Skipped {
			skipped = append(skipped, r.Slug)
		}
	}
	if len(cached) > 0 || len(skipped) > 0 {
		parts := []string{}
		if len(cached) > 0 {
			parts = append(parts, fmt.Sprintf("%d served from cache (%s)", len(cached), strings.Join(cached, ", ")))
		}
		if len(skipped) > 0 {
			parts = append(parts, fmt.Sprintf("%d skipped (%s)", len(skipped), strings.Join(skipped, ", ")))
		}
		io.logf("  ! remote sources: %s — see the warnings above; the build continued.", strings.Join(parts, "; "))
	}

	// Notes and organizations are reported only when there are any: most sites configure neither,
	// and a permanent "0 note(s), 0 organization(s)" would just be noise in every run.
	extras := []string{}
	if n := len(res.Data.Notes); n > 0 {
		extras = append(extras, fmt.Sprintf("%d note(s)", n))
	}
	if n := len(res.Data.Organizations); n > 0 {
		extras = append(extras, fmt.Sprintf("%d organization(s)", n))
	}
	prefix := ""
	if len(extras) > 0 {
		prefix = strings.Join(extras, ", ") + ", "
	}
	io.logf("done: %d repo(s), %s%d blob(s), %d archive(s), %d warning(s) in %dms",
		len(res.Data.Repos), prefix, len(res.Blobs), len(res.Archives), len(res.Data.Warnings),
		time.Since(started).Milliseconds())

	// ingest.failOnDegraded: the artifact is already written and the warnings are already
	// printed, so this only decides the exit code. Opt-in, for CI that would rather fail than
	// publish stale metadata after a rate limit.
	if cfg.Ingest.FailOnDegraded && len(degraded) > 0 {
		return fmt.Errorf("%d source(s) ended the run degraded (%s); failing because ingest.failOnDegraded is set. "+
			"The artifact was still written", len(degraded), strings.Join(degraded, ", "))
	}
	return nil
}
