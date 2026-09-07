package build

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"frznforge/internal/timings"
)

// Rendering in parallel.
//
// # Where the work actually is
//
// The TODO asks for ingest and render to overlap — build a repo's pages while other repos are
// still being fetched. Measured, that is the smaller half of the prize and the half with all the
// correctness hazards: on the four-repo benchmark corpus ingest is 7.1 s against a 204 s render.
//
// Parallelising per REPO is the obvious next answer, and it is also not enough. It is bounded by
// the repository count, and the count is often one: this project's own site was for a long time a
// single repository whose tree/blob/raw multiplier is 85% of its files. Measured at one repo,
// per-repo parallelism does nothing at all (3.74 s at one worker, 3.92 s at thirty-two — slightly
// worse, which is the pool overhead).
//
// So the pool is used at BOTH levels: repositories run concurrently, and inside a repository the
// tree/blob/raw families — the multiplier that is 87-90% of every build measured — run
// concurrently too. A one-repo site gets the same benefit as a fifty-repo one.
//
// # One bound, shared — and why that nearly killed it
//
// Nesting two pools would multiply: N repos × N pages each is N² goroutines fighting over the
// same cores. There is instead ONE semaphore for the whole build, shared by every Builder copy.
//
// The first version of this file had every unit take a slot and then WAIT for its children to
// take slots of their own, which deadlocks the moment the outer level alone can fill the
// semaphore. It shipped, and it hung a 73-repository build on a 32-core machine with thousands
// of goroutines parked in `chan send`: 31 repositories held every slot, each blocked in
// `wg.Wait()` for page goroutines that could never acquire one. It needed more repositories than
// workers, so a single-repository site never saw it.
//
// Two rules keep that from coming back, and both are load-bearing:
//
//  1. **The caller works.** Whoever calls eachConcurrently runs units itself rather than only
//     waiting on helpers. Progress therefore never depends on acquiring anything: even with the
//     semaphore completely full, the calling goroutine — which already holds a slot, or is the
//     main one — drains the queue on its own. This is what makes the nesting safe at any depth.
//  2. **Helpers never block on the semaphore.** They try to take a slot and give up immediately
//     if there is none, because an extra helper is an optimisation and the caller is the
//     guarantee. A blocking acquire here is precisely the bug above.
//
// It also bounds goroutines to the worker count instead of the unit count. The old shape spawned
// one goroutine per page up front — 14,000 of them on the build that hung, each with its own
// Builder copy, before a single page was rendered.
//
// # Why the output is still deterministic
//
// Every page is written to a path derived from the artifact, and no two units write the same
// path. Concurrency changes the ORDER of writes and nothing about their content or their names.
// That is why `Workers: 1` and the default must produce byte-identical trees, and why
// TestSerialAndParallelAgree exists to prove it rather than assume it.
//
// The two things that would break it are handled here rather than in every caller: the shared
// counters, and verbose output — which is buffered per unit and printed in artifact order, so
// `-v` stays readable and reproducible instead of interleaved.

// # Saying so when it happens again
//
// The build that hung produced no evidence at all: no error, no last line, nothing to tell a
// reader that the pool was the place it stopped. So every slot this file takes and returns is
// logged at debug, and each parallel section is logged before it starts as well as after it
// finishes — the same before-AND-after shape ingest uses for git, and for the same reason. A
// section that never returns leaves a "parallel section start" with no matching "done", and the
// occupancy on the acquire records says whether the semaphore was the reason.
//
// It has to be free when nothing is listening and nearly free when something is. Two properties
// make that true: the Enabled check is hoisted out of the loop, so the disabled case costs one
// atomic load per section rather than one per acquire; and the records are bounded by the WORKER
// count, not the unit count — at most one acquire and one release per helper per section, never
// one per page. Nothing here formats a string unless a sink asked for it.

// sectionSeq numbers each parallel section, so an acquire, its release and the section's own two
// records can be paired in a log holding thousands of them written by dozens of goroutines.
var sectionSeq atomic.Int64

// semName is the semaphore's identity in the log. There is exactly one, and naming it is what
// lets a future second pool be told apart from this one in the same file.
const semName = "build.workers"

// eachConcurrently runs fn for each index in [0,n), up to the build's worker bound.
//
// Each worker gets its OWN Builder view: it shares the renderer (html/template is safe for
// concurrent execution) and the artifact (read-only by now), and keeps its own counters and log
// buffer. The only synchronisation left is the index counter and merging the results, in index
// order.
//
// describe names a failing unit — a repo slug, a ref name — so an error says which one.
func (b *Builder) eachConcurrently(n int, describe func(i int) string, fn func(*Builder, int) error) error {
	if n == 0 {
		return nil
	}
	if b.sem == nil || cap(b.sem) <= 1 || n == 1 {
		// A unit may re-parent this Builder's timing step (emitRepos does, so a repo's pages nest
		// under the repo). Restoring it per unit is what stops unit i+1 from being recorded as a
		// child of unit i — the serial path reuses one Builder, unlike the pool below, which hands
		// each worker a copy.
		parent := b.step
		for i := 0; i < n; i++ {
			b.step = parent
			if err := fn(b, i); err != nil {
				b.step = parent
				return fmt.Errorf("%s: %w", describe(i), err)
			}
		}
		b.step = parent
		return nil
	}

	var (
		mu    sync.Mutex
		errs  []string
		wg    sync.WaitGroup
		next  atomic.Int64
		logs  = make([][]string, n)
		count = make([]int, n)
		bytes = make([]int64, n)
	)

	// One unit. The local Builder is reset per unit so the counters and log buffer recorded
	// against index i describe THAT unit and not everything the worker did before it.
	//
	// step is reset for the same reason and is easy to miss: a worker reuses ONE Builder copy for
	// every unit it draws, so a unit that re-parented it (emitRepos does) would otherwise leave
	// the next repo this worker picks up recorded as a child of the previous one.
	runOne := func(local *Builder, i int) {
		local.written, local.bytes, local.log = 0, 0, nil
		local.step = b.step
		if err := fn(local, i); err != nil {
			mu.Lock()
			errs = append(errs, fmt.Sprintf("%s: %v", describe(i), err))
			mu.Unlock()
			return
		}
		logs[i], count[i], bytes[i] = local.log, local.written, local.bytes
	}

	// Every participant runs the same loop: take the next index, do it, repeat until they are
	// gone. Nothing is assigned up front, so a worker that draws a slow unit does not strand the
	// units behind it.
	drain := func() {
		local := *b
		for {
			i := int(next.Add(1)) - 1
			if i >= n {
				return
			}
			runOne(&local, i)
		}
	}

	// Hoisted out of the acquire loop on purpose: with no sink this is one atomic load for the
	// whole section, and with one it is the only branch the hot path pays.
	trace := slog.Default().Enabled(context.Background(), slog.LevelDebug)
	section := ""
	if trace {
		section = strconv.FormatInt(sectionSeq.Add(1), 10)
		slog.Debug("parallel section start", "section", section, "units", n,
			"first", describe(0), "held", len(b.sem), "cap", cap(b.sem))
	}

	started := time.Now()
	helpers := 0
	// At most one helper per free slot, and never more than there is work for — the caller takes
	// one unit itself, so n-1 helpers is the most that can ever be useful.
	for h := 0; h < cap(b.sem) && h < n-1; h++ {
		select {
		case b.sem <- struct{}{}:
			helpers++
			id := helpers
			if trace {
				slog.Debug("sem acquired", "sem", semName, "section", section, "helper", id,
					"held", len(b.sem), "cap", cap(b.sem))
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() {
					<-b.sem
					if trace {
						slog.Debug("sem released", "sem", semName, "section", section, "helper", id,
							"held", len(b.sem), "cap", cap(b.sem))
					}
				}()
				drain()
			}()
		default:
			// The semaphore is full. That is not a reason to wait — it is a reason to stop
			// adding helpers and let the caller get on with it.
			if trace {
				// The record that would have named the old deadlock. "full" here is normal and
				// expected; "full" with no matching section done afterwards is not.
				slog.Debug("sem full; the caller renders this section alone",
					"sem", semName, "section", section, "helpers", helpers,
					"held", len(b.sem), "cap", cap(b.sem), "units", n)
			}
			h = cap(b.sem)
		}
	}

	// The caller is a worker too, and this line is the deadlock fix: if `helpers` came out zero
	// because every slot was taken, the work still happens, here, now.
	drain()
	wg.Wait()

	if trace {
		slog.Debug("parallel section done", "section", section, "units", n, "helpers", helpers,
			"ms", time.Since(started).Milliseconds(), "failed", len(errs))
	}

	for i := 0; i < n; i++ {
		b.written += count[i]
		b.bytes += bytes[i]
		b.log = append(b.log, logs[i]...)
	}
	if len(errs) > 0 {
		// Sorted so a build failing on several units reports them in a stable order rather than
		// in whichever order the goroutines happened to finish.
		sort.Strings(errs)
		return fmt.Errorf("%d of %d units failed:\n  %s", len(errs), n, joinLines(errs))
	}
	return nil
}

// EachRoute is the helper a page family calls to render many independent pages concurrently.
//
// "Independent" is the whole precondition: each call must write its own paths and share nothing
// mutable. That holds for the tree, blob and raw families by construction — one page per tree
// entry per ref — which is exactly why those are the ones worth parallelising.
func (b *Builder) EachRoute(n int, describe func(i int) string, fn func(*Builder, int) error) error {
	return b.eachConcurrently(n, describe, fn)
}

// emitRepos renders every repository, sharing the build's worker bound.
func (b *Builder) emitRepos() error {
	repos := b.Data.Repos
	return b.eachConcurrently(len(repos),
		func(i int) string { return "repo " + repos[i].Slug },
		func(local *Builder, i int) error {
			s := local.step.Child("build.repo", repos[i].Slug)
			// Re-parented for the duration of this repo so every family and every ref inside it
			// hangs under this record rather than under the site. eachConcurrently restores it
			// before the next unit this worker draws.
			local.step = s
			// The DIFFERENCE, not the total: the serial path (--serial, or a one-repo site) reuses
			// the caller's Builder, whose counters carry every page written before this repo. Only
			// the pooled path resets them per unit, so a total here would be right in one mode and
			// cumulative nonsense in the other.
			before, beforeBytes := local.written, local.bytes
			err := emitRepo(local, &repos[i])
			s.Fail(err).DoneWith(timings.Counts{
				"pages": int64(local.written - before),
				"bytes": local.bytes - beforeBytes,
			})
			return err
		})
}

// defaultWorkers is how many units render at once when nothing says otherwise.
//
// GOMAXPROCS, not a fixed number: rendering is CPU-bound (markdown, highlighting, template
// execution) with small synchronous blob reads, so the useful parallelism is the core count. One
// core is left for the writer and the runtime on machines with several.
func defaultWorkers() int {
	n := runtime.GOMAXPROCS(0)
	if n > 2 {
		return n - 1
	}
	return n
}

func joinLines(lines []string) string {
	out := ""
	for i, l := range lines {
		if i > 0 {
			out += "\n  "
		}
		out += l
	}
	return out
}
