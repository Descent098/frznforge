// Package build turns an artifact into a directory of files — the port of `astro build`.
//
// # The output contract
//
// Every route the site emits comes from internal/routes, and every route maps to a file by one
// of two rules — which one depends on whether the route carries a rendered page or bytes,
// because the URL alone cannot tell you (`/404` and `/repos/a/raw/main/LICENSE` look alike).
//
// WritePage, for rendered pages, uses FilePath:
//
//	/                    → index.html
//	/repos/alpha/        → repos/alpha/index.html      (a trailing slash means a directory index)
//	/404                 → 404.html                    (no slash, no extension: a page file)
//
// WriteFile, for bytes — raw blobs, archives, hosted-site files, the search index — uses
// RawPath, which is verbatim:
//
//	/search-index.json            → search-index.json
//	/repos/a/raw/main/x.go        → repos/a/raw/main/x.go
//	/repos/a/raw/main/LICENSE     → repos/a/raw/main/LICENSE     (no extension, still verbatim)
//
// That mapping is not a detail: it is what makes `/repos/alpha/` resolve on any static host
// and what makes a raw URL serve the committed bytes under the committed name. Sending an
// extensionless raw file through the page rule would publish `LICENSE.html`, and the raw URL
// the file table links to would 404.
//
// # How a page family is added
//
// Each family owns ONE file in this package (pages_*.go) exporting one emit function, and
// build.go calls it. Nothing else is shared, which is what lets the families be written
// independently — and what stops two of them quietly disagreeing about the shell.
//
// An emitter's job is: derive its payload from the artifact, hand it to the renderer, and
// write. It must not read the filesystem (except through Builder.Blob), must not consult the
// clock (Site.Now is the build clock), and must not sort with a locale-aware comparison — all
// three are how a "static site" stops being reproducible.
package build

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"frznforge/internal/config"
	"frznforge/internal/frontmatter"
	"frznforge/internal/markdown"
	"frznforge/internal/model"
	"frznforge/internal/render"
	"frznforge/internal/routes"
)

// Options steer one build.
type Options struct {
	// Root is the project directory holding the config.
	Root string
	// OutDir is where the site is written. Defaults to <Root>/dist.
	OutDir string
	// Now is the build clock. Zero means time.Now, but every caller that cares about
	// reproducibility passes one.
	Now time.Time
	// Verbose prints each route as it is written.
	Verbose bool
	// Workers is how many repositories render at once. 0 picks a default from GOMAXPROCS; 1 is
	// the serial mode `--serial` selects, which exists so the two can be compared.
	Workers int
	// Postprocess overrides the config file's `postprocess.command` — this is where the CLI's
	// `--postprocess=<cmd>` arrives. Empty means "not given": the config block and
	// $FRZNFORGE_POSTPROCESS stay in charge. See postprocess.go for the precedence.
	Postprocess string
	// PostprocessOut receives the hook command's stdout and stderr. nil means os.Stdout. Only
	// the hook's output goes here — the build's own verbose lines still print directly, because
	// a field named for one thing that quietly captured everything would be a trap.
	PostprocessOut io.Writer
}

// Result reports what a build produced.
type Result struct {
	Routes  int
	Bytes   int64
	Elapsed time.Duration
}

// Builder carries everything the page emitters need.
type Builder struct {
	Cfg      *config.Resolved
	Data     *model.ForgeData
	Site     *render.Site
	Renderer *render.Renderer
	Router   routes.Router
	OutDir   string
	Verbose  bool

	// blobDir is <ingest.outDir>/blobs, the content-addressed store the raw routes and the
	// blob pages read from.
	blobDir string

	// sem bounds concurrency for the WHOLE build — repositories and the per-repo page families
	// alike. A channel, so every copy of the Builder shares the same one and the two levels
	// cannot multiply into N² goroutines. See parallel.go.
	sem chan struct{}

	written int
	bytes   int64
	// log buffers verbose output instead of printing it. A worker renders into its own copy of
	// the Builder, so printing directly would interleave lines from several repositories; the
	// buffers are drained in artifact order once the pool is done.
	log []string
}

// Run builds the site.
func Run(opts Options) (Result, error) {
	start := time.Now()
	cfg, err := config.Load(opts.Root)
	if err != nil {
		return Result{}, err
	}
	artifactPath := filepath.Join(cfg.OutDir, "forge.json")
	raw, err := os.ReadFile(artifactPath)
	if err != nil {
		if os.IsNotExist(err) {
			return Result{}, fmt.Errorf(
				"no artifact at %s — run `frznforge ingest` first (a build with no artifact would publish an empty site over a good one)",
				artifactPath)
		}
		return Result{}, err
	}
	data, err := model.Parse(raw)
	if err != nil {
		return Result{}, err
	}

	outDir := opts.OutDir
	if outDir == "" {
		outDir = filepath.Join(cfg.Root, "dist")
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}

	site := &render.Site{
		Cfg:    cfg,
		Data:   &data,
		Router: routes.Router{Base: cfg.Site.Base},
		Now:    now,
		Orgs:   map[string]render.Content{},
	}
	if err := loadContent(site); err != nil {
		return Result{}, err
	}
	r, err := render.New(site)
	if err != nil {
		return Result{}, err
	}

	b := &Builder{
		Cfg: cfg, Data: &data, Site: site, Renderer: r,
		Router:  site.Router,
		OutDir:  outDir,
		Verbose: opts.Verbose,
		blobDir: filepath.Join(cfg.OutDir, "blobs"),
	}
	workers := opts.Workers
	if workers == 0 {
		workers = defaultWorkers()
	}
	if workers > 1 {
		b.sem = make(chan struct{}, workers)
	}

	// A build replaces the previous one wholesale. Leaving stale files behind would mean a
	// deleted repository's pages keep serving until someone notices.
	if err := os.RemoveAll(outDir); err != nil {
		return Result{}, fmt.Errorf("clear %s: %w", outDir, err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return Result{}, err
	}

	if err := b.copyAssets(); err != nil {
		return Result{}, err
	}
	if err := b.emitAll(); err != nil {
		return Result{}, err
	}
	if b.Verbose {
		for _, line := range b.log {
			fmt.Println(line)
		}
	}

	// Elapsed is taken before the hook runs. Whatever the user's own tooling costs is the user's
	// tool's, and folding it into the build time would misattribute a slow minifier to this
	// renderer.
	res := Result{Routes: b.written, Bytes: b.bytes, Elapsed: time.Since(start)}

	// The hook fires here and nowhere else: after every step above returned nil. Every early
	// return in this function leaves a partial dist/ on disk, and handing a partial directory to
	// somebody's minifier publishes a site that is neither the old build nor the new one — the
	// worst outcome available, because it looks like a success.
	hook := PostprocessFor(&cfg.Config, opts.Postprocess)
	if err := hook.Run(cfg.Root, outDir, opts.PostprocessOut); err != nil {
		// The site itself is written and the counts are real, so the caller gets both rather than
		// losing them to the failure. It is the hook that failed, and the error says so.
		return res, err
	}
	return res, nil
}

// emitAll walks every page family. The order matches routes.AllRoutes so a reader comparing
// the two sees the same shape.
func (b *Builder) emitAll() error {
	if err := emitProfile(b); err != nil {
		return err
	}
	if err := emitReposListing(b); err != nil {
		return err
	}
	if err := emitNotFound(b); err != nil {
		return err
	}
	if err := b.emitRepos(); err != nil {
		return err
	}
	if err := emitNotes(b); err != nil {
		return err
	}
	if err := emitOrgs(b); err != nil {
		return err
	}
	if err := emitHosted(b); err != nil {
		return err
	}
	return emitSearchIndex(b)
}

// emitRepo emits every page of one repository. Split out so the streaming pipeline (Phase 7)
// has exactly one function to call per repo.
func emitRepo(b *Builder, repo *model.Repo) error {
	if err := emitRepoOverview(b, repo); err != nil {
		return err
	}
	if repo.Empty {
		return nil
	}
	for _, emit := range []func(*Builder, *model.Repo) error{
		emitRepoRefs,     // tree, blob, raw
		emitRepoHistory,  // commits, commit, branches, tags
		emitRepoReleases, // releases index + one page per release
		emitRepoInsights,
		emitRepoArchives,
	} {
		if err := emit(b, repo); err != nil {
			return err
		}
	}
	return nil
}

/* ---- writing ------------------------------------------------------------- */

// FilePath maps a PAGE route to its path inside the output directory. See the package comment
// for the rule; this is the only place that knows it.
//
// Bytes routes go through RawPath instead. Two rules, not one, because `/404` and
// `/repos/a/raw/main/LICENSE` are the same string shape and want opposite answers.
func FilePath(base, route string) string {
	rel := RawPath(base, route)
	switch {
	case rel == "":
		return "index.html"
	case strings.HasSuffix(rel, "/"):
		return rel + "index.html"
	case filepath.Ext(rel) == "":
		// A page route with no trailing slash and no extension is a page file: /404 → 404.html.
		return rel + ".html"
	default:
		return rel
	}
}

// RawPath maps a BYTES route to its path inside the output directory: strip the deploy base,
// keep the rest exactly as the URL spells it.
//
// Verbatim is the whole contract for these. A repository holding `LICENSE`, `Makefile` or
// `Dockerfile` — no extension — must land under that name, or the raw URL every file table
// links to serves nothing.
func RawPath(base, route string) string {
	rel := strings.TrimPrefix(route, base)
	return strings.TrimPrefix(rel, "/")
}

// WritePage renders a body template inside the shell and writes it at route.
func (b *Builder) WritePage(route, tpl string, page render.Page) error {
	var buf bytes.Buffer
	if err := b.Renderer.RenderPage(&buf, tpl, page); err != nil {
		return err
	}
	return b.writeAt(route, FilePath(b.Router.Base, route), buf.Bytes())
}

// WriteFile writes bytes at route, creating directories as needed. The route's path is taken
// verbatim (RawPath) — this is the writer for raw blobs, archives, hosted-site files and the
// search index, none of which may be renamed.
func (b *Builder) WriteFile(route string, content []byte) error {
	return b.writeAt(route, RawPath(b.Router.Base, route), content)
}

// writeAt writes content at a path already resolved by one of the two rules.
//
// Percent-encoded segments are decoded back to the literal name: a URL says
// `read%20me.md` and the file on disk has to be `read me.md`, or the browser's decoded request
// finds nothing. routes.IsRawServable has already excluded the two characters for which no
// such round trip exists.
func (b *Builder) writeAt(route, rel string, content []byte) error {
	decoded, err := decodePath(rel)
	if err != nil {
		return fmt.Errorf("route %s: %w", route, err)
	}
	full := filepath.Join(b.OutDir, filepath.FromSlash(decoded))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(full, content, 0o644); err != nil {
		return err
	}
	b.written++
	b.bytes += int64(len(content))
	if b.Verbose {
		fmt.Printf("  %s\n", route)
	}
	return nil
}

// Blob reads a stored blob by sha. Missing is an error: FileInfo.Stored said it was there, and
// silently writing an empty file would publish a truncated copy of somebody's source.
func (b *Builder) Blob(sha string) ([]byte, error) {
	content, err := os.ReadFile(filepath.Join(b.blobDir, sha))
	if err != nil {
		return nil, fmt.Errorf("blob %s: %w (re-run `frznforge ingest`)", sha, err)
	}
	return content, nil
}

// Markdown renders repo or note markdown at the right trust level.
func (b *Builder) Markdown(src string, trusted bool) string {
	return markdown.Render(src, markdown.Options{Trusted: trusted, Mermaid: b.Cfg.Markdown.Mermaid})
}

/* ---- assets -------------------------------------------------------------- */

// copyAssets copies public/ and web/ into the output verbatim.
//
// Verbatim is the whole point: 0.4.0's rule is that the browser gets the bytes that are on
// disk — no transform, no rename, no content hash. public/ belongs to the site owner, web/ is
// the engine's own UI; they land in the same tree because that is what a static host serves.
func (b *Builder) copyAssets() error {
	for _, dir := range []string{"public", "web"} {
		src := filepath.Join(b.Cfg.Root, dir)
		if _, err := os.Stat(src); os.IsNotExist(err) {
			continue
		}
		if err := copyTree(src, b.OutDir, &b.written, &b.bytes); err != nil {
			return fmt.Errorf("copy %s: %w", dir, err)
		}
	}
	return nil
}

func copyTree(src, dst string, count *int, bytesOut *int64) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, content, 0o644); err != nil {
			return err
		}
		*count++
		*bytesOut += int64(len(content))
		return nil
	})
}

/* ---- hand-written content ------------------------------------------------ */

// loadContent reads the owner's profile.md and every content/orgs/<slug>.md.
//
// A missing file is not an error: an organization without a markdown file still gets a page,
// built from config alone. What IS surfaced is a file no organization claims — one typo in the
// filename otherwise discards the whole file with no page and no other symptom.
func loadContent(site *render.Site) error {
	if raw, err := os.ReadFile(site.Cfg.ProfilePath); err == nil {
		site.Profile = contentFrom(string(raw), true, site.Cfg.Markdown.Mermaid)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", site.Cfg.ProfilePath, err)
	}

	entries, err := os.ReadDir(site.Cfg.OrgsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", site.Cfg.OrgsDir, err)
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		slug := strings.TrimSuffix(e.Name(), ".md")
		ids = append(ids, slug)
		raw, err := os.ReadFile(filepath.Join(site.Cfg.OrgsDir, e.Name()))
		if err != nil {
			return err
		}
		site.Orgs[slug] = contentFrom(string(raw), true, site.Cfg.Markdown.Mermaid)
	}
	sort.Strings(ids)
	site.UnmatchedOrgContent = routes.UnmatchedOrgContent(site.Data, ids)
	return nil
}

func contentFrom(raw string, trusted, mermaid bool) render.Content {
	fm := frontmatter.Parse(raw)
	data := map[string]any{}
	for k, v := range fm.Data {
		if v.IsList {
			data[k] = v.List
		} else {
			data[k] = v.Str
		}
	}
	// The nested mappings — the profile's `forges:`, an organization's `links:` — go in as the
	// ordered entry slice, not as a Go map. Both become rows of pills, and a map would shuffle
	// them between builds.
	for k, entries := range fm.Maps {
		data[k] = entries
	}
	return render.Content{
		Exists:      true,
		Frontmatter: data,
		Body:        htmlOf(markdown.Render(fm.Body, markdown.Options{Trusted: trusted, Mermaid: mermaid})),
	}
}
