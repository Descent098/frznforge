# Importing repositories from a forge

frznforge builds a static site out of git repositories. Those repositories can live on your
disk (`type: 'local'`) or on GitHub, GitLab, Gitea or Forgejo. A remote repository is:

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
npm run frznforge -- init
```

`init` asks for a provider, an account, and which of that account's repositories to add,
then offers to write them into `frznforge.config.ts`. See [`init`](#3-the-init-command)
below.

### By hand

Add entries to the `repos` array in `frznforge.config.ts`.

**GitHub**

```ts
{ type: 'github', owner: 'Descent098', repo: 'ezcv', releases: 'provider' },

// GitHub Enterprise — host is the API base, not the web UI:
{ type: 'github', host: 'https://git.example.com/api/v3', owner: 'team', repo: 'thing' },
```

**GitLab** — one `project` field holding the full namespaced path (groups and subgroups
included); the importer URL-encodes it for you.

```ts
{ type: 'gitlab', project: 'gitlab-org/gitlab-runner' },
{ type: 'gitlab', host: 'https://gitlab.example.com', project: 'group/sub/proj' },
```

**Gitea / Forgejo** — `host` is required and is the instance root (the importer appends
`/api/v1`). They speak the same REST API; the two names exist so the site and the docs can
label your repos correctly.

```ts
{ type: 'gitea',   host: 'https://gitea.example.com', owner: 'me', repo: 'tool' },
{ type: 'forgejo', host: 'https://codeberg.org',      owner: 'me', repo: 'tool' },
```

**Fields every remote source accepts**

| Field | Default | What it does |
|---|---|---|
| `host` | `https://api.github.com` / `https://gitlab.com`; required for gitea/forgejo | API/instance base URL |
| `slug` | the repo name, slugified | URL segment for the repo's pages |
| `overrides` | – | Same shape as `.frznforge.json`; wins over everything else |
| `releases` | `'provider'` (remote) / `'tags'` (local) | Where the releases page gets its content |
| `tokenEnv` | see [tokens](#2-tokens) | Env var holding the API token — **never the token itself** |

Metadata precedence, highest first:

1. `overrides` in `frznforge.config.ts`
2. `.frznforge.json` committed inside the repository
3. what the provider's API reports
4. values derived from the repository itself (detected license, first heading of the README, …)

### Releases: `provider` vs `tags`

- `releases: 'provider'` imports the forge's release objects: title, body, pre-release flag,
  publication date, author and the download assets. Needs an API call per repo.
- `releases: 'tags'` builds the releases page from **annotated git tags** in the mirror, with
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
`frznforge.config.ts`, never prints one, never puts one in a warning or an error message
(they are redacted to `***`), and never stores one in `data/forge.json`. `init` will not
even let you type one in.

| Provider | Variables checked, in order | Scope needed |
|---|---|---|
| GitHub | `FRZNFORGE_GITHUB_TOKEN`, `GITHUB_TOKEN` | `public_repo` (`repo` for private repositories) |
| GitLab | `FRZNFORGE_GITLAB_TOKEN`, `GITLAB_TOKEN` | `read_api` |
| Gitea | `FRZNFORGE_GITEA_TOKEN`, `GITEA_TOKEN` | `read:repository` |
| Forgejo | `FRZNFORGE_FORGEJO_TOKEN`, `FORGEJO_TOKEN` | `read:repository` |

Setting `tokenEnv: 'WORK_GITEA_TOKEN'` on a source **replaces** that list for that source —
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
npm run frznforge -- init          # interactive
npm run frznforge -- --help        # usage
```

What it does, in order:

1. asks which provider (and, for Gitea/Forgejo, the instance URL);
2. asks for the account — a user, an organisation, or a GitLab group path;
3. reports **which environment variable** it consulted for a token and whether one was found
   (never the value), then lists that account's repositories over the API, paginated,
   public-only when there is no token;
4. lets you pick with a numbered multi-select — `1,3,5-8`, `all`, or repository names;
5. asks whether releases should come from the provider or from tags;
6. prints the exact config snippet, shows a `+` line per entry it would add, and asks for a
   `y/N` confirmation before touching anything.

When you confirm, it copies `frznforge.config.ts` to
`frznforge.config.ts.<timestamp>.bak` and splices the new entries into the existing
`repos: [...]` array. The rest of the file — your comments and formatting — is left exactly
as it was. Running `init` twice never duplicates an entry: sources already present are
reported as "already present, skipped".

If the config cannot be found or has no `repos` array, `init` just prints the snippet for you
to paste.

### Flags (for scripting and CI)

| Flag | Meaning |
|---|---|
| `--provider=github\|gitlab\|gitea\|forgejo` | Provider to query |
| `--host=<url>` | API/instance base URL (required for gitea/forgejo) |
| `--account=<name>` | User, organisation, or GitLab group path |
| `--select=all\|1,3,5-8\|name,name` | Which listed repos to add |
| `--releases=provider\|tags` | Release source (default `provider`) |
| `--config=<path>` | Config file to edit (default: nearest `frznforge.config.ts`) |
| `--print` | Print the snippet and stop; never writes a file |
| `--yes` | Write without the confirmation prompt |
| `--help`, `-h` | Usage |

```bash
npm run frznforge -- init --provider=github --account=Descent098 --select=all --print
npm run frznforge -- init --provider=forgejo --host=https://codeberg.org \
  --account=me --select=1-3 --releases=tags --yes
```

`init` is interactive by design. If stdin is not a terminal **and** the flags do not describe
the whole run, it prints how to do it non-interactively and exits `1` — it never hangs
waiting for input that cannot arrive.

---

## 4. The cache directory

```
.frznforge-cache/
  github/api.github.com/descent098/ezcv-1f4a3b02.git
  github/api.github.com/descent098/ezcv-1f4a3b02.meta.json
  gitlab/gitlab.com/gitlab-org/gitlab-runner-7c2d9e51.git
  gitea/gitea.example.com-3000-git/me/tool-0a91c4de.git
  forgejo/codeberg.org/forgejo/forgejo-b3e07a15.git
```

- One bare mirror per remote source; the first build clones, later builds fetch.
- The `-<8 hex>` suffix is a hash of the source's exact identity (provider, host, owner/repo).
  Directory names are lower-cased and stripped of everything outside `[a-z0-9._-]`, so without
  it two different repos — `Widget` and `widget`, or two non-ASCII names — could land on one
  mirror and each be published with the other's code.
- Next to each mirror, a `.meta.json` holds the last successful API answers (description,
  topics, links, license, releases). An offline build, or one where the API is down, serves
  the repo from there instead of publishing it stripped of its metadata.
- Configured with `ingest.cacheDir` (default `./.frznforge-cache`, relative to the config
  file). `FRZNFORGE_CACHE_DIR` overrides it, mostly for tests.
- Git-ignored and completely disposable. **To clear it**, delete the directory:

```bash
rm -rf .frznforge-cache
```
```powershell
Remove-Item -Recurse -Force .frznforge-cache
```

The next build re-clones everything, which is slower but always safe. Delete a single
`*.git` directory under it to refresh just one repo.

---

## 5. Offline and locked-down builds: `ingest.fetch`

```ts
ingest: {
  cacheDir: './.frznforge-cache',
  fetch: 'auto',   // 'auto' | 'never' | 'always'
}
```

| Mode | Behaviour |
|---|---|
| `'auto'` (default) | Refresh over the network; if the network or the API fails, fall back to whatever is in the cache and warn |
| `'never'` | Offline. Never touch the network; build from the mirror **and** the cached API answers. Warns that the data is stale, and says plainly when a repo has nothing cached |
| `'always'` | Always hit the network. Failures are still warnings, never build errors |

A build **never** fails because a forge is unreachable, unauthenticated, or rate limited.
The worst case is a repo missing from the site plus a warning.

Typical offline build: run `npm run build` once while online to populate the cache, then set
`ingest.fetch: 'never'` in `frznforge.config.ts` and build as often as you like with no
network. There is no environment-variable shortcut for this on purpose — an offline build is
a deliberate, committed choice about what the site contains.

---

## 6. Importing a repo you do not control

A repo's README and its release notes are markdown written by whoever can push to it or
publish on it, and frznforge renders them into your site's pages. For any source that is not
`type: 'local'`, that content is treated as untrusted:

- raw HTML in the markdown is **dropped** — `<script>`, `<iframe>`, `<img onerror=…>` and
  everything else. Only markdown-generated HTML survives, so a README that leans on inline
  HTML (badge tables, `<details>` blocks) renders with those parts missing;
- link and image URLs are limited to `http`, `https`, `mailto` and relative targets, so a
  `javascript:` or `data:` link renders inert.

Your own repos (`type: 'local'`) and `content/profile.md` are rendered as written, HTML
included — that is your content.

This is about code execution, not editorial control. Everything else an imported repo says
about itself (its description, topics, links) still goes onto your site, so import repos you
are willing to vouch for, and use `overrides` in `frznforge.config.ts` when you want to say
something different.

---

## 7. Rate limits and warnings

Warnings are printed by `npm run ingest` (`⚠ [code] repo: message`) and counted in the site
footer. The import-related ones:

| Code | Meaning | What to do |
|---|---|---|
| `remote-fetch-failed` | The API call or the clone/fetch failed | Check the network and the host URL. If a cache exists it was used; otherwise the repo is skipped |
| `remote-auth-missing` | The repo answered 401/403/404 and no token is configured | Set the provider's token variable, or drop the entry if the repo is private on purpose |
| `remote-rate-limited` | The provider refused for rate-limit reasons | Set a token (anonymous limits are much lower), or build less often. GitHub's limit resets hourly |
| `remote-cache-stale` | The cached mirror and/or the cached API answers were used instead of fresh data (`fetch: 'never'`, or a failed refresh) | Nothing, if that was intended; otherwise fix the underlying fetch failure. The message says whether anything was cached at all |

GitHub's anonymous limit is 60 requests/hour per IP; a token raises it to 5,000. GitLab,
Gitea and Forgejo vary per instance. frznforge makes roughly two API calls per remote repo
per build, so a token is worth setting even for a handful of repos.

---

## 8. Verify it worked

```bash
npm run ingest
```

Expect a line per repo and a summary. Then:

- **The repo is in the artifact.** `data/forge.json` contains a repo whose `source.type` is
  your provider and whose `source.webUrl` points at the forge.
- **It has history.** Non-zero `commitCount`, a `defaultBranch`, and files under `files`. If
  everything is zero, the mirror clone did not happen — look for a `remote-fetch-failed`
  warning.
- **Releases arrived.** With `releases: 'provider'`, `releaseMode` is `"provider"` and
  `releases` is a non-empty array (unless the repo genuinely has none).
- **No token leaked.** `grep` your token value in `data/forge.json` and in the ingest output —
  there must be zero hits. (There is a test for this, but check once with your own token.)
- **The pages exist.** `npm run build`, then open `dist/repos/<slug>/index.html` and
  `dist/repos/<slug>/releases/index.html`.

A second `npm run ingest` should be much faster: the mirror is only fetched, not cloned.
