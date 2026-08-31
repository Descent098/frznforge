# frznforge

**A read-only forge you can host on a static file server.** Point it at your git
repositories, run a build, and get a browsable site — profile, repository listing, file
browser, history, diffs, branches, tags, releases, insights — as plain HTML.

No server. No database. No accounts, issues, pull requests or stars.

**Status: 0.2.0, released 2026-08-30** — the second release. Everything in
[CHANGELOG.md](CHANGELOG.md) is implemented and tested, and it builds this project's own site.
Expect rough edges; the known ones are listed under [Status](#status) below.

## Who it is for

- You self-host a private forge (Forgejo, Gitea, GitLab) and want a **public, safe,
  always-up** window onto your work that does not expose the forge itself.
- Your code lives on GitHub but you want **your own front door** for it, on your own domain.
- You have a pile of **source-available** projects and want them browsable without asking
  anyone to sign up for anything.
- You want the whole thing to still be there in ten years, because it is a folder of files.

It is not a GitHub replacement. There is nowhere to file an issue, nothing to click "fork" on,
and no git server — the site links back to wherever the repository actually lives.

## What it does

**Ingest** — `npm run ingest` reads git through the CLI and writes one validated JSON artifact
plus a content-addressed blob store.

- Local repositories (including bare ones) and repositories imported from **GitHub, GitLab,
  Gitea and Forgejo** — mirror-cloned into a local cache, with metadata and releases from the
  provider API. Tokens come from the environment only and never reach the artifact.
- Only **committed** content is read, never your working tree.
- Per repo: history, branches, tags, per-ref file trees, per-path last-commit info, per-commit
  file stats, language breakdown, contributors, detected license, README, source zips, and
  monthly commit/contributor/code-size insights.
- **Deterministic**: the same repositories at the same commits produce a byte-identical
  artifact. No clock values, no unsorted iteration.
- Nothing about a repository fails a build. Empty repos, missing paths, a forge that is down —
  all warnings, printed at ingest and counted in the site footer.

**Site** — `astro build` turns the artifact into static pages.

- Profile page from `content/profile.md`: bio, links, contribution graph, activity log,
  pinned repos, aggregated stats.
- Repository listing with search, language/tag/kind filters, sorting and pagination — and it
  works with JavaScript off.
- Repo overview, file browser for every browsable ref, file view with build-time Shiki
  highlighting and `#L42` anchors, markdown Preview/Source toggle, raw and zip downloads,
  paginated history, single-commit pages, branches, tags, releases, insights.
- ```` ```mermaid ```` fences render as diagrams, from a locally bundled copy — no CDN, and
  only on pages that actually hold one. `markdown.mermaid: false` turns them back into code
  blocks, which is also what a reader without JavaScript sees.
- **Notes** — a folder of files published gist-style at `/notes/`.
- **Organizations** — group repos under their own page and listing.
- **Hosted sites** — serve a repo's built branch (`gh-pages` by default) as a real site at
  `/<slug>/`, while its forge view stays at `/repos/<slug>/`.
- Ctrl-K command palette, light/dark with a `t` shortcut, two colour palettes. Every page type
  is checked in **both** themes by `tests/e2e/a11y.spec.ts` — WCAG AA contrast over every
  visible text node, one `<h1>` per page, no skipped heading levels, no duplicate ids, no
  unreachable scroll regions, no sideways scroll at 380px — and the palette tokens themselves
  are pinned in `tests/unit/contrast.test.ts`. The published pages call nothing — no CDN, no
  fonts, no analytics, no forge API.

**Deploy** — copy `dist/` to GitHub Pages, Cloudflare Pages, Netlify, S3, a NAS, or an nginx
box.

## Quick start

```bash
npm install
# edit frznforge.config.ts — your name, and the repos to publish
# edit content/profile.md  — bio, links, pinned repos
npm run ingest     # git → data/forge.json + blobs + archives
npm run dev        # http://localhost:4321/
```

`npm run build` (= ingest + `astro build`) writes the deployable site to `dist/`.

Two commands help you fill that config in:
`npm run frznforge -- new <dir>` scaffolds fresh authoring files, and
`npm run frznforge -- init` walks a forge account and writes the repo entries for you.
`init --web` opens a local browser editor for the whole config — the repo picker plus site,
owner, theme, ingest, organizations and hosted sites — and for your `profile.md`.

## Documentation

| Guide | |
|---|---|
| [Quick start](docs/user/quick-start.md) | Zero to a running site in ten minutes |
| [Starting a site](docs/user/starting-a-site.md) | `frznforge -- new`, and keeping your content in its own repository |
| [Configuration](docs/user/configuration.md) | Every config key, `.frznforge.json`, notes, orgs, warnings |
| [Importing from a forge](docs/user/importing.md) | GitHub/GitLab/Gitea/Forgejo, tokens, the `init` wizard, offline builds |
| [Deploying](docs/user/deploying.md) | Build size, a working Actions workflow, host-by-host settings |
| [Migrating from a forge](docs/user/migrating.md) | What carries over, what doesn't, and how to stay fresh |

Start at [docs/user/](docs/user/README.md).

## Development

```bash
npm test            # vitest unit tests (tests/unit) against fixture git repos in temp dirs
npm run test:e2e    # playwright, builds the site from fixture repos first
npm run check       # astro check
```

Keep all three green. No test may touch the network.

- Data contract: [docs/dev/data-model.md](docs/dev/data-model.md) — `src/lib/data/schema.ts` is
  the ingest ↔ site boundary; any change to it bumps `SCHEMA_VERSION`.
- Phase plan and cross-cutting rules: [docs/dev/plans/plan-phases.md](docs/dev/plans/plan-phases.md).
- Plain CSS only, `hf-` prefix, tokens at the top of `src/styles/global.css` and
  `src/styles/repo.css`.
- `VERSION` is the single source of truth for the version; every change is logged in
  [CHANGELOG.md](CHANGELOG.md).

## Status

**0.2.0**, released 2026-08-30 — the second release. Artifact schema v7.
(0.1.0, released 2026-08-24, was the first usable one, on schema v5.)

Rough edges, honestly:

- **Builds get large fast.** File pages are generated per browsable ref, so a repository's
  file count is multiplied by its branches and tags. The defaults cap that at 1 default branch
  + 10 branches + 25 tags; see [deploying.md](docs/user/deploying.md#3-how-big-will-my-site-be)
  before you point it at fifty repositories.
- **There is no published npm package.** You clone this repository; the generator and your
  site live in the same directory. `frznforge -- new` scaffolds the files you author, but it
  cannot install the engine for you — see
  [starting-a-site.md](docs/user/starting-a-site.md).
- No feeds, no sitemap, no diff view between arbitrary refs, no file-content search.
- Everything is public. A repository listed in the config is published in full, whole history
  included — there is no visibility switch, because static files cannot check who is asking.

## License

MIT — see [LICENSE](LICENSE).
