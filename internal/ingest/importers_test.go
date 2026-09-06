package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"frznforge/internal/config"
	"frznforge/internal/model"
)

// Provider importer tests. Every request is served from the recorded fixtures in
// tests/fixtures/http through an injected Doer — nothing here touches the network.

var isoDateShape = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`)

var (
	githubTestSource = config.RepoSourceConfig{
		Type: "github", Owner: "Descent098", Repo: "ezcv", Host: "https://api.github.com",
	}
	gitlabTestSource = config.RepoSourceConfig{
		Type: "gitlab", Project: "gitlab-org/gitlab-runner", Host: "https://gitlab.com",
	}
	giteaTestSource = config.RepoSourceConfig{
		Type: "gitea", Host: "https://gitea.com", Owner: "gitea", Repo: "tea",
	}
	forgejoTestSource = config.RepoSourceConfig{
		Type: "forgejo", Host: "https://codeberg.org", Owner: "forgejo", Repo: "forgejo",
	}
)

func derefStr(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func wantStr(t testing.TB, what string, got *string, want string) {
	t.Helper()
	if got == nil || *got != want {
		t.Errorf("%s = %s, want %q", what, derefStr(got), want)
	}
}

func wantNil(t testing.TB, what string, got *string) {
	t.Helper()
	if got != nil {
		t.Errorf("%s = %q, want nil", what, *got)
	}
}

func tags(releases []model.Release) []string {
	out := make([]string, len(releases))
	for i, r := range releases {
		out[i] = r.Tag
	}
	return out
}

func assetNames(assets []model.ReleaseAsset) []string {
	out := make([]string, len(assets))
	for i, a := range assets {
		out[i] = a.Name
	}
	return out
}

func wantSequence(t testing.TB, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func checkReleasesValid(t testing.TB, releases []model.Release) {
	t.Helper()
	for _, r := range releases {
		if !isoDateShape.MatchString(r.PublishedAt) {
			t.Errorf("release %s publishedAt = %q, not the artifact's ISO profile", r.Tag, r.PublishedAt)
		}
		if r.Assets == nil {
			t.Errorf("release %s has a nil assets slice, which serialises as null", r.Tag)
		}
	}
}

/* ---- GitHub --------------------------------------------------------------- */

func TestGithubImporterMapsRepoMetadata(t *testing.T) {
	resetSharedBackoff(t)
	doer := newHTTPFixtureDoer(httpFixtureRoute{
		Pattern: "/repos/Descent098/ezcv", Body: loadHTTPFixture(t, "github/repo.json"),
	})
	imp := NewGithubImporter(githubTestSource, ImporterContext{HTTP: doer})
	meta, err := imp.FetchMeta(context.Background())
	if err != nil {
		t.Fatalf("FetchMeta: %v", err)
	}

	wantStr(t, "name", meta.Name, "ezcv")
	wantStr(t, "description", meta.Description,
		"A python-based static site generator for setting up a CV/Resume site")
	wantStr(t, "homepage", meta.Homepage, "https://ezcv.readthedocs.io/en/latest/")
	wantStr(t, "license", meta.License, "MIT")
	wantStr(t, "defaultBranch", meta.DefaultBranch, "master")
	wantStr(t, "issuesUrl", meta.IssuesURL, "https://github.com/Descent098/ezcv/issues")
	if meta.WebURL != "https://github.com/Descent098/ezcv" {
		t.Errorf("webUrl = %q", meta.WebURL)
	}
	if meta.CloneURL != "https://github.com/Descent098/ezcv.git" {
		t.Errorf("cloneUrl = %q", meta.CloneURL)
	}
	for _, topic := range []string{"cli", "python", "static-site-generator"} {
		if !containsString(meta.Topics, topic) {
			t.Errorf("topics %v missing %q", meta.Topics, topic)
		}
	}
	if meta.Template || meta.Archived {
		t.Errorf("template=%v archived=%v", meta.Template, meta.Archived)
	}
	if imp.Provider() != "github" {
		t.Errorf("provider = %q", imp.Provider())
	}
}

func TestGithubImporterMapsReleasesNewestFirst(t *testing.T) {
	resetSharedBackoff(t)
	doer := newHTTPFixtureDoer(httpFixtureRoute{
		Pattern: "/releases", Body: loadHTTPFixture(t, "github/releases.json"),
	})
	imported, err := NewGithubImporter(githubTestSource, ImporterContext{HTTP: doer}).
		FetchReleases(context.Background())
	if err != nil {
		t.Fatalf("FetchReleases: %v", err)
	}

	wantSequence(t, "tags", tags(imported.Releases), []string{"v0.3.5", "v0.3.3", "v0.2.0", "V0.1.1"})
	checkReleasesValid(t, imported.Releases)

	latest := imported.Releases[0]
	if latest.Name != "V0.3.5; November 17th 2023" {
		t.Errorf("name = %q", latest.Name)
	}
	wantStr(t, "url", latest.URL, "https://github.com/Descent098/ezcv/releases/tag/v0.3.5")
	if latest.Prerelease {
		t.Error("v0.3.5 is not a prerelease")
	}
	if latest.PublishedAt != "2023-11-17T21:01:40Z" {
		t.Errorf("publishedAt = %q", latest.PublishedAt)
	}
	wantStr(t, "author", latest.Author, "Descent098")
	if len(latest.Assets) != 0 {
		t.Errorf("assets = %v", latest.Assets)
	}
}

func TestGithubImporterMapsAssetsAndSortsThemByName(t *testing.T) {
	resetSharedBackoff(t)
	doer := newHTTPFixtureDoer(httpFixtureRoute{
		Pattern: "/releases", Body: loadHTTPFixture(t, "github/releases-with-assets.json"),
	})
	imported, err := NewGithubImporter(githubTestSource, ImporterContext{HTTP: doer}).
		FetchReleases(context.Background())
	if err != nil {
		t.Fatalf("FetchReleases: %v", err)
	}
	latest := imported.Releases[0]
	wantSequence(t, "asset names", assetNames(latest.Assets), []string{
		"gh_2.98.0_checksums.txt", "gh_2.98.0_linux_386.deb", "gh_2.98.0_linux_386.rpm",
	})
	first := latest.Assets[0]
	if first.URL != "https://github.com/cli/cli/releases/download/v2.98.0/gh_2.98.0_checksums.txt" {
		t.Errorf("asset url = %q", first.URL)
	}
	if first.Size != 1950 {
		t.Errorf("asset size = %d", first.Size)
	}
	wantStr(t, "asset contentType", first.ContentType, "text/plain; charset=utf-8")

	// Volatile counters must never reach the artifact.
	encoded, err := json.Marshal(latest.Assets)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "download_count") {
		t.Errorf("a download counter reached the artifact: %s", encoded)
	}
}

func TestGithubImporterSkipsDraftReleases(t *testing.T) {
	resetSharedBackoff(t)
	// A draft would appear and disappear with the token used to build.
	var recorded []json.RawMessage
	if err := json.Unmarshal([]byte(loadHTTPFixture(t, "github/releases.json")), &recorded); err != nil {
		t.Fatal(err)
	}
	draft := json.RawMessage(`{"tag_name":"v9.9.9-draft","name":"unpublished","draft":true,` +
		`"prerelease":false,"created_at":"2026-01-01T00:00:00Z","published_at":null,"assets":[]}`)
	body, err := json.Marshal(append([]json.RawMessage{draft}, recorded...))
	if err != nil {
		t.Fatal(err)
	}

	doer := newHTTPFixtureDoer(httpFixtureRoute{Pattern: "/releases", Body: string(body)})
	imported, err := NewGithubImporter(githubTestSource, ImporterContext{HTTP: doer}).
		FetchReleases(context.Background())
	if err != nil {
		t.Fatalf("FetchReleases: %v", err)
	}
	if len(imported.Releases) != 4 {
		t.Fatalf("kept %d releases, want 4", len(imported.Releases))
	}
	if containsString(tags(imported.Releases), "v9.9.9-draft") {
		t.Error("a draft release reached the artifact")
	}
}

func TestGithubImporterFollowsLinkHeaderPagination(t *testing.T) {
	resetSharedBackoff(t)
	var all []json.RawMessage
	if err := json.Unmarshal([]byte(loadHTTPFixture(t, "github/releases.json")), &all); err != nil {
		t.Fatal(err)
	}
	page1, err := json.Marshal(all[:2])
	if err != nil {
		t.Fatal(err)
	}
	page2, err := json.Marshal(all[2:])
	if err != nil {
		t.Fatal(err)
	}

	doer := newHTTPFixtureDoer(
		httpFixtureRoute{Pattern: "page=2", Body: string(page2)},
		httpFixtureRoute{Pattern: "/releases", Body: string(page1), Headers: map[string]string{
			"link": `<https://api.github.com/repos/Descent098/ezcv/releases?per_page=100&page=2>; rel="next"`,
		}},
	)
	imported, err := NewGithubImporter(githubTestSource, ImporterContext{HTTP: doer}).
		FetchReleases(context.Background())
	if err != nil {
		t.Fatalf("FetchReleases: %v", err)
	}
	if len(imported.Releases) != 4 {
		t.Fatalf("releases = %d, want 4", len(imported.Releases))
	}
	calls := doer.recorded()
	if len(calls) != 2 {
		t.Fatalf("requests = %d, want 2", len(calls))
	}
	if !strings.Contains(calls[1].URL, "page=2") {
		t.Errorf("second request = %q", calls[1].URL)
	}
}

func TestGithubImporterSendsABearerTokenOrNothing(t *testing.T) {
	resetSharedBackoff(t)
	route := httpFixtureRoute{Pattern: "/repos/Descent098/ezcv", Body: loadHTTPFixture(t, "github/repo.json")}

	authed := newHTTPFixtureDoer(route)
	if _, err := NewGithubImporter(githubTestSource, ImporterContext{HTTP: authed, Token: "gh-secret"}).
		FetchMeta(context.Background()); err != nil {
		t.Fatal(err)
	}
	headers := authed.recorded()[0].Headers
	if got := headers.Get("authorization"); got != "Bearer gh-secret" {
		t.Errorf("authorization = %q", got)
	}
	if got := headers.Get("user-agent"); got != DefaultUserAgent && !strings.HasPrefix(got, DefaultUserAgent+"/") {
		t.Errorf("user-agent = %q", got)
	}
	if got := headers.Get("accept"); got != "application/vnd.github+json" {
		t.Errorf("accept = %q", got)
	}

	anon := newHTTPFixtureDoer(route)
	if _, err := NewGithubImporter(githubTestSource, ImporterContext{HTTP: anon}).
		FetchMeta(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := anon.recorded()[0].Headers.Get("authorization"); got != "" {
		t.Errorf("an anonymous client sent authorization = %q", got)
	}
}

/* ---- GitLab --------------------------------------------------------------- */

func TestGitlabImporterMapsProjectMetadata(t *testing.T) {
	resetSharedBackoff(t)
	doer := newHTTPFixtureDoer(httpFixtureRoute{
		Pattern: "/projects/", Body: loadHTTPFixture(t, "gitlab/project.json"),
	})
	meta, err := NewGitlabImporter(gitlabTestSource, ImporterContext{HTTP: doer}).
		FetchMeta(context.Background())
	if err != nil {
		t.Fatalf("FetchMeta: %v", err)
	}
	// The license only comes back with ?license=true, which is why it is on the URL.
	if got := doer.recorded()[0].URL; !strings.Contains(got, "/api/v4/projects/gitlab-org%2Fgitlab-runner?license=true") {
		t.Errorf("request URL = %q", got)
	}

	wantStr(t, "name", meta.Name, "gitlab-runner")
	wantStr(t, "description", meta.Description,
		"GitLab Runner is the open source project that is used to run your CI/CD jobs and send the results back to GitLab")
	wantNil(t, "homepage", meta.Homepage)
	wantSequence(t, "topics", meta.Topics, []string{"golang", "hacktoberfest"})
	wantStr(t, "license", meta.License, "MIT")
	wantStr(t, "defaultBranch", meta.DefaultBranch, "main")
	if meta.WebURL != "https://gitlab.com/gitlab-org/gitlab-runner" {
		t.Errorf("webUrl = %q", meta.WebURL)
	}
	if meta.CloneURL != "https://gitlab.com/gitlab-org/gitlab-runner.git" {
		t.Errorf("cloneUrl = %q", meta.CloneURL)
	}
	wantStr(t, "issuesUrl", meta.IssuesURL, "https://gitlab.com/gitlab-org/gitlab-runner/-/issues")
	if meta.Template || meta.Archived {
		t.Errorf("template=%v archived=%v", meta.Template, meta.Archived)
	}
}

func TestGitlabImporterMapsReleasesAndBothAssetKinds(t *testing.T) {
	resetSharedBackoff(t)
	doer := newHTTPFixtureDoer(httpFixtureRoute{
		Pattern: "/releases", Body: loadHTTPFixture(t, "gitlab/releases.json"),
	})
	imported, err := NewGitlabImporter(gitlabTestSource, ImporterContext{HTTP: doer}).
		FetchReleases(context.Background())
	if err != nil {
		t.Fatalf("FetchReleases: %v", err)
	}
	// Date order, not tag order: v19.0.3 was released a day after v19.2.1.
	wantSequence(t, "tags", tags(imported.Releases), []string{"v19.3.0", "v19.2.2", "v19.0.3", "v19.2.1"})
	checkReleasesValid(t, imported.Releases)

	latest := imported.Releases[0]
	if latest.PublishedAt != "2026-08-19T21:10:48Z" {
		t.Errorf("publishedAt = %q (millisecond precision must be truncated, not rounded)", latest.PublishedAt)
	}
	wantStr(t, "url", latest.URL, "https://gitlab.com/gitlab-org/gitlab-runner/-/releases/v19.3.0")
	wantStr(t, "author", latest.Author, "ashvins")
	if !strings.Contains(latest.Body, "v19.3.0") {
		t.Errorf("body did not carry the release notes: %.80q", latest.Body)
	}
	// 3 uploaded links + the 4 archives GitLab generates, sorted by name, sizes unknown.
	if len(latest.Assets) != 7 {
		t.Fatalf("assets = %d, want 7", len(latest.Assets))
	}
	names := assetNames(latest.Assets)
	sorted := append([]string(nil), names...)
	sortStringsCodePoint(sorted)
	wantSequence(t, "asset order", names, sorted)
	sources := 0
	for _, a := range latest.Assets {
		if strings.HasPrefix(a.Name, "Source code") {
			sources++
		}
		if a.Size != 0 || a.ContentType != nil {
			t.Errorf("GitLab reports no size or content type, got %+v", a)
		}
	}
	if sources != 4 {
		t.Errorf("generated source archives = %d, want 4", sources)
	}
}

func TestGitlabImporterAbsolutisesHostRelativeURLs(t *testing.T) {
	resetSharedBackoff(t)
	// direct_asset_url is routinely host-relative. Left as-is it renders on the generated site as
	// a same-origin link (badged "external link") that 404s.
	body := `[{"tag_name":"v1.0.0","released_at":"2026-01-01T00:00:00Z",
		"_links":{"self":"/gitlab-org/gitlab-runner/-/releases/v1.0.0"},
		"assets":{"links":[{"name":"installer.exe","direct_asset_url":"/gitlab-org/gitlab-runner/-/releases/v1.0.0/downloads/installer.exe"}],
		"sources":[{"format":"zip","url":"/gitlab-org/gitlab-runner/-/archive/v1.0.0/x.zip"}]}}]`
	doer := newHTTPFixtureDoer(httpFixtureRoute{Pattern: "/releases", Body: body})
	imported, err := NewGitlabImporter(gitlabTestSource, ImporterContext{HTTP: doer}).
		FetchReleases(context.Background())
	if err != nil {
		t.Fatalf("FetchReleases: %v", err)
	}
	release := imported.Releases[0]
	wantStr(t, "url", release.URL, "https://gitlab.com/gitlab-org/gitlab-runner/-/releases/v1.0.0")
	for _, a := range release.Assets {
		if !strings.HasPrefix(a.URL, "https://gitlab.com/") {
			t.Errorf("asset URL stayed relative: %q", a.URL)
		}
	}
}

func TestGitlabImporterNeverImportsUpcomingRelease(t *testing.T) {
	resetSharedBackoff(t)
	// GitLab computes upcoming_release from `released_at > now`, so it flips on its own and would
	// change forge.json (and the Latest/Pre-release badges) with no change to the repo.
	body := `[{"tag_name":"v2.0.0","released_at":"2099-01-01T00:00:00Z","upcoming_release":true},
		{"tag_name":"v1.0.0","released_at":"2026-01-01T00:00:00Z","upcoming_release":false}]`
	doer := newHTTPFixtureDoer(httpFixtureRoute{Pattern: "/releases", Body: body})
	imported, err := NewGitlabImporter(gitlabTestSource, ImporterContext{HTTP: doer}).
		FetchReleases(context.Background())
	if err != nil {
		t.Fatalf("FetchReleases: %v", err)
	}
	for _, r := range imported.Releases {
		if r.Prerelease {
			t.Errorf("%s imported a wall-clock-derived prerelease flag", r.Tag)
		}
	}
}

func TestGitlabImporterFollowsXNextPagePagination(t *testing.T) {
	resetSharedBackoff(t)
	var all []json.RawMessage
	if err := json.Unmarshal([]byte(loadHTTPFixture(t, "gitlab/releases.json")), &all); err != nil {
		t.Fatal(err)
	}
	page1, _ := json.Marshal(all[:2])
	page2, _ := json.Marshal(all[2:])
	doer := newHTTPFixtureDoer(
		httpFixtureRoute{Pattern: "page=2", Body: string(page2)},
		httpFixtureRoute{Pattern: "/releases", Body: string(page1),
			Headers: map[string]string{"x-next-page": "2"}},
	)
	imported, err := NewGitlabImporter(gitlabTestSource, ImporterContext{HTTP: doer}).
		FetchReleases(context.Background())
	if err != nil {
		t.Fatalf("FetchReleases: %v", err)
	}
	if len(imported.Releases) != 4 {
		t.Fatalf("releases = %d, want 4", len(imported.Releases))
	}
	if got := doer.recorded()[1].URL; !strings.Contains(got, "page=2") {
		t.Errorf("second request = %q", got)
	}
}

func TestGitlabImporterSendsPrivateToken(t *testing.T) {
	resetSharedBackoff(t)
	doer := newHTTPFixtureDoer(httpFixtureRoute{
		Pattern: "/projects/", Body: loadHTTPFixture(t, "gitlab/project.json"),
	})
	if _, err := NewGitlabImporter(gitlabTestSource, ImporterContext{HTTP: doer, Token: "gl-secret"}).
		FetchMeta(context.Background()); err != nil {
		t.Fatal(err)
	}
	headers := doer.recorded()[0].Headers
	if got := headers.Get("private-token"); got != "gl-secret" {
		t.Errorf("private-token = %q", got)
	}
	if got := headers.Get("authorization"); got != "" {
		t.Errorf("GitLab must not get an Authorization header, got %q", got)
	}
}

/* ---- Gitea / Forgejo ------------------------------------------------------ */

func TestGiteaImporterMapsRepoMetadataIncludingLicensesArray(t *testing.T) {
	resetSharedBackoff(t)
	doer := newHTTPFixtureDoer(httpFixtureRoute{
		Pattern: "/repos/gitea/tea", Body: loadHTTPFixture(t, "gitea/repo.json"),
	})
	meta, err := NewGiteaImporter(giteaTestSource, ImporterContext{HTTP: doer}).
		FetchMeta(context.Background())
	if err != nil {
		t.Fatalf("FetchMeta: %v", err)
	}
	wantStr(t, "name", meta.Name, "tea")
	wantStr(t, "description", meta.Description, "A command line tool to interact with Gitea servers")
	wantNil(t, "homepage", meta.Homepage)
	wantSequence(t, "topics", meta.Topics, []string{"gitea", "cli"})
	wantStr(t, "license", meta.License, "MIT")
	wantStr(t, "defaultBranch", meta.DefaultBranch, "main")
	if meta.WebURL != "https://gitea.com/gitea/tea" || meta.CloneURL != "https://gitea.com/gitea/tea.git" {
		t.Errorf("webUrl=%q cloneUrl=%q", meta.WebURL, meta.CloneURL)
	}
	wantStr(t, "issuesUrl", meta.IssuesURL, "https://gitea.com/gitea/tea/issues")
	if meta.Template || meta.Archived {
		t.Errorf("template=%v archived=%v", meta.Template, meta.Archived)
	}
}

func TestGiteaImporterMapsReleasesAndAssets(t *testing.T) {
	resetSharedBackoff(t)
	doer := newHTTPFixtureDoer(httpFixtureRoute{
		Pattern: "/releases", Body: loadHTTPFixture(t, "gitea/releases.json"),
	})
	imported, err := NewGiteaImporter(giteaTestSource, ImporterContext{HTTP: doer}).
		FetchReleases(context.Background())
	if err != nil {
		t.Fatalf("FetchReleases: %v", err)
	}
	wantSequence(t, "tags", tags(imported.Releases), []string{"v0.15.1", "v0.15.0", "v0.14.2", "v0.14.1"})
	checkReleasesValid(t, imported.Releases)

	latest := imported.Releases[0]
	if latest.PublishedAt != "2026-08-02T14:40:05Z" {
		t.Errorf("publishedAt = %q", latest.PublishedAt)
	}
	wantStr(t, "author", latest.Author, "giteabot")
	first := latest.Assets[0]
	if first.Name != "checksums.txt" ||
		first.URL != "https://gitea.com/gitea/tea/releases/download/v0.15.1/checksums.txt" ||
		first.Size != 1842 || first.ContentType != nil {
		t.Errorf("first asset = %+v", first)
	}
}

func TestGiteaImporterSendsATokenHeader(t *testing.T) {
	resetSharedBackoff(t)
	doer := newHTTPFixtureDoer(httpFixtureRoute{
		Pattern: "/repos/gitea/tea", Body: loadHTTPFixture(t, "gitea/repo.json"),
	})
	if _, err := NewGiteaImporter(giteaTestSource, ImporterContext{HTTP: doer, Token: "gt-secret"}).
		FetchMeta(context.Background()); err != nil {
		t.Fatal(err)
	}
	call := doer.recorded()[0]
	if got := call.Headers.Get("authorization"); got != "token gt-secret" {
		t.Errorf("authorization = %q", got)
	}
	if call.URL != "https://gitea.com/api/v1/repos/gitea/tea" {
		t.Errorf("URL = %q", call.URL)
	}
}

func TestForgejoImporterReportsItsOwnProviderAndToleratesNoLicense(t *testing.T) {
	resetSharedBackoff(t)
	doer := newHTTPFixtureDoer(httpFixtureRoute{
		Pattern: "/repos/forgejo/forgejo", Body: loadHTTPFixture(t, "forgejo/repo.json"),
	})
	imp := NewGiteaImporter(forgejoTestSource, ImporterContext{HTTP: doer})
	if imp.Provider() != "forgejo" {
		t.Fatalf("provider = %q", imp.Provider())
	}
	meta, err := imp.FetchMeta(context.Background())
	if err != nil {
		t.Fatalf("FetchMeta: %v", err)
	}
	wantStr(t, "name", meta.Name, "forgejo")
	wantStr(t, "description", meta.Description, "Beyond coding. We forge.")
	wantStr(t, "homepage", meta.Homepage, "https://forgejo.org")
	wantSequence(t, "topics", meta.Topics, []string{"forge", "forgejo", "git", "self-hosted"})
	// Forgejo has no license field at all; the scanner sniffs the cloned LICENSE instead.
	wantNil(t, "license", meta.License)
	wantStr(t, "defaultBranch", meta.DefaultBranch, "forgejo")
	if meta.WebURL != "https://codeberg.org/forgejo/forgejo" {
		t.Errorf("webUrl = %q", meta.WebURL)
	}
	wantStr(t, "issuesUrl", meta.IssuesURL, "https://codeberg.org/forgejo/forgejo/issues")
}

func TestForgejoImporterNormalisesOffsetTimestamps(t *testing.T) {
	resetSharedBackoff(t)
	doer := newHTTPFixtureDoer(httpFixtureRoute{
		Pattern: "/releases", Body: loadHTTPFixture(t, "forgejo/releases.json"),
	})
	imported, err := NewGiteaImporter(forgejoTestSource, ImporterContext{HTTP: doer}).
		FetchReleases(context.Background())
	if err != nil {
		t.Fatalf("FetchReleases: %v", err)
	}
	// v15.0.7 was published minutes after v16.0.2 but before v16.0.3: date order, not tag order.
	wantSequence(t, "tags", tags(imported.Releases), []string{"v16.0.3", "v15.0.7", "v16.0.2"})
	if imported.Releases[0].PublishedAt != "2026-08-20T08:49:50Z" {
		t.Errorf("publishedAt = %q (a +02:00 offset must land in UTC)", imported.Releases[0].PublishedAt)
	}
	checkReleasesValid(t, imported.Releases)
}

/* ---- registry ------------------------------------------------------------- */

func TestCreateImporterPicksTheRightProvider(t *testing.T) {
	if imp := CreateImporter(config.RepoSourceConfig{Type: "local", Path: "./repo"}, ImporterContext{}); imp != nil {
		t.Errorf("local sources have nothing to import, got %v", imp)
	}
	for _, tc := range []struct {
		source config.RepoSourceConfig
		want   string
	}{
		{githubTestSource, "github"},
		{gitlabTestSource, "gitlab"},
		{giteaTestSource, "gitea"},
		{forgejoTestSource, "forgejo"},
	} {
		imp := CreateImporter(tc.source, ImporterContext{})
		if imp == nil {
			t.Fatalf("%s: no importer", tc.want)
		}
		if imp.Provider() != tc.want {
			t.Errorf("provider = %q, want %q", imp.Provider(), tc.want)
		}
	}
}

func TestTokenComesFromTheEnvironmentOnly(t *testing.T) {
	source := config.RepoSourceConfig{Type: "gitea", Host: "https://gitea.example.com", Owner: "a", Repo: "b"}
	wantSequence(t, "token env", TokenEnvFor(source), []string{"FRZNFORGE_GITEA_TOKEN", "GITEA_TOKEN"})
	// An explicit tokenEnv replaces the defaults entirely.
	explicit := source
	explicit.TokenEnv = "MY_TOKEN"
	wantSequence(t, "token env", TokenEnvFor(explicit), []string{"MY_TOKEN"})
	if got := TokenEnvFor(config.RepoSourceConfig{Type: "local", Path: "."}); len(got) != 0 {
		t.Errorf("local token env = %v", got)
	}

	// FRZNFORGE_* wins, values are trimmed, and an empty variable is not a token.
	if got := ResolveToken(source, Env{"GITEA_TOKEN": "second", "FRZNFORGE_GITEA_TOKEN": " first "}); got != "first" {
		t.Errorf("token = %q", got)
	}
	if got := ResolveToken(source, Env{"FRZNFORGE_GITEA_TOKEN": "   ", "GITEA_TOKEN": "second"}); got != "second" {
		t.Errorf("token = %q", got)
	}
	// A non-nil empty environment is "this build has no tokens", never "go look at os.Environ".
	if got := ResolveToken(source, Env{}); got != "" {
		t.Errorf("token = %q", got)
	}
}

/* ---- failures ------------------------------------------------------------- */

func githubFailure(t testing.TB, route httpFixtureRoute) *ImporterError {
	t.Helper()
	resetSharedBackoff(t)
	route.Pattern = "/repos/"
	doer := newHTTPFixtureDoer(route)
	_, err := NewGithubImporter(githubTestSource, ImporterContext{HTTP: doer}).FetchMeta(context.Background())
	var ie *ImporterError
	if !errors.As(err, &ie) {
		t.Fatalf("want an *ImporterError, got %v", err)
	}
	return ie
}

func TestImporter404MapsToNotFound(t *testing.T) {
	err := githubFailure(t, httpFixtureRoute{Status: 404, Body: loadHTTPFixture(t, "github/repo-404.json")})
	if err.Kind != KindNotFound || err.Status != 404 {
		t.Fatalf("kind=%q status=%d", err.Kind, err.Status)
	}
	if !strings.Contains(err.Message, "Not Found") {
		t.Errorf("message = %q", err.Message)
	}
}

func TestImporter401MapsToAuthAndNamesTheMissingToken(t *testing.T) {
	err := githubFailure(t, httpFixtureRoute{Status: 401, Body: `{"message":"Bad credentials"}`})
	if err.Kind != KindAuth {
		t.Fatalf("kind = %q", err.Kind)
	}
	if !strings.Contains(err.Message, "no token configured") {
		t.Errorf("message = %q", err.Message)
	}
}

func TestImporter403WithRateLimitHeadersCarriesRetryAfter(t *testing.T) {
	reset := time.Now().Unix() + 120
	err := githubFailure(t, httpFixtureRoute{
		Status: 403,
		Body:   loadHTTPFixture(t, "github/rate-limited.json"),
		Headers: map[string]string{
			"x-ratelimit-remaining": "0",
			"x-ratelimit-reset":     strconv.FormatInt(reset, 10),
		},
	})
	if err.Kind != KindRateLimit {
		t.Fatalf("kind = %q", err.Kind)
	}
	if err.RetryAfter == nil || *err.RetryAfter <= 100 || *err.RetryAfter > 120 {
		t.Fatalf("retryAfter = %v, want 100 < n <= 120", err.RetryAfter)
	}
}

func TestImporterKeepsTheClockAndTheCallersIPOutOfTheMessage(t *testing.T) {
	// The message ships verbatim in forge.json. A countdown ("retry after 3599s") makes two
	// builds of the same repos differ; GitHub's anonymous body quotes the build host's IP.
	reset := time.Now().Unix() + 3599
	err := githubFailure(t, httpFixtureRoute{
		Status: 403,
		Body:   `{"message":"API rate limit exceeded for 203.0.113.1. (But here is the good news: …)"}`,
		Headers: map[string]string{
			"x-ratelimit-remaining": "0",
			"x-ratelimit-reset":     strconv.FormatInt(reset, 10),
		},
	})
	if err.Kind != KindRateLimit {
		t.Fatalf("kind = %q", err.Kind)
	}
	if regexp.MustCompile(`(?i)retry after`).MatchString(err.Message) {
		t.Errorf("a countdown reached the message: %q", err.Message)
	}
	if regexp.MustCompile(`\b\d{1,3}(\.\d{1,3}){3}\b`).MatchString(err.Message) {
		t.Errorf("an IP reached the message: %q", err.Message)
	}
	if !strings.Contains(err.Message, "[ip]") {
		t.Errorf("message = %q", err.Message)
	}
	// Still available to the caller for console output, just not in the artifact text.
	if err.RetryAfter == nil || *err.RetryAfter <= 0 {
		t.Errorf("retryAfter = %v", err.RetryAfter)
	}
}

func TestScrubIPs(t *testing.T) {
	if got := ScrubIPs("blocked 2001:db8:85a3:0:0:8a2e:370:7334 here"); got != "blocked [ip] here" {
		t.Errorf("IPv6 = %q", got)
	}
	if got := ScrubIPs("resets at 10:30:45"); got != "resets at 10:30:45" {
		t.Errorf("a clock time is not an address: %q", got)
	}
}

func TestImporter403WithoutRateLimitHeadersMapsToAuth(t *testing.T) {
	if err := githubFailure(t, httpFixtureRoute{Status: 403, Body: `{"message":"Forbidden"}`}); err.Kind != KindAuth {
		t.Fatalf("kind = %q", err.Kind)
	}
}

func TestImporterNonJSONBodyMapsToBadResponse(t *testing.T) {
	if err := githubFailure(t, httpFixtureRoute{
		Body:    "<html>upstream proxy error</html>",
		Headers: map[string]string{"content-type": "text/html"},
	}); err.Kind != KindBadResponse {
		t.Fatalf("kind = %q", err.Kind)
	}
}

func TestImporterTransportFailureMapsToNetwork(t *testing.T) {
	resetSharedBackoff(t)
	doer := &erroringDoer{err: errors.New("getaddrinfo ENOTFOUND api.github.com")}
	_, err := NewGithubImporter(githubTestSource, ImporterContext{HTTP: doer}).FetchMeta(context.Background())
	assertImporterKind(t, err, KindNetwork)
}

func TestImporterNeverLeaksTheTokenIntoAnErrorMessage(t *testing.T) {
	resetSharedBackoff(t)
	const token = "ghp_SUPER_SECRET_VALUE_0123456789"

	// A provider that echoes the credential back in its error body is the worst case.
	echo := newHTTPFixtureDoer(httpFixtureRoute{
		Pattern: "/repos/", Status: 401, Body: fmt.Sprintf(`{"message":"token %s is not valid"}`, token),
	})
	_, err := NewGithubImporter(githubTestSource, ImporterContext{HTTP: echo, Token: token}).
		FetchMeta(context.Background())
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("auth error leaked the token: %v", err)
	}
	if !strings.Contains(err.Error(), "***") {
		t.Errorf("message = %q", err.Error())
	}

	thrower := &erroringDoer{err: fmt.Errorf("connect failed for Bearer %s", token)}
	_, err = NewGithubImporter(githubTestSource, ImporterContext{HTTP: thrower, Token: token}).
		FetchMeta(context.Background())
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("network error leaked the token: %v", err)
	}
}

/* ---- JSONClient ----------------------------------------------------------- */

func plainClient(doer Doer, maxPages int) *JSONClient {
	return NewJSONClient(JSONClientOptions{Auth: AuthBearer, HTTP: doer, RetryDelay: -1, MaxPages: maxPages})
}

func TestJSONClientRetriesOnceAfterA5xx(t *testing.T) {
	resetSharedBackoff(t)
	doer := newScriptedDoer(
		httpFixtureRoute{Status: 502, Body: "{}"},
		httpFixtureRoute{Status: 200, Body: `{"ok":true}`},
	)
	if _, err := plainClient(doer, 0).Get(context.Background(), "https://example.test/thing"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if doer.count() != 2 {
		t.Fatalf("requests = %d, want 2", doer.count())
	}
}

func TestJSONClientRetriesOnceAfterATransportFailure(t *testing.T) {
	resetSharedBackoff(t)
	doer := &erroringDoer{err: errors.New("socket hang up")}
	_, err := plainClient(doer, 0).Get(context.Background(), "https://example.test/thing")
	assertImporterKind(t, err, KindNetwork)
	if doer.count() != 2 {
		t.Fatalf("requests = %d, want 2", doer.count())
	}
}

func TestJSONClientClassifiesAPersistent5xxAsNetwork(t *testing.T) {
	resetSharedBackoff(t)
	doer := newScriptedDoer(httpFixtureRoute{Status: 503, Body: `{"message":"boom"}`})
	_, err := plainClient(doer, 0).Get(context.Background(), "https://example.test/thing")
	assertImporterKind(t, err, KindNetwork)
	var ie *ImporterError
	_ = errors.As(err, &ie)
	if ie.Status != 503 {
		t.Errorf("status = %d", ie.Status)
	}
}

func TestJSONClientCapsPaginationAndReportsTruncation(t *testing.T) {
	resetSharedBackoff(t)
	doer := newScriptedDoer(httpFixtureRoute{
		Status: 200, Body: `[1]`,
		Headers: map[string]string{"link": `<https://example.test/items?page=99>; rel="next"`},
	})
	page, err := plainClient(doer, 5).GetAll(context.Background(), "https://example.test/items")
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if doer.count() != 5 || len(page.Items) != 5 {
		t.Fatalf("requests=%d items=%d, want 5/5", doer.count(), len(page.Items))
	}
	// A truncated walk must be distinguishable from a complete one, or the caller silently
	// publishes a repo missing its older releases.
	if !page.Truncated || page.Pages != 5 || page.MaxPages != 5 {
		t.Errorf("page = %+v", page)
	}
}

func TestJSONClientReportsACompleteWalkAsNotTruncated(t *testing.T) {
	resetSharedBackoff(t)
	doer := newScriptedDoer(httpFixtureRoute{Status: 200, Body: `[1,2]`})
	page, err := plainClient(doer, 5).GetAll(context.Background(), "https://example.test/items")
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if len(page.Items) != 2 || page.Truncated || page.Pages != 1 || page.MaxPages != 5 {
		t.Errorf("page = %+v", page)
	}
}

func TestJSONClientRejectsAPaginatedEndpointThatAnswersWithAnObject(t *testing.T) {
	resetSharedBackoff(t)
	doer := newScriptedDoer(httpFixtureRoute{Status: 200, Body: `{"items":[]}`})
	_, err := plainClient(doer, 0).GetAll(context.Background(), "https://example.test/items")
	assertImporterKind(t, err, KindBadResponse)
}

func TestJSONClientKeepsQueryStringsOutOfErrorMessages(t *testing.T) {
	resetSharedBackoff(t)
	doer := newScriptedDoer(httpFixtureRoute{Status: 404, Body: `{"message":"nope"}`})
	_, err := plainClient(doer, 0).Get(context.Background(), "https://example.test/items?private_token=leaky")
	if err == nil || strings.Contains(err.Error(), "private_token") {
		t.Fatalf("message = %v", err)
	}
}

/* ---- normalisation helpers ------------------------------------------------ */

func TestToISODate(t *testing.T) {
	explicit := map[string]string{
		"2026-08-20T10:49:50+02:00": "2026-08-20T08:49:50Z",
		"2026-08-19T21:10:48.199Z":  "2026-08-19T21:10:48Z",
		"2026-08-20T10:49:50-0600":  "2026-08-20T16:49:50Z",
		// Zoneless timestamps are UTC, not the build machine's local time: the same self-hosted
		// payload would otherwise land in the artifact shifted by whatever offset the machine uses.
		"2026-08-20T10:49:50": "2026-08-20T10:49:50Z",
		"2026-08-20 10:49:50": "2026-08-20T10:49:50Z",
		"2026-08-20":          "2026-08-20T00:00:00Z",
	}
	for in, want := range explicit {
		got := ToISODate(json.RawMessage(strconv.Quote(in)))
		if got == nil || *got != want {
			t.Errorf("ToISODate(%q) = %s, want %q", in, derefStr(got), want)
		}
	}
	for _, raw := range []string{`null`, `42`, `""`, `"   "`, `"not a date"`, ``} {
		if got := ToISODate(json.RawMessage(raw)); got != nil {
			t.Errorf("ToISODate(%s) = %q, want nil", raw, *got)
		}
	}
}

func TestAbsoluteURL(t *testing.T) {
	const base = "https://gitlab.test/group/proj"
	cases := [][3]string{
		{"/group/proj/-/releases/v1/downloads/x.bin", base, "https://gitlab.test/group/proj/-/releases/v1/downloads/x.bin"},
		{"uploads/x.bin", base, "https://gitlab.test/group/proj/uploads/x.bin"},
		{"https://cdn.test/x.bin", base, "https://cdn.test/x.bin"},
		// An unusable base must not lose the value.
		{"/x", "not a url", "/x"},
	}
	for _, tc := range cases {
		if got := AbsoluteURL(tc[0], tc[1]); got != tc[2] {
			t.Errorf("AbsoluteURL(%q, %q) = %q, want %q", tc[0], tc[1], got, tc[2])
		}
	}
}

func TestEncodeURIComponentMatchesJavaScript(t *testing.T) {
	// These land in webUrl/cloneUrl, which are artifact bytes, so the escape set has to be
	// JavaScript's rather than Go's url.PathEscape.
	cases := map[string]string{
		"a b":       "a%20b",
		"a/b":       "a%2Fb",
		"g:s":       "g%3As",
		"a+b":       "a%2Bb",
		"a@b":       "a%40b",
		"-_.!~*'()": "-_.!~*'()",
		"文档":        "%E6%96%87%E6%A1%A3",
	}
	for in, want := range cases {
		if got := encodeURIComponent(in); got != want {
			t.Errorf("encodeURIComponent(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToSpdxNormalisesProviderLicenseIds(t *testing.T) {
	wantStr(t, "mit", ToSpdx(json.RawMessage(`"mit"`)), "MIT")
	wantStr(t, "gpl-3.0", ToSpdx(json.RawMessage(`"gpl-3.0"`)), "GPL-3.0-only")
	// An unknown id passes through unchanged rather than being guessed at.
	wantStr(t, "unknown id", ToSpdx(json.RawMessage(`"Weird-1.0"`)), "Weird-1.0")
	// ...and the "there is a LICENSE but we can't tell what it is" answers let the scanner win.
	for _, raw := range []string{`"NOASSERTION"`, `"other"`, `"unknown"`, `null`, `""`} {
		if got := ToSpdx(json.RawMessage(raw)); got != nil {
			t.Errorf("ToSpdx(%s) = %q, want nil", raw, *got)
		}
	}
}

// sortStringsCodePoint sorts in code-point order, never a locale-aware compare.
func sortStringsCodePoint(xs []string) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && CompareStrings(xs[j], xs[j-1]) < 0; j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
}
