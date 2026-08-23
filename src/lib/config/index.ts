/**
 * Config loader: validate, apply defaults, resolve paths. Both ingest and the Astro site use
 * `loadConfig()` so defaults and path resolution happen in exactly one place.
 */
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import userConfig from '../../../frznforge.config';
import { FrznforgeConfigSchema, type FrznforgeConfig, type FrznforgeConfigInput, type RepoSourceConfig } from './schema';

export * from './schema';

/** Absolute path of the project root (directory containing frznforge.config.ts). */
export const PROJECT_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..', '..');

export interface ResolvedConfig extends FrznforgeConfig {
  /** Absolute project root. */
  root: string;
  /** Absolute ingest output directory. */
  outDir: string;
  /** Absolute path to the profile markdown file. */
  profilePath: string;
  /** Repos with absolute paths. */
  repos: Array<RepoSourceConfig & { absPath: string }>;
}

/**
 * Validate, apply defaults, and resolve paths against the project root.
 * `FRZNFORGE_OUT_DIR` (env) overrides `ingest.outDir` — used by the e2e tests to build the
 * site against a fixture artifact without touching the real one.
 */
export function resolveConfig(input: FrznforgeConfigInput, root: string = PROJECT_ROOT): ResolvedConfig {
  const cfg = FrznforgeConfigSchema.parse(input);
  const outDirOverride = typeof process !== 'undefined' ? process.env.FRZNFORGE_OUT_DIR : undefined;
  return {
    ...cfg,
    root,
    outDir: path.resolve(root, outDirOverride || cfg.ingest.outDir),
    profilePath: path.resolve(root, cfg.owner.profile),
    repos: cfg.repos.map((r) => ({ ...r, absPath: path.resolve(root, r.path) })),
  };
}

/** Load and resolve the project's frznforge.config.ts. */
export async function loadConfig(root: string = PROJECT_ROOT): Promise<ResolvedConfig> {
  return resolveConfig(userConfig, root);
}
