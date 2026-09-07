// Command frzndebugger reads what a frznforge run left behind: the run log written by
// internal/logging and the step timings written by internal/timings.
//
//	go build ./cmd/frzndebugger
//
// It is a SECOND binary on purpose. Everything in this directory is a developer's tool — a
// terminal UI, a key decoder, an embedded HTML page — and none of it has any business inside the
// generator that every user of frznforge ships. Keeping it out also keeps the answer to "what
// does frznforge depend on" honest: this program adds nothing to the project's two dependencies,
// and it imports internal/logging and internal/timings only to read the formats they define.
//
// It never writes to data/, never touches dist/ or forge.json, and never runs a build. A viewer
// that could change what a build emits would be a determinism bug wearing a diagnostic's clothes.
//
// Two front ends over one set of answers (analysis.go):
//
//	frzndebugger                 a terminal UI over data/frznforge.log and the timings file
//	frzndebugger --web           the same two views in a browser, on localhost
//
// and a third that is not really a front end: when there is no terminal to draw on — a pipe, a
// console with no VT support, --plain — it prints the same three sections as a listing rather
// than refusing to start. Someone reaching for this is already debugging something else.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"

	"frznforge/internal/config"
	"frznforge/internal/logging"
)

const usage = `frzndebugger -- read the run log and the step timings a frznforge run left behind

Usage
  frzndebugger [--dir=<dir>] [--root=<dir>] [--run=<id>] [--level=<name>] [--grep=<text>]
               [--plain] [--web [--port=<n>]]

  --dir=<dir>     The ingest output directory holding frznforge.log and
                  frznforge-timings.jsonl. Defaults to the ingest.outDir of the config found
                  under --root, and to <root>/data when there is no config.
  --root=<dir>    Where to look for frznforge.config.jsonc. Default ".".
  --run=<id>      Scope the timings to one run id, or "all" for every run in the file.
                  Default: the newest run, which is the one that just failed.
  --level=<name>  Hide log records below this level: debug, info, warn, error, or "all".
  --grep=<text>   Hide log records whose line does not contain this text (case-insensitive).
  --plain         Print the listing instead of drawing a screen, even on a terminal.
  --web           Serve the same views over HTTP on localhost and print the URL.
  --port=<n>      Port for --web. Default 0, which asks the OS for a free one.

Keys in the terminal UI
  1 2 3 / tab  switch between unfinished, timings and log     q or ctrl-c  quit
  j k          move          g G     top / last record        r            re-read the files
  enter        open a step's children                         o O          sort column / reverse
  [ ] a        previous run / next run / all runs             l  /  c      level, find, clear

The timings file is appended to, so it holds the last few runs; the log is truncated every run
and holds only the last one.`

type console struct{ In, Out *os.File }

// app is the process, injected — the same arrangement cmd/frznforge uses and for the same
// reason: a command that reaches for os.Stdout and os.Environ itself cannot be tested.
type app struct {
	Args    []string
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Environ []string
	// Console is the terminal to draw on, or nil when there is not one. Nil forces the plain
	// listing, which is what a redirected run wants anyway.
	Console *console
}

func main() {
	a := &app{
		Args:    os.Args[1:],
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Environ: os.Environ(),
	}
	// Both halves have to be a console: a TUI drawing into a pipe is unreadable, and one reading
	// keys from a file spins.
	if isCharDevice(os.Stdin) && isCharDevice(os.Stdout) {
		a.Console = &console{In: os.Stdin, Out: os.Stdout}
	}
	if err := a.run(); err != nil {
		fmt.Fprintln(os.Stderr, "frzndebugger: "+logging.Scrub(err.Error()))
		os.Exit(1)
	}
}

/* ---- arguments ----------------------------------------------------------- */

type args struct {
	Root     string
	Dir      string
	DirSet   bool
	Run      string
	Level    string
	LevelSet bool
	Grep     string
	Plain    bool
	Web      bool
	Port     int
	Help     bool
}

func parseArgs(argv []string) (args, error) {
	a := args{Root: "."}
	for _, arg := range argv {
		switch {
		case arg == "--help" || arg == "-h":
			a.Help = true
		case strings.HasPrefix(arg, "--dir="):
			a.Dir, a.DirSet = strings.TrimPrefix(arg, "--dir="), true
		case strings.HasPrefix(arg, "--root="):
			a.Root = strings.TrimPrefix(arg, "--root=")
		case strings.HasPrefix(arg, "--run="):
			a.Run = strings.TrimPrefix(arg, "--run=")
		case strings.HasPrefix(arg, "--level="):
			a.Level, a.LevelSet = strings.TrimPrefix(arg, "--level="), true
		case strings.HasPrefix(arg, "--grep="):
			a.Grep = strings.TrimPrefix(arg, "--grep=")
		case arg == "--plain":
			a.Plain = true
		case arg == "--web":
			a.Web = true
		case strings.HasPrefix(arg, "--port="):
			raw := strings.TrimPrefix(arg, "--port=")
			n, err := strconv.Atoi(raw)
			if err != nil || n < 0 || n > 65535 {
				return a, fmt.Errorf("--port needs a number from 0 to 65535, got %q (0 asks the OS for a free one)", raw)
			}
			a.Port = n
		default:
			return a, fmt.Errorf("unknown flag %q -- run frzndebugger --help", arg)
		}
	}
	if a.Web && a.Plain {
		return a, errors.New("--web and --plain ask for different things: --web serves the views in a browser, --plain prints them")
	}
	return a, nil
}

/* ---- running ------------------------------------------------------------- */

func (a *app) run() error {
	flags, err := parseArgs(a.Args)
	if err != nil {
		return err
	}
	if flags.Help {
		fmt.Fprintln(a.Stdout, usage)
		return nil
	}

	// Before anything is read and long before anything is printed. This program shows a file
	// that was scrubbed when it was written, but a file written by an older build, or copied
	// from another machine, has not necessarily met a scrubber at all — and this process is the
	// one about to put it on a screen and into an HTTP response. Registering the environment's
	// secret-looking values here is what lets logging.Scrub, applied at the parse boundary in
	// analysis.go, catch a literal token that the file's own writer never knew about.
	armRedaction(a.Environ)

	dir, err := resolveDir(flags)
	if err != nil {
		return err
	}
	filter := LogFilter{Text: flags.Grep}
	if flags.LevelSet {
		filter.MinLevel, filter.HasLevel = ParseLevel(flags.Level)
	}

	if flags.Web {
		return a.serveWeb(dir, flags)
	}

	data := Load(dir)
	if a.Console == nil || flags.Plain {
		return WritePlain(a.Stdout, data, filter, flags.Run, a.width())
	}
	return a.serveTUI(dir, data, filter, flags)
}

// width is how wide the listing may be. A redirected run has no console to ask and gets the
// default, which is also what makes `frzndebugger > notes.txt` produce the same file everywhere.
func (a *app) width() int {
	if a.Console == nil {
		return defaultCols
	}
	size, stop := newSizer(a.Console.Out)
	defer stop()
	w, _ := size()
	return w
}

// armRedaction registers this process's secret-looking environment values with the scrubber.
//
// It reads NAMES to decide what is a secret, exactly as internal/logging does, and the values it
// reads never leave the scrubber: they are only ever the left-hand side of a replacement, and
// nothing here prints, stores or transmits one. logging.Redact enforces its own minimum length,
// so a variable set to a single character cannot turn every occurrence of that character into
// asterisks.
func armRedaction(environ []string) {
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if ok && logging.SecretKey(name) {
			logging.Redact(value)
		}
	}
}

// resolveDir works out which directory holds the two files.
//
// The config is consulted rather than assumed, because ingest.outDir is configurable and a
// person debugging a site with a custom one should not have to type --dir every time. A config
// that will not load is not this program's problem to diagnose — `frznforge build` reports it
// properly — so it falls back to the documented default and lets the empty-file message say
// something useful instead.
func resolveDir(flags args) (string, error) {
	if flags.DirSet {
		return filepath.Abs(flags.Dir)
	}
	root, err := filepath.Abs(flags.Root)
	if err != nil {
		return "", err
	}
	if cfg, err := config.Load(root); err == nil && cfg.OutDir != "" {
		return cfg.OutDir, nil
	}
	return filepath.Join(root, "data"), nil
}

// serveTUI puts the console into raw mode and runs the loop, or explains why it could not and
// prints the listing instead.
func (a *app) serveTUI(dir string, data Data, filter LogFilter, flags args) error {
	restore, err := rawMode(a.Console.In, a.Console.Out)
	if err != nil {
		// Not fatal, and not silent either: the user asked for a screen and is getting a listing,
		// and being told why on stderr costs one line and saves the "why is it not interactive"
		// question. stderr so a redirected stdout still contains only the listing.
		fmt.Fprintln(a.Stderr, "frzndebugger: "+logging.Scrub(err.Error())+" -- printing the listing instead")
		return WritePlain(a.Stdout, data, filter, flags.Run, a.width())
	}
	// Deferred rather than called at the end of the loop: a panic inside the loop unwinds through
	// this, so the console mode is put back before the runtime prints the stack. A shell left in
	// raw mode is the one failure of a debugging tool that costs more than the bug it was opened
	// for.
	defer restore()

	size, stopSizer := newSizer(a.Console.Out)
	defer stopSizer()

	return runUI(a.Console.In, a.Console.Out, size, uiOptions{
		Data:   data,
		Filter: filter,
		Run:    flags.Run,
		Reload: func() Data { return Load(dir) },
	})
}

// serveWeb binds, prints the URL and blocks — the same Listen/Serve split as internal/serve and
// internal/wizard, for the same reason: with --port=0 the port is only knowable after the bind,
// and the URL has to be printable before the process disappears into Accept.
//
// Nothing opens a browser. `frznforge init --web` does, because a wizard is useless without one;
// this is a viewer opened from a terminal the user is already looking at, and the URL on stdout
// is both the whole interface and the only thing that works over SSH.
func (a *app) serveWeb(dir string, flags args) error {
	srv, err := ListenWeb(WebOptions{Dir: dir, Port: flags.Port})
	if err != nil {
		return err
	}
	// The directory is NOT scrubbed, unlike everything read out of the files. It is the argument
	// the user just typed, and a redactor that fires on a path segment — an environment variable
	// called *_SESSION whose value happens to be a directory name is enough — would print a path
	// that does not exist and send someone looking for a file that is not there.
	fmt.Fprintln(a.Stdout, "frzndebugger -- reading "+dir)
	fmt.Fprintln(a.Stdout, "  "+srv.URL)
	fmt.Fprintln(a.Stdout, "  Only this machine can reach it, and only with the key in that URL. Ctrl-C to stop.")

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	defer signal.Stop(stop)
	go func() {
		if _, ok := <-stop; ok {
			fmt.Fprintln(a.Stdout, "\nstopped")
			_ = srv.Close()
		}
	}()
	return srv.Serve()
}
