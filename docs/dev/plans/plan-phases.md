# Delivery phases

Companion to [plan.md](plan.md). That file is the *what*; this one is the *in what order*.
Each phase ends in something runnable and demoable on its own. Phases are ordered so
that the data pipeline always lands one phase ahead of the UI that consumes it, and so
the product is usable (if thin) from Phase 2 onward.

Conventions used below:

- **Ships** – the user-visible or developer-visible result when the phase is done.
- **Done when** – the acceptance bar. Nothing moves to the next phase until these hold.
- **Tests** – what must exist per the rules in `TODO` (unit + UI for anything crossing the
  ingest ↔ site boundary; sync/regression tests whenever the JSON data model changes).
- Every phase bumps `VERSION` and gets a `CHANGELOG.md` section per the release rules.

---

## Phase 0 — Foundation & direction ✅ *(done 2026-08-23)*

Goal: agree on architecture and look before writing real code.

Ships
- [x] `plan.md`, this file.
- [x] Four throwaway design explorations (profile page + repo page each) under
  `src/pages/designs/*`, static content only, to pick a visual direction.
- [x] Decision record for: config file format, JSON artifact shape (high level), where
  ingest code lives (`src/lib/ingest` vs a separate `packages/` dir).

Done when
- [x] One design direction is chosen (or a hybrid is described in a short note in
  `docs/dev/plans/design-decision.md`).
- [x] The unused explorations are deleted or moved to `docs/dev/plans/design-inspo/`.

Tests: none (no product code yet).

---

## Phase 1 — Ingest core (local repos → JSON) ✅ *(done 2026-08-23)*

Goal: a CLI step that turns one or more *local* git repositories into a single
`data/forge.json` (or one file per repo plus an index) that every later phase reads.
This is the contract the whole site is built on, so it gets the most tests.

Ships
- [x] `frznforge.config.(ts|json)` – site-level config: owner profile path, list of repo
  sources, output dir, pagination size.
- [x] Per-repo metadata file (`.frznforge.yml`/`frznforge.json` inside the repo, overridable
  from the site config): name, short description (≤300 chars), links (homepage, tracker,
  donations, upstream), tags, `template: true|false`, license override, release mode.
- [x] Scanner (concurrent across repos) that emits for each repo:
  - [x] identity + metadata (merged from repo file + site config)
  - [x] default branch, branch list, tag list (with annotated-tag messages)
  - [x] commit history per branch (hash, author, date, subject, body, parents)
  - [x] file tree of the default branch at HEAD (path, size, mode, last-commit ref)
  - [x] language breakdown (bytes per language, by extension/linguist-style map)
  - [x] contributors (name/email → commit count, first/last commit)
  - [x] README contents + detected license
  - [x] raw file contents (or a content-addressed blob store) for browsable files with a
    size cap and binary detection
- [x] `npm run ingest` script; `npm run build` runs ingest first.
- [x] Versioned schema (`schemaVersion` field) + TypeScript types for the artifact.

Done when
- [x] Pointing config at 2+ local repos produces a deterministic JSON artifact (same input →
  byte-identical output, modulo timestamps we explicitly choose to include).
- [x] Schema is documented in `docs/dev/data-model.md`.

Tests
- [x] Unit tests per extractor (branches, tags, tree, languages, contributors, license
  detection) against fixture repos created in a temp dir during tests.
- [x] Snapshot test of the full artifact for a fixture repo.
- [x] Schema validation test (artifact conforms to the published types).

---

## Phase 2 — Site skeleton: shell, profile, listing, repo overview ✅ *(done 2026-08-23)*

Goal: first deployable site. Thin, but every page that exists is real.

Ships
- [x] Global layout from the chosen design: sidebar nav, header search box (static for now),
  light/dark theme, fire/ice token system in plain CSS.
- [x] **Profile overview** from `profile.md` (markdown body + frontmatter): rendered README,
  links (sites, LinkedIn, email, location, workplace, school, other forges), pinned repos
  (≤10) rendered as cards. Contribution graph / top languages / event log are stubbed
  out with "coming in Phase 4" placeholders, *or* omitted entirely — no fake data.
- [x] **Repo listing** page: all repos as cards (name, short description, top 3 languages,
  last updated). Sorting (newest/oldest/name), filters (language, template/normal,
  tags), text search, 50-per-page pagination. Svelte island for the interactive parts;
  pre-rendered first page works without JS.
- [x] **Repo overview** page: metadata panel (description, links, tags, license, template
  banner), language bar, contributor list, rendered README, clone panel (copyable
  `git clone` of the upstream URL, and/or pointer to the zip from Phase 3).
- [x] 404 page.

Done when
- [x] `astro build` produces a fully static site from the Phase 1 artifact with no runtime
  server and no network calls.
- [x] Lighthouse a11y ≥ 90 on the three page types. *(100 / 100 / 100)*

Tests
- [x] Unit: listing filter/sort/search/pagination logic (pure functions, not components).
- [x] UI (Playwright or equivalent): listing filters work with and without JS; profile renders
  frontmatter links; repo page shows README and metadata from the fixture artifact.
- [x] Sync: a test that builds the site from the fixture artifact and asserts every repo in
  the artifact has a generated page and appears in the listing.

---

## Phase 3 — Repo depth: files, history, refs, downloads, releases ✅ *(done 2026-08-23)*

Goal: the repo page becomes a real read-only forge view.

Ships
- [x] File browser: directory listing with last-commit per entry; file view with
  syntax highlighting (build-time, e.g. Shiki), line numbers, line-anchor links,
  raw view, markdown preview/source toggle, image preview, "binary/too large" fallback.
- [x] Commit history page (paginated) and single commit page (message, stats, per-file ±).
  *Diff body was the documented stretch goal — deferred.*
- [x] Branches page and tags page; branch switcher on the repo overview/file browser.
- [x] Source zip download (via `git archive` at ingest) for the default branch and the
  newest `ingest.tagTrees` tags, size shown in the UI. *Per-non-default-branch zips
  deliberately skipped (rarely used; browsable trees exist for all branches).*
- [x] Releases: from annotated tags (tag message rendered as markdown) for plain repos.
  Forge-imported releases come in Phase 5; the UI and data shape are built here.

Done when
- [x] Every path in the fixture repo's tree is reachable by URL and renders.
- [x] Build time budget < 60 s: self-build (151 pages) ≈ 9 s; ingest + astro print timings
  in build output. *(No CI yet — timings live in the build log.)*

Tests
- [x] Unit: path → route mapping, highlighting language detection, zip manifest.
- [x] UI: navigate tree → file → raw; switch branch; download link resolves.
- [x] Sync: artifact tree ↔ generated file routes 1:1; tags in artifact ↔ release pages.

---

## Phase 4 — Profile extras, search & command palette ✅ *(done 2026-08-23)*

Goal: the parts that make it feel like *your* page rather than a directory.

Ships
- [x] Contribution graph (from commit dates across all repos, owner's identities configured
  in `profile.md`).
- [x] Top languages (aggregate of per-repo breakdowns).
- [x] Recent commits event log on the profile.
- [x] Build-time search index (repos, files by path, notes later) + Svelte search UI.
- [x] `Ctrl/Cmd+K` command palette: jump to repo, file, page; theme toggle; "copy clone URL".

Done when
- [x] Profile page matches the plan's GitHub-style reference without placeholders.
- [x] Palette is keyboard-only operable and works offline.

Tests
- [x] Unit: contribution bucketing by day/week, language aggregation, search index ranking.
- [x] UI: palette opens/closes/navigates; graph renders for the fixture.

---

## Phase 5 — Importers ✅ *(done 2026-08-23)*

Goal: repos that live elsewhere can be pulled in on every build.

Ships
- [x] Importer interface + implementations: GitHub, GitLab, Gitea, Forgejo (shared
  Gitea-API base), and "local path" as the existing default.
- [x] Interactive `npm run frznforge init` (or `setup`) command: pick provider, auth token
  (from env, never written to config), select repos, writes them into
  `frznforge.config`.
- [x] On `npm run build`: clone/fetch configured remotes into a cache dir, then run the
  Phase 1 scanner on them. Incremental fetch where possible.
- [x] Release import from each provider; per-repo override to use tag-based releases even
  when hosted on a forge.
- [x] Rate-limit handling and clear error messages when a token is missing/expired.

Done when
- [x] A fresh checkout can build a site containing at least one repo from each supported
  provider — verified live via `npm run smoke:remote` (GitHub Descent098/sdu, GitLab
  gitlab-org/release-cli, Gitea gitea/tea, Forgejo codeberg.org dnkl/fuzzel): 4 repos,
  86 provider releases, 1,309 assets, 15,988 pages built. Run anonymously (public repos);
  a token in env is picked up and only raises the rate limit. CI uses recorded fixtures.

Tests
- [x] Unit: each importer against recorded HTTP fixtures (no live network in CI).
- [x] Sync: imported release list ↔ release pages; imported repo ↔ listing entry.

---

## Phase 6 — Notes & organizations ✅ *(done 2026-08-23)*

Goal: the two remaining "content types".

Ships
- [x] **Notes** (gist-style): a configured folder where each file is a single note and each
  sub-folder is a multi-file note. Markdown gets preview/source toggle; everything else
  gets highlighted source. Notes index page with search; notes appear in the palette.
- [x] **Organizations**: grouping of repos under a named org with its own profile overview
  (same `profile.md` mechanism, own pinned repos, own links). Org listing and per-org
  repo listing reuse the Phase 2 listing component.

Done when
- [x] Notes and orgs are routable, searchable, and covered by the sync tests.

Tests
- [x] Unit: note folder → note model; org config → membership resolution.
- [x] UI: note preview/source toggle; org page renders pinned repos.
- [x] Sync: every note file/folder has a page; every repo with an `org` appears under it.

---

## Phase 7 — Insights, polish, 1.0 ✅ *(done 2026-08-24)*

Goal: make it something other people can adopt.

0.1.0 is cut and every box below is settled, but two are settled as **deliberately not done**
rather than shipped, and one acceptance box stays open on something outside this repository.
They are left unticked on purpose — read the annotations before assuming the phase is closed.

Ships
- [x] Insights page per repo: commits over time, contributors over time, lines-of-code over
  time (computed at ingest from sampled commits to keep builds bounded). Schema v5
  `Repo.insights`; commits/contributors are exact (bucketed from the commit list already in
  the artifact), code size is sampled at `ingest.insights.samples` checkpoints, each one an
  `ls-tree` plus a `cat-file --batch` bounded by `ingest.insights.maxBytesPerSample`. A
  checkpoint over that budget reports bytes with `lines: null` and raises
  `insights-approximate`; the page says so rather than pretending.
- [x] Performance pass — **narrowed to page count, which is what the measurement said the
  problem was.** `ingest.branchTrees` (default `10`, or `'all'`) caps how many non-default
  branches get a browsable tree, exactly as `ingest.tagTrees` does for tags; capped branches
  stay listed with a "not browsable" pill and a `branch-trees-capped` warning instead of a
  dead link. Re-measured on the same four remote repos: **16,388 pages / 5m01 → 13,164 pages
  / 3m24 (−32% build, −31% `dist/`)**. The arithmetic, the numbers, every knob and every
  rejected option are in [performance.md](../performance.md); `npm run measure` reproduces
  the breakdown against any artifact.
- [ ] The rest of what "performance pass" floated — **incremental build caching between runs,
  lazy/client-side generation of the heavy blob pages, asset budgets** — was considered and
  deliberately **not built**. Caching needs an artifact→page dependency graph that has to be
  *right*, because a stale page is a silently wrong site, and Astro has no supported partial
  output; the caching that pays for itself (mirror clones, the content-addressed blob store)
  already exists. A client-side file viewer would delete most of the build and also break
  no-JS reading, filesystem-resolved deep links and every accessibility guarantee currently
  under test. No asset budget is enforced; `deploying.md` documents the size instead.
  Reasoning recorded in [performance.md § "What we did not do, and why"](../performance.md);
  revisit only with a measured build where ingest, not rendering, dominates.
- [x] Accessibility and responsive pass across all pages. Lighthouse a11y **100 with zero
  failing audits on every page type in both themes** (30 URLs, `data-theme` pinned): light
  palette ink retuned to clear AA, Shiki moved to github's high-contrast themes, one `<h1>`
  per page with no skipped levels (all rendered user markdown demoted a level), focus
  returned by the palette, keyboard-reachable scroll regions, and no sideways scroll at
  380px. Gated by `tests/e2e/a11y.spec.ts` (every page type × both themes) and
  `tests/unit/contrast.test.ts` (the palette tokens themselves, in all four palettes).
- [x] User docs in `docs/user/`: [quick start](../../user/quick-start.md),
  [starting a site](../../user/starting-a-site.md),
  [config reference](../../user/configuration.md),
  [importing from a forge](../../user/importing.md),
  [deploy guides](../../user/deploying.md) (GitHub Pages, Cloudflare Pages, Netlify, S3,
  nginx, plus a working Actions workflow), [migrating from a forge](../../user/migrating.md).
- [ ] `create-frznforge` / template repo for new users. **Shipped as
  `npm run frznforge -- new <dir>` instead** — an in-repo scaffolder that writes the six files
  a user authors (`frznforge.config.ts`, `content/profile.md`, `content/notes/`,
  `content/orgs/`, `.gitignore`, `README.md`), never overwrites, and has `--dry-run` /
  `--force`. Left unticked because the plan's actual deliverable was a *published* package or
  template repo, and neither exists: there is no npm publish, and `new` cannot install the
  engine — you still clone this repository. See
  [starting-a-site.md](../../user/starting-a-site.md).
- [x] Cut `v0.1.0`. `VERSION`, `package.json` and the `CHANGELOG.md` heading all read `0.1.0`
  (2026-08-24); `docs/dev/release-checklist.md` holds the by-hand steps that cannot be
  verified from inside the repo.

Done when
- [x] Documentation is enough for someone to go from zero to a deployed site without reading
  source. *(The transcripts in `quick-start.md` are literal and were re-run against a real
  build; `release-checklist.md` says to re-verify them whenever the ingest summary, sidebar
  or repo page changes.)*
- [ ] All tests green; no open "must fix" items. **Tests are green** — `astro check` 0 errors,
  480 unit + 156 e2e passing, `npm run build` 561 pages / 0 warnings, ingest byte-identical
  across runs — but **one blocking item is open and cannot be closed from inside this
  repository**: the clone URL in `quick-start.md`, `starting-a-site.md` and this repo's
  `.frznforge.json` (`links.homepage` / `issues` / `upstream`) does not resolve, so a stranger
  fails on the guide's first command. Tracked in
  [release-checklist.md](../release-checklist.md); tick this once the repository is published
  at that URL (or all five places point at the real one) and the container check passes.

---

## Cross-cutting rules (apply to every phase)

- Everything must work as a fully static build; Svelte only where interaction needs it.
- Plain CSS, design tokens in one place; fire = recent/dynamic, ice = old/static.
- Any change to the JSON data model: bump `schemaVersion`, update `docs/dev/data-model.md`,
  update the snapshot fixtures, and add/adjust sync tests in the same change.
- Features that span ingest and UI ship with tests on both sides in the same change.
- `CHANGELOG.md` + `VERSION` updated with every change per the rules in `TODO`.

## Suggested sequencing notes

- Phase 1 and the Phase 0 design pick can overlap — ingest does not depend on the look.
- Phase 3's diff view and Phase 7's LOC-over-time are the two biggest risks for build
  time; both are explicitly allowed to be sampled/capped.
- Phase 5 can be pulled earlier if the primary use case is "mirror my Forgejo" — nothing
  in Phases 3–4 depends on it.
