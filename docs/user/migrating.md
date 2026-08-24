# Coming from GitHub, GitLab, Gitea or Forgejo

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

Each repository picks one of two sources with `releases:` in its config entry — `'provider'`
(the default for imported repos) or `'tags'` (the default for local ones).

| | `releases: 'provider'` | `releases: 'tags'` |
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
- **Falling back is automatic.** A repo set to `'provider'` whose provider reports no releases
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
- `init` lists private repositories when a token is set, and marks them with a `private` badge.
  `--select=all-np` (or the **Hide private** toggle in `--web`) excludes them.
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

- The slug is the repository name slugified, and can be overridden per entry with `slug:`.
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

```ts
{ type: 'local', path: '../tool',
  overrides: { links: { upstream: 'https://codeberg.org/you/tool' } } },
```

Without an upstream, visitors get the source zip and nothing else. That is a legitimate choice
for a genuinely archived project — just make it on purpose.

---

## 6. Keeping the site fresh

The site is a snapshot of whatever ingest last saw. Three things control how stale it gets.

### Rebuild cadence

| Setup | Freshness |
|---|---|
| Local repos, local build | As fresh as your last `npm run build` |
| Imported repos, CI on push to the site repo | Stale until *this* repo changes — the wrong trigger |
| Imported repos, CI on a nightly `schedule:` | A day behind at worst. This is the right default |
| `repository_dispatch` from each source repo's own workflow | Near-immediate, at the cost of a workflow in every repository |

A rebuild is cheap in wall time and expensive in API calls: frznforge makes roughly two API
calls per imported repository per build. Anonymous GitHub allows 60 requests/hour per IP;
a token raises it to 5,000. Hourly rebuilds of 30 repositories are fine with a token and will
be rate-limited without one (`⚠ [remote-rate-limited]`).

### The mirror cache

`.frznforge-cache/` holds one bare mirror per imported repository plus a `.meta.json` of the
last successful API answers.

- First build clones; later builds run `git remote update --prune`, which is much faster.
- Cache it between CI runs (`actions/cache` on `.frznforge-cache`), or every build re-clones
  every repository from scratch.
- It is disposable. Delete the whole directory to force a clean re-clone, or delete one
  `*.git` directory to refresh a single repository.
- On Windows, a deep `cacheDir` path can hit `MAX_PATH` and the clone fails with
  `Filename too long`. Fix it with `git config --global core.longpaths true`, or point
  `ingest.cacheDir` at something shallow.

### `ingest.fetch`

| Mode | Behaviour |
|---|---|
| `'auto'` (default) | Refresh over the network; fall back to the cache and warn if the network or the API fails |
| `'never'` | Never touch the network. Builds from the mirror and the cached API answers, warning `remote-cache-stale` |
| `'always'` | Always hit the network. Failures are still warnings, never build errors |

**A build never fails because a forge is unreachable.** The worst case is a repository missing
from the site plus a warning. That is the whole point of the cache: your public face does not
go down because someone else's did.

Typical locked-down setup: build once online to populate the cache, commit nothing but the
config, then set `ingest.fetch: 'never'` and build offline as often as you like.

---

## 7. A migration plan that works

1. **Start with one repository.** `{ type: 'github', owner: 'you', repo: 'smallest-thing' }`,
   `npm run ingest`, `npm run dev`. Confirm the description, license, languages and releases
   look right before you scale up.
2. **Bulk-add the rest.** `npm run frznforge -- init --provider=github --account=you
   --select=all-nfna` adds everything except forks and archived repositories, with a
   confirmation and a config backup.
3. **Fix the metadata at the source.** Anything wrong on the site is usually wrong on the
   forge. Commit a `.frznforge.json` in each repository so the description, tags and links
   travel with the code; use `overrides:` in `frznforge.config.ts` only for repositories you
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
