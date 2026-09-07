## Development

frznforge is a **Go binary**. Build it with `go build ./cmd/frznforge`; there is no Node in the
build path and no bundler, transpiler or minifier anywhere.

- `frznforge ingest` → `data/forge.json` + `data/blobs/` + `data/archives/` (all gitignored).
- `frznforge build` = ingest + render. `frznforge build --no-ingest` renders the artifact already
  on disk, which is what you want while iterating on templates or CSS. It refuses when there is
  no artifact rather than quietly building an empty site over a good one.
- `frznforge dev` serves the **last build** over `dist/` and rebuilds nothing. It exits 1 with
  instructions if `dist/` or `data/forge.json` is missing. To see a change, run `build` again.
- `frznforge init [--web]`, `frznforge new <dir>` — set up a site, or scaffold one.
- `frznforge verify [<forge.json>]` reads, validates and re-serializes the artifact, and compares
  byte for byte with the file on disk.
- `frznforge config migrate` converts a 0.3.0 `frznforge.config.ts` into `frznforge.config.jsonc`,
  comments and all.

There is no watch mode and no HMR. The render is fast enough that a rebuild is the loop.

## Project layout & commands

- `frznforge.config.jsonc` — site config (owner, repos to ingest, palette). JSON with comments;
  the comments are the configuration's documentation and every tool that edits it preserves them.
  `content/profile.md` — profile page.
- `cmd/frznforge/` — the CLI. `internal/` — the engine:
  - `internal/model` is the ingest ↔ site contract (schema **v8**). Bump `SchemaVersion` +
    `docs/dev/data-model.md` + the golden fixtures on any change.
  - `internal/ingest/*` reads git **via the CLI only, never the working tree**. This is the
    load-bearing invariant of the project; `internal/ingest/uncommitted_test.go` guards it.
  - `internal/build/pages_*.go` are the page families; `internal/routes` predicts every route and
    `internal/build/sync_test.go` holds the two to each other in both directions.
  - `internal/{render,markdown,highlight,theme,serve,scaffold,wizard,config,frontmatter}`.
- `web/` is the browser half, served **verbatim** — plain ES modules, no transpile, no bundler,
  relative imports with `.js` extensions. `web/js/{format,listing,search,base}.js` are THE
  implementations. Never fork one into a second copy. `web/vendor/` is pre-built third-party code
  (mermaid). Note `web/` is copied from the PROJECT, not embedded in the binary: a site that does
  not carry it builds pages with no stylesheet and no command palette.
- Two Go dependencies, both pure Go: goldmark (markdown) and chroma (highlighting).
- Styles: plain CSS only, `hf-` prefix, tokens at the top of `web/css/global.css` (site-wide) +
  `web/css/repo.css` (repo sub-pages). No Tailwind/Sass.

## House rules

- **Determinism is the central invariant.** Two machines must emit identical bytes from identical
  input. Never range a Go map without sorting the keys; never use a locale-aware comparison; never
  read the clock — take `now time.Time` as a parameter.
- **Comments explain why, not what.** Name the bug the code prevents, the alternative rejected, or
  the invariant at stake. A comment that restates the next line is worse than none.
- **Usability beats accessibility when the two collide.** This is a personal app.
- The **cross-language goldens** in `tests/fixtures/` are generated FROM `web/js/*.js`, which makes
  the JavaScript the reference and Go the thing checked against it. Change one of those files and
  re-run its generator in the same commit — `internal/render/golden_test.go` fails with the exact
  command if you forget.

## Testing

- `go test ./...` — the whole engine. Fixture git repos in temp dirs; nothing reaches the network.
- `npm run test:e2e` — Playwright. `tests/e2e/global-setup.ts` builds fixture repos, seeds the
  provider caches so the two "remote" repos import offline, runs the binary for ingest and build,
  then starts two `frznforge dev` servers. Node is needed for this and nothing else.
- There is **no `npm run check`**: 0.4.0 removed TypeScript rather than upgrading it. Playwright
  transpiles the specs itself. `tsconfig.json` exists for editors only.
- The e2e specs are the invariant the rewrite is measured against and **may not be edited** to
  accommodate a change in the engine. The harness around them may.
- Plans & phase checklist: `docs/dev/plans/`. Coverage audit: `docs/dev/architecture.md`.
