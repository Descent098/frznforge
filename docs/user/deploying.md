# Deploying

frznforge emits a folder of files. Any host that can serve a folder of files can serve it.
This guide covers what the build produces, how large it gets, the two ways to run ingest for a
real deploy, and the exact host settings for GitHub Pages, Cloudflare Pages, Netlify, and a
plain web server.

---

## 1. What `frznforge build` produces

```
$ frznforge build      # = ingest, then render

frznforge build: scanning first — pass --no-ingest to render the artifact on disk instead.
frznforge ingest → /home/you/my-site/data
  ▸ hello-forge
    ✓ hello-forge: 2 commits, 1 branches, 1 tags, 5 files
done: 1 repo(s), 1 note(s), 6 blob(s), 2 archive(s), 0 warning(s) in 460ms

built 165 files (3.9 MB) in 158ms
```

(43 of those 165 are the site's own pages and endpoints. The other 122 are `web/` and `public/`
copied in — most of them the vendored mermaid build.)

`dist/` then looks like this:

```
dist/
├── index.html                     profile page
├── 404.html                       custom not-found page
├── favicon.ico, logo.png          anything you put in public/, copied verbatim
├── search-index.json              feeds the Ctrl-K palette
├── css/                           from web/css/, copied verbatim — no bundling, no hashing
├── js/                            from web/js/, copied verbatim
├── vendor/mermaid/                the vendored diagram renderer, loaded only where used
├── repos/
│   ├── index.html                 the repository listing
│   └── <slug>/
│       ├── index.html             overview
│       ├── branches/, tags/, releases/, insights/
│       ├── releases/<tag>/
│       ├── commits/<ref>/         paginated: page/2/, page/3/, …
│       ├── commit/<sha>/
│       ├── tree/<ref>/<dir>/      file browser
│       ├── blob/<ref>/<path>/     one file, syntax-highlighted
│       ├── raw/<ref>/<path>       the bytes, under the file's real name
│       └── archive/<ref>.zip      source download
├── notes/  <slug>/, <slug>/raw/<path>
├── orgs/   index.html, <slug>/, <slug>/repos/
└── <hosted-slug>/                 one hosting.sites entry, served verbatim:
                                   index.html, assets, whatever the branch holds
```

**There is no `_astro/`, and nothing is content-hashed.** 0.4.0 has no bundler: `public/` and
`web/` are copied into `dist/` byte for byte, under their own names. That is a guarantee rather
than an omission — the browser gets the files that are on disk — and it changes how you set
cache headers (see [§7](#7-any-static-host)). If you want a minifier or a Brotli pass, the
`postprocess` hook in [configuration.md](./configuration.md#the-postprocess-hook) is the seam
for it; nothing runs by default.

Preview the real thing before you ship it:

```bash
frznforge dev        # http://localhost:4321/
```

## 2. The three things a host must do

**1. Serve the site at the path it was built for.** By default every link frznforge emits is
absolute from the root (`/repos/hello-forge/`, `/css/global.css`), so a site built with the
default config has to live at a domain root. To serve it from a sub-path instead — a GitHub
Pages *project* URL like `https://you.github.io/my-forge/` — set `site.base`:

```jsonc
"site": { "base": "/my-forge" },
```

Every URL the site emits is then prefixed with it, including the command palette's search index
and the raw/archive routes. The two must agree: a site built with `"base": "/my-forge"` served
at the root is as broken as a root build served under a sub-path. See [§5](#5-github-pages) for
the Pages recipe.

**2. Resolve directory URLs to `index.html`.** `/repos/hello-forge/` exists on disk as
`repos/hello-forge/index.html`. A host that does not do that mapping serves nothing. Every host
below does it out of the box; a hand-rolled nginx or Apache config needs one line (see
[§7](#7-any-static-host)).

**3. Serve `404.html` for unknown paths.** Not strictly required, but without it a mistyped
URL gets the host's generic error page instead of the site's.

A trailing-slash redirect is *not* required. frznforge only ever links to the slash form, so
`/repos/x` never appears in the HTML; hosts that redirect `/repos/x` → `/repos/x/` are just
being kind to people editing the address bar.

---

## 3. How big will my site be?

Big. Plan for it before you pick a host.

**Pages.** Per repository:

```
1                     overview
+ 3                   branches, tags, releases
+ 1                   insights          (unless ingest.insights.enabled is false)
+ R × (1 + D)         tree pages        R = browsable refs, D = directories
+ R × F               blob pages        F = files
+ R × F'              raw endpoints     F' = files whose content was stored (≤ maxBlobBytes)
+ C                   one page per commit
+ Σ ceil(Bc / 50)     commit history, 50 commits per page, per branch
+ releases            one page per release or annotated tag
+ archives            one .zip per archived ref
```

plus `/`, `/repos/`, `/404`, `/search-index.json`, and the notes and orgs pages.

Hosted sites are counted separately, because they are per `hosting.sites` entry rather than per
repository — most repos contribute none, and one repo hosted twice under two slugs contributes
twice. Each entry adds **one file per file on the hosted branch** (emitted at its literal path
under `/<slug>/`), and, because a hosted branch always gets a browsable file tree, it also adds
one to that repo's `R` — an increment `"branchTrees": 0` cannot take back.

`R` is the multiplier that hurts. It is **1 (the default branch) + `ingest.branchTrees` other
branches + `ingest.tagTrees` tags**, capped at what the repo actually has — 36 by default.

**Measured**, on this project's own site: a **73-repository** account at the default caps, plus
5 notes, 1 organization and one hosted site.

| | |
|---|---|
| Files in `dist/` | 47,158 |
| `dist/` total | 2.7 GB |
| of that, HTML | 1.5 GB across 29,193 pages |
| of that, source zips | 628 MB across 109 files |
| of that, committed images served raw | ~460 MB |
| Build time (render only, 32 threads) | 61 s |
| Build time (`--serial`, same bytes out) | 119 s |

Where the files go, by family — the two multiplier families are 81% of everything:

| Family | Files |
|---|---|
| `blob/` (one page per file per ref) | 19,683 |
| `raw/` (the stored bytes, per file per ref) | 18,392 |
| `tree/` (one page per directory per ref) | 4,093 |
| `commit/` | 3,831 |
| `commits/` (history, 50 per page) | 197 |
| `releases/` | 109 |
| `archive/` (zips) | 109 |
| `branches/`, `tags/`, `insights/` | 73 each |

**Page weight.** The shared stylesheets are `css/global.css` (61 KB) and `css/repo.css` (25 KB),
loaded on every page; three more page-specific sheets bring `css/` to 100 KB in total. All of
`js/` is 49 KB across ten plain ES modules — downloaded once and cached, no framework runtime.
`vendor/mermaid/` is 3.4 MB across 105 files and is fetched **only** on a page that actually
contains a diagram, and only once one scrolls near it.

Two payloads scale with your account rather than with the page: `search-index.json` was 1,021 KB
on the 73-repo corpus, downloaded on the first Ctrl-K and not before; and `/repos/index.html`
was 124 KB, because it server-renders page 1 plus the JSON the listing element filters over.

For the pages themselves, HTML across that whole build: **median 36 KB, mean 54 KB, 90th
percentile 69 KB.** The tail is the file view. Syntax highlighting is baked in at build time, so
the largest page in a build is the largest text file in a repository — and a *minified* file is
the worst case, because it is one enormous line of dense tokens. A vendored 470 KB
`mermaid.esm.min` chunk became a **5.4 MB HTML page**. Files over 500 KB or 5,000 lines are
served in full but not highlighted, with a line on the page saying why, which caps that
expansion.

**The knobs**, in order of effect:

| Setting | Default | Effect |
|---|---|---|
| `ingest.branchTrees` | `10` | Non-default branches with a file browser. `0` = default branch only. Cuts tree/blob/raw pages proportionally |
| `ingest.tagTrees` | `25` | Tags with a file browser **and** a source zip. Also cuts archive size |
| `ingest.archives` | `true` | `false` removes every `.zip` — that was 628 MB of the 2.7 GB above |
| `ingest.maxBlobBytes` | `524288` | Files above this are listed but not stored: no raw endpoint, no highlighted body. This is the lever on the raw-image bulk |
| `ingest.maxCommits` | `null` | Caps the per-branch commit list, and so the per-commit pages |
| `ingest.maxCommitAgeDays` | `null` | Drops commits older than N days (anchored to the repo's newest commit, never the clock), cutting the same pages; composes with `maxCommits` |
| `ingest.insights.enabled` | `true` | `false` drops one page per repo and the sampling work at ingest |

Capping either ref setting is reported so you can see it happened:

```
⚠ [branch-trees-capped] frznforge: 11 of 27 branches have browsable trees (ingest.branchTrees = 10)
⚠ [tag-trees-capped] ezcv: 25 of 40 tags have browsable trees (ingest.tagTrees = 25)
```

A capped branch or tag still appears on the branches/tags page and in the ref switcher — it
just has no file browser.

**Host limits worth checking against those numbers:** Cloudflare Pages allows 20,000 files per
deployment and 25 MB per file; GitHub Pages targets 1 GB per published site; Netlify has no
file cap but large deploys are slow to upload. **A whole account at the default caps fits none
of them.** Trim with `"archives": false` and a lower `branchTrees` before you trim repositories,
and check the last line of `frznforge build` — it reports the file count and total size every
run.

---

## 4. Where should ingest run?

Ingest needs the git repositories. That single fact decides your pipeline.

### Option A — ingest locally, publish the output

Build on your machine and push `dist/` (or upload it) to the host.

```bash
frznforge build
# then: drag dist/ into Netlify, or `wrangler pages deploy dist`, or rsync it
```

- **Works with `"type": "local"` repositories.** The only option if the repos you publish live
  on your disk, on a NAS, or on a private forge nothing in the cloud can reach.
- No CI secrets, no API tokens in a runner, no rate limits.
- Fully reproducible: the same repos at the same commits produce a byte-identical
  `data/forge.json`, and two builds of the same artifact produce a byte-identical `dist/` — so
  you can diff two deploys and get a meaningful answer.
- **But** the site only updates when you build, and if you commit `dist/` you are committing
  gigabytes of generated HTML. `dist/` is git-ignored by default, and it should stay that way
  unless your host deploys from a branch (see the `gh-pages` note below).

### Option B — ingest in CI

The runner clones what it needs and builds on every push.

- **Repositories must be reachable from the runner.** For anything other than the site's own
  repository, that means importing it: `"type": "github" | "gitlab" | "gitea" | "forgejo"`,
  which mirror-clones over HTTPS. A `"type": "local"` path pointing at your laptop does not
  exist in CI — you get `⚠ [repo-not-found]` and a repo missing from the site.
- **`{ "type": "local", "path": "." }`** (publishing the site's own repository) needs
  `fetch-depth: 0` on `actions/checkout`, or you get a shallow clone with a truncated history
  and no tags. Even then a checkout only creates **one** local branch, and frznforge reads
  `refs/heads` — so the site will show a single branch. If you want all of them, import the
  repository as a remote source instead of pointing at `.`; a mirror clone has every ref.
- Private repositories need a token in the runner's environment. The token is used for the
  API *and* for the mirror clone.
- Every build re-clones unless you cache `.frznforge-cache/` between runs.

### Option C — both

Import the public repositories so CI keeps them fresh, and keep the private/local ones out of
the config entirely. Rebuild on a schedule (`on: schedule`) rather than only on push, because
the site's content changes when the *other* repositories change, not when this one does.

---

## 5. GitHub Pages

Pages serves directory URLs, redirects `/x` → `/x/`, and uses a root `404.html`. Nothing to
configure for any of that.

**Project site (served at `https://<user>.github.io/<repo>/`)?** Set the sub-path in the
config and every emitted link carries it:

```jsonc
"site": { "base": "/<repo>" },
```

A *user/organization* site (`<user>.github.io`) is a root deploy — leave `base` out. The
same knob covers any host that serves the forge from a subdirectory.

**Deploy from Actions, not from a branch.** The Actions path uploads an artifact and serves it
verbatim. The branch path (`gh-pages`) runs the files through Jekyll, which has opinions about
directory names it was never asked for. Nothing frznforge emits starts with `_` any more — that
hazard died with `_astro/` — but an empty `.nojekyll` file in `public/` (so it lands in `dist/`)
costs nothing and removes the whole class of surprise. Do it for any branch deploy.

Set the repository's **Settings → Pages → Source** to **GitHub Actions**, then add
`.github/workflows/deploy.yml`:

```yaml
name: Deploy forge

on:
  push:
    branches: [main]
  schedule:
    - cron: '17 5 * * *'    # rebuild daily: imported repos change on their own
  workflow_dispatch:

permissions:
  contents: read
  pages: write
  id-token: write

concurrency:
  group: pages
  cancel-in-progress: false

jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0          # full history + tags for { "type": "local", "path": "." }

      - uses: actions/setup-go@v5
        with:
          go-version: '1.24'

      - run: go build -o frznforge ./cmd/frznforge

      # Keep the mirror clones between runs: without this every build re-clones
      # every imported repository from scratch.
      - uses: actions/cache@v4
        with:
          path: .frznforge-cache
          key: frznforge-cache-${{ github.run_id }}
          restore-keys: frznforge-cache-

      - run: ./frznforge build
        env:
          # The built-in token only reaches THIS repository. To import others —
          # or any private repository — use a PAT in FRZNFORGE_GITHUB_TOKEN.
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          # FRZNFORGE_GITHUB_TOKEN: ${{ secrets.FORGE_GITHUB_PAT }}

      - uses: actions/upload-pages-artifact@v3
        with:
          path: dist

  deploy:
    needs: build
    runs-on: ubuntu-latest
    environment:
      name: github-pages
      url: ${{ steps.deployment.outputs.page_url }}
    steps:
      - id: deployment
        uses: actions/deploy-pages@v4
```

Notes.

- **No Node step.** `go build` is the whole toolchain, and the two Go dependencies are vendored
  into the module cache `actions/setup-go` already keeps. If you are upgrading a 0.3.0 workflow,
  delete `actions/setup-node` and `npm ci` — see
  [migrating.md](./migrating.md#2-node-is-no-longer-required-to-build-go-and-git-are).
- **URL.** A user or organization site (`you.github.io`) is served at the root and needs no
  `site.base`. A project site (`you.github.io/my-forge/`) needs `"base": "/my-forge"` — the
  recipe above. If you would rather serve it at a root anyway, attach a custom domain: add the
  hostname under Settings → Pages and put a `CNAME` file containing that hostname in `public/`
  so it survives every deploy, and drop `site.base`.
- **Tokens.** `secrets.GITHUB_TOKEN` is minted per run and scoped to the repository the
  workflow lives in. It raises the API rate limit and is enough if you only publish that one
  repository. Importing anything else — public or private — needs a personal access token in
  a secret, exposed as `FRZNFORGE_GITHUB_TOKEN` (checked before `GITHUB_TOKEN`).
- **Cache key.** `github.run_id` guarantees a fresh entry each run and `restore-keys` pulls
  the most recent previous one, which is how you get "restore, update, save" out of
  `actions/cache`. Delete the cache entry if a mirror ever gets wedged; the next build
  re-clones.
- **Offline-ish builds.** If you would rather not talk to the forge on every run, set
  `"fetch": "never"` and rely on the cache. It will warn (`remote-cache-stale`) and publish
  whatever it last saw — and that warning count shows in the footer of every page.
- **Size.** Pages targets 1 GB per site and the workflow has to upload the whole artifact
  every time. If a build takes minutes to upload, turn `ingest.archives` off first.
- **Diagnostics.** Every run writes `data/frznforge.log` and `data/frznforge-timings.jsonl`.
  When a CI build fails or stalls, upload `data/` with `actions/upload-artifact` and read it
  locally with `frzndebugger` — it leads with steps that started and never finished. Neither
  file ever reaches `dist/`.

### Publishing a locally-built site to Pages

If your repositories are local-only, build on your machine and push the output to a branch:

```bash
frznforge build
touch dist/.nojekyll            # branch deploys go through Jekyll
cd dist && git init -b gh-pages && git add -A \
  && git commit -m "publish" \
  && git push --force <your-repo-url> gh-pages
```

Then set Settings → Pages → Source to **Deploy from a branch → gh-pages / (root)**. Force-push
each time; there is no value in the history of a generated folder.

---

## 6. Cloudflare Pages and Netlify

Both auto-detect nothing useful here, so set the fields by hand. Neither ships a Go toolchain
you can rely on by default, so the build command has to install one or the binary has to be
built elsewhere.

| | Cloudflare Pages | Netlify |
|---|---|---|
| Build command | `go build -o frznforge ./cmd/frznforge && ./frznforge build` | same |
| Output directory | `dist` | `dist` |
| Go version | `GO_VERSION=1.24` env var | `GO_VERSION=1.24` env var |
| Directory URLs | Yes, automatic | Yes, automatic |
| `/x` → `/x/` | 308 redirect, automatic | Served either way |
| 404 page | Root `404.html`, automatic | Root `404.html`, automatic |
| Hard limits | 20,000 files, 25 MB per file, per deployment | No file cap; upload time grows with the deploy |

Check the current image's Go support before you rely on the build running there at all; if it
does not, [Option A](#option-a--ingest-locally-publish-the-output) plus a direct upload is the
route with no surprises:

```bash
npx wrangler pages deploy dist --project-name=my-forge     # Cloudflare
npx netlify deploy --prod --dir=dist                       # Netlify
```

Both build in CI, so [§4 Option B](#option-b--ingest-in-ci) applies: only imported repositories
exist in the build container. Put provider tokens in the project's environment variables
(`FRZNFORGE_GITHUB_TOKEN`, `FRZNFORGE_GITEA_TOKEN`, …), never in `frznforge.config.jsonc`.

Neither runs Jekyll, so no `.nojekyll` is needed.

**Cloudflare's 20,000-file limit is the one that bites**, and it bites early: the 73-repository
example in [§3](#3-how-big-will-my-site-be) is 47,158 files, and the two multiplier families
alone — `blob/` and `raw/` — account for 38,000 of them. If a deploy is rejected for file count,
set `"archives": false` and `"branchTrees": 0` and rebuild, then look at `maxBlobBytes` and
`tagTrees`. Note that
`"branchTrees": 0` does *not* drop a hosted branch — hosted sites are exempt from the cap by
design — so if you host large built sites, the lever there is `hosting.sites` itself.

Netlify's `_redirects` and `_headers` are not needed. If you add one, put it in `public/`.

---

## 7. Any static host

Object storage, a VPS, a NAS, the box under your desk. Copy `dist/` and make sure the server
does the directory-index mapping.

```bash
rsync -av --delete dist/ you@host:/var/www/forge/
```

`--delete` matters: refs come and go, and a stale `tree/old-branch/` left behind is a page
nobody can reach but a crawler will still find.

**Caching, and why it changed in 0.4.0.** The old build emitted content-hashed bundles under
`/_astro/`, so `immutable` was the correct header for them. There is no bundler now: `css/`,
`js/` and `vendor/` are copied verbatim under stable names, so a file's *contents* can change
without its URL changing. **Do not mark them `immutable`** — a visitor would keep a stale
stylesheet until they cleared their cache. Give them a modest `max-age` and let revalidation do
the rest; the whole shared payload is under 150 KB, so there is very little to win here anyway.
`vendor/mermaid/` is the one directory large enough to be worth caching hard, and it only moves
when you upgrade the engine.

**nginx**

```nginx
server {
    listen 80;
    server_name forge.example.com;
    root /var/www/forge;

    location / {
        try_files $uri $uri/ $uri/index.html =404;
    }

    error_page 404 /404.html;

    # Not content-hashed: cache them, but let the browser revalidate.
    location ~ ^/(css|js)/ {
        add_header Cache-Control "public, max-age=3600, must-revalidate";
    }

    # The vendored diagram renderer only changes when the engine does.
    location /vendor/ {
        add_header Cache-Control "public, max-age=604800";
    }
}
```

**Apache** — `DirectoryIndex index.html` is already the default, so all you need is the 404:

```apache
DocumentRoot /var/www/forge
ErrorDocument 404 /404.html
```

**Caddy**

```
forge.example.com {
    root * /var/www/forge
    file_server
    try_files {path} {path}/index.html
    handle_errors {
        rewrite * /404.html
        file_server
    }
}
```

**S3 + CloudFront.** S3 website hosting resolves `/repos/x/` to `repos/x/index.html` when you
set the index document to `index.html`, and the error document to `404.html`. S3 as a plain
*origin* behind CloudFront does **not** — it returns 403 for a directory path. Either use the
website endpoint as the origin, or attach a CloudFront Function that appends `index.html` to
any request path ending in `/`.

**MIME types.** The `raw/` endpoints are written under the file's real name, so the server
decides their content type. A `README.md` served as `text/markdown` downloads in most
browsers rather than rendering — that is normal, and the highlighted `blob/` page is the one
meant for reading. `frznforge dev` carries its own MIME table for the same paths, so what you
see locally is what a correctly configured host will serve.

---

## 8. Keeping it fresh

The site is a snapshot. Nothing updates until something rebuilds it.

| Setup | How it refreshes |
|---|---|
| Local build + rsync/branch push | When you run `frznforge build` |
| CI on push | When you push to the site repository — **not** when the imported repositories change |
| CI on a schedule | Every cron tick; the imported repos are re-fetched, the local ones are whatever the checkout has |
| CI triggered from elsewhere | A `repository_dispatch` from the other repository's own workflow, for near-immediate updates |

For most people a nightly `schedule:` plus `workflow_dispatch:` is right. Rebuilding more
often than the repositories change just burns rate limit.

---

## 9. Pre-flight checklist

- [ ] `frznforge build` finishes and ingest prints `0 warning(s)`, or you know why it doesn't.
- [ ] `frznforge dev` shows the profile page **with styling** — if it is unstyled, `web/` is not
      in the project directory and `dist/css/` is empty.
- [ ] `/repos/<slug>/` loads, and Ctrl-K opens the palette and finds a repository.
- [ ] The deploy target is a domain **root** — or `site.base` names the subpath it lives under.
- [ ] Every repository in `frznforge.config.jsonc` is reachable from wherever ingest runs.
- [ ] Tokens live in the host's secret store; `git grep` finds none in the repository.
- [ ] `dist/` fits the host's file-count and size limits — the build's last line tells you both.
- [ ] A mistyped URL lands on the frznforge 404 page.
