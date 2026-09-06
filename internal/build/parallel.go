package build

import (
	"fmt"
	"runtime"
	"sort"
	"sync"
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
// the repository count, and the count is often one: this project's own site is a single
// repository whose tree/blob/raw multiplier is 85% of its 1,205 files. Measured at one repo,
// per-repo parallelism does nothing at all (3.74 s at one worker, 3.92 s at thirty-two — slightly
// worse, which is the pool overhead).
//
// So the pool is used at BOTH levels: repositories run concurrently, and inside a repository the
// tree/blob/raw families — the multiplier that is 87-90% of every build measured — run
// concurrently too. A one-repo site gets the same benefit as a fifty-repo one.
//
// # One bound, shared
//
// Nesting two pools would multiply: N repos × N pages each is N² goroutines fighting over the
// same cores. There is instead ONE semaphore for the whole build, created in Run and shared by
// every Builder copy (it is a channel, so a copy shares the same one). Both levels acquire from
// it, so total concurrency is `workers` no matter how the work nests.
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

// eachConcurrently runs fn for each index in [0,n), up to the build's worker bound.
//
// Each unit gets its OWN Builder view: it shares the renderer (html/template is safe for
// concurrent execution) and the artifact (read-only by now), and keeps its own counters and log
// buffer. The only synchronisation left is merging those at the end, in index order.
//
// describe names a failing unit — a repo slug, a ref name — so an error says which one.
func (b *Builder) eachConcurrently(n int, describe func(i int) string, fn func(*Builder, int) error) error {
	if n == 0 {
		return nil
	}
	if b.sem == nil || cap(b.sem) <= 1 || n == 1 {
		for i := 0; i < n; i++ {
			if err := fn(b, i); err != nil {
				return fmt.Errorf("%s: %w", describe(i), err)
			}
		}
		return nil
	}

	var (
		mu    sync.Mutex
		errs  []string
		wg    sync.WaitGroup
		logs  = make([][]string, n)
		count = make([]int, n)
		bytes = make([]int64, n)
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b.sem <- struct{}{}
			defer func() { <-b.sem }()

			local := *b
			local.written, local.bytes, local.log = 0, 0, nil
			if err := fn(&local, i); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Sprintf("%s: %v", describe(i), err))
				mu.Unlock()
				return
			}
			logs[i], count[i], bytes[i] = local.log, local.written, local.bytes
		}(i)
	}
	wg.Wait()

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
		func(local *Builder, i int) error { return emitRepo(local, &repos[i]) })
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
