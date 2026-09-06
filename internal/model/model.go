// Package model is the frznforge data model — the contract between ingest (git → JSON) and
// the site, ported from src/lib/data/schema.ts.
//
// Everything the site renders comes from a ForgeData artifact written by ingest to
// <ingest.outDir>/forge.json plus a content-addressed blob store in
// <ingest.outDir>/blobs/<sha> for file contents (text files under the size cap).
//
// Rules, inherited unchanged from the TypeScript schema:
//   - Any change to this file bumps SchemaVersion, updates docs/dev/data-model.md, and updates
//     the snapshot fixtures + sync tests in the same change. 0.4.0 changes NOTHING here on
//     purpose: while the schema is frozen, "the Go ingest is correct" is a byte-identity
//     statement against the TypeScript one rather than a judgement call.
//   - The artifact is deterministic: same repos at the same commits → byte-identical JSON.
//     Nothing here is a wall-clock timestamp; all dates come from git.
//   - Only committed content is ever represented for repositories. Notes (schema v4) are the
//     one deliberate exception: they are a plain folder on disk, so reading them from disk IS
//     the source of truth.
//
// # Field order is part of the contract
//
// serializeForgeData writes JSON.stringify(data, null, 2), which emits object keys in
// insertion order — and the TypeScript code inserts in declaration order. encoding/json
// marshals struct fields in declaration order too, so the declarations below MUST stay in the
// order the artifact uses. TestRoundTrip is what actually holds this: reorder two fields and
// it fails on real bytes.
//
// # Optional versus nullable
//
// Two different things that both look like "missing" in Go and must not be confused:
//
//   - Zod `.optional()` — the key is ABSENT from the JSON (RepoLinks' four fields). Modelled
//     as a pointer WITH omitempty.
//   - Zod `.nullable()` — the key is PRESENT with value null (Repo.description, insights,
//     readme, …). Modelled as a pointer WITHOUT omitempty, because omitempty would drop the
//     key and change the bytes.
//
// Slices and maps are always emitted, even when empty, so they never carry omitempty — and
// they must never be nil when marshalling, since a nil slice marshals as `null` rather than
// `[]`. Decoding `[]` and `{}` yields non-nil empties, so a round-trip is safe; code that
// BUILDS an artifact has to initialise them (see model.EmptyForgeData).
package model

// SchemaVersion is the artifact version this build reads and writes.
const SchemaVersion = 8

/* ---- git objects ------------------------------------------------------- */

// Person is a git identity.
type Person struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// CommitFileChange is one file touched by a commit, with numstat line counts.
type CommitFileChange struct {
	// Path after the change (rename target).
	Path string `json:"path"`
	// Added lines; null for binary files.
	Additions *int64 `json:"additions"`
	// Deleted lines; null for binary files.
	Deletions *int64 `json:"deletions"`
}

// CommitStats sums over a commit's files (binary files count toward FilesChanged only).
type CommitStats struct {
	FilesChanged int64 `json:"filesChanged"`
	Additions    int64 `json:"additions"`
	Deletions    int64 `json:"deletions"`
}

// Commit is one git commit as the artifact stores it.
type Commit struct {
	Sha        string   `json:"sha"`
	Parents    []string `json:"parents"`
	Author     Person   `json:"author"`
	AuthorDate string   `json:"authorDate"`
	Committer  Person   `json:"committer"`
	CommitDate string   `json:"commitDate"`
	// Subject is the first line of the message.
	Subject string `json:"subject"`
	// Body is everything after the first blank line, trimmed. Empty string if none.
	Body  string             `json:"body"`
	Files []CommitFileChange `json:"files"`
	Stats CommitStats        `json:"stats"`
}

// Branch is a git branch and the commits reachable from its head.
type Branch struct {
	Name string `json:"name"`
	Head string `json:"head"`
	// Commits reachable from Head, newest first (topological, as `git log`).
	Commits        []string `json:"commits"`
	LastCommitDate string   `json:"lastCommitDate"`
}

// Tag is a git tag, annotated or lightweight.
type Tag struct {
	Name string `json:"name"`
	// Target is the commit the tag points at (peeled).
	Target    string `json:"target"`
	Annotated bool   `json:"annotated"`
	// Message is the annotated tag message (trimmed), or null for lightweight tags.
	Message *string `json:"message"`
	Tagger  *Person `json:"tagger"`
	// Date is the tagger date for annotated tags, else the target commit's commit date.
	Date string `json:"date"`
}

// TreeEntry is one entry in a flattened repository tree.
type TreeEntry struct {
	// Path from repo root, forward slashes, no leading slash.
	Path string `json:"path"`
	Name string `json:"name"`
	// Type is one of blob, tree, commit (submodule), symlink.
	Type string `json:"type"`
	// Mode is the git mode string as reported by ls-tree, e.g. "100644".
	Mode string `json:"mode"`
	// Sha is the blob object id for blobs/symlinks, tree id for trees, commit id for submodules.
	Sha string `json:"sha"`
	// Size in bytes; null for trees and submodules.
	Size *int64 `json:"size"`
	// LastCommit is the most recent commit (on the default branch) that touched this path.
	LastCommit string `json:"lastCommit"`
}

// FileInfo is per-file detail for every blob in a tree.
type FileInfo struct {
	Path string `json:"path"`
	// Sha is the blob object id — also the blob store key when Stored is true.
	Sha    string `json:"sha"`
	Size   int64  `json:"size"`
	Binary bool   `json:"binary"`
	// TooLarge means over ingest.maxBlobBytes; content not stored.
	TooLarge bool `json:"tooLarge"`
	// Stored means content was written to <outDir>/blobs/<sha>.
	Stored bool `json:"stored"`
	// Language detected from the extension/filename map, or null.
	Language *string `json:"language"`
}

/* ---- repo metadata ----------------------------------------------------- */

// RepoLinks are the optional outbound links for a repo. Every field is zod `.optional()`, so
// an absent link is an ABSENT KEY — hence omitempty on all four.
type RepoLinks struct {
	Homepage  *string `json:"homepage,omitempty"`
	Issues    *string `json:"issues,omitempty"`
	Donations *string `json:"donations,omitempty"`
	Upstream  *string `json:"upstream,omitempty"`
}

// LanguageStat is one language's share of a repo.
type LanguageStat struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
	// Percent is 0–100, rounded to one decimal; sums to ~100.
	Percent float64 `json:"percent"`
	// Color is a hex colour for bars, from the language map (null → UI neutral).
	Color *string `json:"color"`
}

// Contributor is one person in a repo's history, optionally decorated from config (schema v8).
type Contributor struct {
	Name string `json:"name"`
	// Email is the canonical author email. For a configured contributor with several
	// addresses this is the FIRST one listed in the config; the others merge into this entry.
	Email       string  `json:"email"`
	Commits     int64   `json:"commits"`
	FirstCommit string  `json:"firstCommit"`
	LastCommit  string  `json:"lastCommit"`
	Avatar      *string `json:"avatar"`
	Description *string `json:"description"`
	URL         *string `json:"url"`
}

// License is a repo's detected or configured license.
type License struct {
	// Spdx id if known (e.g. "MIT"), else null.
	Spdx *string `json:"spdx"`
	// File is the license file path in the repo, if detected from a file.
	File *string `json:"file"`
	// Source is "config" or "file".
	Source string `json:"source"`
}

// Readme is the repo's readme, inlined because it renders on the overview.
type Readme struct {
	Path    string `json:"path"`
	Sha     string `json:"sha"`
	Content string `json:"content"`
}

/* ---- insights (schema v5) ----------------------------------------------- */

// CommitPoint is one month of commit activity on the default branch. Exact, not sampled.
type CommitPoint struct {
	// Month is a UTC bucket, YYYY-MM.
	Month   string `json:"month"`
	Commits int64  `json:"commits"`
	// Contributors is distinct author emails active that month — not a running total.
	Contributors int64 `json:"contributors"`
}

// CodeSizePoint is the size of tracked code at one monthly checkpoint.
//
// Lines is null when that checkpoint's text blobs exceeded ingest.insights.maxBytesPerSample;
// at such a checkpoint Bytes also loses its binary filter, so a null Lines marks a point whose
// byte total may be inflated too.
type CodeSizePoint struct {
	Month string `json:"month"`
	Bytes int64  `json:"bytes"`
	Lines *int64 `json:"lines"`
}

// RepoInsights are monthly series over the default branch's history, oldest first.
type RepoInsights struct {
	// Commits is contiguous: a month with no commits is emitted as zeros rather than omitted,
	// so a chart drawn straight from it shows a quiet period as quiet.
	Commits  []CommitPoint   `json:"commits"`
	CodeSize []CodeSizePoint `json:"codeSize"`
	// Sampled is true when CodeSize covers fewer checkpoints than there are months with commits.
	Sampled     bool  `json:"sampled"`
	SampleCount int64 `json:"sampleCount"`
	// Approximate is true when at least one checkpoint has a null Lines.
	Approximate bool `json:"approximate"`
}

/* ---- releases (schema v3) ----------------------------------------------- */

// ReleaseAsset is a downloadable file attached to a provider release.
//
// URL is a plain string, not a validated URL: some providers hand out host-relative paths.
// Nothing volatile lives here — download counters are deliberately NOT imported, because they
// would change the artifact between two builds of the same commits.
type ReleaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Size int64  `json:"size"`
	// ContentType reported by the provider, or null when it reports none.
	ContentType *string `json:"contentType"`
}

// Release is a release imported from a hosting provider (Repo.ReleaseMode == "provider").
type Release struct {
	Tag  string `json:"tag"`
	Name string `json:"name"`
	// Body is release notes as markdown. Empty string when there are none.
	Body string `json:"body"`
	// URL is the release page on the provider, or null.
	URL         *string `json:"url"`
	Prerelease  bool    `json:"prerelease"`
	PublishedAt string  `json:"publishedAt"`
	// Author is the provider username/display name of the publisher, or null.
	Author *string        `json:"author"`
	Assets []ReleaseAsset `json:"assets"`
}

/* ---- warnings ----------------------------------------------------------- */

// Warning is one thing ingest could not do as asked. Codes are listed in WarningCodes.
type Warning struct {
	Code string `json:"code"`
	// Repo slug, or null for site-level warnings.
	Repo    *string `json:"repo"`
	Message string  `json:"message"`
}

// WarningCodes is every code Warning.Code may take, mirroring the zod enum. Kept as data
// rather than as constants so validation can check membership in one place.
var WarningCodes = map[string]bool{
	"repo-empty": true, "default-branch-empty-tree": true, "default-branch-fallback": true,
	"repo-meta-invalid": true, "description-truncated": true, "repo-not-found": true,
	"slug-collision": true, "commits-capped": true, "commits-aged-out": true,
	"tag-trees-capped": true, "branch-trees-capped": true, "insights-approximate": true,
	"remote-fetch-failed": true, "remote-auth-missing": true, "remote-rate-limited": true,
	"remote-cache-stale": true, "note-slug-collision": true, "notes-dir-missing": true,
	"note-file-unservable": true, "repo-path-unservable": true, "org-unknown-repo": true,
	"contributor-unknown-email": true, "repo-unknown-org": true, "hosting-unknown-repo": true,
	"hosting-branch-missing": true, "hosting-file-unservable": true,
}

/* ---- per-ref trees & archives (schema v2) -------------------------------- */

// RefTree is the browsable tree of a non-default ref.
type RefTree struct {
	// Kind is "branch" or "tag".
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Commit the ref peels to.
	Commit string              `json:"commit"`
	Tree   []TreeEntry         `json:"tree"`
	Files  map[string]FileInfo `json:"files"`
}

// Archive is a source archive produced at ingest time with `git archive` (zip).
type Archive struct {
	Ref    string `json:"ref"`
	Kind   string `json:"kind"`
	Commit string `json:"commit"`
	// File is the path relative to the ingest outDir, e.g. "archives/<slug>/<ref-slug>.zip".
	File  string `json:"file"`
	Bytes int64  `json:"bytes"`
}

/* ---- repo --------------------------------------------------------------- */

// RepoSource is where a scanned repository came from.
//
// The TypeScript side is a discriminated union on `type`; here it is one struct whose fields
// are ordered so that each variant emits exactly the keys — and the key order — its union
// member does. `local` emits {type, path}; github/gitea/forgejo emit
// {type, host, owner, repo, webUrl, cloneUrl}; gitlab emits {type, host, project, webUrl,
// cloneUrl}, identifying a repo by its full namespaced path rather than owner + repo. Every
// variant-specific field is therefore optional-with-omitempty.
type RepoSource struct {
	Type     string  `json:"type"`
	Path     *string `json:"path,omitempty"`
	Host     *string `json:"host,omitempty"`
	Owner    *string `json:"owner,omitempty"`
	Repo     *string `json:"repo,omitempty"`
	Project  *string `json:"project,omitempty"`
	WebURL   *string `json:"webUrl,omitempty"`
	CloneURL *string `json:"cloneUrl,omitempty"`
}

// IsRemote reports whether the source came from a hosting provider rather than a local path.
func (s RepoSource) IsRemote() bool { return s.Type != "local" }

// Label is a short human-readable identifier: the scanned path for local repos, the web URL
// for remote ones. Use it in warnings and log lines so they work for every variant.
func (s RepoSource) Label() string {
	if s.Type == "local" {
		if s.Path != nil {
			return *s.Path
		}
		return ""
	}
	if s.WebURL != nil {
		return *s.WebURL
	}
	return ""
}

// Repo is one repository in the artifact.
type Repo struct {
	Slug        string     `json:"slug"`
	Name        string     `json:"name"`
	Description *string    `json:"description"`
	Source      RepoSource `json:"source"`
	Links       RepoLinks  `json:"links"`
	Tags        []string   `json:"tags"`
	Template    bool       `json:"template"`
	License     *License   `json:"license"`
	// ReleaseMode is "tags" (derived from annotated tags) or "provider" (imported).
	ReleaseMode string `json:"releaseMode"`
	// Releases are provider-imported, newest first. Always empty when ReleaseMode is "tags".
	Releases []Release `json:"releases"`

	// Empty is true when the repo has no commits on any branch.
	Empty         bool     `json:"empty"`
	DefaultBranch *string  `json:"defaultBranch"`
	Branches      []Branch `json:"branches"`
	// GitTags is named to avoid clashing with the metadata Tags.
	GitTags []Tag `json:"gitTags"`
	// Commits is every commit reachable from any branch, keyed by sha.
	Commits     map[string]Commit `json:"commits"`
	CommitCount int64             `json:"commitCount"`
	// ExtraCommits are display-support commits (schema v6) that fall OUTSIDE Commits — because
	// the history-narrowing knobs excluded them, or because a tag points at a commit no branch
	// reaches. Never feeds aggregates: contributors, insights, activity and every count read
	// Commits only. Look commits up through CommitFor, which consults both maps.
	ExtraCommits map[string]Commit `json:"extraCommits"`
	// Tree is a flat listing of the default-branch HEAD tree, sorted by path.
	Tree  []TreeEntry         `json:"tree"`
	Files map[string]FileInfo `json:"files"`
	// RefTrees are the trees of NON-default refs, keyed by ref name. An ordered map, not a
	// plain one — see reftreemap.go for the divergence that costs.
	RefTrees     RefTreeMap     `json:"refTrees"`
	Archives     []Archive      `json:"archives"`
	Languages    []LanguageStat `json:"languages"`
	Contributors []Contributor  `json:"contributors"`
	// Insights is null for an empty repo and when ingest.insights.enabled is false.
	Insights *RepoInsights `json:"insights"`
	Readme   *Readme       `json:"readme"`
	// CreatedAt is the date of the first commit on any branch; null when empty.
	CreatedAt *string `json:"createdAt"`
	// UpdatedAt is the date of the most recent commit on any branch; null when empty.
	UpdatedAt *string   `json:"updatedAt"`
	Warnings  []Warning `json:"warnings"`
}

// CommitFor looks a commit up for DISPLAY: the kept history first, then the display-support
// map. Aggregates must NOT use this — they iterate Commits alone so the narrowing knobs keep
// their meaning.
func (r *Repo) CommitFor(sha string) *Commit {
	if sha == "" {
		return nil
	}
	if c, ok := r.Commits[sha]; ok {
		return &c
	}
	if c, ok := r.ExtraCommits[sha]; ok {
		return &c
	}
	return nil
}

/* ---- notes (schema v4) --------------------------------------------------- */

// NoteFile is one file belonging to a note.
//
// Deliberately the same shape as FileInfo plus Name and Markdown, so the note viewer can reuse
// the repo file viewer's rendering without a translation layer. Sha is the sha1 of the raw
// bytes — NOT a git blob object id, since git hashes a `blob <len>\0` header first — so a note
// and a repo file with identical content are stored twice. Accepted: notes are few, and the
// alternative is faking git object ids for content that never was in git.
type NoteFile struct {
	Name string `json:"name"`
	// Path relative to the note root. For a single-file note this equals Name.
	Path     string  `json:"path"`
	Sha      string  `json:"sha"`
	Size     int64   `json:"size"`
	Binary   bool    `json:"binary"`
	TooLarge bool    `json:"tooLarge"`
	Stored   bool    `json:"stored"`
	Language *string `json:"language"`
	// Markdown is true for markdown files — the viewer offers the preview/source toggle.
	Markdown bool `json:"markdown"`
}

// Note is a gist-style note: one file, or one folder of files, under notes.dir.
type Note struct {
	Slug        string   `json:"slug"`
	Title       string   `json:"title"`
	Description *string  `json:"description"`
	Tags        []string `json:"tags"`
	// Date comes from frontmatter only unless notes.useMtime is set — filesystem mtimes are
	// not reproducible across checkouts, so opting into them opts out of a byte-identical
	// artifact.
	Date *string `json:"date"`
	// Kind is "file" (a single file directly in notes.dir) or "folder".
	Kind       string     `json:"kind"`
	Files      []NoteFile `json:"files"`
	TotalBytes int64      `json:"totalBytes"`
}

/* ---- organizations (schema v4) ------------------------------------------- */

// Organization is a named grouping of repos with its own overview page.
//
// Membership is the union of two directions: the org's own repos list in the site config, and
// every repo source that declares org: '<slug>'. Dangling references on either side are
// warnings, never build failures.
type Organization struct {
	Slug        string  `json:"slug"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	// Repos are member slugs, sorted and de-duplicated; every one exists in ForgeData.Repos.
	Repos []string `json:"repos"`
	// Avatar is a public/-relative image path (schema v8), or null.
	Avatar *string `json:"avatar"`
}

/* ---- hosted sites (schema v7) -------------------------------------------- */

// HostedSite is a repo's branch served as a real site at /<slug>/….
//
// Recorded in the artifact so the site build and the sync tests consume the same resolution
// rather than re-deriving it — Ref here is the RESOLVED branch (explicit config, or the first
// existing of gh-pages / main / master).
type HostedSite struct {
	Slug string `json:"slug"`
	Repo string `json:"repo"`
	Ref  string `json:"ref"`
}

/* ---- artifact ------------------------------------------------------------ */

// ForgeData is the artifact. Key order here IS the emitted order.
type ForgeData struct {
	SchemaVersion int `json:"schemaVersion"`
	// Repos sorted by slug.
	Repos []Repo `json:"repos"`
	// Notes ordered by date desc with null dates last, then title asc.
	Notes []Note `json:"notes"`
	// Organizations sorted by slug.
	Organizations []Organization `json:"organizations"`
	// Hosting sorted by slug.
	Hosting []HostedSite `json:"hosting"`
	// Warnings are site-level; repo-level ones are also mirrored here with Repo set.
	Warnings []Warning `json:"warnings"`
}

// EmptyForgeData is what the site builds from when no repos are configured. Every slice is
// non-nil on purpose: a nil slice marshals as `null`, and the artifact says `[]`.
func EmptyForgeData() ForgeData {
	return ForgeData{
		SchemaVersion: SchemaVersion,
		Repos:         []Repo{},
		Notes:         []Note{},
		Organizations: []Organization{},
		Hosting:       []HostedSite{},
		Warnings:      []Warning{},
	}
}

// CompareNotes orders two notes for the artifact: newest first, notes without a date last,
// ties broken by title then slug — code-point order, never a locale-aware compare, which would
// depend on the build machine's ICU data.
func CompareNotes(a, b Note) int {
	ad, bd := "", ""
	if a.Date != nil {
		ad = *a.Date
	}
	if b.Date != nil {
		bd = *b.Date
	}
	if ad != bd {
		if a.Date == nil {
			return 1
		}
		if b.Date == nil {
			return -1
		}
		if ad < bd {
			return 1
		}
		return -1
	}
	if a.Title != b.Title {
		if a.Title < b.Title {
			return -1
		}
		return 1
	}
	if a.Slug < b.Slug {
		return -1
	}
	if a.Slug > b.Slug {
		return 1
	}
	return 0
}
