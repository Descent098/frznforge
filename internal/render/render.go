// Package render turns an artifact into the static site — the port of src/pages/**,
// src/components/** and src/layouts/Base.astro.
//
// # Why html/template
//
// It is stdlib, and its contextual escaping is the property that matters here: the site
// renders repository content — READMEs, commit messages, file paths, release notes — that
// nobody vetted. html/template knows whether it is emitting into an attribute, a URL, a script
// or ordinary text and escapes accordingly, which is exactly the class of bug a string
// concatenator invites. The one rule to keep straight is that anything already rendered to
// HTML (markdown output, highlighted code, the icon sprite) must be typed template.HTML, and
// nothing else may be.
//
// # Templates live in files, not in Go strings
//
// templates/*.gohtml are embedded at build time. Keeping them as files means they diff like
// markup and can be compared against the .astro components they replace during the migration;
// a Go string literal full of HTML would make that review impossible.
package render

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io"
	"strings"
	"time"

	"frznforge/internal/config"
	"frznforge/internal/model"
	"frznforge/internal/routes"
)

//go:embed templates/*.gohtml
var templateFS embed.FS

// Site is everything a page can read: the config, the artifact, the URL builder and the
// hand-written content. Assembled once per build and shared by every page — pages never
// re-derive site-wide facts, which is what keeps them cheap enough to render in parallel.
type Site struct {
	Cfg    *config.Resolved
	Data   *model.ForgeData
	Router routes.Router

	// Now is the build clock, injected rather than read, so a page's "3 days ago" is
	// reproducible in tests and identical across the pages of one build.
	Now time.Time

	// Profile is the owner's profile.md, front matter and rendered body.
	Profile Content
	// Orgs maps an organization slug to its content/orgs/<slug>.md, when it has one.
	Orgs map[string]Content
	// UnmatchedOrgContent lists org markdown files no configured organization claims. Surfaced
	// on /orgs/ because a typo in the filename otherwise discards the whole file silently.
	UnmatchedOrgContent []string
}

// Content is a hand-written markdown file: its front matter and its rendered body.
type Content struct {
	// Exists is false when the file is absent, which is not an error — an org without a
	// markdown file still gets a page, built from config alone.
	Exists      bool
	Frontmatter map[string]any
	Body        template.HTML
}

// Page is the per-page half of a template's data. Site is embedded so templates can reach
// site-wide values without every page struct repeating them — `.Data` in a template is
// therefore the ARTIFACT, and the page's own payload is `.Payload`. They are named apart on
// purpose: an embedded field and an outer field of the same name resolve silently to the outer
// one, which would have made every `.Data.Repos` in the shell quietly wrong.
type Page struct {
	*Site
	// Title is the page title; the shell appends the site title unless they are equal.
	Title string
	// Description fills <meta name="description"> when set.
	Description string
	// Active marks the current primary nav item: profile, repos, notes, orgs, or empty.
	Active string
	// Payload is the page-specific data, whatever the page's template expects.
	Payload any
	// Body is the rendered page body, filled in by RenderPage before the shell runs.
	Body template.HTML
	// ExtraStyles and ExtraScripts are additional stylesheets and modules this page needs —
	// the per-section stylesheets (notes, orgs, insights) and the mermaid renderer.
	ExtraStyles  []string
	ExtraScripts []string
}

// FullTitle is what goes in <title>: the page title, suffixed with the site title unless they
// are the same string.
func (p Page) FullTitle() string {
	if p.Title == p.Cfg.Site.Title {
		return p.Title
	}
	return p.Title + " · " + p.Cfg.Site.Title
}

// SiteHost is the configured site URL without its scheme, shown under the brand — or empty.
func (p Page) SiteHost() string {
	if p.Cfg.Site.URL == "" {
		return ""
	}
	return PrettyURL(p.Cfg.Site.URL)
}

// WarningSummary is the footer tooltip listing every ingest warning, one per line.
func (p Page) WarningSummary() string {
	lines := make([]string, 0, len(p.Data.Warnings))
	for _, w := range p.Data.Warnings {
		lines = append(lines, "["+w.Code+"] "+w.Message)
	}
	return strings.Join(lines, "\n")
}

// Renderer holds the parsed template set.
type Renderer struct {
	tpl  *template.Template
	site *Site
}

// New parses the template set and binds it to a site.
func New(site *Site) (*Renderer, error) {
	r := &Renderer{site: site}
	tpl := template.New("").Funcs(r.funcs())
	tpl, err := tpl.ParseFS(templateFS, "templates/*.gohtml")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	r.tpl = tpl
	return r, nil
}

// Render writes one template by name, without the shell. Used for fragments and for the
// non-HTML outputs (the search index).
func (r *Renderer) Render(w io.Writer, name string, page Page) error {
	page.Site = r.site
	if err := r.tpl.ExecuteTemplate(w, name, page); err != nil {
		return fmt.Errorf("render %s: %w", name, err)
	}
	return nil
}

// RenderPage renders a body template and wraps it in the site shell.
//
// The body goes through a buffer rather than the shell being split into an "open" and a
// "close" partial. That costs one allocation per page and buys templates that are each a
// balanced fragment — which is what lets a page family be written, read and reviewed on its
// own instead of only making sense sandwiched between two halves of another file.
func (r *Renderer) RenderPage(w io.Writer, bodyTemplate string, page Page) error {
	page.Site = r.site
	if r.tpl.Lookup(bodyTemplate) == nil {
		return fmt.Errorf("no template named %q", bodyTemplate)
	}
	var body bytes.Buffer
	if err := r.tpl.ExecuteTemplate(&body, bodyTemplate, page); err != nil {
		return fmt.Errorf("render %s: %w", bodyTemplate, err)
	}
	page.Body = template.HTML(body.String())
	if err := r.tpl.ExecuteTemplate(w, "shell", page); err != nil {
		return fmt.Errorf("render shell for %s: %w", bodyTemplate, err)
	}
	return nil
}

// Has reports whether a template of that name is defined. Used by the build to fail loudly on
// a page family whose template was never written, rather than emitting an empty file.
func (r *Renderer) Has(name string) bool { return r.tpl.Lookup(name) != nil }

// funcs are the helpers templates may call. Deliberately small: anything that needs real logic
// belongs in Go, computed into the page's Data, where it can be tested. A template function is
// for the things that are genuinely presentational.
func (r *Renderer) funcs() template.FuncMap {
	rt := func() routes.Router { return r.site.Router }
	return template.FuncMap{
		// URLs. Every one of these goes through the Router, so a sub-path deploy prefixes
		// everything or nothing — there is no way to hand-write a root-absolute href.
		"withBase":    func(p string) string { return rt().WithBase(p) },
		"homeURL":     func() string { return rt().HomeURL() },
		"reposURL":    func() string { return rt().ReposURL() },
		"notesURL":    func() string { return rt().NotesIndexURL() },
		"orgsURL":     func() string { return rt().OrgsIndexURL() },
		"repoURL":     func(slug string) string { return rt().RepoURL(slug) },
		"noteURL":     func(slug string) string { return rt().NoteURL(slug) },
		"orgURL":      func(slug string) string { return rt().OrgURL(slug) },
		"orgReposURL": func(slug string) string { return rt().OrgReposURL(slug) },
		"noteRawURL":  func(slug, p string) string { return rt().NoteRawURL(slug, p) },
		"treeURL":     func(slug, ref, p string) string { return rt().TreeURL(slug, ref, p) },
		"blobURL":     func(slug, ref, p string) string { return rt().BlobURL(slug, ref, p) },
		"rawURL":      func(slug, ref, p string) string { return rt().RawURL(slug, ref, p) },
		"commitsURL":  func(slug, ref string, page int) string { return rt().CommitsURL(slug, ref, page) },
		"commitURL":   func(slug, sha string) string { return rt().CommitURL(slug, sha) },
		"branchesURL": func(slug string) string { return rt().BranchesURL(slug) },
		"tagsURL":     func(slug string) string { return rt().TagsURL(slug) },
		"releasesURL": func(slug string) string { return rt().ReleasesURL(slug) },
		"releaseURL":  func(slug, tag string) string { return rt().ReleaseURL(slug, tag) },
		"insightsURL": func(slug string) string { return rt().InsightsURL(slug) },
		"archiveURL":  func(slug, ref string) string { return rt().ArchiveURL(slug, ref) },
		"hostedURL":   func(slug string) string { return rt().HostedURL(slug) },

		// Display helpers, ported from web/js/format.js so the two agree. The Go side is the
		// server's copy of that logic; tests/format_sync_test.go pins them to the same fixture.
		"relativeTime":      func(date string) string { return RelativeTime(date, r.site.Now) },
		"relativeTimeShort": func(date string) string { return RelativeTimeShort(date, r.site.Now) },
		"heatClass": func(date string) string {
			return "t-" + HeatFor(date, r.site.Now, r.site.Cfg.Theme.Heat)
		},
		"heat":        func(date string) string { return HeatFor(date, r.site.Now, r.site.Cfg.Theme.Heat) },
		"formatInt":   FormatInt,
		"formatBytes": FormatBytes,
		"initials":    Initials,
		"prettyURL":   PrettyURL,
		"licenseURL":  LicenseURL,
		"monthYear":   MonthYear,
		"isoDay":      IsoDay,

		// Small template conveniences.
		"plural": func(n int, one, many string) string {
			if n == 1 {
				return one
			}
			return many
		},
		"deref": func(p *string) string {
			if p == nil {
				return ""
			}
			return *p
		},
		"orDefault": func(p *string, fallback string) string {
			if p == nil || *p == "" {
				return fallback
			}
			return *p
		},
		"add":  func(a, b int) int { return a + b },
		"sub":  func(a, b int) int { return a - b },
		"join": strings.Join,
		"dict": dict,
	}
}

// dict builds a map inside a template, for passing several values to a partial.
func dict(pairs ...any) (map[string]any, error) {
	if len(pairs)%2 != 0 {
		return nil, fmt.Errorf("dict needs an even number of arguments, got %d", len(pairs))
	}
	out := make(map[string]any, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		key, ok := pairs[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict keys must be strings, got %T", pairs[i])
		}
		out[key] = pairs[i+1]
	}
	return out, nil
}
