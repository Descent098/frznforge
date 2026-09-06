package build

// The repo listing at /repos/ — the port of src/pages/repos/index.astro and
// src/components/RepoListing.astro.
//
// # Enhancement, not hydration
//
// The build renders the COMPLETE default view: the default query, page one — exactly what a
// visitor with JavaScript off gets. web/js/hf-repo-listing.js then adopts that DOM and takes
// over re-rendering. Nothing is rendered twice, which is why three things have to be in the
// markup rather than in the script:
//
//   - the full RepoSummary[] payload, because filtering runs over every repo rather than the
//     page that happens to be showing, and making the first interaction wait on a fetch would
//     be the wrong trade for a few kilobytes;
//   - the four <template> elements the element clones, so card markup has exactly one
//     definition — repo-card.gohtml, mirrored here — instead of a copy in a JS string;
//   - the controls that only appear in some states (Clear, the empty state, the pager), always
//     present and toggled with `hidden`, so the element never constructs chrome.
//
// The query logic itself is render.ApplyListing and friends, the Go twin of web/js/listing.js,
// pinned to it by tests/fixtures/listing-cases.json. This file only chooses the query.

import (
	"encoding/json"
	"fmt"
	"html/template"

	"frznforge/internal/model"
	"frznforge/internal/render"
)

// RepoListingData is one rendered listing: the server's view of the default query plus
// everything the browser element needs to take over.
//
// Exported, along with RepoListing below, because the ORGANIZATION page family emits the same
// listing scoped to one org (/orgs/<slug>/repos/) and calls straight into this — the two must
// not drift into two implementations of the same toolbar.
type RepoListingData struct {
	// BasePath is the path this listing lives at; every link that is not a bare query string
	// (tag chips, the pager's "no filters left" href) is built from it. Ends in a slash.
	BasePath string
	PageSize int
	// NowMillis is the build clock as epoch milliseconds — the element re-renders ages against
	// the same instant the server used, so a card does not jump when it is rebuilt.
	NowMillis int64
	// HeatJSON is theme.heat as the element parses it out of data-heat.
	HeatJSON string

	Query render.ListingQuery
	Sorts []listingSort
	Kinds []listingKind

	Languages []render.Facet
	Tags      []render.Facet

	Items []render.RepoSummary
	// Total is how many repos match; RepoCount is how many exist. The count reads "N of M".
	Total     int
	RepoCount int
	Page      int
	PageCount int

	// Repos is the JSON payload of the <script type="application/json"> block. See
	// listingPayload for why it is typed template.JS.
	Repos template.JS
}

// listingSort is one entry of the sort <select>.
type listingSort struct {
	Key      string
	Label    string
	Selected bool
}

// listingKind is one button of the kind segmented control. Suffix carries the template count,
// which is the only kind that shows one.
type listingKind struct {
	Key    string
	Label  string
	Suffix string
	Active bool
}

// listingKinds are the three buttons, in the order they are shown.
var listingKinds = [][2]string{{"all", "All"}, {"normal", "Repos"}, {"template", "Templates"}}

// RepoListing builds the listing for a set of repos rendered at basePath.
//
// basePath must end in a slash: /repos/ for the site-wide listing, /orgs/<slug>/repos/ for an
// organization's, which is what keeps filtering inside that organization.
//
// pages_orgs.go CALLS THIS for /orgs/<slug>/repos/ and renders the result through the same
// "repo-listing" template — pass the org's member summaries and b.Router.OrgReposURL(slug), and
// the toolbar, facets, payload and <template> blocks all come out scoped to that org. Changing
// the shape here changes that page too.
func RepoListing(b *Builder, repos []render.RepoSummary, basePath string) (*RepoListingData, error) {
	// A static build cannot know the query string. It renders the default view; a visitor
	// arriving at ?tag=cli gets this same HTML and the element applies their query on load.
	query := render.DefaultQuery(b.Cfg.Listing.PageSize)
	result := render.ApplyListing(repos, query)
	languages, tags, templates := render.Facets(repos)

	payload, err := listingPayload(repos)
	if err != nil {
		return nil, err
	}
	heat, err := json.Marshal(b.Cfg.Theme.Heat)
	if err != nil {
		return nil, err
	}

	d := &RepoListingData{
		BasePath:  basePath,
		PageSize:  b.Cfg.Listing.PageSize,
		NowMillis: b.Site.Now.UnixMilli(),
		HeatJSON:  string(heat),
		Query:     query,
		Languages: languages,
		Tags:      tags,
		Items:     result.Items,
		Total:     result.Total,
		RepoCount: len(repos),
		Page:      result.Page,
		PageCount: result.PageCount,
		Repos:     template.JS(payload),
	}
	for _, key := range render.SortKeys {
		d.Sorts = append(d.Sorts, listingSort{Key: key, Label: render.SortLabels[key], Selected: key == query.Sort})
	}
	for _, k := range listingKinds {
		kind := listingKind{Key: k[0], Label: k[1], Active: k[0] == query.Kind}
		if k[0] == "template" && templates > 0 {
			kind.Suffix = fmt.Sprintf(" (%d)", templates)
		}
		d.Kinds = append(d.Kinds, kind)
	}
	return d, nil
}

// listingPayload serialises the summaries for the <script type="application/json"> block.
//
// encoding/json's HTML escaping is left ON, so every `<` (and `>` and `&`) reaches the browser
// as its six-character JSON unicode escape instead. That is what makes the block un-closable:
// a repo description containing a literal `</script>` cannot end the tag early, because no `<`
// is ever emitted. JSON.parse decodes the escapes, so the browser still sees the author's
// original text.
//
// The result is marked template.JS because html/template would otherwise treat the block as a
// script body and re-encode the array as a JSON *string*. Marking it is safe here for one
// reason only — the bytes came out of encoding/json with escaping on, so they provably contain
// no `<` at all. Nothing else may take this path.
func listingPayload(repos []render.RepoSummary) (string, error) {
	// Never nil: `null` would make `JSON.parse(...).map` throw in the browser, where `[]`
	// simply renders an empty listing.
	if repos == nil {
		repos = []render.RepoSummary{}
	}
	out, err := json.Marshal(repos)
	if err != nil {
		return "", fmt.Errorf("repo listing payload: %w", err)
	}
	return string(out), nil
}

// summarizeRepos projects every repo down to its client-safe summary, in artifact order.
func summarizeRepos(repos []model.Repo) []render.RepoSummary {
	out := make([]render.RepoSummary, 0, len(repos))
	for i := range repos {
		out = append(out, render.Summarize(&repos[i]))
	}
	return out
}

// reposPage is the payload of page-repos: the header's count plus the listing itself.
type reposPage struct {
	Count   int
	Listing *RepoListingData
}

// emitReposListing writes /repos/.
func emitReposListing(b *Builder) error {
	repos := summarizeRepos(b.Data.Repos)
	listing, err := RepoListing(b, repos, b.Router.ReposURL())
	if err != nil {
		return err
	}
	return b.WritePage(b.Router.ReposURL(), "page-repos", render.Page{
		Title:       "Repositories",
		Description: fmt.Sprintf("All %d repositories by %s", len(repos), b.Cfg.Owner.Name),
		Active:      "repos",
		Payload:     &reposPage{Count: len(repos), Listing: listing},
	})
}
