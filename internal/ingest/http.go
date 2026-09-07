package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"frznforge/internal/model"
)

// Port of src/lib/importers/http.ts: a tiny JSON-over-HTTP client plus the normalisation
// helpers every provider needs to turn its payloads into artifact shapes.
//
// Why it exists: the four providers differ in auth header, pagination header and error body,
// but everything else — timeouts, one retry, "never leak the token", mapping a failure onto an
// ImporterError kind — is identical, and getting that wrong is what breaks builds.
//
// Two rules this file enforces for everyone:
//   - The token only ever travels in a request header. It is never put in a URL and never
//     interpolated into a message; every message goes through RedactToken anyway.
//   - Nothing volatile is normalised. Download counters, star counts and "now" never reach a
//     value returned from here (the one exception is ImporterError.RetryAfter, which is a hint
//     to the caller and never reaches the artifact).

// AuthScheme is how a provider wants its token presented:
//   - AuthBearer       — `Authorization: Bearer <t>` (GitHub)
//   - AuthPrivateToken — `PRIVATE-TOKEN: <t>` (GitLab)
//   - AuthToken        — `Authorization: token <t>` (Gitea, Forgejo)
type AuthScheme string

const (
	AuthBearer       AuthScheme = "bearer"
	AuthPrivateToken AuthScheme = "private-token"
	AuthToken        AuthScheme = "token"
)

const (
	// DefaultHTTPTimeout abandons a request after this long; an unreachable forge must not
	// stall a build.
	DefaultHTTPTimeout = 20 * time.Second
	// DefaultMaxPages caps the pages followed by GetAll, so a huge repo cannot hang a build.
	// Hitting it is reported (PagedResult.Truncated), never silently swallowed.
	DefaultMaxPages = 20
	// defaultRetryDelay is the backoff before the single retry of a 5xx/network failure.
	defaultRetryDelay = 500 * time.Millisecond
	// attemptsPerRequest is one retry, i.e. two attempts total.
	attemptsPerRequest = 2
)

// Doer is the HTTP seam. *http.Client satisfies it; tests pass an implementation that serves
// recorded fixtures, which is how no test in this package reaches the network.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// defaultDoer is the client a build uses. Per-request deadlines come from the context, so no
// client-level timeout is set here.
var defaultDoer Doer = &http.Client{}

// UserAgentVersion is appended to the User-Agent as `frznforge/<version>` when it is set.
//
// The TypeScript reads the repository's VERSION file relative to its own source directory,
// which a compiled binary has no equivalent of; the command wiring sets this instead. Empty
// leaves the bare product name, which is a valid User-Agent.
var UserAgentVersion = ""

// DefaultUserAgentString is `frznforge`, or `frznforge/<version>` when UserAgentVersion is set.
func DefaultUserAgentString() string {
	if UserAgentVersion == "" {
		return DefaultUserAgent
	}
	return DefaultUserAgent + "/" + UserAgentVersion
}

// JSONClientOptions configures a JSONClient. Zero-valued fields take the defaults above.
type JSONClientOptions struct {
	// Auth is the token presentation for this provider.
	Auth AuthScheme
	// HTTP is injected for tests; nil means defaultDoer.
	HTTP Doer
	// Token is the resolved API token, or "" for an anonymous client.
	Token string
	// UserAgent defaults to DefaultUserAgentString().
	UserAgent string
	// Accept defaults to "application/json".
	Accept  string
	Timeout time.Duration
	// RetryDelay is the pause before the retry. Tests pass a negative value to disable it,
	// since zero means "use the default".
	RetryDelay time.Duration
	MaxPages   int
	// Backoff is the per-origin rate-limit gate. It defaults to the process-wide SharedBackoff
	// so every concurrently-ingested repo on one forge queues behind a single timer; tests pass
	// their own instance with an injected clock and sleep.
	Backoff *OriginBackoff
	// Now is injected for tests; defaults to time.Now. Only Retry-After arithmetic reads it,
	// and nothing derived from it reaches the artifact.
	Now func() time.Time
}

// PagedResult is one paginated GetAll walk: the items, and whether the page cap cut it short.
type PagedResult struct {
	Items []json.RawMessage
	// Truncated is true when the provider still had a next page when MaxPages ran out.
	Truncated bool
	// Pages actually fetched.
	Pages int
	// MaxPages is the cap that was in force.
	MaxPages int
}

// JSONClient is a GET-only JSON client for one provider API.
//
// Every failure surfaces as an *ImporterError with a kind the caller can map onto a remote-*
// warning; nothing here returns a bare error.
type JSONClient struct {
	doer       Doer
	token      string
	userAgent  string
	auth       AuthScheme
	accept     string
	timeout    time.Duration
	retryDelay time.Duration
	maxPages   int
	backoff    *OriginBackoff
	now        func() time.Time
	sleep      func(ctx context.Context, d time.Duration) error
}

// NewJSONClient builds a client from options, filling in every default.
func NewJSONClient(opts JSONClientOptions) *JSONClient {
	c := &JSONClient{
		doer:       opts.HTTP,
		token:      opts.Token,
		userAgent:  opts.UserAgent,
		auth:       opts.Auth,
		accept:     opts.Accept,
		timeout:    opts.Timeout,
		retryDelay: opts.RetryDelay,
		maxPages:   opts.MaxPages,
		backoff:    opts.Backoff,
		now:        opts.Now,
		sleep:      sleepCtx,
	}
	if c.doer == nil {
		c.doer = defaultDoer
	}
	if c.userAgent == "" {
		c.userAgent = DefaultUserAgentString()
	}
	if c.accept == "" {
		c.accept = "application/json"
	}
	if c.timeout == 0 {
		c.timeout = DefaultHTTPTimeout
	}
	if c.retryDelay == 0 {
		c.retryDelay = defaultRetryDelay
	} else if c.retryDelay < 0 {
		c.retryDelay = 0
	}
	if c.maxPages == 0 {
		c.maxPages = DefaultMaxPages
	}
	if c.backoff == nil {
		c.backoff = SharedBackoff
	}
	if c.now == nil {
		c.now = time.Now
	}
	return c
}

// Get fetches one JSON document and returns its raw bytes.
//
// The bytes are returned rather than decoded into a caller struct on purpose: the providers'
// payloads are read field by field through the tolerant helpers below, so a field of an
// unexpected type degrades to "absent" exactly as it does in the TypeScript, instead of failing
// the whole call.
func (c *JSONClient) Get(ctx context.Context, rawURL string) (json.RawMessage, error) {
	resp, err := c.request(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	return c.readJSON(rawURL, resp)
}

// GetAll fetches a JSON array, following pagination until the provider stops offering a next
// page or MaxPages is reached. It handles both styles in use: `Link: <…>; rel="next"`
// (GitHub/Gitea/Forgejo) and `x-next-page` (GitLab).
//
// Truncated is true when the page cap stopped the walk while the provider was still offering
// more. The caller must surface that: a short list and a capped one look identical in the
// artifact, and silently dropping a repo's older releases is worse than saying so.
func (c *JSONClient) GetAll(ctx context.Context, rawURL string) (PagedResult, error) {
	items := []json.RawMessage{}
	next := rawURL
	hasNext := true
	page := 0
	for ; hasNext && page < c.maxPages; page++ {
		current := next
		resp, err := c.request(ctx, current)
		if err != nil {
			return PagedResult{}, err
		}
		status := resp.StatusCode
		nextURL, hasNextPage := nextPageURL(resp.Header, current)
		body, err := c.readJSON(current, resp)
		if err != nil {
			return PagedResult{}, err
		}
		var batch []json.RawMessage
		if err := json.Unmarshal(body, &batch); err != nil {
			return PagedResult{}, &ImporterError{
				Kind:    KindBadResponse,
				Message: fmt.Sprintf("%s did not return a JSON array", describeURL(current, c.token)),
				Status:  status,
			}
		}
		items = append(items, batch...)
		next, hasNext = nextURL, hasNextPage
	}
	return PagedResult{Items: items, Truncated: hasNext, Pages: page, MaxPages: c.maxPages}, nil
}

// headers builds the headers for every request. The token appears here and nowhere else.
func (c *JSONClient) headers() http.Header {
	h := http.Header{}
	h.Set("Accept", c.accept)
	h.Set("User-Agent", c.userAgent)
	if c.token != "" {
		switch c.auth {
		case AuthPrivateToken:
			h.Set("PRIVATE-TOKEN", c.token)
		case AuthBearer:
			h.Set("Authorization", "Bearer "+c.token)
		default:
			h.Set("Authorization", "token "+c.token)
		}
	}
	return h
}

// request makes one request with a timeout, a single retry on a network error or a 5xx, and
// per-origin exponential backoff on rate limits. A 5xx that survives the retry is returned and
// classified as `network` by readJSON: from the build's point of view a broken forge and an
// unreachable one are the same thing.
//
// Rate limits are handled differently from 5xx because they are a property of the HOST, not of
// this request: the gate is keyed on the origin and shared across every client, so parallel
// repos on one forge wait together rather than each hammering the window. See OriginBackoff.
func (c *JSONClient) request(ctx context.Context, rawURL string) (*http.Response, error) {
	origin := OriginOf(rawURL)
	desc := describeURL(rawURL, c.token)
	var cause error
	// Rate-limit retries are counted separately from the 5xx/network retry: they have their own
	// budget and their own (much longer, host-wide) delays.
	rateLimitAttempt := 0

	for attempt := 1; attempt <= attemptsPerRequest; {
		resp, retryNow, err := c.attempt(ctx, rawURL, origin, desc, attempt, &rateLimitAttempt)
		if err != nil {
			// A rate limit is the host's answer, not this request's: it is never retried into,
			// and the caller turns it into a warning plus the cached-metadata fallback.
			if isRateLimitError(err) {
				return nil, err
			}
			cause = err
		} else if resp != nil {
			return resp, nil
		}
		// A rate-limit retry has its own budget and must not consume the 5xx/network one, so the
		// attempt counter does not advance for it.
		if retryNow {
			continue
		}
		if attempt < attemptsPerRequest && c.retryDelay > 0 {
			if serr := c.sleep(ctx, c.retryDelay); serr != nil {
				cause = serr
				break
			}
		}
		attempt++
	}

	return nil, &ImporterError{
		Kind:    KindNetwork,
		Message: fmt.Sprintf("%s failed: %s", desc, RedactToken(errorText(cause), c.token)),
		Cause:   cause,
	}
}

// attempt makes one pass of the retry loop. Exactly one of its three results is meaningful:
// a response to hand back, retryNow to go round again without spending an attempt, or an error
// worth remembering as the cause if every attempt fails.
func (c *JSONClient) attempt(
	ctx context.Context,
	rawURL, origin, desc string,
	attempt int,
	rateLimitAttempt *int,
) (resp *http.Response, retryNow bool, err error) {
	// A hard-blocked origin fails right here, costing no request at all.
	if err := c.backoff.BeforeRequest(ctx, origin, desc); err != nil {
		return nil, false, err
	}
	resp, err = c.send(ctx, rawURL)
	if err != nil {
		return nil, false, err
	}

	if isRateLimitResponse(resp) {
		*rateLimitAttempt++
		retryAfter, hasRetryAfter := parseRetryAfter(resp.Header, c.now())
		retry, noteErr := c.backoff.NoteRateLimit(ctx, origin, retryAfter, hasRetryAfter, *rateLimitAttempt)
		if noteErr != nil {
			drain(resp)
			return nil, false, noteErr
		}
		if retry {
			drain(resp)
			return nil, true, nil
		}
		return resp, false, nil // out of retries (or blocked): let readJSON classify it
	}

	c.backoff.NoteSuccess(origin)
	if resp.StatusCode < 500 || attempt == attemptsPerRequest {
		return resp, false, nil
	}
	drain(resp)
	return nil, false, fmt.Errorf("HTTP %d", resp.StatusCode)
}

func (c *JSONClient) send(ctx context.Context, rawURL string) (*http.Response, error) {
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	// Around the request, not after it. A provider that accepts the connection and then never
	// answers is indistinguishable from a hang unless something recorded the attempt — the
	// timeout above bounds it, but the log is what says which host and which URL.
	reqStarted := time.Now()
	slog.Debug("http start", "url", rawURL)

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header = c.headers()
	resp, err := c.doer.Do(req)
	if err != nil {
		slog.Debug("http failed", "url", rawURL, "ms", time.Since(reqStarted).Milliseconds(), "err", err)
		cancel()
		return nil, err
	}
	slog.Debug("http done", "url", rawURL, "ms", time.Since(reqStarted).Milliseconds(), "status", resp.StatusCode)
	// The body outlives this function, so the deadline has to as well: cancel once the body is
	// closed rather than on return.
	resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

// cancelOnClose ties a per-request context's cancel to the response body's lifetime.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// drain closes a response body that is being discarded on the way to a retry.
func drain(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	_ = resp.Body.Close()
}

// readJSON returns the response body, or the right *ImporterError for a failed one.
func (c *JSONClient) readJSON(rawURL string, resp *http.Response) (json.RawMessage, error) {
	text := bodyText(resp)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, c.toError(rawURL, resp, text)
	}
	if strings.TrimSpace(text) == "" {
		return nil, &ImporterError{
			Kind:    KindBadResponse,
			Message: fmt.Sprintf("%s returned an empty body", describeURL(rawURL, c.token)),
			Status:  resp.StatusCode,
		}
	}
	if !json.Valid([]byte(text)) {
		return nil, &ImporterError{
			Kind:    KindBadResponse,
			Message: fmt.Sprintf("%s returned a body that is not JSON", describeURL(rawURL, c.token)),
			Status:  resp.StatusCode,
		}
	}
	return json.RawMessage(text), nil
}

// toError maps a non-2xx response onto an *ImporterError. It never mentions the token.
//
// The message ends up verbatim inside a Warning in forge.json, which constrains it in two ways
// the raw provider response does not respect:
//   - nothing clock-derived. RetryAfter is a countdown, so it differs between two builds of the
//     same repos; it stays on the error for the console and is kept out of the message.
//   - no host identity. GitHub's anonymous rate-limit body quotes the caller's public IP
//     ("rate limit exceeded for 203.0.113.1"), which has no business being published in a build
//     artifact — ScrubIPs takes it out.
func (c *JSONClient) toError(rawURL string, resp *http.Response, text string) *ImporterError {
	detail := ScrubIPs(RedactToken(providerMessage(text), c.token))
	retryAfter, hasRetryAfter := parseRetryAfter(resp.Header, c.now())
	kind := ClassifyStatus(resp.StatusCode, resp.Header, detail)
	parts := []string{fmt.Sprintf("%s → HTTP %d", describeURL(rawURL, c.token), resp.StatusCode)}
	if detail != "" {
		parts = append(parts, detail)
	}
	if kind == KindAuth && c.token == "" {
		parts = append(parts, "no token configured for this source")
	}
	err := &ImporterError{Kind: kind, Message: strings.Join(parts, " — "), Status: resp.StatusCode}
	if hasRetryAfter {
		v := retryAfter
		err.RetryAfter = &v
	}
	return err
}

func isRateLimitError(err error) bool {
	var ie *ImporterError
	return errors.As(err, &ie) && ie.Kind == KindRateLimit
}

var (
	ipv4Re = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}\b`)
	// Four or more colon-separated hextets: enough to exclude a `10:30:45` clock time.
	ipv6Re = regexp.MustCompile(`(?i)\b(?:[0-9a-f]{1,4}:){3,7}[0-9a-f]{1,4}\b`)
)

// ScrubIPs replaces IPv4/IPv6 literals with `[ip]`.
//
// Provider error bodies quote the requester's own address (GitHub's anonymous rate-limit
// message is the common one). Those bodies are copied into artifact warnings, so the build
// host's public IP would otherwise be published with the site.
func ScrubIPs(text string) string {
	return ipv6Re.ReplaceAllString(ipv4Re.ReplaceAllString(text, "[ip]"), "[ip]")
}

/* ---- failure classification --------------------------------------------- */

// ClassifyStatus maps an HTTP status onto an ImporterErrorKind.
//
// 401/403 are ambiguous: GitHub answers a rate limit with 403 and a bad token with 401, so the
// headers decide. Anything 5xx counts as network — after the retry it is indistinguishable from
// an unreachable host and gets the same "use the cache" treatment.
func ClassifyStatus(status int, headers http.Header, detail string) ImporterErrorKind {
	if status == 404 || status == 410 {
		return KindNotFound
	}
	if status == 429 {
		return KindRateLimit
	}
	if status == 401 || status == 403 {
		if isRateLimited(headers, detail) {
			return KindRateLimit
		}
		return KindAuth
	}
	if status >= 500 {
		return KindNetwork
	}
	return KindBadResponse
}

// isRateLimitResponse reports whether a response is a rate limit, judged from status and
// headers alone.
//
// Deliberately header-only: this runs in request(), before the body has been read, and a 403
// from GitHub is a rate limit or a bad token depending entirely on the headers. ClassifyStatus
// later gets the body too and has the final say on the error kind.
func isRateLimitResponse(resp *http.Response) bool {
	if resp.StatusCode == 429 {
		return true
	}
	if resp.StatusCode != 403 && resp.StatusCode != 401 {
		return false
	}
	return isRateLimited(resp.Header, "")
}

var throttleRe = regexp.MustCompile(`(?i)rate limit|too many requests|throttl`)

// isRateLimited reports whether a response carries a rate-limit signal rather than an auth
// failure.
func isRateLimited(headers http.Header, detail string) bool {
	remaining, ok := headerGet(headers, "x-ratelimit-remaining")
	if !ok {
		remaining, ok = headerGet(headers, "ratelimit-remaining")
	}
	// `Number(remaining) === 0` in the original, and Number("") is 0 — a header that is present
	// but empty therefore counts as "no quota left". Kept, so the two agree on a malformed
	// header rather than one of them retrying into a wall.
	if ok && jsNumber(remaining) == 0 {
		return true
	}
	if _, ok := headerGet(headers, "retry-after"); ok {
		return true
	}
	return throttleRe.MatchString(detail)
}

// parseRetryAfter reports the seconds to wait before retrying, from Retry-After (delta seconds
// or an HTTP date) or a rate-limit reset header (epoch seconds). ok is false when the response
// says nothing.
func parseRetryAfter(headers http.Header, now time.Time) (seconds int, ok bool) {
	nowMS := float64(now.UnixMilli())
	if raw, present := headerGet(headers, "retry-after"); present && strings.TrimSpace(raw) != "" {
		if n, isNum := jsNumberOK(raw); isNum {
			return int(math.Max(0, jsRound(n))), true
		}
		if at, err := http.ParseTime(raw); err == nil {
			return int(math.Max(0, math.Ceil((float64(at.UnixMilli())-nowMS)/1000))), true
		}
	}
	raw, present := headerGet(headers, "x-ratelimit-reset")
	if !present {
		raw, present = headerGet(headers, "ratelimit-reset")
	}
	if present && strings.TrimSpace(raw) != "" {
		if v, isNum := jsNumberOK(raw); isNum {
			// Big values are an epoch (GitHub, GitLab); small ones are already a delta.
			s := v
			if v > 1e9 {
				s = (v*1000 - nowMS) / 1000
			}
			return int(math.Max(0, math.Ceil(s))), true
		}
	}
	return 0, false
}

// providerMessage is a best-effort human message out of a provider's error body; all four use
// `message`, `error_description` or `error`.
func providerMessage(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	var body map[string]json.RawMessage
	if json.Unmarshal([]byte(text), &body) != nil {
		// A non-JSON error body (an HTML proxy page, say) tells us nothing worth quoting.
		return ""
	}
	for _, key := range []string{"message", "error_description", "error"} {
		if v, ok := rawString(body[key]); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

/* ---- pagination ---------------------------------------------------------- */

// nextPageURL is the next page URL from the response headers; ok is false when this was the
// last page.
func nextPageURL(headers http.Header, currentURL string) (next string, ok bool) {
	if link, present := headerGet(headers, "link"); present && link != "" {
		if target, found := ParseLinkHeader(link)["next"]; found && target != "" {
			return resolveAgainst(target, currentURL), true
		}
	}
	// GitLab: no Link header on every deployment, but always x-next-page (empty on the last page).
	if page, present := headerGet(headers, "x-next-page"); present && strings.TrimSpace(page) != "" {
		return setQueryParam(currentURL, "page", strings.TrimSpace(page)), true
	}
	return "", false
}

var linkPartRe = regexp.MustCompile(`^<([^>]+)>\s*;\s*(.+)$`)
var linkRelRe = regexp.MustCompile(`rel\s*=\s*"?([^";]+)"?`)

// ParseLinkHeader turns `<url>; rel="next", <url>; rel="last"` into `{next, last}`.
func ParseLinkHeader(value string) map[string]string {
	links := map[string]string{}
	for _, part := range strings.Split(value, ",") {
		m := linkPartRe.FindStringSubmatch(strings.TrimSpace(part))
		if m == nil {
			continue
		}
		rel := linkRelRe.FindStringSubmatch(m[2])
		if rel == nil {
			continue
		}
		links[strings.TrimSpace(rel[1])] = strings.TrimSpace(m[1])
	}
	return links
}

// resolveAgainst resolves a possibly-relative next-page URL against the page it came from.
func resolveAgainst(target, base string) string {
	b, err := url.Parse(base)
	if err != nil || b.Scheme == "" {
		return target
	}
	ref, err := url.Parse(target)
	if err != nil {
		return target
	}
	return b.ResolveReference(ref).String()
}

// setQueryParam replaces (or appends) one query parameter, preserving the order and the
// encoding of the others. url.Values.Encode would sort the parameters, which changes a URL the
// provider handed us into a different one for no reason.
func setQueryParam(rawURL, key, value string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	replaced := false
	kept := make([]string, 0, 4)
	for _, pair := range strings.Split(u.RawQuery, "&") {
		if pair == "" {
			continue
		}
		name := pair
		if i := strings.IndexByte(pair, '='); i >= 0 {
			name = pair[:i]
		}
		if decoded, err := url.QueryUnescape(name); err == nil {
			name = decoded
		}
		if name != key {
			kept = append(kept, pair)
			continue
		}
		if !replaced {
			kept = append(kept, url.QueryEscape(key)+"="+url.QueryEscape(value))
			replaced = true
		}
	}
	if !replaced {
		kept = append(kept, url.QueryEscape(key)+"="+url.QueryEscape(value))
	}
	u.RawQuery = strings.Join(kept, "&")
	return u.String()
}

/* ---- normalisation shared by the providers ------------------------------- */

// explicitZoneRe matches a trailing `Z` or `±HH:MM` / `±HHMM` offset — i.e. the timestamp says
// which zone it is in.
var explicitZoneRe = regexp.MustCompile(`(?i)(?:Z|[+-]\d{2}:?\d{2})$`)
var clockRe = regexp.MustCompile(`\d{2}:\d{2}`)

// isoDateLayouts are the shapes a provider timestamp arrives in, once ToISODate has appended a
// Z to a zoneless one. RFC3339 covers `Z` and `±HH:MM`, with or without a fraction; the second
// covers the `±HHMM` form GitHub Enterprise and some proxies emit; the rest are date-only
// values, which the ECMAScript date parser reads as UTC.
var isoDateLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04:05Z0700",
	"2006-01-02",
	"2006-01",
	"2006",
}

// ToISODate normalises a provider timestamp to the artifact's IsoDate (UTC, seconds precision),
// or nil when it is missing or unparseable. Providers disagree wildly — GitLab sends
// milliseconds, Forgejo sends `+02:00` offsets — and the artifact has to be byte-identical
// between builds.
//
// A date-TIME carrying no zone (`2026-08-20T10:49:50`, or the space-separated form some
// self-hosted Gitea/Forgejo deployments emit) is read as UTC rather than in the build machine's
// local zone, which would shift the value in the artifact by that machine's offset. A bare date
// already means UTC.
func ToISODate(raw json.RawMessage) *string {
	value, ok := rawString(raw)
	if !ok {
		return nil
	}
	return toISODateString(value)
}

func toISODateString(value string) *string {
	trimmed := jsTrim(value)
	if trimmed == "" {
		return nil
	}
	candidate := trimmed
	if clockRe.MatchString(trimmed) && !explicitZoneRe.MatchString(trimmed) {
		// strings.Replace with n=1: the original is `String.prototype.replace` with a plain
		// string, which also replaces only the first occurrence.
		candidate = strings.Replace(trimmed, " ", "T", 1) + "Z"
	}
	for _, layout := range isoDateLayouts {
		if t, err := time.Parse(layout, candidate); err == nil {
			out := t.UTC().Format("2006-01-02T15:04:05") + "Z"
			return &out
		}
	}
	return nil
}

// AbsoluteURL resolves a provider URL against the repo's web base, so a host-relative one
// (GitLab hands out `/group/proj/-/releases/…/downloads/x` for release assets) does not end up
// in the artifact as a link that resolves against the GENERATED SITE and 404s. It returns the
// input unchanged when it cannot be resolved.
func AbsoluteURL(value, base string) string {
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	b, err := url.Parse(base)
	// A base without a scheme is not a URL to the WHATWG parser the original uses, and resolving
	// against it would silently produce a relative result.
	if err != nil || b.Scheme == "" || b.Host == "" {
		return value
	}
	ref, err := url.Parse(value)
	if err != nil {
		return value
	}
	return b.ResolveReference(ref).String()
}

// rawString decodes a JSON string value; ok is false for absent values and every other type.
func rawString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

// asObject decodes a JSON object into its raw fields, or nil when the value is not an object.
// Field lookups on a nil map yield the zero value, which is exactly what optional chaining does
// on the TypeScript side.
func asObject(raw json.RawMessage) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

// asArray decodes a JSON array into its raw elements, or nil when the value is not an array.
func asArray(raw json.RawMessage) []json.RawMessage {
	var a []json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &a) != nil {
		return nil
	}
	return a
}

// NullIfEmpty is a trimmed string, or nil when the value is absent, empty or not a string.
// Providers use `""` and `null` interchangeably.
func NullIfEmpty(raw json.RawMessage) *string {
	s, ok := rawString(raw)
	if !ok {
		return nil
	}
	return nullIfEmptyString(s)
}

func nullIfEmptyString(s string) *string {
	trimmed := jsTrim(s)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// firstNonNil is the `a ?? b` of the provider mappings.
func firstNonNil(values ...*string) *string {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}

func strPtr(s string) *string { return &s }

// StringArray is the non-empty strings out of a value that should have been a string array.
func StringArray(raw json.RawMessage) []string {
	out := []string{}
	for _, item := range asArray(raw) {
		s, ok := rawString(item)
		if !ok || jsTrim(s) == "" {
			continue
		}
		out = append(out, jsTrim(s))
	}
	return out
}

// ByteSize is a non-negative integer byte count, or 0 when the provider reports none.
func ByteSize(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var n float64
	if json.Unmarshal(raw, &n) != nil {
		return 0
	}
	if math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
		return 0
	}
	return int64(math.Floor(n))
}

// isTrue reports whether a raw value is literally `true` — the `=== true` of the original, so a
// field an unauthenticated payload omits reads as "unknown", never as false.
//
// The comparison is on the raw token rather than through Unmarshal because unmarshalling JSON
// null into a bool succeeds and leaves false, which would make isFalse(null) true and hide
// GitLab's "this field is simply absent for anonymous callers" behind an explicit denial.
func isTrue(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "true"
}

// isFalse reports whether a raw value is literally `false`.
func isFalse(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "false"
}

// CompareStrings is locale-independent string order, so the artifact does not depend on the
// build machine's ICU data.
func CompareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// SortAssets puts assets in a stable order: by name, then URL for ties.
func SortAssets(assets []model.ReleaseAsset) []model.ReleaseAsset {
	out := make([]model.ReleaseAsset, len(assets))
	copy(out, assets)
	sort.SliceStable(out, func(i, j int) bool {
		if c := CompareStrings(out[i].Name, out[j].Name); c != 0 {
			return c < 0
		}
		return CompareStrings(out[i].URL, out[j].URL) < 0
	})
	return out
}

// SortReleases puts releases newest first: publishedAt desc, tag asc as tiebreak.
func SortReleases(releases []model.Release) []model.Release {
	out := make([]model.Release, len(releases))
	copy(out, releases)
	sort.SliceStable(out, func(i, j int) bool {
		if c := CompareStrings(out[j].PublishedAt, out[i].PublishedAt); c != 0 {
			return c < 0
		}
		return CompareStrings(out[i].Tag, out[j].Tag) < 0
	})
	return out
}

// spdxByKey maps a provider license key onto a canonical SPDX id.
//
// GitHub reports spdx_id already canonical, GitLab reports a lowercase key ("mit") and Gitea an
// SPDX-ish string; this maps the ids frznforge's own detector emits so a repo shows the same
// license whether it was sniffed locally or imported.
var spdxByKey = map[string]string{
	"0bsd":         "0BSD",
	"agpl-3.0":     "AGPL-3.0-only",
	"apache-2.0":   "Apache-2.0",
	"artistic-2.0": "Artistic-2.0",
	"bsd-2-clause": "BSD-2-Clause",
	"bsd-3-clause": "BSD-3-Clause",
	"bsl-1.0":      "BSL-1.0",
	"cc0-1.0":      "CC0-1.0",
	"epl-2.0":      "EPL-2.0",
	"eupl-1.2":     "EUPL-1.2",
	"gpl-2.0":      "GPL-2.0-only",
	"gpl-3.0":      "GPL-3.0-only",
	"isc":          "ISC",
	"lgpl-2.1":     "LGPL-2.1-only",
	"lgpl-3.0":     "LGPL-3.0-only",
	"mit":          "MIT",
	"mit-0":        "MIT-0",
	"mpl-2.0":      "MPL-2.0",
	"ms-pl":        "MS-PL",
	"ncsa":         "NCSA",
	"ofl-1.1":      "OFL-1.1",
	"osl-3.0":      "OSL-3.0",
	"postgresql":   "PostgreSQL",
	"unlicense":    "Unlicense",
	"upl-1.0":      "UPL-1.0",
	"wtfpl":        "WTFPL",
	"zlib":         "Zlib",
}

// ToSpdx normalises a provider license id. Unknown ids pass through unchanged rather than being
// guessed at; NOASSERTION (GitHub's "there is a LICENSE but we can't tell what it is") and
// `other` become nil so the scanner's own detection wins.
func ToSpdx(raw json.RawMessage) *string {
	return toSpdxString(NullIfEmpty(raw))
}

func toSpdxString(value *string) *string {
	if value == nil {
		return nil
	}
	key := strings.ToLower(*value)
	if key == "noassertion" || key == "other" || key == "unknown" {
		return nil
	}
	if mapped, ok := spdxByKey[key]; ok {
		return &mapped
	}
	return value
}

/* ---- misc ---------------------------------------------------------------- */

// describeURL strips the query string from a URL for messages: it can carry ids we have no need
// to echo.
func describeURL(rawURL, token string) string {
	display := rawURL
	if u, err := url.Parse(rawURL); err == nil && u.Scheme != "" && u.Host != "" {
		display = u.Scheme + "://" + u.Host + u.EscapedPath()
	}
	return "GET " + RedactToken(display, token)
}

// bodyText reads a response body as text, tolerating a stream that fails mid-read.
func bodyText(resp *http.Response) string {
	if resp.Body == nil {
		return ""
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}
	return string(data)
}

func errorText(cause error) string {
	if cause == nil {
		return "<nil>"
	}
	return cause.Error()
}

// headerGet distinguishes an absent header from one that is present and empty, which
// http.Header.Get cannot — and which decides whether `x-ratelimit-remaining` reads as a rate
// limit.
func headerGet(h http.Header, name string) (string, bool) {
	if h == nil {
		return "", false
	}
	values, ok := h[textproto.CanonicalMIMEHeaderKey(name)]
	if !ok || len(values) == 0 {
		return "", false
	}
	return values[0], true
}

// jsNumber is `Number(s)`: a trimmed numeric string, 0 for the empty string, NaN otherwise.
func jsNumber(s string) float64 {
	v, ok := jsNumberOK(s)
	if !ok {
		return math.NaN()
	}
	return v
}

func jsNumberOK(s string) (float64, bool) {
	trimmed := jsTrim(s)
	if trimmed == "" {
		return 0, true
	}
	v, err := strconv.ParseFloat(trimmed, 64)
	if err != nil || math.IsInf(v, 0) || math.IsNaN(v) {
		return 0, false
	}
	return v, true
}

// jsRound is Math.round: half away from zero towards +Infinity, unlike Go's math.Round which is
// half away from zero in both directions.
func jsRound(v float64) float64 {
	return math.Floor(v + 0.5)
}
