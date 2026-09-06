package ingest

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"net/url"
	"sync"
	"time"
)

// Port of src/lib/importers/backoff.ts.

const (
	defaultBaseDelay   = 1 * time.Second
	defaultMaxDelay    = 60 * time.Second
	defaultMaxAttempts = 4
)

// OriginOf is the origin part of a URL, or the raw string when it will not parse.
//
// "Origin" is scheme + host + port, the same key `new URL(u).origin` produces, so
// https://git.example.com:3000 and https://git.example.com are different hosts and never
// share a limiter.
func OriginOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return rawURL
	}
	return u.Scheme + "://" + u.Host
}

// BackoffOptions configures an OriginBackoff. The zero value gives the production defaults.
type BackoffOptions struct {
	// BaseDelay is the first delay; it doubles per consecutive rate-limited response on the
	// same origin.
	BaseDelay time.Duration
	// MaxDelay is the ceiling for a delay we are willing to sleep through. Longer waits block
	// the origin instead.
	MaxDelay time.Duration
	// MaxAttempts is the total attempts per request, including the first.
	MaxAttempts int
	// Sleep is injected for tests, which advance a fake clock instead of waiting. It returns
	// an error only when the context is cancelled.
	Sleep func(ctx context.Context, d time.Duration) error
	// Now is injected for tests. Defaults to time.Now.
	Now func() time.Time
	// Jitter returns a factor in [0, 1); defaults to rand.Float64. Deterministic in tests.
	Jitter func() float64
}

// originState is one host's current limiter state.
type originState struct {
	// failures counts consecutive rate-limited responses; it drives the exponential growth.
	failures int
	// waitUntil is the instant before which no request to this origin may be sent.
	waitUntil time.Time
	// blockedUntil is the instant before which requests fail immediately instead of waiting.
	blockedUntil time.Time
}

// OriginBackoff is a per-origin rate-limit gate, shared by every JSONClient in the process.
//
// Why per ORIGIN and not per client: ingest runs ingest.concurrency repos at once, and on a
// corpus like "72 GitHub repos" all of them talk to api.github.com. A retry policy held per
// client would have each of those repos discover the same 429 independently and hammer the
// window in parallel. Keyed on the origin, the first repo to be limited makes every other
// request to that host wait behind the same timer, while a request to a different forge
// (codeberg, a self-hosted Gitea) is completely unaffected.
//
// Two kinds of limit, deliberately handled differently:
//
//   - Short (a Retry-After within MaxDelay, or no hint at all): wait, then retry, with
//     exponential growth per consecutive failure on that origin.
//   - Long (the provider says "come back in 40 minutes" — GitHub's hourly quota): do NOT
//     sleep through it and do NOT keep retrying. The origin is marked blocked until that
//     instant and every later request to it fails immediately with a rate-limit error, which
//     PrepareRemote already turns into a remote-rate-limited warning plus the cached-metadata
//     fallback. One repo pays for the discovery; the rest of the build degrades quickly and
//     finishes instead of making N × attempts doomed calls.
//
// Unlike the TypeScript original this is reached from several goroutines at once, so every
// read and write of a state goes through mu. The lock is never held across a sleep.
type OriginBackoff struct {
	mu     sync.Mutex
	states map[string]*originState

	baseDelay time.Duration
	maxDelay  time.Duration
	// MaxAttempts is the rate-limit retry budget, read by JSONClient for reporting.
	MaxAttempts int
	sleep       func(ctx context.Context, d time.Duration) error
	now         func() time.Time
	jitter      func() float64
}

// NewOriginBackoff builds a gate; a zero-valued field takes the production default.
func NewOriginBackoff(opts BackoffOptions) *OriginBackoff {
	b := &OriginBackoff{
		states:      map[string]*originState{},
		baseDelay:   opts.BaseDelay,
		maxDelay:    opts.MaxDelay,
		MaxAttempts: opts.MaxAttempts,
		sleep:       opts.Sleep,
		now:         opts.Now,
		jitter:      opts.Jitter,
	}
	if b.baseDelay == 0 {
		b.baseDelay = defaultBaseDelay
	}
	if b.maxDelay == 0 {
		b.maxDelay = defaultMaxDelay
	}
	if b.MaxAttempts == 0 {
		b.MaxAttempts = defaultMaxAttempts
	}
	if b.sleep == nil {
		b.sleep = sleepCtx
	}
	if b.now == nil {
		b.now = time.Now
	}
	if b.jitter == nil {
		b.jitter = rand.Float64
	}
	return b
}

// sleepCtx waits for d, or returns early when the context is done.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SharedBackoff is the process-wide gate. Every client uses it by default, which is the whole
// point: concurrency is per repo, but a provider's rate limit is per host.
var SharedBackoff = NewOriginBackoff(BackoffOptions{})

// state returns the origin's entry, creating it on first use. Callers hold mu.
func (b *OriginBackoff) state(origin string) *originState {
	s := b.states[origin]
	if s == nil {
		s = &originState{}
		b.states[origin] = s
	}
	return s
}

// BeforeRequest gates a request. It returns a rate-limit ImporterError when the origin is
// hard-blocked (so the caller degrades to its cache immediately), otherwise it sleeps out any
// pending short backoff.
//
// The wait is re-checked in a loop: another in-flight request may extend the window while this
// one is sleeping, and it must not slip through early. The hard-block check is deliberately
// made once, before the loop, exactly as the TypeScript does — a request already sleeping out
// a short window is allowed to finish its wait rather than being converted into a failure by
// something another goroutine discovered meanwhile.
func (b *OriginBackoff) BeforeRequest(ctx context.Context, origin, describeURL string) error {
	b.mu.Lock()
	s := b.state(origin)
	now := b.now()
	if s.blockedUntil.After(now) {
		seconds := int(math.Ceil(s.blockedUntil.Sub(now).Seconds()))
		b.mu.Unlock()
		return &ImporterError{
			Kind:       KindRateLimit,
			Message:    fmt.Sprintf("%s: %s is rate-limited for another %ds; not retrying", describeURL, origin, seconds),
			RetryAfter: &seconds,
		}
	}
	b.mu.Unlock()

	for {
		b.mu.Lock()
		remaining := b.state(origin).waitUntil.Sub(b.now())
		b.mu.Unlock()
		if remaining <= 0 {
			return nil
		}
		if err := b.sleep(ctx, remaining); err != nil {
			return err
		}
	}
}

// NoteSuccess records that a request to this origin came back healthy: forget the
// consecutive-failure count and any window it opened.
func (b *OriginBackoff) NoteSuccess(origin string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.state(origin)
	s.failures = 0
	s.waitUntil = time.Time{}
	s.blockedUntil = time.Time{}
}

// NoteRateLimit records a rate-limited response and decides what happens next.
//
// retryAfter is the provider's own advice in seconds; pass hasRetryAfter false when the
// response gave none. attempt is the number of rate-limited responses this request has seen,
// counted separately from the 5xx/network retry budget.
//
// It reports retry=true when the caller should try again (a short wait was scheduled, and this
// call has already slept it out), false when it should give up and let the cache take over. A
// non-nil error is a rate-limit ImporterError raised while waiting, because a concurrent
// caller hard-blocked the origin in the meantime.
func (b *OriginBackoff) NoteRateLimit(ctx context.Context, origin string, retryAfter int, hasRetryAfter bool, attempt int) (bool, error) {
	b.mu.Lock()
	s := b.state(origin)
	s.failures++

	// The provider's own number wins when it gave one; otherwise grow exponentially from the
	// consecutive-failure count, with jitter so parallel repos do not resynchronise. jitter is
	// called either way so that an injected deterministic sequence advances identically
	// whether or not the provider volunteered a Retry-After.
	exponent := s.failures - 1
	if exponent > 10 {
		exponent = 10
	}
	exponential := float64(b.baseDelay) * math.Pow(2, float64(exponent))
	jittered := time.Duration(exponential * (1 + b.jitter()))
	delay := jittered
	if hasRetryAfter {
		delay = time.Duration(retryAfter) * time.Second
	}

	if delay > b.maxDelay {
		// Too long to sit through. Block the origin for the whole stated window so every other
		// repo on this host fails fast into its cache instead of queueing behind it.
		s.blockedUntil = b.now().Add(delay)
		s.waitUntil = time.Time{}
		b.mu.Unlock()
		return false, nil
	}

	until := b.now().Add(delay)
	if until.After(s.waitUntil) {
		s.waitUntil = until
	}
	maxAttempts := b.MaxAttempts
	b.mu.Unlock()

	if attempt >= maxAttempts {
		return false, nil
	}
	if err := b.BeforeRequest(ctx, origin, origin); err != nil {
		return false, err
	}
	return true, nil
}

// BackoffState is a read-only view of one origin's limiter, for tests and build reporting.
type BackoffState struct {
	Failures int
	Waiting  time.Duration
	Blocked  time.Duration
}

// Peek reports an origin's current state.
func (b *OriginBackoff) Peek(origin string) BackoffState {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.state(origin)
	now := b.now()
	return BackoffState{
		Failures: s.failures,
		Waiting:  maxDuration(0, s.waitUntil.Sub(now)),
		Blocked:  maxDuration(0, s.blockedUntil.Sub(now)),
	}
}

// IsBlocked reports whether the origin is refusing requests outright (used for build reporting).
func (b *OriginBackoff) IsBlocked(origin string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.state(origin)
	return s.blockedUntil.After(b.now())
}

// Reset forgets every origin's state.
//
// An ingest process runs once, so nothing in a build needs this — it exists for long-lived
// processes and for tests, where one case's rate-limited fixture would otherwise block that
// host for every case that follows it in the same process.
func (b *OriginBackoff) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.states = map[string]*originState{}
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
