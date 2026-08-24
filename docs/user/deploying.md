# Deploying

frznforge emits a folder of files. Any host that can serve a folder of files can serve it.
This guide covers what the build produces, the two ways to run ingest for a real deploy, and
the exact host settings for GitHub Pages, Cloudflare Pages, Netlify, and a plain web server.

---

## 1. What `npm run build` produces

```
$ npm run build      # = npm run ingest && astro build

frznforge ingest → …/data
  ▸ hello-forge
    ✓ hello-forge: 2 commits, 1 branches, 1 tags, 5 files
done: 1 repo(s), 1 note(s), 6 blob(s), 2 archive(s), 0 warning(s) in 617ms
…
[build] 28 page(s) built in 1.55s
[build] Complete!
```

`dist/` then looks like this:

```
dist/
├── index.html                     profile page
├── 404.html                       custom not-found page
├── favicon.ico, logo.png          anything you put in public/
├── search-index.json              feeds the Ctrl-K palette
├── _astro/                        hashed CSS + JS bundles
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
└── orgs/   index.html, <slug>/, <slug>/repos/
```

Preview the real thing before you ship it:

```bash
npm run preview       # http://localhost:4321/
```

## 2. The three things a host must do

**1. Serve the site at the root of a domain.** Every link frznforge emits is root-absolute
(`/repos/hello-forge/`, `/_astro/Base.*.css`). There is no `base` path support, so a *project*
URL like `https://you.github.io/my-forge/` will load the profile page with no CSS and every
link broken. Use a user/org Pages site, a Pages subdomain, or a custom domain.

**2. Resolve directory URLs to `index.html`.** The build uses Astro's default directory
format, so `/repos/hello-forge/` exists on disk as `repos/hello-forge/index.html`. A host that
does not do that mapping serves nothing. Every host below does it out of the box; a
hand-rolled nginx or Apache config needs one line (see [§7](#7-any-static-host)).

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

`R` is the multiplier that hurts. It is **1 (the default branch) + `ingest.branchTrees` other
branches + `ingest.tagTrees` tags**, capped at what the repo actually has — 36 by default. A
164-file repository with 16 browsable refs generates 2,624 file pages and 2,624 raw endpoints
before anything else.

**Measured.** Two repositories — a 5-file toy and a 164-file project with 5 branches and 11
tags — built to:

| | |
|---|---|
| Pages | 3,798 |
| Files on disk | 6,592 |
| `dist/` total | 403 MB |
| of that, source zips | 161 MB |
| of that, HTML | 145 MB |
| Build time | 1 m 57 s |

**Page weight.** The shared bundles are `_astro/Base.*.css` (≈ 59 KB) plus ≈ 57 KB of
JavaScript across three chunks (Svelte runtime, the listing island, the command palette) —
downloaded once and cached. HTML per page: the profile ≈ 35 KB, the repo listing ≈ 23 KB, a
repo overview ≈ 36 KB, a commit history page ≈ 28 KB. The outlier is the file view: syntax
highlighting is baked in at build time, so a 40 KB CSS file becomes an 850 KB HTML page and
the largest page in a build is usually the largest text file in a repository.

**The knobs**, in order of effect:

| Setting | Default | Effect |
|---|---|---|
| `ingest.branchTrees` | `10` | Non-default branches with a file browser. `0` = default branch only. Cuts tree/blob/raw pages proportionally |
| `ingest.tagTrees` | `25` | Tags with a file browser **and** a source zip. Also cuts archive size |
| `ingest.archives` | `true` | `false` removes every `.zip` — that was 40 % of the 403 MB above |
| `ingest.maxCommits` | `null` | Caps the per-branch commit list, and so the per-commit pages |
| `ingest.maxBlobBytes` | `524288` | Files above this are listed but not stored: no raw endpoint, no highlighted body |
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
file cap but large deploys are slow to upload. Trim with `archives: false` and a lower
`branchTrees` before you trim repositories.

---

## 4. Where should ingest run?

Ingest needs the git repositories. That single fact decides your pipeline.

### Option A — ingest locally, publish the output

Build on your machine and push `dist/` (or upload it) to the host.

```bash
npm run build
# then: drag dist/ into Netlify, or `wrangler pages deploy dist`, or rsync it
```

- **Works with `type: 'local'` repositories.** The only option if the repos you publish live
  on your disk, on a NAS, or on a private forge nothing in the cloud can reach.
- No CI secrets, no API tokens in a runner, no rate limits.
- Fully reproducible: the same repos at the same commits produce a byte-identical
  `data/forge.json`, so you can diff two builds.
- **But** the site only updates when you build, and if you commit `dist/` you are committing
  hundreds of megabytes of generated HTML. `dist/` is git-ignored by default, and it should
  stay that way unless your host deploys from a branch (see the `gh-pages` note below).

### Option B — ingest in CI

The runner clones what it needs and builds on every push.

- **Repositories must be reachable from the runner.** For anything other than the site's own
  repository, that means importing it: `type: 'github' | 'gitlab' | 'gitea' | 'forgejo'`, which
  mirror-clones over HTTPS. A `type: 'local'` path pointing at your laptop does not exist in
  CI — you get `⚠ [repo-not-found]` and a repo missing from the site.
- **`type: 'local', path: '.'`** (publishing the site's own repository) needs
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

**Deploy from Actions, not from a branch.** The Actions path uploads an artifact and serves it
verbatim. The branch path (`gh-pages`) runs the files through Jekyll, which **ignores every
directory starting with `_`** — including `_astro/`, which is all of your CSS and JavaScript.
If you must deploy from a branch, put an empty `.nojekyll` file in `public/` so it lands in
`dist/`.

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
          fetch-depth: 0          # full history + tags for `type: 'local', path: '.'`

      - uses: actions/setup-node@v4
        with:
          node-version: '24'
          cache: npm

      - run: npm ci

      # Keep the mirror clones between runs: without this every build re-clones
      # every imported repository from scratch.
      - uses: actions/cache@v4
        with:
          path: .frznforge-cache
          key: frznforge-cache-${{ github.run_id }}
          restore-keys: frznforge-cache-

      - run: npm run build
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

- **URL.** A user or organization site (`you.github.io`) is served at the root and works.
  A project site (`you.github.io/my-forge/`) is **not** — see [§2](#2-the-three-things-a-host-must-do).
  Attach a custom domain instead: add the hostname under Settings → Pages and put a `CNAME`
  file containing that hostname in `public/` so it survives every deploy.
- **Tokens.** `secrets.GITHUB_TOKEN` is minted per run and scoped to the repository the
  workflow lives in. It raises the API rate limit and is enough if you only publish that one
  repository. Importing anything else — public or private — needs a personal access token in
  a secret, exposed as `FRZNFORGE_GITHUB_TOKEN` (checked before `GITHUB_TOKEN`).
- **Cache key.** `github.run_id` guarantees a fresh entry each run and `restore-keys` pulls
  the most recent previous one, which is how you get "restore, update, save" out of
  `actions/cache`. Delete the cache entry if a mirror ever gets wedged; the next build
  re-clones.
- **Offline-ish builds.** If you would rather not talk to the forge on every run, set
  `ingest.fetch: 'never'` and rely on the cache. It will warn (`remote-cache-stale`) and
  publish whatever it last saw.
- **Size.** Pages targets 1 GB per site and the workflow has to upload the whole artifact
  every time. If a build takes minutes to upload, turn `ingest.archives` off first.

### Publishing a locally-built site to Pages

If your repositories are local-only, build on your machine and push the output to a branch:

```bash
npm run build
touch dist/.nojekyll            # branch deploys go through Jekyll
cd dist && git init -b gh-pages && git add -A \
  && git commit -m "publish" \
  && git push --force <your-repo-url> gh-pages
```

Then set Settings → Pages → Source to **Deploy from a branch → gh-pages / (root)**. Force-push
each time; there is no value in the history of a generated folder.

---

## 6. Cloudflare Pages and Netlify

Both auto-detect nothing useful here, so set the two fields by hand.

| | Cloudflare Pages | Netlify |
|---|---|---|
| Build command | `npm run build` | `npm run build` |
| Output directory | `dist` | `dist` |
| Node version | `NODE_VERSION=24` env var | `NODE_VERSION=24` env var, or `.nvmrc` |
| Directory URLs | Yes, automatic | Yes, automatic |
| `/x` → `/x/` | 308 redirect, automatic | Served either way |
| 404 page | Root `404.html`, automatic | Root `404.html`, automatic |
| Hard limits | 20,000 files, 25 MB per file, per deployment | No file cap; upload time grows with the deploy |

Both build in CI, so [§4 Option B](#option-b--ingest-in-ci) applies: only imported
repositories exist in the build container. Put provider tokens in the project's environment
variables (`FRZNFORGE_GITHUB_TOKEN`, `FRZNFORGE_GITEA_TOKEN`, …), never in
`frznforge.config.ts`.

Neither runs Jekyll, so `_astro/` is safe.

**Cloudflare's 20,000-file limit is the one that bites.** The two-repository example in
[§3](#3-how-big-will-my-site-be) already used 6,592 files. If a deploy is rejected for file
count, set `ingest.archives: false` and `ingest.branchTrees: 0` and rebuild.

To deploy an already-built directory instead of building in CI:

```bash
npx wrangler pages deploy dist --project-name=my-forge     # Cloudflare
npx netlify deploy --prod --dir=dist                       # Netlify
```

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

    # long-lived, content-hashed bundles
    location /_astro/ {
        add_header Cache-Control "public, max-age=31536000, immutable";
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
meant for reading.

---

## 8. Keeping it fresh

The site is a snapshot. Nothing updates until something rebuilds it.

| Setup | How it refreshes |
|---|---|
| Local build + rsync/branch push | When you run `npm run build` |
| CI on push | When you push to the site repository — **not** when the imported repositories change |
| CI on a schedule | Every cron tick; the imported repos are re-fetched, the local ones are whatever the checkout has |
| CI triggered from elsewhere | A `repository_dispatch` from the other repository's own workflow, for near-immediate updates |

For most people a nightly `schedule:` plus `workflow_dispatch:` is right. Rebuilding more
often than the repositories change just burns rate limit.

---

## 9. Pre-flight checklist

- [ ] `npm run build` finishes and prints `0 warning(s)`, or you know why it doesn't.
- [ ] `npm run preview` shows the profile page with styling, and `/repos/<slug>/` loads.
- [ ] The deploy target is a domain **root**, not a subpath.
- [ ] Every repository in `frznforge.config.ts` is reachable from wherever ingest runs.
- [ ] Tokens live in the host's secret store; `git grep` finds none in the repository.
- [ ] `dist/` fits the host's file-count and size limits.
- [ ] A mistyped URL lands on the frznforge 404 page.
