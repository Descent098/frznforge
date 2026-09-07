package ingest

// The pipeline entry point: the half of src/lib/ingest/index.ts that DRIVES the work — resolve
// each configured source (a local directory, or a provider repo mirror-cloned into the ingest
// cache), scan them `ingest.concurrency` at a time, and hand the finished scans to Assemble.
//
// Two properties this file exists to hold, both easy to lose in a port:
//
//   - Nothing derived from the clock reaches the artifact. Timestamps live only in the run log
//     under ingest.cacheDir, and the clock is injected (Options.Remote.Now) so a test never
//     depends on wall time.
//   - Processing order is not output order, but it is not free either. The pool may finish in
//     any order, so results are indexed by their position in the ORDER SLICE and handed to
//     Assemble in that slice's order — which is config order, except for the misses-first
//     partition below. That order decides which repo keeps a contested slug and where a
//     skipped source's warnings land in the site-level list, so it has to be reproduced
//     exactly rather than approximated with "whatever the goroutines produced".

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"frznforge/internal/config"
	"frznforge/internal/model"
	"frznforge/internal/timings"
)

// DegradedWarningCodes are the warning codes that mean a remote source was published from
// cached or missing provider data rather than a clean fetch. Shared by the run log (which
// records Fresh: false for them) and by ingest.failOnDegraded (which turns them into a non-zero
// exit).
var DegradedWarningCodes = map[string]bool{
	"remote-fetch-failed": true,
	"remote-rate-limited": true,
	"remote-auth-missing": true,
	"remote-cache-stale":  true,
}

// DegradedRepos lists the repo slugs that ended the run degraded, in artifact-warning order,
// de-duplicated.
func DegradedRepos(data model.ForgeData) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, w := range data.Warnings {
		if w.Repo == nil || !DegradedWarningCodes[w.Code] || seen[*w.Repo] {
			continue
		}
		seen[*w.Repo] = true
		out = append(out, *w.Repo)
	}
	return out
}

// RemoteStatus is what ingest did with one remote source. Reporting only — it never enters the
// artifact, which is why it may carry an action and a cooldown flag that no artifact field has.
type RemoteStatus struct {
	Slug     string
	Provider string
	Action   MirrorAction
	// Skipped is true when there was no mirror to scan and the repo was left out entirely.
	Skipped bool
	// Cooldown is true when ingest.reuse.cooldownSeconds is why the network was skipped, as
	// opposed to the freshness window, which produces the same "reused" action. The build prints
	// them differently so a deliberately fast run is never mistaken for a broken one.
	Cooldown bool
}

// Hooks are the progress callbacks the CLI prints from. All optional.
type Hooks struct {
	OnRepoStart func(slug string)
	OnRepoDone  func(repo *model.Repo)
	// OnRemote fires once per remote source, after its mirror has been resolved.
	OnRemote func(status RemoteStatus)
}

// Options are the per-run switches.
type Options struct {
	// Remote holds the injected seams for remote sources (importer factory, git runner, clock,
	// environment). The zero value means "the real ones".
	Remote PrepareRemoteDeps
	// NoCache is --no-cache: read nothing from the cross-run caches — no provider .meta.json
	// fallback, no freshness window, no cooldown, no same-commit probe, no scan-cache replay.
	// Fresh results are still WRITTEN, so the next ordinary run benefits from this one.
	NoCache bool
	// BackfillMetadata is --backfill-metadata: only repos with no cached provider metadata talk
	// to the network, and nothing talks to git. See PrepareRemoteOptions.BackfillMetadata.
	BackfillMetadata bool
	// Step is the timings step this scan's steps hang under, so `frznforge build` records the
	// scan and the render as two halves of one run. nil opens a top-level step instead, which is
	// what `frznforge ingest` and a test both want. internal/timings has no ambient parent by
	// design, so the parent has to arrive as a value.
	Step *timings.Step
}

// timingStep opens a step under parent, or a top-level one when there is no parent. Child on a
// nil *Step is the no-op used when timings are off, which is not the same thing as "this call
// was handed no parent".
func timingStep(parent *timings.Step, kind, name string) *timings.Step {
	if parent == nil {
		return timings.Start(kind, name)
	}
	return parent.Child(kind, name)
}

// Result is the artifact plus the byte stores, and the remote report the CLI prints.
type Result struct {
	AssembleResult
	// Remotes is one entry per remote source, in processing order.
	Remotes []RemoteStatus
}

// scanned is one source's trip through the pool: everything the assembly and the run log need,
// carried together so the results slice stays one value per input position.
type scanned struct {
	result         ScanResult
	remoteWarnings []model.Warning
	remote         *RemoteStatus
	// remoteAbsPath is the mirror path a run-log entry is keyed by; empty for a local source.
	remoteAbsPath string
	fetchStatus   *FetchStatus
	heads         map[string]string
	org           string
	err           error
}

// Ingest scans every configured repo and assembles the artifact.
//
// It is deterministic for the same repositories at the same commits: the only inputs that vary
// between runs — the caches under ingest.cacheDir — are proven-equivalent replays or are
// ignored, and every ordered output is sorted explicitly rather than left to map iteration.
func Ingest(ctx context.Context, cfg *config.Resolved, hooks Hooks, options Options) (Result, error) {
	span := timingStep(options.Step, "ingest.run", "scan")
	defer span.Done()

	opts, err := ScanOptionsFromConfig(cfg)
	if err != nil {
		return Result{}, err
	}

	// Cross-run reuse (ingest.reuse): READS are disabled by --no-cache, writes are not — a
	// --no-cache run is maximally fresh, and the next ordinary run should benefit from it.
	reuse := cfg.Ingest.Reuse
	reuseReads := reuse.Enabled && !options.NoCache
	now := options.Remote.Now
	if now == nil {
		now = time.Now
	}

	// Hashed over the CALLER's config, before the --no-cache fetch override below: the run log
	// this run writes must be readable by the next ordinary run of the same config, or
	// "--no-cache still records its fresh results" would be a dead letter.
	cfgHash, err := ConfigHashFor(cfg)
	if err != nil {
		return Result{}, fmt.Errorf("hash the resolved config for the ingest run log: %w", err)
	}
	var prevRemotes map[string]RunLogEntry
	havePrevRemotes := false
	if reuseReads {
		if prev := ReadRunLog(cfg.CacheDir); prev != nil && prev.ConfigHash == cfgHash {
			prevRemotes, havePrevRemotes = prev.Remotes, true
		}
	}

	// --no-cache forces a full fetch without leaking into the hashed config above.
	remoteCfg := cfg
	if options.NoCache && cfg.Ingest.Fetch != "always" {
		clone := *cfg
		clone.Ingest.Fetch = "always"
		remoteCfg = &clone
	}

	order := orderMissesFirst(cfg, options.NoCache)

	slog.Debug("ingest start", "repos", len(order), "concurrency", cfg.Ingest.Concurrency,
		"outDir", cfg.OutDir, "cacheDir", cfg.CacheDir)

	results := make([]scanned, len(order))
	runPool(ctx, len(order), cfg.Ingest.Concurrency, func(i int) {
		results[i] = ingestOne(ctx, order[i], cfg, remoteCfg, opts, hooks, options, span,
			reuse, reuseReads, prevRemotes, havePrevRemotes, now)
	})
	span.Add("repos", int64(len(order)))

	// A cancelled run leaves the tail of `results` at its zero value, which Assemble would read
	// as "skipped, no warning" and quietly drop. Silently publishing a smaller site is the one
	// failure mode this pipeline must not have, so cancellation is an error, not a short run.
	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("ingest cancelled before every repository was scanned: %w", err)
	}

	entries := make([]ScannedRepo, 0, len(results))
	remotes := []RemoteStatus{}
	type remoteRun struct {
		absPath     string
		action      MirrorAction
		skipped     bool
		warnings    []model.Warning
		fetchStatus *FetchStatus
		heads       map[string]string
	}
	remoteRuns := make([]remoteRun, 0, len(results))
	for _, r := range results {
		if r.err != nil {
			return Result{}, r.err
		}
		if r.remote != nil {
			remotes = append(remotes, *r.remote)
			if r.remoteAbsPath != "" {
				remoteRuns = append(remoteRuns, remoteRun{
					absPath: r.remoteAbsPath, action: r.remote.Action, skipped: r.remote.Skipped,
					warnings: r.remoteWarnings, fetchStatus: r.fetchStatus, heads: r.heads,
				})
			}
		}
		entries = append(entries, ScannedRepo{
			Result:         r.result,
			Org:            r.org,
			RemoteWarnings: r.remoteWarnings,
		})
	}

	assembleStep := span.Child("ingest.assemble", "artifact")
	assembled, err := Assemble(cfg, entries)
	assembleStep.Fail(err).DoneWith(timings.Counts{"repos": int64(len(assembled.Data.Repos))})
	if err != nil {
		return Result{}, err
	}

	// Record this run for the next one's freshness window. A window-skipped source keeps its
	// PREVIOUS stamp — the window must not extend itself, or a build every minute would never
	// refresh anything. A degraded fetch records Fresh: false, so it is always re-attempted.
	if reuse.Enabled {
		log := map[string]RunLogEntry{}
		for _, run := range remoteRuns {
			prev, hasPrev := prevRemotes[run.absPath]
			if run.action == MirrorReused {
				if hasPrev {
					log[run.absPath] = prev
				}
				continue
			}
			degraded := false
			for _, w := range run.warnings {
				if DegradedWarningCodes[w.Code] {
					degraded = true
					break
				}
			}
			entry := RunLogEntry{
				FetchedAt: FormatRunLogTime(now()),
				Fresh:     !run.skipped && !degraded,
			}
			// A nil git half means "not attempted this run" — only backfill mode does that — so
			// the previous run's answer carries forward rather than being overwritten with a
			// failure that never happened.
			switch {
			case run.fetchStatus != nil && run.fetchStatus.Git != nil:
				entry.GitOk = *run.fetchStatus.Git
			case hasPrev:
				entry.GitOk = prev.GitOk
			}
			if run.fetchStatus != nil {
				entry.MetaOk = run.fetchStatus.Meta
			}
			// Heads describe the mirror as it now stands. When they could not be read, keep the
			// previous baseline rather than erasing it: a forgotten baseline costs one un-skipped
			// fetch, whereas a WRONG one would skip a fetch that was needed.
			switch {
			case run.heads != nil:
				entry.Heads = run.heads
			case hasPrev:
				entry.Heads = prev.Heads
			}
			log[run.absPath] = entry
		}
		WriteRunLog(cfg.CacheDir, cfgHash, log)
	}

	return Result{AssembleResult: assembled, Remotes: remotes}, nil
}

// ingestOne resolves and scans a single source. Every failure it can foresee becomes a warning
// carried in the result; only a genuinely unexpected one (a git that will not run at all) is
// returned as an error, because that is not a property of this repository and retrying the rest
// of the config would just produce the same failure N more times.
func ingestOne(
	ctx context.Context,
	src config.ResolvedSource,
	cfg, remoteCfg *config.Resolved,
	opts ScanOptions,
	hooks Hooks,
	options Options,
	parent *timings.Step,
	reuse config.ReuseConfig,
	reuseReads bool,
	prevRemotes map[string]RunLogEntry,
	havePrevRemotes bool,
	now func() time.Time,
) scanned {
	// Must match what PrepareRemote and ScanRepo will settle on, or the warnings raised before
	// the scan get stamped with a slug no repo has. For a remote source that means the configured
	// name, never the mirror directory (which carries the cache-key digest).
	slug := PreScanSlug(src)
	if hooks.OnRepoStart != nil {
		hooks.OnRepoStart(slug)
	}
	// The progress line the user sees says only that this repository started. This says the same
	// thing with a timestamp and a path, and is followed by a matching "repo done" — which is
	// what turns "it stopped after printing a name" into "it stopped scanning THIS, at THIS
	// point, N seconds in".
	repoStarted := time.Now()
	slog.Debug("repo start", "slug", slug, "path", src.AbsPath, "remote", src.IsRemote())
	// The repo's own span. Its two children — the fetch and the scan — are what answer "was that
	// minute the network or the disk", which is the first question anyone asks of a slow ingest.
	repoStep := parent.Child("ingest.repo", slug)
	defer repoStep.Done()
	defer func() {
		slog.Debug("repo done", "slug", slug, "ms", time.Since(repoStarted).Milliseconds())
	}()

	out := scanned{
		// Carried alongside the scan result so organization membership resolves against the FINAL
		// slug (after collision renaming) without putting a site-config concern on model.Repo.
		org:            src.Org,
		remoteWarnings: []model.Warning{},
	}

	// Hosting (schema v7): the branches this repo must serve, matched on the pre-collision slug.
	// The scan has to know before any renaming; a collision loser's forced trees are harmless,
	// and the final binding happens post-rename in ResolveHosting.
	var hosted []*string
	for _, site := range cfg.Hosting.Sites {
		if site.Repo != slug {
			continue
		}
		if site.Branch == "" {
			hosted = append(hosted, nil)
			continue
		}
		branch := site.Branch
		hosted = append(hosted, &branch)
	}

	source := ScanSource{
		AbsPath:        src.AbsPath,
		Slug:           src.Slug,
		Overrides:      src.Overrides,
		HostedRequests: hosted,
	}

	skipRemote := func(message string) scanned {
		repo := slug
		out.result = ScanResult{
			Skipped: true,
			Warning: &model.Warning{Code: "remote-fetch-failed", Repo: &repo, Message: message},
		}
		return out
	}

	if src.IsRemote() {
		out.remoteAbsPath = src.AbsPath
		var prevEntry *RunLogEntry
		if entry, ok := prevRemotes[src.AbsPath]; ok {
			prevEntry = &entry
		}
		// Precedence, cheapest decision first: the fetch mode has already been settled by the
		// caller, then the freshness window (minutes, on by default), then the cooldown (hours,
		// opt-in). Both time skips mean the same thing to PrepareRemote — "use the cache, touch
		// no network" — so they share one flag and differ only in what gets reported.
		//
		// Both apply only under fetch: "auto". "always" is an explicit ask to fetch, and "never"
		// must keep emitting its stale-cache warnings: a window-skip suppressing them would make
		// the same commits produce different artifact bytes depending on timing.
		auto := remoteCfg.Ingest.Fetch == "auto" && havePrevRemotes
		inWindow := auto && WithinFreshWindow(prevEntry, now(), reuse.MaxAgeMinutes)
		inCooldown := auto && !inWindow && WithinCooldown(prevEntry, now(), reuse.CooldownSeconds)

		prepOpts := PrepareRemoteOptions{
			SkipFetch:    inWindow || inCooldown,
			NoCacheReads: options.NoCache,
			// The same-commit probe is only meaningful with a baseline from a previous run, and
			// only when reuse reads are on at all (--no-cache means fetch everything).
			SkipUnchanged:    reuseReads && reuse.SkipUnchanged,
			BackfillMetadata: options.BackfillMetadata,
		}
		if reuseReads && prevEntry != nil {
			prepOpts.KnownHeads = prevEntry.Heads
		}

		// One unreachable forge must never take the build down: PrepareRemote turns every failure
		// it anticipates into a warning, and anything left is caught here as one too.
		//
		// This step spans BOTH halves of a fetch — the provider API calls and the mirror clone or
		// update — because that is the wall time a user waits, and the individual requests inside
		// it are already named in the run log.
		fetchStep := repoStep.Child("ingest.fetch", slug)
		prepared, err := PrepareRemote(ctx, src, remoteCfg, options.Remote, prepOpts)
		if prepOpts.SkipFetch {
			// So a 0 ms fetch reads as "the freshness window said not to" rather than as a
			// suspiciously fast network.
			fetchStep.Add("skipped", 1)
		}
		fetchStep.Fail(err).Done()
		if err != nil {
			out.remote = &RemoteStatus{Slug: slug, Provider: src.Type, Action: MirrorMissing, Skipped: true}
			failed := false
			out.fetchStatus = &FetchStatus{Git: &failed, Meta: false}
			if hooks.OnRemote != nil {
				hooks.OnRemote(*out.remote)
			}
			return skipRemote(fmt.Sprintf("%s import failed (%s); repo skipped", src.Type, err))
		}

		// Remote warnings are raised with Repo nil; the slug is stamped on here so they read the
		// same as scanner warnings and follow the repo through a slug-collision rename.
		out.remoteWarnings = make([]model.Warning, 0, len(prepared.Warnings))
		for _, w := range prepared.Warnings {
			repo := slug
			w.Repo = &repo
			out.remoteWarnings = append(out.remoteWarnings, w)
		}
		out.remote = &RemoteStatus{
			Slug: slug, Provider: src.Type, Action: prepared.Action,
			Skipped: !prepared.Ready, Cooldown: inCooldown,
		}
		out.fetchStatus = prepared.FetchStatus
		if hooks.OnRemote != nil {
			hooks.OnRemote(*out.remote)
		}
		// The mirror's refs as they now stand — the baseline the next run's same-hash probe
		// compares a `git ls-remote` against. Read only when the run log will be written.
		if reuse.Enabled && prepared.Ready {
			out.heads = ReadRefHeads(ctx, prepared.ScanSource.AbsPath)
		}
		if !prepared.Ready {
			return skipRemote(fmt.Sprintf("no usable mirror for %s repo '%s'; repo skipped", src.Type, slug))
		}
		source = prepared.ScanSource
		source.HostedRequests = hosted
	}

	// Scan cache: replay the recorded result when every input is unchanged. The digest covers the
	// refs, HEAD, the scan source (provider metadata included) and the options; a hit rehydrates
	// blob and archive bytes from outDir so WriteArtifact's mirror-and-prune pass sees the full
	// maps. Anything missing is a quiet fallback to a real scan. The entry is written before
	// assembly mutates the repo (slug renames, remote-warning stamping), so what is stored is
	// exactly a fresh scan.
	digest := ""
	cacheFile := ""
	if reuse.Enabled {
		d, err := ScanInputDigest(ctx, source, opts)
		if err != nil {
			out.err = fmt.Errorf("digest the scan inputs for %s: %w", source.AbsPath, err)
			return out
		}
		if d != "" {
			digest = d
			cacheFile = ScanCachePathFor(cfg.CacheDir, source.AbsPath)
		}
	}

	scanStep := repoStep.Child("ingest.scan", slug)
	replayed := false
	if digest != "" && reuseReads {
		if entry := ReadScanCache(cacheFile); entry != nil && entry.InputDigest == digest {
			if r, ok := RehydrateScan(entry, cfg.OutDir); ok {
				out.result, replayed = r, true
			}
		}
	}
	if !replayed {
		r, err := ScanRepo(ctx, source, opts)
		if err != nil {
			out.err = fmt.Errorf("scan %s: %w", source.AbsPath, err)
			scanStep.Fail(out.err).Done()
			return out
		}
		out.result = r
		if digest != "" && r.Repo != nil {
			WriteScanCache(cacheFile, digest, r)
		}
	} else {
		// A replay and a real scan are the same step with wildly different costs. Marking the
		// replay is what stops a cached run's numbers from being read as a fast scanner.
		scanStep.Add("replayed", 1)
	}
	scanStep.DoneWith(scanCounts(out.result))
	if out.result.Repo != nil && hooks.OnRepoDone != nil {
		hooks.OnRepoDone(out.result.Repo)
	}
	return out
}

// scanCounts is the size of what a scan produced, for the timings record. A duration alone
// cannot tell a slow scanner from a big repository; these are what make the number comparable
// between two repos and between two runs of the same one.
func scanCounts(r ScanResult) timings.Counts {
	c := timings.Counts{"blobs": int64(len(r.Blobs)), "archives": int64(len(r.Archives))}
	if r.Repo != nil {
		c["files"] = int64(len(r.Repo.Files))
		c["commits"] = r.Repo.CommitCount
		c["branches"] = int64(len(r.Repo.Branches))
	}
	return c
}

// orderMissesFirst is cfg.Sources, stably partitioned so remote sources with no cached provider
// metadata run first. Local sources never touch the network and keep their place in the tail.
//
// A remote source with no cached metadata is either new or a failure from a previous run, and it
// is the one most likely to need the network. Giving those the head of the queue means a run
// that hits a rate limit spends its budget on the repos that have nothing to fall back on rather
// than on repos that would have been fine serving cache. In backfill mode they are the ONLY
// sources that will use the network, so this is what decides where a limited quota goes.
//
// --no-cache reads nothing from the provider cache, so with it every remote is a "miss" and the
// original order stands. The partition is returned unchanged when nothing missed, which is why a
// local-only config always runs in config order.
func orderMissesFirst(cfg *config.Resolved, noCache bool) []config.ResolvedSource {
	if noCache {
		return cfg.Sources
	}
	misses := []config.ResolvedSource{}
	rest := []config.ResolvedSource{}
	for _, src := range cfg.Sources {
		if !src.IsRemote() {
			rest = append(rest, src)
			continue
		}
		if st, err := os.Stat(ProviderCachePathFor(src.AbsPath)); err == nil && st.Mode().IsRegular() {
			rest = append(rest, src)
		} else {
			misses = append(misses, src)
		}
	}
	if len(misses) == 0 {
		return cfg.Sources
	}
	return append(misses, rest...)
}

// runPool calls fn for every index in [0, n), at most limit at a time.
//
// Results go into a caller-owned slice indexed by position rather than being collected off a
// channel: which repo keeps a contested slug must not depend on which goroutine finished first.
func runPool(ctx context.Context, n, limit int, fn func(i int)) {
	if n == 0 {
		return
	}
	if limit < 1 {
		limit = 1
	}
	if limit > n {
		limit = n
	}
	next := 0
	var mu sync.Mutex
	var wg sync.WaitGroup
	// Workers are counted in and out so a scan that stops names how many were still running when
	// it did. One repository parked in a clone with the other twenty finished is a very different
	// picture from twenty parked together, and neither is visible from the progress lines.
	live := 0
	for w := 0; w < limit; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu.Lock()
			live++
			started := live
			mu.Unlock()
			slog.Debug("ingest worker start", "pool", "ingest.repos", "worker", w,
				"live", started, "limit", limit)
			defer func() {
				mu.Lock()
				live--
				remaining := live
				mu.Unlock()
				slog.Debug("ingest worker done", "pool", "ingest.repos", "worker", w,
					"live", remaining, "limit", limit)
			}()
			for {
				mu.Lock()
				i := next
				next++
				mu.Unlock()
				if i >= n || ctx.Err() != nil {
					return
				}
				fn(i)
			}
		}()
	}
	wg.Wait()
}

// EnsureOutDir creates the artifact directory, reporting the failure in the terms the user can
// act on: an unwritable outDir is one of the few things that stops the whole build.
func EnsureOutDir(outDir string) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("cannot create the ingest output directory %s: %w — check ingest.outDir and its permissions",
			filepath.Clean(outDir), err)
	}
	return nil
}
