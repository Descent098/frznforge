package render

import (
	"html/template"
	"regexp"
	"sort"
	"strings"

	"frznforge/internal/model"
)

// The server half of the repo listing — the Go twin of web/js/listing.js.
//
// The browser rebuilds cards from the same query logic when a filter is touched, so the two
// must agree exactly or the listing changes the instant a visitor interacts with it. Pinned by
// tests/fixtures/listing-cases.json, generated FROM the JavaScript, the same way format.go is.
//
// Only what the SERVER needs lives here: the build renders the default query, page one. The
// browser owns URL parsing and serialisation, because only it has a URL.

// RepoSummary is the client-safe projection of a Repo — no commits, tree or files.
//
// The JSON tags are the wire format of the `<script type="application/json">` payload the
// listing page emits, so they match the RepoSummary typedef in web/js/listing.js exactly.
type RepoSummary struct {
	Slug        string        `json:"slug"`
	Name        string        `json:"name"`
	Description *string       `json:"description"`
	Tags        []string      `json:"tags"`
	Template    bool          `json:"template"`
	Empty       bool          `json:"empty"`
	Languages   []SummaryLang `json:"languages"`
	CommitCount int64         `json:"commitCount"`
	CreatedAt   *string       `json:"createdAt"`
	UpdatedAt   *string       `json:"updatedAt"`
}

// SummaryLang is one language on a card: name, share and colour, nothing else.
type SummaryLang struct {
	Name    string  `json:"name"`
	Percent float64 `json:"percent"`
	Color   *string `json:"color"`
}

// Summarize projects a Repo down to what cards and filtering need.
func Summarize(repo *model.Repo) RepoSummary {
	langs := make([]SummaryLang, 0, len(repo.Languages))
	for _, l := range repo.Languages {
		langs = append(langs, SummaryLang{Name: l.Name, Percent: l.Percent, Color: l.Color})
	}
	tags := repo.Tags
	if tags == nil {
		tags = []string{}
	}
	return RepoSummary{
		Slug: repo.Slug, Name: repo.Name, Description: repo.Description,
		Tags: tags, Template: repo.Template, Empty: repo.Empty,
		Languages: langs, CommitCount: repo.CommitCount,
		CreatedAt: repo.CreatedAt, UpdatedAt: repo.UpdatedAt,
	}
}

// SortKeys are the six sort orders, in the order the <select> lists them.
var SortKeys = []string{"updated-desc", "updated-asc", "created-desc", "created-asc", "name-asc", "name-desc"}

// SortLabels are the human labels for each sort key.
var SortLabels = map[string]string{
	"updated-desc": "Recently updated",
	"updated-asc":  "Least recently updated",
	"created-desc": "Newest",
	"created-asc":  "Oldest",
	"name-asc":     "Name A→Z",
	"name-desc":    "Name Z→A",
}

// ListingQuery is one view of the listing.
type ListingQuery struct {
	Q         string
	Sort      string
	Languages []string
	Tags      []string
	Kind      string // "all" | "template" | "normal"
	Page      int    // 1-based
	PageSize  int
}

// DefaultQuery is what the build renders: everything, newest first, page one.
func DefaultQuery(pageSize int) ListingQuery {
	return ListingQuery{Q: "", Sort: "updated-desc", Languages: []string{}, Tags: []string{}, Kind: "all", Page: 1, PageSize: pageSize}
}

// ListingResult is a page of the listing.
type ListingResult struct {
	Items     []RepoSummary
	Total     int
	Page      int
	PageCount int
}

// Facet is a filter value and how many repos carry it.
type Facet struct {
	Name  string
	Count int
}

// Facets are the distinct languages and tags with repo counts, sorted by count desc then name.
func Facets(repos []RepoSummary) (languages, tags []Facet, templates int) {
	langCount := map[string]int{}
	tagCount := map[string]int{}
	for _, r := range repos {
		for _, l := range r.Languages {
			langCount[l.Name]++
		}
		for _, t := range r.Tags {
			tagCount[t]++
		}
		if r.Template {
			templates++
		}
	}
	return toFacets(langCount), toFacets(tagCount), templates
}

func toFacets(m map[string]int) []Facet {
	out := make([]Facet, 0, len(m))
	for name, count := range m {
		out = append(out, Facet{Name: name, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// MatchesQuery reports whether a repo matches the free-text query. All terms must match, across
// name, slug, description, tags and language names.
func MatchesQuery(repo RepoSummary, q string) bool {
	terms := strings.Fields(strings.ToLower(strings.TrimSpace(q)))
	if len(terms) == 0 {
		return true
	}
	parts := []string{repo.Name, repo.Slug, derefStr(repo.Description)}
	parts = append(parts, repo.Tags...)
	for _, l := range repo.Languages {
		parts = append(parts, l.Name)
	}
	hay := strings.ToLower(strings.Join(parts, " "))
	for _, t := range terms {
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}

// MatchesFilters applies the kind, language and tag filters plus the free-text query.
func MatchesFilters(repo RepoSummary, q ListingQuery) bool {
	if q.Kind == "template" && !repo.Template {
		return false
	}
	if q.Kind == "normal" && repo.Template {
		return false
	}
	if len(q.Languages) > 0 {
		have := map[string]bool{}
		for _, l := range repo.Languages {
			have[l.Name] = true
		}
		for _, want := range q.Languages {
			if !have[want] {
				return false
			}
		}
	}
	if len(q.Tags) > 0 {
		have := map[string]bool{}
		for _, t := range repo.Tags {
			have[t] = true
		}
		for _, want := range q.Tags {
			if !have[want] {
				return false
			}
		}
	}
	return MatchesQuery(repo, q.Q)
}

// SortRepos orders repos by one of SortKeys.
//
// The name tiebreak is case-insensitive on the name and then case-sensitive on the slug,
// matching `localeCompare(name, 'en', {sensitivity:'base'}) || localeCompare(slug)`. It is
// deliberately NOT a locale-aware collation: that depends on the build machine's ICU data, and
// two machines would emit different HTML from the same artifact.
func SortRepos(repos []RepoSummary, sortKey string) []RepoSummary {
	out := append([]RepoSummary(nil), repos...)
	nameAsc := func(a, b RepoSummary) bool {
		la, lb := strings.ToLower(a.Name), strings.ToLower(b.Name)
		if la != lb {
			return la < lb
		}
		return a.Slug < b.Slug
	}
	byDate := func(a, b *string) int {
		x, y := derefStr(a), derefStr(b)
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch sortKey {
		case "updated-desc":
			if c := byDate(b.UpdatedAt, a.UpdatedAt); c != 0 {
				return c < 0
			}
		case "updated-asc":
			if c := byDate(a.UpdatedAt, b.UpdatedAt); c != 0 {
				return c < 0
			}
		case "created-desc":
			if c := byDate(b.CreatedAt, a.CreatedAt); c != 0 {
				return c < 0
			}
		case "created-asc":
			if c := byDate(a.CreatedAt, b.CreatedAt); c != 0 {
				return c < 0
			}
		case "name-desc":
			return nameAsc(b, a)
		}
		return nameAsc(a, b)
	})
	return out
}

// ApplyListing filters, sorts and paginates. The page is clamped into range and PageCount is at
// least 1, so an empty listing still renders "Page 1 of 1" rather than "of 0".
func ApplyListing(repos []RepoSummary, q ListingQuery) ListingResult {
	filtered := make([]RepoSummary, 0, len(repos))
	for _, r := range repos {
		if MatchesFilters(r, q) {
			filtered = append(filtered, r)
		}
	}
	filtered = SortRepos(filtered, q.Sort)

	pageSize := q.PageSize
	if pageSize < 1 {
		pageSize = 1
	}
	pageCount := (len(filtered) + pageSize - 1) / pageSize
	if pageCount < 1 {
		pageCount = 1
	}
	page := q.Page
	if page < 1 {
		page = 1
	}
	if page > pageCount {
		page = pageCount
	}
	start := (page - 1) * pageSize
	end := start + pageSize
	if start > len(filtered) {
		start = len(filtered)
	}
	if end > len(filtered) {
		end = len(filtered)
	}
	return ListingResult{Items: filtered[start:end], Total: len(filtered), Page: page, PageCount: pageCount}
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// hexColor is the shape LangColor passes through untouched: CSS hex notation, 3/4/6/8 digits.
var hexColor = regexp.MustCompile(`^#(?:[0-9A-Fa-f]{3,4}|[0-9A-Fa-f]{6}|[0-9A-Fa-f]{8})$`)

// LangColor turns a language's colour into a CSS value a template may interpolate.
//
// The typed return bypasses html/template's CSS filter — which is the only way to emit
// `var(--hf-lang-other)` at all, because the filter rejects `var(...)` outright and substitutes
// ZgotmplZ. Returning a plain string here therefore did NOT render the fallback colour: it
// rendered the literal text `ZgotmplZ` into a style attribute, for every language the colour map
// does not know. Neither fixture happens to contain one, which is exactly why it went unnoticed.
//
// The bypass is safe ONLY because everything else is rejected first: a value that is not literal
// hex notation becomes the one fixed token, so nothing from the artifact reaches a stylesheet
// uninspected.
func LangColor(l SummaryLang) template.CSS {
	if l.Color != nil && hexColor.MatchString(*l.Color) {
		return template.CSS(*l.Color)
	}
	return template.CSS("var(--hf-lang-other)")
}
