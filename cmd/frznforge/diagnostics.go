package main

// The run's two diagnostic files, opened here and nowhere else.
//
// `--log=debug 2> build.log` only ever helped the person who already suspected trouble. The run
// that actually goes wrong is the one nobody was watching, and by the time anyone wants its
// evidence the process is gone. So every run of the three commands that HAVE an artifact
// directory writes both files unconditionally:
//
//	<ingest.outDir>/frznforge.log            what happened, at debug, truncated per run
//	<ingest.outDir>/frznforge-timings.jsonl  what was slow, appended, one line per step
//
// Three things this deliberately does NOT do:
//
//   - It does not touch the stderr sink. `--log` and FRZNFORGE_LOG still decide, alone, what a
//     terminal sees; the file is a second sink with its own level, and neither call undoes the
//     other (see internal/logging).
//   - It does not fail a run. An unwritable data directory produces one warning line from the
//     package that could not open the file, and the command proceeds with whatever it has.
//     Refusing to build because a diagnostic could not be written would turn a read-only
//     directory into a broken tool.
//   - It does not reach dist/ or forge.json. Both files sit in the ingest output directory,
//     which is already gitignored, and nothing in the build walks it except the blob and archive
//     mirrors — which walk their own subdirectories by name.
//
// init, new, verify and `config migrate` are left out. None of them has an artifact directory to
// write into (`new` runs in an empty one, by definition), none of them shells out or fetches,
// and creating a data/ directory as a side effect of `frznforge verify` would be a surprise.

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"frznforge/internal/config"
	"frznforge/internal/logging"
	"frznforge/internal/timings"
)

// startDiagnostics opens the run log and the timings file for cmd, and returns the run's own
// timings step together with the function that closes both.
//
// The returned step is the parent everything else in the run hangs under, so a `frznforge build`
// records its scan and its render as two halves of one run rather than as two unrelated trees.
// A nil step is a working no-op, which is what a command with no artifact directory gets.
//
// The returned closer takes the run's own error so the last line of the file says how it went.
func startDiagnostics(cmd string, argv []string, io *Io) (*timings.Step, func(error)) {
	dir, ok := diagnosticsDir(cmd, argv)
	if !ok {
		return nil, func(error) {}
	}
	// Both errors are ignored on purpose: each has already been reported to io.Err by the package
	// that could not open its file, and both are returned only so a test can assert the failure
	// path exists.
	// dev writes frznforge-dev.log, not frznforge.log. It is a long-lived server and the file is
	// truncated per run, so sharing the name meant starting the preview destroyed the log of the
	// build you were previewing — the one you actually wanted.
	logName := logging.LogName
	if cmd == "dev" {
		logName = logging.DevLogName
	}
	_ = logging.SetupFileNamed(dir, logName, io.Err)
	_ = timings.Open(dir, io.Err)

	// The first record in the file says what this run is, so a log someone pasted into an issue
	// identifies itself without the command line having to be quoted separately.
	slog.Debug("run start", "command", cmd, "args", strings.Join(argv, " "), "cwd", io.Cwd,
		"log", logging.LogPath(dir), "timings", timings.Path(dir), "run", timings.RunID())

	step := timings.Start("run", cmd)
	return step, func(err error) {
		// The verdict, in the file. Without it the log of a failed run ends exactly like the log
		// of a successful one and the message the user saw on their terminal is the only place the
		// failure exists — which is no help at all once the terminal is gone.
		step.Fail(err)
		// Order matters. The step's record has to be written before the timings file closes, and
		// the "run done" line has to be written before the log closes — a diagnostic that loses
		// its own last record is the one that makes people distrust the rest of it.
		step.Done()
		slog.Debug("run done", "command", cmd, "err", err)
		_ = timings.Close()
		_ = logging.Close()
	}
}

// diagnosticsDir is the artifact directory the two files go in, and whether this command has one.
//
// It resolves --root and --out exactly the way the command itself will. The fallback when the
// config does not load is deliberate and so is its condition: a config error is the command's to
// report properly, and a helper that gave up on one would take the log away from precisely the
// run that needed it — but only if <root>/data is already there. `frznforge dev --dir=…/site`
// in some unrelated directory must not conjure a data/ folder next to whatever the user happened
// to be standing in, and an invented directory is exactly what a fallback with no condition
// produces.
func diagnosticsDir(cmd string, argv []string) (string, bool) {
	switch cmd {
	case "ingest", "build", "dev":
	default:
		return "", false
	}

	root, out := ".", ""
	for _, a := range argv {
		switch {
		case strings.HasPrefix(a, "--root="):
			root = strings.TrimPrefix(a, "--root=")
		case cmd == "ingest" && strings.HasPrefix(a, "--out="):
			// Only ingest's. `build --out=` names the DIST directory, and writing the run log there
			// would publish it with the site.
			out = strings.TrimPrefix(a, "--out=")
		}
	}
	if out != "" {
		// The artifact is going here whether or not it exists yet, so creating it costs nothing
		// that the run was not about to do anyway.
		return out, true
	}
	if cfg, err := config.Load(root); err == nil {
		return cfg.OutDir, true
	}
	fallback := filepath.Join(root, "data")
	if info, err := os.Stat(fallback); err == nil && info.IsDir() {
		return fallback, true
	}
	return "", false
}
