package build

import (
	"fmt"
	"html/template"
	"math"
	"os"
	"regexp"
	"sort"
	"time"

	"frznforge/internal/frontmatter"
	"frznforge/internal/markdown"
	"frznforge/internal/model"
	"frznforge/internal/render"
	"frznforge/internal/routes"
)

// The organizations family: /orgs/, /orgs/<slug>/ and /orgs/<slug>/repos/.
// Port of src/pages/orgs/index.astro, orgs/[slug]/index.astro, orgs/[slug]/repos/index.astro
// and src/components/OrgHeader.astro.
//
// An organization is two halves that must both be optional. Membership, name and avatar come
// from the ARTIFACT; prose, links, pinned repos and a nicer description come from
// content/orgs/<slug>.md, which may not exist. Every branch here therefore has to render a
// complete page from the artifact alone — an org with no markdown file still gets a header, a
// KPI band and a repo grid.
//
// The overview deliberately mirrors the owner profile page (same hf-hero shell, same KPI grid)
// so an organization reads as "a profile for a group of repos" rather than as another kind of
// page.

// orgHexColor is the only shape a language colour may have before it is allowed into a style
// attribute. Everything else falls back to the neutral token — see orgLangColor.
var orgHexColor = regexp.MustCompile(`^#[0-9A-Fa-f]{3,8}$`)

/* ---- payloads ------------------------------------------------------------ */

// orgLang is one language of an aggregate, ready for a dot, a bar or a legend row.
type orgLang struct {
	Name    string
	Bytes   int64
	Percent float64
	Color   template.CSS
}

// orgLink is one labelled link from the markdown file's `links` mapping.
type orgLink struct {
	Label string
	URL   string
}

// orgCard is one tile on /orgs/.
type orgCard struct {
	Org      model.Organization
	URL      string
	ReposURL string
	Count    int64
	// Languages are the org's top three, with the "Other" bucket dropped: a card has room for
	// three dots and "Other 4%" is not one of the three worth spending it on.
	Languages   []orgLang
	UpdatedAt   string
	Heat        string
	Description string
	AvatarSrc   string
	Base        string
}

type orgsIndexPayload struct {
	Cards []orgCard
	// Unmatched are content/orgs/<id>.md files no configured organization claims. Surfaced on
	// the page, not just warned about: the file is only ever reached through an organization's
	// slug, so one typo in the filename silently discards prose, links and pins alike.
	Unmatched []string
}

// orgHeader is the hero band: everything OrgHeader.astro derived, already computed.
type orgHeader struct {
	Org         model.Organization
	Description string
	AvatarSrc   string
	Base        string

	RepoCount int64
	// HotWindow is the phrase for theme.heat.hot, so the KPI copy and the orange recency
	// accent always describe the same window.
	HotWindow     string
	HotDays       int
	TouchedRecent int
	Templates     int
	CommitsYear   int64
	CommitsRecent int64
	FirstCommit   string
	Years         int
	Languages     []orgLang

	Sites []string
	Links []orgLink
}

type orgPayload struct {
	Header orgHeader
	// Body is the rendered content/orgs/<slug>.md, empty when the file does not exist.
	Body template.HTML
	// Heading is "Pinned" when the markdown file pinned member repos, else "Repositories".
	Heading   string
	ReposURL  string
	RepoCount int
	Shown     []render.RepoSummary
	// TagHref is where a card's tag chips point. The overview is not a scoped listing, so they
	// go to the site-wide one, exactly as the Astro page did.
	TagHref string
}

type orgReposPayload struct {
	Org    model.Organization
	OrgURL string
	Count  int
	// Listing is the shared island, scoped to this org. Nil when the org has no members, which
	// is the one case the page renders its own empty state instead.
	Listing *RepoListingData
}

/* ---- emitters ------------------------------------------------------------ */

// emitOrgs writes the index, then each organization's overview and scoped listing.
func emitOrgs(b *Builder) error {
	if err := emitOrgsIndex(b); err != nil {
		return err
	}
	for _, org := range b.Data.Organizations {
		repos := routes.ReposInOrg(b.Data, org)
		if err := emitOrg(b, org, repos); err != nil {
			return fmt.Errorf("org %s: %w", org.Slug, err)
		}
		if err := emitOrgRepos(b, org, repos); err != nil {
			return fmt.Errorf("org %s listing: %w", org.Slug, err)
		}
	}
	return nil
}

// emitOrgsIndex writes /orgs/.
//
// Always built, even with no organizations — routes.OrgRoutes lists it unconditionally and the
// sync test asserts the build against that list. Only the sidebar entry hides at count 0.
func emitOrgsIndex(b *Builder) error {
	cards := make([]orgCard, 0, len(b.Data.Organizations))
	for _, org := range b.Data.Organizations {
		repos := routes.ReposInOrg(b.Data, org)
		// Newest member wins: an org is "hot" when anything inside it moved recently.
		updated := ""
		for _, r := range repos {
			if d := deref(r.UpdatedAt); d > updated {
				updated = d
			}
		}
		content := b.Site.Orgs[org.Slug]
		cards = append(cards, orgCard{
			Org:       org,
			URL:       b.Router.OrgURL(org.Slug),
			ReposURL:  b.Router.OrgReposURL(org.Slug),
			Count:     int64(len(repos)),
			Languages: orgDropOther(orgAggregateLanguages(repos, 3)),
			UpdatedAt: updated,
			Heat:      render.HeatFor(updated, b.Site.Now, b.Cfg.Theme.Heat),
			// Frontmatter wins over the config description: the markdown file is the half an
			// author edits most often, and the only one of the two that can be absent.
			Description: firstNonEmpty(orgFrontString(content, "description"), deref(org.Description)),
			AvatarSrc:   deref(org.Avatar),
			Base:        b.Router.Base,
		})
	}
	page := render.Page{
		Title:       "Organizations",
		Description: fmt.Sprintf("%d organizations", len(cards)),
		Active:      "orgs",
		Payload:     orgsIndexPayload{Cards: cards, Unmatched: b.Site.UnmatchedOrgContent},
		ExtraStyles: []string{b.Router.WithBase("/css/orgs.css")},
	}
	return b.WritePage(b.Router.OrgsIndexURL(), "page-orgs", page)
}

// emitOrg writes /orgs/<slug>/ — the hero, the markdown body, and a pinned or member grid.
func emitOrg(b *Builder, org model.Organization, repos []*model.Repo) error {
	content := b.Site.Orgs[org.Slug]
	description := firstNonEmpty(orgFrontString(content, "description"), deref(org.Description))

	// Pinned repos must be MEMBERS: pinning a repo that belongs to another org would make the
	// page lie about the group. Unknown or non-member slugs are dropped with a warning, the
	// same treatment profile.md's `pinned` gets.
	bySlug := make(map[string]*model.Repo, len(repos))
	for _, r := range repos {
		bySlug[r.Slug] = r
	}
	var pinned []render.RepoSummary
	for _, slug := range orgFrontList(content, "pinned") {
		repo, ok := bySlug[slug]
		if !ok {
			fmt.Fprintf(os.Stderr, "[frznforge] content/orgs/%s.md pins %q, which is not a member of this organization\n", org.Slug, slug)
			continue
		}
		pinned = append(pinned, render.Summarize(repo))
	}

	shown := pinned
	heading := "Pinned"
	if len(pinned) == 0 {
		heading = "Repositories"
		for i, repo := range repos {
			if i == 6 {
				break
			}
			shown = append(shown, render.Summarize(repo))
		}
	}

	body := content.Body
	var scripts []string
	if markdown.ContainsMermaid(string(body)) {
		scripts = append(scripts, b.Router.WithBase("/js/mermaid.js"))
	}
	page := render.Page{
		Title:       org.Name,
		Description: firstNonEmpty(description, org.Name+" — organization"),
		Active:      "orgs",
		Payload: orgPayload{
			Header:    buildOrgHeader(b, org, repos, content, description),
			Body:      body,
			Heading:   heading,
			ReposURL:  b.Router.OrgReposURL(org.Slug),
			RepoCount: len(repos),
			Shown:     shown,
			TagHref:   b.Router.ReposURL(),
		},
		ExtraStyles:  []string{b.Router.WithBase("/css/orgs.css")},
		ExtraScripts: scripts,
	}
	return b.WritePage(b.Router.OrgURL(org.Slug), "page-org", page)
}

// emitOrgRepos writes /orgs/<slug>/repos/ — the site-wide repo listing, scoped to one org.
//
// The island comes from RepoListing (pages_repos_listing.go) rather than being rebuilt here:
// the same component, the same client-side filter/sort/search, only the repo set differs.
// basePath is what keeps a tag chip and the no-JS pager inside the organization — a chip
// hardcoded to the site-wide listing silently drops a visitor out of the org they were
// browsing, which is what tests/e2e/orgs.spec.ts checks.
//
// An org with no members skips the island entirely: its generic "add some repos to
// frznforge.config.ts" empty state would be the wrong advice here, where the fix is
// membership, not ingestion.
func emitOrgRepos(b *Builder, org model.Organization, repos []*model.Repo) error {
	summaries := make([]render.RepoSummary, 0, len(repos))
	for _, repo := range repos {
		summaries = append(summaries, render.Summarize(repo))
	}
	basePath := b.Router.OrgReposURL(org.Slug)

	var listing *RepoListingData
	if len(summaries) > 0 {
		var err error
		listing, err = RepoListing(b, summaries, basePath)
		if err != nil {
			return err
		}
	}
	page := render.Page{
		Title:       org.Name + " repositories",
		Description: fmt.Sprintf("All %d repositories in %s", len(summaries), org.Name),
		Active:      "orgs",
		Payload: orgReposPayload{
			Org:     org,
			OrgURL:  b.Router.OrgURL(org.Slug),
			Count:   len(summaries),
			Listing: listing,
		},
		ExtraStyles: []string{b.Router.WithBase("/css/orgs.css")},
	}
	return b.WritePage(basePath, "page-org-repos", page)
}

/* ---- the hero band ------------------------------------------------------- */

// buildOrgHeader computes everything the hero shows. Port of the frontmatter half of
// OrgHeader.astro.
func buildOrgHeader(b *Builder, org model.Organization, repos []*model.Repo, content render.Content, description string) orgHeader {
	heat := b.Cfg.Theme.Heat
	// The same "recent" window as the profile hero, so the KPI copy and the orange recency
	// accent agree on what counts as recent.
	hotWindow := fmt.Sprintf("in the last %d days", heat.Hot)
	if heat.Hot == 7 {
		hotWindow = "this week"
	}
	touched, templates := 0, 0
	for _, r := range repos {
		if render.HeatFor(deref(r.UpdatedAt), b.Site.Now, heat) == "hot" {
			touched++
		}
		if r.Template {
			templates++
		}
	}
	first := orgEarliestCommit(repos)
	return orgHeader{
		Org:           org,
		Description:   description,
		AvatarSrc:     deref(org.Avatar),
		Base:          b.Router.Base,
		RepoCount:     int64(len(repos)),
		HotWindow:     hotWindow,
		HotDays:       heat.Hot,
		TouchedRecent: touched,
		Templates:     templates,
		CommitsYear:   orgCommitsSince(repos, 365, b.Site.Now),
		CommitsRecent: orgCommitsSince(repos, heat.Hot, b.Site.Now),
		FirstCommit:   first,
		Years:         orgYearsSince(first, b.Site.Now),
		Languages:     orgAggregateLanguages(repos, 5),
		Sites:         orgFrontList(content, "sites"),
		Links:         orgFrontLinks(content),
	}
}

/* ---- aggregates ---------------------------------------------------------- */

// orgAggregateLanguages sums language bytes across repos and recomputes the percentages, with
// everything past the top `top` folded into an "Other" bucket.
//
// Prefixed with org because each page family owns its own helpers: the profile hero computes
// the same aggregate over a different repo set, and two families quietly sharing a function
// here is exactly what the one-file-per-family rule exists to prevent.
func orgAggregateLanguages(repos []*model.Repo, top int) []orgLang {
	type acc struct {
		bytes int64
		color *string
	}
	totals := map[string]*acc{}
	for _, r := range repos {
		for _, l := range r.Languages {
			cur, ok := totals[l.Name]
			if !ok {
				cur = &acc{color: l.Color}
				totals[l.Name] = cur
			}
			cur.bytes += l.Bytes
		}
	}
	var total int64
	for _, v := range totals {
		total += v.bytes
	}
	if total == 0 {
		return nil
	}
	sorted := make([]orgLang, 0, len(totals))
	for name, v := range totals {
		sorted = append(sorted, orgLang{Name: name, Bytes: v.bytes, Color: orgLangColor(v.color)})
	}
	// Bytes descending, then name — code-point order, never a locale-aware compare, so two
	// machines emit the same HTML from the same artifact.
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Bytes != sorted[j].Bytes {
			return sorted[i].Bytes > sorted[j].Bytes
		}
		return sorted[i].Name < sorted[j].Name
	})

	head := sorted
	if len(sorted) > top {
		head = sorted[:top:top]
		var rest int64
		for _, l := range sorted[top:] {
			rest += l.Bytes
		}
		head = append(head, orgLang{Name: "Other", Bytes: rest, Color: orgLangColor(nil)})
	}
	for i := range head {
		head[i].Percent = math.Round(float64(head[i].Bytes)/float64(total)*1000) / 10
	}
	return head
}

// orgDropOther removes the "Other" bucket. The index card has room for three dots, and
// "Other" is not one of the three worth spending it on.
func orgDropOther(langs []orgLang) []orgLang {
	out := make([]orgLang, 0, len(langs))
	for _, l := range langs {
		if l.Name != "Other" {
			out = append(out, l)
		}
	}
	return out
}

// orgLangColor is the only path a language colour takes into a style attribute.
//
// html/template's CSS filter rejects `var(--hf-lang-other)` outright (it becomes ZgotmplZ), so
// the fallback has to be marked template.CSS. That makes this the sink where a colour must be
// PROVEN safe rather than assumed: only a hex literal passes, and anything else — including a
// hand-edited artifact — gets the neutral token instead of reaching CSS unchecked.
func orgLangColor(color *string) template.CSS {
	if color != nil && orgHexColor.MatchString(*color) {
		return template.CSS(*color)
	}
	return template.CSS("var(--hf-lang-other)")
}

// orgCommitsSince counts commits across repos whose commit date falls inside the last `days`.
// Reads Commits alone, never CommitFor: the display-support map exists for lookups, and
// letting it into an aggregate would undo the history-narrowing knobs.
func orgCommitsSince(repos []*model.Repo, days int, now time.Time) int64 {
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	var n int64
	for _, r := range repos {
		for _, c := range r.Commits {
			if d, err := time.Parse(time.RFC3339, c.CommitDate); err == nil && !d.Before(cutoff) {
				n++
			}
		}
	}
	return n
}

// orgEarliestCommit is the earliest createdAt across repos, or "".
func orgEarliestCommit(repos []*model.Repo) string {
	earliest := ""
	for _, r := range repos {
		if d := deref(r.CreatedAt); d != "" && (earliest == "" || d < earliest) {
			earliest = d
		}
	}
	return earliest
}

// orgYearsSince is whole years between `from` and now, floored, never negative.
func orgYearsSince(from string, now time.Time) int {
	if from == "" {
		return 0
	}
	d, err := time.Parse(time.RFC3339, from)
	if err != nil {
		return 0
	}
	years := int(math.Floor(now.Sub(d).Hours() / 24 / 365.25))
	if years < 0 {
		return 0
	}
	return years
}

/* ---- the markdown half --------------------------------------------------- */

// orgFrontString reads a scalar out of an organization's frontmatter.
func orgFrontString(content render.Content, key string) string {
	if s, ok := content.Frontmatter[key].(string); ok {
		return s
	}
	return ""
}

// orgFrontList reads a string sequence out of an organization's frontmatter.
func orgFrontList(content render.Content, key string) []string {
	if l, ok := content.Frontmatter[key].([]string); ok {
		return l
	}
	return nil
}

// orgFrontLinks reads the `links` mapping — labelled outbound links for the hero.
//
// Declaration order, which is what the Astro original's Object.entries gave and what the
// author of the file meant: `links:` is a row of pills, and the one written first belongs
// first. build.contentFrom hands this over as frontmatter's ordered entry slice for exactly
// that reason. The map shapes are still accepted for a payload built by hand, and those are
// sorted by label — a Go map has no order to preserve and a build must emit the same HTML
// twice running.
func orgFrontLinks(content render.Content) []orgLink {
	var out []orgLink
	switch links := content.Frontmatter["links"].(type) {
	case []frontmatter.MapEntry:
		for _, e := range links {
			out = append(out, orgLink{Label: e.Key, URL: e.Value})
		}
		return out
	case map[string]string:
		for label, url := range links {
			out = append(out, orgLink{Label: label, URL: url})
		}
	case map[string]any:
		for label, v := range links {
			if url, ok := v.(string); ok {
				out = append(out, orgLink{Label: label, URL: url})
			}
		}
	default:
		return nil
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}
