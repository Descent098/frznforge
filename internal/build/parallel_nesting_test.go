package build

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// The nesting deadlock, pinned.
//
// eachConcurrently is used at two levels — repositories, and the tree/blob/raw pages inside each
// repository — over ONE shared semaphore. The first version had every unit take a slot and then
// wait for its children to take slots of their own, which deadlocks as soon as the outer level
// can fill the semaphore by itself.
//
// It shipped that way and hung a real build: 73 repositories on a 32-core machine, 31 slots all
// held by repositories parked in wg.Wait(), thousands of page goroutines parked in `chan send`,
// and no error — the process simply stopped. A single-repository site never reproduced it,
// because one repository takes the serial path and never holds a slot at all.
//
// The shape that matters is therefore OUTER UNITS > WORKERS, with nested work inside each. These
// tests are written with deadlines because the failure is a hang: an assertion that never runs
// never fails.
func TestNestedEachConcurrentlyCannotDeadlock(t *testing.T) {
	for _, tc := range []struct{ workers, outer, inner int }{
		{workers: 2, outer: 8, inner: 4},    // the minimal shape: outer exceeds workers
		{workers: 4, outer: 64, inner: 16},  // many more outer units than slots
		{workers: 31, outer: 73, inner: 40}, // the build that actually hung
		{workers: 2, outer: 2, inner: 2},    // exactly at the bound
	} {
		t.Run(fmt.Sprintf("workers=%d/outer=%d/inner=%d", tc.workers, tc.outer, tc.inner), func(t *testing.T) {
			b := &Builder{sem: make(chan struct{}, tc.workers)}
			var done atomic.Int64

			finished := make(chan error, 1)
			go func() {
				finished <- b.eachConcurrently(tc.outer,
					func(i int) string { return fmt.Sprintf("outer %d", i) },
					func(outerB *Builder, i int) error {
						// The nesting: each outer unit runs its own parallel section, exactly as
						// emitRepo does when it reaches emitBlobPages.
						return outerB.eachConcurrently(tc.inner,
							func(j int) string { return fmt.Sprintf("inner %d.%d", i, j) },
							func(innerB *Builder, j int) error {
								done.Add(1)
								innerB.written++
								return nil
							})
					})
			}()

			select {
			case err := <-finished:
				if err != nil {
					t.Fatalf("eachConcurrently: %v", err)
				}
				if got, want := done.Load(), int64(tc.outer*tc.inner); got != want {
					t.Errorf("ran %d units, want %d — some work was dropped", got, want)
				}
				if b.written != tc.outer*tc.inner {
					t.Errorf("counted %d writes, want %d — the merge lost some", b.written, tc.outer*tc.inner)
				}
			case <-time.After(20 * time.Second):
				t.Fatal("nested eachConcurrently deadlocked.\n" +
					"The outer units are holding every semaphore slot while waiting for inner units\n" +
					"that cannot acquire one. The caller has to run units itself rather than only\n" +
					"waiting, and helpers must never block acquiring a slot.")
			}
		})
	}
}

// TestEachConcurrentlyBoundsItsGoroutines pins the other half of the same fix.
//
// The old shape spawned one goroutine per unit up front and let them queue on the semaphore —
// 14,000 goroutines, each carrying its own Builder copy, before a single page was rendered. The
// goroutine count has to follow the worker bound, not the amount of work.
func TestEachConcurrentlyBoundsItsGoroutines(t *testing.T) {
	const workers, units = 4, 5000
	b := &Builder{sem: make(chan struct{}, workers)}

	var live, peak atomic.Int64
	err := b.eachConcurrently(units,
		func(i int) string { return "unit" },
		func(local *Builder, i int) error {
			n := live.Add(1)
			for {
				old := peak.Load()
				if n <= old || peak.CompareAndSwap(old, n) {
					break
				}
			}
			time.Sleep(time.Microsecond)
			live.Add(-1)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}

	// The caller works too, so the ceiling is the helpers plus it. Anything near the unit count
	// means the pool went back to spawning per unit.
	if got := peak.Load(); got > int64(workers+1) {
		t.Errorf("%d units ran %d at once with a bound of %d", units, got, workers)
	}
}

// TestEachConcurrentlyReportsEveryFailure keeps the error path honest under the new shape: a
// worker that hits an error moves on to the next unit rather than abandoning the queue, and every
// failure is still collected and named.
func TestEachConcurrentlyReportsEveryFailure(t *testing.T) {
	b := &Builder{sem: make(chan struct{}, 3)}
	err := b.eachConcurrently(10,
		func(i int) string { return fmt.Sprintf("unit-%02d", i) },
		func(local *Builder, i int) error {
			if i%2 == 0 {
				return fmt.Errorf("boom %d", i)
			}
			return nil
		})
	if err == nil {
		t.Fatal("no error reported for five failing units")
	}
	for _, want := range []string{"unit-00", "unit-02", "unit-04", "unit-06", "unit-08", "5 of 10"} {
		if !contains(err.Error(), want) {
			t.Errorf("the error does not mention %q:\n%v", want, err)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
