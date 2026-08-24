/**
 * frznforge data model — the contract between ingest (git → JSON) and the site.
 *
 * Everything the site renders comes from a `ForgeData` artifact written by ingest to
 * `<ingest.outDir>/forge.json` plus a content-addressed blob store in
 * `<ingest.outDir>/blobs/<sha>` for file contents (text files under the size cap).
 *
 * Rules (see docs/dev/plans/plan-phases.md, "Cross-cutting rules"):
 *  - Any change to this file bumps SCHEMA_VERSION, updates docs/dev/data-model.md, and
 *    updates the snapshot fixtures + sync tests in the same change.
 *  - The artifact is deterministic: same repos at the same commits → byte-identical JSON.
 *    Nothing in here is a wall-clock timestamp; all dates come from git.
 *  - Only committed content is ever represented for *repositories*. Ingest never reads a
 *    repository's working tree. Notes (schema v4) are the one deliberate exception: they are
 *    a plain folder on disk, not a git repo, so reading them from disk *is* the source of
 *    truth. The rule is about git repos, not about the filesystem.
 */
import { z } from 'astro/zod';

export const SCHEMA_VERSION = 4 as const;

/* ---- primitives -------------------------------------------------------- */

/** 40-char lowercase hex git object id. */
export const Sha = z.string().regex(/^[0-9a-f]{40}$/);
/** ISO-8601 UTC timestamp, e.g. "2026-08-23T14:02:11Z" (seconds precision, from git). */
export const IsoDate = z.string().regex(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/);
/** URL-safe slug: lowercase, digits, dashes. */
export const Slug = z.string().regex(/^[a-z0-9][a-z0-9-]*$/);

/* ---- repo metadata (from .frznforge.json in the repo + site config overrides) ---- */

export const RepoLinks = z.object({
  homepage: z.url().optional(),
  issues: z.url().optional(),
  donations: z.url().optional(),
  upstream: z.url().optional(),
});
export type RepoLinks = z.infer<typeof RepoLinks>;

/**
 * Shape of a repo's own `.frznforge.json` (read from the default branch HEAD tree, never
 * the working copy) and of `overrides` in the site config. All fields optional; the site
 * config wins over the in-repo file.
 */
export const RepoMetaInput = z.object({
  name: z.string().min(1).optional(),
  description: z.string().max(300).optional(),
  links: RepoLinks.optional(),
  tags: z.array(z.string().min(1)).optional(),
  template: z.boolean().optional(),
  /** SPDX id override, e.g. "MIT". When absent, ingest detects from a LICENSE file. */
  license: z.string().min(1).optional(),
  /**
   * How releases are produced: `'tags'` derives them from annotated git tags,
   * `'provider'` imports them from the hosting forge's API (schema v3, remote sources only).
   */
  releaseMode: z.enum(['tags', 'provider']).optional(),
});
export type RepoMetaInput = z.infer<typeof RepoMetaInput>;

/* ---- git objects ------------------------------------------------------- */

export const Person = z.object({
  name: z.string(),
  email: z.string(),
});
export type Person = z.infer<typeof Person>;

export const CommitFileChange = z.object({
  /** Path after the change (rename target). */
  path: z.string(),
  /** Added lines; null for binary files. */
  additions: z.number().int().nonnegative().nullable(),
  /** Deleted lines; null for binary files. */
  deletions: z.number().int().nonnegative().nullable(),
});
export type CommitFileChange = z.infer<typeof CommitFileChange>;

export const Commit = z.object({
  sha: Sha,
  parents: z.array(Sha),
  author: Person,
  authorDate: IsoDate,
  committer: Person,
  commitDate: IsoDate,
  /** First line of the message. */
  subject: z.string(),
  /** Everything after the first blank line, trimmed. Empty string if none. */
  body: z.string(),
  /** Files touched by this commit (vs first parent), with numstat line counts. */
  files: z.array(CommitFileChange),
  /** Sums over `files` (binary files count toward filesChanged only). */
  stats: z.object({
    filesChanged: z.number().int().nonnegative(),
    additions: z.number().int().nonnegative(),
    deletions: z.number().int().nonnegative(),
  }),
});
export type Commit = z.infer<typeof Commit>;

export const Branch = z.object({
  name: z.string(),
  head: Sha,
  /** Commit shas reachable from `head`, newest first (topological, as `git log`). */
  commits: z.array(Sha),
  lastCommitDate: IsoDate,
});
export type Branch = z.infer<typeof Branch>;

export const Tag = z.object({
  name: z.string(),
  /** The commit the tag points at (peeled). */
  target: Sha,
  annotated: z.boolean(),
  /** Annotated tag message (trimmed), or null for lightweight tags. */
  message: z.string().nullable(),
  tagger: Person.nullable(),
  /** Tagger date for annotated tags, else the target commit's commit date. */
  date: IsoDate,
});
export type Tag = z.infer<typeof Tag>;

export const TreeEntry = z.object({
  /** Full path from repo root, forward slashes, no leading slash. */
  path: z.string(),
  name: z.string(),
  type: z.enum(['blob', 'tree', 'commit' /* submodule */, 'symlink']),
  /** Git mode string as reported by ls-tree, e.g. "100644". */
  mode: z.string(),
  /** Blob object id for blobs/symlinks; tree id for trees; commit id for submodules. */
  sha: Sha,
  /** Bytes; null for trees and submodules. */
  size: z.number().int().nonnegative().nullable(),
  /** Sha of the most recent commit (on the default branch) that touched this path. */
  lastCommit: Sha,
});
export type TreeEntry = z.infer<typeof TreeEntry>;

export const FileInfo = z.object({
  path: z.string(),
  /** Blob object id — also the key into the blob store when `stored` is true. */
  sha: Sha,
  size: z.number().int().nonnegative(),
  binary: z.boolean(),
  /** Over `ingest.maxBlobBytes`; content not stored. */
  tooLarge: z.boolean(),
  /** Content was written to `<outDir>/blobs/<sha>` (any file within the size cap, binary included since schema v2). */
  stored: z.boolean(),
  /** Detected language name (from extension/filename map) or null. */
  language: z.string().nullable(),
});
export type FileInfo = z.infer<typeof FileInfo>;

export const LanguageStat = z.object({
  name: z.string(),
  bytes: z.number().int().nonnegative(),
  /** 0–100, rounded to one decimal; sums to ~100. */
  percent: z.number(),
  /** Hex colour for bars, from the language map (null → UI neutral). */
  color: z.string().nullable(),
});
export type LanguageStat = z.infer<typeof LanguageStat>;

export const Contributor = z.object({
  name: z.string(),
  email: z.string(),
  commits: z.number().int().positive(),
  firstCommit: IsoDate,
  lastCommit: IsoDate,
});
export type Contributor = z.infer<typeof Contributor>;

export const License = z.object({
  /** SPDX id if known (e.g. "MIT", "Apache-2.0", "GPL-3.0-only"), else null. */
  spdx: z.string().nullable(),
  /** Path of the license file in the repo, if detected from a file. */
  file: z.string().nullable(),
  source: z.enum(['config', 'file']),
});
export type License = z.infer<typeof License>;

export const Readme = z.object({
  path: z.string(),
  sha: Sha,
  /** Raw markdown/text content. Inlined because it is rendered on the repo overview. */
  content: z.string(),
});
export type Readme = z.infer<typeof Readme>;

/* ---- releases (schema v3) ------------------------------------------------ */

/**
 * A downloadable file attached to a provider release.
 *
 * `url` is a plain string, not a validated URL: some providers (GitLab release asset
 * links) hand out host-relative paths, and a schema rejection there would hard-fail the
 * build. Importers should emit an absolute URL whenever the API gives them one.
 *
 * Nothing volatile lives here — download counters are deliberately NOT imported, because
 * they would change the artifact between two builds of the same commits.
 */
export const ReleaseAsset = z.object({
  name: z.string(),
  url: z.string(),
  size: z.number().int().nonnegative(),
  /** MIME type reported by the provider, or null when it reports none. */
  contentType: z.string().nullable(),
});
export type ReleaseAsset = z.infer<typeof ReleaseAsset>;

/**
 * A release imported from a hosting provider (`Repo.releaseMode === 'provider'`).
 *
 * Tag-mode repos have `Repo.releases === []` and the site derives releases from annotated
 * tags instead (see `resolveReleases` in src/lib/routes.ts).
 *
 * `publishedAt` must be normalised to `IsoDate` (UTC, seconds precision) by the importer —
 * providers return varying precision and offsets, and the artifact has to be deterministic.
 */
export const Release = z.object({
  /** Tag name the release points at; matches a `Tag.name` when the tag exists locally. */
  tag: z.string(),
  /** Display title. Importers fall back to the tag name when the provider has no title. */
  name: z.string(),
  /** Release notes as markdown (rendered by the site). Empty string when there are none. */
  body: z.string(),
  /** Release page on the provider, or null. Plain string for the same reason as `ReleaseAsset.url`. */
  url: z.string().nullable(),
  prerelease: z.boolean(),
  publishedAt: IsoDate,
  /** Provider username/display name of the publisher, or null. */
  author: z.string().nullable(),
  assets: z.array(ReleaseAsset),
});
export type Release = z.infer<typeof Release>;

/* ---- warnings ----------------------------------------------------------- */

export const WarningCode = z.enum([
  /** Repo has no commits at all on any branch. */
  'repo-empty',
  /** Default branch HEAD tree has no files (e.g. last commit deleted everything). */
  'default-branch-empty-tree',
  /** HEAD branch is unborn/empty but another branch has commits; that branch was used. */
  'default-branch-fallback',
  /** `.frznforge.json` exists but failed to parse/validate; ignored. */
  'repo-meta-invalid',
  /** Description exceeded 300 chars and was truncated. */
  'description-truncated',
  /** A configured local path is not a git repository; repo skipped. */
  'repo-not-found',
  /** Two repos resolved to the same slug; the later one was suffixed. */
  'slug-collision',
  /** Commit list was capped by `ingest.maxCommits`. */
  'commits-capped',
  /** More tags than `ingest.tagTrees`; older tags have no browsable tree / archive. */
  'tag-trees-capped',
  /** A provider API call or mirror fetch failed (network/API error); cache used or repo skipped. */
  'remote-fetch-failed',
  /** The provider answered 401/403/404 and no token was configured for that source. */
  'remote-auth-missing',
  /** The provider rate-limited us; cache used or repo skipped. */
  'remote-rate-limited',
  /** A previously fetched mirror/cache was used without refreshing (`ingest.fetch: 'never'` or a failed fetch). */
  'remote-cache-stale',
  /** Two notes resolved to the same slug; the later one (walk order) was suffixed (schema v4). */
  'note-slug-collision',
  /** `notes.dir` does not exist or is not a directory; no notes were collected (schema v4). */
  'notes-dir-missing',
  /**
   * A note file's name contains `#` or `%`, which a static raw URL cannot round-trip; the file
   * still renders on the note page but gets no raw/download route (schema v4).
   */
  'note-file-unservable',
  /** A committed repo path or ref name holds `#`/`%`; no static URL can reach it, so it gets no page. */
  'repo-path-unservable',
  /** An `organizations[].repos` entry names a slug no ingested repo has; ignored (schema v4). */
  'org-unknown-repo',
  /** A repo source declares `org` naming an organization that is not configured; ignored (schema v4). */
  'repo-unknown-org',
]);
export type WarningCode = z.infer<typeof WarningCode>;

export const Warning = z.object({
  code: WarningCode,
  /** Repo slug, or null for site-level warnings. */
  repo: z.string().nullable(),
  message: z.string(),
});
export type Warning = z.infer<typeof Warning>;

/* ---- per-ref trees & archives (schema v2) -------------------------------- */

/** Browsable tree of a non-default ref (branches always; the newest `ingest.tagTrees` tags). */
export const RefTree = z.object({
  kind: z.enum(['branch', 'tag']),
  name: z.string(),
  /** The commit the ref peels to. */
  commit: Sha,
  tree: z.array(TreeEntry),
  files: z.record(z.string(), FileInfo),
});
export type RefTree = z.infer<typeof RefTree>;

/** A source archive produced at ingest time with `git archive` (zip). */
export const Archive = z.object({
  ref: z.string(),
  kind: z.enum(['branch', 'tag']),
  commit: Sha,
  /** Path relative to the ingest outDir, e.g. "archives/<slug>/<ref-slug>.zip". */
  file: z.string(),
  bytes: z.number().int().nonnegative(),
});
export type Archive = z.infer<typeof Archive>;

/* ---- repo -------------------------------------------------------------- */

/**
 * Where the scanned repository came from.
 *
 * `local` is a directory on disk. The four remote variants (schema v3) describe a repo that
 * was imported over a provider API and then mirror-cloned into the ingest cache; the scanner
 * still runs on a bare git mirror, so everything downstream of `source` is identical.
 *
 * `host` is the API base URL the importer talked to (`https://api.github.com` for
 * github.com), `webUrl` is the human-facing repo page and `cloneUrl` the URL a visitor can
 * `git clone`. GitLab identifies a repo by its full namespaced `project` path
 * (`group/sub/proj`) rather than owner + repo.
 *
 * Note there is no `path` on remote sources: use `repoSourceLabel(source)` when you just
 * need something printable.
 */
export const RepoSource = z.discriminatedUnion('type', [
  z.object({ type: z.literal('local'), path: z.string() }),
  z.object({
    type: z.literal('github'),
    host: z.url(),
    owner: z.string(),
    repo: z.string(),
    webUrl: z.url(),
    cloneUrl: z.url(),
  }),
  z.object({
    type: z.literal('gitlab'),
    host: z.url(),
    /** Full namespaced project path, e.g. "group/sub/proj". */
    project: z.string(),
    webUrl: z.url(),
    cloneUrl: z.url(),
  }),
  z.object({
    type: z.literal('gitea'),
    host: z.url(),
    owner: z.string(),
    repo: z.string(),
    webUrl: z.url(),
    cloneUrl: z.url(),
  }),
  z.object({
    type: z.literal('forgejo'),
    host: z.url(),
    owner: z.string(),
    repo: z.string(),
    webUrl: z.url(),
    cloneUrl: z.url(),
  }),
]);
export type RepoSource = z.infer<typeof RepoSource>;

/** Every non-`local` `RepoSource.type`. */
export type RemoteProvider = Exclude<RepoSource['type'], 'local'>;

/** A `RepoSource` that came from a hosting provider rather than a local directory. */
export type RemoteRepoSource = Extract<RepoSource, { type: RemoteProvider }>;

/** True for provider-imported sources (everything except `local`). */
export function isRemoteRepoSource(source: RepoSource): source is RemoteRepoSource {
  return source.type !== 'local';
}

/**
 * A short human-readable identifier for a source: the scanned path for local repos, the
 * web URL for remote ones. Use it in warnings and log lines so they work for every variant.
 */
export function repoSourceLabel(source: RepoSource): string {
  return source.type === 'local' ? source.path : source.webUrl;
}

export const Repo = z.object({
  slug: Slug,
  name: z.string(),
  description: z.string().max(300).nullable(),
  source: RepoSource,
  links: RepoLinks,
  tags: z.array(z.string()),
  template: z.boolean(),
  license: License.nullable(),
  /** `'tags'` = releases derived from annotated tags; `'provider'` = imported (schema v3). */
  releaseMode: z.enum(['tags', 'provider']),
  /**
   * Provider-imported releases, newest first (`publishedAt` desc, `tag` asc as tiebreak).
   * Always `[]` when `releaseMode` is `'tags'` — the site then falls back to annotated tags.
   */
  releases: z.array(Release),

  /** True when the repo has no commits on any branch. Most other fields are then empty/null. */
  empty: z.boolean(),
  defaultBranch: z.string().nullable(),
  branches: z.array(Branch),
  /** Git tags (named `gitTags` to avoid clashing with the metadata `tags`). */
  gitTags: z.array(Tag),
  /** Every commit reachable from any branch, keyed by sha. */
  commits: z.record(Sha, Commit),
  commitCount: z.number().int().nonnegative(),
  /** Flat listing of the default-branch HEAD tree (all depths), sorted by path. */
  tree: z.array(TreeEntry),
  /** Per-file info for every blob in `tree`, keyed by path. */
  files: z.record(z.string(), FileInfo),
  /** Trees of NON-default refs (all branches; the newest `ingest.tagTrees` tags), keyed by ref name. */
  refTrees: z.record(z.string(), RefTree),
  /** Source archives (default branch + tags with trees), produced with `git archive`. */
  archives: z.array(Archive),
  languages: z.array(LanguageStat),
  contributors: z.array(Contributor),
  readme: Readme.nullable(),
  /** Date of the first commit on any branch; null when empty. */
  createdAt: IsoDate.nullable(),
  /** Date of the most recent commit on any branch; null when empty. */
  updatedAt: IsoDate.nullable(),
  warnings: z.array(Warning),
});
export type Repo = z.infer<typeof Repo>;

/* ---- notes (schema v4) --------------------------------------------------- */

/**
 * One file belonging to a note.
 *
 * Deliberately the same shape as `FileInfo` plus `name` and `markdown`, so the note viewer
 * can reuse the repo file viewer's rendering (binary fallback, "too large" fallback, Shiki
 * language) without a translation layer.
 *
 * `sha` is the sha1 of the file's raw bytes and is the key into the same content-addressed
 * blob store as repo files (`<outDir>/blobs/<sha>`). That is NOT a git blob object id — git
 * hashes a `blob <len>\0` header first — so a note and a repo file with identical content
 * are stored twice. Accepted: notes are few, and the alternative is faking git object ids
 * for content that never was in git.
 */
export const NoteFile = z.object({
  /** Last path segment, e.g. "snippet.py". */
  name: z.string(),
  /** Path relative to the note root, forward slashes, no leading slash. For a single-file note this equals `name`. */
  path: z.string(),
  /** sha1 of the raw bytes — the blob store key when `stored` is true. */
  sha: Sha,
  size: z.number().int().nonnegative(),
  binary: z.boolean(),
  /** Over `notes.maxFileBytes` (defaults to `ingest.maxBlobBytes`); content not stored. */
  tooLarge: z.boolean(),
  /** Content was written to `<outDir>/blobs/<sha>`. */
  stored: z.boolean(),
  /** Detected language name (same extension/filename map as repo files) or null. */
  language: z.string().nullable(),
  /** True for markdown files — the viewer offers the preview/source toggle for these. */
  markdown: z.boolean(),
});
export type NoteFile = z.infer<typeof NoteFile>;

/**
 * A gist-style note: one file, or one folder of files, under `notes.dir`.
 *
 * `date` is `IsoDate` like everything else in the artifact, so a `YYYY-MM-DD` frontmatter
 * value is normalised to midnight UTC (`2026-08-23` → `2026-08-23T00:00:00Z`). It comes from
 * frontmatter only unless `notes.useMtime` is set — filesystem mtimes are not reproducible
 * across checkouts, so opting into them opts out of a byte-identical artifact.
 */
export const Note = z.object({
  /** URL slug: the file/folder name (extension dropped) slugified. Collisions are suffixed `-2`, `-3`, … */
  slug: Slug,
  /** Frontmatter `title`, else the first H1 of the first markdown file, else the humanised name. */
  title: z.string(),
  description: z.string().nullable(),
  tags: z.array(z.string()),
  /** Frontmatter date (normalised to UTC), or the mtime when `notes.useMtime` is on, else null. */
  date: IsoDate.nullable(),
  /** `'file'` = a single file directly in `notes.dir`; `'folder'` = a sub-folder and everything under it. */
  kind: z.enum(['file', 'folder']),
  /** Every file in the note, sorted by path. Exactly one entry when `kind` is `'file'`. */
  files: z.array(NoteFile),
  /** Sum of `files[].size`. */
  totalBytes: z.number().int().nonnegative(),
});
export type Note = z.infer<typeof Note>;

/* ---- organizations (schema v4) ------------------------------------------- */

/**
 * A named grouping of repos with its own overview page.
 *
 * Membership is the union of two directions: the org's own `repos` list in the site config,
 * and every repo source that declares `org: '<slug>'`. Dangling references on either side are
 * warnings (`org-unknown-repo`, `repo-unknown-org`), never build failures.
 */
export const Organization = z.object({
  slug: Slug,
  name: z.string(),
  description: z.string().nullable(),
  /** Member repo slugs, sorted, de-duplicated; every one of them exists in `ForgeData.repos`. */
  repos: z.array(Slug),
});
export type Organization = z.infer<typeof Organization>;

/* ---- artifact ---------------------------------------------------------- */

/**
 * The artifact. **Key order is part of the contract** — `serializeForgeData` writes the
 * object in declaration order, so the fields below are emitted as
 * `schemaVersion`, `repos`, `notes`, `organizations`, `warnings`. `notes` and `organizations`
 * were added after `repos` and before `warnings` in schema v4; do not reorder them.
 */
export const ForgeData = z.object({
  schemaVersion: z.literal(SCHEMA_VERSION),
  /** Sorted by slug. */
  repos: z.array(Repo),
  /** Notes (schema v4), ordered by `date` desc with null dates last, then `title` asc. */
  notes: z.array(Note),
  /** Organizations (schema v4), sorted by slug. */
  organizations: z.array(Organization),
  /** Site-level warnings (repo-level ones are also mirrored here with `repo` set). */
  warnings: z.array(Warning),
});
export type ForgeData = z.infer<typeof ForgeData>;

/** Validate an unknown value as a ForgeData artifact; throws a ZodError with details. */
export function parseForgeData(value: unknown): ForgeData {
  return ForgeData.parse(value);
}

/** Empty artifact — what the site builds from when no repos are configured. */
export function emptyForgeData(): ForgeData {
  return { schemaVersion: SCHEMA_VERSION, repos: [], notes: [], organizations: [], warnings: [] };
}

/**
 * Compare two notes for artifact order: newest first, notes without a date last, ties broken
 * by title then slug (code-point order — never `localeCompare`, which depends on the build
 * machine's ICU data). Exported so ingest and the site sort identically.
 */
export function compareNotes(a: Note, b: Note): number {
  if (a.date !== b.date) {
    if (a.date === null) return 1;
    if (b.date === null) return -1;
    return a.date < b.date ? 1 : -1;
  }
  if (a.title !== b.title) return a.title < b.title ? -1 : 1;
  return a.slug < b.slug ? -1 : a.slug > b.slug ? 1 : 0;
}
