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

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"frznforge/internal/config"
	"frznforge/internal/ingest"
	"frznforge/internal/model"
)

// ingestCmd runs the pipeline over the config in the current directory.
func ingestCmd(argv []string) error {
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
		fmt.Println("  --backfill-metadata: only repos with no cached provider metadata will be fetched, " +
			"and git is not touched at all.")
	}
	if args.NoCache {
		// Ingest forces a full fetch and reads no provider/scan caches for this run; fresh
		// results are still recorded (under the real config's hash) for the next ordinary run.
		fmt.Println("  --no-cache: fetching everything; provider/scan caches ignored for this run")
	}

	fmt.Printf("frznforge ingest → %s\n", cfg.OutDir)
	if len(cfg.Sources) == 0 {
		fmt.Println("  (no repos configured — writing an empty artifact)")
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
		fmt.Printf("  %d remote source(s) — cache %s (fetch: %s)\n", remoteCount, cfg.CacheDir, fetch)
	}

	started := time.Now()
	res, err := ingest.Ingest(context.Background(), cfg, ingest.Hooks{
		OnRepoStart: func(slug string) { fmt.Printf("  ▸ %s\n", slug) },
		OnRemote: func(s ingest.RemoteStatus) {
			if s.Cooldown {
				fmt.Printf("    ⚠️ %s: this repo is on cooldown\n", s.Slug)
				return
			}
			fmt.Printf("    ⇄ %s (%s: %s)\n", s.Slug, s.Provider, s.Action)
		},
		OnRepoDone: func(repo *model.Repo) {
			empty := ""
			if repo.Empty {
				empty = " (empty)"
			}
			fmt.Printf("    ✓ %s: %d commits, %d branches, %d tags, %d files%s\n",
				repo.Slug, repo.CommitCount, len(repo.Branches), len(repo.GitTags), len(repo.Files), empty)
		},
	}, ingest.Options{NoCache: args.NoCache, BackfillMetadata: args.BackfillMetadata})
	if err != nil {
		return err
	}

	if err := ingest.WriteArtifact(res.Data, res.Blobs, res.Archives, cfg.OutDir); err != nil {
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
		fmt.Fprintf(os.Stderr, "  ⚠ [%s]%s %s\n", w.Code, scope, w.Message)
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
		fmt.Printf("  backfill: %d filled, %d still missing, %d already had metadata (no network)\n",
			len(filled), len(stillMissing), len(replayed))
		if len(filled) > 0 {
			fmt.Printf("    ✓ filled: %s\n", strings.Join(filled, ", "))
		}
		if len(stillMissing) > 0 {
			fmt.Printf("    ⚠️ still missing: %s\n", strings.Join(stillMissing, ", "))
			fmt.Println("      Run it again later — the quota resets, and each run only spends it on these.")
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
		fmt.Printf("  ! remote sources: %s — see the warnings above; the build continued.\n", strings.Join(parts, "; "))
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
	fmt.Printf("done: %d repo(s), %s%d blob(s), %d archive(s), %d warning(s) in %dms\n",
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
