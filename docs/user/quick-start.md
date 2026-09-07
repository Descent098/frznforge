# Quick start

Ten minutes from nothing to a running site: one git repository on your disk, one imported
from GitHub, a profile page and a note.

You need **Go ≥ 1.24** and **git** on your `PATH`. Nothing else — no Node, no database, no
server, no account anywhere.

```
$ go version
go version go1.24.5 windows/amd64

$ git --version
git version 2.50.1.windows.1
```

(The platform suffix is whatever you are on; only the version numbers matter.)

---

## 1. Get the project

frznforge is the site *and* the generator: you clone it, point it at your repositories, and
commit your own config on top.

```bash
git clone https://github.com/Descent098/frznforge my-forge
cd my-forge
go build ./cmd/frznforge
```

That produces one binary in the directory — `frznforge` on Linux and macOS, `frznforge.exe`
on Windows. Put it on your `PATH` if you like; this guide calls it as `./frznforge`, which
works unchanged in PowerShell and in a Unix shell.

The clone arrives configured as *this project's own* demo site. This guide edits those files
in place, which is the fastest way to see something work. When you are ready to keep your
content out of the engine's git history, or you want clean starting files rather than someone
else's — `frznforge new <dir>` scaffolds them, and
[starting-a-site.md](./starting-a-site.md) explains the layouts that work.

The files you will touch:

| File | What it is |
|---|---|
| `frznforge.config.jsonc` | Which repositories to publish, your name, the palette, ingest limits |
| `content/profile.md` | Your profile page — frontmatter for links, markdown body for prose |
| `content/notes/` | Gist-style notes (optional) |
| `content/orgs/` | Organization pages (optional) |

Everything else is the generator.

### Clear the demo content first

The clone is a working site, so `content/` arrives full of the author's material: **five demo
notes** under `content/notes/` and **one organization page**,
`content/orgs/canadian-coding.md`. Neither is yours, and neither disappears on its own.

The notes are the visible half: they keep publishing at `/notes/` and keep the **Notes 5**
count in the sidebar until you delete them. The org page is the quieter half. Step 2 replaces
the `repos` and `organizations` config with your own, and from that moment no configured
organization claims `canadian-coding.md` — so the file renders nowhere, and `/orgs/` grows a
banner saying so:

```
No organization claims content/orgs/canadian-coding.md — nothing renders it. …
```

Nothing is printed to the terminal about it; the report is on the page, because a build
warning about a file that renders nowhere is a warning nobody reads. Clear both now — the
rest of this guide assumes you did, and step 6 writes a note of your own:

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

Open `frznforge.config.jsonc` and replace what is in it with this. The format is **JSON with
comments**: `//` and `/* … */` are fine, and so is a trailing comma before a `}` or a `]`.
Nothing else JSON refuses is allowed — no unquoted keys, no single quotes.

```jsonc
{
  "site": { "title": "Ada's forge" },

  "owner": {
    "name": "Ada Lovelace",
    "handle": "ada",
    "profile": "./content/profile.md",
  },

  "theme": {
    "palette": "hearth", // "hearth" (warm) | "frost" (cool)
  },

  "repos": [
    // A repository on your disk. The path is absolute, or relative to this file.
    { "type": "local", "path": "../hello-forge" },

    // A repository on GitHub: mirror-cloned at build time, releases read from the API.
    { "type": "github", "owner": "Descent098", "repo": "ezcv", "releases": "provider" },
  ],
}
```

Two kinds of entry exist, and both are here: a **local** path, and a repository **imported**
from a forge. Everything you have dropped — `organizations`, `notes`, `content`, `ingest`,
`listing` — has a default, which is why a config this short builds a whole site. The file the
scaffolder writes carries all of them with their defaults spelled out and commented;
[configuration.md](./configuration.md) is the full reference.

The slug (the URL segment) defaults to the directory or repository name, so those two land at
`/repos/hello-forge/` and `/repos/ezcv/`.

**Only committed content is read.** frznforge runs `git` against the repository's object
database, never against your working tree — uncommitted, staged, stashed and untracked files
never reach the site. Bare repositories work fine.

> Don't want to hand-write the imported entries? `./frznforge init` walks a whole
> account and writes them for you — see [importing.md](./importing.md).

---

## 3. Ingest

`./frznforge ingest` turns git into one JSON artifact plus a blob store under `data/`
(git-ignored, regenerated on every build).

```
$ ./frznforge ingest

frznforge ingest → C:\…\my-forge\data
  1 remote source(s) — cache C:\…\my-forge\.frznforge-cache (fetch: auto)
  ▸ ezcv
  ▸ hello-forge
    ✓ hello-forge: 2 commits, 1 branches, 1 tags, 5 files
    ⇄ ezcv (github: cloned)
    ✓ ezcv: 131 commits, 5 branches, 11 tags, 164 files
done: 2 repo(s), 493 blob(s), 14 archive(s), 0 warning(s) in 16445ms
```

`ezcv` is announced first because a source with nothing cached goes to the head of the queue —
the network budget is spent on the repos that actually need it. Repos are scanned
concurrently, so the `▸` lines and the `✓` lines are in different orders; the artifact is
sorted by slug regardless of either.

The blob and archive counts are whatever your repositories happen to contain — `ezcv` moves,
so yours will not be 493. The summary also grows an `N note(s)` and an `N organization(s)`
segment as soon as you have either; with `content/` cleared in step 1 you have neither yet.

The first run of an imported repo clones a bare mirror into `.frznforge-cache/`. Run it again
straight away and it will not touch the network at all: `ingest.reuse` skips a source fetched
successfully in the last two minutes, and skips the whole scan for a repo whose refs have not
moved, so the line reads `reused` and the run finishes in a fraction of the time.

```
    ⇄ ezcv (github: reused)
done: 2 repo(s), 493 blob(s), 14 archive(s), 0 warning(s) in 316ms
```

Past that window an ordinary run fetches the mirror rather than re-cloning it:

```
    ⇄ ezcv (github: fetched)
done: 2 repo(s), 493 blob(s), 14 archive(s), 0 warning(s) in 1338ms
```

`./frznforge ingest --no-cache` forces the long way round — every fetch made, every cache
ignored for the run:

```
$ ./frznforge ingest --no-cache

  --no-cache: fetching everything; provider/scan caches ignored for this run
frznforge ingest → C:\…\my-forge\data
  1 remote source(s) — cache C:\…\my-forge\.frznforge-cache (fetch: always [--no-cache])
  ▸ hello-forge
  ▸ ezcv
    ✓ hello-forge: 2 commits, 1 branches, 1 tags, 5 files
    ⇄ ezcv (github: fetched)
    ✓ ezcv: 131 commits, 5 branches, 11 tags, 164 files
done: 2 repo(s), 493 blob(s), 14 archive(s), 0 warning(s) in 12067ms
```

**Ingest never fails a build because of a repository.** An empty repo, a missing path, a forge
that is down — each of those is a warning, printed as `⚠ [code] repo: message` and counted in
the site footer. Exit code 1 is reserved for a bad config, an unwritable output directory, a
missing `git`, or an artifact that fails its own validation.

---

## 4. Run it

Ingest produced the *data*; the site itself is rendered by the build. `build` does both halves
in one process, so it is the only command you need here — the `ingest` you just ran is inside
it, and it will skip the network because you ran it a moment ago:

```
$ ./frznforge build

frznforge build: scanning first — pass --no-ingest to render the artifact on disk instead.
frznforge ingest → C:\…\my-forge\data
  1 remote source(s) — cache C:\…\my-forge\.frznforge-cache (fetch: auto)
  ▸ hello-forge
  ▸ ezcv
    ⇄ ezcv (github: reused)
    ✓ hello-forge: 2 commits, 1 branches, 1 tags, 5 files
    ✓ ezcv: 131 commits, 5 branches, 11 tags, 164 files
done: 2 repo(s), 493 blob(s), 14 archive(s), 0 warning(s) in 327ms

[frznforge] profile.md pins unknown repo "frznforge"
built 6705 files (387.6 MB) in 8.837s
```

That one message is the demo profile still pinning the author's repositories; step 5 replaces
the file and it goes away. Now serve what you built:

```
$ ./frznforge dev

frznforge dev — serving dist/ from the most recent `frznforge build`.

  Nothing is rebuilt here. These are the static files already in dist/, rendered
  from data/forge.json as it stood at that build. Editing a template, a style, content/
  or the config changes nothing you see until you build again — no file is watched and
  no page is re-rendered while this runs.

    frznforge build     re-render the artifact you already have → dist/
    frznforge ingest    refresh data/forge.json from the repositories first

serving C:\…\my-forge\dist on http://localhost:4321/
```

Open <http://localhost:4321/>.

`./frznforge dev` is a local viewer, not a live-reloading dev server: **the loop is edit →
`./frznforge build` → refresh the browser.** That is the honest shape of a static forge — the
pages are rendered from an artifact, and re-rendering them is what `build` does. You do not
have to restart `dev` to see a rebuild, though: it reads nothing at startup and serves each
file off disk as it is asked for, so a `build` in another terminal shows up on the next
refresh.

Run it before you have ever built and it says so and stops, rather than failing on a missing
`dist/`:

```
$ ./frznforge dev

error: frznforge dev: there is no built site to serve yet.

  missing: data/forge.json
  missing: dist

  Build it first — that is both halves:

    frznforge ingest    scan the repositories into data/forge.json
    frznforge build     render that artifact into dist/

  Then `frznforge dev` again.
```

`./frznforge dev --port=4400` picks a different port; `--dir=<path>` serves a directory other
than `dist/`, and skips the artifact check entirely, so it works in a directory with no
frznforge config in it at all.

**What you should see.** The profile page: your name and bio, four headline panels
(repositories, commits this year, years of history, top languages), a **Profile README**, a
**Recent activity** log, a repository grid and a contribution graph. A docked sidebar with
**Overview**, **Repositories** and a count, and a search box (**Ctrl-K** anywhere opens the
command palette). Press **T** to flip between light and dark.

Click through to `/repos/hello-forge/` and you get a real forge page:

```
ada / hello-forge   A tiny repository used to try frznforge out.
MIT license   default main   1 branch   1 tag   1 contributor

Code   Commits 2   Branches 1   Tags 1   Releases 1   Insights

main ▾    Download ZIP 1.3 KB
Ada Lovelace  Add a version constant · a5f48eb · 2 minutes ago   2 commits

Name              Last commit              Updated
src               Add a version constant   2 minutes ago
.frznforge.json   Initial commit           2 minutes ago
LICENSE           Initial commit           2 minutes ago
README.md         Initial commit           2 minutes ago

README · README.md
  hello-forge
  A tiny repository used to try frznforge out.

About      A tiny repository used to try frznforge out.
Homepage   example.com/hello-forge
Tags       demo  javascript
MIT license · LICENSE   2 commits · last 2 minutes ago   1 tags · latest v0.1.0 created 2 minutes ago
Languages  JavaScript 100%
Contributors 1   Ada Lovelace  2 commits
```

Two things in there are yours rather than the config's. The shas and the relative dates come
from the commits you made a minute ago. And the name on the commit line and in **Contributors**
is your git identity — `git config user.name` — not `owner.name`; the two are separate people
as far as the build is concerned, which is what step 5's `identities` exists to reconcile.
Everything else is derived, so it should match line for line.

The file browser, commit history, single-commit diffs, branches, tags, releases and the
insights charts are all there. The browser covers the default branch plus the 10 most recently
updated branches and the 25 newest tags; those two caps are configurable and matter a lot for
build size (see step 7).

> **`./frznforge dev` serves the last build, and rebuilds nothing.** Re-run `./frznforge
> ingest` on its own and nothing you see changes — the pages in `dist/` were rendered from the
> old artifact, and `ingest` does not render. `./frznforge build` is the command that makes new
> content appear.

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
travels with the code. You can override any of it from `frznforge.config.jsonc` with
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

Save it and re-run `./frznforge build`. No *ingest* is strictly needed — this file is not part
of the artifact, it is read by the render — so `./frznforge build --no-ingest` is enough, and
touches neither git nor the network. The pinned repo now has its own **Pinned** section, and
the `pins unknown repo` message from step 4 is gone.

**`identities` has to be the address you commit under**, which is `git config user.email` and
usually not the address in `email:` above. Put a stranger's address in it and the graph reads
`0 contributions in the last year` while **Commits this year** — which counts everything in the
repository, not just yours — still says 2. Leave `identities` out altogether and the graph
counts *all* commits in *all* your repositories: fine for a solo account, misleading the moment
a repository has other contributors.

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
- `rg -g '*.js' pattern` — only JavaScript files
```

Notes *are* part of the artifact, so this one needs an ingest before it can be rendered —
which is what `build` does first anyway:

```
$ ./frznforge build
…
done: 2 repo(s), 1 note(s), 494 blob(s), 14 archive(s), 0 warning(s) in 340ms

built 6707 files (388.1 MB) in 11.147s
```

Refresh the browser; the running `./frznforge dev` needs no restart. `/notes/` now lists the
note, `/notes/ripgrep-cheatsheet/` renders it with a Preview/Source/Raw toggle, and **Notes 1**
appears in the sidebar. A sub-folder instead of a file makes a multi-file note. Add a
`notes: { … }` block to the config only when you want a different folder, or want to be warned
(`notes-dir-missing`) if that folder disappears — see [configuration.md](./configuration.md).

---

## 7. Build the real thing

```
$ ./frznforge build

frznforge build: scanning first — pass --no-ingest to render the artifact on disk instead.
frznforge ingest → C:\…\my-forge\data
  1 remote source(s) — cache C:\…\my-forge\.frznforge-cache (fetch: auto)
  ▸ hello-forge
  ▸ ezcv
    ⇄ ezcv (github: reused)
    ✓ hello-forge: 2 commits, 1 branches, 1 tags, 5 files
    ✓ ezcv: 131 commits, 5 branches, 11 tags, 164 files
done: 2 repo(s), 1 note(s), 494 blob(s), 14 archive(s), 0 warning(s) in 340ms

built 6707 files (388.1 MB) in 11.073s
```

Everything lands in `dist/` — plain HTML, CSS, a little JavaScript, the raw file endpoints and
the source zips.

That file count is not a typo. Of those 6,707 files, **3,812 are HTML pages**, and a file
browser is generated for the default branch, for the next `ingest.branchTrees` branches (10 by
default) and for the newest `ingest.tagTrees` tags (25 by default) — so a repo's file count is
multiplied by its browsable refs. Two small repositories produced 388 MB, and 162 MB of that is
14 source zips. [deploying.md](./deploying.md#3-how-big-will-my-site-be) has the formula and the
knobs (`branchTrees`, `tagTrees`, `archives`) that bring it down.

Check the output locally — this is the same thing step 4 did:

```bash
./frznforge dev          # http://localhost:4321/  (a notice, then dist/ served)
./frznforge dev --dir=<path>   # serve some other directory, skipping the artifact check
```

Then put `dist/` on a host. [deploying.md](./deploying.md) has a working GitHub Actions
workflow and the trailing-slash rules for Cloudflare Pages, Netlify, nginx and Apache.

---

## Where to go next

| I want to… | Read |
|---|---|
| Know every config key | [configuration.md](./configuration.md) |
| Keep my content out of the engine's git history | [starting-a-site.md](./starting-a-site.md) |
| Publish repos from GitHub / GitLab / Gitea / Forgejo | [importing.md](./importing.md) |
| Get it online | [deploying.md](./deploying.md) |
| Move off a hosted forge | [migrating.md](./migrating.md) |

## If something went wrong

| Symptom | Cause |
|---|---|
| `⚠ [repo-not-found] … is not a git repository` | The `path` is not a git repository. It is relative to `frznforge.config.jsonc`, not to your shell |
| `⚠ [repo-empty] … repository has no commits on any branch` | The repository has no commits. It still gets a page saying so |
| A new page 404s in `./frznforge dev` | `dev` serves the last build. Run `./frznforge build`; `ingest` alone only refreshes the data |
| `error: frznforge dev: there is no built site to serve yet` | You have not built. Run `./frznforge build` first |
| An imported repo is missing | Look for `remote-fetch-failed` / `remote-auth-missing` / `remote-rate-limited` in the ingest output |
| `git clone --mirror failed … Filename too long` | Windows `MAX_PATH`. Run `git config --global core.longpaths true`, or point `ingest.cacheDir` somewhere shallow |
| `error: unknown flag: --` | There is no `--` separator; the flags go straight on the command (`./frznforge ingest --no-cache`) |
| The site is empty | `repos: []`. Ingest says `(no repos configured — writing an empty artifact)` |
| `/orgs/` says "No organization claims content/orgs/&lt;id&gt;.md" | A markdown file in `content/orgs/` with no matching `organizations` entry in the config. Delete the file, or add the org. It is reported on the page rather than in the terminal, and has no warning code |
