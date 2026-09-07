package ingest

// What the ingest is allowed to say out loud.
//
// The file sink turns debug logging on for EVERY run, which means `slog.Debug("http start",
// "url", rawURL)` now reaches disk on every build — and a provider's next-page link can carry a
// credential in its query string even though this client only ever sends one in a header. That
// combination is the reason redaction lives at the sink, and these tests are what say the two
// halves actually meet.

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"frznforge/internal/config"
	"frznforge/internal/logging"
)

// runLog opens the real file sink in a temp directory and returns a reader for what was written.
// The real sink, not a capture handler: the scrubber lives between slog and the file, and a test
// that bypassed it would prove nothing about the file a user pastes into an issue.
func runLog(t *testing.T) func() string {
	t.Helper()
	dir := t.TempDir()
	if err := logging.SetupFile(dir, io.Discard); err != nil {
		t.Fatalf("open the run log: %v", err)
	}
	t.Cleanup(func() { _ = logging.Close() })
	return func() string {
		// Close first: the handler flushes per record, but reading a file the sink still owns is
		// a race waiting to be a flake.
		if err := logging.Close(); err != nil {
			t.Fatalf("close the run log: %v", err)
		}
		raw, err := os.ReadFile(logging.LogPath(dir))
		if err != nil {
			t.Fatalf("read the run log: %v", err)
		}
		return string(raw)
	}
}

func TestATokenInAFetchURLNeverReachesTheRunLog(t *testing.T) {
	// The shape that matters: a URL the client did not build. GitLab hands back `?private_token=`
	// in a paginated link, and "http start" logs the URL verbatim by design — the sink is what
	// makes that safe.
	const secret = "glpat-notarealtokenvalue"
	read := runLog(t)

	target := "https://gitlab.example.com/api/v4/projects/1?private_token=" + secret
	client := NewJSONClient(JSONClientOptions{
		HTTP:       newScriptedDoer(httpFixtureRoute{Status: 200, Body: `{"id":1}`}),
		RetryDelay: -1,
		Backoff:    NewOriginBackoff(BackoffOptions{}),
	})
	if _, err := client.Get(context.Background(), target); err != nil {
		t.Fatalf("get: %v", err)
	}

	log := read()
	if !strings.Contains(log, "http start") || !strings.Contains(log, "http done") {
		t.Fatalf("the fetch was not logged around at all:\n%s", log)
	}
	if strings.Contains(log, secret) {
		t.Errorf("the token reached the run log:\n%s", log)
	}
	if !strings.Contains(log, "private_token="+logging.Redacted) {
		t.Errorf("the token parameter was not redacted in place:\n%s", log)
	}
}

func TestResolveTokenRegistersWhateverTheUserCalledTheirVariable(t *testing.T) {
	// logging harvests the environment by variable NAME, and `"tokenEnv": "MY_PAT"` defeats that
	// by construction. ResolveToken is the only place that sees the value, so it is the only
	// place that can register it — and this is what proves it does.
	const secret = "a-token-from-a-variable-nobody-could-guess"
	read := runLog(t)

	source := config.RepoSourceConfig{Type: "gitea", Owner: "o", Repo: "r", TokenEnv: "SOMETHING_HARMLESS"}
	if got := ResolveToken(source, Env{"SOMETHING_HARMLESS": secret}); got != secret {
		t.Fatalf("ResolveToken = %q", got)
	}
	// Any record at all: what matters is that the literal cannot appear in one from now on.
	logSomething(secret)

	if log := read(); strings.Contains(log, secret) {
		t.Errorf("a token from a user-named variable reached the run log:\n%s", log)
	}
}

func TestTheNetworkGitRunnerIsLoggedBeforeAndAfter(t *testing.T) {
	// A clone is the longest single thing a build does. Without a start record, twenty minutes of
	// silence and a hang look exactly alike.
	read := runLog(t)

	res, err := defaultGitRunner(context.Background(), []string{"--version"},
		GitRunContext{Dir: t.TempDir(), Timeout: 30 * time.Second})
	if err != nil {
		t.Skipf("git is not runnable here: %v", err)
	}
	if res.Code == nil || *res.Code != 0 {
		t.Skipf("git --version exited %v", res.Code)
	}

	log := read()
	for _, want := range []string{`msg="git-net start"`, `msg="git-net done"`, "args=--version"} {
		if !strings.Contains(log, want) {
			t.Errorf("the run log is missing %s:\n%s", want, log)
		}
	}
}

func TestABackoffWaitSaysSoRatherThanLookingLikeAHang(t *testing.T) {
	// A run parked in a rate-limit wait has no process running, no request in flight and nothing
	// on stdout. It is the one stall that is indistinguishable from a hang unless it announces
	// itself.
	read := runLog(t)

	slept := 0
	gate := NewOriginBackoff(BackoffOptions{
		BaseDelay: 2 * time.Second,
		Now:       time.Now,
		Jitter:    func() float64 { return 0 },
		Sleep: func(context.Context, time.Duration) error {
			slept++
			return nil
		},
	})
	// One rate-limited answer with a short Retry-After: short enough to wait out rather than to
	// block the origin, which is the branch that sleeps.
	if _, err := gate.NoteRateLimit(context.Background(), "https://api.example.com", 2, true, 1); err != nil {
		t.Fatalf("NoteRateLimit: %v", err)
	}
	if slept == 0 {
		t.Fatal("the gate did not wait, so there is nothing to have logged")
	}

	log := read()
	for _, want := range []string{"backoff scheduled", "backoff wait start", "api.example.com"} {
		if !strings.Contains(log, want) {
			t.Errorf("the run log is missing %q:\n%s", want, log)
		}
	}
}

// logSomething writes one record quoting text, through the real handler stack.
func logSomething(text string) {
	slog.Debug("a record that happens to quote a secret", "detail", text)
}
