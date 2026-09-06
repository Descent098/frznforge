package ingest

import (
	"context"
	"encoding/json"

	"frznforge/internal/config"
	"frznforge/internal/model"
)

// Port of src/lib/importers/gitea.ts and src/lib/importers/forgejo.ts (REST v1).
//
// Forgejo is an API-compatible fork of Gitea, so one implementation serves both: only the
// provider label — and therefore the token env vars, the cache directory and RepoSource.Type —
// differs, and it comes from the source's own type. Talks to
// `<host>/api/v1/repos/<owner>/<repo>` and its `/releases`.
//
// Compatible does not mean identical, so every field below is feature-detected rather than
// assumed: Gitea reports `licenses: ["MIT"]` while Forgejo has no license field at all (the
// scanner sniffs the cloned LICENSE instead), Forgejo timestamps carry a `+02:00`-style offset,
// and neither provider reports a content type for release assets.

// giteaPageLimit is releases per request. Gitea/Forgejo cap `limit` at their instance page size
// (50 by default).
const giteaPageLimit = "50"

// GiteaImporter reads repo metadata and releases from a Gitea or Forgejo instance.
type GiteaImporter struct {
	source config.RepoSourceConfig
	client *JSONClient
}

// NewGiteaImporter builds an importer for a gitea or forgejo source.
func NewGiteaImporter(source config.RepoSourceConfig, ctx ImporterContext) *GiteaImporter {
	return &GiteaImporter{
		source: source,
		client: NewJSONClient(JSONClientOptions{
			Auth:      AuthToken,
			HTTP:      ctx.HTTP,
			Token:     ctx.Token,
			UserAgent: ctx.UserAgent,
			Backoff:   ctx.Backoff,
			Now:       ctx.Now,
		}),
	}
}

// Provider is derived from the source, so the same type serves both gitea and forgejo.
func (g *GiteaImporter) Provider() string { return g.source.Type }

// FetchMeta reads `GET /repos/{owner}/{repo}`.
func (g *GiteaImporter) FetchMeta(ctx context.Context) (ImportedRepoMeta, error) {
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
	// Forgejo reports no license; the scanner's LICENSE detection fills the gap.
	var license *string
	if ids := StringArray(repo["licenses"]); len(ids) > 0 {
		license = toSpdxString(nullIfEmptyString(ids[0]))
	}
	issuesURL := NullIfEmpty(asObject(repo["external_tracker"])["external_tracker_url"])
	if issuesURL == nil && isTrue(repo["has_issues"]) {
		issuesURL = strPtr(webURL + "/issues")
	}
	return ImportedRepoMeta{
		Name:          &name,
		Description:   NullIfEmpty(repo["description"]),
		Homepage:      NullIfEmpty(repo["website"]),
		Topics:        StringArray(repo["topics"]),
		License:       license,
		DefaultBranch: NullIfEmpty(repo["default_branch"]),
		WebURL:        webURL,
		CloneURL:      cloneURL,
		IssuesURL:     issuesURL,
		Template:      isTrue(repo["template"]),
		Archived:      isTrue(repo["archived"]),
	}, nil
}

// FetchReleases reads `GET /repos/{owner}/{repo}/releases`, paginated.
func (g *GiteaImporter) FetchReleases(ctx context.Context) (ImportedReleases, error) {
	page, err := g.client.GetAll(ctx, g.repoURL()+"/releases?limit="+giteaPageLimit)
	if err != nil {
		return ImportedReleases{}, err
	}
	web := g.fallbackWebURL()
	releases := []model.Release{}
	for _, item := range page.Items {
		entry := asObject(item)
		// Drafts depend on the token used to build; excluding them keeps the artifact stable.
		if isTrue(entry["draft"]) {
			continue
		}
		tag := NullIfEmpty(entry["tag_name"])
		publishedAt := firstNonNil(ToISODate(entry["published_at"]), ToISODate(entry["created_at"]))
		if tag == nil || publishedAt == nil {
			continue
		}
		name := *tag
		if v := NullIfEmpty(entry["name"]); v != nil {
			name = *v
		}
		author := asObject(entry["author"])
		assets := []model.ReleaseAsset{}
		for _, rawAsset := range asArray(entry["assets"]) {
			if asset := giteaAsset(asObject(rawAsset), web); asset != nil {
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
			Author:      firstNonNil(NullIfEmpty(author["login"]), NullIfEmpty(author["username"])),
			Assets:      SortAssets(assets),
		})
	}
	return ImportedReleases{Releases: SortReleases(releases), Truncated: page.Truncated}, nil
}

// repoURL is `<host>/api/v1/repos/<owner>/<repo>`.
func (g *GiteaImporter) repoURL() string {
	return trimTrailingSlashes(g.source.Host) + "/api/v1/repos/" +
		encodeURIComponent(g.source.Owner) + "/" + encodeURIComponent(g.source.Repo)
}

func (g *GiteaImporter) fallbackWebURL() string {
	return trimTrailingSlashes(g.source.Host) + "/" + g.source.Owner + "/" + g.source.Repo
}

// giteaAsset maps a Gitea/Forgejo asset onto an artifact asset. browser_download_url is the only
// URL these APIs give, and neither reports a MIME type; download_count is deliberately ignored,
// because it would change the artifact between two builds of the same commits.
func giteaAsset(asset map[string]json.RawMessage, base string) *model.ReleaseAsset {
	name := NullIfEmpty(asset["name"])
	url := NullIfEmpty(asset["browser_download_url"])
	if name == nil || url == nil {
		return nil
	}
	// Absolutised so a relative URL cannot render as a same-origin link on the generated site.
	return &model.ReleaseAsset{
		Name:        *name,
		URL:         AbsoluteURL(*url, base),
		Size:        ByteSize(asset["size"]),
		ContentType: nil,
	}
}
