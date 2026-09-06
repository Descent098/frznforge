package ingest

import (
	"context"
	"encoding/json"
	"strings"

	"frznforge/internal/config"
	"frznforge/internal/model"
)

// Port of src/lib/importers/github.ts (REST v3). Talks to `<host>/repos/<owner>/<repo>` and
// `<host>/repos/<owner>/<repo>/releases`; host defaults to https://api.github.com and is the
// `/api/v3` base on GitHub Enterprise.

// githubPerPage is releases per request — GitHub's maximum, so most repos need a single round
// trip.
const githubPerPage = "100"

// GithubImporter reads repo metadata and releases from the GitHub REST API.
type GithubImporter struct {
	source config.RepoSourceConfig
	client *JSONClient
}

// NewGithubImporter builds a GitHub importer for one configured source.
func NewGithubImporter(source config.RepoSourceConfig, ctx ImporterContext) *GithubImporter {
	return &GithubImporter{
		source: source,
		client: NewJSONClient(JSONClientOptions{
			Auth:      AuthBearer,
			HTTP:      ctx.HTTP,
			Token:     ctx.Token,
			UserAgent: ctx.UserAgent,
			Accept:    "application/vnd.github+json",
			Backoff:   ctx.Backoff,
			Now:       ctx.Now,
		}),
	}
}

func (g *GithubImporter) Provider() string { return "github" }

// FetchMeta reads `GET /repos/{owner}/{repo}`.
func (g *GithubImporter) FetchMeta(ctx context.Context) (ImportedRepoMeta, error) {
	raw, err := g.client.Get(ctx, g.repoURL())
	if err != nil {
		return ImportedRepoMeta{}, err
	}
	repo := asObject(raw)

	webURL := g.fallbackWebURL()
	if v := NullIfEmpty(repo["html_url"]); v != nil {
		webURL = *v
	}
	cloneURL := webURL + ".git"
	if v := NullIfEmpty(repo["clone_url"]); v != nil {
		cloneURL = *v
	}
	name := g.source.Repo
	if v := NullIfEmpty(repo["name"]); v != nil {
		name = *v
	}
	var issuesURL *string
	if isTrue(repo["has_issues"]) {
		issuesURL = strPtr(webURL + "/issues")
	}
	return ImportedRepoMeta{
		Name:          &name,
		Description:   NullIfEmpty(repo["description"]),
		Homepage:      NullIfEmpty(repo["homepage"]),
		Topics:        StringArray(repo["topics"]),
		License:       ToSpdx(asObject(repo["license"])["spdx_id"]),
		DefaultBranch: NullIfEmpty(repo["default_branch"]),
		WebURL:        webURL,
		CloneURL:      cloneURL,
		IssuesURL:     issuesURL,
		Template:      isTrue(repo["is_template"]),
		Archived:      isTrue(repo["archived"]),
	}, nil
}

// FetchReleases reads `GET /repos/{owner}/{repo}/releases`, paginated.
func (g *GithubImporter) FetchReleases(ctx context.Context) (ImportedReleases, error) {
	page, err := g.client.GetAll(ctx, g.repoURL()+"/releases?per_page="+githubPerPage)
	if err != nil {
		return ImportedReleases{}, err
	}
	web := g.fallbackWebURL()
	releases := []model.Release{}
	for _, item := range page.Items {
		entry := asObject(item)
		// Drafts are invisible to anonymous visitors and would appear and disappear with the
		// token used to build, which is exactly the kind of non-determinism we avoid.
		if isTrue(entry["draft"]) {
			continue
		}
		tag := NullIfEmpty(entry["tag_name"])
		publishedAt := firstNonNil(ToISODate(entry["published_at"]), ToISODate(entry["created_at"]))
		// Without a tag or a usable date there is nothing the site could route to or sort by.
		if tag == nil || publishedAt == nil {
			continue
		}
		name := *tag
		if v := NullIfEmpty(entry["name"]); v != nil {
			name = *v
		}
		assets := []model.ReleaseAsset{}
		for _, rawAsset := range asArray(entry["assets"]) {
			if asset := githubAsset(asObject(rawAsset), web); asset != nil {
				assets = append(assets, *asset)
			}
		}
		releases = append(releases, model.Release{
			Tag:         *tag,
			Name:        name,
			Body:        rawStringOr(entry["body"], ""),
			URL:         mapURL(NullIfEmpty(entry["html_url"]), web),
			Prerelease:  isTrue(entry["prerelease"]),
			PublishedAt: *publishedAt,
			Author:      NullIfEmpty(asObject(entry["author"])["login"]),
			Assets:      SortAssets(assets),
		})
	}
	return ImportedReleases{Releases: SortReleases(releases), Truncated: page.Truncated}, nil
}

// repoURL is `<host>/repos/<owner>/<repo>`, host normalised so a trailing slash cannot double up.
func (g *GithubImporter) repoURL() string {
	base := trimTrailingSlashes(g.source.Host)
	return base + "/repos/" + encodeURIComponent(g.source.Owner) + "/" + encodeURIComponent(g.source.Repo)
}

// fallbackWebURL is used only when the API omits html_url; api.github.com maps to github.com.
func (g *GithubImporter) fallbackWebURL() string {
	base := trimTrailingSlashes(g.source.Host)
	if base == "https://api.github.com" {
		base = "https://github.com"
	}
	base = strings.TrimSuffix(base, "/api/v3")
	return base + "/" + g.source.Owner + "/" + g.source.Repo
}

// githubAsset maps a GitHub asset onto an artifact asset. browser_download_url is the one a
// visitor can actually use; download_count is deliberately ignored, because it would change the
// artifact between two builds of the same commits.
func githubAsset(asset map[string]json.RawMessage, base string) *model.ReleaseAsset {
	name := NullIfEmpty(asset["name"])
	url := firstNonNil(NullIfEmpty(asset["browser_download_url"]), NullIfEmpty(asset["url"]))
	if name == nil || url == nil {
		return nil
	}
	// Absolutised so a relative URL cannot render as a same-origin link on the generated site.
	return &model.ReleaseAsset{
		Name:        *name,
		URL:         AbsoluteURL(*url, base),
		Size:        ByteSize(asset["size"]),
		ContentType: NullIfEmpty(asset["content_type"]),
	}
}

// mapURL makes a provider URL absolute against the repo page, or keeps nil when there was none.
func mapURL(value *string, base string) *string {
	if value == nil {
		return nil
	}
	return strPtr(AbsoluteURL(*value, base))
}

// rawStringOr is `value ?? fallback` for a JSON field the schema types as a plain string.
func rawStringOr(raw json.RawMessage, fallback string) string {
	if s, ok := rawString(raw); ok {
		return s
	}
	return fallback
}

// trimTrailingSlashes is the `/\/+$/` strip every provider applies to its configured host.
func trimTrailingSlashes(s string) string { return strings.TrimRight(s, "/") }

// encodeURIComponent escapes exactly what JavaScript's encodeURIComponent escapes: everything
// outside A-Za-z0-9-_.!~*'(), byte by byte over the UTF-8 encoding, with uppercase hex.
//
// Go's url.PathEscape is not the same function — it leaves $&+,:;=@ alone — and these values
// end up in webUrl/cloneUrl, which are artifact bytes.
func encodeURIComponent(s string) string {
	const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.!~*'()"
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if strings.IndexByte(unreserved, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0f])
	}
	return b.String()
}
