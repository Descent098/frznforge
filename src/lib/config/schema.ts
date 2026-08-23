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
  repos: z.array(RepoSourceConfig).default([]),
  ingest: z.object({
    outDir: z.string().min(1).default('./data'),
    maxBlobBytes: z.number().int().positive().default(512 * 1024),
    maxCommits: z.number().int().positive().nullable().default(null),
    concurrency: z.number().int().positive().default(4),
    /** Newest N tags get browsable trees + archives (schema v2). 0 disables tag trees. */
    tagTrees: z.number().int().nonnegative().default(25),
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
