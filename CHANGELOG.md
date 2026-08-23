# 0.1.0 (unreleased)

initial release


## Features

- **Phase 1 — ingest core.** `npm run ingest` scans local git repos (concurrently) into a versioned JSON artifact (`data/forge.json`, schema v1) plus a content-addressed blob store. Per repo: metadata (from a committed `.frznforge.json`, overridable in `frznforge.config.ts`), default branch, branches, tags (annotated + lightweight), full commit history, default-branch tree with last-commit per path, per-file info (size/binary/language), language breakdown, contributors, README and license detection. Only committed content is read — never the working tree.
- Empty/odd repos never fail a build: empty repo, last-commit-wiped tree, and unborn HEAD with work on another branch are all emitted with warnings (`repo-empty`, `default-branch-empty-tree`, `default-branch-fallback`); non-repo paths are skipped (`repo-not-found`).
- `frznforge.config.ts` is now the full site config (site, owner, theme.palette, repos, ingest limits, listing page size) validated with zod and `defineConfig()`; `FRZNFORGE_OUT_DIR` env override for tests.
- `npm run build` runs ingest first.
- **Phase 2 — site skeleton (Hearth design).** Global layout with docked sidebar, light/dark theme toggle (`t`), palette from config; profile page from `content/profile.md` (frontmatter links/location/pinned + rendered markdown body) with real stats from the artifact (repos, commits this year, years of history, aggregated top languages); repository listing with search, language/tag/kind filters, sorting and pagination (Svelte island, URL-synced, renders fully without JS); repository overview page (metadata/About panel, template banner, language bar, contributors, latest commit, root file table, rendered README, clone panel from the upstream URL, empty-repo state); 404 page.

- Four static design explorations (profile + repo page each) under `/designs/`: Ember, Frost, Anvil, Hearth. Throwaway, not wired to any data; used to pick a visual direction.
- Hearth chosen as the design direction. Added `frznforge.config.ts` with `theme.palette: 'hearth' | 'frost'` (build-time) — same layout, warm vs cool colour palette.

## Bug Fixes

## Other

- Test tooling: vitest (`npm test`) with fixture git repos built in temp dirs under deterministic dates; 67 ingest tests covering every extractor, the three empty-state gotchas, uncommitted-work exclusion, determinism (byte-identical re-ingest) and a redacted artifact snapshot.
- Docs: `docs/dev/data-model.md` (artifact layout, every type, warning codes, schema-bump rule), `docs/user/configuration.md`.
- Added LICENSE (MIT) and a committed `.frznforge.json` for this repo.
- Playwright e2e suite (`npm run test:e2e`): global setup builds fixture git repos → ingest → `astro build` into `tests/.tmp/e2e`, then drives the static site (listing with/without JS, deep links, repo page from committed content only, template/empty states, 404). Vitest sync test asserts artifact ↔ routes/listing parity. Listing/format helpers unit-tested.
- Lighthouse accessibility 100 on profile, listing and repo pages (tertiary text contrast, underlined prose links, accessible names).
- README rewritten for the project; `npm run check` (astro check) is clean.

- Added `docs/dev/plans/plan-phases.md` describing phased delivery (Phase 0 foundation through Phase 7 / 1.0).
- Hearth profile page restructured: repository/commit/years/top-language stats now live inside the hero; README + activity follow directly, then pinned repos.

