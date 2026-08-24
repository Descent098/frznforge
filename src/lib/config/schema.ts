/**
 * Site configuration schema + `defineConfig`. Kept free of node imports and of any import of
 * frznforge.config.ts itself so that file can import `defineConfig` without a cycle.
 */
import { z } from 'astro/zod';
import { RepoMetaInput, Slug } from '../data/schema';

export const Palette = z.enum(['hearth', 'frost']);
export type Palette = z.infer<typeof Palette>;

/**
 * Fields every remote (provider) source accepts on top of its own identity fields.
 *
 * `releases` defaults to `'provider'` for remote sources and `'tags'` for local ones; it is
 * left `optional()` here so ingest can tell "not set" from an explicit choice.
 *
 * `tokenEnv` names an environment variable holding the API token. Tokens live in the
 * environment only — never write one into this config file.
 */
const remoteSourceFields = {
  /** URL slug; defaults to the remote repo name, slugified. */
  slug: Slug.optional(),
  /** Metadata overrides (win over the repo's .frznforge.json and over provider metadata). */
  overrides: RepoMetaInput.optional(),
  /** Where releases come from. Default for remote sources: 'provider'. */
  releases: z.enum(['provider', 'tags']).optional(),
  /** Env var holding the API token; overrides the provider defaults. Never the token itself. */
  tokenEnv: z.string().min(1).optional(),
  /**
   * Slug of an organization this repo belongs to (schema v4). A plain string rather than
   * `Slug` on purpose: naming an organization that is not configured must be a
   * `repo-unknown-org` warning, not a config parse error that fails the whole build.
   */
  org: z.string().min(1).optional(),
} as const;

export const LocalSourceConfig = z.object({
  type: z.literal('local'),
  /** Absolute, or relative to frznforge.config.ts. */
  path: z.string().min(1),
  /** URL slug; defaults to the repo directory name, slugified. */
  slug: Slug.optional(),
  /** Metadata overrides (win over the repo's own .frznforge.json). */
  overrides: RepoMetaInput.optional(),
  /** Where releases come from. Default for local sources: 'tags'. */
  releases: z.enum(['provider', 'tags']).optional(),
  /** Slug of an organization this repo belongs to (schema v4). See `remoteSourceFields.org`. */
  org: z.string().min(1).optional(),
});
export type LocalSourceConfig = z.infer<typeof LocalSourceConfig>;

export const GithubSourceConfig = z.object({
  type: z.literal('github'),
  owner: z.string().min(1),
  repo: z.string().min(1),
  /** API base URL. GitHub Enterprise: "https://git.example.com/api/v3". */
  host: z.url().default('https://api.github.com'),
  ...remoteSourceFields,
});
export type GithubSourceConfig = z.infer<typeof GithubSourceConfig>;

export const GitlabSourceConfig = z.object({
  type: z.literal('gitlab'),
  /** Full namespaced project path, e.g. "group/sub/proj" (URL-encoded by the importer). */
  project: z.string().min(1),
  /** Instance base URL (the importer appends /api/v4). */
  host: z.url().default('https://gitlab.com'),
  ...remoteSourceFields,
});
export type GitlabSourceConfig = z.infer<typeof GitlabSourceConfig>;

export const GiteaSourceConfig = z.object({
  type: z.literal('gitea'),
  /** Instance base URL, e.g. "https://gitea.example.com" (the importer appends /api/v1). */
  host: z.url(),
  owner: z.string().min(1),
  repo: z.string().min(1),
  ...remoteSourceFields,
});
export type GiteaSourceConfig = z.infer<typeof GiteaSourceConfig>;

/** Same REST API as Gitea; a separate type so the UI and docs can name Forgejo correctly. */
export const ForgejoSourceConfig = z.object({
  type: z.literal('forgejo'),
  /** Instance base URL, e.g. "https://codeberg.org" (the importer appends /api/v1). */
  host: z.url(),
  owner: z.string().min(1),
  repo: z.string().min(1),
  ...remoteSourceFields,
});
export type ForgejoSourceConfig = z.infer<typeof ForgejoSourceConfig>;

export const RepoSourceConfig = z.discriminatedUnion('type', [
  LocalSourceConfig,
  GithubSourceConfig,
  GitlabSourceConfig,
  GiteaSourceConfig,
  ForgejoSourceConfig,
]);
export type RepoSourceConfig = z.infer<typeof RepoSourceConfig>;

/** Provider names of the non-local source variants. */
export type RemoteProviderName = Exclude<RepoSourceConfig['type'], 'local'>;

/** A configured source that is fetched from a hosting provider rather than read from disk. */
export type RemoteRepoSourceConfig = Extract<RepoSourceConfig, { type: RemoteProviderName }>;

/** True for every source that is not `type: 'local'`. */
export function isRemoteSourceConfig(source: RepoSourceConfig): source is RemoteRepoSourceConfig {
  return source.type !== 'local';
}

/**
 * Effective release mode for a source: what the user asked for, else 'provider' for remote
 * sources and 'tags' for local ones. A repo's own `.frznforge.json`/`overrides.releaseMode`
 * still wins over this (see mergeMeta).
 */
export function defaultReleaseMode(source: RepoSourceConfig): 'provider' | 'tags' {
  return source.releases ?? (source.type === 'local' ? 'tags' : 'provider');
}

/**
 * One organization (schema v4): a named grouping of repos with its own overview page.
 *
 * `repos` is one of the two ways a repo joins an org; the other is `org: '<slug>'` on the repo
 * source. Membership is the union, so either direction alone is enough. Entries that name a
 * slug no ingested repo has produce an `org-unknown-repo` warning — hence `z.string()` rather
 * than `Slug`, which would turn a typo into a build failure.
 *
 * Prose, links and pinned repos live in `<content.orgs>/<slug>.md`, exactly like the owner's
 * `profile.md`; the org still gets a page when that file is absent.
 */
export const OrganizationConfig = z.object({
  /** URL slug and the value repo sources use in their `org` field. */
  slug: Slug,
  /** Display name. */
  name: z.string().min(1),
  /** Short blurb; the `<content.orgs>/<slug>.md` frontmatter `description` wins over it. */
  description: z.string().max(300).optional(),
  /** Repo slugs that belong to this org. */
  repos: z.array(z.string().min(1)).optional(),
});
export type OrganizationConfig = z.infer<typeof OrganizationConfig>;

export const FrznforgeConfigSchema = z.object({
  site: z.object({
    title: z.string().min(1).default('frznforge'),
    url: z.url().optional(),
    description: z.string().optional(),
  }).prefault({}),
  owner: z.object({
    name: z.string().min(1),
    handle: Slug,
    profile: z.string().min(1).default('./content/profile.md'),
  }),
  theme: z.object({
    palette: Palette.default('hearth'),
  }).prefault({}),
  /**
   * Where hand-written markdown lives, other than `owner.profile` (which keeps its own key for
   * backwards compatibility). One small block so there is a single obvious home for the next
   * one of these; today it holds exactly one entry.
   */
  content: z.object({
    /**
     * Directory of organization profile pages: `<dir>/<org-slug>.md`, same frontmatter+body
     * mechanism as `owner.profile`. Absolute, or relative to frznforge.config.ts. Resolved to
     * `ResolvedConfig.orgsDir`. The directory may be absent — orgs then render from config only.
     */
    orgs: z.string().min(1).default('./content/orgs'),
  }).prefault({}),
  repos: z.array(RepoSourceConfig).default([]),
  /** Gist-style notes read from a plain folder on disk (schema v4). */
  notes: z.object({
    /**
     * Folder holding the notes. Absolute, or relative to frznforge.config.ts. Resolved to
     * `ResolvedConfig.notesDir`. A file directly inside it is a single-file note; a sub-folder
     * is a multi-file note. Missing directory → no notes, plus a `notes-dir-missing` warning
     * if (and only if) this `notes` block is present — see `ResolvedConfig.notesConfigured`.
     *
     * This folder is NOT a git repository, which is why ingest reads it straight from disk.
     * The "never read the working tree" rule is about repositories.
     */
    dir: z.string().min(1).default('./content/notes'),
    /**
     * Fall back to the file's modification time when a note has no frontmatter `date`.
     *
     * Off by default because mtimes are not reproducible: a fresh checkout stamps every file
     * with the clone time, so two builds of the same content would emit different bytes.
     * Turning this on is an explicit trade of determinism for convenience.
     */
    useMtime: z.boolean().default(false),
    /** Byte cap for storing note content. Defaults to `ingest.maxBlobBytes` when unset. */
    maxFileBytes: z.number().int().positive().optional(),
  }).prefault({}),
  /** Organizations repos can be grouped under (schema v4). */
  organizations: z.array(OrganizationConfig).default([]),
  ingest: z.object({
    outDir: z.string().min(1).default('./data'),
    maxBlobBytes: z.number().int().positive().default(512 * 1024),
    maxCommits: z.number().int().positive().nullable().default(null),
    concurrency: z.number().int().positive().default(4),
    /** Newest N tags get browsable trees + archives (schema v2). 0 disables tag trees. */
    tagTrees: z.number().int().nonnegative().default(25),
    /**
     * How many **non-default** branches get browsable trees (schema v5): the N most recently
     * updated, or `'all'` for every branch. `0` gives the default branch only.
     *
     * This is the single biggest lever on build size, because tree/blob/raw pages are emitted
     * per browsable ref: a repo with 27 branches costs 27× its file count in pages. The
     * default branch always has a tree (`Repo.tree`/`Repo.files`) and never counts against the
     * cap. Skipped branches still appear on the branches page and in `Repo.branches`; they
     * just have no file browser. Capping raises `branch-trees-capped`.
     */
    branchTrees: z.union([z.number().int().nonnegative(), z.literal('all')]).default(10),
    /** Produce zip source archives with `git archive` for the default branch + tag-tree tags. */
    archives: z.boolean().default(true),
    /**
     * Where remote repos are mirror-cloned and provider responses are cached (schema v3).
     * Absolute, or relative to frznforge.config.ts. Git-ignored; safe to delete.
     */
    cacheDir: z.string().min(1).default('./.frznforge-cache'),
    /**
     * Network policy for remote sources:
     *  - 'auto'   — fetch, and fall back to the cache when the network/API fails (default)
     *  - 'never'  — offline: use the cache only, warn (`remote-cache-stale`) when refreshing
     *               is skipped and (`remote-fetch-failed`) when there is no cache at all
     *  - 'always' — always hit the network; failures are still warnings, never build errors
     */
    fetch: z.enum(['auto', 'never', 'always']).default('auto'),
    /**
     * Per-repo insights (schema v5): monthly commits/contributors plus a sampled code-size
     * series, computed at ingest and rendered at `/repos/<slug>/insights/`.
     *
     * Commits are exact (the commit list is already in the artifact). Code size is sampled so
     * a long history cannot make the build unbounded, and sampling is deterministic —
     * checkpoints come from the commit list, never from a clock.
     */
    insights: z.object({
      /** Off ⇒ `Repo.insights` is `null` everywhere and no insights page is built. */
      enabled: z.boolean().default(true),
      /** Maximum monthly code-size checkpoints per repo (first and last are always included). */
      samples: z.number().int().positive().default(24),
      /**
       * Byte budget for line counting at ONE checkpoint. Once a checkpoint's text blobs exceed
       * it, that point keeps its `bytes` but reports `lines: null`, the series is marked
       * `approximate`, and the repo gets an `insights-approximate` warning.
       */
      maxBytesPerSample: z.number().int().positive().default(20 * 1024 * 1024),
    }).prefault({}),
  }).prefault({}),
  listing: z.object({
    pageSize: z.number().int().positive().default(50),
  }).prefault({}),
});

/** What the user writes (defaults optional). */
export type FrznforgeConfigInput = z.input<typeof FrznforgeConfigSchema>;
/** What the code consumes (defaults applied, paths still as written). */
export type FrznforgeConfig = z.output<typeof FrznforgeConfigSchema>;

/** Identity helper for type-checking + editor completion in frznforge.config.ts. */
export function defineConfig(config: FrznforgeConfigInput): FrznforgeConfigInput {
  return config;
}
