/**
 * Site configuration schema + `defineConfig`. Kept free of node imports and of any import of
 * frznforge.config.ts itself so that file can import `defineConfig` without a cycle.
 */
import { z } from 'astro/zod';
import { RepoMetaInput, Slug } from '../data/schema';

export const Palette = z.enum(['hearth', 'frost']);
export type Palette = z.infer<typeof Palette>;

export const RepoSourceConfig = z.discriminatedUnion('type', [
  z.object({
    type: z.literal('local'),
    /** Absolute, or relative to frznforge.config.ts. */
    path: z.string().min(1),
    /** URL slug; defaults to the repo directory name, slugified. */
    slug: Slug.optional(),
    /** Metadata overrides (win over the repo's own .frznforge.json). */
    overrides: RepoMetaInput.optional(),
  }),
]);
export type RepoSourceConfig = z.infer<typeof RepoSourceConfig>;

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
