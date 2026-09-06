// Package routes derives every URL the site emits from the artifact — the port of
// src/lib/routes.ts.
//
// The renderer calls these to build pages, and the sync tests call the same functions to
// assert "everything in the artifact has a page" without running a build. One implementation
// for both is the point: a URL builder that only the renderer used could disagree with the
// test that is supposed to police it.
//
// Ref names appear in URLs as a "ref slug": '/' → '~' (a git refname can never contain '~'),
// so `feat/zip` browses at /repos/<slug>/tree/feat~zip/.
//
// Every URL a builder returns is prefixed with the deploy base (site.base), which is why they
// hang off a Router rather than being package functions: the TypeScript original read a
// module-level value that tests had to mock, and a value threaded through explicitly cannot be
// stale.
package routes

import (
	"sort"
	"strings"

	"frznforge/internal/model"
)

// COMMITS_PER_PAGE is how many commits one paginated commit-list page holds.
const CommitsPerPage = 50

// Router builds site URLs under a deploy base.
type Router struct {
	// Base is '' for a root deploy, or '/prefix' with no trailing slash.
	Base string
}

// WithBase prefixes a root-relative path with the deploy base.
func (r Router) WithBase(path string) string { return r.Base + path }

/* ---- encoding ------------------------------------------------------------ */

// unreservedURIComponent is exactly the set JavaScript's encodeURIComponent leaves alone:
// A-Z a-z 0-9 and - _ . ! ~ * ' ( ).
//
// Go's url.PathEscape is NOT the same function — it leaves $ & + , : ; = @ unescaped — so
// using it here would emit different URLs than the TypeScript build for any path containing
// one of those, and those are all legal in a git path. This table is the difference between
// the two renderers agreeing and disagreeing on real repositories.
func unreservedURIComponent(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return true
	case c == '-', c == '_', c == '.', c == '!', c == '~', c == '*', c == '\'', c == '(', c == ')':
		return true
	}
	return false
}

const upperHex = "0123456789ABCDEF"

// EncodeURIComponent matches JavaScript's encodeURIComponent, byte for byte: percent-encoding
// is applied to UTF-8 bytes and the hex digits are uppercase.
func EncodeURIComponent(s string) string {
	needed := false
	for i := 0; i < len(s); i++ {
		if !unreservedURIComponent(s[i]) {
			needed = true
			break
		}
	}
	if !needed {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if unreservedURIComponent(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(upperHex[c>>4])
		b.WriteByte(upperHex[c&0x0F])
	}
	return b.String()
}

// EncodePathSegments percent-encodes each segment of a path, keeping `/` literal so the
// directory structure survives.
//
// Repo paths and ref names are whatever was committed, not slugs: spaces, &, +, ;, # and
// non-ASCII are all legal in a git path, and a raw interpolation produces either an invalid
// href or a build abort. See IsRawServable for the two characters encoding cannot rescue.
func EncodePathSegments(path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		parts[i] = EncodeURIComponent(p)
	}
	return strings.Join(parts, "/")
}

// RefSlug turns a ref name into its URL form.
func RefSlug(refName string) string { return strings.ReplaceAll(refName, "/", "~") }

// RefFromSlug reverses RefSlug.
func RefFromSlug(slug string) string { return strings.ReplaceAll(slug, "~", "/") }

// IsRawServable reports whether a path can appear in a static URL at all.
//
// Two characters cannot round-trip through a static build, however they are encoded:
//
//   - '#' — the build escapes it in the OUTPUT FILENAME (c%23-tips.md lands on disk with a
//     literal %23), so the encoded URL decodes to a name that is not there;
//   - '%' — the generated path is re-encoded and then decoded, and "50%%20off.txt" is not
//     valid percent-encoding, which aborts the whole build.
//
// Rather than emit a dead link (or fail), such a file is published without a route: it still
// renders inline, and ingest raises note-file-unservable / repo-path-unservable.
func IsRawServable(filePath string) bool {
	return !strings.ContainsAny(filePath, "#%")
}

/* ---- site-wide ----------------------------------------------------------- */

func (r Router) HomeURL() string        { return r.WithBase("/") }
func (r Router) ReposURL() string       { return r.WithBase("/repos/") }
func (r Router) NotesIndexURL() string  { return r.WithBase("/notes/") }
func (r Router) OrgsIndexURL() string   { return r.WithBase("/orgs/") }
func (r Router) NotFoundURL() string    { return r.WithBase("/404") }
func (r Router) SearchIndexURL() string { return r.WithBase("/search-index.json") }

func (r Router) RepoURL(slug string) string { return r.WithBase("/repos/" + slug + "/") }
func (r Router) NoteURL(slug string) string { return r.WithBase("/notes/" + slug + "/") }
func (r Router) OrgURL(slug string) string  { return r.WithBase("/orgs/" + slug + "/") }

// OrgReposURL is the full repo listing scoped to one organization.
func (r Router) OrgReposURL(slug string) string { return r.WithBase("/orgs/" + slug + "/repos/") }

// NoteRawURL is the raw bytes of one file in a note. Each segment is percent-encoded, because
// a note file name is authored by hand and may hold spaces, &, + or non-ASCII; directory
// separators stay literal so the URL keeps the note's folder structure. No trailing slash,
// matching RawURL for repo files.
func (r Router) NoteRawURL(slug, filePath string) string {
	return r.WithBase("/notes/" + slug + "/raw/" + EncodePathSegments(filePath))
}

/* ---- repo sub-pages ------------------------------------------------------ */

func (r Router) TreeURL(slug, ref, path string) string {
	tail := ""
	if path != "" {
		tail = EncodePathSegments(path) + "/"
	}
	return r.WithBase("/repos/" + slug + "/tree/" + EncodePathSegments(RefSlug(ref)) + "/" + tail)
}

func (r Router) BlobURL(slug, ref, path string) string {
	return r.WithBase("/repos/" + slug + "/blob/" + EncodePathSegments(RefSlug(ref)) + "/" + EncodePathSegments(path) + "/")
}

func (r Router) RawURL(slug, ref, path string) string {
	return r.WithBase("/repos/" + slug + "/raw/" + EncodePathSegments(RefSlug(ref)) + "/" + EncodePathSegments(path))
}

func (r Router) CommitsURL(slug, ref string, page int) string {
	tail := ""
	if page > 1 {
		tail = "page/" + itoa(page) + "/"
	}
	return r.WithBase("/repos/" + slug + "/commits/" + RefSlug(ref) + "/" + tail)
}

func (r Router) CommitURL(slug, sha string) string {
	return r.WithBase("/repos/" + slug + "/commit/" + sha + "/")
}
func (r Router) BranchesURL(slug string) string { return r.WithBase("/repos/" + slug + "/branches/") }
func (r Router) TagsURL(slug string) string     { return r.WithBase("/repos/" + slug + "/tags/") }
func (r Router) ReleasesURL(slug string) string { return r.WithBase("/repos/" + slug + "/releases/") }
func (r Router) InsightsURL(slug string) string { return r.WithBase("/repos/" + slug + "/insights/") }

func (r Router) ReleaseURL(slug, tag string) string {
	return r.WithBase("/repos/" + slug + "/releases/" + RefSlug(tag) + "/")
}

func (r Router) ArchiveURL(slug, ref string) string {
	return r.WithBase("/repos/" + slug + "/archive/" + RefSlug(ref) + ".zip")
}

// HostedURL is the root URL of a hosted site. Its index.html is emitted at <slug>/index.html,
// so the directory URL resolves through the same directory-index handling every static host
// does — this is the link to hand a human.
func (r Router) HostedURL(slug string) string { return r.WithBase("/" + slug + "/") }

/* ---- browsable refs ------------------------------------------------------ */

// BrowsableRef is a ref that has a tree in the artifact.
type BrowsableRef struct {
	Name      string
	Slugged   string
	Kind      string // "branch" | "tag"
	IsDefault bool
	Commit    string
	Tree      []model.TreeEntry
	Files     map[string]model.FileInfo
}

// BrowsableRefs lists every ref with a browsable tree: the default branch first, then the ref
// trees (branches, then tags), each group in code-point name order.
func BrowsableRefs(repo *model.Repo) []BrowsableRef {
	out := make([]BrowsableRef, 0, 1+len(repo.RefTrees))
	if repo.DefaultBranch != nil && *repo.DefaultBranch != "" {
		name := *repo.DefaultBranch
		head := ""
		for _, b := range repo.Branches {
			if b.Name == name {
				head = b.Head
				break
			}
		}
		out = append(out, BrowsableRef{
			Name: name, Slugged: RefSlug(name), Kind: "branch", IsDefault: true,
			Commit: head, Tree: repo.Tree, Files: repo.Files,
		})
	}
	rest := make([]model.RefTree, 0, len(repo.RefTrees))
	for _, rt := range repo.RefTrees {
		rest = append(rest, rt)
	}
	// Branches before tags, then by name. Code-point order, never a locale-aware compare:
	// localeCompare depends on the build machine's ICU data, so two machines would emit
	// different HTML from the same artifact.
	sort.SliceStable(rest, func(i, j int) bool {
		ti, tj := boolToInt(rest[i].Kind == "tag"), boolToInt(rest[j].Kind == "tag")
		if ti != tj {
			return ti < tj
		}
		return rest[i].Name < rest[j].Name
	})
	for _, rt := range rest {
		out = append(out, BrowsableRef{
			Name: rt.Name, Slugged: RefSlug(rt.Name), Kind: rt.Kind, IsDefault: false,
			Commit: rt.Commit, Tree: rt.Tree, Files: rt.Files,
		})
	}
	return out
}

// FindRef resolves a slugged ref back to its browsable ref, or nil.
func FindRef(repo *model.Repo, sluggedRef string) *BrowsableRef {
	name := RefFromSlug(sluggedRef)
	for _, ref := range BrowsableRefs(repo) {
		if ref.Slugged == sluggedRef || ref.Name == name {
			return &ref
		}
	}
	return nil
}

/* ---- insights ------------------------------------------------------------ */

// HasInsights reports whether a repo has an insights page — and therefore an Insights tab.
//
// One helper so the page, the route list, the sync test and the repo header cannot drift
// apart: a tab without a page is a dead link, a page without a tab is unreachable.
func HasInsights(repo *model.Repo) bool {
	return !repo.Empty && repo.Insights != nil && len(repo.Insights.Commits) > 0
}

/* ---- releases ------------------------------------------------------------ */

// SiteRelease is one release as the site renders it, from either origin.
type SiteRelease struct {
	// Tag is the tag name — also the URL segment.
	Tag  string
	Name string
	// Body is markdown: the provider's release notes, or the annotated tag message.
	Body string
	// URL is the release page on the provider; always empty for tag-derived releases.
	URL        string
	Prerelease bool
	// Date is the publish date (provider) or tag date, ISO UTC.
	Date   string
	Assets []model.ReleaseAsset
	// Source is "provider" or "tag".
	Source string
	// Commit the tag points at, or empty when a provider release names a tag this mirror
	// does not have.
	Commit string
}

// ReleasesOf returns annotated tags, newest first — the tag-mode release list.
//
// Prefer ResolveReleases: this only sees git tags and so misses provider-imported releases.
// Kept because the tags page and the tag-mode pages read it directly.
func ReleasesOf(repo *model.Repo) []model.Tag {
	out := make([]model.Tag, 0, len(repo.GitTags))
	for _, t := range repo.GitTags {
		if t.Annotated {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date > out[j].Date
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// ResolveReleases is the release list for a repo, newest first (date desc, tag asc).
//
// Provider-imported releases win when there are any; otherwise this falls back to the
// annotated-tag derivation, so a repo that switches releaseMode — or a remote whose releases
// could not be fetched this build — still renders.
func ResolveReleases(repo *model.Repo) []SiteRelease {
	var out []SiteRelease
	if len(repo.Releases) > 0 {
		for _, rel := range repo.Releases {
			commit := ""
			for _, t := range repo.GitTags {
				if t.Name == rel.Tag {
					commit = t.Target
					break
				}
			}
			out = append(out, SiteRelease{
				Tag: rel.Tag, Name: rel.Name, Body: rel.Body, URL: deref(rel.URL),
				Prerelease: rel.Prerelease, Date: rel.PublishedAt, Assets: rel.Assets,
				Source: "provider", Commit: commit,
			})
		}
	} else {
		for _, t := range ReleasesOf(repo) {
			out = append(out, SiteRelease{
				Tag: t.Name, Name: t.Name, Body: deref(t.Message), URL: "",
				Prerelease: false, Date: t.Date, Assets: []model.ReleaseAsset{},
				Source: "tag", Commit: t.Target,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date > out[j].Date
		}
		return out[i].Tag < out[j].Tag
	})
	return out
}

/* ---- route enumeration --------------------------------------------------- */

// TreeRoute is one directory page.
type TreeRoute struct {
	Ref  BrowsableRef
	Path string
	URL  string
}

// TreeRoutes lists every directory route for a repo: one per ref root plus one per tree entry.
// Paths and refs no static URL can round-trip are skipped (IsRawServable); ingest has already
// raised repo-path-unservable for them.
func (r Router) TreeRoutes(repo *model.Repo) []TreeRoute {
	var out []TreeRoute
	for _, ref := range BrowsableRefs(repo) {
		if !IsRawServable(ref.Slugged) {
			continue
		}
		out = append(out, TreeRoute{Ref: ref, Path: "", URL: r.TreeURL(repo.Slug, ref.Name, "")})
		for _, e := range ref.Tree {
			if e.Type == "tree" && IsRawServable(e.Path) {
				out = append(out, TreeRoute{Ref: ref, Path: e.Path, URL: r.TreeURL(repo.Slug, ref.Name, e.Path)})
			}
		}
	}
	return out
}

// FileRoute is one file page (blob or raw).
type FileRoute struct {
	Ref   BrowsableRef
	Entry model.TreeEntry
	URL   string
}

// BlobRoutes lists every file page. Submodules are excluded; symlinks get a page.
func (r Router) BlobRoutes(repo *model.Repo) []FileRoute {
	var out []FileRoute
	for _, ref := range BrowsableRefs(repo) {
		if !IsRawServable(ref.Slugged) {
			continue
		}
		for _, e := range ref.Tree {
			if (e.Type == "blob" || e.Type == "symlink") && IsRawServable(e.Path) {
				out = append(out, FileRoute{Ref: ref, Entry: e, URL: r.BlobURL(repo.Slug, ref.Name, e.Path)})
			}
		}
	}
	return out
}

// RawRoutes exist only for stored blobs — content actually present in the blob store. Derived
// from BlobRoutes, so it inherits the unservable-path exclusion.
func (r Router) RawRoutes(repo *model.Repo) []FileRoute {
	var out []FileRoute
	for _, br := range r.BlobRoutes(repo) {
		if info, ok := br.Ref.Files[br.Entry.Path]; ok && info.Stored {
			out = append(out, FileRoute{Ref: br.Ref, Entry: br.Entry, URL: r.RawURL(repo.Slug, br.Ref.Name, br.Entry.Path)})
		}
	}
	return out
}

// CommitsPageCount is how many paginated commit-list pages a branch needs (at least one).
func CommitsPageCount(repo *model.Repo, refName string) int {
	count := 0
	for _, b := range repo.Branches {
		if b.Name == refName {
			count = len(b.Commits)
			break
		}
	}
	pages := (count + CommitsPerPage - 1) / CommitsPerPage
	if pages < 1 {
		return 1
	}
	return pages
}

// HostedFileRoute is one file served by a hosted site.
type HostedFileRoute struct {
	Site model.HostedSite
	Path string
	Sha  string
	URL  string
}

// HostedFiles lists every servable file of every hosted site, in artifact order (sites by
// slug, files by path). Each file is emitted at its LITERAL path under /<slug>/, so /<slug>/
// works through the same directory-index resolution every static host already does.
func (r Router) HostedFiles(data *model.ForgeData) []HostedFileRoute {
	bySlug := make(map[string]*model.Repo, len(data.Repos))
	for i := range data.Repos {
		bySlug[data.Repos[i].Slug] = &data.Repos[i]
	}
	var out []HostedFileRoute
	for _, site := range data.Hosting {
		repo, ok := bySlug[site.Repo]
		if !ok {
			// The resolver only records existing repos; never crash over a hand-edited artifact.
			continue
		}
		files := repo.Files
		if repo.DefaultBranch == nil || site.Ref != *repo.DefaultBranch {
			if rt, ok := repo.RefTrees[site.Ref]; ok {
				files = rt.Files
			} else {
				files = map[string]model.FileInfo{}
			}
		}
		paths := make([]string, 0, len(files))
		for p := range files {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			info := files[p]
			if !info.Stored || !IsRawServable(p) {
				continue
			}
			out = append(out, HostedFileRoute{
				Site: site, Path: p, Sha: info.Sha,
				URL: r.WithBase("/" + site.Slug + "/" + EncodePathSegments(p)),
			})
		}
	}
	return out
}

// HostedSitesFor returns the hosted sites served from a repo, in artifact order.
func HostedSitesFor(data *model.ForgeData, repoSlug string) []model.HostedSite {
	var out []model.HostedSite
	for _, h := range data.Hosting {
		if h.Repo == repoSlug {
			out = append(out, h)
		}
	}
	return out
}

// ReposInOrg lists an organization's member repos, in Organization.Repos order (slug asc).
// Slugs with no matching repo are dropped — ingest already warned (org-unknown-repo), and a
// page must not blow up over a stale config entry.
func ReposInOrg(data *model.ForgeData, org model.Organization) []*model.Repo {
	bySlug := make(map[string]*model.Repo, len(data.Repos))
	for i := range data.Repos {
		bySlug[data.Repos[i].Slug] = &data.Repos[i]
	}
	var out []*model.Repo
	for _, slug := range org.Repos {
		if r, ok := bySlug[slug]; ok {
			out = append(out, r)
		}
	}
	return out
}

// RepoRoutes lists every static URL the site emits for one repo.
func (r Router) RepoRoutes(repo *model.Repo) []string {
	urls := []string{r.RepoURL(repo.Slug)}
	if repo.Empty {
		return urls
	}
	urls = append(urls, r.BranchesURL(repo.Slug), r.TagsURL(repo.Slug), r.ReleasesURL(repo.Slug))
	if HasInsights(repo) {
		urls = append(urls, r.InsightsURL(repo.Slug))
	}
	for _, t := range r.TreeRoutes(repo) {
		urls = append(urls, t.URL)
	}
	for _, b := range r.BlobRoutes(repo) {
		urls = append(urls, b.URL)
	}
	for _, raw := range r.RawRoutes(repo) {
		urls = append(urls, raw.URL)
	}
	for _, b := range repo.Branches {
		for p := 1; p <= CommitsPageCount(repo, b.Name); p++ {
			urls = append(urls, r.CommitsURL(repo.Slug, b.Name, p))
		}
	}
	for _, sha := range sortedKeys(repo.Commits) {
		urls = append(urls, r.CommitURL(repo.Slug, sha))
	}
	// Display-support commits (schema v6) get pages too — file tables and tag rows link to
	// them. Disjoint from Commits by construction, so no dedupe is needed.
	for _, sha := range sortedKeys(repo.ExtraCommits) {
		urls = append(urls, r.CommitURL(repo.Slug, sha))
	}
	// One page per release. Deduped: a provider may report two releases against the same tag.
	seen := map[string]bool{}
	for _, rel := range ResolveReleases(repo) {
		u := r.ReleaseURL(repo.Slug, rel.Tag)
		if !seen[u] {
			seen[u] = true
			urls = append(urls, u)
		}
	}
	for _, a := range repo.Archives {
		urls = append(urls, r.ArchiveURL(repo.Slug, a.Ref))
	}
	return urls
}

// NotesRoutes lists every static URL the site emits for notes: the index, each note, and each
// stored file's raw URL.
func (r Router) NotesRoutes(data *model.ForgeData) []string {
	urls := []string{r.NotesIndexURL()}
	for _, n := range data.Notes {
		urls = append(urls, r.NoteURL(n.Slug))
	}
	for _, n := range data.Notes {
		for _, f := range n.Files {
			if f.Stored && IsRawServable(f.Path) {
				urls = append(urls, r.NoteRawURL(n.Slug, f.Path))
			}
		}
	}
	return urls
}

// OrgRoutes lists every static URL the site emits for organizations.
func (r Router) OrgRoutes(data *model.ForgeData) []string {
	urls := []string{r.OrgsIndexURL()}
	for _, o := range data.Organizations {
		urls = append(urls, r.OrgURL(o.Slug), r.OrgReposURL(o.Slug))
	}
	return urls
}

// AllRoutes is every static URL the site emits.
//
// The notes and orgs index pages are included unconditionally — like /repos/, they exist (and
// say "nothing here yet") even when the artifact has none. Only the sidebar hides them at
// count 0.
func (r Router) AllRoutes(data *model.ForgeData) []string {
	urls := []string{r.HomeURL(), r.ReposURL(), r.NotFoundURL()}
	for i := range data.Repos {
		urls = append(urls, r.RepoRoutes(&data.Repos[i])...)
	}
	urls = append(urls, r.NotesRoutes(data)...)
	urls = append(urls, r.OrgRoutes(data)...)
	for _, f := range r.HostedFiles(data) {
		urls = append(urls, f.URL)
	}
	return urls
}

// UnmatchedOrgContent lists ids in the orgs content directory that no configured organization
// claims, sorted.
//
// An orgs markdown file is only ever reached through an organization's slug, so one typo in
// the filename silently discards the whole file — prose, links, pinned repos — with no page
// and no other symptom. Ingest cannot catch it (the directory is a site-build concern, not an
// artifact one), so /orgs/ reports it at build time instead.
func UnmatchedOrgContent(data *model.ForgeData, contentIDs []string) []string {
	configured := make(map[string]bool, len(data.Organizations))
	for _, o := range data.Organizations {
		configured[o.Slug] = true
	}
	var out []string
	for _, id := range contentIDs {
		if !configured[id] {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

/* ---- small helpers ------------------------------------------------------- */

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	if neg {
		return "-" + string(d)
	}
	return string(d)
}
