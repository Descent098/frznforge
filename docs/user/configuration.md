# Configuration

frznforge is configured in three places:

| Where | What |
|---|---|
| `frznforge.config.ts` (project root) | Site title/URL, owner, colour palette, the list of repositories to ingest, organizations, notes folder, ingest limits, listing page size |
| `content/profile.md` | The profile page: frontmatter for links / location / pinned repos, markdown body rendered as your README |
| `content/orgs/<slug>.md` | One organization's page: frontmatter for links / pinned repos, markdown body |
| `content/notes/` | Your notes — one file, or one folder, per note |
| `.frznforge.json` inside each repository | Per-repo metadata: name, description, links, tags, template flag, license |

Everything is read at **build time**. Change something → run `npm run build` (or
`npm run ingest` then `npm run dev`) to see it.

## `frznforge.config.ts`

```ts
import { defineConfig } from './src/lib/config/schema';

export default defineConfig({
  site: {
    title: 'frznforge',
    url: 'https://forge.example.com',
    // description: 'Kieran’s frozen forge',  // meta description, if profile.md has no bio
    // base: '/mysite',      // serve from a sub-path (e.g. a GitHub Pages project site)
  },
  owner: { name: 'Kieran Wood', handle: 'kieran', profile: './content/profile.md' },
  theme: {
    palette: 'hearth',       // 'hearth' (warm) | 'frost' (cool)
    // heat: { hot: 7, warm: 30, neutral: 180, cool: 365 },  // recency-accent day boundaries
  },
  markdown: {
    mermaid: true,           // render ```mermaid fences as diagrams
  },
  content: {
    orgs: './content/orgs',  // one <org-slug>.md per organization (optional)
  },
  repos: [
    { type: 'local', path: '../useful' },    // slug defaults to the directory name
    { type: 'local', path: 'D:/code/ezcv', slug: 'ezcv', org: 'canadian-coding',
      overrides: { template: true, tags: ['python', 'resume'] } },
    // repos hosted on a forge — see docs/user/importing.md
    { type: 'github', owner: 'Descent098', repo: 'ezcv' },
  ],
  notes: {
    dir: './content/notes',  // one file = one note, one folder = one multi-file note
    useMtime: false,         // see the warning below before turning this on
    // maxFileBytes: 262144, // defaults to ingest.maxBlobBytes
  },
  organizations: [
    { slug: 'canadian-coding', name: 'Canadian Coding',
      description: 'Tools and teaching material.',
      repos: ['useful', 'frznforge'] },
  ],
  hosting: {
    sites: [
      // serve a repo's branch as a real site at /my-site/ (the forge view stays at /repos/…)
      // { repo: 'my-site', slug: 'my-site', branch: 'gh-pages' },  // branch defaults to gh-pages → main → master
    ],
    maxFileBytes: 20 * 1024 * 1024,  // size cap for hosted files (replaces maxBlobBytes there)
  },
  ingest: {
    outDir: './data',        // forge.json + blobs/ + archives/ are written here (gitignored)
    maxBlobBytes: 524288,    // files above this are listed but not stored
    maxCommits: null,        // cap per repo (null = all)
    maxCommitAgeDays: null,  // only commits from the last N days (null = all; needs git ≥ 2.37)
    concurrency: 4,          // repos scanned in parallel
    tagTrees: 25,            // newest N tags get browsable trees + archives (0 = none)
    branchTrees: 10,         // newest N *non-default* branches get browsable trees ('all' = every one)
    archives: true,          // zip source archives (git archive) for default branch + tags
    cacheDir: './.frznforge-cache',  // mirror clones of remote repos (gitignored)
    fetch: 'auto',           // 'auto' | 'never' (offline, cache only) | 'always'
    reuse: {                 // cross-run reuse (see the note below)
      enabled: true,
      maxAgeMinutes: 2,      // don't re-fetch a remote fetched fresh this recently
    },
    insights: {              // per-repo /insights/ page
      enabled: true,
      samples: 24,                    // max monthly code-size checkpoints per repo
      maxBytesPerSample: 20 * 1024 * 1024,  // line-counting budget at one checkpoint
    },
  },
  listing: { pageSize: 50 },
});
```

Notes
- `site.url` is **display only** today: it is rendered as the host label under the site title
  in the sidebar and nowhere else. Leaving it out changes no URL — it is reserved for
  absolute links and feeds later.
- `site.description` is the home page's `<meta name="description">`, used only when
  `content/profile.md` has no `bio` in its frontmatter — the profile's own bio wins.
- `site.base` serves the whole site from a sub-path: with `base: '/mysite'`, every link the
  site emits — pages, raw files, archives, the favicon, the command palette's search
  index — is prefixed with `/mysite`, which is what a GitHub Pages *project* site (or any
  deploy that is not the domain root) needs. Any spelling (`mysite`, `/mysite/`) is
  normalised; omit it (or use `'/'`) for a root deploy, where links stay root-absolute
  exactly as before. See [deploying.md](./deploying.md) for the host-side half.
- `path` is absolute or relative to the config file. Bare repos work too.
- `markdown.mermaid` renders ```` ```mermaid ```` fences as diagrams — everywhere markdown
  renders on the site: the profile, org pages, notes, your local repos, **and imported
  repos' READMEs and release notes**. Importing a repo is choosing to publish its content,
  so its diagrams are your call the same way its prose is; the imported-content renderer
  still strips raw HTML and filters URLs regardless, and mermaid runs in the visitor's
  browser with its own strict-mode sanitiser. Diagrams render from a copy of mermaid
  bundled into the site (no CDN — the published pages still call nothing), loaded only on
  pages that hold a diagram and only once one scrolls near. Without JavaScript the fence
  reads as a code block of the diagram source, which is also what you get with
  `mermaid: false`.
- `theme.heat` sets the *day boundaries* of the fire→ice recency accent: age < `hot` days is
  orange, then warm/neutral/cool, ≥ `cool` days is blue. Values must be strictly ascending.
  The profile and organization "touched / commits recently" stats use the `hot` boundary, so
  the copy and the accent always agree. The colours themselves are not configurable — they
  are the palette's tokens, kept at WCAG AA by the contrast tests.
- `maxCommitAgeDays` keeps only commits from the last N days (it uses
  `git rev-list --since-as-filter`, which needs git ≥ 2.37). The cutoff is measured from the
  repo's **newest commit date**, never from today's date — that keeps builds reproducible,
  and it means a dormant repo keeps its newest N days of history instead of going empty.
  Every branch always keeps its head commit. Everything derived from the commit list —
  commit counts, contributors, `createdAt`, insights — narrows with it. Ingest raises a
  `commits-aged-out` warning when the age cutoff is what dropped history (when `maxCommits`
  truncates first, `commits-capped` is reported instead). File tables keep their per-file
  "Last commit" dates and subjects either way — commits the window dropped but a file or
  tag still points at are carried separately (`extraCommits`) without affecting the counts.
- `tagTrees` / `branchTrees` are the two biggest levers on build size, because the file
  browser (`tree`/`blob`/`raw`) is generated **per browsable ref** — a repo with 27 branches
  costs 27× its file count in pages. `tagTrees` keeps the newest N tags (those also get the
  download archive); `branchTrees` keeps the N most recently updated non-default branches, or
  `'all'`. The default branch always has a file browser and never counts against the cap, so
  the defaults give at most `1 + 10 + 25` browsable refs per repo. Capped refs still appear on
  the branches/tags pages and in the ref switcher — they just have no file browser, and ingest
  says so (`tag-trees-capped`, `branch-trees-capped`). See
  [deploying.md](./deploying.md#3-how-big-will-my-site-be) for the page-count formula, and
  [performance.md](../dev/performance.md) for what these caps are actually measured to save —
  including the case they *don't* help, which is a repo that vendors its dependencies.
- `archives: false` skips zip generation entirely (no download links on the site). Source zips
  are usually the largest single thing in `dist/`.
- `reuse` makes repeat builds cheap without changing a byte of output. Three things happen
  under it: with `fetch: 'auto'`, a remote repo whose last fetch fully succeeded less than
  `maxAgeMinutes` ago is not re-fetched (its cached provider answers and mirror are used
  as-is; `'always'` always fetches, `'never'` never does, and a repo whose last fetch
  failed or was rate-limited is *always* retried); a repo whose branches, tags and
  metadata are unchanged since the last run skips re-scanning entirely and replays the
  recorded result; and **syntax highlighting is remembered between builds** — the single
  biggest cost in rendering the site, measured at 84% of it, so a rebuild of unchanged
  content is roughly **three times faster** (see
  [performance.md](../dev/performance.md#measured-the-highlight-memo-020)). All the
  bookkeeping lives in `cacheDir`; the artifact stays deterministic and timestamp-free, and
  a cached highlight is byte-for-byte what a fresh one would have produced. Bypass the
  ingest half once with:

  ```bash
  npm run ingest -- --no-cache
  ```

  The highlight half is bypassed with `FRZNFORGE_NO_HL_CACHE=1` (it runs during
  `astro build`, not during ingest). Deleting `cacheDir` is always safe: everything in it is
  rebuilt on demand.
- `insights` controls the `/repos/<slug>/insights/` page. Monthly commits and contributors are
  exact — they come from the commit list already in the artifact. Code size over time is
  **sampled**: at most `samples` monthly checkpoints (always including the first and last),
  each one measured with `git ls-tree`, skipping binary and vendored paths. Counting lines
  needs the file contents, so it stops once a checkpoint's text exceeds `maxBytesPerSample`;
  that point then reports bytes but no line count — and its byte total loses the binary filter
  too, because the blobs past the budget were never read and so could not be classified. The
  series is flagged approximate, the page says so, and you get an `insights-approximate`
  warning. Checkpoints are derived from the commit list and never from a clock, so the
  sampling is reproducible. `enabled: false` drops the page.
- `overrides` has the same shape as `.frznforge.json` and wins over it.
- `org` on a repo puts it in that organization; so does listing the repo's slug under
  `organizations[].repos`. Doing both is fine — membership is the union of the two.
- `hosting.sites` serves a repo's branch as a **real site** at `/<slug>/…` — a `gh-pages`
  branch holding a built site is the classic case — while the normal forge view of the
  same repo stays at `/repos/<slug>/`. `branch` left unset picks the first existing of
  `gh-pages`, `main`, `master`; the hosted branch always gets a browsable file tree (even
  past the `branchTrees` cap) and its files are stored up to `hosting.maxFileBytes`
  instead of `maxBlobBytes`, since built sites carry bundles bigger than 512 kB. Slugs the
  build itself owns (`repos`, `notes`, `orgs`, `_astro`, …) or that collide with a file in
  `public/` are hard errors; a mistyped `repo` (`hosting-unknown-repo`) or a branch that does
  not exist (`hosting-branch-missing`) is a warning and the site is simply not served. Every hosted file becomes a page, so hosting a large site grows
  the build the way `branchTrees` does — `npm run measure` shows the count.
- The env var `FRZNFORGE_OUT_DIR` overrides `ingest.outDir`, `FRZNFORGE_CACHE_DIR`
  overrides `ingest.cacheDir`, and `FRZNFORGE_BASE` overrides `site.base` (all used by the
  test suite).
- `cacheDir` / `fetch` only matter when you import repos from a forge —
  see [importing.md](./importing.md) for `github` / `gitlab` / `gitea` / `forgejo` sources,
  API tokens, and offline builds.
- Only **committed** content on **branches** is read. Uncommitted, staged, stashed or
  untracked files never appear in the output.

## `.frznforge.json` (inside a repo)

Read from the default branch's committed tree — not from disk — so it must be committed.

```json
{
  "name": "useful",
  "description": "Up to 300 characters.",
  "links": {
    "homepage": "https://kieranwood.ca/useful",
    "issues": "https://github.com/you/useful/issues",
    "donations": "https://ko-fi.com/you",
    "upstream": "https://github.com/you/useful"
  },
  "tags": ["pwa", "offline"],
  "template": false,
  "license": "MIT",
  "releaseMode": "tags"
}
```

All fields are optional. `license` is an SPDX id override; when absent frznforge detects
MIT / Apache-2.0 / GPL / BSD / MPL / ISC / Unlicense / CC0 / 0BSD from a `LICENSE`-like
file. `upstream` doubles as the clone URL shown on the repo page (this site has no git
server of its own).

## `content/profile.md`

```md
---
bio: One-line tagline under your name.
location: Calgary, AB
workplace: Canadian Coding
school: University of Calgary
email: you@example.com
sites: [https://kieranwood.ca, https://canadiancoding.ca]
linkedin: https://www.linkedin.com/in/you
forges:
  github: https://github.com/you
  codeberg: https://codeberg.org/you
pinned: [useful, frznforge]   # repo slugs, max 10, in order
identities:                   # author emails counted as "you" in the contribution graph
  - you@example.com
---

# Hi 👋
Markdown body — rendered on the profile page.
```

Leave `identities` out to count every commit in every repo. Set it — listing each address you
have ever committed under — as soon as any repo has other contributors, or their commits show
up as yours.

## Notes (`content/notes/`)

Gist-style snippets, published at `/notes/`. Unlike repositories, this folder is read straight
from disk — nothing has to be committed.

```
content/notes/
├── ripgrep-cheatsheet.md   → one note, one file
├── xdg-basedirs.txt        → one note, one file (highlighted source, no preview toggle)
├── dotfiles/               → one note, several files
│   ├── index.md              title, description, date and tags are read from here
│   └── bin/setup.sh
├── .drafts/                → ignored
└── _wip.md                 → ignored
```

- A **file** in the folder is a single-file note. A **sub-folder** is one note containing every
  file underneath it. Names starting with `.` or `_` are skipped at any depth.
- Markdown notes get a preview/source toggle; everything else gets highlighted source. Binary
  files and files over the size cap get the same "can't show this" fallback as repo files.
- Frontmatter is optional and only read from a single-file note's own markdown, or from
  `index.md` / `README.md` inside a folder note:

```md
---
title: ripgrep cheatsheet
description: The flags I always forget.
date: 2026-08-23        # or 2026-08-23T14:30:00Z / 2026-08-23 14:30:00
tags: [cli, search]
---

# ripgrep cheatsheet
```

- Without a `title`, frznforge uses the first `# H1`, then the filename. The URL slug is the
  file or folder name with the extension dropped, slugified; duplicates get `-2`, `-3`
  (`note-slug-collision`).
- **Dates are read as UTC.** `YYYY-MM-DD` means midnight UTC, and a date-time with no `Z` or
  `+HH:MM` is taken as UTC rather than as your machine's timezone — otherwise the same note
  would get a different date (and a different place in the list) on a colleague's laptop. Other
  spellings (`March 4, 2026`, `03/04/2026`) are ignored, and the note shows as undated.
- **Avoid `#` and `%` in note file names.** Everything else is fine — spaces, `&`, accents —
  but those two cannot be expressed in a static file URL, so such a file renders on the page
  without a Raw or Download link and ingest reports `note-file-unservable`.
- **`useMtime`**: undated notes normally sort last. Setting `notes.useMtime: true` dates them
  from the file's modification time instead — convenient, but mtimes change on every fresh
  clone, so two builds of the same content stop producing identical output. Leave it off
  unless you want that trade.
- A missing `notes.dir` is not an error: if you configured a `notes` block you get a
  `notes-dir-missing` warning (and if you did not, no warning at all) plus an empty
  notes page.

## Organizations (`organizations` + `content/orgs/<slug>.md`)

Group repos under a named org with its own overview page at `/orgs/<slug>/` and its own repo
listing at `/orgs/<slug>/repos/`.

The config entry supplies the identity and (optionally) the membership:

```ts
organizations: [
  { slug: 'canadian-coding', name: 'Canadian Coding',
    description: 'Tools and teaching material.',
    repos: ['useful', 'frznforge'] },
],
```

The markdown file supplies the prose, and is entirely optional — an org with no file still
gets a page built from the config and its repos:

```md
---
description: Tools and teaching material.
sites: [https://canadiancoding.ca]
links:
  Docs: https://docs.canadiancoding.ca
  Chat: https://discord.gg/example
pinned: [useful, frznforge]   # repo slugs, max 10, in order
---

# Canadian Coding
Markdown body — rendered on the organization page.
```

The filename is the slug: `content/orgs/canadian-coding.md`. Change the folder with
`content.orgs` in the config.

Typos are warnings, never build failures: an org listing a repo that does not exist gets
`org-unknown-repo`, and a repo naming an org that is not configured gets `repo-unknown-org`.

## Empty and odd repositories

frznforge never fails a build because of a repo's state; it emits a warning and keeps going:

| Situation | Result |
|---|---|
| Repo with no commits | Listed, marked empty, page says so (`repo-empty`) |
| Latest commit deleted every file | Listed with history but no files (`default-branch-empty-tree`) |
| HEAD branch is empty/unborn but another branch has commits | That branch is used as the default (`default-branch-fallback`) |
| Path is not a git repo | Skipped (`repo-not-found`) |
| `.frznforge.json` is invalid | Ignored (`repo-meta-invalid`) |
| A forge is down, or the API call fails | Cached mirror used, else repo skipped (`remote-fetch-failed`) |
| A private repo with no token configured | Skipped, naming the env vars it looked in (`remote-auth-missing`) |
| The provider API rate-limited the build | Cached mirror used, else repo skipped (`remote-rate-limited`) |
| `fetch: 'never'`, or a refresh failed with a cache present | Cached mirror used as-is (`remote-cache-stale`) |
| `notes.dir` does not exist | No notes, empty notes page (`notes-dir-missing`, only if you configured a `notes` block) |
| Two notes slugify to the same name | The later one is suffixed `-2` (`note-slug-collision`) |
| An org lists a repo slug that was not ingested | That entry is dropped (`org-unknown-repo`) |
| A repo names an org that is not configured | The repo joins no org (`repo-unknown-org`) |
| A committed path or ref name contains `#` or `%` | No static URL can reach it: listed without a link (`repo-path-unservable`) |
| A description is longer than 300 characters | Truncated (`description-truncated`) |
| Two repos resolve to the same slug | The later one is suffixed `-2` (`slug-collision`) |
| More commits on a branch than `ingest.maxCommits` | The list is cut to the newest N (`commits-capped`) |
| More tags than `ingest.tagTrees` | Older tags lose their file browser and archive (`tag-trees-capped`) |
| More non-default branches than `ingest.branchTrees` | Older branches lose their file browser (`branch-trees-capped`) |
| A code-size checkpoint exceeded `insights.maxBytesPerSample` | That point reports bytes but no line count, and that byte total may include binaries (`insights-approximate`) |
| `hosting.sites[].repo` names a slug no ingested repo has | That entry is dropped, the site is not served (`hosting-unknown-repo`) |
| A hosted repo has no branch to serve — the configured `branch` does not exist, or none was configured and none of `gh-pages`/`main`/`master` exist | That entry is dropped, the site is not served (`hosting-branch-missing`) |
| A file on a hosted branch has `#` or `%` in its path | The rest of the site is still served; those files get no URL and are missing from it (`hosting-file-unservable`) |

Warnings are printed by `npm run ingest` and counted in the site footer.

One message does **not** come from ingest and so has no code: a markdown file in
`content/orgs/` with no matching `organizations` entry makes `astro build` (and `astro dev`)
print

```
[frznforge] content/orgs/<id>.md does not match any organization in frznforge.config.ts — ignored.
```

once per orphaned file. Delete the file or add the organization.
