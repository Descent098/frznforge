package build

import (
	"fmt"
	"html/template"
	"regexp"
	"strings"

	"frznforge/internal/markdown"
	"frznforge/internal/model"
	"frznforge/internal/render"
	"frznforge/internal/routes"
)

// Releases — the port of src/pages/repos/[slug]/releases/{index,[tag]}.astro and
// ReleaseCard.astro.
//
// Both origins render through the same pages: routes.ResolveReleases prefers the releases a
// provider was asked for and falls back to annotated tags, so a repo that switched releaseMode
// — or one whose remote could not be reached this build — still has a releases page built from
// whatever git knows.
//
// # Trust
//
// A provider release note is written by whoever can publish on that forge, which is not
// necessarily the site owner, so it goes through markdown.IsTrustedSource like any other
// imported repo content. tests/e2e/releases.spec.ts asserts that no <script>, event handler or
// javascript: URL survives into the page.

// providerLabels are the human names of the forges releases can be imported from.
var providerLabels = map[string]string{
	"github": "GitHub", "gitlab": "GitLab", "gitea": "Gitea", "forgejo": "Forgejo",
}

// siteReleases is the release list every page in this family renders from: ResolveReleases with
// one release per tag.
//
// A provider may report two releases against one tag, and the tag owns the URL — routes.go
// dedupes the same way when it enumerates release pages, so a second release on a tag would
// otherwise be a card linking at a page built from the first. Deduping here keeps the index,
// the release pages and the header's Releases count telling one story.
func siteReleases(repo *model.Repo) []routes.SiteRelease {
	all := routes.ResolveReleases(repo)
	out := make([]routes.SiteRelease, 0, len(all))
	seen := make(map[string]bool, len(all))
	for _, rel := range all {
		if seen[rel.Tag] {
			continue
		}
		seen[rel.Tag] = true
		out = append(out, rel)
	}
	return out
}

func emitRepoReleases(b *Builder, repo *model.Repo) error {
	head := newRepoSubHead(b, repo)
	releases := siteReleases(repo)
	// The newest release a visitor should actually install: the first that is not a
	// pre-release. -1 when every one of them is.
	latest := -1
	for i, rel := range releases {
		if !rel.Prerelease {
			latest = i
			break
		}
	}
	if err := emitReleasesIndex(b, repo, head, releases, latest); err != nil {
		return err
	}
	for i, rel := range releases {
		if err := emitRelease(b, repo, head, rel, i == latest); err != nil {
			return fmt.Errorf("release %s: %w", rel.Tag, err)
		}
	}
	return nil
}

/* ---- the index ----------------------------------------------------------- */

// releasesIndex is the releases listing.
type releasesIndex struct {
	Head  repoSubHead
	Label string
	Cards []releaseCard
	// ProviderEmpty picks the empty state. Telling the owner of a provider-backed repo to run
	// `git tag -a` would be wrong advice — their releases come from the forge.
	ProviderEmpty bool
	Name          string
}

// releaseCard is one row of the index — the port of ReleaseCard.astro.
type releaseCard struct {
	Title string
	Tag   string
	// Titled is true when the release has a name of its own; providers often give one
	// ("Sunrise"), while a tag-derived release just repeats the tag.
	Titled     bool
	URL        string
	IsLatest   bool
	Prerelease bool
	Date       string

	CommitURL   string
	CommitShort string
	Assets      int
	// Provider picks the origin icon: a package for an imported release, a tag for a derived
	// one. Origin is the sentence beside it.
	Provider   bool
	Origin     string
	Summary    string
	ArchiveURL string
}

func emitReleasesIndex(b *Builder, repo *model.Repo, head repoSubHead, releases []routes.SiteRelease, latest int) error {
	label := ""
	if repo.Source.Type != "local" {
		label = providerLabels[repo.Source.Type]
	}
	payload := releasesIndex{
		Head:          head.forPage("releases", "", false),
		Label:         label,
		Name:          repo.Name,
		ProviderEmpty: len(releases) == 0 && repo.ReleaseMode == "provider" && label != "",
	}
	for i, rel := range releases {
		card := releaseCard{
			Title:      releaseTitle(rel),
			Tag:        rel.Tag,
			Titled:     releaseTitled(rel),
			URL:        b.Router.ReleaseURL(repo.Slug, rel.Tag),
			IsLatest:   i == latest,
			Prerelease: rel.Prerelease,
			Date:       rel.Date,
			Assets:     len(rel.Assets),
			Provider:   rel.Source == "provider",
			Origin:     originText(rel.Source, label),
			Summary:    summarize(rel.Body),
		}
		if c := repo.CommitFor(rel.Commit); c != nil {
			card.CommitURL = b.Router.CommitURL(repo.Slug, rel.Commit)
			card.CommitShort = shortSha(rel.Commit)
		}
		if a := tagArchive(repo, rel.Tag); a != nil {
			card.ArchiveURL = b.Router.ArchiveURL(repo.Slug, rel.Tag)
			card.Assets++
		}
		payload.Cards = append(payload.Cards, card)
	}
	return b.WritePage(b.Router.ReleasesURL(repo.Slug), "page-releases", render.Page{
		Title:       "Releases · " + repo.Name,
		Description: deref(repo.Description),
		Active:      "repos",
		Payload:     payload,
	})
}

/* ---- one release --------------------------------------------------------- */

// releaseAsset is one downloadable file. Provider assets live off this site; the source zip
// this build produced does not, and the two are marked apart because leaving a static site has
// to be signposted.
type releaseAsset struct {
	Name        string
	URL         string
	Icon        string
	ContentType string
	Size        string
	Local       bool
}

// releasePage is one release, from either origin.
type releasePage struct {
	Head       repoSubHead
	Title      string
	Tag        string
	Titled     bool
	IsLatest   bool
	Prerelease bool
	Date       string
	BackURL    string

	Author      string
	CommitURL   string
	CommitShort string
	TreeURL     string
	Origin      string
	// OriginURL is the release's page on the provider, when it has one.
	OriginURL string
	Provider  bool

	Notes     template.HTML
	HasNotes  bool
	Assets    []releaseAsset
	HasAssets bool
}

func emitRelease(b *Builder, repo *model.Repo, head repoSubHead, rel routes.SiteRelease, isLatest bool) error {
	label := ""
	if repo.Source.Type != "local" {
		label = providerLabels[repo.Source.Type]
	}
	p := releasePage{
		Head:       head.forPage("releases", "", true),
		Title:      releaseTitle(rel),
		Tag:        rel.Tag,
		Titled:     releaseTitled(rel),
		IsLatest:   isLatest,
		Prerelease: rel.Prerelease,
		Date:       rel.Date,
		BackURL:    b.Router.ReleasesURL(repo.Slug),
		Author:     releaseAuthor(repo, rel),
		Origin:     originText(rel.Source, label),
		OriginURL:  rel.URL,
		Provider:   rel.Source == "provider",
	}
	if rel.Commit != "" {
		p.CommitShort = shortSha(rel.Commit)
		if c := repo.CommitFor(rel.Commit); c != nil {
			p.CommitURL = b.Router.CommitURL(repo.Slug, rel.Commit)
		}
	}
	if _, ok := repo.RefTrees.Get(rel.Tag); ok {
		p.TreeURL = b.Router.TreeURL(repo.Slug, rel.Tag, "")
	}
	if strings.TrimSpace(rel.Body) != "" {
		p.HasNotes = true
		p.Notes = htmlOf(b.Markdown(rel.Body, markdown.IsTrustedSource(repo.Source)))
	}
	for _, a := range rel.Assets {
		row := releaseAsset{Name: a.Name, URL: a.URL, Icon: assetIcon(a), ContentType: deref(a.ContentType)}
		if a.Size > 0 {
			row.Size = render.FormatBytes(a.Size)
		}
		p.Assets = append(p.Assets, row)
	}
	if a := tagArchive(repo, rel.Tag); a != nil {
		p.Assets = append(p.Assets, releaseAsset{
			Name: "Source code (zip)", URL: b.Router.ArchiveURL(repo.Slug, rel.Tag),
			Icon: "#i-zip", ContentType: "built from this mirror",
			Size: render.FormatBytes(a.Bytes), Local: true,
		})
	}
	p.HasAssets = len(p.Assets) > 0

	page := render.Page{
		Title:       p.Title + " · Releases · " + repo.Name,
		Description: deref(repo.Description),
		Active:      "repos",
		Payload:     p,
	}
	if p.HasNotes && markdown.ContainsMermaid(string(p.Notes)) {
		page.ExtraScripts = []string{b.Router.WithBase("/js/mermaid.js")}
	}
	return b.WritePage(b.Router.ReleaseURL(repo.Slug, rel.Tag), "page-release", page)
}

/* ---- shared bits --------------------------------------------------------- */

func releaseTitled(rel routes.SiteRelease) bool {
	return strings.TrimSpace(rel.Name) != "" && rel.Name != rel.Tag
}

func releaseTitle(rel routes.SiteRelease) string {
	if releaseTitled(rel) {
		return rel.Name
	}
	return rel.Tag
}

func originText(source, label string) string {
	if source != "provider" {
		return "source: annotated tag"
	}
	return "source: " + firstNonEmpty(label, "provider") + " release"
}

// releaseAuthor reads the publisher back from whichever origin produced the release —
// SiteRelease itself carries none.
func releaseAuthor(repo *model.Repo, rel routes.SiteRelease) string {
	if rel.Source == "provider" {
		for _, r := range repo.Releases {
			if r.Tag == rel.Tag {
				return deref(r.Author)
			}
		}
		return ""
	}
	for _, t := range repo.GitTags {
		if t.Name == rel.Tag && t.Tagger != nil {
			return t.Tagger.Name
		}
	}
	return ""
}

func tagArchive(repo *model.Repo, tag string) *model.Archive {
	for i := range repo.Archives {
		if repo.Archives[i].Kind == "tag" && repo.Archives[i].Ref == tag {
			return &repo.Archives[i]
		}
	}
	return nil
}

func shortSha(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// assetIcon guesses a kind-of-file icon from the MIME type the provider reported, falling back
// to the file name.
func assetIcon(a model.ReleaseAsset) string {
	kind := strings.ToLower(deref(a.ContentType))
	name := strings.ToLower(a.Name)
	switch {
	case archiveTypeRe.MatchString(kind) || archiveNameRe.MatchString(name):
		return "#i-zip"
	case textTypeRe.MatchString(kind) || textNameRe.MatchString(name):
		return "#i-file"
	case packageNameRe.MatchString(name) || kind == "application/octet-stream":
		return "#i-package"
	}
	return "#i-download"
}

var (
	archiveTypeRe = regexp.MustCompile(`zip|gzip|x-tar|x-xz|x-bzip|7z|compressed`)
	archiveNameRe = regexp.MustCompile(`\.(zip|tar|tgz|txz|gz|xz|bz2|7z)$`)
	textTypeRe    = regexp.MustCompile(`^text/|json|xml|yaml`)
	textNameRe    = regexp.MustCompile(`\.(txt|md|json|ya?ml|asc|sig|sha\d*|sha\d*sums?)$`)
	packageNameRe = regexp.MustCompile(`\.(exe|msi|dmg|pkg|deb|rpm|apk|appimage|whl|jar|nupkg)$`)
)

/* ---- the card summary ---------------------------------------------------- */

// summaryChars is how much of a release note a card shows.
const summaryChars = 200

// summarize reduces release-note markdown to one line of prose.
//
// Deliberately a pass over the SOURCE rather than render-then-strip: what a card wants is the
// author's text without its syntax, not the text content of rendered HTML — which would have
// lost the fence contents, kept the table pipes, and cost a markdown render per card.
func summarize(md string) string {
	text := md
	for _, r := range summaryStrips {
		text = r.re.ReplaceAllString(text, r.with)
	}
	text = stripUnderscoreEmphasis(text)
	for _, r := range summaryStripsLate {
		text = r.re.ReplaceAllString(text, r.with)
	}
	text = strings.TrimSpace(collapseSpaceRe.ReplaceAllString(text, " "))

	// Runes, not bytes: a cut in the middle of a character would be a mojibake card. This is
	// the one place the count can differ from the TypeScript's UTF-16 length, and only for
	// characters outside the basic plane.
	runes := []rune(text)
	if len(runes) <= summaryChars {
		return text
	}
	return trailingWordRe.ReplaceAllString(string(runes[:summaryChars]), "") + "…"
}

type reSub struct {
	re   *regexp.Regexp
	with string
}

var (
	summaryStrips = []reSub{
		{regexp.MustCompile("(?s)```.*?```"), " "},
		{regexp.MustCompile("(?s)~~~.*?~~~"), " "},
		{regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`), "$1"},
		{regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`), "$1"},
		{regexp.MustCompile("`([^`]*)`"), "$1"},
		{regexp.MustCompile(`(?m)^\s{0,3}#{1,6}\s+`), ""},
		{regexp.MustCompile(`(?m)^\s{0,3}>\s?`), ""},
		{regexp.MustCompile(`(?m)^\s{0,3}(?:[-*+]|\d+[.)])\s+`), ""},
		{regexp.MustCompile(`(?m)^\s{0,3}(?:[-*_]\s*){3,}$`), " "},
	}
	summaryStripsLate = []reSub{
		{regexp.MustCompile(`\*\*|__|~~|\*`), ""},
		{regexp.MustCompile(`<[^>]+>`), " "},
	}
	collapseSpaceRe = regexp.MustCompile(`\s+`)
	trailingWordRe  = regexp.MustCompile(`\s+\S*$`)
)

var (
	// The candidate for `_emphasis_`: the string start, or one non-word character, then the
	// underscored run. A lone `_` inside an identifier is left alone, which is the whole point.
	emphasisAtStartRe = regexp.MustCompile(`^_([^_]+)_`)
	emphasisRe        = regexp.MustCompile(`[^0-9A-Za-z_]_([^_]+)_`)
)

// stripUnderscoreEmphasis is the TypeScript's `/(^|\W)_([^_]+)_(?=\W|$)/g` replacement.
//
// RE2 has no lookahead, and the lookahead is load-bearing: it decides whether a run is emphasis
// without consuming the character that follows, so `_a_ _b_` loses both pairs of markers. The
// scan below reproduces that exactly — on a rejected candidate it resumes one byte past the
// candidate's start, which is where a regex engine would have tried next.
func stripUnderscoreEmphasis(s string) string {
	var b strings.Builder
	written, scan := 0, 0
	for scan < len(s) {
		var loc []int
		if scan == 0 {
			loc = emphasisAtStartRe.FindStringSubmatchIndex(s)
		}
		if loc == nil {
			m := emphasisRe.FindStringSubmatchIndex(s[scan:])
			if m == nil {
				break
			}
			loc = []int{scan + m[0], scan + m[1], scan + m[2], scan + m[3]}
		}
		start, end, inner := loc[0], loc[1], s[loc[2]:loc[3]]
		if end < len(s) && isWordByte(s[end]) {
			scan = start + 1
			continue
		}
		b.WriteString(s[written:start])
		// Everything up to the opening underscore is the delimiter the candidate consumed —
		// empty at the string start, one character otherwise. The replacement keeps it.
		b.WriteString(s[start : loc[2]-1])
		b.WriteString(inner)
		written, scan = end, end
	}
	b.WriteString(s[written:])
	return b.String()
}

func isWordByte(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}
