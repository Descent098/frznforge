# Starting a site

Zero to a built site. Everything here happens on your machine at build time; the site you
publish is a folder of static files that never calls anything.

This guide is about **`frznforge new`**: what it scaffolds and how to keep your content out of
the engine's git history. If you would rather see something on screen first and worry about file
layout later, start with [quick-start.md](./quick-start.md) — it edits the demo site in place and
gets you to a running page in ten minutes, then sends you back here.

You need **Go ≥ 1.24** and **git**. Nothing else.

---

## The short version

```sh
git clone https://github.com/Descent098/frznforge my-site
cd my-site
go build ./cmd/frznforge

rm -rf content frznforge.config.jsonc README.md   # the author's demo site (incl. notes and orgs)
./frznforge new . --force                         # yours in its place
./frznforge build                                 # ingest + render → dist/
./frznforge dev                                   # http://localhost:4321/
```

(PowerShell: `Remove-Item -Recurse -Force content, frznforge.config.jsonc, README.md`.)

The delete is not optional housekeeping. `new` **never overwrites an existing file**, with or
without `--force`: in an untouched clone four of the six (`frznforge.config.jsonc`,
`content/profile.md`, `.gitignore`, `README.md`) already exist and are left byte-for-byte alone,
while `content/notes/welcome.md` and `content/orgs/example-org.md.example` *would* be added —
landing an example note and an example org page in among the demo content you did not remove.
`--dry-run` prints exactly that list before anything is written:

```
$ ./frznforge new . --force --dry-run

Dry run — nothing was written to /home/you/my-site.

Would create:
  + content/notes/welcome.md               an example note (safe to delete)
  + content/orgs/example-org.md.example    an example org page (inert until renamed)
  = frznforge.config.jsonc                 already there, would be left alone
  = content/profile.md                     already there, would be left alone
  = .gitignore                             already there, would be left alone
  = README.md                              already there, would be left alone

Re-run without --dry-run to write these files.
```

Removing the whole `content/` directory — notes and orgs included, which the `rm` above does —
is what makes the scaffold land clean.

Read on for what each generated file is for, and for the arrangement that keeps your content
out of the engine's git history.

---

## 1. `new` — scaffold the files you author

```
frznforge new <dir> [--force] [--dry-run]
```

It writes six files:

| File | What it is |
|---|---|
| `frznforge.config.jsonc` | Site title, your name and handle, palette, **the repositories to ingest**, organizations, notes folder, ingest limits. One commented example of every source type. |
| `content/profile.md` | Your profile page. Frontmatter → links, location, pinned repos; the markdown body → your README. |
| `content/notes/welcome.md` | An example note, explaining the notes folder. Delete it once you have one of your own. |
| `content/orgs/example-org.md.example` | An example organization page. Inert until you rename it to `<org-slug>.md`. |
| `.gitignore` | Ignores `data/`, `dist/`, `.frznforge-cache/`, the `frznforge` binary if you built it in place, and `.env`. |
| `README.md` | Your site's README — the three commands and what to edit. Not frznforge's. |

Then it prints the exact files to edit, in the order they matter — and, when `web/` is not
already sitting in the target directory, a first step telling you to put the engine there.

### Rules it lives by

- **It refuses a directory that already has files in it** unless you pass `--force`:

  ```
  error: /home/you/my-site is not empty. Re-run with --force to add the missing files to it
  (existing files are never overwritten), or pick an empty directory.
  ```

  A directory holding nothing but `.git` counts as empty, so `git init my-site` first is fine.
- **It never overwrites an existing file**, `--force` or not. Files that were already there are
  reported with `=` and left byte-for-byte alone. Running `new` twice is therefore safe, and
  safe again after you have edited everything.
- **`--dry-run` writes nothing**, not even the target directory. It prints the same list a real
  run would create, marking anything that already exists.

```
$ frznforge new my-site --dry-run
Dry run — nothing was written to /home/you/my-site (would be created).

Would create:
  + frznforge.config.jsonc                 site config — start here
  + content/profile.md                     your profile page
  + content/notes/welcome.md               an example note (safe to delete)
  + content/orgs/example-org.md.example    an example org page (inert until renamed)
  + .gitignore                             ignores data/, dist/, the cache, the binary
  + README.md                              your site’s README, not frznforge’s

Re-run without --dry-run to write these files.
```

---

## 2. The engine is the binary plus `web/`

`new` writes the files **you** author. It does not write the engine, and the engine is two
things:

1. **The `frznforge` binary.** `go build ./cmd/frznforge` in a frznforge checkout produces it.
   Put it on your `PATH`, or keep it in the site directory and call it as `./frznforge`. It can
   live anywhere — it takes the project root from where you run it (or from `--root=<dir>`).
2. **`web/`, which has to sit beside your content.** It is the browser half: the stylesheets,
   the web components (`<hf-repo-listing>`, `<hf-command-palette>`), the shared
   listing/format/search modules and the vendored mermaid build. **It is not embedded in the
   binary** — the build copies it out of your project directory into `dist/` verbatim, which is
   the whole no-build guarantee: no transpile, no bundler, no content hashing, the browser gets
   the bytes on disk.

That second one is the trap worth stating twice, because it fails quietly. **A site without
`web/` builds successfully and then has no stylesheet and a dead listing.** Every page still
links `/css/global.css` and `/js/*.js`; those URLs just 404.

`public/` is optional and yours: anything in it is copied into `dist/` verbatim too, which is
where avatars go (see [configuration.md](configuration.md)). The engine's own `public/` holds
only `favicon.ico` and `logo.png` — copy it if you want frznforge's icons, or drop your own two
files in with those names.

Two ways to arrange this:

**Clone, then scaffold into the clone** (the short version above). Simplest, and `git pull`
brings you engine updates. Your content and the engine share one git history.

**Scaffold somewhere else, then copy the engine in.**

```sh
frznforge new ../my-site
cp -r web public ../my-site/
cd ../my-site && frznforge build
```

Your content directory stays yours; updating means copying a newer `web/` over the top and
rebuilding the binary. Two directories, and one of them is optional.

### Why there is no `go install frznforge`

The obvious shape — `go install github.com/Descent098/frznforge/cmd/frznforge@latest` — does not
work, and the reason is one line in `go.mod`: the module is declared as `module frznforge`, not
as a domain-qualified path. The Go tool can only fetch a module whose path is a URL it can
resolve, so there is nothing for `go install` to go and get. Clone and `go build`.

Nothing is published to a registry either — no npm package, no Homebrew formula, no release
binaries. `new` does the honest half: it writes exactly the files a site owner authors, correct
on the first build, and tells you where the engine goes.

---

## 3. What to edit, in order

### `frznforge.config.jsonc`

The file is JSON with comments and trailing commas. Start at the top: `site.title`,
`owner.name`, `owner.handle` (a lowercase slug, rendered as `@handle`).

Then the part that matters — `"repos"`. Until something is in it, the site builds correctly and
lists nothing. Every source type is in the generated file, commented out:

```jsonc
"repos": [
  // { "type": "local",   "path": "../my-project", "slug": "my-project" },
  // { "type": "github",  "owner": "you", "repo": "my-project" },
  // { "type": "gitlab",  "project": "you/my-project" },
  // { "type": "gitea",   "host": "https://gitea.example.com", "owner": "you", "repo": "my-project" },
  // { "type": "forgejo", "host": "https://codeberg.org", "owner": "you", "repo": "my-project" },
],
```

Start with one `local` entry pointing at a repository you already have — it needs no network and
no token, so it is the fastest way to see a real page. Remote sources are mirror-cloned into
`.frznforge-cache/` on the first build; see [importing.md](importing.md) for tokens, private
repositories and offline builds, and [configuration.md](configuration.md) for every key.

`frznforge init` can fill this array in for you by listing an account's repositories over the
provider's API, and it splices the entries in without disturbing the comments around them.

A key the loader does not recognise fails the build rather than being ignored:

```
error: …/frznforge.config.jsonc: unrecognised or mistyped setting: json: unknown field "branchtrees"
```

### `content/profile.md`

The frontmatter is links and metadata (`bio`, `location`, `sites`, `linkedin`, `forges`,
`pinned`, `identities`); the markdown under it is rendered as your README on the profile page.
Every key is optional — delete what you do not want.

Three worth knowing:

- `pinned` takes **repo slugs** (the URL segments from your config), not display names. A slug
  no ingested repo has is reported on stderr and skipped:
  `[frznforge] profile.md pins unknown repo "usefull"`.
- `identities` lists the author emails that count as "you" in the contribution graph. Leave it
  empty to count every commit in every ingested repo.
- The frontmatter parser reads a deliberately small subset of YAML — `key: scalar`,
  `key: [a, b]`, a `- item` list, and one level of indented `label: value` for `forges:` and
  `links:`. Anything fancier is **dropped rather than guessed at**, and dropped silently. If a
  key is not showing up on the page, that is the first thing to check: a partial parse would put
  a chip reading `[a, b]` in front of a visitor, and nobody would ever look at that and see a
  bug.

### `content/notes/`

A file in this folder is a note at `/notes/<name>/`. A **folder** is one multi-file note, with
`index.md` (or the first markdown file) as the body and the other files browsable beside it.
Dates come from frontmatter; a note with no `date` has no date, because falling back to file
modification times would mean two builds of the same content stopped being byte-identical
(`notes.useMtime` turns that trade on if you want it).

Delete `welcome.md` once you have written something.

### `content/orgs/`

Organizations are groupings of repos with their own overview page. An org needs an entry in
`"organizations"` in the config; the markdown file is optional prose, links and pinned repos on
top of it. The filename **is** the slug: `my-org.md` belongs to `{ "slug": "my-org", … }`.

To use the generated example: add the org to your config, rename `example-org.md.example` →
`my-org.md`, rebuild. The `.md.example` extension is why a fresh site does not ship with a stray
organization — the loader only reads `*.md`.

An empty (or absent) `content/orgs/` is fine and prints nothing. A markdown file in it that **no
configured organization claims** is not fine, and does not hide in a log: `/orgs/` renders a
banner naming the file. An orgs file is only ever reached through an organization's slug, so one
typo in the filename would otherwise discard the whole thing — prose, links, pins — with no
symptom at all.

---

## 4. Build it

```sh
frznforge ingest   # read the git repositories → data/forge.json + data/blobs/ + data/archives/
frznforge build    # ingest, then render the static site into dist/
frznforge dev      # serve dist/ on http://localhost:4321 — the last build, nothing rebuilt
```

`frznforge build` runs the ingest and then the render — it is the whole build, and nothing else
has to run first. `--no-ingest` skips the scan and re-renders the artifact you already have,
which is what you want while iterating on content or the config's presentation keys; it refuses
when there is no artifact rather than publishing an empty site over a good one:

```
$ frznforge build --no-ingest

error: --no-ingest needs an artifact to render, and there is none yet.

  missing: data/forge.json

  Run the ingest once first:

    frznforge build       scan, then render
    frznforge ingest      scan only

  After that, --no-ingest re-renders that artifact as often as you like.
```

`frznforge dev` prints a notice saying it rebuilds nothing, then serves `dist/`. Run it before
your first build and it names what is missing and the command that produces it, rather than
serving an empty directory that answers every request with "not found". **There is no watch mode
and no HMR.** The loop is **edit → `frznforge build` → refresh the browser**, whether you
changed a repository, `content/`, or the config — and the render is fast enough that this is not
the compromise it sounds like.

`--port=<n>` picks a different port; `--dir=<path>` serves some other directory (and then skips
the artifact check, since there is no project behind it).

Everything in `dist/` is plain files. Upload the folder anywhere that serves static content.

### First build, sanity check

A scaffolded site with no repositories configured builds **eight files of its own**: the profile
(`/`), the empty repo listing, the notes listing, your example note, the orgs listing, a 404,
the note's raw view, and `search-index.json`.

```
$ frznforge build

frznforge build: scanning first — pass --no-ingest to render the artifact on disk instead.
frznforge ingest → /home/you/my-site/data
  (no repos configured — writing an empty artifact)
done: 0 repo(s), 1 note(s), 1 blob(s), 0 archive(s), 0 warning(s) in 2ms

built 8 files (0.1 MB) in 15ms
```

On top of that come `web/` and `public/`, copied in verbatim. In frznforge's own checkout that
is 120 and 2 files — most of it the vendored mermaid build — so the same site with the engine
beside it reports **130 files**. If the number jumps like that when you copy `web/` in, the
asset half is wired up; if it does not, that is why your pages have no styling.

---

## 5. Keeping builds small

Two knobs decide how many pages you generate, because file-browser pages are emitted **per
browsable ref**:

| Key | Default | Effect |
|---|---|---|
| `ingest.branchTrees` | `10` | How many non-default branches get a browsable file tree. The default branch always has one and never counts against this. `"all"` for every branch. |
| `ingest.tagTrees` | `25` | How many of the newest tags get a browsable tree and a source archive. |

A repo with 500 files costs roughly its file count in tree/blob/raw pages **for every browsable
ref**, so these two are the difference between a build you wait a second for and one you wait a
minute for. Lower them, rebuild, and watch the file count on the build's last line.
[deploying.md §3](./deploying.md#3-how-big-will-my-site-be) has the formula and the rest of the
levers; [configuration.md](configuration.md) has every ingest key.

---

## Troubleshooting

**`... is not empty. Re-run with --force`** — the target directory has files in it. `--force`
adds the missing ones without touching anything that is there; `--dry-run` shows you what that
would mean first.

**`no frznforge.config.jsonc in …`** — the full message is

```
error: no frznforge.config.jsonc in . — run `frznforge config migrate` if you still have a frznforge.config.ts
```

You are in the wrong directory, or you are upgrading from 0.3.0. See
[migrating.md](./migrating.md#part-a--upgrading-frznforge-030--040).

**`unrecognised or mistyped setting: json: unknown field "…"`** — a typo in a config key. The
loader refuses unknown fields on purpose: the alternative is a site that quietly ignores the
setting you thought you changed.

**The build succeeds and every page is unstyled, with a listing that does nothing** — `web/` is
not in the project directory. It is served verbatim rather than embedded in the binary; see
[section 2](#2-the-engine-is-the-binary-plus-web). Confirm by looking for `dist/css/global.css`.

**The site builds but lists no repositories** — `"repos": []` in the config. That is the
default; uncomment one of the five examples. Ingest says so too:
`(no repos configured — writing an empty artifact)`.

**A key from `content/profile.md` (or a note, or an org page) does not appear on the page** —
the frontmatter parser reads a small YAML subset and drops what it cannot read, silently. Check
the indentation, and check that a nested mapping is only one level deep.

**A new page 404s under `frznforge dev`** — `dev` serves the last build. Run `frznforge build`;
`ingest` alone only refreshes the data.
