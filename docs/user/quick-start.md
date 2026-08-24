# Quick start

Ten minutes from nothing to a running site: one git repository on your disk, one imported
from GitHub, a profile page and a note.

You need **Node ≥ 22.12** and **git** on your `PATH`. Nothing else — no database, no server,
no account anywhere.

```bash
node --version   # v24.6.0
git --version    # git version 2.50.1
```

---

## 1. Get the project

frznforge is the site *and* the generator: you clone it, point it at your repositories, and
commit your own config on top.

```bash
git clone https://github.com/Descent098/frznforge my-forge
cd my-forge
npm install
```

The clone arrives configured as *this project's own* demo site. This guide edits those files
in place, which is the fastest way to see something work. When you are ready to keep your
content out of the engine's git history — or you just want clean starting files rather than
someone else's — `npm run frznforge -- new <dir>` scaffolds them, and
[starting-a-site.md](./starting-a-site.md) explains the layouts that work.

The files you will touch:

| File | What it is |
|---|---|
| `frznforge.config.ts` | Which repositories to publish, your name, the palette, ingest limits |
| `content/profile.md` | Your profile page — frontmatter for links, markdown body for prose |
| `content/notes/` | Gist-style notes (optional) |
| `content/orgs/` | Organization pages (optional) |

Everything else is the generator.

### Clear the demo content first

The clone is a working site, so `content/` arrives full of the author's material: **five demo
notes** under `content/notes/` and **one organization page**, `content/orgs/canadian-coding.md`.
Neither is yours, and the org page in particular will make *every* build print

```
[frznforge] content/orgs/canadian-coding.md does not match any organization in frznforge.config.ts — ignored.
```

the moment you replace the `repos`/`organizations` config in step 2. Clear both now — the rest
of this guide assumes you did, and step 6 writes a note of your own:

```bash
rm -rf content/notes/* content/orgs/*.md
```

```powershell
Remove-Item -Recurse -Force content\notes\*, content\orgs\*.md
```

(`content/profile.md` stays — step 5 edits it.)

---

## 2. Point it at a repository

### Make one to point at

`hello-forge` is the toy repository the rest of this guide reads from. It is five files and
two commits — build it next to `my-forge` so the config's `../hello-forge` resolves, or skip
this and point the config at any repository you already have (the transcripts below will then
show your numbers instead of these).

```bash
cd ..
mkdir hello-forge && cd hello-forge
git init -b main

cat > .frznforge.json <<'JSON'
{
  "description": "A tiny repository used to try frznforge out.",
  "tags": ["demo", "javascript"],
  "links": { "homepage": "https://example.com/hello-forge" }
}
JSON
printf 'MIT License\n\nCopyright (c) 2026 Ada Lovelace\n' > LICENSE
printf '# hello-forge\n\nA tiny repository used to try frznforge out.\n' > README.md
mkdir src && printf 'console.log("hello, forge");\n' > src/index.js
git add -A && git commit -m "Initial commit"

printf 'export const VERSION = "0.1.0";\n' > src/version.js
git add -A && git commit -m "Add a version constant"
git tag -a v0.1.0 -m "First release"

cd ../my-forge
```

That is exactly what step 4 shows on the page: 2 commits, 1 branch, 1 annotated tag (so there
is a release), an MIT `LICENSE`, and 100% JavaScript.

### Configure it

Open `frznforge.config.ts` and replace the `repos` array. Two kinds of entry exist: a
**local** path, and a repository **imported** from a forge.

```ts
import { defineConfig } from './src/lib/config/schema';

export default defineConfig({
  site: { title: "Ada's forge" },
  owner: { name: 'Ada Lovelace', handle: 'ada' },
  theme: { palette: 'hearth' },          // 'hearth' (warm) | 'frost' (cool)
  repos: [
    // a repository on your disk — path is absolute, or relative to this file
    { type: 'local', path: '../hello-forge' },

    // a repository on GitHub — mirror-cloned at build time, releases read from the API
    { type: 'github', owner: 'Descent098', repo: 'ezcv', releases: 'provider' },
  ],
});
```

The slug (the URL segment) defaults to the directory or repository name, so those two land at
`/repos/hello-forge/` and `/repos/ezcv/`.

**Only committed content is read.** frznforge runs `git` against the repository's object
database, never against your working tree — uncommitted, staged, stashed and untracked files
never reach the site. Bare repositories work fine.

> Don't want to hand-write the imported entries? `npm run frznforge -- init` walks a whole
> account and writes them for you — see [importing.md](./importing.md).

---

## 3. Ingest

`npm run ingest` turns git into one JSON artifact plus a blob store under `data/`
(git-ignored, regenerated on every build).

```
$ npm run ingest

frznforge ingest → C:\…\my-forge\data
  1 remote source(s) — cache C:\…\my-forge\.frznforge-cache (fetch: auto)
  ▸ hello-forge
  ▸ ezcv
    ✓ hello-forge: 2 commits, 1 branches, 1 tags, 5 files
    ⇄ ezcv (github: cloned)
    ✓ ezcv: 131 commits, 5 branches, 11 tags, 164 files
done: 2 repo(s), 493 blob(s), 14 archive(s), 0 warning(s) in 19845ms
```

The blob and archive counts are whatever your repositories happen to contain — `ezcv` moves,
so yours will not be 493. The summary also grows an `N note(s)` and an `N organization(s)`
segment as soon as you have either; with `content/` cleared in step 1 you have neither yet.

The first run of an imported repo clones a bare mirror into `.frznforge-cache/`; later runs
only fetch it:

```
    ⇄ ezcv (github: fetched)
done: 2 repo(s), 493 blob(s), 14 archive(s), 0 warning(s) in 16443ms
```

**Ingest never fails a build because of a repository.** An empty repo, a missing path, a forge
that is down — each of those is a warning, printed as `⚠ [code] repo: message` and counted in
the site footer. Exit code 1 is reserved for a bad config, an unwritable output directory, or
a missing `git`.

---

## 4. Run it

```
$ npm run dev

 astro  v7.2.4 ready in 2531 ms
┃ Local    http://localhost:4321/
┃ Network  use --host to expose
```

Open <http://localhost:4321/>.

**What you should see.** The profile page: your name and bio, a contribution graph, an
activity log, and cards for your pinned repositories. A docked sidebar with **Overview**,
**Repositories** and a count, and a search box (**Ctrl-K** anywhere opens the command
palette). Press **t** to flip between light and dark.

Click through to `/repos/hello-forge/` and you get a real forge page:

```
ada / hello-forge   A tiny repository used to try frznforge out.
MIT license   default main   1 branch   1 tag   1 contributor

Code   Commits 2   Branches 1   Tags 1   Releases 1   Insights

main ▾    Download ZIP 1.4 KB
Ada Lovelace  Add a version constant · 16c35f0 · 6 months ago   2 commits

Name              Last commit              Updated
src               Add a version constant   6 months ago
.frznforge.json   Initial commit           7 months ago
LICENSE           Initial commit           7 months ago
README.md         Initial commit           7 months ago

README · README.md
  hello-forge
  A tiny repository used to try frznforge out.
  …

About      A tiny repository used to try frznforge out.
Homepage   example.com/hello-forge
Tags       demo  javascript
MIT license · LICENSE   2 commits · last 6 months ago   1 tags · latest v0.1.0
Languages  JavaScript 100%
Contributors  Ada Lovelace  2 commits
```

(Your shas and the relative dates will differ — you just made those commits. Everything else
is derived, so it should match line for line.)

The file browser, commit history, single-commit diffs, branches, tags, releases and the
insights charts are all there. The browser covers the default branch plus the 10 most recently
updated branches and the 25 newest tags; those two caps are configurable and matter a lot for
build size (see step 7).

> **The dev server reads `data/forge.json` once, at startup.** Re-run `npm run ingest` and
> the new pages will 404 until you restart `npm run dev`. This catches everyone once.

### Where did that metadata come from?

`hello-forge` has a `.frznforge.json` committed at its root:

```json
{
  "description": "A tiny repository used to try frznforge out.",
  "tags": ["demo", "javascript"],
  "links": { "homepage": "https://example.com/hello-forge" }
}
```

That file is optional and lives *inside* the repository it describes, so the description
travels with the code. You can override any of it from `frznforge.config.ts` with
`overrides: { … }`. The MIT badge, the language bar and the contributor list are detected —
nobody typed those.

---

## 5. Your profile page

`content/profile.md` is frontmatter plus a markdown body:

```md
---
bio: Writes small programs and long footnotes.
location: London
email: ada@example.com
sites: [https://example.com]
forges:
  github: https://github.com/ada
pinned: [hello-forge]          # repo slugs, max 10, in order
identities: [ada@example.com]  # emails counted as "you" in the contribution graph
---

# Hi, I'm Ada

Everything I publish lives here.
```

Save it and the dev server reloads — no ingest needed, this file is not part of the artifact.

`identities` lists every address you commit under. Leave it out and the contribution graph
counts *all* commits in *all* your repositories — fine for a solo account, misleading the
moment a repository has other contributors.

---

## 6. A first note

Notes are gist-style snippets at `/notes/`. Unlike repositories they are read straight from
disk, so nothing has to be committed — and `content/notes/` is the default location, so there
is nothing to switch on. Write `content/notes/ripgrep-cheatsheet.md`:

```md
---
title: ripgrep cheatsheet
description: The flags I always forget.
date: 2026-03-14
tags: [cli, search]
---

# ripgrep cheatsheet

- `rg -n pattern` — show line numbers
- `rg -g '*.ts' pattern` — only TypeScript files
```

Notes *are* part of the artifact, so re-run ingest and restart the dev server:

```
$ npm run ingest
…
done: 2 repo(s), 1 note(s), 494 blob(s), 14 archive(s), 0 warning(s) in 16182ms
```

`/notes/` now lists it, `/notes/ripgrep-cheatsheet/` renders it with a Preview/Source toggle,
and **Notes** appears in the sidebar. A sub-folder instead of a file makes a multi-file note.
Add a `notes: { … }` block to the config only when you want a different folder, or want to be
warned (`notes-dir-missing`) if that folder disappears — see
[configuration.md](./configuration.md).

---

## 7. Build the real thing

```
$ npm run build      # = npm run ingest && astro build

[build] 3798 page(s) built in 1m 57s
[build] Complete!
```

Everything lands in `dist/` — plain HTML, CSS, a little JavaScript, the raw file endpoints and
the source zips.

That page count is not a typo. A file browser is generated for the default branch, for the
next `ingest.branchTrees` branches (10 by default) and for the newest `ingest.tagTrees` tags
(25 by default), so a repo's file count is multiplied by its browsable refs. Two small
repositories produced 3,798 pages and a 400 MB `dist/`, 161 MB of which was source zips.
[deploying.md](./deploying.md#3-how-big-will-my-site-be) has the formula and the knobs
(`branchTrees`, `tagTrees`, `archives`) that bring it down.

Check the output locally with the production server:

```bash
npm run preview      # http://localhost:4321/
```

Then put `dist/` on a host. [deploying.md](./deploying.md) has a working GitHub Actions
workflow and the trailing-slash rules for Cloudflare Pages, Netlify, nginx and Apache.

---

## Where to go next

| I want to… | Read |
|---|---|
| Know every config key | [configuration.md](./configuration.md) |
| Publish repos from GitHub / GitLab / Gitea / Forgejo | [importing.md](./importing.md) |
| Get it online | [deploying.md](./deploying.md) |
| Move off a hosted forge | [migrating.md](./migrating.md) |

## If something went wrong

| Symptom | Cause |
|---|---|
| `⚠ [repo-not-found]` | The `path` is not a git repository. It is relative to `frznforge.config.ts`, not to your shell |
| `⚠ [repo-empty]` | The repository has no commits. It still gets a page saying so |
| A new page 404s in dev | The dev server is holding the old artifact — restart it after `npm run ingest` |
| An imported repo is missing | Look for `remote-fetch-failed` / `remote-auth-missing` / `remote-rate-limited` in the ingest output |
| `Filename too long` while cloning a mirror | Windows `MAX_PATH`. Run `git config --global core.longpaths true`, or point `ingest.cacheDir` somewhere shallow |
| The site is empty | `repos: []`. Ingest says `(no repos configured — writing an empty artifact)` |
| `[frznforge] content/orgs/<id>.md does not match any organization…` | A markdown file in `content/orgs/` with no matching `organizations` entry in the config. Delete the file, or add the org. Printed by the build, not by ingest, so it has no warning code |
