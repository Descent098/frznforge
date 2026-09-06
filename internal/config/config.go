// Package config loads and resolves frznforge.config.jsonc — the port of
// src/lib/config/schema.ts and src/lib/config/index.ts.
//
// Both ingest and the renderer call Load, so defaults and path resolution happen in exactly
// one place. Two things differ from the TypeScript original, both consequences of the engine
// no longer being able to execute the config:
//
//   - The file is JSONC rather than TypeScript. `frznforge config migrate` converts one, and
//     the comments come across — see jsonc.go for why that mattered enough to keep.
//   - Values that used to be expressions (`512 * 1024`) are literals. Nothing else moves:
//     every key, default and validation rule below matches the zod schema it replaces, which
//     is what TestMatchesTypeScriptDefaults checks against the real implementation.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Filename is the config file frznforge looks for, next to the project root.
const Filename = "frznforge.config.jsonc"

/* ---- sources ------------------------------------------------------------- */

// RepoSourceConfig is one configured repository. It is the flattened union of the five
// TypeScript source variants; Type selects which fields are meaningful, and Validate rejects a
// combination that does not belong together.
type RepoSourceConfig struct {
	Type string `json:"type"`

	// local
	Path string `json:"path,omitempty"`

	// github / gitea / forgejo
	Owner string `json:"owner,omitempty"`
	Repo  string `json:"repo,omitempty"`
	// gitlab: full namespaced project path, e.g. "group/sub/proj".
	Project string `json:"project,omitempty"`
	// Host is the API/instance base URL. Defaulted per provider by applyDefaults.
	Host string `json:"host,omitempty"`

	// every variant
	Slug      string         `json:"slug,omitempty"`
	Overrides *RepoMetaInput `json:"overrides,omitempty"`
	// Releases is "provider" or "tags"; empty means "not set", which DefaultReleaseMode reads.
	Releases string `json:"releases,omitempty"`
	// TokenEnv names an environment variable holding the API token. Tokens live in the
	// environment only — never write one into the config file.
	TokenEnv string `json:"tokenEnv,omitempty"`
	// Org is a plain string, not a validated slug, on purpose: naming an organization that is
	// not configured must be a repo-unknown-org warning, not a parse error that fails the build.
	Org string `json:"org,omitempty"`
}

// RepoMetaInput mirrors a repo's own .frznforge.json and the `overrides` block.
type RepoMetaInput struct {
	Name        *string    `json:"name,omitempty"`
	Description *string    `json:"description,omitempty"`
	Links       *RepoLinks `json:"links,omitempty"`
	Tags        []string   `json:"tags,omitempty"`
	Template    *bool      `json:"template,omitempty"`
	License     *string    `json:"license,omitempty"`
	ReleaseMode *string    `json:"releaseMode,omitempty"`
}

// RepoLinks are the outbound links a repo may declare.
type RepoLinks struct {
	Homepage  *string `json:"homepage,omitempty"`
	Issues    *string `json:"issues,omitempty"`
	Donations *string `json:"donations,omitempty"`
	Upstream  *string `json:"upstream,omitempty"`
}

// IsRemote reports whether this source is fetched from a provider rather than read from disk.
func (s RepoSourceConfig) IsRemote() bool { return s.Type != "local" }

// DefaultReleaseMode is what the user asked for, else "provider" for remote sources and "tags"
// for local ones. A repo's own .frznforge.json still wins over this.
func (s RepoSourceConfig) DefaultReleaseMode() string {
	if s.Releases != "" {
		return s.Releases
	}
	if s.Type == "local" {
		return "tags"
	}
	return "provider"
}

/* ---- the rest of the schema ---------------------------------------------- */

type SiteConfig struct {
	Title       string `json:"title"`
	URL         string `json:"url,omitempty"`
	Description string `json:"description,omitempty"`
	// Base serves the site from a sub-path, e.g. "/mysite". Normalised to a leading slash and
	// no trailing slash; empty for a root deploy.
	Base string `json:"base,omitempty"`
}

type OwnerConfig struct {
	Name   string `json:"name"`
	Handle string `json:"handle"`
	// Profile is the markdown file rendered on the profile page.
	Profile string `json:"profile"`
	// Avatar is a public/-relative path. Local only, deliberately: frznforge's published pages
	// call no third party.
	Avatar string `json:"avatar,omitempty"`
}

// HeatConfig holds the day boundaries for the recency accent. Must be strictly ascending; the
// boundaries are configurable, the colours are not.
type HeatConfig struct {
	Hot     int `json:"hot"`
	Warm    int `json:"warm"`
	Neutral int `json:"neutral"`
	Cool    int `json:"cool"`
}

type ThemeConfig struct {
	Palette string     `json:"palette"`
	Heat    HeatConfig `json:"heat"`
}

type MarkdownConfig struct {
	// Mermaid renders ```mermaid fences as diagrams, everywhere markdown renders.
	Mermaid bool `json:"mermaid"`
}

type ContentConfig struct {
	// Orgs is the directory of organization profile pages: <dir>/<org-slug>.md.
	Orgs string `json:"orgs"`
}

type NotesConfig struct {
	Dir string `json:"dir"`
	// UseMtime falls back to file modification times when a note has no frontmatter date.
	// Off by default because mtimes are not reproducible across checkouts.
	UseMtime bool `json:"useMtime"`
	// MaxFileBytes defaults to ingest.maxBlobBytes when unset.
	MaxFileBytes *int64 `json:"maxFileBytes,omitempty"`
}

type OrganizationConfig struct {
	Slug        string   `json:"slug"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Repos       []string `json:"repos,omitempty"`
	Avatar      string   `json:"avatar,omitempty"`
}

// ContributorConfig credits a person who is not the owner, matched to git history by email.
type ContributorConfig struct {
	Name string `json:"name"`
	// Emails is a list because one person routinely commits under several addresses; every
	// matching git contributor is MERGED into one entry.
	Emails      []string `json:"emails"`
	Avatar      string   `json:"avatar,omitempty"`
	Description string   `json:"description,omitempty"`
	URL         string   `json:"url,omitempty"`
}

type HostedSiteConfig struct {
	Repo   string `json:"repo"`
	Slug   string `json:"slug,omitempty"`
	Branch string `json:"branch,omitempty"`
}

type HostingConfig struct {
	Sites []HostedSiteConfig `json:"sites"`
	// MaxFileBytes replaces ingest.maxBlobBytes on a hosted branch — a built site's bundles
	// routinely exceed 512 KB, and an unstored file is a silent 404 on the hosted site.
	MaxFileBytes int64 `json:"maxFileBytes"`
}

type ReuseConfig struct {
	Enabled       bool    `json:"enabled"`
	MaxAgeMinutes float64 `json:"maxAgeMinutes"`
	SkipUnchanged bool    `json:"skipUnchanged"`
	// CooldownSeconds is null (nil) by default, which disables it.
	CooldownSeconds *int64 `json:"cooldownSeconds"`
}

type InsightsConfig struct {
	Enabled bool `json:"enabled"`
	// Samples is the maximum monthly code-size checkpoints per repo.
	Samples int `json:"samples"`
	// MaxBytesPerSample is the byte budget for line counting at ONE checkpoint.
	MaxBytesPerSample int64 `json:"maxBytesPerSample"`
}

type IngestConfig struct {
	OutDir       string `json:"outDir"`
	MaxBlobBytes int64  `json:"maxBlobBytes"`
	// MaxCommits caps commits stored per repo; null = all.
	MaxCommits *int64 `json:"maxCommits"`
	// MaxCommitAgeDays keeps only commits from the last N days, anchored to the repo's newest
	// commit date — never to the clock — so the artifact stays reproducible. null = no limit.
	MaxCommitAgeDays *int64 `json:"maxCommitAgeDays"`
	Concurrency      int    `json:"concurrency"`
	TagTrees         int    `json:"tagTrees"`
	// BranchTrees is a non-negative count or the string "all". It is the single biggest lever
	// on build size, because tree/blob/raw pages are emitted per browsable ref.
	BranchTrees json.RawMessage `json:"branchTrees"`
	Archives    bool            `json:"archives"`
	CacheDir    string          `json:"cacheDir"`
	// Fetch is the network policy for remote sources: auto, never, or always.
	Fetch string `json:"fetch"`
	// FailOnDegraded makes a run that published from cached provider data exit non-zero.
	FailOnDegraded bool           `json:"failOnDegraded"`
	Reuse          ReuseConfig    `json:"reuse"`
	Insights       InsightsConfig `json:"insights"`
}

// BranchTreesLimit returns the configured cap and whether it is unlimited ("all").
func (i IngestConfig) BranchTreesLimit() (limit int, all bool, err error) {
	if len(i.BranchTrees) == 0 {
		return 10, false, nil
	}
	var s string
	if err := json.Unmarshal(i.BranchTrees, &s); err == nil {
		if s != "all" {
			return 0, false, fmt.Errorf("ingest.branchTrees: %q is not a number or \"all\"", s)
		}
		return 0, true, nil
	}
	var n int
	if err := json.Unmarshal(i.BranchTrees, &n); err != nil {
		return 0, false, fmt.Errorf(`ingest.branchTrees must be a non-negative number or "all"`)
	}
	if n < 0 {
		return 0, false, fmt.Errorf("ingest.branchTrees must be non-negative, got %d", n)
	}
	return n, false, nil
}

type ListingConfig struct {
	PageSize int `json:"pageSize"`
}

// Config is the file's contents with defaults applied. Paths are still as written.
type Config struct {
	Site          SiteConfig           `json:"site"`
	Owner         OwnerConfig          `json:"owner"`
	Theme         ThemeConfig          `json:"theme"`
	Markdown      MarkdownConfig       `json:"markdown"`
	Content       ContentConfig        `json:"content"`
	Repos         []RepoSourceConfig   `json:"repos"`
	Notes         NotesConfig          `json:"notes"`
	Organizations []OrganizationConfig `json:"organizations"`
	Contributors  []ContributorConfig  `json:"contributors"`
	Hosting       HostingConfig        `json:"hosting"`
	Ingest        IngestConfig         `json:"ingest"`
	Listing       ListingConfig        `json:"listing"`

	// notesConfigured records whether the file declared a `notes` block at all. notes.dir has
	// a default, so NotesDir always points somewhere; without this flag a site that never
	// opted into notes would raise notes-dir-missing on every build and show a permanent
	// warning in its footer. A warning has to mean "something you asked for did not happen".
	notesConfigured bool
}

// NotesConfigured reports whether the config file declared a notes block.
func (c *Config) NotesConfigured() bool { return c.notesConfigured }

/* ---- resolved ------------------------------------------------------------ */

// ResolvedSource is a configured source plus the absolute path its scan reads.
type ResolvedSource struct {
	RepoSourceConfig
	// AbsPath is the directory for a local repo, or the mirror clone inside CacheDir for a
	// remote one — which may not exist yet; the importer creates it before the scanner runs.
	// Downstream code therefore has one shape for every source.
	AbsPath string
}

// Resolved is Config with every path made absolute.
type Resolved struct {
	Config
	// Root is the absolute project root (the directory holding the config file).
	Root string
	// OutDir is the absolute ingest output directory.
	OutDir string
	// CacheDir is the absolute mirror/response cache directory.
	CacheDir string
	// ProfilePath is the absolute owner profile markdown file.
	ProfilePath string
	// NotesDir may not exist; see Config.NotesConfigured.
	NotesDir string
	// OrgsDir may not exist — orgs then render from config alone.
	OrgsDir string
	// Sources are the repos with an absolute path to scan.
	Sources []ResolvedSource
}

/* ---- loading ------------------------------------------------------------- */

// Load reads, parses, defaults, validates and resolves the config file at root.
func Load(root string) (*Resolved, error) {
	path := filepath.Join(root, Filename)
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("no %s in %s — run `frznforge config migrate` if you still have a frznforge.config.ts", Filename, root)
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	cfg, err := ParseBytes(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return Resolve(cfg, root)
}

// ParseBytes turns JSONC bytes into a defaulted, validated Config.
func ParseBytes(raw []byte) (*Config, error) {
	stripped := StripJSONC(TrimBOM(raw))

	// Decode twice: once into the struct, once into a generic map, so applyDefaults can tell
	// "absent" from "present and zero" for the handful of places where that matters (notes,
	// and every boolean whose default is true).
	var present map[string]json.RawMessage
	if err := json.Unmarshal(stripped, &present); err != nil {
		return nil, fmt.Errorf("not valid JSON (with comments): %w", err)
	}
	var cfg Config
	dec := json.NewDecoder(strings.NewReader(string(stripped)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("unrecognised or mistyped setting: %w", err)
	}

	cfg.notesConfigured = hasKey(present, "notes")
	if err := applyDefaults(&cfg, present); err != nil {
		return nil, err
	}
	if err := Validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func hasKey(m map[string]json.RawMessage, key string) bool { _, ok := m[key]; return ok }

// sub returns the raw sub-object at key, or nil.
func sub(m map[string]json.RawMessage, key string) map[string]json.RawMessage {
	raw, ok := m[key]
	if !ok {
		return nil
	}
	var out map[string]json.RawMessage
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

// applyDefaults fills in every zod `.default()` from src/lib/config/schema.ts.
//
// Booleans need the `present` map: Go cannot tell `false` from absent, and four of them
// default to TRUE (markdown.mermaid, ingest.archives, ingest.reuse.enabled,
// ingest.insights.enabled). Getting this wrong silently disables a feature the user never
// touched, which is why they are handled explicitly rather than by zero-value.
func applyDefaults(c *Config, present map[string]json.RawMessage) error {
	str := func(p *string, def string) {
		if *p == "" {
			*p = def
		}
	}
	num := func(p *int, def int) {
		if *p == 0 {
			*p = def
		}
	}
	num64 := func(p *int64, def int64) {
		if *p == 0 {
			*p = def
		}
	}
	boolDefaultTrue := func(p *bool, block map[string]json.RawMessage, key string) {
		if !hasKey(block, key) {
			*p = true
		}
	}

	str(&c.Site.Title, "frznforge")
	if c.Site.Base != "" {
		c.Site.Base = NormalizeBase(c.Site.Base)
	}

	str(&c.Owner.Profile, "./content/profile.md")

	str(&c.Theme.Palette, "hearth")
	num(&c.Theme.Heat.Hot, 7)
	num(&c.Theme.Heat.Warm, 30)
	num(&c.Theme.Heat.Neutral, 180)
	num(&c.Theme.Heat.Cool, 365)

	boolDefaultTrue(&c.Markdown.Mermaid, sub(present, "markdown"), "mermaid")

	str(&c.Content.Orgs, "./content/orgs")
	str(&c.Notes.Dir, "./content/notes")

	ingest := sub(present, "ingest")
	str(&c.Ingest.OutDir, "./data")
	num64(&c.Ingest.MaxBlobBytes, 512*1024)
	num(&c.Ingest.Concurrency, 4)
	if !hasKey(ingest, "tagTrees") {
		c.Ingest.TagTrees = 25
	}
	if len(c.Ingest.BranchTrees) == 0 {
		c.Ingest.BranchTrees = json.RawMessage("10")
	}
	boolDefaultTrue(&c.Ingest.Archives, ingest, "archives")
	str(&c.Ingest.CacheDir, "./.frznforge-cache")
	str(&c.Ingest.Fetch, "auto")

	reuse := sub(ingest, "reuse")
	boolDefaultTrue(&c.Ingest.Reuse.Enabled, reuse, "enabled")
	if c.Ingest.Reuse.MaxAgeMinutes == 0 {
		c.Ingest.Reuse.MaxAgeMinutes = 2
	}

	insights := sub(ingest, "insights")
	boolDefaultTrue(&c.Ingest.Insights.Enabled, insights, "enabled")
	num(&c.Ingest.Insights.Samples, 24)
	num64(&c.Ingest.Insights.MaxBytesPerSample, 20*1024*1024)

	num64(&c.Hosting.MaxFileBytes, 20*1024*1024)
	num(&c.Listing.PageSize, 50)

	if c.Repos == nil {
		c.Repos = []RepoSourceConfig{}
	}
	if c.Organizations == nil {
		c.Organizations = []OrganizationConfig{}
	}
	if c.Contributors == nil {
		c.Contributors = []ContributorConfig{}
	}
	if c.Hosting.Sites == nil {
		c.Hosting.Sites = []HostedSiteConfig{}
	}

	// Per-provider host defaults, applied only where the provider has one. gitea and forgejo
	// deliberately have none: they are self-hosted, so there is nothing to guess.
	for i := range c.Repos {
		switch c.Repos[i].Type {
		case "github":
			str(&c.Repos[i].Host, "https://api.github.com")
		case "gitlab":
			str(&c.Repos[i].Host, "https://gitlab.com")
		}
	}
	return nil
}

// NormalizeBase turns any of `mysite`, `/mysite`, `/mysite/` into `/mysite`; `”` and `'/'`
// mean a root deploy and normalise to `”`. Shared with the FRZNFORGE_BASE env override so the
// two cannot disagree.
func NormalizeBase(value string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(value), "/")
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "/") {
		return trimmed
	}
	return "/" + trimmed
}

/* ---- validation ---------------------------------------------------------- */

var (
	slugRe       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	schemeRe     = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.-]*:`)
	baseBadRe    = regexp.MustCompile(`[\s#%?]`)
	validFetch   = map[string]bool{"auto": true, "never": true, "always": true}
	validPalette = map[string]bool{"hearth": true, "frost": true}
	// ReservedHostingSlugs are top-level path segments the build itself emits. A hosted site
	// may not claim any of them — colliding with /repos/ would shadow the whole forge.
	ReservedHostingSlugs = map[string]bool{
		"repos": true, "notes": true, "orgs": true, "_astro": true,
		"search-index.json": true, "index.html": true, "404.html": true,
		"logo.png": true, "favicon.ico": true,
	}
)

// Validate enforces the rules the zod schema enforced. Failures are authoring mistakes, so
// every one of them is fatal here — unlike repo *state* problems, which become warnings.
func Validate(c *Config) error {
	var problems []string
	bad := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	if c.Owner.Name == "" {
		bad("owner.name is required")
	}
	if !slugRe.MatchString(c.Owner.Handle) {
		bad("owner.handle %q must be lowercase letters, digits and dashes", c.Owner.Handle)
	}
	if c.Site.Base != "" && baseBadRe.MatchString(c.Site.Base) {
		bad("site.base must not contain whitespace, #, %% or ?")
	}
	if !validPalette[c.Theme.Palette] {
		bad("theme.palette %q is not \"hearth\" or \"frost\"", c.Theme.Palette)
	}
	h := c.Theme.Heat
	if !(h.Hot < h.Warm && h.Warm < h.Neutral && h.Neutral < h.Cool) {
		bad("theme.heat boundaries must be strictly ascending (hot < warm < neutral < cool), got %d/%d/%d/%d",
			h.Hot, h.Warm, h.Neutral, h.Cool)
	}
	if !validFetch[c.Ingest.Fetch] {
		bad("ingest.fetch %q is not auto, never or always", c.Ingest.Fetch)
	}
	if _, _, err := c.Ingest.BranchTreesLimit(); err != nil {
		bad("%s", err.Error())
	}
	for _, p := range []struct {
		name, value string
	}{
		{"owner.avatar", c.Owner.Avatar},
	} {
		if p.value != "" {
			if err := checkPublicPath(p.value); err != nil {
				bad("%s: %s", p.name, err)
			}
		}
	}

	for i, s := range c.Repos {
		at := fmt.Sprintf("repos[%d]", i)
		switch s.Type {
		case "local":
			if s.Path == "" {
				bad("%s: local sources need a path", at)
			}
		case "github", "gitea", "forgejo":
			if s.Owner == "" || s.Repo == "" {
				bad("%s: %s sources need owner and repo", at, s.Type)
			}
			if s.Type != "github" && s.Host == "" {
				bad("%s: %s sources need an explicit host (they are self-hosted)", at, s.Type)
			}
		case "gitlab":
			if s.Project == "" {
				bad("%s: gitlab sources need a project path", at)
			}
		default:
			bad("%s: type %q is not local, github, gitlab, gitea or forgejo", at, s.Type)
		}
		if s.Slug != "" && !slugRe.MatchString(s.Slug) {
			bad("%s.slug %q must be lowercase letters, digits and dashes", at, s.Slug)
		}
		if s.Releases != "" && s.Releases != "provider" && s.Releases != "tags" {
			bad("%s.releases %q is not \"provider\" or \"tags\"", at, s.Releases)
		}
	}

	for i, o := range c.Organizations {
		if !slugRe.MatchString(o.Slug) {
			bad("organizations[%d].slug %q must be lowercase letters, digits and dashes", i, o.Slug)
		}
		if o.Name == "" {
			bad("organizations[%d].name is required", i)
		}
		if o.Avatar != "" {
			if err := checkPublicPath(o.Avatar); err != nil {
				bad("organizations[%d].avatar: %s", i, err)
			}
		}
	}

	for i, p := range c.Contributors {
		if p.Name == "" {
			bad("contributors[%d].name is required", i)
		}
		if len(p.Emails) == 0 {
			bad("contributors[%d].emails needs at least one address", i)
		}
		if p.Avatar != "" {
			if err := checkPublicPath(p.Avatar); err != nil {
				bad("contributors[%d].avatar: %s", i, err)
			}
		}
	}

	// Hosting slugs are structural: a collision or a reserved name would shadow real routes,
	// so they fail the parse rather than becoming warnings the way unknown repo names do.
	seen := map[string]bool{}
	for i, s := range c.Hosting.Sites {
		slug := s.Slug
		if slug == "" {
			slug = s.Repo
			if !slugRe.MatchString(slug) {
				bad("hosting.sites[%d].repo %q is not usable as a path segment; set an explicit slug", i, s.Repo)
			}
		}
		if ReservedHostingSlugs[slug] {
			bad("hosting.sites[%d]: hosted slug %q is a path the build itself owns", i, slug)
		}
		if seen[slug] {
			bad("hosting.sites[%d]: hosted slug %q is used twice", i, slug)
		}
		seen[slug] = true
	}

	if len(problems) > 0 {
		return fmt.Errorf("config is not valid:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// checkPublicPath enforces the PublicPath rules: a path inside public/, never a URL, never
// escaping with "..". Local only, deliberately — frznforge's published pages call no third
// party, and an avatar pointing at a forge's CDN would break that for every visitor.
func checkPublicPath(v string) error {
	if schemeRe.MatchString(v) || strings.HasPrefix(v, "//") {
		return errors.New("must be a path inside public/, not a URL (frznforge pages load no third-party assets)")
	}
	if strings.Contains(v, `\`) {
		return errors.New("use forward slashes")
	}
	for _, seg := range strings.Split(v, "/") {
		if seg == ".." {
			return errors.New(`must not escape public/ with ".."`)
		}
	}
	return nil
}

// PublicPath normalises a public/-relative path to a leading slash, matching the zod
// transform.
func PublicPath(v string) string {
	return "/" + strings.TrimLeft(v, "/")
}

/* ---- resolution ---------------------------------------------------------- */

// Resolve turns a parsed Config into absolute paths, applying the FRZNFORGE_* env overrides
// the e2e harness relies on.
func Resolve(c *Config, root string) (*Resolved, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	// Same env seams as resolveConfig in src/lib/config/index.ts, so a Go build and a
	// TypeScript build of the same fixture land in the same directories.
	if v, ok := os.LookupEnv("FRZNFORGE_BASE"); ok {
		c.Site.Base = NormalizeBase(v)
	}
	outDir := envOr("FRZNFORGE_OUT_DIR", c.Ingest.OutDir)
	cacheDir := envOr("FRZNFORGE_CACHE_DIR", c.Ingest.CacheDir)

	r := &Resolved{
		Config:      *c,
		Root:        abs,
		OutDir:      resolveFrom(abs, outDir),
		CacheDir:    resolveFrom(abs, cacheDir),
		ProfilePath: resolveFrom(abs, c.Owner.Profile),
		NotesDir:    resolveFrom(abs, c.Notes.Dir),
		OrgsDir:     resolveFrom(abs, c.Content.Orgs),
	}
	r.Sources = make([]ResolvedSource, 0, len(c.Repos))
	for _, s := range c.Repos {
		rs := ResolvedSource{RepoSourceConfig: s}
		if s.Type == "local" {
			rs.AbsPath = resolveFrom(abs, s.Path)
		} else {
			rs.AbsPath = filepath.Join(r.CacheDir, "mirrors", MirrorDirName(s))
		}
		r.Sources = append(r.Sources, rs)
	}
	return r, nil
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func resolveFrom(root, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(root, p)
}

// MirrorDirName is the cache subdirectory a remote source mirrors into. Derived from the
// source's identity so two sources cannot collide, and stable across runs so the mirror is
// reused.
func MirrorDirName(s RepoSourceConfig) string {
	safe := func(v string) string {
		return strings.Map(func(r rune) rune {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
				return r
			case r >= 'A' && r <= 'Z':
				return r + 32
			default:
				return '-'
			}
		}, v)
	}
	host := safe(strings.TrimPrefix(strings.TrimPrefix(s.Host, "https://"), "http://"))
	if s.Type == "gitlab" {
		return s.Type + "-" + host + "-" + safe(s.Project)
	}
	return s.Type + "-" + host + "-" + safe(s.Owner) + "-" + safe(s.Repo)
}
