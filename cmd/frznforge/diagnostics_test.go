package main

// The two diagnostic files, from the outside: they exist after a run nobody asked to be logged,
// they carry the run's evidence, and neither of them reaches the site.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"frznforge/internal/config"
	"frznforge/internal/logging"
	"frznforge/internal/timings"
)

func TestBuildWritesBothDiagnosticFilesWithoutBeingAsked(t *testing.T) {
	// The whole point of the file sink: the run that goes wrong is the one nobody thought to pass
	// --log to. No flag is given here, and both files still have to be there afterwards.
	root := t.TempDir()
	writeMinimalConfig(t, root)
	writeEmptyArtifact(t, root)

	tio := newTestIo(root)
	if err := run([]string{"build", "--no-ingest", "--root=" + root}, tio.Io); err != nil {
		t.Fatalf("build: %v", err)
	}

	data := filepath.Join(root, "data")
	log := readFile(t, logging.LogPath(data))
	for _, want := range []string{
		"run start", "command=build",
		// The render's own before-and-after pair. A family that never returns leaves the first of
		// these with no second, which is the whole diagnostic.
		`msg="family start" family=build.profile`,
		`msg="family done" family=build.profile`,
		`msg="family done" family=build.search-index`,
		"run done",
	} {
		mustContain(t, log, want, "the run log does not describe the run")
	}

	records := readTimings(t, timings.Path(data))
	kinds := map[string]bool{}
	for _, r := range records {
		kinds[r.Kind] = true
	}
	for _, want := range []string{"run", "build.site", "build.profile", "build.search-index"} {
		if !kinds[want] {
			t.Errorf("no %q record in the timings file; got %v", want, kindList(kinds))
		}
	}

	// Neither file may be published. dist/ is the site, and a run log in it would be served.
	for _, name := range []string{logging.LogName, timings.FileName} {
		if _, err := os.Stat(filepath.Join(root, "dist", name)); !os.IsNotExist(err) {
			t.Errorf("%s reached dist/", name)
		}
	}
}

func TestTheRunLogIsWrittenWhateverLogSaysAboutStderr(t *testing.T) {
	// --log controls the terminal and nothing else. A run with logging explicitly off still owes
	// the user a file, and a run with it on must not print the file's contents twice.
	for _, argv := range [][]string{
		{"build", "--no-ingest"},
		{"build", "--no-ingest", "--log=off"},
		{"build", "--no-ingest", "--log=error"},
	} {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			root := t.TempDir()
			writeMinimalConfig(t, root)
			writeEmptyArtifact(t, root)

			// The stderr sink is process-wide and setUpLogging never turns it back off, so a
			// case that switches it on would otherwise write into the next case's dead buffer.
			t.Cleanup(func() { logging.Setup("", nil) })

			tio := newTestIo(root)
			if err := run(append(argv, "--root="+root), tio.Io); err != nil {
				t.Fatalf("build: %v", err)
			}
			log := readFile(t, logging.LogPath(filepath.Join(root, "data")))
			mustContain(t, log, "run start", "the run log is empty for "+strings.Join(argv, " "))
			// Debug records belong on stderr only when the user asked for them.
			mustNotContain(t, tio.stderr(), "build.site", "a debug record leaked to stderr with --log off")
		})
	}
}

func TestTheRunLogIsReplacedPerRunAndTheTimingsFileIsNot(t *testing.T) {
	// The two files answer different questions and therefore have opposite growth rules: "what
	// did the run that just failed do" is only ever about the last run, while "what was slow"
	// is only answerable by comparing runs.
	root := t.TempDir()
	writeMinimalConfig(t, root)
	writeEmptyArtifact(t, root)
	data := filepath.Join(root, "data")

	counts := []int{}
	for i := 0; i < 2; i++ {
		tio := newTestIo(root)
		if err := run([]string{"build", "--no-ingest", "--root=" + root}, tio.Io); err != nil {
			t.Fatalf("build %d: %v", i, err)
		}
		counts = append(counts, len(readTimings(t, timings.Path(data))))
	}
	// Record count, not run id: two runs of one test process share a pid and can share a second,
	// which is all a run id is made of. What matters here is that the second run did not replace
	// the first one's lines.
	if counts[1] <= counts[0] {
		t.Errorf("the timings file went from %d to %d records; it is not appending", counts[0], counts[1])
	}
	if n := strings.Count(readFile(t, logging.LogPath(data)), "run start"); n != 1 {
		t.Errorf("the run log holds %d runs; it should have been replaced", n)
	}
}

func TestDiagnosticsDirFollowsTheFlagsTheCommandItselfReads(t *testing.T) {
	// `ingest --out=` names the artifact directory; `build --out=` names dist/. Reading the
	// second as the first would write the run log into the published site.
	//
	// A project whose config does not load still gets its log, from the documented default — but
	// only where that directory already exists, so `dev` in an unrelated folder invents nothing.
	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, config.Filename), []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(broken, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	bare := t.TempDir()

	cases := []struct {
		cmd  string
		argv []string
		want string
		ok   bool
	}{
		{"ingest", []string{"--out=elsewhere"}, "elsewhere", true},
		{"build", []string{"--out=dist2", "--root=" + broken}, filepath.Join(broken, "data"), true},
		{"dev", []string{"--root=" + broken}, filepath.Join(broken, "data"), true},
		{"dev", []string{"--root=" + bare, "--dir=somewhere"}, "", false},
		{"verify", nil, "", false},
		{"init", nil, "", false},
		{"new", nil, "", false},
	}
	for _, c := range cases {
		got, ok := diagnosticsDir(c.cmd, c.argv)
		if ok != c.ok || (c.ok && got != c.want) {
			t.Errorf("diagnosticsDir(%q, %v) = %q,%v want %q,%v", c.cmd, c.argv, got, ok, c.want, c.ok)
		}
	}
}

func TestAnUnwritableDiagnosticsDirectoryDoesNotFailTheRun(t *testing.T) {
	// Diagnostics are not the product. A data directory that cannot be written is worth one
	// warning, not a refusal to build.
	root := t.TempDir()
	writeMinimalConfig(t, root)
	writeEmptyArtifact(t, root)
	// A FILE where the artifact directory's log has to go: MkdirAll succeeds (data/ exists) and
	// the open fails, which is the failure this path exists for and works the same on Windows.
	if err := os.Mkdir(filepath.Join(root, "data", logging.LogName), 0o755); err != nil {
		t.Fatal(err)
	}

	tio := newTestIo(root)
	if err := run([]string{"build", "--no-ingest", "--root=" + root}, tio.Io); err != nil {
		t.Fatalf("an unopenable run log failed the build: %v", err)
	}
	mustContain(t, tio.stderr(), "warning:", "nothing said the run log could not be opened")
	mustContain(t, tio.stderr(), "continuing without it", "the warning does not say the run continued")
}

/* ---- helpers -------------------------------------------------------------- */

func readTimings(t *testing.T, path string) []timings.Record {
	t.Helper()
	records, err := timings.ParseFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(records) == 0 {
		t.Fatalf("%s is empty", path)
	}
	// Every line must be a complete JSON object: a reader that trips over a torn record is a
	// reader nobody trusts.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if !json.Valid([]byte(line)) {
			t.Fatalf("timings line is not JSON: %q", line)
		}
	}
	return records
}

func kindList(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
