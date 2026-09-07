package ingest

import (
	"context"
	"os"
	"strings"
	"time"

	"frznforge/internal/config"
	"frznforge/internal/logging"
	"frznforge/internal/model"
)

// Port of src/lib/importers/types.ts and src/lib/importers/index.ts.
//
// An importer is the *metadata* half of a remote source: it answers two questions over the
// provider's REST API — "what does this repo say about itself" and "what releases does it
// have". The git half is a plain `git clone --mirror` into the ingest cache, scanned by the
// existing local scanner, so nothing in an importer ever touches git.
//
// Rules every implementation obeys:
//   - Tokens come from the ENVIRONMENT only, never from config. Never log one, never put one
//     in an error message, a warning or the artifact; run anything interpolated through
//     RedactToken first.
//   - Never hard-fail the build. Return an *ImporterError and let the caller choose between
//     "use the cache" and "skip the repo with a warning".
//   - Deterministic output. No wall-clock values, no download counters, no star counts.
//     Timestamps are normalised to the artifact's ISO profile (UTC, seconds precision).
//   - Injectable HTTP, so tests run against recorded JSON fixtures with no network.

// DefaultUserAgent is sent as User-Agent when a context does not override it. GitHub rejects
// requests without one.
const DefaultUserAgent = "frznforge"

// ImportedRepoMeta is repo metadata as the provider reports it. It is fed into the scanner as
// low-precedence defaults: config overrides > the repo's committed .frznforge.json > this >
// values derived from the repository itself.
//
// The JSON tags are the on-disk shape of the provider response cache and match the TypeScript
// object key for key, so a cache written by either implementation is readable by the other.
type ImportedRepoMeta struct {
	// Name is the repo name exactly as the provider spells it (MyProject, 文档). Without it the
	// display name would fall back to the mirror's directory basename, which is lower-cased and
	// ASCII-only because it is a filesystem key, not user-facing text.
	Name        *string  `json:"name"`
	Description *string  `json:"description"`
	Homepage    *string  `json:"homepage"`
	Topics      []string `json:"topics"`
	// License is an SPDX id when the provider reports one, else nil (the scanner still sniffs
	// the cloned LICENSE file).
	License       *string `json:"license"`
	DefaultBranch *string `json:"defaultBranch"`
	// WebURL is the human-facing repo page.
	WebURL string `json:"webUrl"`
	// CloneURL is what a visitor can `git clone` — also what the mirror is cloned from.
	CloneURL  string  `json:"cloneUrl"`
	IssuesURL *string `json:"issuesUrl"`
	Template  bool    `json:"template"`
	Archived  bool    `json:"archived"`
}

// ImportedReleases is a repo's release list as imported, plus whether the pagination cap cut
// it short.
type ImportedReleases struct {
	// Releases are newest first: publishedAt desc, tag asc as tiebreak. Empty when the repo has
	// none.
	Releases []model.Release
	// Truncated is true when the provider still had pages left when the client's page cap ran
	// out, so this list is missing the oldest releases. The caller warns; it must not look like
	// a short list.
	Truncated bool
}

// Importer is what a provider module exposes. One instance per configured remote source.
type Importer interface {
	// Provider is "github", "gitlab", "gitea" or "forgejo".
	Provider() string
	FetchMeta(ctx context.Context) (ImportedRepoMeta, error)
	FetchReleases(ctx context.Context) (ImportedReleases, error)
}

// ImporterErrorKind says why an importer call failed. The kinds map 1:1 onto the remote-*
// warning codes.
type ImporterErrorKind string

const (
	KindAuth        ImporterErrorKind = "auth"
	KindNotFound    ImporterErrorKind = "not-found"
	KindRateLimit   ImporterErrorKind = "rate-limit"
	KindNetwork     ImporterErrorKind = "network"
	KindBadResponse ImporterErrorKind = "bad-response"
)

// ImporterError is a failed provider call. It is always catchable by the caller and always
// turned into a warning — an unreachable, unauthenticated or rate-limited remote must never
// fail a build.
//
// Message MUST NOT contain a token: build it from the URL and status only, and pass anything
// user-supplied through RedactToken.
type ImporterError struct {
	Kind    ImporterErrorKind
	Message string
	// Status is the HTTP status when the failure came from a response; 0 when there was none.
	Status int
	// RetryAfter is seconds to wait, parsed from Retry-After / rate-limit reset headers. It is
	// a countdown, so it stays here for the console and never reaches the artifact.
	RetryAfter *int
	Cause      error
}

func (e *ImporterError) Error() string { return e.Message }

func (e *ImporterError) Unwrap() error { return e.Cause }

// ImporterContext is everything an importer needs beyond its source config.
type ImporterContext struct {
	// HTTP is injected for tests; nil means the package's default client.
	HTTP Doer
	// Token is the API token, already resolved from the environment. "" is anonymous.
	Token string
	// UserAgent defaults to DefaultUserAgent plus the VERSION file when one is readable.
	UserAgent string
	// Backoff is the per-origin rate-limit gate; nil means SharedBackoff.
	Backoff *OriginBackoff
	// Now is injected for tests, so Retry-After arithmetic is deterministic.
	Now func() time.Time
}

// providerTokenEnv is the default token env var per provider, used when the source sets no
// tokenEnv.
var providerTokenEnv = map[string]string{
	"github":  "GITHUB_TOKEN",
	"gitlab":  "GITLAB_TOKEN",
	"gitea":   "GITEA_TOKEN",
	"forgejo": "FORGEJO_TOKEN",
}

// TokenEnvFor lists the environment variables to consult for a source's token, in order:
//
//  1. source.TokenEnv, when set — an explicit choice replaces the defaults entirely;
//  2. otherwise FRZNFORGE_<PROVIDER>_TOKEN, then the provider's conventional variable
//     (GITHUB_TOKEN, GITLAB_TOKEN, GITEA_TOKEN, FORGEJO_TOKEN).
//
// Local sources have no token and get an empty list.
func TokenEnvFor(source config.RepoSourceConfig) []string {
	if source.Type == "local" {
		return nil
	}
	if source.TokenEnv != "" {
		return []string{source.TokenEnv}
	}
	return []string{
		"FRZNFORGE_" + strings.ToUpper(source.Type) + "_TOKEN",
		providerTokenEnv[source.Type],
	}
}

// Env is the environment a token is read from.
//
// A nil Env reads the real process environment, which is what a build does; a non-nil one
// (including an empty map) is used verbatim, which is what tests do. The distinction matters:
// `Env{}` means "this build has no tokens", not "go look at os.Environ".
type Env map[string]string

// Lookup reads one variable. It is exported because the wizard reports WHICH variable a token
// came from, and it has to ask the same environment the resolver did — including the nil case.
func (e Env) Lookup(name string) string {
	if name == "" {
		return ""
	}
	if e == nil {
		return os.Getenv(name)
	}
	return e[name]
}

// ResolveToken returns the first non-empty value among TokenEnvFor(source), trimmed, or "" when
// none is set. Tokens live in the environment only — a token never comes from the config file.
//
// Every token that comes out of here is registered with internal/logging on the way, and this is
// the one place that can do it: logging harvests the environment by variable NAME, and
// `"tokenEnv": "MY_PAT"` lets a user call theirs anything at all, which no name-based scan can
// recognise. Registering it here means a value that reached this process is scrubbed out of both
// diagnostic files from the next record onwards, whatever call site later puts it in one.
func ResolveToken(source config.RepoSourceConfig, env Env) string {
	for _, name := range TokenEnvFor(source) {
		if v := strings.TrimSpace(env.Lookup(name)); v != "" {
			logging.Redact(v)
			return v
		}
	}
	return ""
}

// RedactToken replaces every occurrence of token with "***". Call it on anything that might
// have been built from a URL or header carrying credentials before it reaches a message, a
// warning or a log line.
func RedactToken(text, token string) string {
	if token == "" {
		return text
	}
	return strings.ReplaceAll(text, token, "***")
}

// CreateImporter builds the importer for a configured source, or nil for type "local" (nothing
// to import — the scanner reads the directory directly).
//
// The token must already be resolved into ctx by the caller: unlike the TypeScript, this does
// not silently reach for the process environment, so a test that forgets to pass an Env gets an
// anonymous client rather than the developer's own credentials.
func CreateImporter(source config.RepoSourceConfig, ctx ImporterContext) Importer {
	switch source.Type {
	case "github":
		return NewGithubImporter(source, ctx)
	case "gitlab":
		return NewGitlabImporter(source, ctx)
	case "gitea", "forgejo":
		return NewGiteaImporter(source, ctx)
	default:
		return nil
	}
}
