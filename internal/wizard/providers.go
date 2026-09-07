package wizard

// The provider side of the picker: which forges exist, where their tokens come from, and how an
// account's repositories are listed. Ported from the listing half of scripts/cli.ts.
//
// The token never reaches the browser and never reaches a host the terminal did not authorise —
// see hostTrustedForToken, which is the rule that makes the page's free-text "Custom API host"
// field safe to offer.

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

// ProviderInfo is everything the page needs to render one provider's row, and everything the
// server needs to decide where a token may be sent.
type ProviderInfo struct {
	Label string `json:"label"`
	// DefaultHost is the API base written into the config when the user does not override it;
	// empty for the self-hosted providers, which have nothing to guess.
	DefaultHost string `json:"defaultHost"`
	// HostRequired marks the providers that must be asked for a host.
	HostRequired   bool   `json:"hostRequired"`
	HostSuggestion string `json:"hostSuggestion"`
	AccountLabel   string `json:"accountLabel"`
	// TokenScopes is the scope wording shown next to the token hint.
	TokenScopes string `json:"tokenScopes"`
}

// providerNames is the display order, and the order /api/context reports.
var providerNames = []string{"github", "gitlab", "gitea", "forgejo"}

var providers = map[string]ProviderInfo{
	"github": {
		Label:          "GitHub",
		DefaultHost:    "https://api.github.com",
		HostSuggestion: "https://api.github.com",
		AccountLabel:   "user or organisation",
		TokenScopes:    "public_repo (add repo for private repositories)",
	},
	"gitlab": {
		Label:          "GitLab",
		DefaultHost:    "https://gitlab.com",
		HostSuggestion: "https://gitlab.com",
		AccountLabel:   "user or group path",
		TokenScopes:    "read_api",
	},
	"gitea": {
		Label:          "Gitea",
		HostRequired:   true,
		HostSuggestion: "https://gitea.example.com",
		AccountLabel:   "user or organisation",
		TokenScopes:    "read:repository",
	},
	"forgejo": {
		Label:          "Forgejo",
		HostRequired:   true,
		HostSuggestion: "https://codeberg.org",
		AccountLabel:   "user or organisation",
		TokenScopes:    "read:repository",
	},
}

func knownProvider(name string) bool { return providers[name].Label != "" }

/* ------------------------------------------------------------------ tokens */

// tokenStatus is which environment variables a provider's token can come from, and whether one
// of them is set. The token itself never leaves this process.
type tokenStatus struct {
	names []string
	from  string
	token string
}

// statusFor reads the environment for a provider's token. env is the injected environment, so a
// test never depends on the developer's own credentials.
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

// sameHost compares two API bases the way entryKey does: no trailing slash, case-insensitive.
func sameHost(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(strings.TrimRight(a, "/"), strings.TrimRight(b, "/"))
}

// hostTrustedForToken answers: may the environment's provider token be sent to this host?
//
// Only two hosts qualify — the provider's own default API base, and the --host the user typed on
// their own command line FOR THAT SAME PROVIDER. Everything else, including anything the browser
// types into "Custom API host", is listed without credentials. Binding the CLI host to the CLI
// provider matters: without it, `--provider=gitea --host=https://intranet` would let the page
// switch to GitHub and post the GitHub token to that same intranet box.
func hostTrustedForToken(provider, host, cliProvider, cliHost string) bool {
	if sameHost(host, providers[provider].DefaultHost) {
		return true
	}
	return cliProvider == provider && sameHost(host, cliHost)
}

/* ------------------------------------------------------------------ listing */

// RemoteRepo is one repository as a provider listing describes it.
type RemoteRepo struct {
	// Name is the repository name (GitLab: the last path segment).
	Name string `json:"name"`
	// FullName is `owner/name`, or the full namespaced path on GitLab.
	FullName string `json:"fullName"`
	Owner    string `json:"owner"`
	// Project is GitLab's full namespaced project path.
	Project     string  `json:"project,omitempty"`
	Description *string `json:"description"`
	// Archived and Fork are nil when the listing endpoint does not report them, which is not
	// the same as false: GitLab's project listings answer neither question, and a filter that
	// silently keeps every fork is worse than one the page can grey out.
	Archived *bool `json:"archived"`
	Private  bool  `json:"private"`
	Fork     *bool `json:"fork"`
}

// excludeFilters are the quick filters the page offers, and the single place they are named.
// known() is what /api/repos reports per listing, so a filter the data cannot answer for is
// disabled rather than left looking functional.
var excludeFilters = []struct {
	one   string
	known func(RemoteRepo) bool
}{
	{"fork", func(r RemoteRepo) bool { return r.Fork != nil }},
	{"archived", func(r RemoteRepo) bool { return r.Archived != nil }},
	{"private", func(_ RemoteRepo) bool { return true }},
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
}

// httpError carries the status so the caller can tell "no such user, try the org endpoint" from
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

func prefixed(values []string, prefix string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = prefix + v
	}
	return out
}

// paginate walks a provider endpoint until a short page comes back.
//
// Stopping at maxPages while the last page was still full means the listing is INCOMPLETE, and
// that has to be said out loud: a caller cannot tell a truncated list from a complete one, and a
// selection made against a truncated listing quietly picks the wrong repositories.
func (r listRequest) paginate(page func(int) string, perPage int) ([]json.RawMessage, []string, error) {
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
			return nil, nil, err
		}
		if status < 200 || status >= 300 {
			return nil, nil, r.listingError(target, status, header)
		}
		var items []json.RawMessage
		if err := json.Unmarshal(body, &items); err != nil {
			return nil, nil, fmt.Errorf("%s did not return a list of repositories", target)
		}
		out = append(out, items...)
		if len(items) < perPage {
			break
		}
	}
	var warnings []string
	if truncated {
		warnings = append(warnings, fmt.Sprintf(
			"warning: listing stopped after %d repositories (%d-page cap) and is incomplete — "+
				"name the repositories you want instead of picking from this list.", len(out), maxPages))
	}
	return out, warnings, nil
}

// listRepos lists an account's repositories.
//
// User endpoints are tried before organisation/group ones because a personal account is the
// common case; a 404 on the first is not an error, it just means "try the other one". Results
// are sorted by full name so the page's table is stable between loads.
func listRepos(r listRequest) ([]RemoteRepo, []string, error) {
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
	var warnings []string
	var lastErr error
	listed := false
	for _, page := range attempts {
		items, warns, err := r.paginate(page, perPage)
		if err == nil {
			raw, warnings, listed = items, warns, true
			break
		}
		lastErr = err
		if he, ok := err.(*httpError); ok && he.status == 404 {
			continue
		}
		return nil, nil, err
	}
	if !listed {
		if lastErr != nil {
			return nil, nil, lastErr
		}
		return nil, nil, fmt.Errorf("no repositories found for %s", account)
	}

	repos := make([]RemoteRepo, 0, len(raw))
	for _, item := range raw {
		var fields map[string]any
		if json.Unmarshal(item, &fields) != nil {
			continue
		}
		if r.provider == "gitlab" {
			repo, ok := gitlabRepo(fields)
			if ok {
				repos = append(repos, repo)
			}
			continue
		}
		repo, ok := gitRepo(fields, account)
		if ok {
			repos = append(repos, repo)
		}
	}
	sort.SliceStable(repos, func(i, j int) bool { return repos[i].FullName < repos[j].FullName })
	return repos, warnings, nil
}

func str(fields map[string]any, key string) string {
	if v, ok := fields[key].(string); ok {
		return v
	}
	return ""
}

func strPtr(fields map[string]any, key string) *string {
	if v, ok := fields[key].(string); ok && v != "" {
		return &v
	}
	return nil
}

func boolPtr(v bool) *bool { return &v }

// gitlabRepo maps one GitLab project.
//
// GitLab's LIST representations answer neither flag the way the single-project endpoint does:
// `forked_from_project` is never present on a list item, and `archived` is present for groups
// but absent from the simple representation a user listing returns. Reporting false would make
// the page's filters look like they ran; nil makes it say it cannot answer.
func gitlabRepo(fields map[string]any) (RemoteRepo, bool) {
	full := str(fields, "path_with_namespace")
	if full == "" {
		return RemoteRepo{}, false
	}
	segments := strings.Split(full, "/")
	repo := RemoteRepo{
		Name:        segments[len(segments)-1],
		FullName:    full,
		Owner:       strings.Join(segments[:len(segments)-1], "/"),
		Project:     full,
		Description: strPtr(fields, "description"),
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
func gitRepo(fields map[string]any, account string) (RemoteRepo, bool) {
	name := str(fields, "name")
	if name == "" {
		return RemoteRepo{}, false
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
	return RemoteRepo{
		Name:        name,
		FullName:    full,
		Owner:       owner,
		Description: strPtr(fields, "description"),
		Archived:    boolPtr(isTrue("archived")),
		Private:     isTrue("private"),
		Fork:        boolPtr(isTrue("fork")),
	}, true
}
