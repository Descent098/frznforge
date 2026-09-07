// Package wizard is the browser-based `frznforge init --web` — the port of
// scripts/lib/web-init.ts and the page it serves, scripts/lib/web-init-page.html.
//
// The page is already plain HTML and JavaScript with no build step, so it crosses over as an
// embedded asset rather than being rewritten. What changes is the server underneath it and the
// file it edits: JSONC instead of TypeScript, spliced the same comment-aware way so a person's
// comments and formatting survive an edit to the field next to them.
//
// One thing disappears entirely: scripts/lib/config-load.ts, a whole child process that existed
// only because tsx caches modules by path and the wizard could not re-read its own config
// in-process. A file is just a file: every handler here reads it fresh, and the memo, the
// generation counter and the 20-second spawn timeout all go with the child process.
//
// The security model, because a page on localhost is still a page:
//
//   - Bound to 127.0.0.1 only. Never 0.0.0.0: the wizard can write files, so it must not be
//     reachable from whatever network the machine happens to be on.
//   - A per-run session key, printed in the URL and required on every request, page included.
//     Loopback is reachable by a fetch() from any site the user has open; the key is what makes
//     that fail.
//   - Host and Origin pinning, which is what stops DNS rebinding: an attacker's name resolved to
//     127.0.0.1 still arrives carrying THEIR Host header.
//   - The provider token never reaches the browser and only ever goes to a host the terminal
//     authorised (hostTrustedForToken in providers.go).
//   - The browser never names a file. Every path written is derived here, from --config or from
//     findConfigFile.
package wizard

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"time"

	"frznforge/internal/ingest"
)

// Options steer one wizard session.
type Options struct {
	// Root is the directory the search for a config starts in.
	Root string
	// ConfigPath names the config file outright (`--config`). Empty means findConfigFile.
	ConfigPath string
	// Port to listen on. 0 picks an ephemeral one, which is what the e2e suite uses.
	Port int
	// NoOpen suppresses opening a browser — set by tests and by headless use.
	NoOpen bool
	// Out receives the human-facing lines (the URL, the session key, the summary).
	Out io.Writer

	// Provider, Host, Account and Releases are the terminal's own flags, offered to the page as
	// starting values. Host is load-bearing beyond that: it is the ONE non-default host this
	// session may send Provider's token to (hostTrustedForToken).
	Provider string
	Host     string
	Account  string
	Releases string

	// Env is the environment provider tokens are read from. Nil reads the real one; a non-nil
	// (even empty) map is used verbatim, which is how a test avoids the developer's own tokens.
	Env ingest.Env
	// Client is the HTTP client used for provider listings. Nil gets a 30-second default.
	Client *http.Client
	// Now supplies the timestamp a backup file is named after. Nil uses the wall clock; a test
	// pins it so the name it asserts on is the name it gets.
	Now func() time.Time
}

// Result is what a finished session reports.
type Result struct {
	// URL the wizard listened on, including the session key.
	URL string
	// Writes counts the saves this session made — 0 means nothing on disk changed.
	Writes int
}

// Serve runs the wizard until the visitor presses Done, and returns when the server has stopped.
//
// Listen is split out because with --port=0 the chosen port is only knowable after the bind, and
// the URL has to be printable before the process disappears into Accept — the same split, for
// the same reason, as internal/serve.
func Serve(opts Options) (Result, error) {
	session, err := Listen(opts)
	if err != nil {
		return Result{}, err
	}
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	fmt.Fprintln(out, "frznforge init — the wizard is open in your browser.")
	fmt.Fprintln(out, "  "+session.URL)
	fmt.Fprintln(out, "  Only this machine can reach it, and only with the key in that URL. Ctrl-C to stop.")
	if session.ConfigPath == "" {
		fmt.Fprintln(out, "  No frznforge.config.jsonc found — the wizard will show a snippet to paste instead.")
	}
	if !opts.NoOpen {
		openBrowser(session.URL, out)
	}

	// Ctrl-C is the documented way out of this command, so it ends the session properly — with
	// the summary line, rather than the process vanishing and leaving the user wondering whether
	// their last save landed. Only this wrapper does it: Listen is what a test drives, and a
	// library that installs signal handlers behind a caller's back is a nuisance.
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	defer signal.Stop(interrupt)
	go func() {
		if _, ok := <-interrupt; ok {
			session.finish("\nInterrupted. "+session.wroteSummary(), nil)
		}
	}()

	err = session.Serve()
	return Result{URL: session.URL, Writes: session.Writes()}, err
}

// openBrowser is best effort. A failure is not an error: the URL is on stdout either way, which
// is also the only thing that works over SSH.
func openBrowser(url string, out io.Writer) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		shell := os.Getenv("ComSpec")
		if shell == "" {
			shell = "cmd.exe"
		}
		// The empty argument is `start`'s title parameter: without it, a quoted URL would be
		// taken as the window title and no browser would open.
		cmd = exec.Command(shell, "/d", "/s", "/c", "start", "", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(out, "  (could not open a browser automatically — open the URL above)")
		return
	}
	// Nothing waits for it: the browser outlives the wizard, and Wait would hold a zombie for
	// however long the user leaves the tab open.
	go func() { _ = cmd.Wait() }()
}
