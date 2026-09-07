package logging

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// The run log.
//
// `--log=debug 2> build.log` answers "what happened" only for the person who already suspected
// something would go wrong. The run that actually fails is the one nobody was watching, and it
// has already finished by the time anyone wants its diagnostics. So the file is written on
// EVERY run, at debug level, whether or not stderr logging was asked for — the cost is one
// buffered write per record from 27 call sites, which does not show up next to a build that
// renders thousands of pages.
//
// Three decisions worth naming:
//
//   - It goes in the ingest output directory. `data/` is already gitignored, already this run's
//     own directory, and already where someone looks when a build produced the wrong site.
//   - Truncate on open, not append. A file that grows forever is a file nobody reads; the
//     question is "what did the run that just failed do", and the answer is only ever the last
//     run. (The timings file is the opposite and appends, because comparing runs IS its job.)
//   - A path that cannot be opened is reported once and then ignored. Diagnostics are not the
//     product: refusing to build because the log file could not be created would turn a
//     read-only directory into a broken tool.

// LogName is the run log's file name inside the ingest output directory.
const LogName = "frznforge.log"

// DevLogName is the server's own log, kept apart from the build's on purpose.
//
// `frznforge build` then `frznforge dev` is the documented loop, and the file is truncated per
// run — so sharing one name meant the command you run to LOOK at the site erased the record of
// the command that built it. The run you want to debug is always the one before the one you are
// running now.
//
// It is also a different shape of thing: a build is a run with an end, a dev server is a process
// that lives for hours and logs a request at a time.
const DevLogName = "frznforge-dev.log"

// LogPath is where SetupFile writes for a given ingest output directory.
func LogPath(outDir string) string { return filepath.Join(outDir, LogName) }

// LogPathNamed is LogPath for a caller that chooses the file, e.g. DevLogName.
func LogPathNamed(outDir, name string) string { return filepath.Join(outDir, name) }

// syncWriter serializes writes to the log file and flushes each one.
//
// The flush is to the OS, not to the disk: the failure this defends against is a process that
// hangs and gets killed, and a killed process's page cache survives it. An fsync per record
// would defend against a power cut instead, at a cost — milliseconds a record — that a
// heavily parallel build cannot pay for diagnostics it usually never reads.
//
// The mutex covers Write and Flush together. slog's TextHandler already serializes its own
// writes, but a handler is free to issue more than one Write per record, and a Flush racing a
// half-written record would tear a line in the file — which is exactly the file you cannot
// afford to have lied to you.
type syncWriter struct {
	mu  sync.Mutex
	buf *bufio.Writer
	f   *os.File
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf == nil {
		return len(p), nil // closed; drop rather than fail a record
	}
	n, err := w.buf.Write(p)
	if err != nil {
		return n, err
	}
	return n, w.buf.Flush()
}

func (w *syncWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf == nil {
		return nil
	}
	err := w.buf.Flush()
	w.buf = nil
	if cerr := w.f.Close(); err == nil {
		err = cerr
	}
	return err
}

// current is the open file sink, so a second SetupFile or a Close can reach it.
var current struct {
	mu sync.Mutex
	w  *syncWriter
}

// SetupFile opens <outDir>/frznforge.log, truncating it, and adds it as a second sink at debug
// level. It is independent of Setup: turning stderr logging on or off does not touch the file,
// and the file is written whether or not stderr logging was ever turned on.
//
// A failure is reported once to errOut (os.Stderr when nil) and returned. Production callers
// can ignore the returned error — it has already been reported and the run continues with
// whatever sinks it had. It is returned for tests, which need to assert the failure path
// exists rather than read it off a terminal.
//
// Calling it twice closes the first file. The last call wins.
func SetupFile(outDir string, errOut io.Writer) error {
	return SetupFileNamed(outDir, LogName, errOut)
}

// SetupFileNamed is SetupFile with the file name chosen by the caller, so a long-lived command
// can keep its own log instead of overwriting the last build's.
func SetupFileNamed(outDir, name string, errOut io.Writer) error {
	if errOut == nil {
		errOut = os.Stderr
	}
	// Before the first record, not after: a secret registered later would not retroactively
	// clean a line already on disk.
	loadEnvSecrets()

	path := LogPathNamed(outDir, name)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return reportLogFailure(errOut, path, err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return reportLogFailure(errOut, path, err)
	}

	w := &syncWriter{buf: bufio.NewWriterSize(f, 8<<10), f: f}
	handler := slog.NewTextHandler(w, &slog.HandlerOptions{
		// Always debug. The stderr level is the user's choice about noise; the file's level is
		// this package's choice about evidence, and there is no evidence in a filtered file.
		Level: slog.LevelDebug,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				// A full local timestamp with milliseconds. Unlike the stderr format this file
				// outlives its terminal, so it has to say which day it is, and the offset has to
				// be there for anyone comparing it against a server's UTC log.
				a.Value = slog.StringValue(a.Value.Time().Format("2006-01-02T15:04:05.000-07:00"))
			}
			return a
		},
	})

	current.mu.Lock()
	old := current.w
	current.w = w
	current.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	setSink(&sinks.file, handler)
	return nil
}

func reportLogFailure(errOut io.Writer, path string, err error) error {
	wrapped := fmt.Errorf("could not open the run log %s: %w", path, err)
	// Once, on stderr, and then never again — a diagnostic that nags about diagnostics is worse
	// than the missing file.
	fmt.Fprintln(errOut, "warning: "+Scrub(wrapped.Error())+" (continuing without it)")
	return wrapped
}

// Close flushes and closes the run log, removing it as a sink. It is safe to call when no file
// was ever opened, and safe to call twice.
func Close() error {
	current.mu.Lock()
	w := current.w
	current.w = nil
	current.mu.Unlock()
	setSink(&sinks.file, nil)
	if w == nil {
		return nil
	}
	return w.Close()
}
