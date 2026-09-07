// Command frznforge is the 0.4.0 engine: one binary that ingests repositories and renders the
// static site.
//
// It carries:
//
//   - `ingest`, the git-and-network half: it scans the configured repositories and writes
//     forge.json plus its blob and archive stores. Its acceptance bar is byte identity with
//     `npm run ingest` for the same repositories at the same commits.
//   - `build`, ingest plus the pure half: artifact in, static site out. `--no-ingest` renders
//     the artifact already on disk and reads no git and no network at all.
//   - `dev`, a static server over the last build, which is also what the e2e suite runs.
//   - `init` and `new`, the setup pair: pick repositories from a forge into an existing config,
//     or write a fresh site's files into an empty directory.
//   - `verify`, the wedge the rest of the rewrite is checked against: it proves Go reads and
//     re-emits the artifact byte for byte, so every later claim about the Go ingest being
//     correct is a diff rather than an opinion.
//   - `config migrate`, which converts a site's frznforge.config.ts into the JSONC the Go
//     loader reads — comments and all, since they are the configuration's documentation.
//
// Every command takes an *Io rather than reaching for os.Stdin/os.Stdout, the environment or
// the clock; see io.go for why.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"frznforge/internal/build"
	"frznforge/internal/config"
	"frznforge/internal/ingest"
	"frznforge/internal/logging"
	"frznforge/internal/model"
	"frznforge/internal/timings"
)

const usage = `frznforge — static forge site generator

Usage
  frznforge build [--no-ingest] [--no-cache] [--backfill-metadata] [--root=<dir>]
                  [--out=<dir>] [--workers=<n>] [--serial] [--postprocess=<cmd>] [-v]
                                    Scan the configured repositories, then render the site into
                                    dist/. This is the whole build: nothing else has to be run
                                    first.
                                    --no-ingest skips the scan and renders the artifact already
                                    on disk — for iterating on templates and styles, or after a
                                    --backfill-metadata run. It never creates an artifact, so it
                                    refuses when there is none rather than publishing an empty
                                    site over a good one.
                                    --no-cache and --backfill-metadata configure the ingest
                                    half; passing either with --no-ingest is a contradiction and
                                    is refused.
                                    --serial renders one page at a time; it exists so the
                                    parallel build can be compared against it.
                                    --postprocess runs your own command over dist/ once the
                                    build has succeeded, overriding postprocess.command for this
                                    run. frznforge itself never minifies, bundles or hashes.

  frznforge ingest [--no-cache] [--backfill-metadata] [--root=<dir>] [--out=<dir>]
                                    Scan the configured repositories into
                                    <ingest.outDir>/forge.json plus blobs/ and archives/, and
                                    render nothing. Use it when you want the artifact refreshed
                                    without waiting for a render.
                                    --no-cache ignores the provider, freshness and scan caches
                                    for this run; --backfill-metadata spends the provider quota
                                    only on repos that have no metadata yet.

  frznforge dev [--port=<n>] [--dir=<dir>] [--base=<path>] [--host=<addr>] [--quiet]
                                    Serve the last build over HTTP. Renders nothing and watches
                                    nothing — run "build" to see a change.

  frznforge init [--provider=<name>] [--account=<name>] [--select=<spec>] [--web] [...]
                                    Add repositories to ` + config.Filename + `: list an
                                    account's repositories on GitHub, GitLab, Gitea or Forgejo
                                    and pick the ones you want. Whatever the flags do not answer
                                    is asked at the prompt, so a run naming --provider,
                                    --account and --select needs no terminal and works in a
                                    script. Tokens are read from the environment only and are
                                    never written to the config.
                                    --web does the same in a local browser UI, plus the settings
                                    the prompt does not offer (theme, owner, orgs, profile.md).
                                    "frznforge init --help" lists every option.

  frznforge new <dir> [--force] [--dry-run]
                                    Scaffold the files you author — config, profile, notes,
                                    orgs, .gitignore, README — into a new directory. An existing
                                    file is never overwritten; --force only allows a directory
                                    that already has something in it.

  frznforge verify [<forge.json>]   Read an artifact, validate it, re-serialize it, and
                                    compare byte for byte with the file on disk.
                                    Defaults to data/forge.json.

  frznforge config migrate [--force]
                                    Convert ./frznforge.config.ts into
                                    ./` + config.Filename + `, comments and all. Refuses to
                                    overwrite an existing .jsonc unless --force is given.

  frznforge help                    This message.

Diagnostics
  --log[=<level>]                   Write what the run is doing to stderr, on any command:
                                    error, warn, info or debug (bare --log means debug).
                                    FRZNFORGE_LOG does the same without retyping the command.
                                    Every git call, every HTTP request, every subprocess and
                                    every wait on the worker pool is recorded before it starts
                                    and again when it finishes, so a run that stops names what
                                    it stopped on:
                                      frznforge build --log=debug 2> build.log

  build, ingest and dev also write two files into <ingest.outDir> (data/ by default) on every
  run, whether or not --log was given, so the run nobody was watching still leaves evidence:

    data/frznforge.log               everything the run did, at debug. Replaced each run.
    data/frznforge-timings.jsonl     how long each step took, one JSON object per line,
                                     appended so runs can be compared against each other.

  Neither file is ever published: they live beside forge.json, not in dist/. --log controls the
  terminal only and does not turn either of them off.
`

func main() {
	if err := run(os.Args[1:], DefaultIo()); err != nil {
		fmt.Fprintln(os.Stderr, "error: "+err.Error())
		os.Exit(1)
	}
}

// setUpLogging consumes a leading or trailing --log=<level> (and -v/--verbose as a shorthand for
// --log=debug on commands that do not already own -v), installs the logger, and returns the
// arguments with it removed.
//
// FRZNFORGE_LOG does the same thing without a flag, for the second attempt at a command that
// has already been typed once. The flag wins when both are given.
func setUpLogging(args []string, io *Io) []string {
	level := os.Getenv(logging.EnvVar)
	out := make([]string, 0, len(args))
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--log="):
			level = strings.TrimPrefix(a, "--log=")
		case a == "--log":
			// A bare --log means "as much as you have", which is what someone reaching for it
			// during a hang wants.
			level = "debug"
		default:
			out = append(out, a)
		}
	}
	if _, on := logging.Level(level); on {
		logging.Setup(level, io.Err)
		slog.Debug("frznforge starting", "args", strings.Join(args, " "), "cwd", io.Cwd)
	}
	return out
}

func run(args []string, io *Io) (err error) {
	// --log is read before the subcommand and stripped from the arguments, so every command
	// gets it without each one growing its own flag. It goes to stderr and is off unless asked
	// for: progress belongs on stdout, diagnostics do not, and redirecting one must not disturb
	// the other.
	args = setUpLogging(args, io)

	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(io.Out, usage)
		return nil
	}

	// The run log and the timings file, for the commands that have somewhere to put them. Opened
	// after --log so the stderr sink is already installed and the two compose, and closed however
	// this function returns. See diagnostics.go.
	step, stop := startDiagnostics(args[0], args[1:], io)
	// Named result, read here: the closer writes the run's verdict into both files, and a plain
	// `defer stop()` would have nothing to write it from.
	defer func() { stop(err) }()

	switch args[0] {
	case "ingest":
		return ingestCmd(args[1:], io, step)
	case "build":
		return buildCmd(args[1:], io, step)
	case "dev":
		return devCmd(args[1:], io, step)
	case "init":
		return initCmd(args[1:], io)
	case "new":
		return newCmd(args[1:], io)
	case "verify":
		path := filepath.Join("data", "forge.json")
		if len(args) > 1 {
			path = args[1]
		}
		return verify(path, io)
	case "config":
		return configCmd(args[1:], io)
	default:
		return fmt.Errorf("unknown command %q\n\n%s", args[0], usage)
	}
}

/* ---- build ---------------------------------------------------------------- */

// buildArgs is `frznforge build`'s command line, split by destination.
type buildArgs struct {
	// NoIngest skips the scan and renders whatever artifact is already on disk.
	NoIngest bool
	// Ingest are the flags that configure the scan half.
	Ingest ingest.IngestArgs
	// ingestGiven names the ingest flags that were actually passed, so the conflict message can
	// quote them back rather than describing them.
	ingestGiven []string
	Build       build.Options
	// Root is where the config is read from; it is also build.Options.Root.
	Root string
	// OutDirSet records whether --out was given, so `--out=` cannot be mistaken for the default.
	OutDirSet bool
}

// parseBuildArgs splits `frznforge build …` into its three destinations.
//
// Unlike scripts/build.ts, an unknown flag is an error rather than being forwarded: that script
// passed the leftovers to `astro build`, which reported its own bad input. There is no
// downstream any more — this binary is the renderer — so a flag nobody handles would silently
// do nothing.
func parseBuildArgs(argv []string) (buildArgs, error) {
	args := buildArgs{Root: "."}
	for _, a := range argv {
		switch {
		case a == "--no-ingest":
			args.NoIngest = true
		case a == "--no-cache":
			args.Ingest.NoCache = true
			args.ingestGiven = append(args.ingestGiven, a)
		case a == "--backfill-metadata":
			args.Ingest.BackfillMetadata = true
			args.ingestGiven = append(args.ingestGiven, a)
		case a == "-v" || a == "--verbose":
			args.Build.Verbose = true
		case strings.HasPrefix(a, "--root="):
			args.Root = strings.TrimPrefix(a, "--root=")
		case strings.HasPrefix(a, "--out="):
			args.Build.OutDir = strings.TrimPrefix(a, "--out=")
			args.OutDirSet = true
		case a == "--serial":
			args.Build.Workers = 1
		case strings.HasPrefix(a, "--workers="):
			raw := strings.TrimPrefix(a, "--workers=")
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 {
				return args, fmt.Errorf("build: --workers needs a positive number, got %q", raw)
			}
			args.Build.Workers = n
		case strings.HasPrefix(a, "--postprocess="):
			// Last one wins, as with every other repeated flag. An empty value is "not given" —
			// see internal/build/postprocess.go: it must not silence a configured hook.
			args.Build.Postprocess = strings.TrimPrefix(a, "--postprocess=")
		default:
			return args, fmt.Errorf("build: unknown flag %q\n\n%s", a, usage)
		}
	}
	args.Build.Root = args.Root
	if args.Ingest.NoCache && args.Ingest.BackfillMetadata {
		// The same refusal ParseIngestArgs makes, made here because `build` parses these itself:
		// --no-cache reads nothing from the provider cache, so every repo would look like a gap
		// and the run would be a full refetch wearing the wrong name.
		return args, errors.New("build: --no-cache and --backfill-metadata are opposites; pass only one")
	}
	// --no-ingest with ingest flags is a contradiction worth refusing: the caller has asked for
	// an ingest behaviour AND asked for no ingest, so one of the two was a mistake and silently
	// dropping either would be the wrong guess.
	if args.NoIngest && len(args.ingestGiven) > 0 {
		return args, fmt.Errorf(
			"build: --no-ingest cannot be combined with %s — those configure an ingest that --no-ingest skips. Drop one",
			strings.Join(args.ingestGiven, " "))
	}
	return args, nil
}

// buildCmd ingests and renders.
//
// step is the run's timings step, so the scan and the render are recorded as two children of one
// run instead of as two unrelated top-level spans.
func buildCmd(argv []string, io *Io, step *timings.Step) error {
	args, err := parseBuildArgs(argv)
	if err != nil {
		return err
	}

	// The artifact directory comes from the config, so --no-ingest can name the file it is
	// missing. A config that will not load is not this command's problem to diagnose — the
	// ingest below, or build.Run, reports it properly — so fall back to the documented default.
	outDir := filepath.Join(args.Root, "data")
	if cfg, err := config.Load(args.Root); err == nil {
		outDir = cfg.OutDir
	}
	artifactFile := filepath.Join(outDir, "forge.json")
	// Absolute, because config.Load resolves outDir to an absolute path and filepath.Rel cannot
	// relate an absolute target to a relative base — on Windows it fails outright, and the
	// message would name the full temp path instead of `data/forge.json`.
	root, err := filepath.Abs(args.Root)
	if err != nil {
		root = args.Root
	}

	if args.NoIngest {
		if _, err := os.Stat(artifactFile); err != nil {
			return errors.New(strings.Join(noIngestRefusal(root, artifactFile), "\n"))
		}
		io.logf("--no-ingest: rendering %s as it stands; no repo is fetched or scanned.",
			rel(root, artifactFile))
	} else {
		// Said out loud because it is the expensive half and the surprising one: `build` scans
		// before it renders, and a reader watching a log wants the flag that skips it named
		// where the time is being spent.
		io.log("frznforge build: scanning first — pass --no-ingest to render the artifact on disk instead.")
		// In-process, unlike scripts/build.ts, which had to spawn `tsx scripts/ingest.ts` and
		// forward signals to it. One process means one exit code and no shim to find on PATH.
		if err := runIngest(args.Root, "", args.Ingest, io, step); err != nil {
			// A failed ingest means the artifact is missing or stale, so rendering it would
			// publish something nobody asked for. Stop with the ingest's own message.
			return err
		}
		io.log("")
	}

	args.Build.PostprocessOut = io.Out
	args.Build.Step = step
	res, err := build.Run(args.Build)
	if err != nil {
		return err
	}
	io.logf("built %d files (%.1f MB) in %s",
		res.Routes, float64(res.Bytes)/(1024*1024), res.Elapsed.Round(time.Millisecond))
	return nil
}

// noIngestRefusal is what `--no-ingest` says when there is no artifact to render.
//
// Without this check the renderer would fail on its own — but with a message about a missing
// file rather than about the flag that made it matter, and the fix ("run it once without
// --no-ingest") would be nowhere on screen.
func noIngestRefusal(root, artifactFile string) []string {
	return []string{
		"--no-ingest needs an artifact to render, and there is none yet.",
		"",
		"  missing: " + rel(root, artifactFile),
		"",
		"  Run the ingest once first:",
		"",
		"    frznforge build       scan, then render",
		"    frznforge ingest      scan only",
		"",
		"  After that, --no-ingest re-renders that artifact as often as you like.",
	}
}

// rel is a path relative to the project root, forward-slashed — the form a reader can paste
// into a shell. It falls back to the absolute path when the target is outside the root.
func rel(root, target string) string {
	r, err := filepath.Rel(root, target)
	if err != nil || r == "" || strings.HasPrefix(r, "..") {
		return filepath.ToSlash(target)
	}
	return filepath.ToSlash(r)
}

/* ---- config --------------------------------------------------------------- */

func configCmd(args []string, io *Io) error {
	if len(args) == 0 {
		return fmt.Errorf("config needs a subcommand\n\n%s", usage)
	}
	switch args[0] {
	case "migrate":
		force := false
		for _, a := range args[1:] {
			switch a {
			case "--force", "-f":
				force = true
			default:
				return fmt.Errorf("unknown flag %q for `config migrate` (only --force)", a)
			}
		}
		return migrateConfig(force, io)
	default:
		return fmt.Errorf("unknown config subcommand %q — did you mean `config migrate`?", args[0])
	}
}

// migrateConfig converts the config in the current directory.
//
// It never overwrites without --force and never deletes the TypeScript file: for one phase the
// nothing reads the .ts any more, and a migration that destroys its own input leaves the user
// with nothing to compare against when a value looks wrong.
func migrateConfig(force bool, io *Io) error {
	src, err := os.ReadFile(config.TSFilename)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("no ./%s here — run this from the directory holding your config", config.TSFilename)
		}
		return fmt.Errorf("read ./%s: %w", config.TSFilename, err)
	}
	if _, err := os.Stat(config.Filename); err == nil && !force {
		return fmt.Errorf("./%s already exists — pass --force to overwrite it", config.Filename)
	}

	out, err := config.MigrateTS(src)
	if err != nil {
		return fmt.Errorf("./%s: %w", config.TSFilename, err)
	}
	if err := os.WriteFile(config.Filename, out, 0o644); err != nil {
		return fmt.Errorf("write ./%s: %w", config.Filename, err)
	}

	// The comment counts are the headline number: this converter exists to carry documentation
	// across, so a run that quietly halved it should be visible without opening the file.
	before, _ := config.CountCommentLines(src)
	after, _ := config.CountCommentLines(out)
	io.logf("wrote ./%s — %d lines, %d of them carrying comments (%d in ./%s)",
		config.Filename, countLines(out), after, before, config.TSFilename)

	// Parsing what was just written is the only claim worth making about it: the file is not a
	// migration until the loader that will read it from now on accepts it.
	cfg, err := config.ParseBytes(out)
	if err != nil {
		io.errf("warning: the converted file does not load yet:\n  %v\nEdit ./%s and re-check with `frznforge config migrate --force`.", err, config.Filename)
		return nil
	}
	io.logf("loads cleanly — %d repo(s), %d organization(s), %d contributor(s), palette %q",
		len(cfg.Repos), len(cfg.Organizations), len(cfg.Contributors), cfg.Theme.Palette)
	io.logf("./%s is left in place and nothing reads it any more — delete it whenever you are satisfied the conversion is right.", config.TSFilename)
	return nil
}

// countLines counts the lines in b. The final newline terminates the last line rather than
// starting an empty one, so a 83-line file that ends in \n is not reported as 84.
func countLines(b []byte) int {
	n := bytes.Count(b, []byte("\n"))
	if len(b) > 0 && b[len(b)-1] != '\n' {
		n++
	}
	return n
}

/* ---- verify --------------------------------------------------------------- */

// verify is the byte-identity oracle described in the package comment.
//
// It reports the FIRST differing byte with context rather than just "differs", because the
// failures this catches are things like a unicode escape or a dropped key — invisible in a
// diff of two 5 MB files unless something points at them.
func verify(path string, io *Io) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	data, err := model.Parse(raw)
	if err != nil {
		return err
	}
	out, err := model.Serialize(data)
	if err != nil {
		return err
	}
	if bytes.Equal(raw, out) {
		io.logf("ok %s — %d bytes, schema v%d, %d repos, %d notes, %d orgs, re-serialized byte for byte",
			path, len(raw), data.SchemaVersion, len(data.Repos), len(data.Notes), len(data.Organizations))
		return nil
	}
	return fmt.Errorf("re-serialized artifact differs from %s\n%s", path, describeDiff(raw, out))
}

// describeDiff points at the first differing byte with a window of context on both sides.
func describeDiff(want, got []byte) string {
	i := 0
	for i < len(want) && i < len(got) && want[i] == got[i] {
		i++
	}
	line := 1 + bytes.Count(want[:i], []byte("\n"))
	window := func(b []byte) string {
		lo := max(0, i-60)
		hi := min(len(b), i+60)
		return fmt.Sprintf("%q", string(b[lo:hi]))
	}
	return fmt.Sprintf("  first difference at byte %d (line %d)\n  on disk:  %s\n  ours:     %s\n  lengths:  %d on disk, %d ours",
		i, line, window(want), window(got), len(want), len(got))
}
