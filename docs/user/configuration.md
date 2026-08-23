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
    // repos hosted on a forge — see docs/user/importing.md
    { type: 'github', owner: 'Descent098', repo: 'ezcv' },
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

Warnings are printed by `npm run ingest` and counted in the site footer.
