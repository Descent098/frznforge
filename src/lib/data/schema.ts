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
 *  - Only committed content is ever represented. Ingest never reads the working tree.
 */
import { z } from 'astro/zod';

export const SCHEMA_VERSION = 1 as const;

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
  /** How releases are produced. Phase 1 supports only 'tags'. */
  releaseMode: z.enum(['tags']).optional(),
});
export type RepoMetaInput = z.infer<typeof RepoMetaInput>;

/* ---- git objects ------------------------------------------------------- */

export const Person = z.object({
  name: z.string(),
  email: z.string(),
});
export type Person = z.infer<typeof Person>;

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
  /** Content was written to `<outDir>/blobs/<sha>` (text, within size cap). */
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
]);
export type WarningCode = z.infer<typeof WarningCode>;

export const Warning = z.object({
  code: WarningCode,
  /** Repo slug, or null for site-level warnings. */
  repo: z.string().nullable(),
  message: z.string(),
});
export type Warning = z.infer<typeof Warning>;

/* ---- repo -------------------------------------------------------------- */

export const RepoSource = z.discriminatedUnion('type', [
  z.object({ type: z.literal('local'), path: z.string() }),
]);
export type RepoSource = z.infer<typeof RepoSource>;

export const Repo = z.object({
  slug: Slug,
  name: z.string(),
  description: z.string().max(300).nullable(),
  source: RepoSource,
  links: RepoLinks,
  tags: z.array(z.string()),
  template: z.boolean(),
  license: License.nullable(),
  releaseMode: z.enum(['tags']),

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

/* ---- artifact ---------------------------------------------------------- */

export const ForgeData = z.object({
  schemaVersion: z.literal(SCHEMA_VERSION),
  /** Sorted by slug. */
  repos: z.array(Repo),
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
  return { schemaVersion: SCHEMA_VERSION, repos: [], warnings: [] };
}
