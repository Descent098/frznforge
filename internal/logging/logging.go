// Package logging is frznforge's diagnostic channel.
//
// It exists because of a specific failure: `frznforge build` printed "▸ frznforge" and then sat
// there, silently, for an hour. Nothing was wrong with the output — the process really had
// stopped — but there was no way to find out WHERE from outside, because the only thing the
// ingest ever said was which repository it had started. A run that stops has to be able to tell
// you what it was doing.
//
// The rules this follows:
//
//   - Off by default, and free when off. `slog.Default()` is a discard handler unless something
//     turns it on, so the normal run prints exactly what it printed before. The progress lines
//     the user reads are NOT log records; they go to stdout through Io and stay there.
//   - Diagnostics go to stderr. Redirecting them must never disturb the artifact or the
//     progress output, and `frznforge ingest 2> log.txt` has to be a complete answer to "what
//     happened".
//   - Every external process is logged around, not just after. A command that never returns
//     leaves a "starting" record with no matching "finished" one, which is precisely the
//     evidence the hour-long hang did not produce.
//
// There are two sinks and they are independent (see file.go):
//
//	Setup(level, stderr)     the user's choice, off unless --log or FRZNFORGE_LOG asks
//	SetupFile(outDir, errOut) <outDir>/frznforge.log, every run, always at debug
//
// Neither call undoes the other, in either order, because the CLI installs the stderr sink from
// the command line before it has read the config that says where outDir is.
//
// Whatever reaches either sink is redacted first, once, by the handler rather than by the call
// sites (see redact.go). Nothing in this package writes to the site, to dist/ or to forge.json:
// a diagnostic that could change what a build emits would be a determinism bug, not a feature.
//
// Records are logfmt — slog's TextHandler — in both sinks. The audience is a person reading a
// terminal or a file, and logfmt stays parseable for a tool that wants to read the file back:
// `time=… level=… msg=… key=value`, one record per line, with the file's timestamps carrying
// the date and UTC offset that stderr's do not.
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
)

// EnvVar turns logging on without a flag, for a CI job or a `set` in a shell that has already
// typed the command once.
const EnvVar = "FRZNFORGE_LOG"

// discard is the default: enabled for no level, so every call site costs one comparison.
type discard struct{}

func (discard) Enabled(context.Context, slog.Level) bool  { return false }
func (discard) Handle(context.Context, slog.Record) error { return nil }
func (d discard) WithAttrs([]slog.Attr) slog.Handler      { return d }
func (d discard) WithGroup(string) slog.Handler           { return d }

func init() { slog.SetDefault(slog.New(discard{})) }

// Level parses a level name. The empty string and "off" both mean off, so an unset environment
// variable and an explicitly disabled one take the same path.
//
// An unrecognised name is "debug" rather than an error: someone typing --log=verbose wants more
// output, and refusing to run is a worse answer than giving it to them.
func Level(name string) (slog.Level, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "off", "none", "false":
		return 0, false
	case "error":
		return slog.LevelError, true
	case "warn", "warning":
		return slog.LevelWarn, true
	case "info":
		return slog.LevelInfo, true
	default:
		return slog.LevelDebug, true
	}
}

// Setup installs the stderr sink at the given level, writing to w. Passing an empty name turns
// the stderr sink off; it does NOT touch the file sink, which is written on every run.
//
// Text, not JSON. The audience is a person reading a terminal or pasting a file into an issue,
// and every field here is short enough to stay on one line.
func Setup(name string, w io.Writer) {
	level, on := Level(name)
	if !on {
		setSink(&sinks.stderr, nil)
		return
	}
	if w == nil {
		w = os.Stderr
	}
	// Harvest the environment's secrets before the first record can carry one.
	loadEnvSecrets()
	setSink(&sinks.stderr, slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: level,
		// The timestamp is what makes a hang readable: the gap between the last record and now
		// is the answer. Seconds resolution would hide a fast loop, so it keeps milliseconds.
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				a.Value = slog.StringValue(a.Value.Time().Format("15:04:05.000"))
			}
			return a
		},
	}))
}

// FromEnv turns logging on if EnvVar is set. Returns whether it did, so a caller can say so.
func FromEnv() bool {
	name := os.Getenv(EnvVar)
	if _, on := Level(name); !on {
		return false
	}
	Setup(name, os.Stderr)
	return true
}
