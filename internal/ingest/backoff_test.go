package ingest

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// Per-origin rate-limit backoff. The motivating case is a corpus of ~72 GitHub repos ingested
// `concurrency` at a time: every one of them talks to api.github.com, so the retry policy has to
// be a property of the HOST, not of one client or one repo.
//
// Nothing here sleeps: the clock and the sleep are injected, and the assertions are about what
// was scheduled and how many requests actually went out.

// fakeClock is a backoff whose sleep advances a counter instead of waiting.
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	slept []time.Duration
	// park makes sleep never return, modelling a caller still waiting inside its backoff.
	park bool
}

func newFakeBackoff(t testing.TB, opts BackoffOptions) (*OriginBackoff, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: time.Unix(1_000_000, 0)}
	if opts.BaseDelay == 0 {
		opts.BaseDelay = time.Second
	}
	if opts.MaxDelay == 0 {
		opts.MaxDelay = 60 * time.Second
	}
	if opts.MaxAttempts == 0 {
		opts.MaxAttempts = 4
	}
	// Deterministic: assert the exact exponential ladder.
	opts.Jitter = func() float64 { return 0 }
	opts.Now = clock.read
	opts.Sleep = clock.sleep
	return NewOriginBackoff(opts), clock
}

func (c *fakeClock) read() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	c.slept = append(c.slept, d)
	park := c.park
	if !park {
		c.now = c.now.Add(d)
	}
	c.mu.Unlock()
	if park {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *fakeClock) sleeps() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]time.Duration, len(c.slept))
	copy(out, c.slept)
	return out
}

func wantSleeps(t testing.TB, got []time.Duration, want ...time.Duration) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("slept %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("slept %v, want %v", got, want)
		}
	}
}

const gh = "https://api.github.com"

func TestBackoffGrowsExponentiallyPerConsecutiveRateLimit(t *testing.T) {
	b, clock := newFakeBackoff(t, BackoffOptions{})
	ctx := context.Background()
	// No Retry-After: the delay comes from the consecutive-failure count. Jitter is 0, so the
	// ladder is exactly base, base*2, base*4.
	for attempt := 1; attempt <= 3; attempt++ {
		retry, err := b.NoteRateLimit(ctx, gh, 0, false, attempt)
		if err != nil || !retry {
			t.Fatalf("attempt %d: retry=%v err=%v", attempt, retry, err)
		}
	}
	wantSleeps(t, clock.sleeps(), time.Second, 2*time.Second, 4*time.Second)
}

func TestBackoffHonoursRetryAfter(t *testing.T) {
	b, clock := newFakeBackoff(t, BackoffOptions{})
	retry, err := b.NoteRateLimit(context.Background(), gh, 7, true, 1)
	if err != nil || !retry {
		t.Fatalf("retry=%v err=%v", retry, err)
	}
	wantSleeps(t, clock.sleeps(), 7*time.Second)
}

func TestBackoffStopsAtTheAttemptBudget(t *testing.T) {
	b, _ := newFakeBackoff(t, BackoffOptions{MaxAttempts: 2})
	ctx := context.Background()
	if retry, _ := b.NoteRateLimit(ctx, gh, 1, true, 1); !retry {
		t.Fatal("first rate limit should have been retried")
	}
	if retry, _ := b.NoteRateLimit(ctx, gh, 1, true, 2); retry {
		t.Fatal("the budget was spent; the caller must fall back to its cache")
	}
}

func TestBackoffBlocksRatherThanSleepsThroughALongLimit(t *testing.T) {
	// GitHub's hourly quota says "come back in 40 minutes". Sleeping that out would hang the
	// build; retrying would waste every repo's attempts. The origin is marked blocked instead.
	b, clock := newFakeBackoff(t, BackoffOptions{})
	ctx := context.Background()
	if retry, err := b.NoteRateLimit(ctx, gh, 2400, true, 1); retry || err != nil {
		t.Fatalf("retry=%v err=%v", retry, err)
	}
	wantSleeps(t, clock.sleeps()) // nothing was waited on
	if !b.IsBlocked(gh) {
		t.Fatal("the origin should be blocked")
	}

	err := b.BeforeRequest(ctx, gh, "GET /repos/x")
	if err == nil || !strings.Contains(err.Error(), "rate-limited for another") {
		t.Fatalf("BeforeRequest err = %v", err)
	}
	// ...and it is the kind that maps onto remote-rate-limited plus the cached fallback.
	var ie *ImporterError
	if !errors.As(err, &ie) || ie.Kind != KindRateLimit {
		t.Fatalf("want a rate-limit ImporterError, got %#v", err)
	}
	if ie.RetryAfter == nil || *ie.RetryAfter != 2400 {
		t.Fatalf("RetryAfter = %v", ie.RetryAfter)
	}
}

func TestBackoffIsolatesOrigins(t *testing.T) {
	b, clock := newFakeBackoff(t, BackoffOptions{})
	ctx := context.Background()
	if _, err := b.NoteRateLimit(ctx, gh, 30, true, 1); err != nil {
		t.Fatal(err)
	}
	wantSleeps(t, clock.sleeps(), 30*time.Second)

	// codeberg has its own (clean) state — no wait at all.
	if err := b.BeforeRequest(ctx, "https://codeberg.org", "GET /x"); err != nil {
		t.Fatal(err)
	}
	wantSleeps(t, clock.sleeps(), 30*time.Second)
	if got := b.Peek("https://codeberg.org").Failures; got != 0 {
		t.Fatalf("codeberg failures = %d", got)
	}
}

func TestBackoffSuccessClearsTheFailureCount(t *testing.T) {
	b, clock := newFakeBackoff(t, BackoffOptions{})
	ctx := context.Background()
	_, _ = b.NoteRateLimit(ctx, gh, 0, false, 1)
	_, _ = b.NoteRateLimit(ctx, gh, 0, false, 2)
	b.NoteSuccess(gh)

	before := len(clock.sleeps())
	if _, err := b.NoteRateLimit(ctx, gh, 0, false, 1); err != nil {
		t.Fatal(err)
	}
	got := clock.sleeps()[before:]
	// Back to the first rung, not the third.
	wantSleeps(t, got, time.Second)
}

func TestBackoffHoldsOneSharedWindowPerOrigin(t *testing.T) {
	// Modelling real concurrency: caller A hits the limit and is still sleeping while caller B
	// arrives. B must see A's window, not a fresh one.
	b, clock := newFakeBackoff(t, BackoffOptions{})
	clock.mu.Lock()
	clock.park = true
	clock.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	parked := make(chan struct{})
	go func() {
		defer close(parked)
		_, _ = b.NoteRateLimit(ctx, gh, 30, true, 1)
	}()
	// Wait for A to have opened the window before reading it.
	waitFor(t, func() bool { return b.Peek(gh).Waiting > 0 })

	if got := b.Peek(gh).Waiting; got != 30*time.Second {
		t.Fatalf("waiting = %v", got)
	}
	clock.advance(10 * time.Second)
	if got := b.Peek(gh).Waiting; got != 20*time.Second {
		t.Fatalf("waiting after 10s = %v", got)
	}
	// ...and a different origin is untouched by it.
	if got := b.Peek("https://codeberg.org").Waiting; got != 0 {
		t.Fatalf("codeberg waiting = %v", got)
	}
	clock.advance(20 * time.Second)
	if got := b.Peek(gh).Waiting; got != 0 {
		t.Fatalf("waiting after the window = %v", got)
	}
	cancel()
	<-parked
}

func TestOriginOfKeysOnSchemeHostAndPort(t *testing.T) {
	cases := map[string]string{
		"https://api.github.com/repos/a/b":      "https://api.github.com",
		"https://git.example.com:3000/api/v1/x": "https://git.example.com:3000",
		"https://gitlab.com/api/v4/y":           "https://gitlab.com",
		"not a url":                             "not a url",
		"https://codeberg.org/api/v1/repos/a/b": "https://codeberg.org",
	}
	for in, want := range cases {
		if got := OriginOf(in); got != want {
			t.Errorf("OriginOf(%q) = %q, want %q", in, got, want)
		}
	}
	// Different hosts must not collide.
	if OriginOf("https://codeberg.org/api/v1/x") == OriginOf("https://api.github.com/x") {
		t.Error("two forges collapsed onto one origin")
	}
}

/* ---- JSONClient rate-limit handling -------------------------------------- */

func rateLimitClient(doer Doer, backoff *OriginBackoff) *JSONClient {
	return NewJSONClient(JSONClientOptions{
		Auth: AuthBearer, HTTP: doer, RetryDelay: -1, Backoff: backoff,
	})
}

func TestClientRetriesA429WithoutSpendingThe5xxBudget(t *testing.T) {
	b, _ := newFakeBackoff(t, BackoffOptions{})
	doer := newScriptedDoer(
		httpFixtureRoute{Status: 429, Headers: map[string]string{"retry-after": "2"}},
		httpFixtureRoute{Status: 429, Headers: map[string]string{"retry-after": "2"}},
		httpFixtureRoute{Status: 200, Body: `{"name":"ok"}`},
	)
	raw, err := rateLimitClient(doer, b).Get(context.Background(), gh+"/repos/a/b")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if name := NullIfEmpty(asObject(raw)["name"]); name == nil || *name != "ok" {
		t.Fatalf("name = %v", name)
	}
	if doer.count() != 3 { // two rate-limited attempts then the good one
		t.Fatalf("requests = %d, want 3", doer.count())
	}
}

func TestClientTreatsGithub403WithNoQuotaLeftAsARateLimit(t *testing.T) {
	b, _ := newFakeBackoff(t, BackoffOptions{})
	doer := newScriptedDoer(
		httpFixtureRoute{Status: 403, Headers: map[string]string{"x-ratelimit-remaining": "0"}},
		httpFixtureRoute{Status: 200, Body: `{"name":"ok"}`},
	)
	if _, err := rateLimitClient(doer, b).Get(context.Background(), gh+"/repos/a/b"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if doer.count() != 2 {
		t.Fatalf("requests = %d, want 2", doer.count())
	}
}

func TestClientGivesUpAfterTheAttemptBudget(t *testing.T) {
	b, _ := newFakeBackoff(t, BackoffOptions{MaxAttempts: 2})
	doer := newScriptedDoer(httpFixtureRoute{Status: 429, Headers: map[string]string{"retry-after": "1"}})
	_, err := rateLimitClient(doer, b).Get(context.Background(), gh+"/repos/a/b")
	assertImporterKind(t, err, KindRateLimit)
	if doer.count() != 2 {
		t.Fatalf("requests = %d, want 2", doer.count())
	}
}

func TestClientHardBlockCostsNoFurtherRequests(t *testing.T) {
	// The 72-repo case: repo #1 discovers the hourly limit, repos #2..72 must not each make four
	// more doomed calls — they fail fast and fall back to their cached metadata.
	b, _ := newFakeBackoff(t, BackoffOptions{})
	doer := newScriptedDoer(httpFixtureRoute{Status: 429, Headers: map[string]string{"retry-after": "3600"}})
	client := rateLimitClient(doer, b)

	_, err := client.Get(context.Background(), gh+"/repos/a/b")
	assertImporterKind(t, err, KindRateLimit)
	afterFirst := doer.count()

	_, err = client.Get(context.Background(), gh+"/repos/c/d")
	assertImporterKind(t, err, KindRateLimit)
	if doer.count() != afterFirst {
		t.Fatalf("a blocked origin still sent %d requests", doer.count()-afterFirst)
	}

	// A different forge still works.
	other := newScriptedDoer(httpFixtureRoute{Status: 200, Body: `{"name":"ok"}`})
	if _, err := rateLimitClient(other, b).Get(context.Background(), "https://codeberg.org/api/v1/repos/a/b"); err != nil {
		t.Fatalf("codeberg: %v", err)
	}
	if other.count() != 1 {
		t.Fatalf("codeberg requests = %d", other.count())
	}
}

func TestClientDoesNotTreatAPlain401AsARateLimit(t *testing.T) {
	b, _ := newFakeBackoff(t, BackoffOptions{})
	doer := newScriptedDoer(httpFixtureRoute{Status: 401, Body: `{"message":"Bad credentials"}`})
	_, err := rateLimitClient(doer, b).Get(context.Background(), gh+"/repos/a/b")
	assertImporterKind(t, err, KindAuth)
	if doer.count() != 1 { // no retry ladder for a bad token
		t.Fatalf("requests = %d, want 1", doer.count())
	}
}

func assertImporterKind(t testing.TB, err error, want ImporterErrorKind) {
	t.Helper()
	var ie *ImporterError
	if !errors.As(err, &ie) {
		t.Fatalf("want an *ImporterError, got %v", err)
	}
	if ie.Kind != want {
		t.Fatalf("kind = %q, want %q (message: %s)", ie.Kind, want, ie.Message)
	}
}

// waitFor spins until cond holds, failing the test rather than hanging forever.
func waitFor(t testing.TB, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition never became true")
		}
		time.Sleep(time.Millisecond)
	}
}
