package logging

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// newLogDir makes a scratch directory and arranges for the sinks to be torn down BEFORE the
// directory is removed. The order is load-bearing on Windows, where a directory holding an open
// file handle cannot be deleted: t.Cleanup runs last-registered-first, so the temp dir has to be
// claimed before the teardown that closes the log.
func newLogDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resetSinks(t)
	return dir
}

// readLog reads the run log WITHOUT closing it, which is the point: the failure the file exists
// for is a process that never gets to close anything.
func readLog(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(LogPath(dir))
	if err != nil {
		t.Fatalf("read %s: %v", LogPath(dir), err)
	}
	return string(b)
}

// The file is written on every run at debug level even when stderr logging was never asked for,
// and every record is on disk before Close.
func TestFileWrittenWithStderrOff(t *testing.T) {
	dir := newLogDir(t)
	Setup("", nil) // stderr off, the default for a normal run

	if err := SetupFile(dir, nil); err != nil {
		t.Fatalf("SetupFile: %v", err)
	}
	slog.Debug("a debug record", "slug", "kieran/frznforge")

	out := readLog(t, dir)
	if !strings.Contains(out, "a debug record") || !strings.Contains(out, "slug=kieran/frznforge") {
		t.Fatalf("record missing from the file before Close:\n%s", out)
	}
	if !strings.Contains(out, "level=DEBUG") {
		t.Fatalf("file was not written at debug level:\n%s", out)
	}
}

// Truncate on open: the file answers "what did the run that just failed do", so the previous
// run's records must not still be in it.
func TestFileTruncatesPerRun(t *testing.T) {
	dir := newLogDir(t)

	_ = SetupFile(dir, nil)
	slog.Debug("first run")
	_ = Close()

	_ = SetupFile(dir, nil)
	slog.Debug("second run")

	out := readLog(t, dir)
	if strings.Contains(out, "first run") {
		t.Fatalf("previous run survived the truncate:\n%s", out)
	}
	if !strings.Contains(out, "second run") {
		t.Fatalf("current run missing:\n%s", out)
	}
}

// The two sinks have independent levels: --log=warn must not filter the file.
func TestSinksAreIndependent(t *testing.T) {
	dir := newLogDir(t)
	var stderr bytes.Buffer

	Setup("warn", &stderr)
	if err := SetupFile(dir, nil); err != nil {
		t.Fatalf("SetupFile: %v", err)
	}

	slog.Debug("only in the file")
	slog.Warn("in both")

	file := readLog(t, dir)
	if !strings.Contains(file, "only in the file") || !strings.Contains(file, "in both") {
		t.Fatalf("file is missing records:\n%s", file)
	}
	if strings.Contains(stderr.String(), "only in the file") {
		t.Fatalf("debug record leaked to a stderr sink set to warn:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "in both") {
		t.Fatalf("warn record missing from stderr:\n%s", stderr.String())
	}
}

// The CLI installs the stderr sink from --log before it has read the config that says where the
// output directory is, so the two calls arrive in that order — and must also survive the other.
func TestSinkOrderDoesNotMatter(t *testing.T) {
	for _, order := range []string{"stderr first", "file first"} {
		t.Run(order, func(t *testing.T) {
			dir := newLogDir(t)
			var stderr bytes.Buffer
			if order == "stderr first" {
				Setup("debug", &stderr)
				_ = SetupFile(dir, nil)
			} else {
				_ = SetupFile(dir, nil)
				Setup("debug", &stderr)
			}
			slog.Debug("both sinks")
			if !strings.Contains(stderr.String(), "both sinks") {
				t.Errorf("stderr sink lost the record:\n%s", stderr.String())
			}
			if !strings.Contains(readLog(t, dir), "both sinks") {
				t.Errorf("file sink lost the record")
			}
		})
	}
}

// Turning stderr logging off must not close the file: they are independent.
func TestSetupOffKeepsFile(t *testing.T) {
	dir := newLogDir(t)
	_ = SetupFile(dir, nil)
	Setup("debug", &bytes.Buffer{})
	Setup("", nil) // e.g. a second Setup with no level

	slog.Debug("still recorded")
	if !strings.Contains(readLog(t, dir), "still recorded") {
		t.Fatal("turning stderr off closed the file sink")
	}
}

// A log path that cannot be opened is reported once and the run carries on.
func TestUnopenableFileNeverFailsTheRun(t *testing.T) {
	dir := newLogDir(t)
	// A regular file where the output directory should be: MkdirAll cannot succeed on this on
	// any platform, which is the portable way to make the open fail.
	blocked := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	if err := SetupFile(blocked, &stderr); err == nil {
		t.Fatal("SetupFile returned nil for an unopenable path")
	}
	if !strings.Contains(stderr.String(), "warning:") || !strings.Contains(stderr.String(), "continuing without it") {
		t.Fatalf("failure was not reported to the caller's stderr: %q", stderr.String())
	}
	// And logging still works.
	var out bytes.Buffer
	Setup("debug", &out)
	slog.Debug("still logging")
	if !strings.Contains(out.String(), "still logging") {
		t.Fatal("a failed file sink broke stderr logging")
	}
}

// Close on a package that never opened a file, twice, must not panic.
func TestCloseIsSafeWhenNothingWasOpened(t *testing.T) {
	resetSinks(t)
	if err := Close(); err != nil {
		t.Fatalf("Close with no file: %v", err)
	}
	if err := Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// The build logs from hundreds of goroutines. Every record must land whole, on its own line.
func TestConcurrentWritersProduceWholeLines(t *testing.T) {
	dir := newLogDir(t)
	if err := SetupFile(dir, nil); err != nil {
		t.Fatalf("SetupFile: %v", err)
	}

	const goroutines, each = 32, 50
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				slog.Debug("concurrent record", "goroutine", g, "i", i, "pad", strings.Repeat("x", 200))
			}
		}(g)
	}
	wg.Wait()
	if err := Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lines := strings.Split(strings.TrimRight(readLog(t, dir), "\n"), "\n")
	if len(lines) != goroutines*each {
		t.Fatalf("got %d lines, want %d", len(lines), goroutines*each)
	}
	for i, l := range lines {
		if !strings.HasPrefix(l, "time=") || !strings.Contains(l, `msg="concurrent record"`) ||
			!strings.HasSuffix(l, strings.Repeat("x", 200)) {
			t.Fatalf("line %d is torn: %q", i, l)
		}
	}
}

// Redaction is enforced at the sink, so it covers the file as well as stderr.
func TestFileIsRedacted(t *testing.T) {
	dir := newLogDir(t)
	const token = "ghp_fileSinkRedaction01"
	Redact(token)
	_ = SetupFile(dir, nil)

	slog.Debug("fetching "+token, "url", "https://api.github.com/x/"+token, "token", token)

	if out := readLog(t, dir); strings.Contains(out, token) {
		t.Fatalf("token reached the file:\n%s", out)
	}
}

// A secret named by an environment variable this process is running with must be redacted
// without anyone calling Redact.
func TestEnvironmentSecretsAreHarvested(t *testing.T) {
	resetSinks(t)
	const token = "ghp_environmentHarvest01"
	t.Setenv("FRZNFORGE_GITHUB_TOKEN", token)
	// loadEnvSecrets runs once per process and another test may already have tripped it.
	envOnce = sync.Once{}

	var buf bytes.Buffer
	Setup("debug", &buf)
	slog.Debug("using " + token)

	if strings.Contains(buf.String(), token) {
		t.Fatalf("a token from the environment reached the log:\n%s", buf.String())
	}
}
