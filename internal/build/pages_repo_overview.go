package build

import (
	"html/template"
	"net/url"
	"sort"
	"strings"

	"frznforge/internal/markdown"
	"frznforge/internal/model"
	"frznforge/internal/render"
	"frznforge/internal/routes"
)

// The repository overview — /repos/<slug>/ — plus the header every repo page wears.
//
// Port of src/pages/repos/[slug]/index.astro and src/components/RepoHeader.astro. The header
// builder lives here because the overview is the page it was written for; the other repo
// families call the same repoHead below so that the tab strip and the pages it links to are
// derived once. A tab computed twice is a tab that eventually points at a page nobody emitted.

/* ---- the shared repo header ---------------------------------------------- */

// repoTabLabels name the tab each repo sub-page sits on, for the visually-hidden <h1> the
// header draws. The overview has no entry: its heading is the repo name on its own.
var repoTabLabels = map[string]string{
	"commits":  "Commits",
	"branches": "Branches",
	"tags":     "Tags",
	"releases": "Releases",
	"insights": "Insights",
}

// repoHead builds the dict repo-header.gohtml renders.
//
// Four of its values are things a template cannot work out for itself — the site owner's
// handle, the upstream's hostname, how many releases there are and whether an insights page
// exists — and the last two decide what the tab strip shows. Every repo family goes through
// here so the tabs and the emitted pages cannot disagree: a tab without a page is a dead link,
// a page without a tab is unreachable.
//
// Callers adjust the two optional keys in place: a page that draws its own visible <h1> sets
// ["OwnHeading"] = true, and one with a better heading than "<repo> · <section>" (a blob page
// names the path being browsed) overwrites ["Heading"].
func repoHead(b *Builder, repo *model.Repo, active string) map[string]any {
	heading := repo.Name
	if label, ok := repoTabLabels[active]; ok {
		heading = repo.Name + " · " + label
	}
	return map[string]any{
		"Repo":         repo,
		"Active":       active,
		"Heading":      heading,
		"OwnHeading":   false,
		"Handle":       b.Cfg.Owner.Handle,
		"UpstreamHost": upstreamHost(repo),
		"Releases":     len(routes.ResolveReleases(repo)),
		"ShowInsights": routes.HasInsights(repo),
	}
}

// upstreamHost is the bare hostname behind "Open on github.com".
//
// Empty when there is no upstream or when it will not parse: the button then reads "Open on",
// which is untidy, but a repo whose configured upstream is malformed must not take the build
// down with it.
func upstreamHost(repo *model.Repo) string {
	raw := deref(repo.Links.Upstream)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
}

/* ---- the overview page --------------------------------------------------- */

// repoOverview is what page-repo.gohtml renders.
type repoOverview struct {
	Head map[string]any
	Repo *model.Repo

	// The ref switcher, split the way its popup groups refs. CurrentRef is nil for a repo with
	// no default branch, and the switcher is then not drawn at all.
	CurrentRef  *routes.BrowsableRef
	RefBranches []routes.BrowsableRef
	RefTags     []routes.BrowsableRef

	// The clone popup. CloneURL is empty when there is no upstream to clone from — this site
	// runs no git server, so there would be nothing to put in the box.
	CloneURL       string
	UpstreamHost   string
	DefaultArchive *model.Archive

	HeadCommit *model.Commit
	// Files is the default branch's root directory, already sorted and linked; empty for a repo
	// whose default branch has no tree, which is the case the "no files" card covers.
	Files []repoFileRow

	Readme     *model.Readme
	ReadmeHTML template.HTML
	// ReadmeText is a README that is not markdown; the template puts it in a <pre> and lets
	// html/template escape it.
	ReadmeText string

	Links     []repoAboutLink
	LatestTag *model.Tag
}

// repoFileRow is one row of the root file table.
type repoFileRow struct {
	Entry model.TreeEntry
	// Href is empty when the entry has no page here; Why then says which of the two reasons
	// applies, because the row still lists and has to explain itself.
	Href   string
	Why    string // "submodule" | "unservable"
	Commit *model.Commit
}

// repoAboutLink is one line of the About panel's link list.
type repoAboutLink struct {
	Label string
	Icon  string
	URL   string
	// Text overrides the displayed link text. Hosted sites are internal URLs, so prettyURL —
	// which strips a protocol they do not have — is the wrong rendering for them.
	Text string
}

// emitRepoOverview writes /repos/<slug>/.
func emitRepoOverview(b *Builder, repo *model.Repo) error {
	description := repo.Name + " — " + b.Cfg.Owner.Name
	if repo.Description != nil {
		description = *repo.Description
	}
	page := render.Page{
		Title:       repo.Name,
		Description: description,
		Active:      "repos",
	}

	payload := &repoOverview{
		Head:         repoHead(b, repo, "code"),
		Repo:         repo,
		UpstreamHost: upstreamHost(repo),
		Links:        aboutLinks(b, repo),
	}

	if !repo.Empty {
		fillOverviewBody(b, repo, payload, &page)
	}

	// The clone button is a `button[data-copy]`, so the page needs the copy handler. Emitted
	// even for a repo with no clone URL, matching the Astro page: the component was included
	// unconditionally there, and dropping it here would be a difference for no gain.
	page.ExtraScripts = append(page.ExtraScripts, b.Router.WithBase("/js/copy.js"))

	page.Payload = payload
	return b.WritePage(b.Router.RepoURL(repo.Slug), "page-repo", page)
}

// fillOverviewBody derives everything below the header for a repo that has commits.
func fillOverviewBody(b *Builder, repo *model.Repo, p *repoOverview, page *render.Page) {
	defaultBranch := deref(repo.DefaultBranch)

	for _, ref := range routes.BrowsableRefs(repo) {
		if ref.IsDefault {
			p.CurrentRef = &ref
		}
		if ref.Kind == "tag" {
			p.RefTags = append(p.RefTags, ref)
		} else {
			p.RefBranches = append(p.RefBranches, ref)
		}
	}

	if defaultBranch != "" {
		for _, br := range repo.Branches {
			if br.Name == defaultBranch {
				p.HeadCommit = repo.CommitFor(br.Head)
				break
			}
		}
		for i := range repo.Archives {
			if repo.Archives[i].Kind == "branch" && repo.Archives[i].Ref == defaultBranch {
				p.DefaultArchive = &repo.Archives[i]
				break
			}
		}
	}

	p.CloneURL = cloneURL(deref(repo.Links.Upstream))
	p.LatestTag = latestTag(repo)

	// Root-level entries only: the overview shows one directory, and the rest of the tree is
	// the file browser's job.
	if defaultBranch != "" {
		var root []model.TreeEntry
		for _, e := range repo.Tree {
			if !strings.Contains(e.Path, "/") {
				root = append(root, e)
			}
		}
		p.Files = fileRows(b, repo, defaultBranch, root)
	}

	if repo.Readme != nil {
		p.Readme = repo.Readme
		if markdown.IsMarkdownPath(repo.Readme.Path) {
			html := b.Markdown(repo.Readme.Content, markdown.IsTrustedSource(repo.Source))
			p.ReadmeHTML = htmlOf(html)
			if markdown.ContainsMermaid(html) {
				page.ExtraScripts = append(page.ExtraScripts, b.Router.WithBase("/js/mermaid.js"))
			}
		} else {
			p.ReadmeText = repo.Readme.Content
		}
	}
}

// fileRows sorts one directory's entries the way the file table lists them — directories
// first, then by name — and resolves each row's link and last commit.
//
// The name comparison is case-insensitive and then falls back to the artifact's own order
// (which is by path), rather than the locale-aware compare the TypeScript used: a collation
// that depends on the build machine's ICU data would emit different HTML on two machines from
// the same artifact.
func fileRows(b *Builder, repo *model.Repo, refName string, entries []model.TreeEntry) []repoFileRow {
	sorted := append([]model.TreeEntry(nil), entries...)
	sort.SliceStable(sorted, func(i, j int) bool {
		di, dj := sorted[i].Type == "tree", sorted[j].Type == "tree"
		if di != dj {
			return di
		}
		return strings.ToLower(sorted[i].Name) < strings.ToLower(sorted[j].Name)
	})

	rows := make([]repoFileRow, 0, len(sorted))
	for _, e := range sorted {
		row := repoFileRow{Entry: e, Commit: repo.CommitFor(e.LastCommit)}
		switch {
		case e.Type == "commit":
			// A submodule is a pointer into another repository; there is no page for it here.
			row.Why = "submodule"
		case !routes.IsRawServable(e.Path):
			// '#' or '%' in the path — no static URL round-trips it. Ingest already warned
			// (repo-path-unservable); the row still lists so the tree stays an honest view of
			// the commit.
			row.Why = "unservable"
		case e.Type == "tree":
			row.Href = b.Router.TreeURL(repo.Slug, refName, e.Path)
		default:
			row.Href = b.Router.BlobURL(repo.Slug, refName, e.Path)
		}
		rows = append(rows, row)
	}
	return rows
}

// aboutLinks are the About panel's outbound links, in the order the panel lists them.
func aboutLinks(b *Builder, repo *model.Repo) []repoAboutLink {
	var links []repoAboutLink
	// Sites this repo is published as. The hosting binding lives on the artifact root rather
	// than on the repo, so it is looked up here rather than read off the repo.
	for _, site := range routes.HostedSitesFor(b.Data, repo.Slug) {
		links = append(links, repoAboutLink{
			Label: "Hosted site", Icon: "#i-globe",
			URL: b.Router.HostedURL(site.Slug), Text: "/" + site.Slug + "/",
		})
	}
	if u := deref(repo.Links.Homepage); u != "" {
		links = append(links, repoAboutLink{Label: "Homepage", Icon: "#i-globe", URL: u})
	}
	if u := deref(repo.Links.Issues); u != "" {
		links = append(links, repoAboutLink{Label: "Issue tracker", Icon: "#i-bolt", URL: u})
	}
	if u := deref(repo.Links.Donations); u != "" {
		links = append(links, repoAboutLink{Label: "Donations", Icon: "#i-heart", URL: u})
	}
	if u := deref(repo.Links.Upstream); u != "" {
		icon := "#i-forge"
		if strings.Contains(u, "github.com") {
			icon = "#i-github"
		}
		links = append(links, repoAboutLink{Label: "Upstream", Icon: icon, URL: u})
	}
	return links
}

// cloneURL turns an upstream link into something `git clone` accepts, or returns "" when
// there is no upstream: this site is a mirror and runs no git server of its own, so the only
// honest clone URL is somebody else's.
func cloneURL(upstream string) string {
	if upstream == "" {
		return ""
	}
	trimmed := strings.TrimRight(upstream, "/")
	if strings.HasSuffix(upstream, ".git") {
		return trimmed
	}
	return trimmed + ".git"
}

// latestTag is the most recently dated git tag, or nil. Ties keep artifact order, which is by
// name — the same stable choice the tags page makes.
func latestTag(repo *model.Repo) *model.Tag {
	if len(repo.GitTags) == 0 {
		return nil
	}
	sorted := append([]model.Tag(nil), repo.GitTags...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Date > sorted[j].Date })
	return &sorted[0]
}
