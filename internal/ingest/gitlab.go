package ingest

import (
	"context"
	"encoding/json"
	"strings"

	"frznforge/internal/config"
	"frznforge/internal/model"
)

// Port of src/lib/importers/gitlab.ts (REST v4). Talks to
// `<host>/api/v4/projects/<url-encoded project path>` and its `/releases`; host defaults to
// https://gitlab.com.
//
// GitLab is the odd one out and needs the most translation:
//   - the project payload names things differently (http_url_to_repo, web_url, tag_list), and
//     an unauthenticated response simply OMITS fields like `archived` — missing means unknown,
//     never false;
//   - `license` is returned only with ?license=true;
//   - releases have no id, no draft and no prerelease. GitLab's `upcoming_release` is NOT a
//     substitute: the API computes it from `released_at > now`, so it flips on its own once a
//     scheduled release date passes and would change forge.json with no change to the repo.
//     Imported GitLab releases therefore always report Prerelease false.
//   - the notes live in `description`, and assets split into assets.links[] and the four
//     auto-generated assets.sources[] — NEITHER carries a size or a content type, so imported
//     GitLab assets report Size 0. Asset links may also be host-relative, so every URL is
//     resolved against the project's web URL before it reaches the artifact.

// gitlabPerPage is GitLab's maximum page size.
const gitlabPerPage = "100"

// GitlabImporter reads project metadata and releases from the GitLab REST API.
type GitlabImporter struct {
	source config.RepoSourceConfig
	client *JSONClient
}

// NewGitlabImporter builds a GitLab importer for one configured source.
func NewGitlabImporter(source config.RepoSourceConfig, ctx ImporterContext) *GitlabImporter {
	return &GitlabImporter{
		source: source,
		client: NewJSONClient(JSONClientOptions{
			Auth:      AuthPrivateToken,
			HTTP:      ctx.HTTP,
			Token:     ctx.Token,
			UserAgent: ctx.UserAgent,
			Backoff:   ctx.Backoff,
			Now:       ctx.Now,
		}),
	}
}

func (g *GitlabImporter) Provider() string { return "gitlab" }

// FetchMeta reads `GET /projects/:id?license=true`.
func (g *GitlabImporter) FetchMeta(ctx context.Context) (ImportedRepoMeta, error) {
	raw, err := g.client.Get(ctx, g.projectURL()+"?license=true")
	if err != nil {
		return ImportedRepoMeta{}, err
	}
	project := asObject(raw)

	webURL := g.fallbackWebURL()
	if v := NullIfEmpty(project["web_url"]); v != nil {
		webURL = *v
	}
	cloneURL := webURL + ".git"
	if v := NullIfEmpty(project["http_url_to_repo"]); v != nil {
		cloneURL = *v
	}
	topics := StringArray(project["topics"])
	if len(topics) == 0 {
		topics = StringArray(project["tag_list"])
	}
	name := firstNonNil(
		NullIfEmpty(project["path"]),
		NullIfEmpty(project["name"]),
		strPtr(g.projectName()),
	)
	license := asObject(project["license"])
	// An unauthenticated payload omits issues_enabled; only an explicit false hides the link.
	var issuesURL *string
	if !isFalse(project["issues_enabled"]) {
		issuesURL = strPtr(webURL + "/-/issues")
	}
	return ImportedRepoMeta{
		Name:          name,
		Description:   NullIfEmpty(project["description"]),
		Homepage:      NullIfEmpty(project["homepage"]),
		Topics:        topics,
		License:       firstNonNil(ToSpdx(license["key"]), NullIfEmpty(license["nickname"])),
		DefaultBranch: NullIfEmpty(project["default_branch"]),
		WebURL:        webURL,
		CloneURL:      cloneURL,
		IssuesURL:     issuesURL,
		// GitLab projects have no "template" flag in this payload.
		Template: false,
		Archived: isTrue(project["archived"]),
	}, nil
}

// FetchReleases reads `GET /projects/:id/releases`, paginated.
func (g *GitlabImporter) FetchReleases(ctx context.Context) (ImportedReleases, error) {
	page, err := g.client.GetAll(ctx, g.projectURL()+"/releases?per_page="+gitlabPerPage)
	if err != nil {
		return ImportedReleases{}, err
	}
	web := g.fallbackWebURL()
	releases := []model.Release{}
	for _, item := range page.Items {
		entry := asObject(item)
		tag := NullIfEmpty(entry["tag_name"])
		// released_at is the display date and can differ from created_at.
		publishedAt := firstNonNil(ToISODate(entry["released_at"]), ToISODate(entry["created_at"]))
		if tag == nil || publishedAt == nil {
			continue
		}
		name := *tag
		if v := NullIfEmpty(entry["name"]); v != nil {
			name = *v
		}
		self := firstNonNil(
			NullIfEmpty(asObject(entry["_links"])["self"]),
			strPtr(g.releaseURL(*tag)),
		)
		author := asObject(entry["author"])
		releases = append(releases, model.Release{
			Tag:  *tag,
			Name: name,
			Body: rawStringOr(entry["description"], ""),
			URL:  strPtr(AbsoluteURL(*self, web)),
			// Deliberately not upcoming_release: GitLab derives that from the wall clock
			// (released_at > now), so importing it would make the artifact change by itself.
			Prerelease:  false,
			PublishedAt: *publishedAt,
			Author:      firstNonNil(NullIfEmpty(author["username"]), NullIfEmpty(author["name"])),
			Assets:      SortAssets(gitlabAssets(entry, web)),
		})
	}
	return ImportedReleases{Releases: SortReleases(releases), Truncated: page.Truncated}, nil
}

// gitlabAssets are the uploaded links first-class, plus the four archives GitLab generates for
// every tag. Neither carries a size or a content type, so both report Size 0 / ContentType nil.
func gitlabAssets(entry map[string]json.RawMessage, web string) []model.ReleaseAsset {
	assets := []model.ReleaseAsset{}
	blocks := asObject(entry["assets"])
	for _, raw := range asArray(blocks["links"]) {
		link := asObject(raw)
		name := NullIfEmpty(link["name"])
		url := firstNonNil(NullIfEmpty(link["direct_asset_url"]), NullIfEmpty(link["url"]))
		if name == nil || url == nil {
			continue
		}
		// direct_asset_url is routinely host-relative; left as-is it would render as a link into
		// the generated site. Size and content type are simply not available here.
		assets = append(assets, model.ReleaseAsset{
			Name: *name, URL: AbsoluteURL(*url, web), Size: 0, ContentType: nil,
		})
	}
	for _, raw := range asArray(blocks["sources"]) {
		source := asObject(raw)
		format := NullIfEmpty(source["format"])
		url := NullIfEmpty(source["url"])
		if format == nil || url == nil {
			continue
		}
		assets = append(assets, model.ReleaseAsset{
			Name: "Source code (" + *format + ")", URL: AbsoluteURL(*url, web), Size: 0, ContentType: nil,
		})
	}
	return assets
}

// projectName is the last segment of the configured namespaced path — the project's own name.
func (g *GitlabImporter) projectName() string {
	segments := nonEmptySegments(g.source.Project)
	if len(segments) == 0 {
		return g.source.Project
	}
	return segments[len(segments)-1]
}

// projectURL is `<host>/api/v4/projects/<url-encoded path with namespace>`.
func (g *GitlabImporter) projectURL() string {
	return trimTrailingSlashes(g.source.Host) + "/api/v4/projects/" + encodeURIComponent(g.source.Project)
}

func (g *GitlabImporter) releaseURL(tag string) string {
	return g.fallbackWebURL() + "/-/releases/" + encodeURIComponent(tag)
}

// fallbackWebURL is derived from the config, for the rare payload that omits web_url.
func (g *GitlabImporter) fallbackWebURL() string {
	return trimTrailingSlashes(g.source.Host) + "/" + strings.TrimLeft(g.source.Project, "/")
}

// nonEmptySegments is `value.split('/').filter(Boolean)`.
func nonEmptySegments(value string) []string {
	out := make([]string, 0, 4)
	for _, seg := range strings.Split(value, "/") {
		if seg != "" {
			out = append(out, seg)
		}
	}
	return out
}
