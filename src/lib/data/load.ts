/**
 * Site-side loader for the ingest artifact. Used by Astro pages at build time.
 * Never throws on a missing artifact — the site builds (empty) and logs a warning, so a
 * fresh checkout with no repos configured still produces a deployable site.
 */
import fs from 'node:fs';
import path from 'node:path';
import { SCHEMA_VERSION, emptyForgeData, parseForgeData, type ForgeData, type Repo } from './schema';

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
    const raw: unknown = JSON.parse(fs.readFileSync(file, 'utf8'));
    // An artifact from an older frznforge is the single most likely reason validation fails,
    // and a bare ZodError dump ("expected 8") does not tell anyone what to do about it. The
    // artifact is fully derived from the repos, so the fix is always the same: re-ingest.
    const found = (raw as { schemaVersion?: unknown } | null)?.schemaVersion;
    if (typeof found === 'number' && found !== SCHEMA_VERSION) {
      throw new Error(
        `[frznforge] ${file} was written by a different version of frznforge ` +
          `(artifact schema v${found}; this build needs v${SCHEMA_VERSION}). ` +
          'Re-run `npm run build` (or `npm run ingest`) to rebuild it — nothing is lost, ' +
          'the artifact is derived entirely from your repositories.',
      );
    }
    data = parseForgeData(raw);
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
