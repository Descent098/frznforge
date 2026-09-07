package build

// The postprocess hook: the one place a user's own tooling gets to touch the output.
//
// frznforge never minifies, bundles or hashes — 0.4.0's rule is that the browser gets the bytes
// that are on disk, and every asset is copied verbatim. That rule is worth keeping, but it is
// not worth *enforcing* on somebody who wants a minifier, a Brotli pass or an rsync, so this
// runs one user-supplied command over the finished output directory. The default is nothing:
// no command configured, no process started, no behaviour to explain.
//
// It runs AFTER the build, over the finished directory, and never as part of it. Anything the
// command does is outside the reproducibility guarantee the rest of this package makes — which
// is exactly why it is a separate step with its own name in the log rather than a stage of Run.
//
//	{
//	  "postprocess": {
//	    "command": "npx esbuild --minify --outdir=dist dist/js/*.js",
//	    "dir": "."          // optional; the project root by default
//	  }
//	}
//
// The command is handed to the platform shell (`sh -c`, or `cmd /C` on Windows) because that is
// how people write hooks — with a pipe, a glob or a `&&` in them — and a hand-rolled argv
// splitter would only be a quieter way of getting quoting wrong.
//
// # Where it fires
//
// Run (build.go) calls the hook itself, as its last act and only when every step before it
// returned nil. A command wiring `--postprocess=<cmd>` therefore passes it through
// Options.Postprocess and must NOT call Postprocess.Run a second time afterwards — that would
// run the user's minifier twice over its own output, which for anything non-idempotent is a
// corrupted site rather than a wasted second.
//
// # Precedence
//
// Most explicit first, and each level replaces only the command:
//
//	--postprocess=<cmd>      the flag, for this one run
//	$FRZNFORGE_POSTPROCESS   the machine, for a CI job with no business editing a checked-out file
//	postprocess.command      the project, checked in next to everything else it builds with
//
// An empty value at either of the first two levels is NOT a value: it never silences a
// configured hook, because `--postprocess=` with nothing after it is a typo and an unset
// variable exported as "" is a shell accident. Turning the hook off means deleting the block.
//
// `postprocess.dir` is not overridden by either. It says where this project's tools run, which
// does not stop being true because someone changed the command for one build.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"frznforge/internal/config"
)

// PostprocessEnvVar names the command in the environment, for a CI job that has no business
// editing the config file it checked out.
const PostprocessEnvVar = "FRZNFORGE_POSTPROCESS"

// Postprocess is the `postprocess` block with the behaviour attached. The fields and their JSON
// tags are config.PostprocessConfig's — one definition of the block, so the loader that
// validates it and the code that runs it cannot drift into disagreeing about what it looks like.
type Postprocess config.PostprocessConfig

// Configured reports whether anything would run.
func (p Postprocess) Configured() bool { return config.PostprocessConfig(p).Configured() }

// PostprocessFor resolves the hook from a config that is already loaded — the path Run takes,
// so a build parses frznforge.config.jsonc once and config.Validate has already had its say
// about the block.
func PostprocessFor(cfg *config.Config, override string) Postprocess {
	var p Postprocess
	if cfg != nil {
		p = Postprocess(cfg.Postprocess)
	}
	return applyOverrides(p, override)
}

// LoadPostprocess resolves the hook for the site at root, reading the config file itself.
//
// This is the entry point for a caller with no loaded config. It reads the file directly rather
// than through config.Load because the block configures the toolchain around a build rather than
// anything the pages render: nothing in the site schema depends on it, and reading it here keeps
// `--postprocess=…` working in a directory whose config does not load at all. A caller that has
// a *config.Config in hand wants PostprocessFor instead.
func LoadPostprocess(root, override string) (Postprocess, error) {
	var p Postprocess

	raw, err := os.ReadFile(filepath.Join(root, config.Filename))
	switch {
	case err == nil:
		var file struct {
			Postprocess *Postprocess `json:"postprocess"`
		}
		// Unknown keys are ignored here on purpose — every other key in the file belongs to
		// config.Load, which has already had its say by the time a build gets this far.
		if err := json.Unmarshal(config.StripJSONC(config.TrimBOM(raw)), &file); err != nil {
			return Postprocess{}, fmt.Errorf("%s: cannot read the postprocess block: %w", config.Filename, err)
		}
		if file.Postprocess != nil {
			p = *file.Postprocess
		}
		// The same validation config.Load would have applied. Skipping it here is how the lenient
		// path becomes the one where a broken block runs anyway.
		if err := config.ValidatePostprocess(config.PostprocessConfig(p)); err != nil {
			return Postprocess{}, fmt.Errorf("%s: %w", config.Filename, err)
		}
	case errors.Is(err, os.ErrNotExist):
		// No config file: the flag and the environment are the only ways to ask for a hook.
	default:
		return Postprocess{}, fmt.Errorf("read %s: %w", config.Filename, err)
	}

	return applyOverrides(p, override), nil
}

// applyOverrides layers the environment and the flag over the configured block. The precedence
// and the "empty is not a value" rule are the package comment's; this is the one place either is
// implemented, so the two entry points above cannot disagree about them.
func applyOverrides(p Postprocess, override string) Postprocess {
	if v := strings.TrimSpace(os.Getenv(PostprocessEnvVar)); v != "" {
		p.Command = v
	}
	if strings.TrimSpace(override) != "" {
		p.Command = override
	}
	return p
}

// Run runs the hook over outDir. A hook with no command does nothing and says nothing.
//
// Both of the command's streams are echoed to out, unseparated: a tool's progress and its
// warnings read as one ordered story in a build log, and splitting them only shuffles the two
// against each other.
func (p Postprocess) Run(root, outDir string, out io.Writer) error {
	if !p.Configured() {
		return nil
	}
	if out == nil {
		out = os.Stdout
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	outAbs, err := filepath.Abs(outDir)
	if err != nil {
		return err
	}
	workDir := rootAbs
	if p.Dir != "" {
		workDir = p.Dir
		if !filepath.IsAbs(workDir) {
			workDir = filepath.Join(rootAbs, workDir)
		}
	}
	if info, err := os.Stat(workDir); err != nil || !info.IsDir() {
		return fmt.Errorf("postprocess: working directory %s does not exist — fix `postprocess.dir` in %s",
			workDir, config.Filename)
	}

	fmt.Fprintf(out, "postprocess: %s\n", p.Command)
	started := time.Now()

	// The third process this program can start, and the only one it did not write: a user's
	// minifier that never returns hangs `frznforge build` exactly as a git that never returns
	// does, so it gets the same before-and-after pair. The command is the user's own text and can
	// carry anything — `curl -H "Authorization: Bearer $T"` is a realistic hook — so it reaches
	// the log only through the sink's scrubber, which strips an Authorization header and a token
	// query parameter whether or not this process ever knew the value.
	slog.Debug("postprocess start", "command", p.Command, "dir", workDir, "dist", outAbs)

	cmd := shellCommand(p.Command)
	cmd.Dir = workDir
	cmd.Stdout = out
	cmd.Stderr = out
	// The output directory travels in the environment rather than as an argument so the command
	// stays a plain shell line the user can paste into a terminal. FRZNFORGE_OUT_DIR is
	// deliberately NOT reused: that name already means the *ingest* output (data/), and a hook
	// that emptied it would delete the artifact instead of the site.
	cmd.Env = append(os.Environ(),
		"FRZNFORGE_DIST_DIR="+outAbs,
		"FRZNFORGE_ROOT="+rootAbs,
	)

	if err := cmd.Run(); err != nil {
		slog.Debug("postprocess done", "command", p.Command,
			"ms", time.Since(started).Milliseconds(), "err", err)
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return fmt.Errorf("postprocess: the command exited %d\n  command: %s\n  in:      %s\n"+
				"  The site in %s is already written and was left exactly as the command found it. "+
				"Fix the command (or drop --postprocess) and run `frznforge build` again",
				exit.ExitCode(), p.Command, workDir, outAbs)
		}
		return fmt.Errorf("postprocess: could not start the command: %w\n  command: %s\n"+
			"  It runs through %s, so it has to be something that shell can find",
			err, p.Command, shellName)
	}
	slog.Debug("postprocess done", "command", p.Command,
		"ms", time.Since(started).Milliseconds(), "err", nil)
	fmt.Fprintf(out, "postprocess: done in %s\n", time.Since(started).Round(time.Millisecond))
	return nil
}

// shellName and shellCommand — the platform's command interpreter and how to hand it a command
// line — live in postprocess_windows.go and postprocess_notwindows.go. This was one `if
// runtime.GOOS` until Windows quoting turned out to need syscall.SysProcAttr, which does not
// compile off Windows; the two files say why.
