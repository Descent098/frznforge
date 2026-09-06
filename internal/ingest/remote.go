package ingest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"frznforge/internal/config"
	"frznforge/internal/model"
)

// Port of src/lib/ingest/remote.ts: turn a configured provider repo into something the local
// scanner can read.
//
// Two halves, deliberately kept apart:
//   - METADATA comes from the provider REST API through an Importer (importers.go);
//   - GIT comes from a plain `git clone --mirror` into <ingest.cacheDir>/…, which ScanRepo then
//     reads exactly as it reads a local bare repo.
//
// Both halves are cached under the cache dir: the mirror as a bare repo, the importer's
// normalised answers next to it as `<repo>.meta.json`. An offline build (or one whose provider
// API is down) therefore keeps the repo's description, topics, links and releases instead of
// silently publishing it stripped bare.
//
// Invariants:
//   - A build must never fail because a forge is down, private or rate limited. Every failure
//     becomes a model.Warning and, where a cache exists, the cached data is used instead.
//   - Tokens come from the environment only and never reach disk, a log line, a warning or the
//     artifact. The clone credential is passed per-invocation through the child's environment
//     (see AuthEnv) — not on the remote URL, so it cannot land in .git/config, and not on argv,
//     so it is not readable in the OS process list by other users on the machine.
//   - Nothing volatile enters the artifact: mirror actions are reported back to the caller for
//     console output only.

// DefaultGitTimeout is the hard ceiling on a single network git invocation.
const DefaultGitTimeout = 300 * time.Second

// MirrorAction is what EnsureMirror did (or could not do).
//
// "reused" is the freshness window (ingest.reuse): the last successful fetch was recent enough
// that neither the provider API nor the mirror was touched — deliberately fresh, not stale.
type MirrorAction string

const (
	MirrorCloned  MirrorAction = "cloned"
	MirrorFetched MirrorAction = "fetched"
	MirrorCached  MirrorAction = "cached"
	MirrorMissing MirrorAction = "missing"
	MirrorReused  MirrorAction = "reused"
	MirrorCurrent MirrorAction = "current"
)

// GitRunResult is one finished network-git process.
type GitRunResult struct {
	// Code is the exit status, or nil when the process was killed rather than exiting.
	Code *int
	// Signal names the signal that killed it — "SIGKILL" for a timeout — and is empty otherwise.
	Signal string
	Stdout string
	Stderr string
}

// GitRunContext is everything a GitRunner needs besides the argv.
type GitRunContext struct {
	Dir     string
	Timeout time.Duration
	// Env is extra environment for the child — this is where the clone credential travels.
	Env map[string]string
}

// GitRunner is an injectable git runner so tests can observe (or refuse) every invocation.
type GitRunner func(ctx context.Context, args []string, opts GitRunContext) (GitRunResult, error)

// EnsureMirrorOptions are the knobs one mirror refresh reads.
type EnsureMirrorOptions struct {
	// Fetch is the network policy, from ingest.fetch: "auto", "never" or "always".
	Fetch string
	// CloneURL is the URL to clone from. It must not carry credentials — the token is sent as a
	// header.
	CloneURL string
	// Token is the API/clone token, already resolved from the environment.
	Token string
	// Timeout is the per-invocation ceiling; zero means DefaultGitTimeout.
	Timeout time.Duration
	// Run is an injectable git runner (tests); nil means the real one.
	Run GitRunner
	// SkipUnchanged is ingest.reuse.skipUnchanged: ask the remote what refs it has (one
	// `git ls-remote`) and skip `git remote update` entirely when the mirror already matches.
	// It needs KnownHeads; without a baseline there is nothing to compare against.
	SkipUnchanged bool
	// KnownHeads is the mirror's refs at the end of the previous run (RunLogEntry.Heads).
	// Compared against the remote's; ALL refs equal means the fetch has nothing to do.
	KnownHeads map[string]string
}

// EnsureMirrorResult is what one mirror refresh settled on.
type EnsureMirrorResult struct {
	// Path is the mirror path, whether or not it exists.
	Path   string
	Action MirrorAction
	// Error says why the mirror could not be refreshed or created. Already redacted; safe to
	// show.
	Error error
}

/* ---- git plumbing -------------------------------------------------------- */

// mirrorEnvOverrides is the non-interactive environment for network git.
//
// GIT_TERMINAL_PROMPT=0 plus an empty askpass means a private repo without a usable token fails
// fast instead of blocking the build on a credential prompt — a GUI helper would otherwise hang
// a CI run forever.
var mirrorEnvOverrides = map[string]string{
	"GIT_TERMINAL_PROMPT": "0",
	"GIT_ASKPASS":         "",
	"SSH_ASKPASS":         "",
	"GCM_INTERACTIVE":     "Never",
	"GIT_CONFIG_NOSYSTEM": "1",
	"GIT_OPTIONAL_LOCKS":  "0",
	"LC_ALL":              "C",
	"LANG":                "C",
	"GIT_PAGER":           "cat",
}

// mirrorEnv is the process environment with the non-interactive overrides and `extra` applied.
// The overrides are appended in sorted order so two runs build byte-identical environments.
func mirrorEnv(extra map[string]string) []string {
	overrides := make(map[string]string, len(mirrorEnvOverrides)+len(extra))
	for k, v := range mirrorEnvOverrides {
		overrides[k] = v
	}
	for k, v := range extra {
		overrides[k] = v
	}
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, kv := range os.Environ() {
		name := kv
		if eq := strings.IndexByte(kv, '='); eq >= 0 {
			name = kv[:eq]
		}
		if _, replaced := overrides[name]; !replaced {
			env = append(env, kv)
		}
	}
	names := make([]string, 0, len(overrides))
	for k := range overrides {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		env = append(env, k+"="+overrides[k])
	}
	return env
}

// defaultGitRunner runs one network git invocation, turning a timeout into a killed process
// rather than an error so the caller can report it as a timeout.
func defaultGitRunner(ctx context.Context, args []string, opts GitRunContext) (GitRunResult, error) {
	runCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "git", args...)
	cmd.Dir = opts.Dir
	cmd.Env = mirrorEnv(opts.Env)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	res := GitRunResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		res.Signal = "SIGKILL"
		return res, nil
	}
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			// A spawn that dies — an over-long destination path on Windows is the easy repro —
			// must reach the caller as an error result, never as a panic that takes the whole
			// ingest down over one unreachable remote.
			return GitRunResult{}, fmt.Errorf("failed to run git (is it installed and on PATH?): %w", err)
		}
		code := exitErr.ExitCode()
		res.Code = &code
		return res, nil
	}
	zero := 0
	res.Code = &zero
	return res, nil
}

// AuthEnv is the git configuration for one invocation, passed through the child's ENVIRONMENT.
//
// Three things are being avoided at once:
//   - a token in the remote URL (https://token@host/…) would be written into the mirror's
//     `config` file and sit on disk forever;
//   - a token in `-c http.extraheader=…` on argv is readable by every other process on the
//     machine for the duration of the clone (/proc/<pid>/cmdline, Get-CimInstance
//     Win32_Process), which matters on shared CI runners;
//   - a configured credential helper could pop a GUI dialog and stall the build forever, so it
//     is disabled with an empty credential.helper whether or not there is a token.
//
// GIT_CONFIG_COUNT/GIT_CONFIG_KEY_n/GIT_CONFIG_VALUE_n apply to this process and the transport
// helpers it spawns, and are never persisted into the cloned repository.
//
// The username half of the basic credential is provider-specific: GitLab only accepts a personal
// access token when the username is `oauth2`, while GitHub/Gitea/Forgejo accept the conventional
// `x-access-token`.
func AuthEnv(provider, token string) map[string]string {
	env := map[string]string{
		"GIT_CONFIG_COUNT":   "1",
		"GIT_CONFIG_KEY_0":   "credential.helper",
		"GIT_CONFIG_VALUE_0": "",
	}
	if token == "" {
		return env
	}
	user := "x-access-token"
	if provider == "gitlab" {
		user = "oauth2"
	}
	basic := base64.StdEncoding.EncodeToString([]byte(user + ":" + token))
	env["GIT_CONFIG_COUNT"] = "2"
	env["GIT_CONFIG_KEY_1"] = "http.extraheader"
	env["GIT_CONFIG_VALUE_1"] = "Authorization: Basic " + basic
	return env
}

var credentialInURLRe = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/\s@]+@`)

// redactSecrets strips anything credential-shaped out of text bound for a warning or a log line.
func redactSecrets(text, token string) string {
	out := RedactToken(text, token)
	if token != "" {
		out = strings.ReplaceAll(out, base64.StdEncoding.EncodeToString([]byte(token)), "***")
	}
	// https://user:pass@host/… → https://***@host/…
	return credentialInURLRe.ReplaceAllString(out, "${1}***@")
}

// gitStderrSummary collapses git's stderr into one short, stable sentence.
func gitStderrSummary(stderr, token string) string {
	parts := make([]string, 0, 4)
	for _, line := range splitLines(stderr) {
		if trimmed := jsTrim(line); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	redacted := redactSecrets(strings.Join(parts, "; "), token)
	// The cut is in UTF-16 units because the original is `String.prototype.slice`; git's stderr
	// is effectively always ASCII, so the surrogate-splitting corner this shares with JS has no
	// reachable case.
	if utf16Len(redacted) > 300 {
		return utf16Slice(redacted, 297) + "…"
	}
	return redacted
}

func utf16Slice(s string, units int) string {
	encoded := utf16.Encode([]rune(s))
	if units >= len(encoded) {
		return s
	}
	return string(utf16.Decode(encoded[:units]))
}

func gitFailure(label string, r GitRunResult, token string, timeout time.Duration) error {
	if r.Signal != "" {
		return fmt.Errorf("%s timed out after %ds", label, int(jsRound(timeout.Seconds())))
	}
	code := "null"
	if r.Code != nil {
		code = fmt.Sprint(*r.Code)
	}
	detail := gitStderrSummary(r.Stderr, token)
	if detail == "" {
		return fmt.Errorf("%s failed (exit %s)", label, code)
	}
	return fmt.Errorf("%s failed (exit %s): %s", label, code, detail)
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// mirrorLocks serialises EnsureMirror per destination: two sources resolving to one mirror would
// otherwise race, and the loser's cleanup (removing a failed clone) would delete the winner's
// fresh mirror. The cache path is derived from the source's identity so that is near-impossible,
// but a hand-written duplicate config entry is still legal and must not corrupt anything.
var mirrorLocks = &keyedMutex{}

type keyedMutex struct {
	mu sync.Mutex
	m  map[string]*keyedMutexEntry
}

type keyedMutexEntry struct {
	mu   sync.Mutex
	refs int
}

// lock takes the per-key mutex and returns its release, dropping the entry once nothing is
// queued behind it so the map does not grow for the life of the process.
func (k *keyedMutex) lock(key string) func() {
	k.mu.Lock()
	if k.m == nil {
		k.m = map[string]*keyedMutexEntry{}
	}
	e := k.m[key]
	if e == nil {
		e = &keyedMutexEntry{}
		k.m[key] = e
	}
	e.refs++
	k.mu.Unlock()

	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		k.mu.Lock()
		e.refs--
		if e.refs == 0 {
			delete(k.m, key)
		}
		k.mu.Unlock()
	}
}

// EnsureMirror makes sure cachePath holds a usable bare mirror of source, honouring the fetch
// policy:
//
//   - "never"  — never touches the network: the existing cache ("cached") or "missing".
//   - "auto"   — `git clone --mirror` when the cache is absent, otherwise
//     `git remote update --prune`; a failed refresh falls back to the cache
//     ("cached") with Error set so the caller can warn.
//   - "always" — same as "auto" today. The distinction is kept so a future freshness heuristic
//     can make "auto" skip a recent fetch without changing configs.
//
// It never returns a Go error for a network or auth problem: the failure is in Result.Error, and
// the build carries on with whatever cache it has.
func EnsureMirror(ctx context.Context, source config.RepoSourceConfig, cachePath string, opts EnsureMirrorOptions) EnsureMirrorResult {
	release := mirrorLocks.lock(cachePath)
	defer release()
	return ensureMirrorLocked(ctx, source, cachePath, opts)
}

func ensureMirrorLocked(ctx context.Context, source config.RepoSourceConfig, cachePath string, opts EnsureMirrorOptions) EnsureMirrorResult {
	run := opts.Run
	if run == nil {
		run = defaultGitRunner
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = DefaultGitTimeout
	}
	token := opts.Token

	exists, err := IsGitRepo(ctx, cachePath)
	if err != nil {
		// git could not be started at all. That is neither "there is a cache" nor "there is
		// none", so say so rather than guessing and reporting a misleading "not a repository".
		return EnsureMirrorResult{Path: cachePath, Action: MirrorMissing, Error: err}
	}

	if opts.Fetch == "never" {
		if exists {
			return EnsureMirrorResult{Path: cachePath, Action: MirrorCached}
		}
		return EnsureMirrorResult{
			Path:   cachePath,
			Action: MirrorMissing,
			Error:  fmt.Errorf("no cached mirror at %s and ingest.fetch is 'never'", cachePath),
		}
	}

	env := AuthEnv(source.Type, token)
	// attempt runs git, turning even a failed spawn into something the caller can warn about.
	attempt := func(args []string, dir string) (GitRunResult, error) {
		return run(ctx, args, GitRunContext{Dir: dir, Timeout: timeout, Env: env})
	}

	if exists {
		// Same-commit skip: one cheap ls-remote against the remote's ref advertisement, versus
		// the refs this mirror ended the last run with. Mirrors fetch per REPOSITORY, so the only
		// honest granularity is "every ref matches" — then `remote update` provably has nothing
		// to do. Any difference, or any failure of the probe, falls through to a normal update:
		// the skip may never be the reason a change is missed.
		if opts.SkipUnchanged && len(opts.KnownHeads) > 0 {
			probe, probeErr := attempt([]string{"ls-remote", "--heads", "--tags", "--", opts.CloneURL}, cachePath)
			if probeErr == nil && probe.Code != nil && *probe.Code == 0 {
				if RefsEqual(ParseRefLines(probe.Stdout), opts.KnownHeads) {
					return EnsureMirrorResult{Path: cachePath, Action: MirrorCurrent}
				}
			}
		}
		r, runErr := attempt([]string{"-C", cachePath, "remote", "update", "--prune"}, cachePath)
		if runErr != nil {
			return EnsureMirrorResult{Path: cachePath, Action: MirrorCached, Error: runErr}
		}
		if r.Code != nil && *r.Code == 0 {
			return EnsureMirrorResult{Path: cachePath, Action: MirrorFetched}
		}
		return EnsureMirrorResult{
			Path:   cachePath,
			Action: MirrorCached,
			Error:  gitFailure("git remote update", r, token, timeout),
		}
	}

	if pathExists(cachePath) {
		return EnsureMirrorResult{
			Path:   cachePath,
			Action: MirrorMissing,
			Error:  fmt.Errorf("%s exists but is not a git repository; delete it and re-run", cachePath),
		}
	}

	parent := filepath.Dir(cachePath)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return EnsureMirrorResult{Path: cachePath, Action: MirrorMissing, Error: err}
	}
	r, runErr := attempt([]string{"clone", "--mirror", "--quiet", "--", opts.CloneURL, cachePath}, parent)
	if runErr == nil && r.Code != nil && *r.Code == 0 {
		return EnsureMirrorResult{Path: cachePath, Action: MirrorCloned}
	}
	// A half-written clone would make every later run fail the "exists but is not a repo" check.
	// Safe to remove: the lock plus the pathExists check above mean this call created it.
	_ = os.RemoveAll(cachePath)
	failure := runErr
	if failure == nil {
		failure = gitFailure("git clone --mirror", r, token, timeout)
	}
	return EnsureMirrorResult{Path: cachePath, Action: MirrorMissing, Error: failure}
}

/* ---- provider URLs ------------------------------------------------------- */

// isHTTPURL reports whether value parses as an http(s) URL — the gate on every provider-supplied
// link before it becomes an artifact value.
func isHTTPURL(value *string) bool {
	if value == nil || *value == "" {
		return false
	}
	u, err := url.Parse(*value)
	if err != nil || u.Host == "" {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

var apiV3Re = regexp.MustCompile(`/api/v3/?$`)

// providerWebBase is the human-facing base URL of the instance; the API base is not always
// browsable.
func providerWebBase(source config.RepoSourceConfig) string {
	host := trimTrailingSlashes(source.Host)
	if source.Type != "github" {
		return host
	}
	u, err := url.Parse(host)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return host
	}
	if u.Hostname() == "api.github.com" {
		return "https://github.com"
	}
	// GitHub Enterprise: https://git.example.com/api/v3 → https://git.example.com
	u.Path = apiV3Re.ReplaceAllString(u.Path, "")
	u.RawPath = ""
	return trimTrailingSlashes(u.String())
}

// deriveWebURL is a best-effort repo page URL, used when the API could not be reached.
func deriveWebURL(source config.RepoSourceConfig) string {
	tail := source.Owner + "/" + source.Repo
	if source.Type == "gitlab" {
		tail = source.Project
	}
	segments := nonEmptySegments(tail)
	encoded := make([]string, len(segments))
	for i, seg := range segments {
		encoded[i] = encodeURIComponent(seg)
	}
	return providerWebBase(source) + "/" + strings.Join(encoded, "/")
}

// SourceRepoName is the repo name a remote source carries in the config, before any provider
// call: the `repo` field, or the last segment of a GitLab `project` path. Used for the default
// slug and display name so neither depends on the lossy cache directory name.
func SourceRepoName(source config.RepoSourceConfig) string {
	if source.Type != "gitlab" {
		return source.Repo
	}
	segments := nonEmptySegments(source.Project)
	if len(segments) == 0 {
		return source.Project
	}
	return segments[len(segments)-1]
}

// buildRepoSource assembles the artifact's `source` value. model.RepoSource declares its fields
// in the order each union member emits them, so each variant here fills exactly its own keys.
func buildRepoSource(source config.RepoSourceConfig, webURL, cloneURL string) *model.RepoSource {
	out := &model.RepoSource{
		Type:     source.Type,
		Host:     strPtr(source.Host),
		WebURL:   strPtr(webURL),
		CloneURL: strPtr(cloneURL),
	}
	if source.Type == "gitlab" {
		out.Project = strPtr(source.Project)
		return out
	}
	out.Owner = strPtr(source.Owner)
	out.Repo = strPtr(source.Repo)
	return out
}

/* ---- prepareRemote ------------------------------------------------------- */

// PrepareRemoteDeps are the seams PrepareRemote takes so a test can run it with no network.
type PrepareRemoteDeps struct {
	// CreateImporter is the importer factory; nil means the real registry. Return an untyped nil
	// to model "this source has no importer" — a typed nil pointer is a non-nil interface and
	// would be called.
	CreateImporter func(source config.RepoSourceConfig, ctx ImporterContext) Importer
	// EnsureMirror is the mirror driver; nil means EnsureMirror.
	EnsureMirror func(ctx context.Context, source config.RepoSourceConfig, cachePath string, opts EnsureMirrorOptions) EnsureMirrorResult
	// Git is an injectable git runner, forwarded to EnsureMirror.
	Git GitRunner
	// HTTP is an injectable HTTP client, forwarded to the importer.
	HTTP Doer
	// Env is the environment the token is read from; nil means the process environment.
	Env Env
	// Now is the clock, injected so tests stay deterministic. Nothing derived from it reaches
	// the artifact — it only ever lands in the run log.
	Now func() time.Time
	// Timeout is a per-invocation git timeout override.
	Timeout time.Duration
	// Backoff is the per-origin rate-limit gate handed to the importer; nil means SharedBackoff.
	Backoff *OriginBackoff
}

// FetchStatus records how each half of a remote fetch went, for the run log.
type FetchStatus struct {
	// Git is nil when git was not attempted at all (backfill mode), so the caller carries the
	// previous run's answer forward rather than recording a failure that never happened.
	Git *bool
	// Meta is true only when every provider call this run wanted actually succeeded — a releases
	// failure that fell back to cache is still a failed metadata fetch.
	Meta bool
}

// PrepareRemoteResult is one resolved remote source.
type PrepareRemoteResult struct {
	// ScanSource is ready to hand to ScanRepo (only meaningful when Ready).
	ScanSource ScanSource
	// ProviderMeta is provider metadata as a lowest-precedence metadata layer, or nil when
	// unavailable.
	ProviderMeta *config.RepoMetaInput
	// Releases are the provider releases (empty in tag mode or when the call failed).
	Releases []model.Release
	// Warnings carry Repo nil — the caller stamps the slug.
	Warnings []model.Warning
	Action   MirrorAction
	// Ready is false when there is no mirror to scan; the caller must skip the repo.
	Ready bool
	// FetchStatus is nil when no fetch was attempted at all — the freshness window skipped it —
	// and the caller keeps the previous run's record rather than inventing one.
	//
	// Reported here rather than inferred from the warning codes because remote-cache-stale is
	// raised both for a mirror that could not be refreshed and for provider metadata served from
	// cache: the code alone cannot tell the two halves apart.
	FetchStatus *FetchStatus
}

// PrepareRemoteOptions are the per-run skips the caller has already decided on.
type PrepareRemoteOptions struct {
	// SkipFetch is the freshness window (ingest.reuse): the caller has established that this
	// source was fully freshly fetched moments ago. When the provider cache and the mirror both
	// exist, the network is skipped entirely and their contents are used with NO warning — they
	// are what a fetch would have returned, so the artifact bytes cannot differ. It falls back to
	// a real fetch when either is missing.
	SkipFetch bool
	// NoCacheReads is --no-cache: never read the provider .meta.json (failures then degrade
	// harder).
	NoCacheReads bool
	// SkipUnchanged is ingest.reuse.skipUnchanged, and KnownHeads is the baseline it needs: the
	// mirror's refs at the end of the previous run. Together they let EnsureMirror prove a fetch
	// is unnecessary.
	SkipUnchanged bool
	KnownHeads    map[string]string
	// BackfillMetadata is --backfill-metadata: spend the provider's rate limit ONLY on repos that
	// have no cached metadata yet, and do not touch git at all.
	//
	// The case it exists for: a large account against an anonymous or nearly-spent API quota.
	// Cloning is cheap and unmetered, so every mirror succeeds and every repo gets its commits —
	// but metadata is metered, and a full run re-requests it for every repo, including the ones
	// whose cached answer is already on disk. The quota runs out partway through and the same
	// tail of repos is left blank on every subsequent run, because each run spends the budget the
	// same way before reaching them.
	//
	// In this mode a repo with cached metadata makes no API call and no git call (its cached
	// answer is used with no warning — the same reasoning as the freshness window: it is exactly
	// what a fetch would have returned), so the whole budget goes to the repos that have nothing.
	BackfillMetadata bool
}

func warn(code, message string) model.Warning {
	return model.Warning{Code: code, Repo: nil, Message: message}
}

// importerWarning maps an importer failure onto the right remote-* warning code.
func importerWarning(err error, what string, source config.RepoSourceConfig, token string) model.Warning {
	message := what + ": " + redactSecrets(err.Error(), token)
	var ie *ImporterError
	if errors.As(err, &ie) {
		if ie.Kind == KindRateLimit {
			return warn("remote-rate-limited", message)
		}
		if (ie.Kind == KindAuth || ie.Kind == KindNotFound) && token == "" {
			return warn("remote-auth-missing", fmt.Sprintf("%s (no API token found in %s)",
				message, strings.Join(TokenEnvFor(source), " or ")))
		}
	}
	return warn("remote-fetch-failed", message)
}

// metaLayer turns provider metadata into the RepoMetaInput layer the scanner merges BELOW the
// repo's own .frznforge.json. Only fields the data model actually has survive; anything that
// would fail validation (a non-URL homepage, an over-long description) is dropped or truncated
// here rather than blowing up artifact writing.
//
// The assembled layer is validated before it leaves, for the same reason ReadRepoMetaFile
// validates the in-repo file: this is remote data, and one bad field must degrade to a warning,
// never to a hard failure that kills the whole build.
func metaLayer(meta *ImportedRepoMeta, warnings *[]model.Warning) *config.RepoMetaInput {
	layer := &config.RepoMetaInput{}
	if meta.Name != nil && *meta.Name != "" {
		layer.Name = meta.Name
	}
	if meta.Description != nil && *meta.Description != "" {
		if isDescriptionTooLong(*meta.Description) {
			truncated := truncateDescription(*meta.Description)
			layer.Description = &truncated
			*warnings = append(*warnings, warn("description-truncated",
				fmt.Sprintf("provider description exceeded %d characters and was truncated", MaxDescription)))
		} else {
			layer.Description = meta.Description
		}
	}
	links := &config.RepoLinks{}
	if isHTTPURL(meta.Homepage) {
		links.Homepage = meta.Homepage
	}
	if isHTTPURL(meta.IssuesURL) {
		links.Issues = meta.IssuesURL
	}
	if meta.WebURL != "" && isHTTPURL(&meta.WebURL) {
		links.Upstream = strPtr(meta.WebURL)
	}
	if links.Homepage != nil || links.Issues != nil || links.Donations != nil || links.Upstream != nil {
		layer.Links = links
	}
	topics := make([]string, 0, len(meta.Topics))
	for _, t := range meta.Topics {
		if trimmed := jsTrim(t); trimmed != "" {
			topics = append(topics, trimmed)
		}
	}
	if len(topics) > 0 {
		layer.Tags = topics
	}
	if meta.Template {
		layer.Template = &meta.Template
	}
	if meta.License != nil && *meta.License != "" {
		layer.License = meta.License
	}
	return validateLayer(layer, warnings)
}

// validateLayer drops whatever the artifact schema would reject, with a warning naming the
// fields. It never fails.
func validateLayer(layer *config.RepoMetaInput, warnings *[]model.Warning) *config.RepoMetaInput {
	bad := metaLayerBadFields(layer)
	if len(bad) == 0 {
		return layer
	}
	rest := *layer
	for _, field := range bad {
		switch field {
		case "name":
			rest.Name = nil
		case "description":
			rest.Description = nil
		case "links":
			rest.Links = nil
		case "tags":
			rest.Tags = nil
		case "license":
			rest.License = nil
		case "releaseMode":
			rest.ReleaseMode = nil
		}
	}
	*warnings = append(*warnings, warn("remote-fetch-failed",
		"provider metadata failed validation; dropped field(s): "+strings.Join(bad, ", ")))
	if len(metaLayerBadFields(&rest)) > 0 {
		return &config.RepoMetaInput{}
	}
	return &rest
}

// metaLayerBadFields lists the top-level fields of a RepoMetaInput that the schema rejects,
// sorted and de-duplicated — the same set the zod issue paths would name.
func metaLayerBadFields(m *config.RepoMetaInput) []string {
	seen := map[string]bool{}
	add := func(field string) { seen[field] = true }

	if m.Name != nil && *m.Name == "" {
		add("name")
	}
	if m.Description != nil && isDescriptionTooLong(*m.Description) {
		add("description")
	}
	if m.License != nil && *m.License == "" {
		add("license")
	}
	if m.ReleaseMode != nil && *m.ReleaseMode != "tags" && *m.ReleaseMode != "provider" {
		add("releaseMode")
	}
	for _, t := range m.Tags {
		if t == "" {
			add("tags")
		}
	}
	if m.Links != nil {
		for _, v := range []*string{m.Links.Homepage, m.Links.Issues, m.Links.Donations, m.Links.Upstream} {
			if v == nil {
				continue
			}
			if u, err := url.Parse(*v); err != nil || u.Scheme == "" {
				add("links")
			}
		}
	}

	out := make([]string, 0, len(seen))
	for field := range seen {
		out = append(out, field)
	}
	sort.Strings(out)
	return out
}

/* ---- provider response cache -------------------------------------------- */

// providerCacheVersion is bumped whenever the on-disk shape below changes; a mismatch is treated
// as "no cache".
const providerCacheVersion = 1

// providerCache is one source's cached importer answers. The JSON tags match the TypeScript
// object key for key, so either implementation can read the other's file.
type providerCache struct {
	Version  int               `json:"version"`
	Meta     *ImportedRepoMeta `json:"meta"`
	Releases []model.Release   `json:"releases"`
}

// ProviderCachePathFor is where one source's importer answers are cached: next to its mirror, as
// `<repo>.meta.json`.
//
// Only normalised, non-volatile values are stored — the same fields a live call would have
// produced — so serving a build from here yields a byte-identical artifact. No token, no
// timestamps, no counters.
func ProviderCachePathFor(cachePath string) string {
	base := filepath.Base(cachePath)
	if len(base) >= 4 && strings.EqualFold(base[len(base)-4:], ".git") {
		base = base[:len(base)-4]
	}
	return filepath.Join(filepath.Dir(cachePath), base+".meta.json")
}

// readProviderCache reads a cache file; nil means "nothing cached" — absent, unreadable, corrupt
// or written by another version all mean the same thing.
func readProviderCache(file string) *providerCache {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var envelope struct {
		Version  int               `json:"version"`
		Meta     json.RawMessage   `json:"meta"`
		Releases []json.RawMessage `json:"releases"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Version != providerCacheVersion {
		return nil
	}
	out := &providerCache{Version: providerCacheVersion, Releases: []model.Release{}}
	// A meta block of the wrong shape degrades to "no metadata", not to a discarded cache: the
	// releases beside it are still exactly what a fetch would have returned.
	var meta ImportedRepoMeta
	if len(envelope.Meta) > 0 && json.Unmarshal(envelope.Meta, &meta) == nil {
		if meta.Topics == nil {
			meta.Topics = []string{}
		}
		out.Meta = &meta
	}
	for _, rawRelease := range envelope.Releases {
		var release model.Release
		if json.Unmarshal(rawRelease, &release) != nil {
			continue
		}
		if !releaseIsValid(release) {
			continue
		}
		out.Releases = append(out.Releases, release)
	}
	return out
}

var isoDateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`)

// releaseIsValid is the Release schema's own check: the shape came out of encoding/json, so only
// the invariants the site relies on silently are left to verify.
func releaseIsValid(r model.Release) bool {
	if !isoDateRe.MatchString(r.PublishedAt) {
		return false
	}
	if r.Assets == nil {
		return false
	}
	for _, a := range r.Assets {
		if a.Size < 0 {
			return false
		}
	}
	return true
}

// writeProviderCache is a best-effort write: a read-only or full cache directory must not fail a
// build.
func writeProviderCache(file string, data providerCache) {
	if data.Releases == nil {
		data.Releases = []model.Release{}
	}
	encoded, err := marshalJSONLikeStringify(data)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(file), 0o755) != nil {
		return
	}
	_ = os.WriteFile(file, encoded, 0o644)
	// the cache is an optimisation, never a requirement
}

/* ---- PrepareRemote ------------------------------------------------------- */

// PrepareRemote resolves one remote source into a ScanSource pointing at a local mirror.
//
// Order matters: metadata FIRST (it supplies the clone URL), then the mirror. A failed API call
// is not fatal — the mirror is still refreshed from the derived clone URL, the provider data
// falls back to the on-disk cache written by the last successful build, and a repo that has been
// ingested before keeps rendering exactly as it did.
func PrepareRemote(
	ctx context.Context,
	source config.ResolvedSource,
	cfg *config.Resolved,
	deps PrepareRemoteDeps,
	opts PrepareRemoteOptions,
) (PrepareRemoteResult, error) {
	warnings := []model.Warning{}
	cachePath := source.AbsPath
	token := ResolveToken(source.RepoSourceConfig, deps.Env)
	makeImporter := deps.CreateImporter
	if makeImporter == nil {
		makeImporter = CreateImporter
	}
	mirror := deps.EnsureMirror
	if mirror == nil {
		mirror = EnsureMirror
	}

	// The in-repo .frznforge.json is only readable once the mirror exists, so the decision to
	// call the releases endpoint uses the config layers alone; ScanRepo still has the last word
	// on releaseMode and drops the imported list when the repo asks for tag mode.
	releaseMode := source.DefaultReleaseMode()
	if source.Overrides != nil && source.Overrides.ReleaseMode != nil {
		releaseMode = *source.Overrides.ReleaseMode
	}
	wantProviderReleases := releaseMode == "provider"

	cacheFile := ProviderCachePathFor(cachePath)
	// onDisk exists solely so a partial-failure WRITE can preserve the half it did not refetch;
	// --no-cache means "serve nothing from the cache", never "destroy the cache state later
	// offline builds depend on".
	onDisk := readProviderCache(cacheFile)
	cached := onDisk
	if opts.NoCacheReads {
		cached = nil
	}

	// Backfill mode: this repo already has its metadata, so it needs nothing from the network.
	// Handled by the same replay path as the freshness window — the two skips differ only in WHY
	// they fired, and both mean "the cache is exactly what a fetch would return".
	backfillSatisfied := opts.BackfillMetadata && cached != nil

	if (opts.SkipFetch || backfillSatisfied) && cached != nil {
		mirrorReady, err := IsGitRepo(ctx, cachePath)
		if err != nil {
			return PrepareRemoteResult{}, err
		}
		// Freshness window: everything a fetch would return is already on disk from a fully
		// successful fetch moments ago. No importer call, no git, no warning — same bytes.
		if mirrorReady {
			releases := []model.Release{}
			if wantProviderReleases {
				releases = cached.Releases
			}
			webURL, cloneURL := providerURLs(cached.Meta, source.RepoSourceConfig)
			var layer *config.RepoMetaInput
			if cached.Meta != nil {
				layer = metaLayer(cached.Meta, &warnings)
			}
			return PrepareRemoteResult{
				ScanSource:   newScanSource(source, cachePath, layer, releases, webURL, cloneURL),
				ProviderMeta: layer,
				Releases:     releases,
				Warnings:     warnings,
				Action:       MirrorReused,
				Ready:        true,
				// Nothing was attempted; the caller carries the previous stamp.
				FetchStatus: nil,
			}, nil
		}
	}

	importer := makeImporter(source.RepoSourceConfig, ImporterContext{
		HTTP:    deps.HTTP,
		Token:   token,
		Backoff: deps.Backoff,
		Now:     deps.Now,
	})

	var providerMeta *ImportedRepoMeta
	releases := []model.Release{}
	freshMeta := false
	freshReleases := false

	if cfg.Ingest.Fetch == "never" {
		message := "ingest.fetch is 'never' and nothing is cached; this build has no provider description, topics, links or releases for this repo"
		if cached != nil {
			message = "ingest.fetch is 'never'; provider metadata and releases came from the cache and were not refreshed"
		}
		warnings = append(warnings, warn("remote-cache-stale", message))
	} else if importer != nil {
		if meta, err := importer.FetchMeta(ctx); err != nil {
			warnings = append(warnings, importerWarning(err, "provider metadata", source.RepoSourceConfig, token))
		} else {
			providerMeta = &meta
			freshMeta = true
		}
		if wantProviderReleases {
			if imported, err := importer.FetchReleases(ctx); err != nil {
				warnings = append(warnings, importerWarning(err, "provider releases", source.RepoSourceConfig, token))
			} else {
				releases = imported.Releases
				freshReleases = true
				if imported.Truncated {
					warnings = append(warnings, warn("remote-fetch-failed", fmt.Sprintf(
						"the provider has more releases than one build will page through; the oldest are missing (kept %d)",
						len(releases))))
				}
			}
		}
	}

	// Fall back to the last successful build's answers rather than publishing the repo stripped
	// of its description, topics, links and releases because the API blipped.
	usedCache := []string{}
	if !freshMeta && cached != nil && cached.Meta != nil {
		providerMeta = cached.Meta
		usedCache = append(usedCache, "metadata")
	}
	if wantProviderReleases && !freshReleases && cached != nil && len(cached.Releases) > 0 {
		releases = cached.Releases
		usedCache = append(usedCache, "releases")
	}
	if len(usedCache) > 0 && cfg.Ingest.Fetch != "never" {
		warnings = append(warnings, warn("remote-cache-stale",
			"cached provider "+strings.Join(usedCache, " and ")+" were used"))
	}

	if freshMeta || freshReleases {
		entry := providerCache{Version: providerCacheVersion}
		if freshMeta {
			entry.Meta = providerMeta
		} else if onDisk != nil {
			entry.Meta = onDisk.Meta
		}
		if freshReleases {
			entry.Releases = releases
		} else if onDisk != nil {
			entry.Releases = onDisk.Releases
		}
		writeProviderCache(cacheFile, entry)
	}

	webURL, cloneURL := providerURLs(providerMeta, source.RepoSourceConfig)

	mirrorOpts := EnsureMirrorOptions{
		// Backfill spends nothing on git: the mirrors are already there (cloning is unmetered,
		// which is why the commits were never the problem) and this run exists to buy metadata.
		Fetch:         cfg.Ingest.Fetch,
		CloneURL:      cloneURL,
		Token:         token,
		SkipUnchanged: opts.SkipUnchanged,
		KnownHeads:    opts.KnownHeads,
		Timeout:       deps.Timeout,
		Run:           deps.Git,
	}
	if opts.BackfillMetadata {
		mirrorOpts.Fetch = "never"
	}
	result := mirror(ctx, source.RepoSourceConfig, cachePath, mirrorOpts)

	if result.Error != nil {
		warnings = append(warnings, warn("remote-fetch-failed", redactSecrets(result.Error.Error(), token)))
	}
	// "cached" means the update failed — EXCEPT in backfill mode, where not updating is the whole
	// point and warning about it would flag every repo in a healthy run.
	if result.Action == MirrorCached && cfg.Ingest.Fetch != "never" && !opts.BackfillMetadata {
		warnings = append(warnings, warn("remote-cache-stale",
			"the mirror could not be refreshed; the cached clone was used"))
	}

	var layer *config.RepoMetaInput
	if providerMeta != nil {
		layer = metaLayer(providerMeta, &warnings)
	}

	gitOK := result.Action == MirrorFetched || result.Action == MirrorCloned || result.Action == MirrorCurrent
	status := &FetchStatus{
		// "fetched"/"cloned" mean git actually talked to the remote this run, and "current" means
		// it asked and was told there was nothing to fetch — all three leave the mirror provably
		// up to date. "cached" means the update FAILED and the old mirror was used; "missing"
		// means there is no mirror at all.
		//
		// nil = not attempted, which only happens in backfill mode. It is NOT false: the caller
		// carries the previous run's answer forward rather than recording a failure that never
		// happened, so a backfill cannot downgrade what the run log knows about git.
		Git:  &gitOK,
		Meta: freshMeta && (!wantProviderReleases || freshReleases),
	}
	if opts.BackfillMetadata && result.Action != MirrorMissing {
		status.Git = nil
	}

	return PrepareRemoteResult{
		ScanSource:   newScanSource(source, result.Path, layer, releases, webURL, cloneURL),
		ProviderMeta: layer,
		Releases:     releases,
		Warnings:     warnings,
		Action:       result.Action,
		Ready:        result.Action != MirrorMissing,
		FetchStatus:  status,
	}, nil
}

// providerURLs picks the repo's web and clone URLs, preferring what the provider reported and
// falling back to what the config alone implies.
func providerURLs(meta *ImportedRepoMeta, source config.RepoSourceConfig) (webURL, cloneURL string) {
	webURL = deriveWebURL(source)
	if meta != nil && isHTTPURL(&meta.WebURL) {
		webURL = meta.WebURL
	}
	cloneURL = webURL + ".git"
	if meta != nil && isHTTPURL(&meta.CloneURL) {
		cloneURL = meta.CloneURL
	}
	return webURL, cloneURL
}

// newScanSource assembles the handoff to ScanRepo.
//
// The slug and display name are derived from the CONFIG, never from the mirror's directory name:
// that name is a sanitised, lower-cased, hash-suffixed filesystem key, not something to show a
// visitor.
func newScanSource(
	source config.ResolvedSource,
	absPath string,
	layer *config.RepoMetaInput,
	releases []model.Release,
	webURL, cloneURL string,
) ScanSource {
	slug := source.Slug
	if slug == "" {
		slug = Slugify(SourceRepoName(source.RepoSourceConfig))
	}
	return ScanSource{
		AbsPath:      absPath,
		Slug:         slug,
		DefaultName:  SourceRepoName(source.RepoSourceConfig),
		Overrides:    source.Overrides,
		ProviderMeta: layer,
		Source:       buildRepoSource(source.RepoSourceConfig, webURL, cloneURL),
		ReleaseMode:  source.DefaultReleaseMode(),
		Releases:     releases,
	}
}

// marshalJSONLikeStringify writes a value the way `JSON.stringify(v, null, 2) + '\n'` does, so a
// cache file written here is byte-identical to one written by the TypeScript ingest.
func marshalJSONLikeStringify(v any) ([]byte, error) {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return []byte(buf.String()), nil
}
