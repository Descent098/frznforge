package main

// The provider side of `frznforge init`: which forges exist, where their tokens come from, and
// how an account's repositories are listed. Ported from the listing half of scripts/cli.ts.
//
// KNOWN DUPLICATION: internal/wizard/providers.go carries the same table and the same listing
// walk for `init --web`, unexported. The two were written to the same TypeScript and must stay
// in step; the fix is to hoist this file and entries.go into an internal package both front ends
// call, which is a change that spans package boundaries and so is not this file's to make. Until
// then, a change to one is a change to both.
//
// Tokens are read from the environment and never written anywhere. `init` reports WHICH variable
// it consulted and whether a value was found, never the value itself, and it never offers to
// type one in.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"frznforge/internal/config"
	"frznforge/internal/ingest"
)

// providerInfo is everything init needs to prompt for, and to write, one provider.
type providerInfo struct {
	label string
	// defaultHost is the API base written into the config when the user does not override it;
	// empty for the self-hosted providers, which have nothing to guess.
	defaultHost string
	// hostRequired marks the providers that must be asked for a host.
	hostRequired   bool
	hostSuggestion string
	accountLabel   string
	// tokenScopes is the scope wording shown beside the provider in the menu.
	tokenScopes string
}

// providerNames is the menu order, and the order the help text lists.
var providerNames = []string{"github", "gitlab", "gitea", "forgejo"}

var providers = map[string]providerInfo{
	"github": {
		label:          "GitHub",
		defaultHost:    "https://api.github.com",
		hostSuggestion: "https://api.github.com",
		accountLabel:   "user or organisation",
		tokenScopes:    "public_repo (add repo for private repositories)",
	},
	"gitlab": {
		label:          "GitLab",
		defaultHost:    "https://gitlab.com",
		hostSuggestion: "https://gitlab.com",
		accountLabel:   "user or group path",
		tokenScopes:    "read_api",
	},
	"gitea": {
		label:          "Gitea",
		hostRequired:   true,
		hostSuggestion: "https://gitea.example.com",
		accountLabel:   "user or organisation",
		tokenScopes:    "read:repository",
	},
	"forgejo": {
		label:          "Forgejo",
		hostRequired:   true,
		hostSuggestion: "https://codeberg.org",
		accountLabel:   "user or organisation",
		tokenScopes:    "read:repository",
	},
}

func knownProvider(name string) bool { return providers[name].label != "" }

/* ---- tokens --------------------------------------------------------------- */

// tokenStatus is which environment variables a provider's token can come from, and whether one
// of them is set.
type tokenStatus struct {
	// names are the variables consulted, in order.
	names []string
	// from is the variable a value came from, or "".
	from  string
	token string
}

// statusFor reads the environment for a provider's token. env is injected so a test never
// depends on the developer's own credentials.
func statusFor(provider string, env ingest.Env) tokenStatus {
	source := config.RepoSourceConfig{Type: provider, Owner: "x", Repo: "y"}
	if provider == "gitlab" {
		source = config.RepoSourceConfig{Type: "gitlab", Project: "x/y"}
	}
	names := ingest.TokenEnvFor(source)
	token := ingest.ResolveToken(source, env)
	from := ""
	if token != "" {
		for _, name := range names {
			if strings.TrimSpace(env.Lookup(name)) == token {
				from = name
				break
			}
		}
	}
	return tokenStatus{names: names, from: from, token: token}
}

// tokenMessage is the line printed before any API call.
//
// It names the variables and says whether a value was found. It must never contain the value
// itself — a token pasted into a terminal transcript, an issue or a screen share is a leaked
// token, and this line is printed on every run.
func (s tokenStatus) message() string {
	if s.from != "" {
		return "Token: using $" + s.from + " (value never shown, never written to the config)."
	}
	return "Token: none set (checked " + strings.Join(prefixed(s.names, "$"), ", ") + ") — listing public repositories only."
}

func prefixed(values []string, prefix string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = prefix + v
	}
	return out
}

/* ---- listing -------------------------------------------------------------- */

// remoteRepo is one repository as a provider listing describes it.
type remoteRepo struct {
	// Name is the repository name (GitLab: the last path segment).
	Name string
	// FullName is `owner/name`, or the full namespaced path on GitLab.
	FullName string
	Owner    string
	// Project is GitLab's full namespaced project path.
	Project     string
	Description string
	// Archived and Fork are nil when the listing endpoint does not report them, which is NOT
	// the same as false: GitLab's project listings answer neither question, and a filter that
	// silently keeps every fork is worse than one that refuses. See excludeFilters.known.
	Archived *bool
	Private  bool
	Fork     *bool
}

// listRequest is one listing: where from, as whom.
type listRequest struct {
	provider string
	host     string
	account  string
	token    string
	client   *http.Client
	// maxPages is the safety valve so a huge account cannot spin forever.
	maxPages int
	// warn receives non-fatal listing problems — today, a truncated page walk.
	warn func(string)
}

// httpError carries the status so listRepos can tell "no such user, try the org endpoint" from
// a real failure.
type httpError struct {
	status  int
	message string
}

func (e *httpError) Error() string { return e.message }

const defaultUserAgent = "frznforge-init"

func (r listRequest) headers(req *http.Request) {
	req.Header.Set("accept", "application/json")
	req.Header.Set("user-agent", defaultUserAgent)
	if r.token == "" {
		return
	}
	switch r.provider {
	case "gitlab":
		req.Header.Set("private-token", r.token)
	case "github":
		req.Header.Set("authorization", "Bearer "+r.token)
	default:
		req.Header.Set("authorization", "token "+r.token)
	}
}

// getJSON fetches one page, turning every failure into an error with the token scrubbed out.
func (r listRequest) getJSON(target string) (int, []byte, http.Header, error) {
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("not a usable URL: %s", target)
	}
	r.headers(req)
	client := r.client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("could not reach %s: %s", target, ingest.RedactToken(err.Error(), r.token))
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("could not read the answer from %s: %s", target, ingest.RedactToken(err.Error(), r.token))
	}
	return res.StatusCode, body, res.Header, nil
}

// listingError turns a non-2xx listing response into something the user can act on. It never
// echoes the token.
func (r listRequest) listingError(target string, status int, header http.Header) error {
	envs := strings.Join(prefixed(statusFor(r.provider, ingest.Env{}).names, "$"), " or ")
	host := target
	if u, err := url.Parse(target); err == nil {
		host = u.Host
	}
	switch {
	case header.Get("x-ratelimit-remaining") == "0" || status == 429:
		advice := fmt.Sprintf("Set %s to raise the limit.", envs)
		if r.token != "" {
			advice = "Wait for the limit to reset and try again."
		}
		return &httpError{status: status, message: fmt.Sprintf("rate limited by %s (HTTP %d). %s", host, status, advice)}
	case status == 401 || status == 403:
		advice := fmt.Sprintf("Set %s for private repositories.", envs)
		if r.token != "" {
			advice = fmt.Sprintf("The token in %s may be expired or missing a read scope.", envs)
		}
		return &httpError{status: status, message: fmt.Sprintf("not authorised (HTTP %d). %s", status, advice)}
	case status == 404:
		return &httpError{status: status, message: fmt.Sprintf("no such account (HTTP 404): %s", target)}
	default:
		return &httpError{status: status, message: fmt.Sprintf("%s failed with HTTP %d", target, status)}
	}
}

// paginate walks a provider endpoint until a short page comes back.
//
// Stopping at maxPages while the last page was still full means the listing is INCOMPLETE, and
// that has to be said out loud: a caller cannot tell a truncated list from a complete one, and
// `--select=1,3,5-8` written against a truncated listing silently picks the wrong repositories.
func (r listRequest) paginate(page func(int) string, perPage int) ([]json.RawMessage, error) {
	maxPages := r.maxPages
	if maxPages <= 0 {
		maxPages = 10
	}
	var out []json.RawMessage
	truncated := false
	for p := 1; ; p++ {
		if p > maxPages {
			truncated = true
			break
		}
		target := page(p)
		status, body, header, err := r.getJSON(target)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, r.listingError(target, status, header)
		}
		var items []json.RawMessage
		if err := json.Unmarshal(body, &items); err != nil {
			return nil, fmt.Errorf("%s did not return a list of repositories", target)
		}
		out = append(out, items...)
		if len(items) < perPage {
			break
		}
	}
	if truncated && r.warn != nil {
		r.warn(fmt.Sprintf(
			"warning: listing stopped after %d repositories (%d-page cap) and is incomplete — "+
				"use --select=name,name to name the repositories you want.", len(out), maxPages))
	}
	return out, nil
}

// listRepos lists an account's repositories.
//
// User endpoints are tried before organisation/group ones because a personal account is the
// common case; a 404 on the first is not an error, it just means "try the other one". Results
// are sorted by full name so a `--select=1,2` in a script is stable between runs.
func listRepos(r listRequest) ([]remoteRepo, error) {
	host := strings.TrimRight(r.host, "/")
	account := strings.TrimSpace(r.account)
	enc := url.PathEscape(account)

	var attempts []func(int) string
	perPage := 100
	switch r.provider {
	case "github":
		attempts = []func(int) string{
			func(p int) string {
				return fmt.Sprintf("%s/users/%s/repos?per_page=100&page=%d&sort=full_name", host, enc, p)
			},
			func(p int) string {
				return fmt.Sprintf("%s/orgs/%s/repos?per_page=100&page=%d&sort=full_name", host, enc, p)
			},
		}
	case "gitlab":
		attempts = []func(int) string{
			func(p int) string {
				return fmt.Sprintf("%s/api/v4/users/%s/projects?per_page=100&page=%d&order_by=path&sort=asc", host, enc, p)
			},
			func(p int) string {
				return fmt.Sprintf("%s/api/v4/groups/%s/projects?per_page=100&page=%d&include_subgroups=true&order_by=path&sort=asc", host, enc, p)
			},
		}
	default:
		perPage = 50
		attempts = []func(int) string{
			func(p int) string { return fmt.Sprintf("%s/api/v1/users/%s/repos?limit=50&page=%d", host, enc, p) },
			func(p int) string { return fmt.Sprintf("%s/api/v1/orgs/%s/repos?limit=50&page=%d", host, enc, p) },
		}
	}

	var raw []json.RawMessage
	var lastErr error
	listed := false
	for _, page := range attempts {
		items, err := r.paginate(page, perPage)
		if err == nil {
			raw, listed = items, true
			break
		}
		lastErr = err
		if he, ok := err.(*httpError); ok && he.status == 404 {
			continue
		}
		return nil, err
	}
	if !listed {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("no repositories found for %s", account)
	}

	repos := make([]remoteRepo, 0, len(raw))
	for _, item := range raw {
		var fields map[string]any
		if json.Unmarshal(item, &fields) != nil {
			continue
		}
		if r.provider == "gitlab" {
			if repo, ok := gitlabRepo(fields); ok {
				repos = append(repos, repo)
			}
			continue
		}
		if repo, ok := gitRepo(fields, account); ok {
			repos = append(repos, repo)
		}
	}
	// Plain byte comparison, never a locale-aware one: the order decides which repository
	// `--select=3` means, and it must be the same on every machine.
	sort.SliceStable(repos, func(i, j int) bool { return repos[i].FullName < repos[j].FullName })
	return repos, nil
}

func str(fields map[string]any, key string) string {
	if v, ok := fields[key].(string); ok {
		return v
	}
	return ""
}

func boolPtr(v bool) *bool { return &v }

// gitlabRepo maps one GitLab project.
//
// GitLab's LIST representations answer neither flag the way the single-project endpoint does:
// `forked_from_project` is never present on a list item (verified on both /users/:id/projects
// and /groups/:id/projects), and `archived` is present for groups but absent from the simple
// representation a user listing returns. Reporting false would make `all-nf`/`all-na` look like
// they ran; nil makes resolveSelection say out loud that it cannot answer.
func gitlabRepo(fields map[string]any) (remoteRepo, bool) {
	full := str(fields, "path_with_namespace")
	if full == "" {
		return remoteRepo{}, false
	}
	segments := strings.Split(full, "/")
	repo := remoteRepo{
		Name:        segments[len(segments)-1],
		FullName:    full,
		Owner:       strings.Join(segments[:len(segments)-1], "/"),
		Project:     full,
		Description: str(fields, "description"),
		Private:     str(fields, "visibility") != "" && str(fields, "visibility") != "public",
	}
	if archived, ok := fields["archived"].(bool); ok {
		repo.Archived = boolPtr(archived)
	}
	if forked, present := fields["forked_from_project"]; present {
		repo.Fork = boolPtr(forked != nil)
	}
	return repo, true
}

// gitRepo maps one GitHub, Gitea or Forgejo repository. All three carry `fork` and `archived` on
// every list item, so here a missing key really does mean false rather than "not reported".
func gitRepo(fields map[string]any, account string) (remoteRepo, bool) {
	name := str(fields, "name")
	if name == "" {
		return remoteRepo{}, false
	}
	owner := ""
	if o, ok := fields["owner"].(map[string]any); ok {
		owner = str(o, "login")
	}
	full := str(fields, "full_name")
	if full == "" {
		if owner != "" {
			full = owner + "/" + name
		} else {
			full = name
		}
	}
	if owner == "" {
		owner = account
		if before, _, found := strings.Cut(full, "/"); found {
			owner = before
		}
	}
	isTrue := func(key string) bool { v, _ := fields[key].(bool); return v }
	return remoteRepo{
		Name:        name,
		FullName:    full,
		Owner:       owner,
		Description: str(fields, "description"),
		Archived:    boolPtr(isTrue("archived")),
		Private:     isTrue("private"),
		Fork:        boolPtr(isTrue("fork")),
	}, true
}
