# Delivery phases — 0.2.0

Companion to [version-2.md](version-2.md). That file is the *what*; this one is the *in what
order*. Same shape as [plan-phases.md](plan-phases.md) (the 0.1.0 plan): each phase ends in
something runnable and demoable on its own; prerequisites land before the phases that consume
them (base path before hosting); each feature lands both of its sides — data and UI — inside
one phase, per the both-sides rule below; and the wizard phase comes last before the release
phase because it must expose every setting the earlier phases add.

Conventions used below:

- **Ships** – the user-visible or developer-visible result when the phase is done.
- **Done when** – the acceptance bar. Nothing moves to the next phase until these hold.
- **Tests** – what must exist per the rules in `TODO`: unit + UI coverage for anything
  crossing the ingest ↔ site boundary or the server ↔ browser boundary (both sides tested in
  the same change), and sync/regression tests whenever the artifact changes.
- **Versioning** – the first change of 0.2.0 — whichever phase it belongs to — bumps
  `VERSION` and `package.json` to `0.2.0` and opens a `# 0.2.0 (unreleased)` heading in
  `CHANGELOG.md` (0.1.0 is dated, so per the `TODO` rules a new heading opens). Every change
  in every phase then adds its entries under that heading's **Features** / **Bug Fixes** /
  **Other** sub-headings; the release date is stamped only in Phase 9.
- **Data model** – any change to `src/lib/data/schema.ts` that alters emitted JSON follows
  [data-model.md § Bumping SCHEMA_VERSION](../data-model.md#bumping-schema_version)'s own
  four steps in the same change — bump `SCHEMA_VERSION`; update `docs/dev/data-model.md`;
  regenerate the ingest snapshot and adjust the extractor unit tests; add the `CHANGELOG.md`
  entry — plus the 0.1.0 cross-cutting rule's fifth: add/adjust the sync tests.
  Additive `WarningCode` widenings do not bump but still update the doc's tables.

Standing constraints every phase inherits (from `CLAUDE.md` and the 0.1.0 cross-cutting
rules): the build stays fully static; `src/lib/format.ts` and `src/lib/listing.ts` stay
browser-safe (no node imports — config values reach islands as props only); ingest stays
deterministic (no wall-clock values in `forge.json`, ever — timestamps live in
`ingest.cacheDir` sidecars only); plain CSS with `hf-` tokens; and `npm test`,
`npm run test:e2e`, `npm run check` all green before every commit.

---

## Phase 1 — Configuration knobs: recency accent & ingest timeframe ✅ *(done 2026-08-28)*

Goal: land the two small config features first. They are independent, ship user value
immediately, open the 0.2.0 changelog section, and give the Phase 8 wizard settings to
expose.

Ships
- [x] **Configurable recency accent** (version-2.md "Currently the site shows orange when
  repos are recently modified, and blue when not. Make this value configurable"). New
  `theme.heat` config key beside `theme.palette` in `src/lib/config/schema.ts`:
  `{ hot: 7, warm: 30, neutral: 180, cool: 365 }` (days), prefaulted to today's constants,
  with a zod refinement rejecting non-positive or non-ascending values. `heatFor()` in
  `src/lib/format.ts` gains an optional thresholds parameter defaulting to an exported
  `DEFAULT_HEAT` — a pure data argument, so format.ts stays browser-safe. Threading follows
  the existing `pageSize`/`now` pattern: `.astro` callers read `getConfig().theme.heat` in
  frontmatter; `RepoListing.svelte` → `RepoCard.svelte` get it as a prop; `buildContribGraph`
  (`src/lib/contrib.ts`) takes the same optional argument. Every hardcoded 7-day literal
  reads `theme.heat.hot` so the stats and the accent agree: the `commitsSince(repos, 7)`
  call sites *and* the inline `7 * 86_400_000` `touchedThisWeek` filters, both present in
  `src/pages/index.astro` and `src/components/OrgHeader.astro`. Colours are *not*
  configurable in 0.2.0 — the `--hf-ember` / `--hf-ice` tokens are pinned by
  `tests/unit/contrast.test.ts` and a user-supplied colour could not carry the WCAG
  guarantee; the knob is the day boundaries. (Reading check before starting: version-2.md's
  "make this value configurable" could also mean the colours themselves — this plan reads it
  as the day boundary *because* the colour tokens carry the contrast guarantee. Confirm, or
  scope a set of preset accent pairs that keep `contrast.test.ts` green.)
  *As built: the boundaries thread as `heatDays` — `.astro` components read
  `getConfig().theme.heat` in their own frontmatter (less invasive than prop-drilling, same
  build-time result), islands get it as a prop beside `now`; `commitsSince` call sites and
  both inline `touchedThisWeek` filters now use `theme.heat.hot`, with the KPI copy going
  dynamic ("in the last N days") for non-default values.*
- [x] **Ingest timeframe limit** (version-2.md "limit by number of days elapsed"). New
  `ingest.maxCommitAgeDays: number | null` (default `null`) beside `maxCommits` in the
  config schema, flowing through `ScanOptions` into `loadBranches`
  (`src/lib/ingest/refs.ts`) as a date cutoff beside the existing `--max-count` logic in its
  `git rev-list --topo-order` invocation — using `--since-as-filter`, not plain `--since`:
  `--since` stops traversal at the first old commit and can drop in-window commits reachable
  only through clock-skewed older ones, and the byte-identity acceptance below leans on
  exact truncation semantics. **The cutoff is anchored to the newest commit date across the repo's
  branches, never to the clock** — same principle as the insights checkpoints ("derived from
  the commit list, never from a clock") — so the same repo at the same commits produces the
  same artifact on any machine, on any day. Document the consequence in the config reference
  and data-model.md: a dormant repo keeps its last `maxCommitAgeDays` of history rather than
  going empty. Truncation raises a new `commits-aged-out` warning (sibling of
  `commits-capped`), and the doc spells out what narrows with the history: `createdAt`,
  `commitCount`, `contributors`, the insights series.
- [x] `VERSION` + `package.json` → `0.2.0`; `CHANGELOG.md` gains `# 0.2.0 (unreleased)` with
  these two features under **Features**.

Done when
- [x] A fixture repo with old commits, ingested with `maxCommitAgeDays` set, produces a
  smaller artifact that is byte-identical across two runs. *Stronger than planned: the
  anchor never reads a clock at all, so there is no "different day" input to vary — the
  double-run equality tests in `commits.test.ts` pin it.*
- [x] A configured `theme.heat` visibly moves a repo card between accent buckets, and the
  server-rendered card and the hydrated island agree. *Deviation, on purpose: the e2e build
  reads the checked-in `frznforge.config.ts` (only `outDir`/`cacheDir` have env overrides,
  and `resolveConfig` documents why no more knobs are added), so a non-default cutoff cannot
  reach the fixture site without changing the real site's config. The e2e
  (`site.spec.ts`) walks the whole pipeline with the shipped config — alpha `heat-hot` via
  its now-relative commit, bravo `heat-cold`, SSR + hydrated island agreeing — and the
  configured flip itself is pinned at unit level (`format.test.ts`,
  `config-knobs.test.ts`).*

Tests
- [x] Unit: `format.test.ts` — custom thresholds probe each boundary; omitting the argument
  preserves 7/30/180/365. `config-knobs.test.ts` — defaults, partial configs, rejection of
  unordered/non-positive values. `commits.test.ts` — `--since-as-filter` truncation,
  `commits-aged-out` raised once, the keep-the-head rule for stale branches, interplay with
  `maxCommits`, determinism across runs, and insights narrowing with the kept commits.
- [x] UI (e2e): heat classes on repo cards asserted end to end (see the Done-when
  annotation for why the shipped config, not a fixture one, drives it).
- [x] Data model: no `SCHEMA_VERSION` bump (both features are config-side; `commits-aged-out`
  is an additive `WarningCode` widening) — data-model.md's warning table, `Repo.commits`
  and `Branch.commits` prose updated in the same change.

---

## Phase 2 — Ingest performance: run reuse, scan cache, `--no-cache` ✅ *(done 2026-08-28)*

Goal: make the caches that already exist actually save time, and answer version-2.md's
"the rebuild time with a constructed cache is as slow as a build without one" honestly: the
mirror/meta cache saves *network* only — every run still re-scans every repo — and ingest is
already the small half (7.1 s warm ingest vs 204 s astro render on the benchmark corpus, per
[performance.md](../performance.md)). This phase attacks the ingest half; Phase 3 confronts
the render half.

Ships
- [x] **CLI flags for ingest.** `scripts/ingest.ts` parses argv for the first time.
  `--no-cache` = `fetch: 'always'` + ignore provider `.meta.json` reads + ignore the
  run/scan caches below, composing with the `FRZNFORGE_OUT_DIR`/`FRZNFORGE_CACHE_DIR` env
  overrides the e2e harness uses. `npm run ingest -- --no-cache` documented in
  configuration.md. *As built: `parseIngestArgs` lives in `src/lib/ingest/reuse.ts` (unit-
  testable, unlike the script); unknown flags are a hard error. A `--no-cache` run still
  RECORDS its fresh results — ignoring means not reading, and the next ordinary run should
  benefit from a maximally fresh one.*
- [x] **Run-reuse with a freshness window** (version-2.md "successful content cache run less
  than 2 mins ago"). New sidecar `<ingest.cacheDir>/last-run.json`; config
  `ingest.reuse: { enabled: true, maxAgeMinutes: 2 }`. Semantics — the window governs the
  *network*, the scan cache below governs the *scan*: within the window, remote sources
  whose last fetch fully succeeded are not re-fetched; a repo recorded as degraded
  (`remote-fetch-failed` / `remote-rate-limited` / `remote-cache-stale` / skipped) is
  **always** re-attempted, which is exactly the rate-limited-large-refresh case version-2.md
  calls out. Reuse never changes artifact bytes — it skips work and emits the identical
  artifact, or it runs. Timestamps live only in this sidecar; the clock is the
  already-plumbed `PrepareRemoteDeps.now`. *As built, three refinements: the log stores a
  per-source `{ fetchedAt, fresh }` stamp (not one run-level `finishedAt`) and a window-skip
  keeps the old stamp, so the window can never extend itself; the window applies under
  `fetch: 'auto'` only — `'always'` is an explicit ask to fetch, and `'never'` must keep its
  stale-cache warnings or the same commits would produce different bytes depending on
  timing; and the plan's "re-run only the degraded repos and merge" partial-rerun machinery
  proved unnecessary — never window-skipping a degraded source achieves the same healing
  with none of the merge/ordering complexity.*
- [x] **Per-repo scan cache.** `<ingest.cacheDir>/scan/<digest>.json` keyed by the repo's
  ref-head shas + a hash of `ScanOptions` + the committed `.frznforge.json` blob sha +
  config overrides **+ a digest of the provider metadata layer** (the `.meta.json` content —
  provider descriptions, links, topics and releases change with *no* ref-head change, and a
  key without them would replay stale releases into a schema-valid artifact: the silently
  wrong site performance.md forbids). A hit skips `scanRepo()` and replays the recorded
  `Repo` — and must also **rehydrate the blob and archive maps** for that repo, reading
  bytes back from the content-addressed store and `archives/`: `writeArtifact` mirrors
  `data/` against the in-memory maps and deletes anything absent from them, so a replay
  contributing no buffers would delete the repo's blobs and zips. Assembly-order invariants
  (slug-collision renaming, fixed warning ordering) are reproduced by re-running the
  assembly pass over cached + fresh repos alike. This is what makes a no-change
  `npm run build` skip the `ls-tree`/`cat-file`/`git archive` work that dominates warm
  ingest. *As built: the digest also covers HEAD and every branch/tag ref (tags change scan
  output with no head change), and the cached `Repo` is re-validated against the schema on
  read, so a corrupt cache degrades to a re-scan, never to a failed build. The entry is
  written before assembly mutates the repo (slug renames, remote-warning stamping), so what
  is stored is exactly a fresh scan. Review-driven hardening: archive rehydration verifies a
  stored content hash per zip — archive paths are slug-keyed, not content-addressed, and
  after a slug-collision rename a cached path can hold the* colliding *repo's zip; the
  adversarial review reproduced exactly that silent swap, and the hash check turns it into
  a quiet re-scan (`reuse.test.ts` "a slug collision never lets a replay publish the other
  repo's archive bytes").*
- [x] Before/after numbers recorded in [performance.md](../performance.md). *`npm run
  measure` decomposes page counts, which reuse does not change — the moved metric is ingest
  wall time, recorded instead: self-build ≈2.0 s cold → ≈0.22 s warm (−89%), with the
  7.1 s-vs-204 s proportion note kept honest.*

Done when
- [x] Two back-to-back `npm run ingest` runs: the second completes in a small fraction of the
  first (≈2.0 s → ≈0.22 s measured) and `forge.json` is byte-identical (pinned by
  `reuse.test.ts` byte-equality over full `ingest()` runs).
- [x] A run with one rate-limited repo followed by a run inside the window re-fetches
  the degraded repo and heals it (`reuse.test.ts` "always re-attempts a degraded repo").
- [x] `--no-cache` demonstrably ignores every cache (mirror fetch behaviour aside — mirrors
  are still incremental via `git remote update`).

Tests
- [x] Unit: `tests/unit/reuse.test.ts` — fresh-window reuse with the injected clock, stale
  window runs, config-hash change runs, head-sha invalidation, options invalidation,
  **refreshed provider metadata with unchanged heads** (the stale-release hazard), degraded
  repo always re-fetched, byte-identity of replayed runs, the writeArtifact prune hazard
  (warm run leaves every blob/zip intact), missing-blob fallback, `parseIngestArgs`, and
  `withinFreshWindow` boundaries. *Deviation, on purpose: the planned `GitRunner` seam in
  `scanRepo` was not added — a cache hit is proven black-box instead, by tampering with the
  cached entry and seeing the tampered value in the artifact (only a replay can produce
  it), which is strictly stronger than counting git calls and leaves the scanner untouched.*
- [x] UI (e2e): the e2e build runs the whole pipeline with reuse enabled (fresh path — its
  fixture tree is wiped every run by design); the warm-path byte-identity is pinned at unit
  level over full `ingest()` runs, which the site build consumes byte-for-byte.
- [x] Data model: no schema change; the snapshot and determinism suites ran unchanged, and
  the replay byte-identity assertions double as the no-leak guard for `last-run.json` /
  `RemoteStatus`.

---

## Phase 3 — Performance investigations: render concurrency, page skipping, sqlite (spike)

Goal: the remaining version-2.md performance items are investigations, not commitments.
Each gets a spike, a measurement, and a recorded decision in
[performance.md](../performance.md) — implemented if it clears its bar, written up as
deliberately-not-done if not (the Phase 7-of-0.1.0 precedent). The standing bar from
performance.md applies: incremental output caching is revisited *"only with a measured build
where ingest, not rendering, dominates"* — after Phase 2, rendering dominates harder than
ever, so the honest expected outcome for page-skipping is a documented rejection.

Ships
- [ ] **Astro render concurrency** ("concurrently build island-pages … one thread per
  repo"): Astro exposes `build.concurrency` as its only supported parallelism knob — there
  is no per-repo build unit and no supported multi-process partial build, and performance.md
  already rejected dist-merging sharding. Measure `build.concurrency` values on the
  benchmark corpus with `npm run measure`; adopt the best value in `astro.config.mjs` if it
  wins, record the numbers either way.
- [ ] **Skip unchanged pages** ("if the most recent commit hash matches the one on the page,
  skip rebuilding it"): spike only. `dist/` retains nothing machine-readable identifying
  commits, Astro has no partial-output mode, so this means frznforge owning a
  route → input-hash manifest plus copy-forward around a full build — the design
  performance.md rejected ("a stale page is a silently wrong site"). The spike must
  enumerate the cross-page state (search-index.json, listing pages, footer warning counts,
  shared `_astro/` hashes) and either prove a safe subset with a correctness argument and
  tests, or record the rejection with numbers.
- [ ] **sqlite for blobs + cache** ("investigate if using sqlite … can help"): spike behind
  the existing seam — all blob reads funnel through `readBlob`/`readBlobBuffer` in
  `src/lib/data/load.ts`, all writes through `writeArtifact` in `src/lib/ingest/index.ts`,
  so the blast radius is two files plus tests. Constraints the spike must respect: reads are
  synchronous inside Astro frontmatter (`node:sqlite`'s `DatabaseSync` avoids a native dep —
  verify its flag status on the pinned Node range, else `better-sqlite3` and the
  dependency-minimalism argument that entails); Windows file locking is first-class; the
  self-build blob store is small (hundreds of files, single-digit MB), so the win case is
  the large remote corpus — measure there. Bar: a measured, meaningful ingest or build win,
  or it is written up and closed. If adopted: data-model.md's `data/` layout section is
  rewritten, and — although the letter of the bump rule does not fire (`forge.json` stays
  byte-identical and `schema.ts` untouched; only the storage backend moves) — bump
  `SCHEMA_VERSION` anyway as a *deliberate extension*: the version literal is the only guard
  that forces a stale file-layout `data/` dir to be re-ingested. Plus a
  `readBlob`/`readBlobBuffer` contract test run against both backends before the swap.

Done when
- [ ] performance.md has a dated section with measurements and a decision for all three;
  anything adopted shipped with its tests; anything rejected has its reasoning recorded next
  to the 0.1.0 rejections.

Tests
- [ ] Whatever is adopted carries the tests named above (contract tests for a storage swap;
  byte-identical-dist proof for any page skipping; `npm run measure -- --json` before/after
  for concurrency). A pure rejection ships no code and needs no tests — the deliverable is the
  document.

---

## Phase 4 — Mermaid diagrams in markdown

Goal: ```` ```mermaid ```` fences in rendered markdown become diagrams — without breaking
the untrusted-content boundary, the zero-third-party-asset rule, the a11y gate, or build
determinism. Today such a fence renders as an escaped `<code class="language-mermaid">`
block; nothing consumes it.

Ships
- [ ] **Trusted content only.** A `code` renderer override in both Marked instances in
  `src/lib/markdown.ts`: in trusted mode (owner content — profile, orgs, notes, local
  repos) a mermaid fence emits an `hf-mermaid` container carrying the source; in untrusted
  mode (imported repos' READMEs/release notes) it stays exactly today's plain code block.
  Mermaid executes an attacker-influenced grammar and has an XSS history; its
  `securityLevel: 'strict'` sanitiser is precisely the third-party-sanitiser dependency
  `markdown.ts` refuses on principle. Rendering owner-authored diagrams and printing
  imported ones is the same trust line the HTML-stripping already draws. Documented in the
  config reference and on the page (the code block is its own honest fallback).
- [ ] **Client-side island, loaded only where needed.** Mermaid is bundled locally through
  Vite (never a CDN — the published pages call nothing) and hydrated `client:visible` only
  on pages whose rendered markdown actually contains a diagram — `renderMarkdown` grows a
  way to report that (e.g. return `{ html, hasMermaid }`) so the ~half-megabyte gzip bundle
  never touches diagram-free pages. Build-time SSR was considered and rejected: mermaid has
  no DOM-free renderer, so SSR means jsdom or a Playwright dependency inside `npm run
  build`, breaking "builds anywhere". The bundle cost lands as a new documented row in
  performance.md's asset table.
- [ ] **Theming, determinism, a11y.** Diagrams re-render on the `data-theme` toggle (observe
  the attribute; palette via `themeVariables` driven from `--hf-*` tokens where mermaid
  allows). Diagram ids are seeded deterministically per fence (the duplicate-id a11y probe
  and multi-file notes with several diagrams are the forcing cases). Each rendered diagram
  gets an accessible name (`role="img"` + label from the fence's first line or an
  accompanying caption); the overflow wrapper gets `tabindex="0"` (which is what the a11y
  sweep's unfocusable-scroller probe keys on — there is no class allowlist for scrollers),
  and if a wide diagram can exceed the viewport its class joins the sweep's
  horizontal-overflow *culprit filter* instead.
- [ ] Config: `markdown: { mermaid: boolean }` (default `true`), the first key of a new
  `markdown` block in the config schema.

Done when
- [ ] A fixture README and a two-diagram multi-file note render as SVG in both themes with
  the a11y sweep green; the imported-repo fixture (`charlie`, which already carries XSS
  payloads) renders its fence as an inert code block; diagram-free pages ship no mermaid
  bytes.

Tests
- [ ] Unit: `markdown.test.ts` — trusted fence → container, untrusted fence → escaped code
  block (extend the existing that-content-cannot-execute guards), deterministic ids,
  `hasMermaid` reporting, non-mermaid fences unaffected.
- [ ] UI (e2e): diagram renders on a fixture repo README and a note (fixtures added in
  `tests/e2e/global-setup.ts`, which automatically feeds the a11y inventory); theme toggle
  re-renders; the untrusted fixture stays inert; a network/asset assertion that non-diagram
  pages don't load the bundle.
- [ ] Data model: none — rendering is entirely site-side; the artifact is unchanged.

---

## Phase 5 — Base path

Goal: the site can be served from a sub-path (version-2.md "e.g. `/mysite` as the root").
This is the deepest cross-cutting change in 0.2.0 — the documented contract today is
"every link frznforge emits is root-absolute" — and it lands *before* hosting (Phase 6) so
hosted routes are born base-aware.

Ships
- [ ] `site.base` config key (normalised: leading slash, no trailing slash). `astro.config.mjs`
  becomes `astro.config.ts` and imports `resolveConfig` (the pattern `src/content.config.ts`
  already proves) to set Astro's `base` — one config-read path, no duplicated key.
- [ ] One choke point: the URL builders in `src/lib/routes.ts` (repoUrl, treeUrl, blobUrl,
  rawUrl, commit/branches/tags/releases/insights/archive/note/org helpers, the index
  constants, `allRoutes`) apply the base through one *guarded* helper —
  `import.meta.env?.BASE_URL ?? '/'` with a settable fallback — because Vite inlines
  `BASE_URL` into server *and* island bundles, but `routes.ts` is also imported entirely
  outside Vite (`scripts/measure-build.ts` under tsx, the Playwright suite), where a bare
  module-scope `import.meta.env.BASE_URL` read would throw. `format.ts`/`listing.ts` stay
  config-free, and every helper-built URL — including most of the search index
  (`src/lib/search.ts`) — inherits the prefix. The index's two hardcoded page entries
  (`'/'` and `'/repos/'`) do *not* inherit it and join the sweep below (or move onto the
  routes constants).
- [ ] The hardcoded-literal sweep, every one now base-aware: `Sidebar.astro` (`/`,
  `/repos/`, `/notes/`, `/orgs/`), `Base.astro` (`/logo.png`, `/favicon.ico`),
  `404.astro`, `RepoHeader.astro`, `pages/index.astro`, `RepoCard.svelte`
  (`/repos/<slug>/`, `/repos/?tag=`), `RepoListing.svelte`'s `basePath = '/repos/'`
  default prop (the main listing relies on the default for its no-JS pager and tag chips),
  the repo overview's own `/repos/?tag=` links (`src/pages/repos/[slug]/index.astro`),
  `src/lib/search.ts`'s two literal page entries, and `CommandPalette.svelte`'s
  `fetch('/search-index.json')`. The Done-when dist grep is the backstop for anything this
  list still missed.
- [ ] Docs: `configuration.md`'s root-absolute contract rewritten;
  `deploying.md` gains the sub-path deploy recipe (GitHub Pages project sites being the
  motivating case).

Done when
- [ ] A build with `site.base: '/mysite'` served from that prefix works end-to-end: sidebar
  nav, repo cards, deep tree/blob/raw links, archives, the palette (open, search, jump),
  favicon — with zero root-absolute leaks (assert by grepping dist for `href="/` outside
  the base).

Tests
- [ ] Unit: routes helpers under a mocked `BASE_URL` (default base asserts today's exact
  URLs so nothing regresses); the three sync suites re-run green — they consume the same
  helpers, which is the point of the choke-point design.
- [ ] UI (e2e): a second, deliberately small base-path build (fewer fixture repos — the main
  e2e build is already the suite's dominant cost) served under a prefix by `serve.ts` in a
  new prefix mode; asserts nav, palette fetch, raw/archive URLs, search-index doc URLs.
- [ ] Data model: none — the artifact stores no *site-relative* URLs; the URLs it does
  store (source `webUrl`/`cloneUrl`, repo links, release URLs) are external and unaffected
  by `site.base`.

---

## Phase 6 — Static-site hosting

Goal: a repo that *is* a static site can be served *as* a site at a top-level path, while
its normal forge view stays at `/repos/<slug>/` (version-2.md's `my-site` / `gh-pages` /
`/mysite` example).

Ships
- [ ] **Config.** New top-level `hosting` block: an array (consistent with
  `organizations[]`, diverging deliberately from version-2.md's object sketch)
  `hosting: { sites: [{ repo: 'my-site', slug?: <defaults to repo slug>, branch?: <auto> }] }`.
  Unset `branch` resolves in version-2.md's stated order — `gh-pages`, then `main`, then
  `master` — falling back with a warning if none exists. `repo` is a plain string (typos
  become build warnings, the `org:` precedent), but *structural* errors are hard config
  errors via zod refinement: duplicate hosted slugs, and **reserved paths** — `repos`,
  `notes`, `orgs` (the version-2.md three) plus everything else the build owns:
  `_astro`, `search-index.json`, `404.html`, `index.html`, `logo.png`, `favicon.ico` — with
  a build-time collision check against the user-owned contents of `public/`.
- [ ] **Ingest.** A hosted branch always gets a `RefTree` — exempt from the `branchTrees`
  cap (a dormant `gh-pages` is exactly the branch the cap would drop) — and its files are
  exempt from `maxBlobBytes` (a built site's bundles and images routinely exceed 512 KB;
  without the exemption the hosted site 404s silently), bounded instead by a
  `hosting.maxFileBytes` (generous default). New warnings: `hosting-unknown-repo`,
  `hosting-branch-missing`, `hosting-file-unservable` (`#`/`%` paths, the standing rule).
  **This bumps `SCHEMA_VERSION`** (to 6 from today's v5 — one more if a Phase 3 sqlite
  adoption bumped first): the blob-cap exemption changes what `FileInfo.stored` means for
  hosted refs, and the artifact records which ref each hosted entry resolved to (so the
  site build and the sync tests consume the artifact, not a re-derivation) — the full
  same-change update (bump, data-model.md, snapshot + extractor tests, changelog entry,
  sync tests).
- [ ] **Emission.** A `hostedRoutes(data, cfg)` enumerator in `src/lib/routes.ts`, folded
  into `allRoutes()` so the exhaustive-route sync contract survives, feeding a static
  endpoint (`src/pages/[hosted]/[...path].ts`-style, the raw-endpoint pattern: bytes via
  `readBlobBuffer`, content type via `src/lib/mime.ts`), with `/<slug>/` serving the
  branch's `index.html`. Base-aware from birth (Phase 5). The docs state the cost plainly:
  hosted files multiply page count exactly like the `branchTrees` lever, and
  `npm run measure` learns the new route family.

Done when
- [ ] A fixture repo with a `gh-pages` branch serves its `index.html` at `/mysite/` with
  correct content types for its assets, while `/repos/my-site/` still renders the normal
  repo view; a config claiming slug `repos` fails the build with a hard error naming the
  reserved set.

Tests
- [ ] Unit: config validation (reserved set, duplicate slugs, `public/` collision); branch
  resolution order; cap exemption (extend `branch-cap.test.ts`: hosted branch treed beyond
  the cap); blob-cap exemption boundaries + `hosting-file-unservable`; `hostedRoutes`
  joined into the sync suites *in both directions* (every hosted file has a route; every
  hosted route resolves to stored bytes); dangling `repo`/`branch` warnings in the
  `orgs.test.ts` style; schema + snapshot regenerated for the bump.
- [ ] UI (e2e): the hosted fixture site loads and its assets resolve; the repo view
  coexists; `a11y.spec.ts`'s page inventory makes a *deliberate, documented* decision to
  exclude hosted pages from the contrast/heading gate — they are arbitrary user content,
  not frznforge chrome, and gating them would fail spuriously.
- [ ] Data model: the v6 bump above, executed as one change with its docs, snapshot and
  sync tests — this is the phase where the `TODO` data-model rules bite hardest.

---

## Phase 7 — Logo refresh

Goal: version-2.md's UI item — simplify `public/logo.png` (keeping the frozen-anvil-with-
flames aesthetic) and actually integrate the mark into the UI. Today the 1254×1254, ~1.2 MB
PNG is referenced exactly once — as a favicon on every page — and the visible sidebar brand
is an unrelated `#i-flame` sprite icon.

Ships
- [ ] A simplified mark with an SVG master (committed under `docs/` or `src/assets/`);
  regenerated small `public/logo.png` and matching `public/favicon.ico` (the 1.2 MB
  favicon-on-every-page is the quiet bug this fixes; keep the hrefs stable in
  `Base.astro` — they are base-aware after Phase 5).
- [ ] The mark integrated as a sprite symbol in `IconSprite.astro`, replacing `#i-flame` in
  `Sidebar.astro`'s `.hf-brand-mark` (adjusting the `.hf-brand-mark` sizing/gradient rules
  in `global.css` as needed, both themes, both palettes). Decorative usage carries
  `aria-hidden` so the a11y sweep stays clean.

Done when
- [ ] The new mark ships in sidebar + favicon, looks right in light/dark × hearth/frost, and
  the favicon payload is small (add a size guard so the 1.2 MB regression can't return).

Tests
- [ ] UI (e2e): icon links resolve in the built dist; a dist-size assertion on the icon
  files; a11y sweep green (the brand link keeps its accessible name — the WCAG 2.5.3 fix
  from 0.1.0 must survive the swap).
- [ ] Data model / unit: none — no artifact or logic change.

---

## Phase 8 — Web wizard: whole-config editing

Goal: `npm run frznforge -- init --web` grows from a repo picker into the configuration
surface version-2.md describes: owner/profile settings, `profile.md` editing, organizations,
ingest settings, and **every setting 0.2.0 added** (`theme.heat`, `ingest.maxCommitAgeDays`,
`ingest.reuse`, `site.base`, `hosting`, `markdown.mermaid`). Last phase before release
because of that dependency.

Ships
- [ ] **A config-edit engine** (`scripts/lib/config-edit.ts`) built on the existing
  string/comment-aware primitives (`matchBracket`, `stripComments`, `quote`):
  `setObjectField(source, path, value)`, `insertIntoArray` / `removeFromArray`
  (parameterising today's `repos`-only `insertRepos`). The textual-edit contract from
  `scripts/cli.ts` stays load-bearing: **only the edited field's bytes change** — comments,
  formatting and expressions (`512 * 1024`) elsewhere survive byte-for-byte, and an
  expression-valued field the user did not touch is never flattened. Every written string
  goes through `quote()`-grade escaping (owner names and org descriptions are free text —
  the current config already contains an escaped apostrophe).
- [ ] **Session lifecycle.** The write-once latch becomes a serialized multi-write session:
  per-file write queue, an explicit **Done** button ends the run, one coalesced `.bak` per
  file per session, and the idle-timeout warns about unsaved editor state instead of
  silently discarding it. The security envelope is unchanged: 127.0.0.1 bind, per-run key,
  Host/Origin pinning, token never serialized to the browser, paths always resolved
  server-side.
- [ ] **Sections.** `/api/config` serves current values + schema defaults — derived
  per block from the sub-schemas that carry them (`FrznforgeConfigSchema.parse({})` itself
  throws: `owner` has required fields and no prefault) — so the page never duplicates a
  default. An **Owner** card (name/handle/profile path); an **Ingest settings** card in a
  `<details>` accordion (version-2.md's ask), covering the full `ingest` block including
  the new 0.2.0 keys, with current-vs-default display; an **Organizations** card
  (add/edit/remove `organizations[]` entries including their nested `repos` arrays, plus
  scaffolding `content/orgs/<slug>.md` from the `new`-command template); **Hosting**,
  **theme**, and **markdown/base** fields grouped sensibly. "Configure everything" means
  everything: the remaining top-level keys — `site`, `listing.pageSize`, the
  `content`/`notes` paths — are cheap schema-driven fields in a general card, and a
  **Sources** card lists existing `repos[]` entries with remove (and the field edits the
  array splicer makes cheap: slug, org, releases mode); adding sources stays the picker's
  job.
- [ ] **Profile editor.** `GET/POST /api/profile` for `content/profile.md`, path pinned
  server-side from the resolved config (the browser never names a file — the standing
  rule). The YAML frontmatter block round-trips untouched; the editor edits the body.
  The WYSIWYG: version-2.md names **retoken**, which is not the npm package of that name
  (that is an unrelated 2022 tokenizer) — resolve the intended editor and vendor it into
  the page or a local asset route (the CSP forbids external loads); ship the body editor
  as a textarea with a server-rendered preview first so the phase does not block on the
  dependency, and slot the WYSIWYG in when resolved.

Done when
- [ ] Starting from this repo's own config, a user can — entirely from the page — edit
  owner fields, toggle an ingest setting, add and remove an organization, set a heat
  cutoff, and edit the profile body; the resulting `frznforge.config.ts` differs from the
  original *only* in the edited fields, and `npm run build` succeeds on it.
- [ ] The provider token appears in no response from any endpoint, old or new (the existing
  sweep, extended).

Tests
- [ ] Unit: a splicer suite mirroring the `insertRepos` blocks — comments, expressions,
  escaped quotes, nested arrays, idempotence, and byte-identity outside the edited field;
  endpoint suites for `/api/config` and `/api/profile` (path pinning, frontmatter
  preservation, `.bak` behaviour, malformed-payload rejection); lifecycle tests
  *replacing* the write-once tests (serialized writes, two-tab behaviour, explicit
  finish); the token-never-leaks sweep over every new endpoint.
- [ ] UI (e2e): the wizard's first Playwright spec — boot `runWebInit` against a temp
  config (a second `webServer` entry or per-spec fixture; the static server owns 4399, so
  a distinct fixed port), drive the real page through one edit of each section, assert the
  file on disk. The page JS has been untested since 0.1.0; it stops being so in the phase
  that triples it.
- [ ] Data model: none — the wizard edits config and content files, never the artifact.

---

## Phase 9 — Docs, checklist, cut 0.2.0

Goal: everything a stranger needs, then the release.

Ships
- [ ] Docs sweep: `configuration.md` documents every key 0.2.0 added (`theme.heat`,
  `ingest.maxCommitAgeDays`, `ingest.reuse`, `site.base`, `hosting`, `markdown.mermaid`,
  the ingest CLI flags); `data-model.md` reflects the final 0.2.0 schema version and the
  new warnings;
  `performance.md` carries the Phase 2/3 measurements and decisions; `deploying.md` covers
  sub-path deploys; `importing.md`/`quick-start.md` transcripts re-verified (the
  release-checklist trigger: the ingest summary and repo pages changed).
- [ ] `docs/dev/release-checklist.md` run and extended with the 0.2.0 items that cannot be
  verified from inside the repo: click through a hosted site and a base-path deploy by
  hand, in both themes.
- [ ] `CHANGELOG.md`'s `0.2.0 (unreleased)` heading gains its release date; `VERSION`,
  `package.json` and the heading agree.

Done when
- [ ] All three gates green (`npm test`, `npm run test:e2e`, `npm run check`),
  `npm run build` clean, ingest byte-identical across runs, and the checklist's manual
  items ticked or explicitly carried over with a reason (the 0.1.0 precedent: honest
  annotations beat silent ticks).

Tests
- [ ] None new — the deliverable is the docs sweep, the checklist run, and the three gates
  staying green over everything the earlier phases shipped.

---

## Cross-cutting rules (apply to every phase — the `TODO` contract, restated)

- **Data model changes**: bump `SCHEMA_VERSION`, update `docs/dev/data-model.md`, regenerate
  the ingest snapshot, and add/adjust the sync tests *in the same change* — never across
  changes, so no intermediate commit has a desynchronized contract. Additive warning codes
  skip the bump but not the docs.
- **Both sides tested**: any feature spanning ingest ↔ site (timeframe, hosting) or
  server ↔ browser (wizard, mermaid, base path) lands with unit tests *and* UI/e2e tests in
  the same change — a feature is not done when only one side of it is proven.
- **Changelog discipline**: every change lands its `CHANGELOG.md` entry under
  `0.2.0 (unreleased)` in the right category — **Features** / **Bug Fixes** / **Other**
  (docs, refinements, and everything else) — in the same commit as the change. The section
  exists from 0.2.0's first landed change onward; nothing waits for the release to be
  written down.
- **Determinism**: no wall-clock values in `forge.json`, ever. Freshness and caching
  metadata live in `ingest.cacheDir` sidecars; time-dependent logic takes injected clocks
  (`now` parameters, `PrepareRemoteDeps.now`); the timeframe cutoff anchors to commit
  dates.
- **Browser safety**: `format.ts` and `listing.ts` never import node or config code; config
  values reach islands as serialized props (or Vite-inlined `import.meta.env.BASE_URL`).
- **The gates**: Lighthouse-grade a11y is regression-tested, not asserted — new page types
  and new interactive elements join `a11y.spec.ts`'s inventory (or record a deliberate
  exclusion, as hosting does); `npm test`, `npm run test:e2e`, `npm run check` green before
  every commit.

## Sequencing notes

- Phases 1–2 are independent of everything and of each other; start either first.
- Phase 3 is deliberately time-boxed spikes — it can run in parallel with Phases 4–7 and
  must not block them; its deliverable is the decision record. One exception: if the sqlite
  spike is adopted, its schema-version bump lands strictly before or after Phase 6's bump,
  never with both in flight — the same-change rule tolerates no two open bumps.
- Phase 4 (mermaid) and Phase 7 (logo) touch almost nothing else and can slot anywhere
  after Phase 1; they are sized as breathers between the two deep phases (5–6 and 8).
- Phase 5 must precede Phase 6: hosted routes should be born base-aware rather than
  retrofitted, and both phases touch `routes.ts`/`allRoutes()` — sequencing them avoids a
  conflict in the sync-test contract.
- Phase 8 must be last-before-release: version-2.md explicitly asks the wizard to expose
  "any new settings added in this version", which only exist once Phases 1–6 land.
