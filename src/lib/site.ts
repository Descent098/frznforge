/**
 * Build-time helpers shared by pages: config + artifact access. Re-exports the pure helpers
 * from ./format so pages can import everything from one place.
 */
import { loadConfig, type ResolvedConfig } from './config/index';
import { loadForgeData } from './data/load';
import type { ForgeData } from './data/schema';

export * from './format';

let configPromise: Promise<ResolvedConfig> | null = null;

/** Resolved frznforge.config.ts (cached for the build). */
export function getConfig(): Promise<ResolvedConfig> {
  return (configPromise ??= loadConfig());
}

/** The ingest artifact (cached for the build). */
export async function getData(): Promise<ForgeData> {
  const cfg = await getConfig();
  return loadForgeData(cfg.outDir);
}
