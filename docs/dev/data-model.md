# Data model (ingest artifact)

`npm run ingest` turns the repositories listed in `frznforge.config.ts` — local directories
and, since schema v3, repos hosted on GitHub / GitLab / Gitea / Forgejo — into one JSON
artifact plus a blob store. Since schema v4 it also collects **notes** (a plain folder of
files on disk) and resolves **organizations** (groupings of repos) into the same artifact.
Every page of the site is built from this artifact and nothing else — the site never talks to
git and never talks to a forge.

The schema is defined once, as zod objects + inferred types, in `src/lib/data/schema.ts`.
This document explains the layout and the meaning of each field; the schema file is the
source of truth for exact shapes.

## Layout on disk

```
<ingest.outDir>/              (default ./data, git-ignored)
├── forge.json                the ForgeData artifact (pretty-printed, 2-space indent, trailing newline)
├── blobs/
│   └── <sha>                 raw content of every *stored* file, keyed by git blob id
└── archives/
    └── <slug>/
        └── <ref-slug>.zip    zip source archive per ref (default branch + treed tags)
```

- `forge.json` is read by `loadForgeData(outDir)` (`src/lib/data/load.ts`), which validates
  it with `parseForgeData`. A missing artifact is not an error — the site builds empty.
- `blobs/<sha>` holds the bytes of each file whose `FileInfo.stored` is `true` — since
  schema v2 this includes binary files within the size cap. The sha is the git blob object
  id, so identical content across repos/paths/branches is stored once.
  `readBlob(outDir, sha)` returns it as UTF-8 text. Since schema v4 **note** content lives in
  the same directory, keyed by `NoteFile.sha` — `sha1('note <len>\0' + bytes)`. The `note`
  prefix mirrors git's own `blob <len>\0` domain separation and is load-bearing: the two key
  spaces share one directory, so hashing note bytes bare would let a note whose raw bytes
  happen to *be* a `blob <len>\0…` pre-image take that repo file's key and — notes are merged
  last — overwrite it. Domain-separated, no note key can ever come from a `blob` pre-image. A
  note and a repo file with identical content therefore occupy two entries; notes are few and
  the alternative is minting fake git object ids for content that never was in git.
- `archives/<slug>/<ref-slug>.zip` are produced with `git archive --format=zip <commit>`
  (committed content only, never the working tree). `<ref-slug>` is the ref name with every
  `/` replaced by `~` (git refnames can never contain `~`, so this is collision-free), e.g.
  tag `rel/1.0` → `rel~1.0.zip`. The prefix inside the zip is `<slug>-<ref-slug>/`.
- `writeArtifact(data, blobs, archives, outDir)` makes `blobs/` and `archives/` mirror the
  current artifact exactly: missing or size-mismatched files are (re)written and any file
  not referenced by the current run is deleted.

### Remote mirror cache (schema v3)

Remote sources are not scanned over the network. Each one is mirror-cloned into
`ingest.cacheDir` (default `./.frznforge-cache`, resolved absolute like `outDir`,
git-ignored) and the ordinary local scanner then runs on that bare mirror:

```
<ingest.cacheDir>/
└── <provider>/                            github | gitlab | gitea | forgejo
    └── <host-slug>/                       api.github.com, gitea.example.com-3000, …
        ├── <owner>/<repo>-<digest>.git    bare mirror (GitLab: the namespace nests)
        └── <owner>/<repo>-<digest>.meta.json   last successful importer answers
```

- The path is computed by `cachePathFor(cacheDir, source)` (`src/lib/config/index.ts`) and is
  what `ResolvedConfig.repos[].absPath` points at for a remote source, so everything
  downstream sees a single shape. It may not exist yet on the first build.
- `<host-slug>` is the host URL with the scheme stripped and every character outside
  `[a-z0-9.-]` (notably `:` and `/`, illegal in Windows paths) replaced by `-`. Owner, repo
  and namespace segments are sanitised the same way, capped at 48 characters, and reserved
  Windows basenames (`con`, `aux`, `com1`, …) are prefixed with `_`.
- `<digest>` is the first 8 hex characters of sha256 over the source's *unsanitised* identity
  (provider, host, and `<owner>/<repo>` or the GitLab project path). The sanitising above is
  lossy — it folds case and collapses every non-ASCII name to the same slug — so the digest is
  what makes the mapping injective. Without it two unrelated repos could share one mirror and
  each be published with the other's git content.
- The sibling `.meta.json` caches the importer's *normalised* answers (`ImportedRepoMeta` +
  `Release[]`): no tokens, no timestamps, no counters, so serving a build from it produces
  the same bytes a live call would have. It is read whenever `ingest.fetch` is `'never'` or an
  API call fails, which is what makes `remote-cache-stale` mean *stale* rather than *absent*.
- Neither the mirror path nor its basename is ever user-visible: a remote repo's default
  `slug` and `name` come from the config (and the provider's `name`), not from the directory.
- First run: `git clone --mirror`. Later runs: `git remote update --prune`.
- `ingest.fetch` controls the network: `'auto'` (default) fetches and falls back to the cache
  on failure, `'never'` is offline and uses the cache only, `'always'` always refreshes.
  None of the three can fail the build — see the warnings table.
- The cache is disposable. Deleting it costs a re-clone, nothing else; it is never read by
  the site and never referenced from the artifact.

## Guarantees

**Deterministic.** The same repositories at the same commits produce a byte-identical
`forge.json` and the same blob set, regardless of machine, clock or scan order:

- No wall-clock timestamps; every date comes from git and is normalised to UTC.
- Object keys are emitted in the order declared in the schema; `commits` keys (shas) and
  `files` keys (paths) are inserted in sorted order.
- Arrays have a defined order: `repos` by slug; `tree` by path; `branches` by name;
  `gitTags` by name; `contributors` by commit count desc, then name, then email;
  `languages` by bytes desc, then name; `Branch.commits` newest-first (topological);
  `notes` by date desc with undated notes last, then title, then slug (`compareNotes`);
  `Note.files` by path; `organizations` by slug; `Organization.repos` by slug.
- Warnings are collected in a fixed order: site-level, then notes, then organizations, then
  per repo in slug order.
- **One opt-out, and it is loud.** `notes.useMtime` lets an undated note take its date from
  the filesystem. Mtimes are not reproducible — a fresh clone stamps every file with the clone
  time — so switching it on trades away byte-identical output. It is off by default and
  documented as non-deterministic wherever it appears.
- **No volatile counters.** Provider APIs report values that change without the repository
  changing — stars, forks, watchers, open-issue counts, release asset download counts, "last
  fetched at" timestamps. None of them are imported. If a field would differ between two
  builds of the same commits and releases, it does not belong in the artifact.
- Imported timestamps are normalised to `IsoDate` (UTC, seconds precision) by the importer,
  so a provider changing its date formatting cannot change the bytes.

**Committed content only — for repositories.** Ingest never reads a repository's working tree
or index. It uses only git plumbing that reads committed objects (`for-each-ref`, `rev-list`,
`log`, `ls-tree`, `cat-file`, `rev-parse`). Untracked files, unstaged modifications and staged
but uncommitted changes are invisible; bare repositories work the same as checkouts.

Notes (schema v4) are the one deliberate exception, and they do not weaken the rule: `notes.dir`
is a plain folder, not a git repository. There is no committed tree to prefer and no working
copy to avoid, so `src/lib/ingest/notes.ts` reads it with `node:fs`. If you ever point
`notes.dir` inside a checkout, ingest still sees whatever is on disk — that is the contract.

**Never fails on odd repositories.** Empty repos, repos whose HEAD tree is empty, unborn
default branches, missing metadata files, etc. are reported as warnings; the repo entry is
still emitted (and valid) and the build continues. Only hard problems (git missing,
unwritable output dir, invalid site config) fail `npm run ingest`.

**Never fails on an unreachable forge.** A remote that is offline, unauthenticated, private
or rate-limited produces a `remote-*` warning and the build continues, using the cached
mirror when one exists and skipping the repo when it does not.

**Tokens never reach the artifact.** API tokens are read from environment variables only
(`tokenEnv`, else `FRZNFORGE_<PROVIDER>_TOKEN`, else `GITHUB_TOKEN` / `GITLAB_TOKEN` /
`GITEA_TOKEN` / `FORGEJO_TOKEN`). They are never written to config, never logged, and are
redacted to `***` in any warning or error message. The clone credential is passed to git as a
one-shot `-c http.extraheader=Authorization: Basic …` placed *before* the subcommand — never
on the remote URL and never via `git clone -c` — so it cannot be persisted into the mirror's
`.git/config`.

## `ForgeData` (top level)

Emitted in exactly this key order — `serializeForgeData` writes the object in the order the
fields are declared in `ForgeData`, and the snapshot tests compare bytes.

| field           | meaning                                                                          |
| --------------- | -------------------------------------------------------------------------------- |
| `schemaVersion` | Literal `SCHEMA_VERSION` (currently `4`). The site refuses artifacts of another version. |
| `repos`         | `Repo[]`, sorted by slug.                                                        |
| `notes`         | `Note[]` (schema v4), date desc / undated last / title asc — see "Notes".         |
| `organizations` | `Organization[]` (schema v4), sorted by slug — see "Organizations".               |
| `warnings`      | `Warning[]` — site-level warnings plus a mirror of every repo's own warnings (with `repo` set). |

## `Repo`

Identity + metadata. Precedence, highest first: site-config `overrides` > the repo's committed
`.frznforge.json` > provider metadata (schema v3, remote sources only) > values derived from
the repository itself.

| field         | meaning                                                                                                |
| ------------- | ------------------------------------------------------------------------------------------------------ |
| `slug`        | URL slug. From config `slug`, else the repo directory name slugified (`My_Repo` → `my-repo`). Collisions are suffixed `-2`, `-3`, … |
| `name`        | Display name. Default: directory basename (`.git` suffix dropped for bare repos).                       |
| `description` | ≤ 300 UTF-16 code units or `null` (the unit `z.string().max(300)` counts). Longer in-repo and provider values are truncated, never splitting a surrogate pair, with a `description-truncated` warning. Over-long config overrides are a config error. |
| `source`      | Discriminated on `type` — see "`RepoSource`" below.                                                    |
| `links`       | `{ homepage?, issues?, donations?, upstream? }` URLs.                                                   |
| `tags`        | Free-form topic tags (metadata, not git tags).                                                          |
| `template`    | Repo is a template.                                                                                    |
| `license`     | `License \| null` — see below.                                                                         |
| `releaseMode` | `'tags'` (releases derived from annotated tags) or `'provider'` (imported from the forge API, schema v3). Defaults to `'tags'` for local sources and `'provider'` for remote ones; `releases` in the source config and `releaseMode` in the repo metadata override that. |
| `releases`    | `Release[]` (schema v3) — imported releases, newest first (`publishedAt` desc, `tag` asc tiebreak). Always `[]` in tag mode, and `[]` in provider mode when the repo has no releases or the fetch failed. |

Git-derived:

| field           | meaning                                                                                              |
| --------------- | ---------------------------------------------------------------------------------------------------- |
| `empty`         | `true` when no branch has any commit. Then `defaultBranch`, `createdAt`, `updatedAt`, `readme` are `null`, lists are empty, `commitCount` is 0. |
| `defaultBranch` | HEAD's branch if it has commits; else `main`, then `master`, then the branch with the newest commit (warning `default-branch-fallback`). |
| `branches`      | Local branches (`refs/heads/*` only — no remote-tracking refs), sorted by name.                        |
| `gitTags`       | Git tags, sorted by name.                                                                            |
| `commits`       | Every commit listed in any branch's `commits`, keyed by sha (sorted). With `ingest.maxCommits` set, only the kept commits are present. |
| `commitCount`   | `Object.keys(commits).length`.                                                                       |
| `tree`          | Flat listing of the default-branch HEAD tree — every blob, tree, symlink and submodule at every depth, sorted by path. |
| `files`         | `FileInfo` for every blob/symlink in `tree`, keyed by path (sorted).                                 |
| `refTrees`      | `Record<refName, RefTree>` — browsable trees for every **non-default** branch plus the newest `ingest.tagTrees` tags (schema v2). The default branch is only in `tree`/`files`. Keys: branches first (name order), then treed tags (name order). |
| `archives`      | `Archive[]` — zip source archives for the default branch + every treed tag (schema v2). `[]` when `ingest.archives` is `false` or the repo is empty. Order: default branch first, then tags by name. |
| `languages`     | `LanguageStat[]` — see "Language stats". Computed from the **default branch** only, as are `contributors`, `readme`, license detection and `.frznforge.json`. |
| `contributors`  | Authors grouped by lower-cased email.                                                                |
| `readme`        | Root `README.md` (preferred) / `README` / `README.*`, with inlined content; `null` if absent, binary, or over `maxBlobBytes`. |
| `createdAt`     | Earliest commit date (committer date) among `commits`.                                               |
| `updatedAt`     | Latest commit date among `commits`.                                                                  |
| `warnings`      | This repo's warnings (`repo` = slug).                                                                |

### `Branch`

| field            | meaning                                                                 |
| ---------------- | ----------------------------------------------------------------------- |
| `name`           | Short name (`main`).                                                    |
| `head`           | Sha of the tip commit.                                                  |
| `commits`        | Shas reachable from `head`, newest first, topological order. Truncated to `ingest.maxCommits` when set (warning `commits-capped`, once per repo). |
| `lastCommitDate` | Commit date of `head`.                                                  |

### `Tag`

| field       | meaning                                                                         |
| ----------- | ------------------------------------------------------------------------------- |
| `name`      | Short name (`v1.2.0`).                                                          |
| `target`    | The commit the tag points at (annotated tags are peeled).                        |
| `annotated` | `true` for annotated tag objects, `false` for lightweight refs.                 |
| `message`   | Annotated tag message, trimmed; `null` for lightweight tags.                    |
| `tagger`    | `{ name, email }` for annotated tags (email without `<>`), else `null`.         |
| `date`      | Tagger date for annotated tags, else the target commit's commit date.           |

Tags that point at trees or blobs (not commits) are not representable and are skipped.

### `Commit`

| field                       | meaning                                                       |
| --------------------------- | ------------------------------------------------------------- |
| `sha`, `parents`            | Object id and parent ids (empty for root commits).            |
| `author`, `authorDate`      | `{ name, email }`, ISO UTC.                                   |
| `committer`, `commitDate`   | `{ name, email }`, ISO UTC.                                   |
| `subject`                   | First paragraph of the message (`%s`).                         |
| `body`                      | Everything after the first blank line, trimmed; `''` if none. |
| `files`                     | `CommitFileChange[]` (schema v2): the files touched by the commit vs its **first parent**, from `git log --numstat`, in git's diff order. Each is `{ path, additions, deletions }`; binary files have `null` counts. Renames record the **target** path. **Merge commits have `files: []`** — plain `git log` emits no numstat for merges, and this is the documented behaviour, not an omission. Root commits count every file as added. |
| `stats`                     | `{ filesChanged, additions, deletions }` — sums over `files`. Binary files count toward `filesChanged` but not the line sums. |

### `TreeEntry`

| field        | meaning                                                                                     |
| ------------ | ------------------------------------------------------------------------------------------- |
| `path`       | Full path from the repo root, forward slashes.                                              |
| `name`       | Last path segment.                                                                          |
| `type`       | `blob`, `tree`, `symlink` (mode 120000) or `commit` (submodule, mode 160000).               |
| `mode`       | Git mode string (`100644`, `100755`, `040000`, …).                                          |
| `sha`        | Blob id / tree id / submodule commit id.                                                    |
| `size`       | Bytes for blobs and symlinks; `null` for trees and submodules.                              |
| `lastCommit` | Sha of the newest commit on the default branch that touched the path (directories: newest among descendants). Falls back to the HEAD commit when history simplification hides it. |

### `FileInfo`

| field      | meaning                                                                       |
| ---------- | ----------------------------------------------------------------------------- |
| `path`     | Same key as in `files`.                                                       |
| `sha`      | Blob id — the key into `blobs/` when `stored`.                                 |
| `size`     | Bytes.                                                                        |
| `binary`   | A NUL byte occurs in the first 8000 bytes (git's heuristic).                  |
| `tooLarge` | `size > ingest.maxBlobBytes`; content not stored.                             |
| `stored`   | `size <= ingest.maxBlobBytes` — content written to `blobs/<sha>`. Since schema v2 this **includes binary files** within the cap (for raw file serving / image previews); before v2 binaries were never stored. |
| `language` | Language name from the extension/filename map (`src/lib/ingest/languages.ts`) or `null`. |

### `RefTree` (schema v2)

`{ kind, name, commit, tree, files }` — one entry per non-default ref in `Repo.refTrees`:

- `kind` is `'branch'` or `'tag'`; `name` is the short ref name (also the record key);
  `commit` is the commit the ref points at (annotated tags are peeled).
- `tree`/`files` have exactly the same shape and rules as `Repo.tree`/`Repo.files`, but
  `TreeEntry.lastCommit` is computed by walking history from **that ref's** head. Stored
  blobs land in the same content-addressed `blobs/` store, so files unchanged across
  branches cost nothing extra.
- Every non-default branch gets a tree. Tags are capped at the newest `ingest.tagTrees`
  (by tag date desc — tagger date for annotated tags, target commit date for lightweight
  ones; ties broken by name). When tags are skipped by the cap, the repo gets one
  `tag-trees-capped` warning. When a tag peels to a commit whose tree was already computed
  (the default branch head or another ref), the tree/files are reused rather than rescanned.
- In the pathological case of a branch and a tag sharing a name, the branch keeps the key
  and the tag gets no tree.

### `Archive` (schema v2)

`{ ref, kind, commit, file, bytes }` — one entry per zip in `Repo.archives`, covering the
default branch plus every tag that has a `RefTree`. `file` is the outDir-relative path
`archives/<slug>/<ref-slug>.zip` (`<ref-slug>`: `/` → `~`); `bytes` is the zip size.
Produced with `git archive --format=zip --prefix=<slug>-<ref-slug>/ <commit>` — mtimes
inside the zip come from the commit, so output is deterministic for a fixed commit.
Disabled entirely (`archives: []`) by `ingest.archives: false`.

### `LanguageStat` / language stats

Bytes per language summed over files that are: non-binary, have a detected language that
is *code* (Markdown, JSON, YAML, TOML, XML, INI, CSV, plain text, lockfiles, ignore files
and similar docs/config are excluded), and are not in a vendored location
(`node_modules/`, `vendor/`, `dist/`, `build/`, `.git/`, `*.min.*`, …). Unknown extensions
are not counted. `percent` is rounded to one decimal and the array sums to 100 (rounding
drift is folded into the largest entry). `color` is the language's hex colour or `null`.
Empty array when nothing counts.

### `Contributor`

Commits grouped by author email, lower-cased; `name` is the name used on the most recent
commit; `firstCommit`/`lastCommit` are author dates. Sorted by `commits` desc, then name.

### `License`

| field    | meaning                                                                                         |
| -------- | ----------------------------------------------------------------------------------------------- |
| `spdx`   | SPDX id if recognised (MIT, Apache-2.0, GPL-2.0-only, GPL-3.0-only, LGPL-2.1-only, LGPL-3.0-only, AGPL-3.0-only, BSD-2-Clause, BSD-3-Clause, MPL-2.0, ISC, Unlicense, CC0-1.0, 0BSD) else `null`. |
| `file`   | Root license file that was detected (`LICENSE`, `LICENSE.*`, `LICENCE*`, `COPYING*`, `UNLICENSE`, case-insensitive) or `null`. |
| `source` | `'config'` when the SPDX id came from metadata `license` (the detected file, if any, is still recorded), else `'file'`. |

`null` when there is neither a license file nor a config override.

### `Readme`

`{ path, sha, content }` — content decoded as UTF-8 and inlined (it is rendered on the
repo overview). Candidates are root-level only; `README.md` is preferred over `README`,
`README.txt`, and other `README.*`.

### `RepoSource`

Where the repo came from, discriminated on `type`. Remote variants arrived in schema v3.

| variant                        | fields                                                  |
| ------------------------------ | ------------------------------------------------------- |
| `local`                        | `path` — the absolute directory that was scanned.        |
| `github`                       | `host`, `owner`, `repo`, `webUrl`, `cloneUrl`            |
| `gitlab`                       | `host`, `project`, `webUrl`, `cloneUrl`                  |
| `gitea`                        | `host`, `owner`, `repo`, `webUrl`, `cloneUrl`            |
| `forgejo`                      | `host`, `owner`, `repo`, `webUrl`, `cloneUrl`            |

- `host` is the API base URL the importer talked to (`https://api.github.com` for github.com,
  the instance root for GitLab/Gitea/Forgejo). `webUrl` is the human-facing repo page;
  `cloneUrl` is what the clone panel shows and what the mirror was cloned from.
- GitLab identifies a project by its full namespaced `project` path (`group/sub/proj`)
  instead of owner + repo.
- `forgejo` is the same REST API as `gitea`; it is a separate variant purely so the UI and
  the docs can name Forgejo correctly.
- There is no `path` on remote variants. `repoSourceLabel(source)` returns something
  printable for every variant (the path for `local`, the `webUrl` otherwise), and
  `isRemoteRepoSource(source)` narrows to the non-local ones.
- Whatever the variant, the scanner ran against a local bare git repository — the directory
  for `local`, the mirror in `ingest.cacheDir` for the rest.

### `Release` / `ReleaseAsset` (schema v3)

One entry per release imported from a provider; only present when `releaseMode` is
`'provider'`.

| field         | meaning                                                                         |
| ------------- | --------------------------------------------------------------------------------- |
| `tag`         | Tag the release points at; matches a `Tag.name` when the mirror has that tag.     |
| `name`        | Display title; the importer falls back to the tag name when the provider has none. |
| `body`        | Release notes as markdown; `''` when there are none.                              |
| `url`         | Release page on the provider, or `null`.                                          |
| `prerelease`  | Provider's prerelease flag. Always `false` for GitLab, which has none (`upcoming_release` is clock-derived and would not be deterministic). |
| `publishedAt` | `IsoDate`, normalised to UTC seconds precision by the importer.                   |
| `author`      | Publisher's provider username/display name, or `null`.                            |
| `assets`      | `ReleaseAsset[]` — `{ name, url, size, contentType }`, in the provider's order.   |

`ReleaseAsset.url` and `Release.url` are plain strings rather than validated URLs: some
providers (GitLab release asset links) hand out host-relative paths, and rejecting one in
the schema would hard-fail a build over a remote's formatting choice. The importers resolve
such a URL against the repo's web URL before storing it, so what actually lands here is
absolute — otherwise the site would render it as a link into itself and 404.

**No download counts, ever.** They are the archetypal volatile counter — see "Guarantees".

The site reads releases through `resolveReleases(repo)` (`src/lib/routes.ts`), which returns
`SiteRelease[]`: provider releases when `repo.releases` is non-empty, otherwise the
annotated-tag derivation, newest first either way. `SiteRelease.source` (`'provider'` /
`'tag'`) tells the UI which it got, and `SiteRelease.commit` is the tag target or `null`.

### `Warning`

`{ code, repo, message }` — `repo` is the slug, or `null` for site-level warnings.

| code                        | level | meaning                                                                                  |
| --------------------------- | ----- | ---------------------------------------------------------------------------------------- |
| `repo-empty`                | repo  | No commits on any branch. `empty: true`.                                                 |
| `default-branch-empty-tree` | repo  | The default branch has commits but its HEAD tree has no entries (e.g. last commit deleted everything). |
| `default-branch-fallback`   | repo  | HEAD's branch is unborn (or HEAD is detached/unreadable) but other branches exist; one of them was used as the default. |
| `repo-meta-invalid`         | repo  | `.frznforge.json` exists in the tree but is not valid JSON / does not match `RepoMetaInput`; ignored. |
| `description-truncated`     | repo  | In-repo description exceeded 300 characters and was truncated.                           |
| `commits-capped`            | repo  | One or more branch commit lists were truncated to `ingest.maxCommits`.                   |
| `tag-trees-capped`          | repo  | More tags than `ingest.tagTrees`; only the newest N have browsable trees / archives ("N of M tags have browsable trees"). |
| `repo-not-found`            | site  | A configured path is not a git repository (or is a path inside one); the entry was skipped. |
| `slug-collision`            | site  | Two configured repos resolved to the same slug; the later one (config order) was suffixed. `repo` holds the new slug. |
| `remote-fetch-failed`       | repo  | A provider API call or mirror fetch failed (network error, bad response, unreachable host). The cached mirror was used if there is one, otherwise the repo was skipped. Never contains a token. |
| `remote-auth-missing`       | repo  | The provider answered 401/403/404 and no token was configured for that source — most likely a private repo. Names the env vars that were consulted, never a value. |
| `remote-rate-limited`       | repo  | The provider rate-limited the build. Cache used, or repo skipped.               |
| `remote-cache-stale`        | repo  | A previously fetched mirror was used without refreshing — `ingest.fetch: 'never'`, or a fetch that failed while a cache existed. |
| `notes-dir-missing`         | site  | A configured `notes.dir` does not exist or is not a directory. No notes were collected; the build continues and `/notes/` renders empty. Only raised when the config declares a `notes` block — a site that never opted into notes is not warned about the defaulted folder being absent. |
| `note-slug-collision`       | site  | Two notes slugified to the same value; the later one in walk order was suffixed `-2`, `-3`, … `repo` is `null`. |
| `note-file-unservable`      | site  | A note file's name contains `#` or `%`, which no static raw URL can round-trip (the build escapes `#` in the emitted filename, and `%` aborts the build outright). The file is still collected, stored and rendered inline; it just gets no `/notes/<slug>/raw/<path>` route and no Raw/Download link. `repo` is `null`. |
| `repo-path-unservable`     | repo  | A committed path (or a ref name) contains `#` or `%`, which no static URL can round-trip — same cause as `note-file-unservable`. The path is still ingested and still listed in the file table, but gets no `tree`/`blob`/`raw` route and is rendered unlinked. A ref whose name is affected loses the whole ref's file routes. |
| `org-unknown-repo`          | site  | An `organizations[].repos` entry names a slug no ingested repo has (typo, or the repo was removed/skipped). The entry is dropped from `Organization.repos`. |
| `repo-unknown-org`          | repo  | A repo source declares `org: '<slug>'` that is not in `organizations[]`. The repo joins no org. `repo` holds the repo's slug. |

### Path encoding in repo routes

`tree`, `blob` and `raw` URLs carry a committed path, which — like a note file name — is not
a slug: spaces, `&`, `+`, `;` and non-ASCII are all legal in git. Every segment is
percent-encoded (`docs/read me.md` → `/repos/x/blob/main/docs/read%20me.md/`) while `/` stays
literal, so the tree structure survives; the ref slug is encoded the same way (`~` is
unreserved, so ordinary refs are unchanged).

`#` and `%` survive no static round-trip — the build writes `c%23-tips.md` to disk, so even a
correctly encoded request misses, and a literal `%` is invalid percent-encoding that aborts
the build. Paths and refs holding either get no route at all: `isRawServable()` in
`src/lib/routes.ts` gates `treeRoutes`/`blobRoutes`/`rawRoutes`, ingest raises
`repo-path-unservable`, and the file table lists the entry unlinked rather than emitting a
dead href. This is the same rule the notes side applies — see `note-file-unservable`.

## Notes (schema v4)

Gist-style snippets. They come from a plain folder on disk (`notes.dir`, default
`./content/notes`), not from git — see "Committed content only" above for why that is not a
rule violation.

### Folder convention

```
content/notes/
├── ripgrep-cheatsheet.md      → Note { kind: 'file',   files: [ripgrep-cheatsheet.md] }
├── xdg-basedirs.txt           → Note { kind: 'file',   files: [xdg-basedirs.txt] }
├── dotfiles/                  → Note { kind: 'folder', files: [index.md, bin/setup.sh, …] }
│   ├── index.md                 frontmatter + title are read from here
│   └── bin/setup.sh             NoteFile.path = "bin/setup.sh"
├── .drafts/                   → ignored
└── _wip.md                    → ignored
```

- A **file** directly in `notes.dir` is a single-file note. A **sub-folder** is a multi-file
  note holding every file underneath it, recursively; `NoteFile.path` is relative to the note
  root and uses forward slashes on every platform.
- Anything whose name starts with `.` or `_` is skipped, at any depth. There is no special
  README convention beyond the frontmatter lookup below — a folder's `README.md` is an
  ordinary note file that happens to be where frontmatter is read from.
- Only files are notes; there is nothing to collect from an empty folder, so it is skipped.

### Frontmatter, title and date

Markdown notes may carry YAML frontmatter with `title`, `description`, `date` and `tags`. For
a folder note it is read from `index.md`, else `README.md`, inside the folder; other files'
frontmatter is left alone (it is part of their content).

| field         | resolution                                                                                      |
| ------------- | ------------------------------------------------------------------------------------------------ |
| `title`       | frontmatter `title` → the first `# H1` in the (first) markdown file → the file/folder name humanised (`xdg-basedirs` → "Xdg basedirs"). |
| `description` | frontmatter `description`, else `null`.                                                          |
| `tags`        | frontmatter `tags[]`, else `[]`.                                                                 |
| `date`        | frontmatter `date`, normalised to `IsoDate` (UTC, seconds precision). Two shapes are accepted: `YYYY-MM-DD` (→ midnight UTC), and `YYYY-MM-DD` plus a time separated by `T` or a space, with or without a `Z`/`±HH:MM` zone. **A missing zone means UTC, not the build machine's zone** — `Date.parse` reads a bare date-time as local time, which would give the same frontmatter a different date, a different note order and a different `forge.json` on every machine. Anything else (`March 4, 2026`, `03/04/2026`, RFC 2822) is dropped for that same reason. With `notes.useMtime: true`, an undated note falls back to the file's mtime. Otherwise `null`. |
| `slug`        | the file/folder name with the extension dropped, slugified. Collisions are suffixed `-2`, `-3`, … in walk order, with a `note-slug-collision` warning. |

**`notes.useMtime` is the artifact's only non-deterministic input.** Off by default; see
"Guarantees".

### `Note`

| field        | meaning                                                                            |
| ------------ | ------------------------------------------------------------------------------------ |
| `slug`       | URL slug — `/notes/<slug>/`.                                                        |
| `title`      | Resolved as above.                                                                  |
| `description`| `string \| null`.                                                                   |
| `tags`       | Free-form tags from frontmatter; drive the filters on `/notes/`.                    |
| `date`       | `IsoDate \| null`.                                                                  |
| `kind`       | `'file'` (a file directly in `notes.dir`) or `'folder'`.                             |
| `files`      | `NoteFile[]`, sorted by path. Exactly one entry when `kind` is `'file'`.            |
| `totalBytes` | Sum of `files[].size`.                                                              |

### `NoteFile`

Same shape as `FileInfo` plus `name` and `markdown`, so the note viewer reuses the repo file
viewer's rendering (binary fallback, "too large" fallback, Shiki language) unchanged.

| field      | meaning                                                                            |
| ---------- | ------------------------------------------------------------------------------------ |
| `name`     | Last path segment.                                                                  |
| `path`     | Path relative to the note root, forward slashes. Equals `name` for a single-file note. |
| `sha`      | `sha1('note <len>\0' + bytes)` — the `blobs/` key when `stored`. **Not** a git blob id; see "Layout on disk". |
| `size`     | Bytes.                                                                              |
| `binary`   | A NUL byte occurs in the first 8000 bytes (same heuristic as repo files).           |
| `tooLarge` | `size > notes.maxFileBytes` (which defaults to `ingest.maxBlobBytes`).              |
| `stored`   | Content was written to `blobs/<sha>`. Necessary but not sufficient for a raw route — a path holding `#` or `%` gets none either (`note-file-unservable`). |
| `language` | From the same extension/filename map as repo files, or `null`.                      |
| `markdown` | The viewer offers the preview/source toggle for these.                              |

### Routes

`/notes/` (index, always built), `/notes/<slug>/`, and `/notes/<slug>/raw/<file-path>` for
every stored file whose path `isRawServable()` accepts. Derived by `notesRoutes(data)` in
`src/lib/routes.ts`.

Note file names are authored by hand rather than slugified, so each raw-URL segment is
percent-encoded — `read me.md` → `/notes/n/raw/read%20me.md` — while `/` stays literal so the
folder structure survives. `#` and `%` have no encoding that survives a static build and are
excluded; see `note-file-unservable`.

## Organizations (schema v4)

A named grouping of repos with its own overview page. Configured in `frznforge.config.ts`;
prose lives in an optional markdown file.

### Membership

The union of two directions, so either alone is enough and both together is a no-op:

1. `organizations[].repos` — the org lists the repo slugs it contains.
2. `repos[].org` — a repo source declares which org it belongs to.

Resolution runs **after** slug-collision renaming, so membership always names the slug that
actually reached the artifact. Dangling references never fail a build: an org naming a repo
that does not exist raises `org-unknown-repo` and the entry is dropped; a repo naming an org
that is not configured raises `repo-unknown-org` and the repo joins no org.

### `Organization`

| field         | meaning                                                                           |
| ------------- | ----------------------------------------------------------------------------------- |
| `slug`        | URL slug and the value repo sources put in their `org` field.                       |
| `name`        | Display name, from config.                                                          |
| `description` | Config `description`, or `null`. The markdown file's frontmatter `description` wins at render time. |
| `repos`       | Member repo slugs, sorted and de-duplicated; every one exists in `ForgeData.repos`. |

An org with no members is still emitted — it has a page either way.

### `<content.orgs>/<org-slug>.md`

Optional, exactly the `content/profile.md` mechanism: an Astro content collection (`orgs` in
`src/content.config.ts`) whose entry `id` is the filename without the extension — i.e. the org
slug. Frontmatter is `{ description?, sites: url[] = [], links?: Record<string, url>,
pinned: string[] = [] }` and the body is rendered on the overview. Missing file = the org
still gets a page, built from config plus its member repos.

### Routes

`/orgs/` (index, always built), `/orgs/<slug>/` (overview) and `/orgs/<slug>/repos/` (the full
repo listing scoped to the org, reusing the Phase 2 listing island). Derived by
`orgRoutes(data)` in `src/lib/routes.ts`.

## Per-repo metadata: `.frznforge.json`

Read from the default branch's HEAD tree (`<branch>:.frznforge.json`), never from disk.
Shape is `RepoMetaInput`: `{ name?, description?, links?, tags?, template?, license?,
releaseMode? }`. The site config's `overrides` for that repo win field-by-field.

## Version history

- **v4** — notes and organizations: `ForgeData.notes` (`Note[]` with `NoteFile[]`) and
  `ForgeData.organizations` (`Organization[]`), both declared after `repos` and before
  `warnings`; new warnings `notes-dir-missing`, `note-slug-collision`, `org-unknown-repo`,
  `repo-unknown-org`; new config `notes.{dir,useMtime,maxFileBytes}`, `organizations[]`,
  `content.orgs`, and `org?` on every repo source; note content shares the existing `blobs/`
  store, keyed by `sha1('note <len>\0' + bytes)` so a note key can never collide with a git
  object id. Fifth new warning: `note-file-unservable`; sixth, `repo-path-unservable` (the
  same `#`/`%` rule applied to committed repo paths and ref names — see "Path encoding in
  repo routes"). Both were added additively inside v4 rather than bumping: they widen the
  `WarningCode` enum only, and `data/` is regenerated from scratch on every build.
- **v3** — remote sources: `RepoSource` gains `github` / `gitlab` / `gitea` / `forgejo`
  variants (`host`, `owner`+`repo` or `project`, `webUrl`, `cloneUrl`); `Repo.releases`
  (`Release[]`, with `ReleaseAsset[]`); `Repo.releaseMode` widened to `'tags' | 'provider'`;
  new warnings `remote-fetch-failed`, `remote-auth-missing`, `remote-rate-limited`,
  `remote-cache-stale`; new config `ingest.cacheDir` + `ingest.fetch` and the mirror cache
  layout described above.
- **v2** — `Commit.files` + `Commit.stats` (numstat vs first parent; merges empty),
  `Repo.refTrees` (non-default branches + newest `ingest.tagTrees` tags),
  `Repo.archives` + the `archives/` directory (`git archive` zips, `ingest.archives`),
  `FileInfo.stored` now also true for binary files within `maxBlobBytes`, new warning
  `tag-trees-capped`.
- **v1** — initial schema.

## Bumping `SCHEMA_VERSION`

Any change to `src/lib/data/schema.ts` that changes the emitted JSON (new/removed/renamed
field, changed meaning, changed ordering rule) must, in the same change:

1. Bump `SCHEMA_VERSION` (the site only accepts the exact literal, so an old `data/` will
   be rejected until `npm run ingest` is re-run — that is intended).
2. Update this document.
3. Update the snapshot in `tests/unit/__snapshots__/ingest.test.ts.snap`
   (`npx vitest run -u tests/unit/ingest.test.ts`) and adjust the unit tests for the
   extractor involved.
4. Add a `CHANGELOG.md` entry.

Purely additive *internal* changes (a new warning code, a new language in the map) do not
change the shape and do not need a bump, but still update this document's tables.
