# Configuration

frznforge is configured in three places:

| Where | What |
|---|---|
| `frznforge.config.ts` (project root) | Site title/URL, owner, colour palette, the list of repositories to ingest, ingest limits, listing page size |
| `content/profile.md` | The profile page: frontmatter for links / location / pinned repos, markdown body rendered as your README |
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
  repos: [
    { type: 'local', path: '../useful' },    // slug defaults to the directory name
    { type: 'local', path: 'D:/code/ezcv', slug: 'ezcv',
      overrides: { template: true, tags: ['python', 'resume'] } },
  ],
  ingest: {
    outDir: './data',        // forge.json + blobs/ are written here (gitignored)
    maxBlobBytes: 524288,    // text files above this are listed but not stored
    maxCommits: null,        // cap per repo (null = all)
    concurrency: 4,          // repos scanned in parallel
  },
  listing: { pageSize: 50 },
});
```

Notes
- `path` is absolute or relative to the config file. Bare repos work too.
- `overrides` has the same shape as `.frznforge.json` and wins over it.
- The env var `FRZNFORGE_OUT_DIR` overrides `ingest.outDir` (used by the test suite).
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

## Empty and odd repositories

frznforge never fails a build because of a repo's state; it emits a warning and keeps going:

| Situation | Result |
|---|---|
| Repo with no commits | Listed, marked empty, page says so (`repo-empty`) |
| Latest commit deleted every file | Listed with history but no files (`default-branch-empty-tree`) |
| HEAD branch is empty/unborn but another branch has commits | That branch is used as the default (`default-branch-fallback`) |
| Path is not a git repo | Skipped (`repo-not-found`) |
| `.frznforge.json` is invalid | Ignored (`repo-meta-invalid`) |

Warnings are printed by `npm run ingest` and counted in the site footer.
