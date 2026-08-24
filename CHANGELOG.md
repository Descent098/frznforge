# 0.1.0 (unreleased)

initial release


## Features

- **Phase 1 — ingest core.** `npm run ingest` scans local git repos (concurrently) into a versioned JSON artifact (`data/forge.json`, schema v1) plus a content-addressed blob store. Per repo: metadata (from a committed `.frznforge.json`, overridable in `frznforge.config.ts`), default branch, branches, tags (annotated + lightweight), full commit history, default-branch tree with last-commit per path, per-file info (size/binary/language), language breakdown, contributors, README and license detection. Only committed content is read — never the working tree.
- Empty/odd repos never fail a build: empty repo, last-commit-wiped tree, and unborn HEAD with work on another branch are all emitted with warnings (`repo-empty`, `default-branch-empty-tree`, `default-branch-fallback`); non-repo paths are skipped (`repo-not-found`).
- `frznforge.config.ts` is now the full site config (site, owner, theme.palette, repos, ingest limits, listing page size) validated with zod and `defineConfig()`; `FRZNFORGE_OUT_DIR` env override for tests.
- `npm run build` runs ingest first.
- **Phase 6 — notes & organizations.** **Notes** are a gist-style folder (`content/notes/`): a file is a single-file note, a subfolder is a multi-file note. Markdown notes take optional frontmatter (title, description, date, tags) and render with the same Preview/Source toggle as repo files; everything else gets Shiki-highlighted source with a line gutter, images render inline, and every stored file has a raw URL. There is a searchable notes index, and notes join the Ctrl+K palette.
- **Organizations** group repos under a named umbrella with their own overview page — hero, aggregated KPIs and languages, a markdown profile from `content/orgs/<slug>.md`, pinned/member repo cards, and a `/orgs/<slug>/repos/` listing that reuses the Phase 2 listing island. Membership can be declared from either side (`organizations[].repos` or `org:` on a repo source); mismatches warn instead of failing.
- Schema v4: `notes` and `organizations` in the artifact, four new warning codes (`note-slug-collision`, `notes-dir-missing`, `org-unknown-repo`, `repo-unknown-org`). Note content shares the content-addressed blob store with repo files.
- **Phase 5 — importers.** Repositories hosted on **GitHub, GitLab, Gitea and Forgejo** can be listed in `frznforge.config.ts` instead of pointed at locally: frznforge reads their metadata and releases over the provider REST API, mirror-clones them into a local cache (`ingest.cacheDir`), and runs the existing scanner over the mirror, so every Phase 2–4 feature works for imported repos. Tokens come from environment variables only (never written to config, never logged, never in the artifact); public repos work anonymously.
- Provider **releases** (name, markdown notes, prerelease flag, author, assets with sizes) are imported into the artifact and rendered on the releases pages, falling back to annotated tags for local repos. Release pages label their source.
- Offline/failure behaviour: an unreachable, unauthenticated or rate-limited forge never fails a build — it warns (`remote-fetch-failed`, `remote-auth-missing`, `remote-rate-limited`, `remote-cache-stale`) and falls back to the local mirror. `ingest.fetch: 'auto' | 'never' | 'always'` controls network use; `'never'` builds fully offline from the cache.
- `npm run frznforge -- init` — interactive setup: pick a provider, walk an account's repositories, multi-select, and write the entries into `frznforge.config.ts` (with a backup and a confirmation). Non-interactive flags (`--provider`, `--account`, `--select`, `--print`, `--yes`) for scripting.
- `npm run smoke:remote` — live end-to-end check that builds a throwaway site from one small public repo on each provider.
- **Phase 3 — repo depth.** The repo page is now a real read-only forge view: file browser for every branch (and the newest 25 tags) with per-path last-commit info and a no-JS ref switcher; file view with build-time Shiki highlighting (light+dark), CSS line-number gutter and `#L42` anchors, markdown Preview/Source toggle, image preview, symlink/binary/too-large fallbacks; Raw/Download endpoints; paginated commit history and single-commit pages with per-file +/− stats; branches, tags and releases pages (releases render annotated-tag messages as markdown); source zip downloads produced with `git archive` at ingest.
- Ingest schema v3: provider `releases` on each repo, `releaseMode: 'tags' | 'provider'`, a remote `source` union (provider/host/owner/repo + web and clone URLs), and four remote warning codes. Markdown from imported repos (READMEs and release notes) is now rendered in an untrusted mode that strips raw HTML and unsafe URL schemes, since it comes from repos the site owner may not control.
- Ingest schema v2: per-ref trees (`refTrees`), zip `archives`, per-commit `files`/`stats` from numstat, binary blobs (≤ cap) now stored for raw serving; new `ingest.tagTrees` / `ingest.archives` config; new `tag-trees-capped` warning.
- **Phase 2 — site skeleton (Hearth design).** Global layout with docked sidebar, light/dark theme toggle (`t`), palette from config; profile page from `content/profile.md` (frontmatter links/location/pinned + rendered markdown body) with real stats from the artifact (repos, commits this year, years of history, aggregated top languages); repository listing with search, language/tag/kind filters, sorting and pagination (Svelte island, URL-synced, renders fully without JS); repository overview page (metadata/About panel, template banner, language bar, contributors, latest commit, root file table, rendered README, clone panel from the upstream URL, empty-repo state); 404 page.

- Four static design explorations (profile + repo page each) under `/designs/`: Ember, Frost, Anvil, Hearth. Throwaway, not wired to any data; used to pick a visual direction.
- Hearth chosen as the design direction. Added `frznforge.config.ts` with `theme.palette: 'hearth' | 'frost'` (build-time) — same layout, warm vs cool colour palette.

## Bug Fixes

- Repo file routes no longer break on URL-special characters in a committed path. Every `tree`/`blob`/`raw` segment (and the ref slug) is now percent-encoded, so a file named `read me.md` gets a valid link instead of `href="…/read me.md/"`, and a `%` in a name no longer aborts `astro build`. `#` and `%` cannot round-trip a static host at all, so those paths are listed in the file table without a link and ingest raises `repo-path-unservable` — the same rule the notes side already applied.
- The sidebar owner link and theme toggle no longer fail WCAG 2.5.3 "Label in Name" (Lighthouse `label-content-name-mismatch`, previously failing on every page). Their `aria-label`s replaced the visible text rather than containing it; the purpose is now appended in a visually hidden span, and the mobile layout clips those labels instead of removing them so both controls keep an accessible name when the sidebar collapses.

- Code views no longer render a phantom trailing line: a file ending in a newline made Shiki emit one extra `.line`, so the gutter showed one more line than the "N lines" label. Affected every repo file view and, once they landed, note files.

## Other

- Test tooling: vitest (`npm test`) with fixture git repos built in temp dirs under deterministic dates; 67 ingest tests covering every extractor, the three empty-state gotchas, uncommitted-work exclusion, determinism (byte-identical re-ingest) and a redacted artifact snapshot.
- Docs: `docs/dev/data-model.md` (artifact layout, every type, warning codes, schema-bump rule), `docs/user/configuration.md`.
- Added LICENSE (MIT) and a committed `.frznforge.json` for this repo.
- Playwright e2e suite (`npm run test:e2e`): global setup builds fixture git repos → ingest → `astro build` into `tests/.tmp/e2e`, then drives the static site (listing with/without JS, deep links, repo page from committed content only, template/empty states, 404). Vitest sync test asserts artifact ↔ routes/listing parity. Listing/format helpers unit-tested.
- **Phase 4 — profile extras & command palette.** Contribution heat map (52 weeks; hue = recency fire→ice, intensity = commit-count quartile; identities configurable in `profile.md` frontmatter; streak + busiest-day footer), recent-activity event log (pushes grouped per repo/branch/day + tag events), and a Ctrl/Cmd+K command palette: fuzzy search over repos, default-branch file paths and pages from a build-time `/search-index.json`, plus actions (toggle theme, copy clone URL); keyboard-only operable, works offline, `/` also opens it, sidebar search box wired to it.
- Lighthouse accessibility 100 on profile, listing and repo pages (tertiary text contrast, underlined prose links, accessible names).
- README rewritten for the project; `npm run check` (astro check) is clean.

- Added `docs/dev/plans/plan-phases.md` describing phased delivery (Phase 0 foundation through Phase 7 / 1.0).
- Hearth profile page restructured: repository/commit/years/top-language stats now live inside the hero; README + activity follow directly, then pinned repos.

