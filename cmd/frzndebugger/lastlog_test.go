package main

import (
	"testing"
	"time"
)

// The floor under "at least N" for a step that never finished, and the guard on where it comes
// from.
//
// The timings file records COMPLETIONS, and the step worth asking about is the one with none. So
// the bound used to stop at the last child that managed to finish: a build killed after 4.1
// seconds reported "at least 304ms", understating by thirteen times the single number someone
// opens this program to see. The run log kept writing the whole time, and its last line is a far
// better answer to "when did this stop".
//
// The guard matters as much as the fix. The log is truncated per run; the timings file is
// appended across runs. A stale timings file beside a fresh log is the ordinary state of a data
// directory, and taking the floor from the wrong run would turn "at least 4s" into "at least
// seven hours" — a confident wrong answer, which is worse than the timid one it replaced.
func TestLastLogTimeOnlyTrustsItsOwnRun(t *testing.T) {
	const runID = "20260906T224903Z-4812"
	logFor := func(id string) []LogRecord {
		return []LogRecord{
			{N: 1, Time: "2026-09-06T22:49:03.100-06:00", Msg: "run start", Attrs: []Attr{{Key: "run", Value: id}}},
			{N: 2, Time: "2026-09-06T22:49:07.500-06:00", Msg: "git start"},
		}
	}

	t.Run("the log names this run, so its last line is the floor", func(t *testing.T) {
		got := LastLogTime(logFor(runID), runID)
		want := time.Date(2026, 9, 6, 22, 49, 7, 500_000_000, time.FixedZone("", -6*3600))
		if !got.Equal(want) {
			t.Errorf("got %v, want the log's last timestamp %v", got, want)
		}
	})

	t.Run("the log names a different run, so it says nothing", func(t *testing.T) {
		if got := LastLogTime(logFor("20260101T000000Z-1"), runID); !got.IsZero() {
			t.Errorf("got %v, want the zero time — this log belongs to another run", got)
		}
	})

	t.Run("the log names no run at all", func(t *testing.T) {
		bare := []LogRecord{{N: 1, Time: "2026-09-06T22:49:07.500-06:00", Msg: "something"}}
		if got := LastLogTime(bare, runID); !got.IsZero() {
			t.Errorf("got %v, want the zero time — nothing ties this log to the run", got)
		}
	})

	t.Run("no run id to match against", func(t *testing.T) {
		if got := LastLogTime(logFor(runID), ""); !got.IsZero() {
			t.Errorf("got %v, want the zero time", got)
		}
	})

	t.Run("a stderr-shaped timestamp is refused rather than half-read", func(t *testing.T) {
		// The stderr sink writes a time with no date. Parsing it would put the run in year zero
		// and produce a negative bound; no answer is the honest one.
		short := []LogRecord{
			{N: 1, Time: "22:49:03.100", Msg: "run start", Attrs: []Attr{{Key: "run", Value: runID}}},
		}
		if got := LastLogTime(short, runID); !got.IsZero() {
			t.Errorf("got %v, want the zero time for a dateless stamp", got)
		}
	})
}
