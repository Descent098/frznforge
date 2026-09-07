package main

// The CLI's seam onto the outside world — the port of scripts/cli.ts's `Io`.
//
// Every command takes one of these instead of reaching for os.Stdin, os.Stdout, os.Getenv or
// time.Now. That is what makes `init` testable: the interactive picker is a loop over questions
// and answers, and a test that has to spawn a terminal to reach it is a test nobody writes.
//
// It carries more than three streams because the same reasoning applies to everything else the
// commands read from the process: the token environment (so a test never depends on the
// developer's own credentials), the working directory (so a fixture directory is a value, not a
// chdir), the HTTP client (so a listing can be served from a httptest server), and the clock (so
// the name of a .bak file is the name the test asserts).

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"frznforge/internal/ingest"
)

// Io is the process environment, injected.
//
// Every command takes it as a parameter named `io`, which shadows the standard library's io
// package inside those functions. That is deliberate — `io.log(…)` at a hundred call sites reads
// better than any alternative — but it means a function that needs an io.Reader or io.Writer of
// its own has to name the parameter something else or take the stream from this struct.
type Io struct {
	// In supplies the answers to prompts. Nil means "no answers": every question fails with
	// errInputClosed rather than blocking forever.
	In  io.Reader
	Out io.Writer
	Err io.Writer

	// IsTTY reports whether a person is there to answer a question. It is separate from In
	// because a piped run has an In that reads fine and still must never be *asked* anything:
	// a script that stops at an unanswerable prompt looks like a hang.
	IsTTY bool

	// Cwd is what a relative path on the command line resolves against.
	Cwd string

	// Env is the environment provider tokens are read from. Nil reads the real one; a non-nil
	// (even empty) map is used verbatim. See ingest.Env — the distinction is deliberate there
	// too, and this field must not collapse it.
	Env ingest.Env

	// Client is the HTTP client provider listings go through. Nil gets a 30-second default.
	Client *http.Client

	// Now supplies the timestamp a backup file is named after. Nil uses the wall clock.
	Now func() time.Time
}

// DefaultIo is the real process: the streams, the real environment, the real clock.
func DefaultIo() *Io {
	cwd, err := os.Getwd()
	if err != nil {
		// A process with no working directory cannot resolve a relative --config or scaffold
		// into "my-site", but it can still print help and run `verify /abs/path`. Empty Cwd is
		// the honest answer; the commands that need one join against "." and fail on their own.
		cwd = ""
	}
	return &Io{
		In:    os.Stdin,
		Out:   os.Stdout,
		Err:   os.Stderr,
		IsTTY: isTerminal(os.Stdin),
		Cwd:   cwd,
	}
}

// isTerminal reports whether f is a console rather than a pipe or a file.
//
// Stat's character-device bit rather than a syscall wrapper: it answers correctly on Windows
// consoles, Unix ttys and every redirection of both, and it costs no dependency.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func (io *Io) logf(format string, args ...any) {
	fmt.Fprintf(io.Out, format+"\n", args...)
}

// log prints one line verbatim. Separate from logf because most of what init prints is a
// pre-built line — a rendered config entry, a snippet — and passing those through a format
// string would let a `%` in a repository name eat the next argument.
func (io *Io) log(line string) {
	fmt.Fprintln(io.Out, line)
}

func (io *Io) errf(format string, args ...any) {
	fmt.Fprintf(io.Err, format+"\n", args...)
}

// clock is the time a run should call "now".
func (io *Io) clock() time.Time {
	if io.Now != nil {
		return io.Now()
	}
	return time.Now()
}

/* ---- prompting ------------------------------------------------------------ */

// errInputClosed ends a run whose questions can no longer be answered. Without it, the ask
// loops (askRequired, choose, confirm — all of which re-ask on a bad answer) would spin forever
// against a closed stdin.
var errInputClosed = errors.New("input ended before the question was answered — nothing was written")

// prompter asks line-based questions over Io's streams.
//
// Deliberately dumb, like the prompt.ts it ports: no raw mode, no cursor tricks, no spinners.
// The init flow is a handful of questions, and a plain line-based prompt behaves identically in
// Windows Terminal, PowerShell, a Git Bash pipe and a test's strings.Reader.
//
// Nothing here ever reads a secret. Tokens come from the environment only, so there is
// deliberately no masked-input helper to reach for.
type prompter struct {
	in  *bufio.Reader
	out io.Writer
}

func newPrompter(io *Io) *prompter {
	in := io.In
	if in == nil {
		in = strings.NewReader("")
	}
	return &prompter{in: bufio.NewReader(in), out: io.Out}
}

func (p *prompter) write(line string) { fmt.Fprintln(p.out, line) }

// ask puts one question and returns the trimmed answer, or fallback when the answer is empty.
func (p *prompter) ask(question, fallback string) (string, error) {
	suffix := ""
	if fallback != "" {
		suffix = " [" + fallback + "]"
	}
	fmt.Fprintf(p.out, "%s%s: ", question, suffix)
	line, err := p.in.ReadString('\n')
	// A final line with no newline is still an answer: `printf 'all'` (no \n) returns io.EOF
	// alongside the text, and treating that as "input ended" would throw away what was typed.
	if err != nil && line == "" {
		return "", errInputClosed
	}
	answer := strings.TrimSpace(line)
	if answer == "" {
		return fallback, nil
	}
	return answer, nil
}

// askRequired re-asks until the answer is non-empty.
func (p *prompter) askRequired(question, fallback string) (string, error) {
	for {
		answer, err := p.ask(question, fallback)
		if err != nil {
			return "", err
		}
		if answer != "" {
			return answer, nil
		}
		p.write("  a value is required.")
	}
}

// choice is one option in a numbered menu.
type choice struct {
	value string
	label string
	// hint is shown after the label, dash-separated.
	hint string
}

// choose prints a numbered menu and re-asks until the answer is in range.
func (p *prompter) choose(question string, choices []choice, defaultIndex int) (string, error) {
	for i, c := range choices {
		line := fmt.Sprintf("  %d. %s", i+1, c.label)
		if c.hint != "" {
			line += " — " + c.hint
		}
		p.write(line)
	}
	for {
		raw, err := p.ask(question, strconv.Itoa(defaultIndex+1))
		if err != nil {
			return "", err
		}
		n, convErr := strconv.Atoi(raw)
		if convErr == nil && n >= 1 && n <= len(choices) {
			return choices[n-1].value, nil
		}
		p.write(fmt.Sprintf("  pick a number between 1 and %d.", len(choices)))
	}
}

// confirm asks y/N. Anything unrecognised re-asks rather than being guessed at — this gates a
// file write, and reading "maybe" as "yes" is the one mistake that cannot be undone.
func (p *prompter) confirm(question string, defaultYes bool) (bool, error) {
	suffix := " [y/N]"
	if defaultYes {
		suffix = " [Y/n]"
	}
	for {
		raw, err := p.ask(question+suffix, "")
		if err != nil {
			return false, err
		}
		switch strings.ToLower(raw) {
		case "":
			return defaultYes, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		p.write("  answer y or n.")
	}
}
