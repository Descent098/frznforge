# Configuration

frznforge is configured in five places:

| Where | What |
|---|---|
| `frznforge.config.jsonc` (project root) | Site title/URL, owner, colour palette, the list of repositories to ingest, organizations, contributors, notes folder, ingest limits, listing page size, the postprocess hook |
| `content/profile.md` | The profile page: frontmatter for links / location / pinned repos, markdown body rendered as your README |
| `content/orgs/<slug>.md` | One organization's page: frontmatter for links / pinned repos, markdown body |
| `content/notes/` | Your notes — one file, or one folder, per note |
| `.frznforge.json` inside each repository | Per-repo metadata: name, description, links, tags, template flag, license |

Everything is read at **build time**. Change something → run `frznforge build`, then
`frznforge dev` to look at the result (`frznforge dev` serves the last build; it rebuilds
nothing).

## The file is JSON with comments, and the comments are the point

`frznforge.config.jsonc` is JSONC: ordinary JSON, plus `//` and `/* … */` comments, plus a
permitted trailing comma before a `}` or a `]`. Nothing else JSON refuses is allowed — no
unquoted keys, no single quotes, no expressions. `512 * 1024` was legal in the 0.3.0
TypeScript config and is not legal here; write `524288`.

The comments are not decoration. This file is the only documentation open in front of you while
you edit it, so **every tool that writes to it preserves them**:

- `frznforge config migrate` re-emits every byte that is not JavaScript-specific verbatim —
  comments, blank lines and indentation included (`internal/config/migrate.go`);
- `frznforge init` splices new `repos` entries into the existing array and leaves the rest of
  the file alone;
- `frznforge init --web` edits one field at a time through the same comment-aware walkers, and
  re-reads the file afterwards to check it still parses to the config it approved.

If a tool ever hands you back a file with the comments stripped, that is a bug worth reporting,
not a formatting preference.

**A key the loader does not recognise is a hard error, not a shrug.** The decoder runs with
unknown fields disallowed, so a typo fails the run with

```
error: …/frznforge.config.jsonc: unrecognised or mistyped setting: json: unknown field "branchtrees"
```

rather than building a site that quietly ignores the setting you thought you had changed.

If there is no `frznforge.config.jsonc` at all:

```
error: no frznforge.config.jsonc in . — run `frznforge config migrate` if you still have a frznforge.config.ts
```

## `frznforge.config.jsonc`

Every key below is shown with its default. Only `owner.name` and `owner.handle` have no
default and must be supplied.

```jsonc
{
  "site": {
    "title": "frznforge",
    "url": "https://forge.example.com",        // optional — the host label under the site title
    // "description": "Kieran's frozen forge", // meta description, if profile.md has no bio
    // "base": "/mysite",                      // serve from a sub-path (a GitHub Pages project site)
  },

  "owner": {
    "name": "Kieran Wood",                     // required
    "handle": "kieran",                        // required — lowercase letters, digits, dashes
    "profile": "./content/profile.md",
    // "avatar": "images/owner.png",           // a file in public/ — see "Pictures" below
  },

  "theme": {
    "palette": "hearth",                       // "hearth" (warm) | "frost" (cool)
    "heat": { "hot": 7, "warm": 30, "neutral": 180, "cool": 365 },
  },

  "markdown": {
    "mermaid": true,                           // render mermaid code fences as diagrams
  },

  "content": {
    "orgs": "./content/orgs",                  // one <org-slug>.md per organization (optional)
  },

  "repos": [
    { "type": "local", "path": "../useful" },  // slug defaults to the directory name
    { "type": "local", "path": "D:/code/ezcv", "slug": "ezcv", "org": "canadian-coding",
      "overrides": { "template": true, "tags": ["python", "resume"] } },
    // repos hosted on a forge — see docs/user/importing.md
    { "type": "github", "owner": "Descent098", "repo": "sdu" },
  ],

  "notes": {
    "dir": "./content/notes",                  // one file = one note, one folder = one multi-file note
    "useMtime": false,                         // see the warning below before turning this on
    // "maxFileBytes": 262144,                 // defaults to ingest.maxBlobBytes
  },

  "organizations": [
    { "slug": "canadian-coding", "name": "Canadian Coding",
      "description": "Tools and teaching material.",
      // "avatar": "images/orgs/canadian-coding.png",
      "repos": ["useful", "frznforge"] },
  ],

  "contributors": [                            // credit people properly — see "Pictures" below
    { "name": "Kieran Wood", "emails": ["kieran@example.com", "work@example.com"],
      "avatar": "images/kieran.png", "url": "https://kieranwood.ca" },
  ],

  "hosting": {
    "sites": [
      // serve a repo's branch as a real site at /my-site/ (the forge view stays at /repos/…)
      // { "repo": "my-site", "slug": "my-site", "branch": "gh-pages" },
    ],
    "maxFileBytes": 20971520,                  // 20 MB — replaces maxBlobBytes on a hosted branch
  },

  "ingest": {
    "outDir": "./data",                        // forge.json + blobs/ + archives/ (gitignored)
    "maxBlobBytes": 524288,                    // 512 kB — files above this are listed, not stored
    "maxCommits": null,                        // cap per repo (null = all)
    "maxCommitAgeDays": null,                  // only commits from the last N days (needs git ≥ 2.37)
    "concurrency": 4,                          // repos scanned in parallel
    "tagTrees": 25,                            // newest N tags get browsable trees + archives (0 = none)
    "branchTrees": 10,                         // newest N *non-default* branches ("all" = every one)
    "archives": true,                          // zip source archives for the default branch + tags
    "cacheDir": "./.frznforge-cache",          // mirror clones of remote repos (gitignored)
    "fetch": "auto",                           // "auto" | "never" (offline) | "always"
    "failOnDegraded": false,                   // true = exit non-zero if any repo fell back to cache
    "skipMetaRefetches": false,                // true = serve cached provider metadata; releases + git still fetched
    "reuse": {
      "enabled": true,
      "maxAgeMinutes": 2,                      // don't re-fetch a remote fetched this recently
      "skipUnchanged": false,                  // opt-in: ls-remote first, skip the fetch if nothing moved
      "cooldownSeconds": null,                 // opt-in: don't re-fetch within N seconds of a success
    },
    "insights": {
      "enabled": true,
      "samples": 24,                           // max monthly code-size checkpoints per repo
      "maxBytesPerSample": 20971520,           // 20 MB — line-counting budget at one checkpoint
    },
  },

  "listing": { "pageSize": 50 },

  // "postprocess": { "command": "…", "dir": "." },   // see "The postprocess hook" below
}
```

### `site`

- `site.title` defaults to `"frznforge"`. It is the `<title>` suffix on every page and the
  brand text in the sidebar.
- `site.url` is **display only**: it is rendered as the host label under the site title in the
  sidebar (`internal/render/render.go:101`) and nowhere else. Leaving it out changes no URL — it
  is reserved for absolute links and feeds later.
- `site.description` is the home page's `<meta name="description">`, used only when
  `content/profile.md` has no `bio` in its frontmatter — the profile's own bio wins.
- `site.base` serves the whole site from a sub-path: with `"base": "/mysite"`, every link the
  site emits — pages, raw files, archives, the stylesheets, the favicon, the command palette's
  search index — is prefixed with `/mysite`, which is what a GitHub Pages *project* site (or any
  deploy that is not the domain root) needs. Any spelling (`mysite`, `/mysite/`) is normalised;
  omit it (or use `"/"`) for a root deploy. It must not contain whitespace, `#`, `%` or `?`. See
  [deploying.md](./deploying.md) for the host-side half.

### `owner`

- `owner.name` is required and is the display name at the top of the profile.
- `owner.handle` is required and must match `^[a-z0-9][a-z0-9-]*$` — it is rendered as
  `@handle`. Anything else fails the parse with
  `owner.handle "Kieran W" must be lowercase letters, digits and dashes`.
- `owner.profile` points at the markdown file rendered on the profile page.
- `owner.avatar` is a path inside `public/`. See **Pictures** below.

### `theme`

- `theme.palette` is `"hearth"` or `"frost"` and nothing else; a third value fails the parse.
  Layout and components are identical between the two — only colours change.
- `theme.heat` sets the *day boundaries* of the fire→ice recency accent: age < `hot` days is
  orange, then warm/neutral/cool, ≥ `cool` days is blue. The four values must be strictly
  ascending, or the parse fails with the numbers you wrote:
  `theme.heat boundaries must be strictly ascending (hot < warm < neutral < cool), got 30/7/180/365`.
  The profile and organization "touched recently" stats use the `hot` boundary, so the copy and
  the accent always agree. The colours themselves are not configurable — they are the palette's
  tokens, kept at WCAG AA by `internal/theme/contrast_test.go`.

### `markdown`

- `markdown.mermaid` (default **true**) renders ```` ```mermaid ```` fences as diagrams —
  everywhere markdown renders on the site: the profile, org pages, notes, your local repos,
  **and imported repos' READMEs and release notes**. Importing a repo is choosing to publish its
  content, so its diagrams are your call the same way its prose is; the imported-content
  renderer still strips raw HTML and filters URLs regardless, and mermaid runs in the visitor's
  browser with its own strict-mode sanitiser. Diagrams render from the copy of mermaid in
  `web/vendor/` that the build copies into `dist/` (no CDN — the published pages still call
  nothing), loaded only on pages that hold a diagram and only once one scrolls near. Without
  JavaScript the fence reads as a code block of the diagram source, which is also what you get
  with `"mermaid": false`.

### `repos`

`path` is absolute or relative to the config file. Bare repos work too. Every entry accepts
`slug`, `org`, `releases` and `overrides`; remote entries also accept `host` and `tokenEnv`.
The five types and their required fields:

| `type` | Needs | Notes |
|---|---|---|
| `local` | `path` | The only type that touches no network |
| `github` | `owner`, `repo` | `host` defaults to `https://api.github.com` |
| `gitlab` | `project` | Full namespaced path; `host` defaults to `https://gitlab.com` |
| `gitea` | `owner`, `repo`, `host` | Self-hosted, so there is no host to guess |
| `forgejo` | `owner`, `repo`, `host` | Same |

A missing required field is a parse error naming the index —
`repos[2]: gitea sources need an explicit host (they are self-hosted)` — as is an unknown type
or a slug that is not a lowercase slug. See [importing.md](./importing.md) for tokens, the
mirror cache and offline builds.

- `overrides` has the same shape as `.frznforge.json` and wins over it.
- `org` on a repo puts it in that organization; so does listing the repo's slug under
  `organizations[].repos`. Doing both is fine — membership is the union of the two. Naming an
  org that is not configured is a `repo-unknown-org` warning, not an error, on purpose: a typo
  in a grouping should not fail a build.

### `ingest`

- `maxCommitAgeDays` keeps only commits from the last N days (it uses
  `git rev-list --since-as-filter`, which needs git ≥ 2.37). The cutoff is measured from the
  repo's **newest commit date**, never from today's date — that keeps builds reproducible, and
  it means a dormant repo keeps its newest N days of history instead of going empty. Every
  branch always keeps its head commit. Everything derived from the commit list — commit counts,
  contributors, `createdAt`, insights — narrows with it. Ingest raises `commits-aged-out` when
  the age cutoff is what dropped history (when `maxCommits` truncates first, `commits-capped` is
  reported instead). File tables keep their per-file "Last commit" dates and subjects either
  way — commits the window dropped but a file or tag still points at are carried separately
  (`extraCommits`) without affecting the counts.
- `tagTrees` / `branchTrees` are the two biggest levers on build size, because the file browser
  (`tree`/`blob`/`raw`) is generated **per browsable ref** — a repo with 27 branches costs 27×
  its file count in pages. `tagTrees` keeps the newest N tags (those also get the download
  archive); `branchTrees` keeps the N most recently updated non-default branches, or `"all"`.
  The default branch always has a file browser and never counts against the cap, so the defaults
  give at most `1 + 10 + 25` browsable refs per repo. Capped refs still appear on the
  branches/tags pages and in the ref switcher — they just have no file browser, and ingest says
  so (`tag-trees-capped`, `branch-trees-capped`). `branchTrees` is the one key that takes either
  a number or a string: anything else fails with
  `ingest.branchTrees must be a non-negative number or "all"`. See
  [deploying.md](./deploying.md#3-how-big-will-my-site-be) for the page-count formula.
- `archives: false` skips zip generation entirely (no download links on the site). On a large
  account the source zips are hundreds of megabytes.
- `reuse` makes repeat builds cheap without changing a byte of output. Two mechanisms, both
  living in `cacheDir` sidecars so `forge.json` keeps its no-timestamp promise
  (`internal/ingest/reuse.go:20`):
  - **The run log** (`<cacheDir>/last-run.json`) records when and how well each remote source
    was last fetched. With `"fetch": "auto"`, a remote whose last fetch *fully* succeeded less
    than `maxAgeMinutes` ago is not re-fetched — its cached provider answers and mirror are used
    as-is. A repo whose last fetch failed, was rate-limited or served stale data is **always**
    retried, so a rate-limited run heals itself.
  - **The scan cache** (`<cacheDir>/scan/<digest>.json`) holds one repo's complete scan output,
    keyed by a digest over everything the scan reads: every ref name and object id, HEAD, the
    provider metadata, the overrides and the scan options. A repo whose inputs are unchanged
    replays the recorded result instead of re-scanning. Provider metadata is part of the key on
    purpose — descriptions and releases move with no ref moving, and a key without them would
    replay stale releases into a schema-valid artifact.

  A replayed result is validated and its blob and archive bytes are read back from the
  content-addressed stores, so the artifact is identical either way; anything missing or invalid
  falls back to a real scan. Deleting `cacheDir` is always safe — everything in it is rebuilt on
  demand. Bypass the whole thing for one run with:

  ```bash
  frznforge ingest --no-cache
  ```

  **There is no highlight cache in 0.4.0.** 0.2.0 memoised syntax highlighting under
  `<cacheDir>/highlight/` because Shiki was 84% of the render; chroma is a different order of
  tool and a completely cold render of this project's own 73-repository corpus is faster than
  the old *warm* one, so the memo was deleted rather than ported
  (`internal/highlight/highlight.go:15`). If you are upgrading, `<cacheDir>/highlight/` is dead
  weight — nothing writes or reads it — and safe to delete.

  The opposite of `--no-cache` is **`--backfill-metadata`**, for the case where the API quota,
  not the network, is what is failing:

  ```bash
  frznforge ingest --backfill-metadata
  ```

  It fetches provider metadata **only for repos that have none cached yet**, and touches git for
  nothing at all. Cloning is unmetered, so on a large account the commits always arrive — but
  metadata is metered, and an ordinary run re-requests it for every repo, spends the budget on
  repos whose answer is already on disk, and runs out before it reaches the ones that have
  nothing. That leaves the *same* repos blank on every run. This mode spends the whole budget on
  the gaps, so a few repeat runs fill them in, and it reports which:

  ```
    backfill: 13 filled, 4 still missing, 56 already had metadata (no network)
  ```

  It is not a partial ingest — every other repo replays its cached answer, and the artifact it
  writes is the one a full run would have written. The whole cycle is one command:

  ```bash
  frznforge build --backfill-metadata
  ```

  or, if the artifact is already filled in and you only want the pages rebuilt,

  ```bash
  frznforge build --no-ingest
  ```

  The render is not partial: a repo's description and license appear in the header of *every*
  one of its pages, and in the listing, the profile, the org pages and the search index, so
  there is no small set of pages to redo. `--no-cache` and `--backfill-metadata` are opposites
  and passing both is refused, as is `--refresh-meta` with `--backfill-metadata` (it asks for
  every repo's metadata, which is the spend the backfill exists to avoid); so is passing any of
  the three alongside `--no-ingest`, which would skip the ingest they configure.
- **`reuse.skipUnchanged` and `reuse.cooldownSeconds` are the two opt-in refetch controls**,
  both off by default because they trade a guarantee of freshness for speed:
  - `"skipUnchanged": true` runs one `git ls-remote` per remote repo and skips
    `git remote update` when the mirror already holds **every** ref the remote does, at the same
    object ids. Mirrors fetch per repository rather than per branch, so whole-repo is the only
    honest granularity: any difference at all — a moved branch, a new tag, a deleted ref — falls
    through to a normal fetch, as does any failure of the probe itself. It pays off on a large
    corpus of mostly-idle repos and costs one cheap round-trip otherwise.
  - `"cooldownSeconds": 3600` skips a repo entirely when its last **fully successful** fetch was
    less than an hour ago (the freshness window above is the same idea sized in minutes; this
    one is for hours). "Successful" means both halves — git *and* provider metadata — so a repo
    whose metadata was rate-limited is never held back. Skipped repos are reported during the
    run as `⚠️ <repo>: this repo is on cooldown`.
- **`"skipMetaRefetches": true`** (default `false`) stops re-requesting each repo's provider
  **metadata record** — the single call that returns its name, description, topics, links and
  license — and serves the copy already in `<cacheDir>/mirrors/<name>.meta.json` instead. That is
  one API request per repo per build that no longer happens, which on a large account is the
  difference between a build that fits inside an anonymous quota and one that runs out partway
  down the list. Reach for it when metadata requests, not the network, are what is failing.

  **Releases are still fetched, and so is git.** New commits and new releases still appear; only
  the description-and-topics record is frozen. A new release is content a visitor came for, where
  a changed description is cosmetic — and if release calls are what costs you, the lever for that
  is per-source and already there: `"releases": "tags"` on the repo entry — or `"releaseMode":
  "tags"` in the repo's own `.frznforge.json`, which spells the same idea differently — makes the
  call disappear entirely.

  It only ever serves a record a successful call actually produced — one carrying a name, a web
  URL and a clone URL. A cache file that is absent, empty (`"meta": {}`), truncated or
  hand-written does not satisfy it, so *"I asked once and got nothing"* never turns into *"never
  ask again"*; a repo with no cached metadata is always fetched, and is fetched first. A blank
  `description` is fine and does **not** disqualify a record: plenty of repositories have none,
  and requiring one would create a permanent class of repos that re-fetch on every build forever.

  It applies under `"fetch": "auto"` only, and there are three ways to override it:

  ```bash
  frznforge ingest --refresh-meta     # ignore the setting for this run, and change nothing else
  frznforge ingest --no-cache         # refetch everything: metadata, mirrors, scan cache
  ```

  …plus `"fetch": "always"`, which overrides it permanently. Deleting a repo's `.meta.json`
  works too. `--refresh-meta` overrides **this key and nothing else** — the freshness window and
  the cooldown above are time-bounded and expire on their own, so `--no-cache` is still the
  answer when one of those is in the way. It cannot be combined with `--backfill-metadata`, whose
  whole point is to fetch only the gaps.

  The three neighbours are easy to confuse, so:

  | | What it saves | Bounded by |
  |---|---|---|
  | `ingest.reuse` (window, cooldown, `skipUnchanged`) | git *and* API traffic | time — minutes or hours, then everything refreshes |
  | `--backfill-metadata` | one run's API quota, spent only on repos with no metadata | that run; it is a flag, not a setting |
  | `skipMetaRefetches` | one API request per repo, on every build | nothing — the record is served until you refresh it |

  That last row is the cost, and it is worth stating plainly: **the cached record is an input, and
  this key lets a stale input persist without limit.** Two machines whose caches are different
  ages publish different bytes for as long as the caches differ, where today that divergence heals
  itself within `maxAgeMinutes`. Every run says what it served:

  ```
    skipMetaRefetches: 71 repo(s) served cached provider metadata (0 metadata requests). Pass --refresh-meta to re-ask; releases and git were fetched as usual.
  ```

  Once a metadata fetch has actually happened *since you turned the key on*, the line gains the
  age of the oldest record it served:

  ```
    skipMetaRefetches: 71 repo(s) served cached provider metadata (0 metadata requests); oldest fetched 2026-06-14. Pass --refresh-meta to re-ask; releases and git were fetched as usual.
  ```

  It is missing at first, and that is honest rather than broken: the timestamp records when
  `FetchMeta` last ran, and adding this key to your config changes the config hash, which discards
  the run log that would have carried an older stamp forward. Until the next real fetch — a
  `--refresh-meta` run, a new repo, or a cache you deleted — frznforge genuinely does not know how
  old the record on disk is, and says nothing rather than guessing.

  It raises **no warning**, deliberately. Warnings are part of `forge.json` and are rendered in
  every page's footer, so a warning here would make the published file depend on how warm the
  building machine's cache happened to be — the same reasoning the freshness window's silence
  rests on. The console line and `--refresh-meta` are where the staleness surfaces instead.

  Two consequences to know before turning it on:

  - **`failOnDegraded` stops saying anything about the age of your metadata.** A deliberate skip
    is not a degraded run, so it raises no warning and never trips the exit code. A CI job that
    would rather fail than publish stale metadata should leave this key off, or pass
    `--refresh-meta`.
  - A repo **renamed on the provider** keeps being published under its old name, with its old
    web and clone URLs, until you refresh it — those come from the cached record like everything
    else the key freezes. Fetching is unaffected: an existing mirror updates with
    `git remote update --prune`, which uses the remote the mirror itself stores rather than
    anything the cache says, and a repo with no mirror yet has no cached record to skip on either.
    So this is a wrong name on a page, not a failed build. `--refresh-meta` clears it.
- **Rate limits back off per origin.** A 429 (or GitHub's 403-with-no-quota-left) is retried
  with exponential backoff keyed to the *host*, so all the repos being ingested in parallel from
  one forge wait behind a single timer instead of each hammering the window, while a different
  forge is unaffected. The provider's own `Retry-After` is honoured when it sends one. If a
  forge asks for longer than a minute, the run stops calling it altogether for that period and
  the remaining repos on that host fall back to their cached metadata immediately rather than
  each burning a full retry ladder. Repos with **no** cached metadata are fetched first, so a
  run that does hit a limit spends its budget on the repos that have nothing to fall back on.
- `"failOnDegraded": true` makes `frznforge ingest` exit non-zero when any repo ended the run
  published from cached or missing provider data (a `remote-fetch-failed`, `remote-rate-limited`,
  `remote-auth-missing` or `remote-cache-stale` warning). The artifact is still written and the
  warnings still print — only the exit code changes. For CI that would rather fail than quietly
  publish stale metadata after a rate limit. Note that `skipMetaRefetches` above raises no
  warning, so with that key on this check no longer covers metadata age at all.
- `insights` controls the `/repos/<slug>/insights/` page. Monthly commits and contributors are
  exact — they come from the commit list already in the artifact. Code size over time is
  **sampled**: at most `samples` monthly checkpoints (always including the first and last), each
  one measured with `git ls-tree`, skipping binary and vendored paths. Counting lines needs the
  file contents, so it stops once a checkpoint's text exceeds `maxBytesPerSample`; that point
  then reports bytes but no line count — and its byte total loses the binary filter too, because
  the blobs past the budget were never read and so could not be classified. The series is
  flagged approximate, the page says so, and you get an `insights-approximate` warning.
  Checkpoints are derived from the commit list and never from a clock, so the sampling is
  reproducible. `"enabled": false` drops the page.

### Pictures (`owner.avatar`, `organizations[].avatar`, `contributors[].avatar`)

Put the image anywhere under `public/` — `public/images/` is the convention — and give the path
relative to `public/` (`images/owner.png`; a leading `/` is fine too). The build copies
`public/` into `dist/` verbatim, so nothing is ingested, resized or cached: what you commit is
what ships, and a sub-path deploy (`site.base`) prefixes it automatically. Where no picture is
set the site keeps drawing the initials block it always has.

Paths only — **not URLs**. A frznforge site loads nothing from a third party, and an avatar
pointing at a forge's CDN would break that on every page it appears on; the config rejects
anything with a scheme, a leading `//`, a backslash, or a `..` segment
(`internal/config/config.go:677`):

```
error: config is not valid:
  - owner.avatar: must be a path inside public/, not a URL (frznforge pages load no third-party assets)
```

### `contributors[]` credits the people git only knows as an email address

Each entry claims one or more `emails` and can set `name`, `avatar`, `description` and `url`;
those then show wherever that person appears. Two things worth knowing:

- Listing several `emails` **merges** them into one contributor — commits summed, first and last
  activity widened — because one person committing from two machines is one person. The first
  address listed becomes the canonical one.
- `name` overrides whatever git recorded, which is usually the point.

Entries are purely additive: contributors are still discovered from git, and an entry whose
emails appear nowhere in this build is reported (`contributor-unknown-email`) and ignored — the
repo that person worked on may not be part of this site at all. `name` and at least one email
are required.

### `hosting.sites`

`hosting.sites` serves a repo's branch as a **real site** at `/<slug>/…` — a `gh-pages` branch
holding a built site is the classic case — while the normal forge view of the same repo stays at
`/repos/<slug>/`. `branch` left unset picks the first existing of `gh-pages`, `main`, `master`;
the hosted branch always gets a browsable file tree (even past the `branchTrees` cap) and its
files are stored up to `hosting.maxFileBytes` instead of `maxBlobBytes`, since built sites carry
bundles bigger than 512 kB.

Slugs are structural, so a bad one fails the parse rather than becoming a warning: a slug the
build itself owns (`repos`, `notes`, `orgs`, `search-index.json`, `index.html`, `404.html`,
`logo.png`, `favicon.ico`, and the legacy `_astro`) is refused, and so is the same slug used
twice. A mistyped `repo` (`hosting-unknown-repo`) or a branch that does not exist
(`hosting-branch-missing`) is a warning, and that entry is dropped rather than served.

> The reserved list predates 0.4.0 and does **not** yet cover the three directories the engine's
> own `web/` lands in — `css/`, `js/` and `vendor/`. A hosted site with one of those slugs would
> be written over the stylesheets. Pick something else until the list catches up.

Every hosted file becomes a file in `dist/`, so hosting a large site grows the build the way
`branchTrees` does. The last line of `frznforge build` is the total it wrote, which is how you
see the cost:

```
built 130 files (3.6 MB) in 115ms
```

### The postprocess hook

frznforge never minifies, bundles or hashes: the browser gets the bytes that are on disk, and
every asset under `public/` and `web/` is copied verbatim. That rule is worth keeping and not
worth *enforcing* on somebody who wants a minifier, a Brotli pass or an rsync — so there is
exactly one seam where your own tooling touches the output.

```jsonc
"postprocess": {
  "command": "npx esbuild --minify --outdir=dist dist/js/*.js",
  "dir": ".",          // optional; the project root by default
},
```

- **Nothing runs by default.** No block, no command, no process started.
- `command` is a shell line, handed to `sh -c` (or `cmd /C` on Windows), so a pipe, a glob or an
  `&&` all work.
- `dir` is the command's working directory, relative to the project root. The project root is
  the default because a hook that also reads a tool config needs it. Use forward slashes: a
  backslash is a path separator only on Windows, and the config refuses one rather than let
  `"dir": "tools\\min"` work on one machine and fail on the next.
- **The output directory arrives in the environment**, not as an argument, so the command stays
  a line you can paste into a terminal: `$FRZNFORGE_DIST_DIR` is the absolute output directory
  and `$FRZNFORGE_ROOT` is the project root. `FRZNFORGE_OUT_DIR` is deliberately *not* reused —
  that name already means the *ingest* output (`data/`), and a hook that emptied it would delete
  your artifact instead of your site.
- **It fires only after a build that succeeded**, as the last act of `frznforge build`. Handing
  a half-written `dist/` to a minifier publishes a site that is neither the old one nor the new
  one, which is the worst outcome available because it looks like success.
- A command that exits non-zero **fails the build**, with its own output already on screen and a
  message saying the site was left exactly as the command found it.

Two overrides, most explicit first, and each replaces only the command:

| Where | For |
|---|---|
| `--postprocess=<cmd>` | This one run |
| `$FRZNFORGE_POSTPROCESS` | A CI job with no business editing a checked-out file |
| `postprocess.command` | The project, checked in beside everything else it builds with |

An empty value at either of the first two levels is *not* a value and never silences a
configured hook — `--postprocess=` with nothing after it is a typo, and an unset variable
exported as `""` is a shell accident. Turning the hook off means deleting the block. `dir` is
not overridden by either: it says where this project's tools run, which does not stop being true
because someone changed the command for one build.

A block that would never run is refused rather than ignored:

```
error: config is not valid:
  - postprocess.dir is "tools/min" but postprocess.command is empty, so nothing would run — add a command, or delete the whole block
```

### Environment overrides

| Variable | Overrides |
|---|---|
| `FRZNFORGE_OUT_DIR` | `ingest.outDir` |
| `FRZNFORGE_CACHE_DIR` | `ingest.cacheDir` |
| `FRZNFORGE_BASE` | `site.base` |
| `FRZNFORGE_POSTPROCESS` | `postprocess.command` |
| `FRZNFORGE_LOG` | `--log=<level>` on any command |

The first three exist for the test suite and for CI that builds the same tree into two places.

### Diagnostics every run leaves behind

`build`, `ingest` and `dev` each write two files into `ingest.outDir` (`data/` by default),
whether or not you asked for them, because the run that goes wrong is the one nobody was
watching:

| File | What it holds |
|---|---|
| `data/frznforge.log` | Everything the last run did, at debug. Truncated per run. `frznforge dev` writes `frznforge-dev.log` instead, so a long-lived server cannot erase the build you are debugging |
| `data/frznforge-timings.jsonl` | What each step cost, one JSON object per line, appended so runs can be compared |

Neither is ever published — they live beside `forge.json`, not in `dist/`. `--log=<level>`
(`error`, `warn`, `info`, `debug`; a bare `--log` means debug) controls **stderr only** and
turns neither file off. Progress stays on stdout, so `frznforge build --log=debug 2> build.log`
separates the two cleanly. The companion binary `frzndebugger` reads both files — a terminal
explorer by default, `--web` for a browser, `--plain` for a script — and leads with steps that
started and never finished, which is the answer when a run stops.

### Committed content only

Only **committed** content on **branches** is read. frznforge runs `git` against the
repository's object database, never against your working tree, so uncommitted, staged, stashed
and untracked files never appear in the output. This is the load-bearing invariant of the
project and it has its own test (`internal/ingest/uncommitted_test.go`).

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

Metadata precedence, highest first: `overrides` in the config, then this file, then the
provider's API, then values derived from the repository itself.

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

`pinned` takes **repo slugs** (the URL segments from your config), not display names. A slug no
ingested repo has is skipped with a line on stderr:

```
[frznforge] profile.md pins unknown repo "usefull"
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
- **`useMtime`**: undated notes normally sort last. Setting `"useMtime": true` dates them from
  the file's modification time instead — convenient, but mtimes change on every fresh clone, so
  two builds of the same content stop producing identical output. Leave it off unless you want
  that trade.
- A missing `notes.dir` is not an error: if you declared a `notes` block you get a
  `notes-dir-missing` warning (and if you did not, no warning at all) plus an empty notes page.
  The distinction is deliberate — a warning has to mean "something you asked for did not
  happen", and `notes.dir` has a default, so a site that never opted into notes would otherwise
  carry a permanent warning in its footer.

## Organizations (`organizations` + `content/orgs/<slug>.md`)

Group repos under a named org with its own overview page at `/orgs/<slug>/` and its own repo
listing at `/orgs/<slug>/repos/`.

The config entry supplies the identity and (optionally) the membership:

```jsonc
"organizations": [
  { "slug": "canadian-coding", "name": "Canadian Coding",
    "description": "Tools and teaching material.",
    "repos": ["useful", "frznforge"] },
],
```

The markdown file supplies the prose, and is entirely optional — an org with no file still gets
a page built from the config and its repos:

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
`content.orgs` in the config. A slug must be lowercase letters, digits and dashes, and `name` is
required; both are parse errors.

Typos are warnings, never build failures: an org listing a repo that does not exist gets
`org-unknown-repo`, and a repo naming an org that is not configured gets `repo-unknown-org`.

**A markdown file no organization claims shows up on the site, not in a log.** An orgs markdown
file is only ever reached through an organization's slug, so one typo in the filename silently
discards the whole file — prose, links, pins. The 0.3.0 build printed a console warning; a
warning nobody reads is not a symptom, so `/orgs/` now carries a banner naming the file
(`internal/render/templates/page-orgs.gohtml:25`). Delete the file, or add the organization.

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
| The provider API rate-limited the run | Cached mirror used, else repo skipped (`remote-rate-limited`) |
| `"fetch": "never"`, or a refresh failed with a cache present | Cached mirror used as-is (`remote-cache-stale`) |
| `notes.dir` does not exist | No notes, empty notes page (`notes-dir-missing`, only if you declared a `notes` block) |
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
| `ingest.maxCommitAgeDays` cut a branch's history | The older commits are dropped, the branch head is always kept (`commits-aged-out`) |
| A note's filename has `#` or `%` in it | It still renders inline, but gets no raw/download URL (`note-file-unservable`) |
| A `contributors[]` entry claims emails no ingested repo has commits from | The entry decorates nobody and is ignored (`contributor-unknown-email`) |

Those 26 codes are the whole list (`internal/model/model.go:270`). They are printed by
`frznforge ingest` as `⚠ [code] repo: message` and counted in the site footer, where hovering the
count shows every one of them.

Exit code 1 is reserved for things you can only fix by editing something: a config that does not
parse or does not validate, an unwritable output directory, a missing `git`, or
`ingest.failOnDegraded` with a degraded repo. A repository's *state* is never one of them.
