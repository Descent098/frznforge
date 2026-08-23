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

## Phase 5 — Importers *(next)*

Goal: repos that live elsewhere can be pulled in on every build.

Ships
- [ ] Importer interface + implementations: GitHub, GitLab, Gitea, Forgejo (shared
  Gitea-API base), and "local path" as the existing default.
- [ ] Interactive `npm run frznforge init` (or `setup`) command: pick provider, auth token
  (from env, never written to config), select repos, writes them into
  `frznforge.config`.
- [ ] On `npm run build`: clone/fetch configured remotes into a cache dir, then run the
  Phase 1 scanner on them. Incremental fetch where possible.
- [ ] Release import from each provider; per-repo override to use tag-based releases even
  when hosted on a forge.
- [ ] Rate-limit handling and clear error messages when a token is missing/expired.

Done when
- [ ] A fresh checkout with a token in env can build a site containing at least one repo from
  each supported provider (verified manually; CI uses recorded fixtures).

Tests
- [ ] Unit: each importer against recorded HTTP fixtures (no live network in CI).
- [ ] Sync: imported release list ↔ release pages; imported repo ↔ listing entry.

---

## Phase 6 — Notes & organizations

Goal: the two remaining "content types".

Ships
- [ ] **Notes** (gist-style): a configured folder where each file is a single note and each
  sub-folder is a multi-file note. Markdown gets preview/source toggle; everything else
  gets highlighted source. Notes index page with search; notes appear in the palette.
- [ ] **Organizations**: grouping of repos under a named org with its own profile overview
  (same `profile.md` mechanism, own pinned repos, own links). Org listing and per-org
  repo listing reuse the Phase 2 listing component.

Done when
- [ ] Notes and orgs are routable, searchable, and covered by the sync tests.

Tests
- [ ] Unit: note folder → note model; org config → membership resolution.
- [ ] UI: note preview/source toggle; org page renders pinned repos.
- [ ] Sync: every note file/folder has a page; every repo with an `org` appears under it.

---

## Phase 7 — Insights, polish, 1.0

Goal: make it something other people can adopt.

Ships
- [ ] Insights page per repo: commits over time, contributors over time, lines-of-code over
  time (computed at ingest from sampled commits to keep builds bounded).
- [ ] Performance pass: build caching between runs, lazy-generated heavy pages, asset
  budgets.
- [ ] Accessibility and responsive pass across all pages.
- [ ] User docs in `docs/user/`: quick start, config reference, deploy guides (GitHub Pages,
  Cloudflare Pages, any static host), migrating from a forge.
- [ ] `create-frznforge` / template repo for new users.
- [ ] Cut `v1.0.0`.

Done when
- [ ] Documentation is enough for someone to go from zero to a deployed site without reading
  source.
- [ ] All tests green; no open "must fix" items.

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
