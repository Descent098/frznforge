# Data model (ingest artifact)

`npm run ingest` turns the local repositories listed in `frznforge.config.ts` into one JSON
artifact plus a blob store. Every page of the site is built from this artifact and nothing
else — the site never talks to git.

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
  `readBlob(outDir, sha)` returns it as UTF-8 text.
- `archives/<slug>/<ref-slug>.zip` are produced with `git archive --format=zip <commit>`
  (committed content only, never the working tree). `<ref-slug>` is the ref name with every
  `/` replaced by `~` (git refnames can never contain `~`, so this is collision-free), e.g.
  tag `rel/1.0` → `rel~1.0.zip`. The prefix inside the zip is `<slug>-<ref-slug>/`.
- `writeArtifact(data, blobs, archives, outDir)` makes `blobs/` and `archives/` mirror the
  current artifact exactly: missing or size-mismatched files are (re)written and any file
  not referenced by the current run is deleted.

## Guarantees

**Deterministic.** The same repositories at the same commits produce a byte-identical
`forge.json` and the same blob set, regardless of machine, clock or scan order:

- No wall-clock timestamps; every date comes from git and is normalised to UTC.
- Object keys are emitted in the order declared in the schema; `commits` keys (shas) and
  `files` keys (paths) are inserted in sorted order.
- Arrays have a defined order: `repos` by slug; `tree` by path; `branches` by name;
  `gitTags` by name; `contributors` by commit count desc, then name, then email;
  `languages` by bytes desc, then name; `Branch.commits` newest-first (topological).
- Warnings are collected in a fixed order (site-level first, then per repo in slug order).

**Committed content only.** Ingest never reads the working tree or the index. It uses
only git plumbing that reads committed objects (`for-each-ref`, `rev-list`, `log`,
`ls-tree`, `cat-file`, `rev-parse`). Untracked files, unstaged modifications and staged
but uncommitted changes are invisible; bare repositories work the same as checkouts.

**Never fails on odd repositories.** Empty repos, repos whose HEAD tree is empty, unborn
default branches, missing metadata files, etc. are reported as warnings; the repo entry is
still emitted (and valid) and the build continues. Only hard problems (git missing,
unwritable output dir, invalid site config) fail `npm run ingest`.

## `ForgeData` (top level)

| field           | meaning                                                                          |
| --------------- | -------------------------------------------------------------------------------- |
| `schemaVersion` | Literal `SCHEMA_VERSION` (currently `2`). The site refuses artifacts of another version. |
| `repos`         | `Repo[]`, sorted by slug.                                                        |
| `warnings`      | `Warning[]` — site-level warnings plus a mirror of every repo's own warnings (with `repo` set). |

## `Repo`

Identity + metadata (merged: site-config `overrides` > in-repo `.frznforge.json` > defaults):

| field         | meaning                                                                                                |
| ------------- | ------------------------------------------------------------------------------------------------------ |
| `slug`        | URL slug. From config `slug`, else the repo directory name slugified (`My_Repo` → `my-repo`). Collisions are suffixed `-2`, `-3`, … |
| `name`        | Display name. Default: directory basename (`.git` suffix dropped for bare repos).                       |
| `description` | ≤ 300 chars or `null`. In-repo values longer than 300 are truncated to 297 + `…` (warning). Over-long config overrides are a config error. |
| `source`      | `{ type: 'local', path }` — absolute path that was scanned.                                            |
| `links`       | `{ homepage?, issues?, donations?, upstream? }` URLs.                                                   |
| `tags`        | Free-form topic tags (metadata, not git tags).                                                          |
| `template`    | Repo is a template.                                                                                    |
| `license`     | `License \| null` — see below.                                                                         |
| `releaseMode` | `'tags'` (only mode in Phase 1).                                                                       |

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

## Per-repo metadata: `.frznforge.json`

Read from the default branch's HEAD tree (`<branch>:.frznforge.json`), never from disk.
Shape is `RepoMetaInput`: `{ name?, description?, links?, tags?, template?, license?,
releaseMode? }`. The site config's `overrides` for that repo win field-by-field.

## Version history

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
