/**
 * Site-side loader for the ingest artifact. Used by Astro pages at build time.
 * Never throws on a missing artifact — the site builds (empty) and logs a warning, so a
 * fresh checkout with no repos configured still produces a deployable site.
 */
import fs from 'node:fs';
import path from 'node:path';
import { emptyForgeData, parseForgeData, type ForgeData, type Repo } from './schema';

export const ARTIFACT_FILENAME = 'forge.json';
export const BLOB_DIRNAME = 'blobs';

let cache: { outDir: string; data: ForgeData } | null = null;

/** Read + validate `<outDir>/forge.json`. Cached per outDir for the lifetime of the build. */
export function loadForgeData(outDir: string): ForgeData {
  if (cache && cache.outDir === outDir) return cache.data;
  const file = path.join(outDir, ARTIFACT_FILENAME);
  let data: ForgeData;
  if (!fs.existsSync(file)) {
    console.warn(`[frznforge] no artifact at ${file} — run \`npm run ingest\`. Building an empty site.`);
    data = emptyForgeData();
  } else {
    data = parseForgeData(JSON.parse(fs.readFileSync(file, 'utf8')));
  }
  cache = { outDir, data };
  return data;
}

/** Drop the cache (tests). */
export function resetForgeDataCache(): void {
  cache = null;
}

/** Read a stored blob by sha as raw bytes; null when not stored (too large / unknown). */
export function readBlobBuffer(outDir: string, sha: string): Buffer | null {
  const file = path.join(outDir, BLOB_DIRNAME, sha);
  return fs.existsSync(file) ? fs.readFileSync(file) : null;
}

/** Read a stored blob by sha as utf8 text; null when not stored (binary / too large / unknown). */
export function readBlob(outDir: string, sha: string): string | null {
  return readBlobBuffer(outDir, sha)?.toString('utf8') ?? null;
}

export function findRepo(data: ForgeData, slug: string): Repo | undefined {
  return data.repos.find((r) => r.slug === slug);
}
