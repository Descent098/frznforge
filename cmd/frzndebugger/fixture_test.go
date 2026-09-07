package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// timeoutAfter is the deadline a test gives code that must terminate on a malformed file. Long
// enough that a slow machine never trips it, short enough that a genuine hang fails the run
// rather than the CI job's own timeout.
func timeoutAfter() <-chan time.Time { return time.After(5 * time.Second) }

// The fixtures are written as literal file bytes rather than through internal/timings' recorder
// on purpose: this program's whole job is to read a format someone else writes, and a test that
// round-trips through the writer would pass even if both halves drifted from the documented
// schema together.

// killedRun is a run that stopped: ids 1, 2, 4 and 5 are on disk and 3 is not, so step 3 started
// and never came back. Steps 4 and 5 name it as their parent, which is what makes it describable
// rather than merely missing.
const killedRun = `{"v":1,"run":"20260906T224903Z-4812","id":"2","parent":"1","ts":"2026-09-06T22:49:03.100Z","kind":"ingest.repo","name":"kieran/alpha","ms":100.5,"counts":{"refs":3}}
{"v":1,"run":"20260906T224903Z-4812","id":"4","parent":"3","ts":"2026-09-06T22:49:03.300Z","kind":"git.fetch","name":"kieran/beta","ms":900}
{"v":1,"run":"20260906T224903Z-4812","id":"5","parent":"3","ts":"2026-09-06T22:49:04.300Z","kind":"git.log","name":"kieran/beta","ms":50,"err":"timed out"}
{"v":1,"run":"20260906T224903Z-4812","id":"1","span":true,"ts":"2026-09-06T22:49:03.000Z","kind":"build","name":"site","ms":5000,"counts":{"pages":812}}
`

// completeRun finished cleanly: ids 1, 2 and 3 with no holes.
const completeRun = `{"v":1,"run":"20260905T101010Z-100","id":"2","parent":"1","ts":"2026-09-05T10:10:10.100Z","kind":"ingest.repo","name":"kieran/alpha","ms":80}
{"v":1,"run":"20260905T101010Z-100","id":"3","parent":"1","ts":"2026-09-05T10:10:10.200Z","kind":"ingest.repo","name":"kieran/beta","ms":120}
{"v":1,"run":"20260905T101010Z-100","id":"1","span":true,"ts":"2026-09-05T10:10:10.000Z","kind":"build","name":"site","ms":400,"counts":{"pages":40}}
`

// tornTail is the last line of a process that was killed mid-write: no newline, half an object.
const tornTail = `{"v":1,"run":"20260906T224903Z-4812","id":"6","ts":"2026-09-06T22:49:0`

// sampleLog is what internal/logging's TextHandler writes, including a credential-shaped URL
// that never met a scrubber and a line that is not a record at all.
const sampleLog = `time=2026-09-06T22:49:03.512-07:00 level=DEBUG msg="git start" args="rev-parse HEAD" repo=alpha
time=2026-09-06T22:49:03.612-07:00 level=INFO msg="ingest finished" repos=2
time=2026-09-06T22:49:04.000-07:00 level=WARN msg="cache miss" key=beta
time=2026-09-06T22:49:05.000-07:00 level=ERROR msg="clone failed" url=https://x-access-token:ghs_supersecretvalue@example.com/r.git
this line is not a record at all
`

// fixtureDir writes both files into a fresh temp directory and returns it.
func fixtureDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "frznforge.log"), sampleLog)
	write(t, filepath.Join(dir, "frznforge-timings.jsonl"), completeRun+killedRun+tornTail)
	return dir
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
