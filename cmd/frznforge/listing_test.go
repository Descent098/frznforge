package main

// Tokens and provider listings — the port of cli.test.ts's "token reporting" and "listings that
// do not report every flag" blocks.
//
// Every request below goes to an httptest server on loopback: this package's tests must never
// depend on a network, a rate limit or somebody's real account.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"frznforge/internal/ingest"
)

// secret is a value that must never appear in anything init prints. It looks like a real token
// on purpose — a substring match against something short would pass by accident.
const secret = "ghp_supersecretvalue1234567890"

/* ---- the provider table --------------------------------------------------- */

func TestEveryListedProviderIsFullyDescribed(t *testing.T) {
	// providerNames drives the menu, the --provider validation and the help text, while the
	// details come from a separate map. A name added to one and not the other gives a menu entry
	// with a blank label and a host of "", which lists nothing and explains nothing.
	if len(providerNames) != len(providers) {
		t.Errorf("%d providers listed, %d described", len(providerNames), len(providers))
	}
	for _, name := range providerNames {
		info, ok := providers[name]
		if !ok {
			t.Errorf("%s is offered in the menu but has no entry", name)
			continue
		}
		if !knownProvider(name) {
			t.Errorf("%s is offered in the menu but --provider=%s would be rejected", name, name)
		}
		if info.label == "" || info.accountLabel == "" || info.tokenScopes == "" {
			t.Errorf("%s: label/accountLabel/tokenScopes = %q/%q/%q", name, info.label, info.accountLabel, info.tokenScopes)
		}
		// Self-hosted forges have nothing to guess, so they must be asked — and the prompt needs
		// an example to show. The hosted ones must have a default, or a fully-flagged run with no
		// --host would query the empty string.
		if info.hostRequired {
			if info.defaultHost != "" || info.hostSuggestion == "" {
				t.Errorf("%s: hostRequired with defaultHost %q and suggestion %q", name, info.defaultHost, info.hostSuggestion)
			}
		} else if info.defaultHost == "" {
			t.Errorf("%s: no default host and none is asked for", name)
		}
		// The token variables come from ingest, which is where the build reads them too. A
		// provider init reported a different variable for would send the user to set the wrong one.
		if len(statusFor(name, ingest.Env{}).names) != 2 {
			t.Errorf("%s consults %v", name, statusFor(name, ingest.Env{}).names)
		}
	}
}

/* ---- tokens --------------------------------------------------------------- */

func TestTokenStatusConsultsTheDocumentedVariablesInOrder(t *testing.T) {
	// The order is the promise: a user who sets FRZNFORGE_GITHUB_TOKEN for frznforge alone must
	// not have it shadowed by the GITHUB_TOKEN their shell already exports for something else.
	for provider, want := range map[string][]string{
		"github":  {"FRZNFORGE_GITHUB_TOKEN", "GITHUB_TOKEN"},
		"gitlab":  {"FRZNFORGE_GITLAB_TOKEN", "GITLAB_TOKEN"},
		"gitea":   {"FRZNFORGE_GITEA_TOKEN", "GITEA_TOKEN"},
		"forgejo": {"FRZNFORGE_FORGEJO_TOKEN", "FORGEJO_TOKEN"},
	} {
		if got := statusFor(provider, ingest.Env{}).names; !equalStrings(got, want) {
			t.Errorf("%s consults %v, want %v", provider, got, want)
		}
	}

	if got := statusFor("github", ingest.Env{"GITHUB_TOKEN": secret}).from; got != "GITHUB_TOKEN" {
		t.Errorf("from = %q", got)
	}
	if got := statusFor("github", ingest.Env{"FRZNFORGE_GITHUB_TOKEN": secret, "GITHUB_TOKEN": "other"}).from; got != "FRZNFORGE_GITHUB_TOKEN" {
		t.Errorf("the frznforge-specific variable did not win: from = %q", got)
	}
	if got := statusFor("github", ingest.Env{}).from; got != "" {
		t.Errorf("an empty environment reported a token from %q", got)
	}
}

func TestTokenMessageNamesTheVariableAndNeverTheValue(t *testing.T) {
	// This line is printed on every run. A token that reaches a terminal transcript, an issue or
	// a screen share is a leaked token, and there is no way to un-leak it.
	found := statusFor("github", ingest.Env{"GITHUB_TOKEN": secret}).message()
	mustContain(t, found, "$GITHUB_TOKEN", "the message does not say which variable was used")
	mustNotContain(t, found, secret, "the token itself was printed")

	missing := statusFor("gitea", ingest.Env{}).message()
	mustContain(t, missing, "$GITEA_TOKEN", "the message does not say which variable to set")
	mustContain(t, missing, "public repositories only", "the message does not say what the user is missing")
}

/* ---- listings ------------------------------------------------------------- */

func TestListReposReadsEveryFlagGitHubReports(t *testing.T) {
	// GitHub's list items carry fork, archived and private on every entry, so a missing key
	// really does mean false here — unlike GitLab below.
	srv := fakeProvider(t, map[string]any{
		"/users/me/repos": []any{
			listingItem("me", "a", nil),
			listingItem("me", "b", map[string]any{"fork": true, "archived": true, "private": true}),
		},
	})
	repos, err := listRepos(listRequest{provider: "github", host: srv.URL, account: "me", client: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 {
		t.Fatalf("listed %v", names(repos))
	}
	if *repos[0].Fork || *repos[0].Archived || repos[0].Private {
		t.Errorf("a plain repo came back flagged: %+v", repos[0])
	}
	if !*repos[1].Fork || !*repos[1].Archived || !repos[1].Private {
		t.Errorf("a flagged repo came back plain: %+v", repos[1])
	}
	// With all three known, the filters can run.
	outcome, err := resolveSelection(repos, "all-nfna")
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(names(outcome.repos), []string{"me/a"}) {
		t.Errorf("all-nfna kept %v", names(outcome.repos))
	}
}

func TestListReposSortsByFullNameSoAnIndexMeansTheSameThingTwice(t *testing.T) {
	// `--select=1,3` in a script has to pick the same repositories on every run and on every
	// machine. The provider's own page order is not a promise, and a locale-aware sort would make
	// the answer depend on who ran it.
	srv := fakeProvider(t, map[string]any{
		"/users/me/repos": []any{
			listingItem("me", "zulu", nil),
			listingItem("me", "Alpha", nil),
			listingItem("me", "alpha", nil),
			listingItem("me", "mike", nil),
		},
	})
	repos, err := listRepos(listRequest{provider: "github", host: srv.URL, account: "me", client: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	// Plain byte order: uppercase sorts before lowercase, which is what every machine agrees on.
	if got := names(repos); !equalStrings(got, []string{"me/Alpha", "me/alpha", "me/mike", "me/zulu"}) {
		t.Errorf("order = %v", got)
	}
}

func TestListReposFallsBackFromTheUserEndpointToTheOrganisationOne(t *testing.T) {
	// A personal account is the common case so it is tried first, but an organisation 404s there.
	// Without the fallback, `--account=my-org` simply says "no such account".
	srv := fakeProvider(t, map[string]any{
		"/orgs/acme/repos": []any{listingItem("acme", "tool", nil)},
	})
	repos, err := listRepos(listRequest{provider: "github", host: srv.URL, account: "acme", client: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(names(repos), []string{"acme/tool"}) {
		t.Errorf("listed %v", names(repos))
	}

	// Neither endpoint knows the account: the 404 from the last attempt is what the user sees.
	_, err = listRepos(listRequest{provider: "github", host: srv.URL, account: "nobody", client: srv.Client()})
	if err == nil {
		t.Fatal("an unknown account listed successfully")
	}
	mustContain(t, err.Error(), "no such account (HTTP 404)", "a 404 was not reported as a missing account")
}

func TestListReposUsesEachProvidersOwnEndpoints(t *testing.T) {
	// Getting a path wrong is a 404 that reads like "no such account", which sends the user to
	// check their spelling instead of the tool.
	cases := []struct {
		provider string
		path     string
		body     any
		want     []string
	}{
		{"gitea", "/api/v1/users/me/repos", []any{listingItem("me", "g", nil)}, []string{"me/g"}},
		{"forgejo", "/api/v1/users/me/repos", []any{listingItem("me", "f", nil)}, []string{"me/f"}},
		{"gitlab", "/api/v4/users/me/projects",
			[]any{map[string]any{"path_with_namespace": "me/proj", "visibility": "public"}}, []string{"me/proj"}},
	}
	for _, c := range cases {
		t.Run(c.provider, func(t *testing.T) {
			srv := fakeProvider(t, map[string]any{c.path: c.body})
			repos, err := listRepos(listRequest{provider: c.provider, host: srv.URL, account: "me", client: srv.Client()})
			if err != nil {
				t.Fatal(err)
			}
			if !equalStrings(names(repos), c.want) {
				t.Errorf("listed %v, want %v", names(repos), c.want)
			}
		})
	}
}

func TestAGitLabUserListingReportsForkAndArchivedAsUnknown(t *testing.T) {
	// The simple representation a user project listing returns carries neither flag. `false`
	// would mean "not a fork"; nil means "this listing never said", which is what lets
	// resolveSelection refuse `all-nf` instead of quietly keeping every fork.
	srv := fakeProvider(t, map[string]any{
		"/api/v4/users/me/projects": []any{
			map[string]any{"path_with_namespace": "me/plain", "visibility": "public"},
			map[string]any{"path_with_namespace": "me/mirror", "description": "a fork, but the listing does not say so", "visibility": "public"},
		},
	})
	repos, err := listRepos(listRequest{provider: "gitlab", host: srv.URL, account: "me", client: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range repos {
		if r.Fork != nil || r.Archived != nil {
			t.Errorf("%s: fork=%v archived=%v, want both unknown", r.FullName, r.Fork, r.Archived)
		}
		if r.Private {
			t.Errorf("%s: a public project was reported private", r.FullName)
		}
	}
	// Visibility IS reported, so `np` still works against the same listing.
	if _, err := resolveSelection(repos, "all-np"); err != nil {
		t.Errorf("all-np: %v", err)
	}
	if _, err := resolveSelection(repos, "all-nf"); err == nil {
		t.Error("all-nf ran against a listing that cannot answer it")
	}
}

func TestAGitLabGroupListingReportsArchivedWhichItDoesCarry(t *testing.T) {
	// Group listings are richer than user listings, and the mapping must not flatten both to the
	// poorest common shape: `all-na` is usable here and has to stay usable.
	srv := fakeProvider(t, map[string]any{
		"/api/v4/groups/g/projects": []any{
			map[string]any{"path_with_namespace": "g/live", "visibility": "public", "archived": false},
			map[string]any{"path_with_namespace": "g/old", "visibility": "public", "archived": true},
			map[string]any{"path_with_namespace": "g/secret", "visibility": "private"},
		},
	})
	repos, err := listRepos(listRequest{provider: "gitlab", host: srv.URL, account: "g", client: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 3 {
		t.Fatalf("listed %v", names(repos))
	}
	// Sorted by full name: live, old, secret.
	if repos[0].Archived == nil || *repos[0].Archived || repos[1].Archived == nil || !*repos[1].Archived {
		t.Errorf("archived = %v / %v", repos[0].Archived, repos[1].Archived)
	}
	// `g/secret` omits `archived` entirely, so it stays unknown rather than being read as false.
	if repos[2].Archived != nil {
		t.Errorf("a project with no archived key was given one: %v", *repos[2].Archived)
	}
	if !repos[2].Private || repos[0].Private || repos[1].Private {
		t.Errorf("visibility mapped wrongly: %v", names(repos))
	}
	for _, r := range repos {
		if r.Fork != nil {
			t.Errorf("%s claims to know whether it is a fork; no GitLab listing does", r.FullName)
		}
	}
}

/* ---- failures the user has to act on -------------------------------------- */

func TestARateLimitedProviderBecomesAReadableError(t *testing.T) {
	// GitHub answers a rate limit with 403 and x-ratelimit-remaining: 0. Reporting that as "not
	// authorised" would send the user to make a token they may already have.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("x-ratelimit-remaining", "0")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
	}))
	defer srv.Close()

	_, err := listRepos(listRequest{provider: "github", host: srv.URL, account: "me", client: srv.Client()})
	if err == nil {
		t.Fatal("a rate limit listed successfully")
	}
	mustContain(t, err.Error(), "rate limited by", "the error does not name the cause")
	mustContain(t, err.Error(), "$GITHUB_TOKEN", "the error does not say what raises the limit")

	// With a token already set the advice changes: there is nothing left to set, so the only
	// thing to do is wait.
	_, err = listRepos(listRequest{provider: "github", host: srv.URL, account: "me", token: secret, client: srv.Client()})
	if err == nil {
		t.Fatal("a rate limit listed successfully")
	}
	mustContain(t, err.Error(), "Wait for the limit to reset", "an authenticated user is told to set a token they already set")
	mustNotContain(t, err.Error(), secret, "the token leaked into an error message")
}

func TestAnUnauthorisedListingSaysWhichVariableToSet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	_, err := listRepos(listRequest{provider: "forgejo", host: srv.URL, account: "me", client: srv.Client()})
	if err == nil {
		t.Fatal("a 401 listed successfully")
	}
	mustContain(t, err.Error(), "not authorised (HTTP 401)", "the status is not reported")
	mustContain(t, err.Error(), "$FRZNFORGE_FORGEJO_TOKEN", "the error does not name the variable to set")
}

func TestANonListResponseIsReportedAsSuchRatherThanCrashing(t *testing.T) {
	// A misconfigured host (a proxy login page, an HTML error) answers 200 with something that is
	// not an array. "did not return a list of repositories" points at the host; a decode panic
	// points at nothing.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"message":"hello"}`))
	}))
	defer srv.Close()
	_, err := listRepos(listRequest{provider: "gitea", host: srv.URL, account: "me", client: srv.Client()})
	if err == nil || !strings.Contains(err.Error(), "did not return a list of repositories") {
		t.Errorf("err = %v", err)
	}
}

func TestATruncatedListingSaysSoOutLoud(t *testing.T) {
	// A full last page means there was more. A caller cannot tell a truncated listing from a
	// complete one, and `--select=1,3,5-8` written against a truncated one silently picks the
	// wrong repositories — so the warning names the escape hatch.
	const perPage = 50 // gitea's page size; a full page is what makes paginate ask for another
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		page := make([]any, perPage)
		for i := range page {
			page[i] = listingItem("me", fmt.Sprintf("r%03d", i), nil)
		}
		_ = json.NewEncoder(w).Encode(page)
	}))
	defer srv.Close()

	var warnings []string
	repos, err := listRepos(listRequest{
		provider: "gitea", host: srv.URL, account: "me", client: srv.Client(),
		maxPages: 2,
		warn:     func(line string) { warnings = append(warnings, line) },
	})
	if err != nil {
		t.Fatal(err)
	}
	// Two pages were taken and then the walk stopped; the repos it did get are still returned,
	// because an incomplete listing the user is warned about beats no listing at all.
	if len(repos) != 2*perPage {
		t.Errorf("got %d repos from a 2-page cap", len(repos))
	}
	joined := strings.Join(warnings, "\n")
	mustContain(t, joined, "listing stopped after 100 repositories", "the truncation was not reported")
	mustContain(t, joined, "--select=name,name", "the warning does not offer a way to name what you want")
}

func TestAShortPageEndsTheWalkWithoutAWarning(t *testing.T) {
	// The counterpart to the test above: a listing that fits must never be reported as truncated,
	// or every ordinary run prints a warning nobody can act on.
	srv := fakeProvider(t, map[string]any{
		"/api/v1/users/me/repos": []any{listingItem("me", "only", nil)},
	})
	var warnings []string
	if _, err := listRepos(listRequest{
		provider: "gitea", host: srv.URL, account: "me", client: srv.Client(),
		maxPages: 1, warn: func(line string) { warnings = append(warnings, line) },
	}); err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("a complete listing warned: %v", warnings)
	}
}

func TestListReposSendsEachProvidersOwnAuthHeader(t *testing.T) {
	// The three forges spell authentication differently, and sending the wrong header is a
	// listing that silently shows only public repositories — which looks exactly like having no
	// token at all.
	cases := []struct {
		provider, path, header, value string
	}{
		{"github", "/users/me/repos", "authorization", "Bearer " + secret},
		{"gitlab", "/api/v4/users/me/projects", "private-token", secret},
		{"gitea", "/api/v1/users/me/repos", "authorization", "token " + secret},
		{"forgejo", "/api/v1/users/me/repos", "authorization", "token " + secret},
	}
	for _, c := range cases {
		t.Run(c.provider, func(t *testing.T) {
			var got string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.Header.Get(c.header)
				_, _ = w.Write([]byte(`[]`))
			}))
			defer srv.Close()
			if _, err := listRepos(listRequest{
				provider: c.provider, host: srv.URL, account: "me", token: secret, client: srv.Client(),
			}); err != nil {
				t.Fatal(err)
			}
			if got != c.value {
				t.Errorf("%s sent %s: %q, want %q", c.provider, c.header, got, c.value)
			}
		})
	}
}
