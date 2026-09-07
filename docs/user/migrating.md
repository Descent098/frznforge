# Migrating

Two different migrations live here.

- **[Part A — upgrading frznforge 0.3.0 → 0.4.0](#part-a--upgrading-frznforge-030--040).** The
  engine changed language. Read this if you already have a frznforge site.
- **[Part B — coming from GitHub, GitLab, Gitea or Forgejo](#part-b--coming-from-github-gitlab-gitea-or-forgejo).**
  What a hosted forge gives you that frznforge does not, and what to do about each gap.

---

# Part A — upgrading frznforge 0.3.0 → 0.4.0

0.4.0 replaced the engine: an Astro/TypeScript static site generator became a single Go binary.
About 24,000 lines of TypeScript, Astro and Svelte are gone, along with every npm dependency but
Playwright's.

**Your data survives all of it.** The artifact schema stayed at **v8**, deliberately, for the
whole version — that is what made "the Go ingest is correct" a byte-for-byte comparison against
the TypeScript one rather than a judgement call, and the side effect for you is that an existing
`data/` directory keeps working. So does your content, your `.frznforge.json` files, your
profile frontmatter, and every URL the site emits.

Five things change. In the order you will hit them:

## 1. Convert the config: `frznforge config migrate`

`frznforge.config.ts` was TypeScript because the old engine could execute it. A Go binary
cannot, so the format is now **JSONC** — JSON with comments and trailing commas.

That file is roughly 60% comments, and those comments are the configuration's documentation, so
the converter is built around keeping them: it walks the source token by token and re-emits
every byte that is not JavaScript-specific *verbatim*, comments and blank lines and indentation
included.

Run it from the directory holding the config:

```bash
frznforge config migrate
```

```
wrote ./frznforge.config.jsonc — 24 lines, 3 of them carrying comments (3 in ./frznforge.config.ts)
loads cleanly — 2 repo(s), 0 organization(s), 0 contributor(s), palette "hearth"
```

The two comment counts are the headline: a conversion that quietly halved your documentation
should be visible without opening the file. Then it parses what it just wrote with the loader
that will read it from now on — the file is not a migration until that loader accepts it.

What actually changes in the text:

| Before | After |
|---|---|
| `import { defineConfig } …` / `export default defineConfig({` / `});` | dropped — the bare object is left, at the indentation it already had |
| `palette: 'hearth'` | `"palette": "hearth"` — keys quoted, strings re-quoted with JSON escaping |
| `maxBlobBytes: 512 * 1024, // 512 kB` | `"maxBlobBytes": 524288, // 512 kB (512 * 1024)` |
| `// a repository on your disk` | unchanged, in place |

Expressions fold to literals, and the original is recorded in a trailing comment where the
result is not self-evident. **Anything the converter cannot fold — a function call, a variable,
a template placeholder, a spread — stops the run and names the line.** A migration that guesses
is worse than one that stops: a silently wrong `maxBlobBytes` is a truncated site nobody notices
for a month.

Two more things worth knowing:

- It **never overwrites** an existing `frznforge.config.jsonc` without `--force`, and it never
  deletes `frznforge.config.ts`. Keep the old file around until you have compared a build; then
  delete it yourself.
- If the converted file does not load, it says so and leaves it on disk for you to fix rather
  than throwing the conversion away.

From here on, [configuration.md](./configuration.md) is the reference — every key, every
default, every validation message.

## 2. Node is no longer required to build. Go and git are.

```bash
go build ./cmd/frznforge
```

That is the whole toolchain: **Go ≥ 1.24 and git**. No `npm install`, no lockfile, no
`node_modules`, no bundler, no transpiler, no minifier. The binary has two dependencies —
goldmark for markdown and chroma for highlighting, both pure Go — and both are compiled in.

Node is still needed for exactly one thing: running the Playwright browser suite
(`npm run test:e2e`). If you do not run the tests, you do not need Node at all.

You can drop from your site's repository: `node_modules/`, `package.json`,
`package-lock.json`, `astro.config.ts`, `svelte.config.js`, `tsconfig.json`, `vitest.config.ts`
and `.nvmrc`. Update your CI: `actions/setup-node` + `npm ci` becomes `actions/setup-go` +
`go build`, and [deploying.md](./deploying.md#5-github-pages) has the whole workflow.

## 3. `npm run <x>` becomes `frznforge <x>`

| 0.3.0 | 0.4.0 |
|---|---|
| `npm install` | `go build ./cmd/frznforge` |
| `npm run ingest` | `frznforge ingest` |
| `npm run build` | `frznforge build` |
| `npm run dev` | `frznforge dev` |
| `npm run preview` | `frznforge dev` — one server now, and it is the one the tests use |
| `npm run astro dev` | **gone.** There is no watch mode and no HMR; the render is fast enough that `frznforge build` *is* the loop |
| `npm run frznforge -- init` | `frznforge init` |
| `npm run frznforge -- init --web` | `frznforge init --web` |
| `npm run frznforge -- new <dir>` | `frznforge new <dir>` |
| `npm run ingest -- --no-cache` | `frznforge ingest --no-cache` |
| `npm run build -- --no-ingest` | `frznforge build --no-ingest` |
| `npm run build -- --backfill-metadata` | `frznforge build --backfill-metadata` |
| `npm test` | `go test ./internal/... ./cmd/...` |
| `npm run check` | **gone.** TypeScript was removed rather than upgraded, so there is nothing to type-check |
| `npm run test:e2e` | `npm run test:e2e` — unchanged; Playwright is the last thing that needs Node |
| `npm run measure` | **gone.** `frznforge build`'s last line reports the file count and size |

**The `--` is gone with npm.** `npm run ingest -- --no-cache` needed it to get past npm's own
argument parsing; `frznforge ingest --no-cache` does not.

Two new commands have no 0.3.0 equivalent:

- `frznforge verify [<forge.json>]` reads an artifact, validates it, re-serializes it and
  compares byte for byte with the file on disk.
- `frzndebugger` is a second binary (`go build ./cmd/frzndebugger`) that reads the run log and
  the timings file every build leaves behind — a terminal explorer by default, `--web` for a
  browser, `--plain` for a script. It leads with steps that started and never finished, which is
  the answer when a run stops.

And every command now takes `--log=<error|warn|info|debug>` (or `FRZNFORGE_LOG`), which writes
to stderr while progress stays on stdout:

```bash
frznforge build --log=debug 2> build.log
```

## 4. `data/` and `.frznforge-cache/` are reused as they are

The artifact is **schema v8, unchanged by 0.4.0**. `frznforge build --no-ingest` will render the
`data/forge.json` your 0.3.0 build wrote, from the same blobs and the same archives, without
touching git or the network. That is the cheapest way to see the new engine on your own content:

```bash
frznforge build --no-ingest
frznforge dev
```

`.frznforge-cache/` is reused too, with two caveats and one deletion:

- **The mirror directory names changed.** 0.3.0 nested them per provider and host and appended a
  hash (`github/api.github.com/descent098/ezcv-1f4a3b02.git`); 0.4.0 uses one flat directory
  per source, `<cacheDir>/mirrors/<type>-<host>-<owner>-<repo>-<digest>`. Your old mirrors are not found
  under the new names, so the first 0.4.0 ingest **re-clones every imported repository**. That
  is slow once and correct afterwards. The old nested directories are dead weight; delete them
  (or delete the whole cache and let it rebuild — everything in it is disposable by design).
- **Scan-cache entries do not cross implementations.** `<cacheDir>/scan/` is keyed by a digest
  over the scan's inputs, and Go and JavaScript spell a struct's fields differently, so a 0.3.0
  entry is simply never a hit. It costs one un-skipped scan per repo and then rewrites itself.
  A cache miss costs time, never correctness.
- **`<cacheDir>/highlight/` is dead. Delete it.** 0.2.0 memoised syntax highlighting there
  because Shiki was 84% of the render. 0.4.0 ships **no highlight memo at all** — nothing writes
  that directory and nothing reads it — because the measurement said not to: a completely cold
  Go render of this project's own site is 1.84 s against the 6.57 s the TypeScript build managed
  *warm*. On a 73-repository corpus, that directory was megabytes of gzipped HTML fragments you
  can now reclaim.

```bash
rm -rf .frznforge-cache/highlight
```

```powershell
Remove-Item -Recurse -Force .frznforge-cache\highlight
```

`data/frznforge.log` and `data/frznforge-timings.jsonl` are new and will appear beside your
artifact after the first run. They are diagnostics, never published, and already covered by the
`/data/` line in your `.gitignore`.

## 5. Code blocks change colour, and `dist/` changes shape

Syntax highlighting moved from **Shiki to chroma**, because Shiki is a JavaScript library and
there is no JavaScript left in the build path. The colours are not a byte-for-byte imitation of
Shiki's, and that was a deliberate call rather than an accident:

- Shiki shipped two whole themes as CSS custom properties **on every span**, so every byte of
  every code file carried its own palette inline. chroma emits **token classes**, and the
  colours for those classes are written once into `web/css/repo.css` under the same
  `[data-theme]` attribute the rest of the site already uses.
- That makes the light/dark switch on code the same mechanism as everything else on the page,
  puts every colour in one file you can edit, and makes the HTML smaller.

Everything structural around the colours is unchanged, because it is load-bearing: one
`<span class="line" id="Ln">` per line, so `#L12` anchors still work with pure CSS `:target`;
the same gutter numbering; the same line counts. Only the palette moved.

One highlighting **bug** was fixed on the way across: `.cfg` and `.conf` files rendered with no
colouring at all, because ingest labels them `INI` and the old language table was keyed `Ini`.
The Go map is checked in both directions by a test, so a language name ingest can emit that the
map does not cover is now a build failure rather than an uncoloured file.

`dist/` also changes shape. `_astro/` is gone — there is no bundler, so there are no hashed
bundles. In its place the build copies `web/` in verbatim:

| 0.3.0 | 0.4.0 |
|---|---|
| `dist/_astro/*.css`, `dist/_astro/*.js` (content-hashed) | `dist/css/*.css`, `dist/js/*.js`, `dist/vendor/mermaid/` (plain names) |

If your host has a cache rule pinned to `/_astro/` — a `Cache-Control: immutable` block in nginx
is the common one — **it now matches nothing, and worse, the paths it should be protecting are
no longer content-hashed**, so `immutable` is the wrong header for them. See
[deploying.md §7](./deploying.md#7-any-static-host) for the replacement.

## 6. If you assembled your site by copying the engine

`web/` is part of the engine and it is **not** embedded in the binary: the build copies it out of
your project directory into `dist/`, so it has to be sitting beside your content. A site that
does not carry it builds without complaint and then serves pages with no stylesheet and a dead
listing.

So the copy list changed. Out: `src/`, `scripts/`, `astro.config.ts`, `svelte.config.js`,
`tsconfig.json`, `package.json`. In: the `frznforge` binary (on your `PATH`, or in the
directory) and `web/`. [starting-a-site.md](./starting-a-site.md) has the current recipe.

## What did not change

Worth stating explicitly, because a rewrite this size invites the assumption that everything
moved:

- **Every URL.** The route set is the same, at the same paths, including raw endpoints and
  archives. Nothing you have linked to breaks.
- **The artifact.** Schema v8, same bytes for the same repositories at the same commits.
- **`.frznforge.json`, profile frontmatter, note frontmatter, org frontmatter.** Same keys.
- **Every config key and default**, apart from the format they are written in and the loss of
  expressions. The Go loader was checked field for field against the zod schema it replaced.
- **The markup.** The Playwright suite that judged the old engine is the acceptance bar for the
  whole rewrite, and it passes against the new one. Two specs were edited, and only where they
  named the *previous highlighter's own CSS class* rather than anything the site guarantees;
  they now match the containers the site owns, and they pass against both engines.

Two visible differences besides the code colours, both fixes:

- **File tables sort differently.** The old renderer ordered names with `localeCompare`, which
  depends on the build machine's ICU data — so two builds of the same artifact on two machines
  could emit different HTML. The Go renderer orders by code point, which is stable and matches
  git's own tree order. In practice `config.go` now sorts before `config_test.go` instead of
  after it.
- **The command palette no longer offers file results that 404.** The search index used to list
  every blob path including ones holding `#` or `%`, which get no page because no static URL can
  round-trip them.

---

# Part B — coming from GitHub, GitLab, Gitea or Forgejo

frznforge is not a forge replacement. It is a **read-only showcase** built from git: a browsable,
cloneable, always-up public face for work that lives somewhere else — a private Forgejo, a NAS,
a folder of repositories on your laptop, or a GitHub account you would rather not send people to.

Read this before you migrate, because the honest answer to "does it carry over my X" is often
no, and that is a design decision rather than a missing feature.

---

## 1. What comes over, and what does not

### Carried over

| From the forge | Where it lands |
|---|---|
| Full commit history, all branches, all tags | Commit list, per-branch history, single-commit pages with per-file +/− stats |
| The file tree, at every browsable ref | File browser with build-time syntax highlighting, raw endpoints, source zips |
| README | Rendered on the repo overview |
| LICENSE | Detected (MIT, Apache-2.0, GPL, BSD, MPL, ISC, Unlicense, CC0, 0BSD) and shown as a badge |
| Repository description | The blurb under the repo name and in the listing |
| Topics / labels | Repository tags, which the listing can filter on |
| Homepage URL | The **Homepage** link in the About panel |
| Issue-tracker URL | An **Issues** link that points back at the forge |
| Web URL | The **Open on \<host\>** button, and the clone URL shown in the Code panel |
| Template flag | A template banner on the repo page |
| Releases | The releases page — see [§2](#2-how-releases-map) |
| Authors and committers | Contributor list, per-repo and aggregated on the profile |
| Default branch | The ref the repo opens on |

Languages, per-path last-commit info, the contribution graph and the activity log are all
*derived* from that git data — nothing on the forge produces them.

### Not carried over

| Thing | Why, and what to do instead |
|---|---|
| **Issues** | There is no database and no writable anything. Keep `links.issues` pointing at the forge, so every repo page has a one-click route to where the conversation actually happens |
| **Pull / merge requests** | Same. The merge commits are in the history; the review threads are not |
| **Wikis** | A wiki is a separate git repository — publish it as its own repo entry, or move the pages into `content/notes/` |
| **Stars, forks, watchers** | Deliberately absent. There are no counters anywhere on the site |
| **CI / Actions / pipelines** | No runners, no status badges. The workflow *files* are browsable like any other source |
| **Discussions, projects, milestones** | No equivalent |
| **Packages / container registries** | No equivalent. Release **assets** are linked, but they are served from the forge, not rehosted |
| **Gists / snippets** | The closest thing is `content/notes/` — a folder of files on disk, published at `/notes/`. They are not imported; you copy them across |
| **Git hosting itself** | frznforge serves HTML and zips, never the git protocol. `git clone https://your-forge-site/…` will not work; see [§5](#5-people-still-need-somewhere-to-clone-from) |
| **User accounts, permissions, protected branches, webhooks** | Nothing to migrate — the output is files |
| **Server-side code search** | The Ctrl-K palette runs in the browser over an index of repositories, default-branch file **paths**, notes and orgs. File *contents* are not searchable |
| **Git LFS content** | A mirror clone does not fetch LFS objects, so an LFS-tracked file shows its pointer text, not the asset. Keep large binaries out, or link them as release assets |
| **Submodules** | Listed in the tree as submodule entries; they get no page and are not cloned |

---

## 2. How releases map

Each repository picks one of two sources with `"releases"` in its config entry — `"provider"`
(the default for imported repos) or `"tags"` (the default for local ones).

| | `"releases": "provider"` | `"releases": "tags"` |
|---|---|---|
| Source | The forge's release objects, over the API | Annotated git tags in the mirror |
| Title | The release name | The tag name |
| Body | The release notes, rendered as markdown | The tag message, rendered as markdown |
| Pre-release flag | Yes (except GitLab) | Always false |
| Date | Publication date | Tag date |
| Assets | Listed with name and size, linked to the forge | None — a tag has no attachments |
| Needs network | Yes, one API call per repo | No |

Two things to know:

- **Assets are linked, not rehosted.** A release asset URL points back at the forge. If the
  repository disappears from there, those links break; the source zips frznforge generates
  itself (`archive/<ref>.zip`) do not.
- **Falling back is automatic.** A repo set to `"provider"` whose provider reports no releases
  renders its annotated tags instead, so a build that could not reach the API still produces a
  releases page.

Per-provider quirks (the full list is in [importing.md](./importing.md)):

- **GitHub** — everything maps cleanly.
- **GitLab** — its API reports no asset size and no content type, so those show as `0` and
  unknown, and there is no pre-release flag (`upcoming_release` is derived from the clock,
  which would make your artifact change by itself, so it is ignored).
- **Gitea / Forgejo** — same REST API, both fully supported; Forgejo's license field is often
  empty, in which case the LICENSE file in the clone is sniffed instead.

**Lightweight tags produce nothing.** The tag-mode releases page is built from *annotated*
tags only, because a lightweight tag has no message, no date of its own and no author. They
still appear on the tags page and in the ref switcher.

---

## 3. Private repositories

A private repository is imported the same way as a public one — it just needs a token in the
environment (`FRZNFORGE_GITHUB_TOKEN`, `FRZNFORGE_GITLAB_TOKEN`, …). The token is used both
for the API and for the mirror clone.

**Then it is on your public website.** frznforge has no concept of visibility: everything in
`repos` is published, in full, including every branch and the whole history. There is no
per-repo "private" switch, because the output is a folder of static files and static files
cannot check who is asking.

So:

- Import a private repository only when you have decided to make it public.
- Without a token it is invisible and you get `⚠ [remote-auth-missing]`, naming the variables
  it checked. That is the safe default, not a failure.
- `frznforge init` lists private repositories when a token is set, and marks them with a
  `private` badge. `--select=all-np` (or the **Hide private** toggle in `--web`) excludes them.
- A published site cannot be un-published from the config alone. Remove the entry, rebuild,
  **and** redeploy — the old pages sit on the host until something overwrites them. `rsync
  --delete`, or a host that replaces the whole deployment, matters here.

Secrets in *history* are the same problem they always were: the entire history is published,
so a key committed three years ago and reverted is still browsable at its commit page.

---

## 4. URL mapping

If you are replacing links to a forge, the shapes line up closely:

| Forge | frznforge |
|---|---|
| `/<owner>/<repo>` | `/repos/<slug>/` |
| `/<owner>/<repo>/tree/<ref>/<dir>` | `/repos/<slug>/tree/<ref>/<dir>/` |
| `/<owner>/<repo>/blob/<ref>/<path>` | `/repos/<slug>/blob/<ref>/<path>/` |
| `/<owner>/<repo>/raw/<ref>/<path>` | `/repos/<slug>/raw/<ref>/<path>` |
| `/<owner>/<repo>/commits/<ref>` | `/repos/<slug>/commits/<ref>/` (`page/2/`, …) |
| `/<owner>/<repo>/commit/<sha>` | `/repos/<slug>/commit/<sha>/` |
| `/<owner>/<repo>/branches`, `/tags`, `/releases` | `/repos/<slug>/branches/`, `/tags/`, `/releases/` |
| `/<owner>/<repo>/releases/tag/<tag>` | `/repos/<slug>/releases/<tag>/` |
| `/<owner>/<repo>/archive/<ref>.zip` | `/repos/<slug>/archive/<ref>.zip` |
| `/<owner>` (profile) | `/` |
| `/orgs/<org>` | `/orgs/<slug>/` |
| Pulse / insights | `/repos/<slug>/insights/` |
| Gists | `/notes/<slug>/` |

Two differences to watch:

- The slug is the repository name slugified, and can be overridden per entry with `"slug"`.
  Pick your slugs before you publish; changing one later breaks every link to it.
- A `/` in a branch name becomes `~` in the URL (`feat/zip` → `/tree/feat~zip/`), because a
  slash would be indistinguishable from a directory separator.

`#` and `%` in a committed path cannot be expressed in a static URL at all. Those files are
listed in the file table without a link, and ingest raises `repo-path-unservable`.

---

## 5. People still need somewhere to clone from

frznforge serves HTML, raw files and source zips. It does not speak the git protocol, and it
never will — that would need a server.

The Code panel on each repo page shows a clone URL only when the repo has an `upstream` link,
and it says plainly that the site is a read-only mirror. Imported repositories get `upstream`
automatically from the provider. **Local repositories do not** — set it yourself:

```json
// .frznforge.json, committed at the root of the repository
{
  "links": {
    "upstream": "https://codeberg.org/you/tool",
    "issues": "https://codeberg.org/you/tool/issues"
  }
}
```

or, if you cannot commit to that repository:

```jsonc
{ "type": "local", "path": "../tool",
  "overrides": { "links": { "upstream": "https://codeberg.org/you/tool" } } },
```

Without an upstream, visitors get the source zip and nothing else. That is a legitimate choice
for a genuinely archived project — just make it on purpose.

---

## 6. Keeping the site fresh

The site is a snapshot of whatever ingest last saw. Three things control how stale it gets.

### Rebuild cadence

| Setup | Freshness |
|---|---|
| Local repos, local build | As fresh as your last `frznforge build` |
| Imported repos, CI on push to the site repo | Stale until *this* repo changes — the wrong trigger |
| Imported repos, CI on a nightly `schedule:` | A day behind at worst. This is the right default |
| `repository_dispatch` from each source repo's own workflow | Near-immediate, at the cost of a workflow in every repository |

A rebuild is cheap in wall time and expensive in API calls: frznforge makes roughly two API
calls per imported repository per build. Anonymous GitHub allows 60 requests/hour per IP;
a token raises it to 5,000. Hourly rebuilds of 30 repositories are fine with a token and will
be rate-limited without one (`⚠ [remote-rate-limited]`).

### The mirror cache

`.frznforge-cache/mirrors/` holds one bare mirror per imported repository —
`<type>-<host>-<owner>-<repo>-<digest>`, where the digest disambiguates two sources whose
readable halves sanitise to the same string — plus a sibling `<same-name>.meta.json` of the last successful
API answers.

- First build clones; later builds run `git remote update --prune`, which is much faster.
- Cache it between CI runs (`actions/cache` on `.frznforge-cache`), or every build re-clones
  every repository from scratch.
- It is disposable. Delete the whole directory to force a clean re-clone, or delete one mirror
  directory to refresh a single repository.
- On Windows, a deep `cacheDir` path can hit `MAX_PATH` and the clone fails with
  `Filename too long`. Fix it with `git config --global core.longpaths true`, or point
  `ingest.cacheDir` at something shallow.

### `ingest.fetch`

| Mode | Behaviour |
|---|---|
| `"auto"` (default) | Refresh over the network; fall back to the cache and warn if the network or the API fails |
| `"never"` | Never touch the network. Builds from the mirror and the cached API answers, warning `remote-cache-stale` |
| `"always"` | Always hit the network. Failures are still warnings, never build errors |

**A build never fails because a forge is unreachable.** The worst case is a repository missing
from the site plus a warning. That is the whole point of the cache: your public face does not
go down because someone else's did.

Typical locked-down setup: build once online to populate the cache, commit nothing but the
config, then set `"fetch": "never"` and build offline as often as you like.

---

## 7. A migration plan that works

1. **Start with one repository.** `{ "type": "github", "owner": "you", "repo": "smallest-thing" }`,
   `frznforge build`, then `frznforge dev` to look at it. Confirm the description, license,
   languages and releases look right before you scale up.
2. **Bulk-add the rest.**
   `frznforge init --provider=github --account=you --select=all-nfna` adds everything except
   forks and archived repositories, with a confirmation and a config backup.
3. **Fix the metadata at the source.** Anything wrong on the site is usually wrong on the
   forge. Commit a `.frznforge.json` in each repository so the description, tags and links
   travel with the code; use `"overrides"` in `frznforge.config.jsonc` only for repositories you
   cannot commit to.
4. **Point the escape hatches back at the forge.** `links.issues` and `links.upstream` on every
   repository you still accept contributions to.
5. **Write the profile.** `content/profile.md` is the front page; `pinned:` decides what a
   first-time visitor sees. Set `identities:` to the emails you commit under.
6. **Decide about archives and branch trees** before the first deploy —
   see [deploying.md §3](./deploying.md#3-how-big-will-my-site-be).
7. **Deploy, then set a rebuild schedule.** [deploying.md](./deploying.md).
8. **Only then** consider making the forge account private, or archiving it. Keep the git
   remote alive: the site links to it for cloning and for issues.
