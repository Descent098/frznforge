# Starting a site

Zero to a built site. Everything here happens on your machine at build time; the site you
publish is a folder of static files that never calls anything.

This guide is about **`frznforge -- new`**: what it scaffolds and how to keep your content out
of the engine's git history. If you would rather see something on screen first and worry about
file layout later, start with [quick-start.md](./quick-start.md) — it edits the demo site in
place and gets you to a running page in ten minutes, then sends you back here.

---

## The short version

```sh
git clone https://github.com/Descent098/frznforge my-site
cd my-site
npm install

rm -rf content frznforge.config.ts README.md   # the author's demo site (incl. content/notes, content/orgs)
npm run frznforge -- new . --force             # yours in its place
npm run build                                  # ingest + astro build → dist/
```

(PowerShell: `Remove-Item -Recurse -Force content, frznforge.config.ts, README.md`.)

The delete is not optional housekeeping. `new` **never overwrites an existing file**, with or
without `--force`: in an untouched clone four of the six (`frznforge.config.ts`,
`content/profile.md`, `.gitignore`, `README.md`) already exist and are left byte-for-byte
alone, while `content/notes/welcome.md` and `content/orgs/example-org.md.example` *would* be
added — landing an example note and an example org page in among the demo content you did not
remove. `--dry-run` prints exactly that list before anything is written:

```
$ npm run frznforge -- new . --force --dry-run

Would create:
  + content/notes/welcome.md               an example note (safe to delete)
  + content/orgs/example-org.md.example    an example org page (inert until renamed)
  = frznforge.config.ts                    already there, would be left alone
  = content/profile.md                     already there, would be left alone
  = .gitignore                             already there, would be left alone
  = README.md                              already there, would be left alone
```

Removing the whole `content/` directory — notes and orgs included, which the `rm` above does —
is what makes the scaffold land clean.

Read on for what each generated file is for, and for the arrangement that keeps your content
out of the engine's git history.

---

## 1. `new` — scaffold the files you author

```
npm run frznforge -- new <dir> [--force] [--dry-run]
```

It writes six files:

| File | What it is |
|---|---|
| `frznforge.config.ts` | Site title, your name and handle, palette, **the repositories to ingest**, organizations, notes folder, ingest limits. One commented example of every source type. |
| `content/profile.md` | Your profile page. Frontmatter → links, location, pinned repos; the markdown body → your README. |
| `content/notes/welcome.md` | An example note, explaining the notes folder. Delete it once you have one of your own. |
| `content/orgs/example-org.md.example` | An example organization page. Inert until you rename it to `<org-slug>.md`. |
| `.gitignore` | Ignores `data/`, `dist/`, `.frznforge-cache/`, `node_modules/`, `.astro/` and `.env`. |
| `README.md` | Your site's README — the three commands and what to edit. Not frznforge's. |

Then it prints the exact files to edit, in the order they matter.

### Rules it lives by

- **It refuses a directory that already has files in it** unless you pass `--force`. A
  directory holding nothing but `.git` counts as empty, so `git init my-site` first is fine.
- **It never overwrites an existing file**, `--force` or not. Files that were already there
  are reported with `=` and left byte-for-byte alone. Running `new` twice is therefore safe,
  and safe again after you have edited everything.
- **`--dry-run` writes nothing**, not even the target directory. It prints the same list a
  real run would create, marking anything that already exists.

```
$ npm run frznforge -- new my-site --dry-run
Dry run — nothing was written to /home/you/my-site (would be created).

Would create:
  + frznforge.config.ts                    site config — start here
  + content/profile.md                     your profile page
  + content/notes/welcome.md               an example note (safe to delete)
  + content/orgs/example-org.md.example    an example org page (inert until renamed)
  + .gitignore                             ignores data/, dist/, the cache, node_modules
  + README.md                              your site’s README, not frznforge’s

Re-run without --dry-run to write these files.
```

---

## 2. The engine has to be in the same directory

`new` writes the files **you** author. It does not write the generator: `src/`, `scripts/`,
`public/`, `astro.config.mjs`, `svelte.config.js`, `tsconfig.json` and `package.json` all come
from a frznforge checkout and belong in the same directory as your `frznforge.config.ts`.

That is not an accident of packaging, it is how the config is loaded: `frznforge.config.ts`
does `import { defineConfig } from './src/lib/config/schema'`, and the loader resolves the
project root from the engine's own location. Config and engine live side by side.

Two ways to arrange that:

**Clone, then scaffold into the clone** (the short version above). Simplest, and `git pull`
brings you engine updates. Your content and the engine share one git history.

**Scaffold somewhere else, then copy the engine in.**

```sh
npm run frznforge -- new ../my-site
cp -r src scripts public package.json astro.config.mjs svelte.config.js tsconfig.json ../my-site/
cd ../my-site && npm install
```

Your content directory stays yours; updating means copying a newer engine over the top.

### Why there is no `npm create frznforge`

`npm create frznforge` / `npx create-frznforge` is the shape this command wants to be, and it
is not available: **nothing is published to a registry**. Making it real needs three things,
none of which are code you are missing —

1. frznforge published to npm as a package the site can depend on, with the engine behind a
   real entry point (`frznforge/config` for `defineConfig`, a `frznforge build` bin) instead
   of a relative `./src/...` import;
2. a second published package, `create-frznforge`, whose `bin` is this scaffold plus an
   `npm install frznforge` step;
3. a release process that keeps the two in step with the artifact `schemaVersion`.

Until then, `new` does the honest half: it writes exactly the files that would be scaffolded,
correct on the first build, and tells you where the engine goes. If you later switch to a
published package, the generated files carry over unchanged apart from that one import line.

---

## 3. What to edit, in order

### `frznforge.config.ts`

Start at the top: `site.title`, `owner.name`, `owner.handle` (a lowercase slug, rendered as
`@handle`).

Then the part that matters — `repos`. Until something is in it, the site builds correctly and
lists nothing. Every source type is in the generated file, commented out:

```ts
repos: [
  { type: 'local',   path: '../my-project' },
  { type: 'github',  owner: 'you', repo: 'my-project' },
  { type: 'gitlab',  project: 'you/my-project' },
  { type: 'gitea',   host: 'https://gitea.example.com', owner: 'you', repo: 'my-project' },
  { type: 'forgejo', host: 'https://codeberg.org', owner: 'you', repo: 'my-project' },
],
```

Start with one `local` entry pointing at a repository you already have — it needs no network
and no token, so it is the fastest way to see a real page. Remote sources are mirror-cloned
into `.frznforge-cache/` on the first build; see
[importing.md](importing.md) for tokens, private repositories and offline builds, and
[configuration.md](configuration.md) for every key.

`npm run frznforge -- init` can fill this array in for you by listing an account's
repositories over the provider's API.

### `content/profile.md`

The frontmatter is links and metadata (`bio`, `location`, `sites`, `linkedin`, `forges`,
`pinned`, `identities`); the markdown under it is rendered as your README on the profile
page. Every key is optional — delete what you do not want. URLs must be real URLs and `email`
must be a real address, or the build tells you which one is not.

Two worth knowing:

- `pinned` takes **repo slugs** (the URL segments from your config), not display names.
- `identities` lists the author emails that count as "you" in the contribution graph. Leave
  it empty to count every commit in every ingested repo.

### `content/notes/`

A file in this folder is a note at `/notes/<name>/`. A **folder** is one multi-file note, with
`index.md` (or the first markdown file) as the body and the other files browsable beside it.
Dates come from frontmatter; a note with no `date` has no date, because falling back to file
modification times would mean two builds of the same content stopped being byte-identical
(`notes.useMtime` turns that trade on if you want it).

Delete `welcome.md` once you have written something.

### `content/orgs/`

Organizations are groupings of repos with their own overview page. An org needs an entry in
`organizations` in the config; the markdown file is optional prose, links and pinned repos on
top of it. The filename **is** the slug: `my-org.md` belongs to `{ slug: 'my-org', … }`.

To use the generated example: add the org to your config, rename
`example-org.md.example` → `my-org.md`, rebuild.

> Until that folder holds at least one `.md`, your build prints two harmless lines —
> `[glob-loader] No files found matching "*.md" in directory "content/orgs"` and
> `The collection "orgs" does not exist or is empty`. Nothing is wrong: there are no
> organization pages yet. They go away the moment you add one, or you can ignore them.

---

## 4. Build it

```sh
npm run ingest   # read the git repositories → data/forge.json + data/blobs/ + data/archives/
npm run dev      # local preview on http://localhost:4321, using the last ingest
npm run build    # ingest, then build the static site into dist/
```

`npm run build` is just `npm run ingest && astro build`. Re-run `ingest` after changing which
repositories you list or after committing to one of them; `dev` picks up edits to `content/`
without it.

Everything in `dist/` is plain files. Upload the folder anywhere that serves static content.

### First build, sanity check

A scaffolded site with no repositories configured builds **six pages** — the profile (`/`),
the empty repo listing, the notes listing, your example note, the orgs listing and a 404 —
plus the note's raw view and `search-index.json`. If you get that, the pipeline works and
everything from here is content.

---

## 5. Keeping builds small

Two knobs decide how many pages you generate, because file-browser pages are emitted **per
browsable ref**:

| Key | Default | Effect |
|---|---|---|
| `ingest.branchTrees` | `10` | How many non-default branches get a browsable file tree. The default branch always has one and never counts against this. |
| `ingest.tagTrees` | `25` | How many of the newest tags get a browsable tree and a source archive. |

A repo with 500 files costs roughly its file count in tree/blob/raw pages **for every
browsable ref**, so these two are the difference between a build you wait a minute for and one
you wait five minutes for. Lower them, rebuild, and watch the page count Astro prints at the
end. [configuration.md](configuration.md) has the rest of the ingest keys.

---

## Troubleshooting

**`... is not empty. Re-run with --force`** — the target directory has files in it. `--force`
adds the missing ones without touching anything that is there; `--dry-run` shows you what that
would mean first.

**`Cannot find module '…/src/lib/config/index' imported from …/scripts/ingest.ts`** — the
engine is not in the same directory as `frznforge.config.ts`. See
[section 2](#2-the-engine-has-to-be-in-the-same-directory).

**`[WARN] Missing pages directory: src/pages`, then `0 page(s) built`** — the same cause, and
the nastier shape of it: with `src/` missing (or only half-copied) `astro build` does not
fail, it cheerfully builds nothing and exits 0. If a build "succeeds" but `dist/` has no
pages in it, check that `src/`, `scripts/`, `public/` and `astro.config.mjs` all came across.

**The site builds but lists no repositories** — `repos: []` in the config. That is the default;
uncomment one of the examples.

**Invalid URL / invalid email during the build** — a frontmatter value in `content/profile.md`
that is a placeholder rather than a real value. Delete the key or fill it in; the error names
the file and the field.
