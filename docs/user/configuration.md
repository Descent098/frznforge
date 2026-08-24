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
  site: { title: 'frznforge', url: 'https://forge.example.com' },
  owner: { name: 'Kieran Wood', handle: 'kieran', profile: './content/profile.md' },
  theme: { palette: 'hearth' },              // 'hearth' (warm) | 'frost' (cool)
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
  ingest: {
    outDir: './data',        // forge.json + blobs/ + archives/ are written here (gitignored)
    maxBlobBytes: 524288,    // files above this are listed but not stored
    maxCommits: null,        // cap per repo (null = all)
    concurrency: 4,          // repos scanned in parallel
    tagTrees: 25,            // newest N tags get browsable trees + archives (0 = none)
    archives: true,          // zip source archives (git archive) for default branch + tags
    cacheDir: './.frznforge-cache',  // mirror clones of remote repos (gitignored)
    fetch: 'auto',           // 'auto' | 'never' (offline, cache only) | 'always'
  },
  listing: { pageSize: 50 },
});
```

Notes
- `path` is absolute or relative to the config file. Bare repos work too.
- `tagTrees`: every branch is always browsable; tags beyond the newest N lose their file
  tree and download archive (a `tag-trees-capped` warning tells you when that happens).
- `archives: false` skips zip generation entirely (no download links on the site).
- `overrides` has the same shape as `.frznforge.json` and wins over it.
- `org` on a repo puts it in that organization; so does listing the repo's slug under
  `organizations[].repos`. Doing both is fine — membership is the union of the two.
- The env var `FRZNFORGE_OUT_DIR` overrides `ingest.outDir`, and `FRZNFORGE_CACHE_DIR`
  overrides `ingest.cacheDir` (both used by the test suite).
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
---

# Hi 👋
Markdown body — rendered on the profile page.
```

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

Warnings are printed by `npm run ingest` and counted in the site footer.
