package build

import (
	"fmt"
	"html/template"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"frznforge/internal/highlight"
	"frznforge/internal/markdown"
	"frznforge/internal/model"
	"frznforge/internal/render"
	"frznforge/internal/routes"
	"frznforge/internal/timings"
)

// The ref-scoped file browser — the port of src/pages/repos/[slug]/{tree,blob,raw}/… together
// with FileTable.astro and RefSwitcher.astro.
//
// This family is the multiplier: tree, blob and raw are 87-90% of the routes a real site emits,
// so anything that does not actually vary per page is computed once and reused. Three things
// were per-page in the Astro original and are not here: the repo header (same on every page of
// a repo), the ref switcher's "does this path exist over there" answer (a scan of every other
// ref's whole tree, once per page), and a directory's own entries (a scan of the ref's whole
// tree, once per directory). Everything else is a straight port.

/* ---- the repo header ----------------------------------------------------- */

// sectionLabels is RepoHeader.astro's SECTION_LABEL: the word appended to the repo name in a
// sub-page's hidden <h1>. Code has none — the page IS the repo.
var sectionLabels = map[string]string{
	"code": "", "commits": "Commits", "branches": "Branches",
	"tags": "Tags", "releases": "Releases", "insights": "Insights",
}

// repoSubHead is the payload of the repo-sub-head partial: the port of RepoHeader.astro.
//
// It lives with this family rather than in a shared template file because the repo overview
// family owns its own copy while the port is split by family — a partial two families edit
// independently is exactly how two families come to disagree about the chrome. When both have
// landed, one of them becomes the shared partial.
//
// Built ONCE per repo and copied per page, because only the heading and the current tab vary:
// the release list, the license URL and the upstream host are identical on all thousand pages
// of a thousand-file repo, and re-deriving them there was the header's whole cost.
type repoSubHead struct {
	// Active is the current tab: code, commits, branches, tags, releases or insights.
	Active string
	// Heading fills the visually hidden <h1> every sub-page owes a screen reader.
	Heading string
	// OwnHeading suppresses that <h1> for a page that draws a visible one (only Insights).
	OwnHeading bool

	OwnerHandle string
	HomeURL     string

	Name     string
	Template bool
	Desc     string

	Upstream     string
	UpstreamHost string

	HasLicense   bool
	LicenseURL   string
	LicenseLabel string

	DefaultBranch string
	Branches      int
	Tags          int
	Contributors  int
	CommitCount   int64
	Empty         bool
	Releases      int
	ShowInsights  bool

	RepoURL     string
	CommitsURL  string
	BranchesURL string
	TagsURL     string
	ReleasesURL string
	InsightsURL string
}

// newRepoSubHead derives the parts of the header that are fixed for a repo.
func newRepoSubHead(b *Builder, repo *model.Repo) repoSubHead {
	h := repoSubHead{
		OwnerHandle:   b.Cfg.Owner.Handle,
		HomeURL:       b.Router.HomeURL(),
		Name:          repo.Name,
		Template:      repo.Template,
		Desc:          deref(repo.Description),
		DefaultBranch: deref(repo.DefaultBranch),
		Branches:      len(repo.Branches),
		Tags:          len(repo.GitTags),
		Contributors:  len(repo.Contributors),
		CommitCount:   repo.CommitCount,
		Empty:         repo.Empty,
		Releases:      len(siteReleases(repo)),
		ShowInsights:  routes.HasInsights(repo),
		RepoURL:       b.Router.RepoURL(repo.Slug),
		BranchesURL:   b.Router.BranchesURL(repo.Slug),
		TagsURL:       b.Router.TagsURL(repo.Slug),
		ReleasesURL:   b.Router.ReleasesURL(repo.Slug),
		InsightsURL:   b.Router.InsightsURL(repo.Slug),
	}
	if repo.DefaultBranch != nil {
		h.CommitsURL = b.Router.CommitsURL(repo.Slug, *repo.DefaultBranch, 1)
	}
	if u := deref(repo.Links.Upstream); u != "" {
		h.Upstream = u
		h.UpstreamHost = hostOf(u)
	}
	if repo.License != nil {
		spdx := deref(repo.License.Spdx)
		h.HasLicense = true
		h.LicenseLabel = firstNonEmpty(spdx, "Custom")
		h.LicenseURL = render.LicenseURL(spdx)
	}
	return h
}

// forPage stamps the per-page half of the header. An empty heading takes RepoHeader.astro's
// generated one; the tree and blob pages pass the path being browsed instead, which is a far
// better "where am I" than the repo name every page of the repo shares.
func (h repoSubHead) forPage(active, heading string, ownHeading bool) repoSubHead {
	h.Active = active
	h.OwnHeading = ownHeading
	if heading == "" {
		if label := sectionLabels[active]; label != "" {
			heading = h.Name + " · " + label
		} else {
			heading = h.Name
		}
	}
	h.Heading = heading
	return h
}

// hostOf strips the scheme and a leading "www." off an upstream URL, the way the component did
// with `new URL(u).hostname`. A URL the config author mistyped keeps
// its raw text on the button: the host name is a decoration, and failing a build over one would
// be worse than showing it whole.
func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
		return strings.TrimPrefix(u.Hostname(), "www.")
	}
	return raw
}

/* ---- the family ---------------------------------------------------------- */

func emitRepoRefs(b *Builder, repo *model.Repo) error {
	head := newRepoSubHead(b, repo)
	if err := b.timed("build.tree", repo.Slug, func(local *Builder) error {
		return emitTreePages(local, repo, head)
	}); err != nil {
		return err
	}
	if err := b.timed("build.blob", repo.Slug, func(local *Builder) error {
		return emitBlobPages(local, repo, head)
	}); err != nil {
		return err
	}
	return b.timed("build.raw", repo.Slug, func(local *Builder) error {
		return emitRawFiles(local, repo)
	})
}

// refName is how a ref is named in the timings file: the repo it belongs to, then the ref.
//
// Both halves are needed. Without the slug every repository's "main" aggregates into one row,
// which is exactly the wrong answer to "which ref is slow"; without the ref the repo's families
// are indistinguishable from each other.
func refName(slug, ref string) string { return slug + "@" + ref }

// fileSegments splits a route list into one contiguous range per ref.
//
// BlobRoutes and RawRoutes both walk BrowsableRefs in order, so a ref's routes are always
// adjacent and a segment is a pair of indices rather than a second copy of the list. That is
// what lets each ref be rendered — and therefore measured — as its own parallel section without
// re-sorting anything or changing which page lands where.
func fileSegments(list []routes.FileRoute) [][2]int {
	var out [][2]int
	for i := 0; i < len(list); {
		j := i + 1
		for j < len(list) && list[j].Ref.Name == list[i].Ref.Name {
			j++
		}
		out = append(out, [2]int{i, j})
		i = j
	}
	return out
}

/* ---- tree ---------------------------------------------------------------- */

// crumb is one link in a path breadcrumb.
type crumb struct {
	Name string
	URL  string
	// Last marks the segment the reader is standing on, which is drawn as text, not a link.
	Last bool
}

// readmeView is the README rendered below a directory listing, GitHub-style.
type readmeView struct {
	Path string
	// IsMarkdown picks the presentation: rendered markdown, or the file's text in a <pre>.
	IsMarkdown bool
	HTML       template.HTML
	Text       string
}

// treePage is one directory of one ref.
type treePage struct {
	Head     repoSubHead
	Switcher refSwitcher
	RepoName string
	RootURL  string
	Crumbs   []crumb
	Files    fileTable
	Readme   *readmeView
	// NoReadme is the "No README in this directory." line — a sub-directory only, since the
	// repo root without one already says so on the overview.
	NoReadme bool
}

func emitTreePages(b *Builder, repo *model.Repo, head repoSubHead) error {
	refs := browsableDirs(repo)
	trusted := markdown.IsTrustedSource(repo.Source)

	// Rebuilt whenever the ref changes; TreeRoutes walks the refs in order, so this is one
	// pass over each ref's tree for all of that ref's directory pages.
	var (
		curRef   string
		children map[string][]model.TreeEntry
	)
	// The per-ref step rides the same "the ref changed" edge the tree cache does. This loop is
	// serial and in ref order, so the window between two edges IS that ref's tree build — no
	// accounting needed beyond closing the previous step.
	var refStep *timings.Step
	refPages := int64(0)
	closeRef := func(err error) {
		refStep.Fail(err).DoneWith(timings.Counts{"pages": refPages})
		refStep, refPages = nil, 0
	}
	for _, route := range b.Router.TreeRoutes(repo) {
		if children == nil || route.Ref.Name != curRef {
			closeRef(nil)
			curRef = route.Ref.Name
			children = childrenByDir(route.Ref.Tree)
			refStep = b.step.Child("build.ref", refName(repo.Slug, curRef))
		}
		entries := children[route.Path]

		segments := []string{}
		if route.Path != "" {
			segments = strings.Split(route.Path, "/")
		}
		crumbs := make([]crumb, 0, len(segments))
		for i, name := range segments {
			crumbs = append(crumbs, crumb{
				Name: name,
				URL:  b.Router.TreeURL(repo.Slug, route.Ref.Name, strings.Join(segments[:i+1], "/")),
				Last: i == len(segments)-1,
			})
		}
		parentURL := ""
		if route.Path != "" {
			parentURL = b.Router.TreeURL(repo.Slug, route.Ref.Name, strings.Join(segments[:len(segments)-1], "/"))
		}

		readme, err := dirReadme(b, route.Ref, entries, trusted)
		if err != nil {
			err = fmt.Errorf("readme in %s of %s: %w", route.Path, repo.Slug, err)
			closeRef(err)
			return err
		}

		title := repo.Name + " at " + route.Ref.Name
		if route.Path != "" {
			title = route.Path + " · " + title
		}
		page := render.Page{
			Title:       title,
			Description: deref(repo.Description),
			Active:      "repos",
			Payload: treePage{
				Head:     head.forPage("code", title, false),
				Switcher: newRefSwitcher(b.Router, repo.Slug, refs, route.Ref, route.Path),
				RepoName: repo.Name,
				RootURL:  b.Router.TreeURL(repo.Slug, route.Ref.Name, ""),
				Crumbs:   crumbs,
				Files:    newFileTable(b, repo, route.Ref.Name, entries, parentURL),
				Readme:   readme,
				NoReadme: readme == nil && route.Path != "",
			},
		}
		if readme != nil && readme.IsMarkdown && markdown.ContainsMermaid(string(readme.HTML)) {
			page.ExtraScripts = []string{b.Router.WithBase("/js/mermaid.js")}
		}
		if err := b.WritePage(route.URL, "page-tree", page); err != nil {
			closeRef(err)
			return err
		}
		refPages++
	}
	closeRef(nil)
	return nil
}

// childrenByDir buckets a flat tree by the directory each entry sits directly in.
//
// The Astro component filtered the whole tree per directory page, which is O(entries) work
// repeated once per directory — the single largest avoidable cost in this family on a repo of
// any size. One pass builds every bucket, in tree order.
func childrenByDir(tree []model.TreeEntry) map[string][]model.TreeEntry {
	out := make(map[string][]model.TreeEntry)
	for _, e := range tree {
		dir := ""
		if i := strings.LastIndex(e.Path, "/"); i >= 0 {
			dir = e.Path[:i]
		}
		out[dir] = append(out[dir], e)
	}
	return out
}

var readmeNameRe = regexp.MustCompile(`(?i)^readme(\.|$)`)

// dirReadme renders the README sitting in this directory, or nil when there is none to show.
//
// A README that is binary or was never stored yields nil rather than an error: the file exists
// in the commit, it simply has no content here, and a directory listing is not the place to
// fail a build over it.
func dirReadme(b *Builder, ref routes.BrowsableRef, entries []model.TreeEntry, trusted bool) (*readmeView, error) {
	var entry *model.TreeEntry
	for i := range entries {
		e := &entries[i]
		if (e.Type == "blob" || e.Type == "symlink") && readmeNameRe.MatchString(e.Name) {
			entry = e
			break
		}
	}
	if entry == nil {
		return nil, nil
	}
	info, ok := ref.Files[entry.Path]
	if !ok || !info.Stored || info.Binary {
		return nil, nil
	}
	content, err := b.Blob(info.Sha)
	if err != nil {
		return nil, err
	}
	view := &readmeView{Path: entry.Path, IsMarkdown: markdown.IsMarkdownPath(entry.Path)}
	if view.IsMarkdown {
		view.HTML = htmlOf(b.Markdown(string(content), trusted))
	} else {
		// Not marked as HTML: the template writes it into a <pre> and html/template escapes
		// it there, which is the same inert result the TypeScript reached by escaping the
		// four characters by hand.
		view.Text = string(content)
	}
	return view, nil
}

/* ---- the file table ------------------------------------------------------ */

// fileRow is one row of a directory listing.
type fileRow struct {
	Name  string
	IsDir bool
	// URL is empty when the entry has no page at all. Why then says which of the two reasons
	// it is, because "listed but not a link" needs an explanation on the row itself.
	URL   string
	Why   string // "submodule" | "unservable"
	Title string

	CommitURL string
	Subject   string
	// Date is the last commit's commit date, or empty when this build kept no such commit.
	Date string
}

// fileTable is the payload of the file-table partial.
type fileTable struct {
	// ParentURL is the ".." row; empty at a ref root, which has no level to go up to.
	ParentURL string
	Rows      []fileRow
}

// newFileTable resolves a directory's entries into rows: directories first, then by name.
//
// The order is the Astro component's `localeCompare(name, 'en', {sensitivity: 'base'})` reduced
// to a case-insensitive code-point compare on a stable sort. A real locale collation reads the
// build machine's ICU data, and two machines would then emit different HTML from one artifact.
func newFileTable(b *Builder, repo *model.Repo, refName string, entries []model.TreeEntry, parentURL string) fileTable {
	rows := make([]fileRow, 0, len(entries))
	for _, e := range entries {
		row := fileRow{Name: e.Name, IsDir: e.Type == "tree"}
		switch {
		case e.Type == "commit":
			// A submodule is a pointer at another repository; there is nothing here to open.
			row.Why = "submodule"
		case !routes.IsRawServable(e.Path):
			row.Why = "unservable"
			row.Title = "'" + e.Name + "' contains a character (# or %) that a static URL cannot round-trip, so it has no page here."
		case row.IsDir:
			row.URL = b.Router.TreeURL(repo.Slug, refName, e.Path)
		default:
			row.URL = b.Router.BlobURL(repo.Slug, refName, e.Path)
		}
		if c := repo.CommitFor(e.LastCommit); c != nil {
			row.CommitURL = b.Router.CommitURL(repo.Slug, c.Sha)
			row.Subject = c.Subject
			row.Date = c.CommitDate
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].IsDir != rows[j].IsDir {
			return rows[i].IsDir
		}
		return strings.ToLower(rows[i].Name) < strings.ToLower(rows[j].Name)
	})
	return fileTable{ParentURL: parentURL, Rows: rows}
}

/* ---- the ref switcher ---------------------------------------------------- */

// refOption is one ref in the switcher popup.
type refOption struct {
	Name      string
	URL       string
	IsCurrent bool
	IsDefault bool
}

// refSwitcher is the payload of the ref-switcher partial.
type refSwitcher struct {
	// Kind and Name describe the ref being browsed — the summary button's icon and label.
	Kind     string
	Name     string
	Branches []refOption
	Tags     []refOption
}

// refDirs is one browsable ref plus the set of directory paths it holds.
type refDirs struct {
	Ref  routes.BrowsableRef
	Dirs map[string]bool
}

// browsableDirs indexes every ref's directories once per repo.
//
// The switcher offers each ref the SAME directory when that ref has it, which the Astro
// component answered with `ref.tree.some(…)` — a scan of every other ref's entire tree, on
// every directory page. One map lookup per ref instead.
func browsableDirs(repo *model.Repo) []refDirs {
	refs := routes.BrowsableRefs(repo)
	out := make([]refDirs, 0, len(refs))
	for _, ref := range refs {
		dirs := make(map[string]bool)
		for _, e := range ref.Tree {
			if e.Type == "tree" {
				dirs[e.Path] = true
			}
		}
		out = append(out, refDirs{Ref: ref, Dirs: dirs})
	}
	return out
}

func newRefSwitcher(r routes.Router, slug string, refs []refDirs, current routes.BrowsableRef, dirPath string) refSwitcher {
	sw := refSwitcher{Kind: current.Kind, Name: current.Name}
	for _, rd := range refs {
		target := ""
		if dirPath != "" && rd.Dirs[dirPath] {
			target = dirPath
		}
		opt := refOption{
			Name:      rd.Ref.Name,
			URL:       r.TreeURL(slug, rd.Ref.Name, target),
			IsCurrent: rd.Ref.Name == current.Name,
			IsDefault: rd.Ref.IsDefault,
		}
		if rd.Ref.Kind == "tag" {
			sw.Tags = append(sw.Tags, opt)
		} else {
			sw.Branches = append(sw.Branches, opt)
		}
	}
	return sw
}

/* ---- blob ---------------------------------------------------------------- */

// The two caps a blob page will not highlight past. Beyond either, the file is still served in
// full — as plain text with a line saying why the colours are missing.
const (
	highlightMaxBytes = 500 * 1024
	highlightMaxLines = 5000
)

// imageExtRe are the extensions rendered as a picture rather than as text. SVG is in here and
// is not binary: the extension decides, not the content.
var imageExtRe = regexp.MustCompile(`(?i)\.(png|jpe?g|gif|webp|svg|ico)$`)

// blobPage is one file of one ref.
//
// Mode picks the body, mirroring the Astro page's chain of ternaries: markdown (with the
// preview/source toggle), symlink, image, code, plain (too big to highlight) or empty (binary,
// or over the ingest cap so nothing was stored).
type blobPage struct {
	Head     repoSubHead
	RepoName string
	RootURL  string
	Crumbs   []crumb
	FileName string
	FilePath string
	RefName  string
	RefIsTag bool

	Mode string

	HasLines bool
	Lines    int64
	Size     int64
	Language string
	TooLarge bool
	Symlink  bool
	Binary   bool

	// RawURL is empty for a file whose content this build does not hold, which is also what
	// removes the Raw and Download buttons.
	RawURL string

	Preview template.HTML
	Source  template.HTML
	// HasSource is false when the markdown was too big to highlight; the toggle then
	// disappears and the source view falls back to plain text.
	HasSource bool

	Code      template.HTML
	PlainText string
	SkipNote  string

	SymlinkTarget string
	SymlinkURL    string
}

func emitBlobPages(b *Builder, repo *model.Repo, head repoSubHead) error {
	trusted := markdown.IsTrustedSource(repo.Source)
	mermaid := b.Router.WithBase("/js/mermaid.js")
	copyJS := b.Router.WithBase("/js/copy.js")

	// Per-ref lookups are built for every ref BEFORE any page renders, so the pages can then run
	// concurrently over immutable state. Blob pages are the largest family in the build — 450 of
	// this project's own 1,205 files — and they are perfectly independent of one another, which
	// is what makes this the place parallelism actually pays.
	routeList := b.Router.BlobRoutes(repo)
	byRef := make(map[string]*treeTypes)
	for _, route := range routeList {
		if _, ok := byRef[route.Ref.Name]; !ok {
			byRef[route.Ref.Name] = newTreeTypes(route.Ref.Tree)
		}
	}

	// One parallel section per ref rather than one over the whole list. The pages are identical
	// either way — each writes its own path — but a section is the only honest unit to put a
	// wall-clock number on, and "ref main took 12s" is the question this file gets asked. A repo
	// with a single browsable ref (most of them) takes exactly the path it took before.
	for _, seg := range fileSegments(routeList) {
		base, n := seg[0], seg[1]-seg[0]
		s := b.step.Child("build.ref", refName(repo.Slug, routeList[base].Ref.Name))
		err := b.EachRoute(n,
			func(i int) string {
				return "blob " + routeList[base+i].Entry.Path + " at " + routeList[base+i].Ref.Name
			},
			func(local *Builder, i int) error {
				route := routeList[base+i]
				payload, err := newBlobPage(local, repo, route, head, trusted, byRef[route.Ref.Name])
				if err != nil {
					return err
				}
				return writeBlobPage(local, repo, route, payload, mermaid, copyJS)
			})
		s.Fail(err).DoneWith(timings.Counts{"pages": int64(n)})
		if err != nil {
			return err
		}
	}
	return nil
}

// writeBlobPage wraps one blob payload in a page and writes it.
//
// Split out of the loop when blob pages became concurrent: the per-page work has to be a
// function a worker can call, and separating "build the payload" from "decide the page chrome"
// keeps the concurrency in one place instead of threaded through the payload builder.
func writeBlobPage(b *Builder, repo *model.Repo, route routes.FileRoute, payload blobPage, mermaid, copyJS string) error {
	page := render.Page{
		Title:       route.Entry.Path + " · " + repo.Name + " at " + route.Ref.Name,
		Description: deref(repo.Description),
		Active:      "repos",
		Payload:     payload,
	}
	// Mermaid first, then the copy button's script — the order the Astro build emitted them in,
	// and the parity harness compares script order like any other element order.
	if markdown.ContainsMermaid(string(payload.Preview)) {
		page.ExtraScripts = append(page.ExtraScripts, mermaid)
	}
	page.ExtraScripts = append(page.ExtraScripts, copyJS)
	return b.WritePage(route.URL, "page-blob", page)
}

// treeTypes answers "what kind of entry is at this path" for one ref, indexing the tree the
// first time it is asked. Only a symlink ever asks — most repos have none, and this is a map of
// the whole tree, so building it eagerly on every ref would be paid for by nobody.
type treeTypes struct {
	types map[string]string
}

// newTreeTypes builds the lookup eagerly.
//
// It used to fill itself on first use, which was fine while one goroutine walked one ref at a
// time. Blob pages now render concurrently, and a lazily-populated shared map is a data race —
// so the map is built once, up front, and is read-only from then on.
func newTreeTypes(tree []model.TreeEntry) *treeTypes {
	types := make(map[string]string, len(tree))
	for _, e := range tree {
		types[e.Path] = e.Type
	}
	return &treeTypes{types: types}
}

func (t *treeTypes) typeOf(path string) string { return t.types[path] }

func newBlobPage(b *Builder, repo *model.Repo, route routes.FileRoute, head repoSubHead, trusted bool, types *treeTypes) (blobPage, error) {
	ref, entry := route.Ref, route.Entry
	info, hasInfo := ref.Files[entry.Path]

	segments := strings.Split(entry.Path, "/")
	dirs := segments[:len(segments)-1]
	crumbs := make([]crumb, 0, len(dirs))
	for i, name := range dirs {
		crumbs = append(crumbs, crumb{Name: name, URL: b.Router.TreeURL(repo.Slug, ref.Name, strings.Join(dirs[:i+1], "/"))})
	}

	p := blobPage{
		Head:     head.forPage("code", entry.Path+" · "+repo.Name+" at "+ref.Name, false),
		RepoName: repo.Name,
		RootURL:  b.Router.TreeURL(repo.Slug, ref.Name, ""),
		Crumbs:   crumbs,
		FileName: segments[len(segments)-1],
		FilePath: entry.Path,
		RefName:  ref.Name,
		RefIsTag: ref.Kind == "tag",
		Symlink:  entry.Type == "symlink",
	}
	if entry.Size != nil {
		p.Size = *entry.Size
	}
	if hasInfo {
		p.Size = info.Size
		p.Binary = info.Binary
		p.TooLarge = info.TooLarge
		p.Language = deref(info.Language)
		if info.Stored {
			p.RawURL = b.Router.RawURL(repo.Slug, ref.Name, entry.Path)
		}
	}
	stored := hasInfo && info.Stored

	if p.Symlink {
		p.Mode = "symlink"
		if stored {
			// A symlink's blob IS its target path.
			content, err := b.Blob(info.Sha)
			if err != nil {
				return p, err
			}
			target := strings.TrimSpace(string(content))
			p.SymlinkTarget = target
			// A target that is not in this ref's tree gets no link: it points outside the
			// repository, or at something this build did not keep.
			switch t := types.typeOf(target); {
			case target == "":
			case t == "tree":
				p.SymlinkURL = b.Router.TreeURL(repo.Slug, ref.Name, target)
			case t != "":
				p.SymlinkURL = b.Router.BlobURL(repo.Slug, ref.Name, target)
			}
		}
		return p, nil
	}

	// The line count belongs to the FILE, not to the body that ends up being shown. An SVG is
	// an image by extension and text by content, and the Astro page counted its lines in the
	// meta row before ever choosing a body — so the count is taken here, above the image and
	// empty branches, or `logo.svg` loses its "56 lines ·" while every other text file keeps it.
	var text string
	if stored && !p.Binary {
		content, err := b.Blob(info.Sha)
		if err != nil {
			return p, err
		}
		text = string(content)
		p.HasLines = true
		p.Lines = int64(highlight.CountLines(text))
	}

	if stored && imageExtRe.MatchString(entry.Path) {
		p.Mode = "image"
		return p, nil
	}
	if !stored || p.Binary {
		// Nothing to show: either it is binary, or ingest never stored it (over the cap).
		p.Mode = "empty"
		return p, nil
	}

	tooBig := p.Size > highlightMaxBytes || p.Lines > highlightMaxLines

	if markdown.IsMarkdownPath(entry.Path) {
		p.Mode = "markdown"
		p.Preview = htmlOf(b.Markdown(text, trusted))
		if !tooBig {
			p.HasSource = true
			p.Source = htmlOf(highlight.Highlight(text, "Markdown", entry.Path, ""))
		} else {
			p.PlainText = text
		}
		return p, nil
	}
	if tooBig {
		p.Mode = "plain"
		p.PlainText = text
		p.SkipNote = fmt.Sprintf("Syntax highlighting skipped — file exceeds %s lines / %s.",
			render.FormatInt(highlightMaxLines), render.FormatBytes(highlightMaxBytes))
		return p, nil
	}
	p.Mode = "code"
	p.Code = htmlOf(highlight.Highlight(text, p.Language, entry.Path, ""))
	return p, nil
}

/* ---- raw ----------------------------------------------------------------- */

// emitRawFiles writes the committed bytes of every stored blob at its raw URL.
//
// No template and no transformation: the raw URL exists so that what a browser downloads is
// byte-for-byte what was committed, under the name it was committed as. The content type is
// the static host's to decide — a directory of files has no headers of its own.
func emitRawFiles(b *Builder, repo *model.Repo) error {
	// Pure copies with no shared state — the easiest work in the build to spread out, and 439
	// files of it on this project's own site.
	routeList := b.Router.RawRoutes(repo)
	// Per ref, for the reason emitBlobPages gives.
	for _, seg := range fileSegments(routeList) {
		base, n := seg[0], seg[1]-seg[0]
		s := b.step.Child("build.ref", refName(repo.Slug, routeList[base].Ref.Name))
		err := b.EachRoute(n,
			func(i int) string {
				return "raw " + routeList[base+i].Entry.Path + " at " + routeList[base+i].Ref.Name
			},
			func(local *Builder, i int) error {
				route := routeList[base+i]
				info := route.Ref.Files[route.Entry.Path]
				content, err := local.Blob(info.Sha)
				if err != nil {
					return err
				}
				return local.WriteFile(route.URL, content)
			})
		s.Fail(err).DoneWith(timings.Counts{"pages": int64(n)})
		if err != nil {
			return err
		}
	}
	return nil
}
