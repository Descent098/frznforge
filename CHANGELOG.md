# 0.2.0 (unreleased)

Plans: [docs/dev/plans/version-2-phased.md](docs/dev/plans/version-2-phased.md).

## Features

- **The recency accent is configurable.** `theme.heat` in `frznforge.config.ts` sets the day
  boundaries of the fire→ice accent (`{ hot: 7, warm: 30, neutral: 180, cool: 365 }` by
  default, strictly ascending, validated). The boundaries travel as data — `.astro` pages
  read them from config at build time, Svelte islands receive them as a `heatDays` prop next
  to `now` — so `format.ts` stays browser-safe and server + client render identically. The
  profile and organization KPI copy ("N touched this week", "+N in the last 7 days") now
  reads the same `hot` boundary, so the stats and the orange accent always agree; with a
  non-default `hot` the copy says "in the last N days". The colours themselves are *not*
  configurable: they are the palette's `--hf-ember`/`--hf-ice` tokens, pinned to WCAG AA by
  the contrast tests.
- **Ingest can be limited to recent history.** `ingest.maxCommitAgeDays` keeps only commits
  from the last N days (via `git rev-list --since-as-filter`, git ≥ 2.37). The cutoff is
  anchored to the repo's newest commit date, never to the clock, so the artifact stays
  byte-identical for the same commits no matter when or where it is built; a dormant repo
  keeps its newest N days rather than going empty, and every branch always keeps its head
  commit (even a clock-skewed head the filter would drop). History dropped by the age
  cutoff raises a `commits-aged-out` warning (when `maxCommits` truncates first,
  `commits-capped` is reported instead), and everything derived from the commit list —
  `commitCount`, `createdAt`, contributors, insights — narrows with it. Composes with
  `ingest.maxCommits` (whichever cuts first).
- **Repeat builds skip work they have already done — without changing a byte of output.**
  Two mechanisms under the new `ingest.reuse` config (`{ enabled: true, maxAgeMinutes: 2 }`),
  with every piece of bookkeeping confined to `ingest.cacheDir` sidecars so `forge.json`
  keeps its no-timestamp determinism guarantee:
  - a **freshness window**: under `fetch: 'auto'`, a remote source whose last fetch fully
    succeeded less than `maxAgeMinutes` ago is not re-fetched — its cached provider answers
    and mirror are used as-is, with no warning, because they are exactly what a fetch would
    have returned. A source whose last fetch failed, was rate-limited or served stale is
    **always** retried, so a rate-limited bulk refresh heals itself run by run instead of
    freezing the gaps in place. A window-skip never extends the window.
  - a **per-repo scan cache**: a repo whose refs, HEAD, metadata inputs (provider
    description/links/releases included) and scan options are unchanged since the last run
    skips `git` entirely and replays the recorded result, reading blob and archive bytes
    back from the content-addressed stores. Anything missing, stale or corrupt degrades to
    a quiet re-scan — never to a wrong artifact or a failed build. On this repository's own
    site a warm re-ingest drops from ≈2.0 s to ≈0.23 s (−89%; see
    [performance.md](docs/dev/performance.md)).
- **`npm run ingest -- --no-cache`** — ingest's first CLI flag: fetch everything, read no
  provider cache, replay no scan cache, for one run (fresh results are still recorded, so
  the next ordinary run benefits). Unknown flags are an error, not a shrug.
- **```` ```mermaid ```` fences render as diagrams** (`markdown.mermaid`, default on) —
  everywhere markdown renders, imported repos' READMEs and release notes included:
  importing a repo is choosing to publish its content, so its diagrams are the owner's
  call the same way its prose is. The imported-content renderer still strips raw HTML and
  filters URLs regardless, and mermaid runs with its own strict-mode sanitiser. The build
  stays static and deterministic: pages ship the escaped diagram source (an honest code
  block, which is also the no-JS fallback), and a locally bundled mermaid — never a CDN —
  renders it in the visitor's browser, loaded only on pages that actually hold a diagram
  and only once one nears the viewport. Measured: a diagram-free page still transfers the
  same ~48 kB of JS it did before; diagrams re-render on the theme toggle, get per-diagram
  deterministic ids (multi-diagram pages keep the duplicate-id a11y probe green), and
  carry `role="img"` with a scrollable, keyboard-focusable container. The bundle-size
  arithmetic is in [performance.md](docs/dev/performance.md).
- **The site can deploy under a sub-path** (`site.base`, e.g. `'/mysite'` for a GitHub
  Pages *project* site). One choke point (`src/lib/base.ts`, reading Astro's inlined
  `BASE_URL`) prefixes every URL the site emits — pages, file/raw/archive routes, the
  favicon links, the command palette's `search-index.json` fetch and every document inside
  it — and the handful of hand-written chrome links were swept onto it. The old
  "every link is root-absolute" contract still holds for root deploys, byte for byte.
  Astro's config became `astro.config.ts` so `base` comes from `frznforge.config.ts`
  instead of a second, driftable copy. Guarded so `npm run measure` and the test tooling
  (which load the URL builders outside Vite) keep working, and `FRZNFORGE_BASE` joins the
  test-suite env overrides. The e2e suite now builds the fixture site a second time under
  `/mysite`, serves it prefix-mounted (requests outside the prefix 404, like a real
  sub-path host), drives navigation and the palette through it, and scans every built HTML
  file for root-absolute leaks — zero tolerated.

- **A repo can be served as a real static site** (`hosting.sites`, **artifact schema v7**):
  point an entry at a repo and its `gh-pages` branch (or an explicit branch; the fallback
  order is `gh-pages` → `main` → `master`) is served file-for-file at `/<slug>/…` — with
  correct content types, `index.html` directory resolution, and the repo's normal forge
  view intact at `/repos/<slug>/`, gh-pages browsable like any ref. The hosted branch
  always gets a tree (exempt from the `branchTrees` cap — a dormant `gh-pages` is exactly
  the branch the recency cap would drop) and its files are stored up to
  `hosting.maxFileBytes` (default 20 MiB) instead of `maxBlobBytes`, because a built
  site's bundles routinely exceed 512 kB and an unstored file is a silent 404. Reserved
  slugs (`repos`, `notes`, `orgs`, `_astro`, …), duplicates and collisions with `public/`
  files are hard errors; a mistyped repo or missing branch degrades to a warning
  (`hosting-unknown-repo` / `hosting-branch-missing`), the organizations way. The
  resolution is recorded in the artifact, every hosted file is a real route in the
  exhaustive sync contract, hosted pages are deliberately excluded from the a11y sweep
  (they are the user's own content, not forge chrome), and it all composes with
  `site.base`. Costs are stated honestly: hosted files multiply page count like the
  `branchTrees` lever, and `npm run measure` reports them as their own line.

- **The init wizard edits the whole config now, not just the repo list**
  (`npm run frznforge -- init --web`). Two new page sections: **Settings** — site
  title/URL/description/`site.base`, owner fields, palette and `theme.heat`,
  `markdown.mermaid`, listing/notes/content paths, the full `ingest` block (reuse and
  insights included) behind an accordion, plus list editors for organizations, hosted
  sites and the sources already in the config — and **Profile**, a body editor for
  `owner.profile` with a server-rendered preview in which a YAML frontmatter block
  round-trips byte-for-byte. Every edit is textual and field-precise: comments,
  formatting and expression values (`512 * 1024`) survive everywhere but the field being
  changed (`scripts/lib/config-edit.ts`, the same engine the repo splicer now shares).
  Settings edits are judged by the config schema *before* the file moves — a descending
  `theme.heat` or a reserved hosting slug is refused with the schema's own message — and
  verified after: the wizard re-loads the written file in a fresh process and rolls back
  unless it parses to exactly the config that check approved (so an edit that landed in a
  comment or a key the editor cannot follow is undone, not silently misapplied). The
  session became multi-write: serialized
  writes (two tabs can no longer lose each other's saves), an explicit **Done** button,
  and one coalesced `.bak` per file per session holding the pre-wizard state. The
  security envelope is unchanged and now tested over every new endpoint: loopback-only,
  per-run session key, Host/Origin pinning, a server-side allow-list of editable fields,
  paths always resolved server-side, and the provider token in no response. The page
  gained its first Playwright suite along the way.

## Bug Fixes

- File tables no longer lose their per-file "Last commit" dates and subjects when
  `ingest.maxCommits` (or the new `ingest.maxCommitAgeDays`) narrows the kept history:
  per-path last commits and tag/release target commits that fall outside the kept list are
  now carried in a separate `Repo.extraCommits` map (**artifact schema v6**), each with its
  own `/commit/` page, and the site resolves display lookups through both maps. The
  `maxCommits` half of this predates 0.2.0 — a mature repo with a commit cap showed dashes
  for most of its front-page file table. Aggregates are deliberately untouched:
  contributors, insights, activity, the contribution graph and every count still read only
  the narrowed `commits`, so the limiting knobs keep their meaning.

## Other

- **The logo grew up (and shrank 60×).** The mark is now a hand-drawn vector — a navy
  anvil with iced-over edges, icicles under the face, and a flame in the site's ember→ice
  brand gradient — committed as `src/assets/logo.svg` and rendered to assets by
  `scripts/render-logo.ts` (Playwright chromium; no image toolchain added). `public/logo.png`
  drops from a 1254×1254, 1,218 kB illustration — downloaded as a favicon on every page —
  to a 21 kB 512px render, and `favicon.ico` is regenerated at 16/32/48 (3.3 kB). The
  sidebar brand tile now shows the anvil (`#i-anvil` sprite symbol) instead of the generic
  flame, and `tests/unit/assets.test.ts` guards the formats and sizes so the megabyte
  favicon cannot return.
- Pages render two at a time (`build.concurrency: 2` in `astro.config.mjs`) — measured ~8%
  faster than Astro's default of 1 on the self-build, with 4 measurably worse. The other
  two 0.2.0 performance investigations were measured and **rejected, with the numbers
  recorded**: skip-unchanged-pages (the 0.1.0 rejection stands, and `ingest.reuse` moved
  its revisit bar further away) and sqlite for the blob store (the entire store reads in
  45 ms on the self-build — there is nothing there to win). All three write-ups are in
  [performance.md](docs/dev/performance.md).

# 0.1.0 (2026-08-24)

First release. frznforge turns a set of git repositories into a **static, read-only forge
site** — profile, repository listing, file browser, history, single-commit pages, branches,
tags, releases, insights, notes and organizations — built once and served from any file host.
No server, no database, no accounts, issues, pull requests or stars. Artifact schema v5.

Everything below is implemented and tested, and builds this project's own site. Known
limitations are listed under [Status in the README](README.md#status).

## Features

- **The shape of the thing.** `npm run ingest` reads git through the CLI and writes one
  validated JSON artifact (`data/forge.json`, schema v5) plus a content-addressed blob store;
  `astro build` turns that artifact into static HTML and nothing else. Only *committed*
  content is ever read — never your working tree. Ingest is **deterministic**: the same
  repositories at the same commits produce a byte-identical artifact (no clock values, no
  unsorted iteration). Nothing about a single repository can fail a build — every problem is a
  coded warning, printed at ingest and counted in the site footer. The published pages call
  nothing: no CDN, no fonts, no analytics, no forge API.

- **Configuration.** `frznforge.config.ts` is the whole site config — site, owner,
  `theme.palette`, repo sources, ingest limits, listing page size — validated with zod through
  `defineConfig()`. Per-repo metadata can also live in the repo itself as a committed
  `.frznforge.json` (name, description, links, tags, `template`, license override, release
  mode), overridable from the site config.

- **Ingest.** Repos are scanned concurrently. Per repo: merged metadata, default branch,
  branches, annotated and lightweight tags, full commit history with per-commit file stats
  from numstat, per-ref file trees with last-commit info per path, per-file size/binary/
  language, language breakdown, contributors, README and license detection, source zips built
  with `git archive`, and browsable file content in the blob store (text and binaries, under a
  size cap). Odd repos are emitted rather than skipped: an empty repo, a tree wiped by its
  last commit, and an unborn HEAD with work on another branch all build with a warning
  (`repo-empty`, `default-branch-empty-tree`, `default-branch-fallback`); a path that is not a
  git repository is skipped with `repo-not-found`. `npm run build` runs ingest first.

- **The site (Hearth design).** Global layout with a docked sidebar, light/dark toggle (`t`)
  and a build-time colour palette (`hearth` warm or `frost` cool). Profile page from
  `content/profile.md` — frontmatter links/location/pinned repos plus the rendered body, with
  real aggregate stats from the artifact. Repository listing with search, language/tag/kind
  filters, sorting and 50-per-page pagination as a URL-synced Svelte island that **renders
  fully without JavaScript**. Repo overview with the metadata/About panel, template banner,
  language bar, contributors, latest commit, root file table, rendered README, clone panel and
  an honest empty-repo state. 404 page.

- **Repo depth.** A file browser for the default branch, every browsable branch and the newest
  `ingest.tagTrees` tags, with per-path last-commit info and a ref switcher that works without
  JS; file view with build-time Shiki highlighting (light and dark), a CSS line-number gutter
  and `#L42` anchors, a markdown Preview/Source toggle, image preview, and symlink/binary/
  too-large fallbacks; raw and download endpoints; source zip downloads with sizes; paginated
  commit history and single-commit pages with per-file +/−; branches, tags and releases pages.

- **Profile extras and the command palette.** A contribution heat map (52 weeks; hue = recency
  fire→ice, intensity = commit-count quartile; owner identities configured in `profile.md`;
  streak and busiest-day footer), a recent-activity event log (pushes grouped per repo/branch/
  day, plus tag events), and a `Ctrl`/`Cmd`+`K` command palette with fuzzy search over repos,
  default-branch file paths, notes and pages from a build-time `/search-index.json`, plus
  actions (toggle theme, copy clone URL). `/` opens it too; the sidebar search box is wired to
  it; it is keyboard-only operable and works offline.

- **Importers — GitHub, GitLab, Gitea, Forgejo.** Repos hosted elsewhere can be listed in the
  config instead of pointed at locally: frznforge reads metadata and releases over the
  provider REST API, mirror-clones them into `ingest.cacheDir`, and runs the same scanner over
  the mirror, so every local feature works for imported repos. Tokens come from environment
  variables only — never written to config, never logged, never in the artifact — and public
  repos work anonymously. Provider **releases** (name, markdown notes, prerelease flag,
  author, assets with sizes) render on the releases pages and label their source; local repos
  fall back to annotated tags, and `releaseMode` can force tags per repo. An unreachable,
  unauthenticated or rate-limited forge never fails a build — it warns
  (`remote-fetch-failed`, `remote-auth-missing`, `remote-rate-limited`, `remote-cache-stale`)
  and falls back to the local mirror. `ingest.fetch: 'auto' | 'never' | 'always'` controls
  network use; `'never'` builds fully offline from the cache. Markdown from imported repos
  (READMEs, release notes) renders in an untrusted mode that strips raw HTML and unsafe URL
  schemes, since it comes from repos you may not control.

- **`npm run frznforge -- init`** fills that config in for you: pick a provider, walk an
  account's repositories, multi-select, and have the entries written into
  `frznforge.config.ts` with a backup and a confirmation. Non-interactive flags
  (`--provider`, `--account`, `--select`, `--print`, `--yes`) for scripting. Selection
  specifiers let `all` carry exclusion filters — `all-nf` (no forks), `all-na` (no archived),
  `all-np` (no private) — concatenated (`all-nfna`) or dash-separated (`all-nf-na`), in any
  order, case-insensitive, and the command reports what it dropped and why
  (`selected 71 of 118 (excluded 32 forks, 15 archived)`). `--web` does the same job in an
  interactive local page: browse the account in a filterable table, choose the release source,
  preview the exact snippet, write it. The server binds to 127.0.0.1 only, requires a per-run
  key in the URL, checks `Host`/`Origin` against DNS rebinding, and never sends the provider
  token to the browser — the page asks the local server, which calls the provider. `--port`
  and `--no-open` control it.

- **`npm run frznforge -- new <dir>`** scaffolds a fresh site: `frznforge.config.ts`,
  `content/profile.md`, `content/notes/`, `content/orgs/`, `.gitignore` and a README, so a new
  user starts from working files instead of a blank directory. It never overwrites an existing
  file; `--dry-run` prints the file list and `--force` writes into a non-empty directory.
  There is no published npm package, so `new` writes only the files you author — see
  [starting-a-site.md](docs/user/starting-a-site.md).

- **Notes** are a gist-style folder (`content/notes/`): a file is a single-file note, a
  subfolder is a multi-file note. Markdown notes take optional frontmatter (title,
  description, date, tags) and render with the same Preview/Source toggle as repo files;
  everything else gets Shiki-highlighted source with a line gutter; images render inline; every
  stored file has a raw URL. There is a searchable notes index, and notes join the palette.
  Note content shares the content-addressed blob store with repo files.

- **Organizations** group repos under a named umbrella with their own overview page — hero,
  aggregated KPIs and languages, a markdown profile from `content/orgs/<slug>.md`,
  pinned/member repo cards, and a `/orgs/<slug>/repos/` listing that reuses the same listing
  island. Membership can be declared from either side (`organizations[].repos` or `org:` on a
  repo source); mismatches warn (`org-unknown-repo`, `repo-unknown-org`) instead of failing.

- **Insights** — a per-repo `/insights/` page with three monthly charts over the default
  branch: commits, distinct contributors, and code size. Commits and contributors are exact
  (bucketed from the commit list already in the artifact); code size is measured at up to
  `ingest.insights.samples` evenly spaced checkpoints, each one an `ls-tree` plus a bounded
  `cat-file --batch`, so a repo with ten thousand commits costs the same as one with three
  hundred. Quiet months are drawn as real gaps rather than closed up, the x-axis is a true
  month scale, and every chart carries a screen-reader data table with a caption. The page is
  honest about its own limits: a repo with one month of history says so, sampled series say
  how many checkpoints were measured out of how many months with commits, and a checkpoint
  that exceeded `ingest.insights.maxBytesPerSample` reports bytes without a line count and
  raises `insights-approximate`. Code size deliberately counts a wider file set than the
  language bar (it keeps prose and unknown-language files, dropping only vendored paths and
  binaries) and the UI says so. Configured under `ingest.insights` (`enabled`, `samples`,
  `maxBytesPerSample`); `Repo.insights` is `null` for empty repos and the tab disappears.

- **Build size is now a knob, and a documented one.** `ingest.branchTrees` (default `10`,
  or `'all'`) caps how many non-default branches get a browsable file tree — the default
  branch always has one and never counts against the cap — because tree/blob/raw pages are
  generated *per ref* and are the whole page count. On a four-repo remote build this took
  16,388 pages / 5m01 down to 13,164 pages / 3m24 (−32% build time, −31% output size).
  Branches past the cap are still listed, with a "not browsable" pill and a banner instead of
  a dead link, and ingest raises `branch-trees-capped`. [docs/dev/performance.md](docs/dev/performance.md)
  gives the page-count arithmetic, the measured numbers, every knob that moves them, and what
  was considered and rejected; `npm run measure` reproduces the breakdown against your own
  artifact.

- **Accessibility and responsive behaviour are gated, not asserted.** Lighthouse accessibility
  is 100 with zero failing audits on every page type in **both** themes. Shiki now uses
  github's high-contrast themes so no code token falls below AA. Every page has exactly one
  `<h1>` (repo sub-pages get a visually hidden one) and no skipped heading levels; all rendered
  user markdown is demoted a level so a `#` in a README cannot collide with the page heading.
  Scrollable regions are keyboard-reachable, the command palette returns focus where it found
  it, and no page type scrolls sideways at 380px. Two test suites hold this:
  `tests/e2e/a11y.spec.ts` walks every page type in both themes (contrast over every visible
  text node, heading structure, duplicate ids, unfocusable scroll regions, sideways scroll,
  live `/tree/` links), and `tests/unit/contrast.test.ts` pins the palette tokens themselves
  against the surfaces and composited tints they are painted on, in all four palettes.

- **User documentation** in [docs/user/](docs/user/README.md), enough to go from zero to a
  deployed site without reading source: [quick start](docs/user/quick-start.md),
  [starting a site](docs/user/starting-a-site.md),
  [configuration reference](docs/user/configuration.md),
  [importing from a forge](docs/user/importing.md),
  [deploying](docs/user/deploying.md) (build size, a working Actions workflow, host-by-host
  settings), and [migrating from a forge](docs/user/migrating.md).

## Bug Fixes

- Repo file routes no longer break on URL-special characters in a committed path. Every
  `tree`/`blob`/`raw` segment (and the ref slug) is percent-encoded, so a file named
  `read me.md` gets a valid link instead of `href="…/read me.md/"`, and a `%` in a name no
  longer aborts `astro build`. `#` and `%` cannot round-trip a static host at all, so those
  paths are listed in the file table without a link and ingest raises `repo-path-unservable` —
  the same rule the notes side already applied.
- Light-theme text colours failed WCAG AA. The ink tokens in both palettes were retuned to the
  lightest values clearing 4.5:1 against every surface they are painted on — including each
  colour's own `-soft` tint flattened over card and page — and the "cool" repo-age accent no
  longer uses a 2:1 blue on light. Dark-theme ice was lifted where a chip tint stacks on the
  ref switcher's current-row tint.
- Syntax highlighting shipped four sub-AA token colours from `github-light` (worst: 3.2:1) and
  a comment grey that was being patched by hand in CSS. Both themes moved to github's
  high-contrast variants and the per-literal override is gone.
- The sidebar owner link and theme toggle failed WCAG 2.5.3 "Label in Name" on every page:
  their `aria-label`s replaced the visible text rather than containing it. The purpose is now
  appended in a visually hidden span, and the mobile layout clips those labels instead of
  removing them, so both controls keep an accessible name when the sidebar collapses. The
  commit page's copy button had the same defect and now names the sha it copies.
- The repo overview and the profile had broken heading structure — a skipped level when a
  README starts at `#`, and a second `<h1>` from rendered user markdown. All rendered markdown
  is demoted a level, the About sidebar's headings moved up to `h2`, and repo sub-pages that
  had no `<h1>` at all now emit a visually hidden one.
- Code views no longer render a phantom trailing line: a file ending in a newline made Shiki
  emit one extra `.line`, so the gutter showed one more line than the "N lines" label.
  Affected every repo file view and, once they landed, note files. Line counting now uses
  editor semantics throughout, so a file without a trailing newline is no longer undercounted.
- A multi-file note emitted duplicate `id="L1"` anchors, one set per file. Highlighted output
  takes an id prefix, so line anchors on multi-file pages are `f-<file>-L5`; single-file pages
  keep bare `L<n>`.
- The command palette dropped focus into the void on close. It records what was focused when
  it opened and restores it on Escape, overlay click and cancelled runs.
- Scrollable regions with no focusable content — markdown code blocks and the contribution
  graph — could not be scrolled by keyboard. They now carry `tabindex="0"`, and the graph got
  a labelled `role="group"`.
- The repo overview's commit bar pushed the whole document sideways below ~480px, the only
  page type that did. It wraps now.
- `summary` elements had no visible focus ring.

## Other

- **Tests.** `npm test` — vitest unit tests against fixture git repos built in temp dirs under
  deterministic dates: every extractor, the empty-state gotchas, uncommitted-work exclusion,
  determinism (byte-identical re-ingest), a redacted artifact snapshot, listing/format
  helpers, route mapping, insights sampling, palette contrast. `npm run test:e2e` — Playwright
  builds fixture repos → ingest → `astro build` into `tests/.tmp/e2e`, then drives the static
  site (listing with and without JS, deep links, file browser, ref switcher, history,
  releases, notes, orgs, insights, 404, and the two-theme accessibility sweep). Sync tests
  assert artifact ↔ page parity in both directions. No test touches the network.
  `npm run smoke:remote` is the opt-in live check that builds a throwaway site from one small
  public repo on each provider.
- **Developer docs.** [docs/dev/data-model.md](docs/dev/data-model.md) (artifact layout, every
  type, warning codes, the schema-bump rule),
  [docs/dev/performance.md](docs/dev/performance.md),
  [docs/dev/release-checklist.md](docs/dev/release-checklist.md), and
  [docs/dev/plans/plan-phases.md](docs/dev/plans/plan-phases.md).
- `VERSION`, `package.json` and the changelog heading all read `0.1.0`.
- LICENSE (MIT) and a committed `.frznforge.json` for this repo.
- **Design history.** Four static explorations (profile + repo page each, plus an index) were
  built under `src/pages/designs/` — Ember, Frost, Anvil, Hearth — throwaway, wired to no
  data, purely to pick a direction. Hearth won and became the real site; the rest are archived
  in [docs/dev/plans/design-options/](docs/dev/plans/design-options/README.md) and no longer
  ship. Frost's colours were liked too, which is why `theme.palette: 'hearth' | 'frost'`
  exists as a build-time warm/cool switch.
