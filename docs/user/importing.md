# Importing repositories from a forge

frznforge builds a static site out of git repositories. Those repositories can live on your
disk (`"type": "local"`) or on GitHub, GitLab, Gitea or Forgejo. A remote repository is:

1. described over the provider's REST API (description, topics, license, default branch,
   releases), then
2. **mirror-cloned** into a local cache (`git clone --mirror`, refreshed with
   `git remote update --prune` on later builds), and
3. scanned by exactly the same code that reads a local repo.

Everything happens at build time. The published site never calls a forge.

---

## 1. Add the repositories

### Interactively

```
frznforge init
```

`init` asks for a provider, an account, and which of that account's repositories to add,
then offers to write them into `frznforge.config.jsonc`. See [`init`](#3-the-init-command)
below.

### By hand

Add entries to the `"repos"` array in `frznforge.config.jsonc`. It is JSON with comments, so
quote every key.

**GitHub**

```jsonc
{ "type": "github", "owner": "Descent098", "repo": "ezcv", "releases": "provider" },

// GitHub Enterprise — host is the API base, not the web UI:
{ "type": "github", "host": "https://git.example.com/api/v3", "owner": "team", "repo": "thing" },
```

**GitLab** — one `project` field holding the full namespaced path (groups and subgroups
included); the importer URL-encodes it for you.

```jsonc
{ "type": "gitlab", "project": "gitlab-org/gitlab-runner" },
{ "type": "gitlab", "host": "https://gitlab.example.com", "project": "group/sub/proj" },
```

**Gitea / Forgejo** — `host` is required and is the instance root (the importer appends
`/api/v1`). They speak the same REST API; the two names exist so the site and the docs can
label your repos correctly.

```jsonc
{ "type": "gitea",   "host": "https://gitea.example.com", "owner": "me", "repo": "tool" },
{ "type": "forgejo", "host": "https://codeberg.org",      "owner": "me", "repo": "tool" },
```

Leave a required field out and the config fails to load, naming the entry by index:

```
error: config is not valid:
  - repos[2]: gitea sources need an explicit host (they are self-hosted)
```

**Fields every remote source accepts**

| Field | Default | What it does |
|---|---|---|
| `host` | `https://api.github.com` / `https://gitlab.com`; required for gitea/forgejo | API/instance base URL |
| `slug` | the repo name, slugified | URL segment for the repo's pages |
| `overrides` | – | Same shape as `.frznforge.json`; wins over everything else |
| `releases` | `"provider"` (remote) / `"tags"` (local) | Where the releases page gets its content |
| `tokenEnv` | see [tokens](#2-tokens) | Env var holding the API token — **never the token itself** |
| `org` | – | Slug of an organization this repo belongs to (see [organizations](configuration.md#organizations-organizations--contentorgsslugmd)). Naming one that is not configured is a warning, not an error |

Metadata precedence, highest first:

1. `overrides` in `frznforge.config.jsonc`
2. `.frznforge.json` committed inside the repository
3. what the provider's API reports
4. values derived from the repository itself (detected license, first heading of the README, …)

### Releases: `provider` vs `tags`

- `"releases": "provider"` imports the forge's release objects: title, body, pre-release flag,
  publication date, author and the download assets. Needs an API call per repo.
- `"releases": "tags"` builds the releases page from **annotated git tags** in the mirror, with
  the tag message rendered as markdown. No API call, works fully offline, and is the right
  choice for repos whose "releases" are just tags.

If a provider reports no releases, the site falls back to annotated tags automatically.

> **GitLab caveats.** Its releases API reports no asset size and no content type, so those
> come out as `0` and `null`. It also has no draft or pre-release flag: `upcoming_release`
> looks like one but GitLab derives it from the clock (`released_at > now`), which would make
> your artifact change by itself, so `prerelease` is always `false` for GitLab releases.
> Release and asset URLs are resolved against the project's web URL, because GitLab hands out
> host-relative asset links.

---

## 2. Tokens

Tokens are read from **environment variables only**. frznforge never writes one into
`frznforge.config.jsonc`, never prints one, never puts one in a warning or an error message
(they are redacted to `***`), and never stores one in `data/forge.json`. `init` will not
even let you type one in.

| Provider | Variables checked, in order | Scope needed |
|---|---|---|
| GitHub | `FRZNFORGE_GITHUB_TOKEN`, `GITHUB_TOKEN` | `public_repo` (`repo` for private repositories) |
| GitLab | `FRZNFORGE_GITLAB_TOKEN`, `GITLAB_TOKEN` | `read_api` |
| Gitea | `FRZNFORGE_GITEA_TOKEN`, `GITEA_TOKEN` | `read:repository` |
| Forgejo | `FRZNFORGE_FORGEJO_TOKEN`, `FORGEJO_TOKEN` | `read:repository` |

Setting `"tokenEnv": "WORK_GITEA_TOKEN"` on a source **replaces** that list for that source —
useful when you pull from two instances of the same provider.

Without a token, public repositories still work; you just get the anonymous rate limit and
private repositories are invisible.

```bash
# bash / zsh
export GITHUB_TOKEN=…
```
```powershell
# PowerShell
$env:GITHUB_TOKEN = '…'
```

For CI, put the token in the job's secret store and expose it as an environment variable.
Do not commit it, and do not pass it on the command line where it lands in shell history.

---

## 3. The `init` command

```
frznforge init          # interactive
frznforge init --help   # usage, including the token variables above
```

What it does, in order:

1. asks which provider (and, for Gitea/Forgejo, the instance URL);
2. asks for the account — a user, an organisation, or a GitLab group path;
3. reports **which environment variable** it consulted for a token and whether one was found
   (never the value), then lists that account's repositories over the API, paginated,
   public-only when there is no token;
4. lets you pick with a numbered multi-select — `1,3,5-8`, `all`, `all-nf` (all except forks),
   or repository names; see [selection specifiers](#selection-specifiers);
5. asks whether releases should come from the provider or from tags;
6. prints the exact config snippet, shows a `+` line per entry it would add, and asks for a
   `y/N` confirmation before touching anything.

When you confirm, it copies `frznforge.config.jsonc` to
`frznforge.config.jsonc.<timestamp>.bak` — for example
`frznforge.config.jsonc.20260823T195100Z.bak` — and splices the new entries into the existing
`"repos": [ … ]` array. The rest of the file — your comments and formatting — is left exactly as
it was. Running `init` twice never duplicates an entry: sources already present are reported as
`(already present, skipped)`.

If the config cannot be found or has no `"repos"` array, `init` just prints the snippet for you
to paste. Without `--config`, it looks for the nearest `frznforge.config.jsonc` at or above the
working directory.

### Selection specifiers

The same syntax works at the interactive prompt and in `--select=`.

| Specifier | Selects |
|---|---|
| `all` | every listed repository |
| `none` | nothing — `init` says "Nothing selected" and exits `0` |
| `1,3,5-8` | by position in the listing (1-based, ranges allowed) |
| `ezcv,sdu`, `me/ezcv` | by name or `owner/name`, case-insensitive |
| `all-nf` | all **except forks** |
| `all-na` | all **except archived** repositories |
| `all-np` | all **except private** repositories |
| `all-nfna`, `all-nf-na` | combined — flags concatenate or dash-separate, in any order |

An **empty answer** is the one place the two differ. `--select=` with nothing after it means
`none`; at the prompt an empty line takes the default the prompt shows in brackets, which is
`[all]`. Nothing is written either way without the `y/N` confirmation that follows.

A repository whose own name begins with `all-` — `all-contributors`, say — wins over the filter
grammar: an exact name or `owner/name` match is looked up first, so `--select=all-contributors`
selects that one repository and never reads `co` as a filter code.

Every exclusion flag is `n` + the initial of what it drops. They are case-insensitive
(`ALL-NF` works), and an unknown one is refused by name rather than silently ignored:

```
--select: unknown filter 'nx' in 'all-nx'; known: nf = forks, na = archived, np = private, and no listed repository is named 'all-nx'
```

(The trailing clause is there because an exact repository name wins over the filter grammar —
see the `all-contributors` case above.)

Exclusions apply **only** to the `all` form: `1,3` and `ezcv,sdu` name repositories outright,
so a fork or an archived repo you asked for by name is still added. They also cannot be mixed
into a list — `all-nf,ezcv` is refused rather than half-honoured.

**Not every provider can answer every flag.** GitLab's project listings do not report which
projects are forks, and report `archived` only for groups, not for user accounts. Rather than
quietly keep the repositories you asked to drop, `init` refuses the flag:

```
--select: 'nf' cannot be applied here: this provider's repository listing does not say which
repositories are forks. Select by name or index instead (e.g. name,name or 1,3,5-8).
```

The same is true of the **Hide forks / Hide archived** toggles in `--web`: a toggle the listing
cannot answer for is shown struck through and disabled instead of doing nothing.

When a filter drops anything, `init` says what it dropped before printing the snippet:

```
selected 86 of 118 (excluded 32 forks)
selected 71 of 118 (excluded 32 forks, 15 archived)
```

When two filters would drop the same repository it is counted once, under the first reason in
the `nf, na, np` order — so the numbers add up to the repositories actually removed
(`32 + 15 = 47`, and `118 − 71 = 47`), not to the account's totals for each flag.

If a filter drops *everything*, `init` says so and exits `1` instead of writing an empty
selection:

```
all-nf excluded all 4 repositories (4 forks) — nothing to add.
```

### Flags (for scripting and CI)

| Flag | Meaning |
|---|---|
| `--provider=github\|gitlab\|gitea\|forgejo` | Provider to query |
| `--host=<url>` | API/instance base URL (required for gitea/forgejo) |
| `--account=<name>` | User, organisation, or GitLab group path |
| `--select=<spec>` | Which listed repos to add — see [selection specifiers](#selection-specifiers) |
| `--releases=provider\|tags` | Release source (default `provider`) |
| `--config=<path>` | Config file to edit (default: the nearest `frznforge.config.jsonc` at or above the cwd) |
| `--print` | Print the snippet and stop; never writes a file |
| `--yes` | Write without the confirmation prompt |
| `--web` | Pick the repos in a local browser UI (interactive; cannot be combined with `--print`) |
| `--port=<n>` | Port for `--web` (default: a free one) |
| `--no-open` | Do not launch a browser for `--web`; print the URL instead |
| `--root=<dir>` | Directory the run is relative to (default: the cwd) |
| `--help`, `-h` | Usage |

Every option also accepts a space instead of `=` (`--account YOU`), and a following `--flag` is
never taken as a value.

```bash
frznforge init --provider=github --account=Descent098 --select=all --print

# a big account in one go: everything except forks and archived repositories
frznforge init --provider=github --account=Descent098 --select=all-nfna --yes

frznforge init --provider=forgejo --host=https://codeberg.org \
  --account=me --select=1-3 --releases=tags --yes
```

`init` is interactive by design. If stdin is not a terminal **and** the flags do not describe
the whole run, it prints how to do it non-interactively and exits `1` — it never hangs
waiting for input that cannot arrive.

### Picking in a browser: `--web`

```
frznforge init --web
```

`--web` replaces the numbered prompt with a small local web UI for choosing repositories — and
for editing the rest of `frznforge.config.jsonc` and your profile page too: same providers, same
tokens (still environment-only), same textual config editing. It is interactive — it needs a
browser on the machine you run it on, so it is not a CI form, and it cannot be combined with
`--print`.

`--provider`, `--host`, `--account` and `--releases` pre-seed the form (an account given on the
command line is listed as soon as the page opens), `--config` fixes the file that gets written,
and `--port` / `--no-open` control the server. `--select` and `--yes` have no effect: under
`--web` the picking and the confirming both happen in the page.

#### What you see

The wizard opens one page with five numbered sections plus a **Done**/**Cancel** control, and it
does not move on until you tell it to:

1. **Source** — provider, account, and (for Gitea/Forgejo, or behind *Custom API host* for the
   others) the instance URL. Under the fields it says which environment variable it consulted
   for a token and whether one was found — the **name only**, never a value.
2. **Repositories** — everything the account exposes, in a table with a checkbox per row, the
   description, and `fork` / `archived` / `private` badges. There is a text filter, quick
   toggles for **Hide forks / Hide archived / Hide private** (the same cuts the terminal's
   `all-nfna` specifiers make, and struck through and disabled when the provider's listing
   cannot answer for that flag — see [selection specifiers](#selection-specifiers)),
   **Select all / visible / none**, a click-to-sort *Repository* column, and a live
   "12 of 20 selected" count. Forks and archived repositories start unticked; everything else
   starts ticked.
3. **Write the config** — the release mode, then a live preview of the *exact* snippet that
   will be spliced in, the full path it goes to, and **Write to config**.
4. **Settings** — the rest of the config file: site title/URL/description and
   [`site.base`](configuration.md), owner name/handle/profile path, theme palette and the
   `theme.heat` recency boundaries, mermaid rendering, listing/notes/content paths, the whole
   `ingest` block behind an accordion (blob and commit caps, concurrency, reuse, insights, …),
   plus list editors for **organizations**, **contributors**, **hosted sites**
   (`hosting.sites`) and the **sources** already in the config (add sources with the picker
   above). The card shows the values your file actually sets, with schema defaults filling
   the gaps; **Save settings** writes only the fields you changed.

   Each list row also has an **Edit** disclosure, so an entry can be corrected in place rather
   than removed and re-added (which used to lose its comments). An entry's *identity* — an
   organization's slug, which repos point at — stays non-editable on purpose; change that by
   removing and re-adding.

   Fields that hold a picture (`owner.avatar`, an organization's or a contributor's `avatar`)
   have a file picker beside them. The upload writes the image into `public/images/` and fills
   the field with the path it chose — **the browser never names a file on disk**; the server
   derives the name from what you are editing and the image type it detects in the bytes. You
   still press Save, so an upload never silently rewrites your config.
5. **Profile** — the body of `content/profile.md` (or wherever `owner.profile` points) in a
   plain editor with a server-rendered preview. A YAML frontmatter block at the top of the file
   is shown read-only and round-trips byte-for-byte — the wizard edits the body, never your
   metadata.

**Done** ends the session, and **saves anything you edited but did not save**: a changed
settings field, a rewritten profile body. If one of those values is rejected, the wizard says so
and stays open rather than exiting having discarded it — **Cancel** is still the way to leave
without saving.

Every write is the same *textual* operation the terminal flow performs: only the edited field's
bytes change, and everything else in the file — comments, formatting, blank lines — is
untouched. Repo entries already present are skipped, so writing twice adds nothing the second
time.

A **Settings** save is guarded twice over. Before a byte of the file moves, the change is applied
to the loaded config and run through the same validation `frznforge build` uses, so a value the
schema refuses (a descending `theme.heat`, a reserved hosting slug) is rejected with the
loader's own message. After the write, the wizard re-reads the file and checks it parses to
*exactly* the config that check approved — if the textual edit went astray (a value that landed
in a comment, a key the editor could not follow), the previous bytes are put back and nothing is
left half-applied. The repo picker's own writes go through the same well-tested splicer the
terminal `init` uses.

The session stays up for any number of writes until **Done** (or **Cancel**, Ctrl-C, or 15 idle
minutes) ends it. The first write to each file takes one timestamped `<name>.<stamp>.bak` of its
pre-wizard state — however many saves follow, undoing the whole session is that one copy. (A
file the wizard *creates* had no pre-wizard state, so it gets no `.bak`.)

#### Why it is safe to have a web page that can write your config

`--web` is a page that can edit a file, so it is locked down on purpose:

- **It listens on `127.0.0.1` only** — never `0.0.0.0`. Nothing on your network can reach it.
- **Every request needs a one-time session key.** The key is minted per run and lives in the
  URL that gets printed and opened (`http://127.0.0.1:<port>/?s=…`). Any request without it —
  page or API — gets a `403`. That is what stops another program on your machine, or a random
  site open in another tab, from driving the wizard: `127.0.0.1` is reachable from any page in
  your browser, the session key is not guessable.
- **The `Host` and `Origin` headers are pinned** to the loopback address and port it is
  actually listening on, which is what defeats DNS rebinding.
- **The browser never names the file.** Writes go to the paths resolved by the process — the
  config from `--config` or the nearest `frznforge.config.jsonc`, the profile from that config's
  own `owner.profile` — and to nothing else. The settings endpoints only accept fields from a
  server-side allow-list, so the page cannot invent an edit the wizard was never meant to make.
- **The page cannot phone home.** It is served with a Content-Security-Policy that starts
  `default-src 'none'; connect-src 'self'` (and goes on to pin styles, scripts, images,
  form targets, `base-uri` and framing), so the browser itself refuses any request to
  anywhere but the local server. No CDN, no fonts, no analytics.

**The provider token never reaches the browser.** The page asks the local process for a
listing; the process reads `$GITHUB_TOKEN` (or whichever variable applies) and calls the
provider itself. All the page is ever told is the *name* of the variable a token came from.
Error messages are scrubbed before they are sent, and there is a test that drives every
endpoint with a sentinel token in the environment and asserts it appears in no response body
and no response header.

**…and it is only ever sent to a host you named yourself.** The *Custom API host* field is a
free-text box in a web page, so a host typed into it would otherwise get an
`Authorization: Bearer <your PAT>` for a URL nobody vetted. Instead the token goes out only
when the host is the provider's own API base (`https://api.github.com`, `https://gitlab.com`)
or the exact `--host` you passed on the command line *for that same provider*. Any other host
is listed **anonymously** — public repositories only — and the page says so:

```
codeberg.example is not Gitea's own API host, so $FRZNFORGE_GITEA_TOKEN was not sent to it —
only public repositories are listed. Restart with --host=… if that host really is your instance.
```

So a self-hosted instance you want listed *with* a token is started as
`frznforge init --web --provider=forgejo --host=https://codeberg.org`.

#### `--port` and `--no-open`

```bash
frznforge init --web --port=8765   # fixed port instead of a free one
frznforge init --web --no-open     # print the URL, do not launch a browser
```

`--no-open` is for headless machines and for when the automatic launch picks the wrong
browser. The URL is printed either way.

#### Over SSH

The server is bound to loopback on the remote machine, so forward the port rather than
exposing it:

```bash
# on the remote machine
frznforge init --web --port=8765 --no-open
# → http://127.0.0.1:8765/?s=<key>

# on your laptop, in another terminal
ssh -N -L 8765:127.0.0.1:8765 you@remote
```

Then paste the printed URL — **including the `?s=…` key** — into your local browser. The port
has to match on both sides: the server checks that the `Host` header names `127.0.0.1`,
`localhost` or `[::1]` on the port it is listening on, so `-L 9000:127.0.0.1:8765` would be
refused. There is no option to bind anything but loopback, and a reverse proxy in front of it
would fail the same `Host` check.

---

## 4. The cache directory

```
.frznforge-cache/
  last-run.json                                              when each remote was last fetched
  mirrors/
    github-api.github.com-descent098-ezcv/                   the bare mirror
    github-api.github.com-descent098-ezcv.meta.json          its cached API answers
    gitlab-gitlab.com-gitlab-org-gitlab-runner/
    gitlab-gitlab.com-gitlab-org-gitlab-runner.meta.json
    gitea-gitea.example.com-me-tool/
    forgejo-codeberg.org-forgejo-forgejo/
  scan/
    <digest>.json                                            one repo's complete scan output
```

- **One bare mirror per remote source**, in a flat `mirrors/` directory named
  `<type>-<host>-<owner>-<repo>` (`<type>-<host>-<project>` for GitLab). The name is derived
  from the source's whole identity so two sources cannot collide, and it is stable across runs
  so the mirror is reused: the first build clones, later builds fetch. Characters outside
  `[a-z0-9._-]` become `-` and everything is lower-cased.
- **Next to each mirror, a `.meta.json`** holds the last successful API answers (description,
  topics, links, license, releases). Only normalised, non-volatile values are stored — no
  token, no timestamps, no counters — so an offline build served from here yields a
  byte-identical artifact rather than a repo stripped of its metadata.
- **`scan/`** holds one repo's complete scan output per entry, keyed by a digest over everything
  the scan reads: every ref name and object id, HEAD, the provider metadata, the overrides and
  the scan options. A repo whose inputs have not moved replays its recorded result instead of
  re-scanning.
- **`last-run.json`** records when and how well each remote source was last fetched, which is
  what the `ingest.reuse` freshness window and cooldown read.
- Configured with `ingest.cacheDir` (default `./.frznforge-cache`, relative to the config file).
  `FRZNFORGE_CACHE_DIR` overrides it, mostly for tests.
- **On Windows**, a deep project path plus the mirror's own directory depth can cross
  `MAX_PATH`, and the clone fails with `⚠ [remote-fetch-failed] … fatal: cannot stat
  '…/hooks/applypatch-msg.sample': Filename too long`. Fix it with
  `git config --global core.longpaths true`, or point `ingest.cacheDir` at a shallow
  directory such as `C:/ff-cache`.
- Git-ignored and completely disposable. **To clear it**, delete the directory:

```bash
rm -rf .frznforge-cache
```
```powershell
Remove-Item -Recurse -Force .frznforge-cache
```

The next build re-clones everything, which is slower but always safe. Delete a single directory
under `mirrors/` to refresh just one repo.

> **Upgrading from 0.3.0?** The mirror layout used to be nested and hash-suffixed
> (`github/api.github.com/descent098/ezcv-1f4a3b02.git`). Those directories are not found under
> the new names, so the first 0.4.0 ingest re-clones every imported repository and the old tree
> is dead weight. `<cacheDir>/highlight/` is dead too — 0.4.0 ships no highlight memo. See
> [migrating.md](./migrating.md#4-data-and-frznforge-cache-are-reused-as-they-are).

---

## 5. Offline and locked-down builds: `ingest.fetch`

```jsonc
"ingest": {
  "cacheDir": "./.frznforge-cache",
  "fetch": "auto",   // "auto" | "never" | "always"
}
```

| Mode | Behaviour |
|---|---|
| `"auto"` (default) | Refresh over the network; if the network or the API fails, fall back to whatever is in the cache and warn |
| `"never"` | Offline. Never touch the network; build from the mirror **and** the cached API answers. Warns that the data is stale, and says plainly when a repo has nothing cached |
| `"always"` | Always hit the network. Failures are still warnings, never build errors |

A build **never** fails because a forge is unreachable, unauthenticated, or rate limited.
The worst case is a repo missing from the site plus a warning.

Typical offline build: run `frznforge build` once while online to populate the cache, then set
`"fetch": "never"` in `frznforge.config.jsonc` and build as often as you like with no network.
There is no environment-variable shortcut for this on purpose — an offline build is a
deliberate, committed choice about what the site contains.

Note the cost `"never"` carries: every remote repo raises `remote-cache-stale`, and the warning
count is rendered in the footer of every page, so a permanently offline site advertises itself
as permanently degraded. If that bothers you, the freshness controls in
[configuration.md](configuration.md) (`reuse.maxAgeMinutes`, `reuse.cooldownSeconds`,
`reuse.skipUnchanged`) cut the traffic without lying about it.

---

## 6. Importing a repo you do not control

A repo's README and its release notes are markdown written by whoever can push to it or
publish on it, and frznforge renders them into your site's pages. For any source that is not
`"type": "local"`, that content is treated as untrusted:

- raw HTML in the markdown is **dropped** — `<script>`, `<iframe>`, `<img onerror=…>` and
  everything else. Only markdown-generated HTML survives, so a README that leans on inline
  HTML (badge tables, `<details>` blocks) renders with those parts missing;
- link and image URLs are limited to `http`, `https`, `mailto` and relative targets, so a
  `javascript:` or `data:` link renders inert.

Your own repos (`"type": "local"`) and `content/profile.md` are rendered as written, HTML
included — that is your content.

This is about code execution, not editorial control. Everything else an imported repo says
about itself (its description, topics, links) still goes onto your site, so import repos you
are willing to vouch for, and use `"overrides"` when you want to say something different.

---

## 7. Rate limits and warnings

Warnings are printed by `frznforge ingest` (`⚠ [code] repo: message`) and counted in the site
footer, where hovering the count lists every one. The import-related ones:

| Code | Meaning | What to do |
|---|---|---|
| `remote-fetch-failed` | The API call or the clone/fetch failed | Check the network and the host URL. If a cache exists it was used; otherwise the repo is skipped |
| `remote-auth-missing` | The repo answered 401/403/404 and no token is configured | Set the provider's token variable, or drop the entry if the repo is private on purpose. The message names the variables it checked |
| `remote-rate-limited` | The provider refused for rate-limit reasons | Set a token (anonymous limits are much lower), or build less often. GitHub's limit resets hourly |
| `remote-cache-stale` | The cached mirror and/or the cached API answers were used instead of fresh data (`"fetch": "never"`, or a failed refresh) | Nothing, if that was intended; otherwise fix the underlying fetch failure. The message says whether anything was cached at all |

GitHub's anonymous limit is 60 requests/hour per IP; a token raises it to 5,000. GitLab,
Gitea and Forgejo vary per instance. frznforge makes roughly two API calls per remote repo
per build, so a token is worth setting even for a handful of repos.

**What the build does about it.** A 429 — or GitHub's 403-with-no-quota-left — is retried with
exponential backoff keyed to the **forge**, not to the repo, so the repos being ingested in
parallel from one host wait behind a single timer instead of each hammering the window; a
different forge is unaffected. The provider's own `Retry-After` is honoured. If a forge asks for
longer than a minute, the run stops calling it for that period and the remaining repos on that
host fall back to their cached metadata immediately rather than each burning a retry ladder.
Repos with **no** cached metadata are fetched first, so a run that does hit a limit spends its
budget where there is nothing to fall back on.

If you build often against a large account, two opt-in knobs in
[configuration.md](configuration.md) cut the traffic further: `ingest.reuse.cooldownSeconds`
(skip a repo fetched successfully within the cooldown — this is the one that reduces API calls)
and `ingest.reuse.skipUnchanged` (one `git ls-remote` decides whether the mirror already has
everything, which saves *git* traffic). Set `ingest.failOnDegraded: true` if you would rather a
rate-limited build failed than quietly published stale metadata.

**If some repos are permanently blank**, the quota ran out before the run reached them and will
do so again next time, because an ordinary run re-requests metadata for every repo — including
the ones whose answer is already cached. Fix it with:

```bash
frznforge ingest --backfill-metadata
```

which asks the provider only about the repos that have no metadata yet, and skips git entirely —
or `frznforge build --backfill-metadata` to do that and render in one step. It reports what it
did:

```
  backfill: 13 filled, 4 still missing, 56 already had metadata (no network)
    ✓ filled: ahd, aoc-2024, …
    ⚠️ still missing: ezcv, sdu, …
      Run it again later — the quota resets, and each run only spends it on these.
```

Repeat until nothing is still missing. A token is the other half of the answer: anonymous GitHub
allows 60 requests an hour, and frznforge needs roughly two per repo, so an account of more than
~30 repos cannot be refreshed in one anonymous run at all.

---

## 8. Verify it worked

```bash
frznforge ingest
```

Expect a line per repo and a summary:

```
frznforge ingest → /home/you/my-site/data
  2 remote source(s) — cache /home/you/my-site/.frznforge-cache (fetch: auto)
  ▸ hello-forge
  ▸ ezcv
    ✓ hello-forge: 2 commits, 1 branches, 1 tags, 5 files
    ⇄ ezcv (github: cloned)
    ✓ ezcv: 131 commits, 5 branches, 11 tags, 164 files
done: 2 repo(s), 493 blob(s), 14 archive(s), 0 warning(s) in 19845ms
```

Then:

- **The repo is in the artifact.** `data/forge.json` contains a repo whose `source.type` is
  your provider and whose `source.webUrl` points at the forge. `frznforge verify` will tell you
  the artifact is well-formed and re-serializes byte for byte.
- **It has history.** Non-zero `commitCount`, a `defaultBranch`, and files under `files`. If
  everything is zero, the mirror clone did not happen — look for a `remote-fetch-failed`
  warning.
- **Releases arrived.** With `"releases": "provider"`, `releaseMode` is `"provider"` and
  `releases` is a non-empty array (unless the repo genuinely has none).
- **No token leaked.** `grep` your token value in `data/forge.json` and in the ingest output —
  there must be zero hits. (There is a test for this, but check once with your own token.)
- **The pages exist.** `frznforge build`, then open `dist/repos/<slug>/index.html` and
  `dist/repos/<slug>/releases/index.html`.

A second `frznforge ingest` should be much faster: the mirror is only fetched, not cloned, and
a repo whose refs have not moved replays its recorded scan (`⇄ ezcv (github: reused)`).
