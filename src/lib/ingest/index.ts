/**
 * Ingest public API: scan every configured local repo into a `ForgeData` artifact plus a
 * content-addressed blob store, and write both to disk.
 */
import fs from 'node:fs/promises';
import path from 'node:path';
import type { ResolvedConfig } from '../config/index';
import { ARTIFACT_FILENAME, BLOB_DIRNAME } from '../data/load';
import { SCHEMA_VERSION, parseForgeData, type ForgeData, type Repo, type Warning } from '../data/schema';
import { slugFor } from './meta';
import { scanRepo } from './scan';

export { scanRepo } from './scan';
export type { ScanOptions, ScanResult, ScanSource } from './scan';

export interface IngestHooks {
  onRepoStart?(slug: string): void;
  onRepoDone?(repo: Repo): void;
}

/** Run `fn` over `items` with at most `limit` in flight; results keep input order. */
async function pool<T, R>(items: T[], limit: number, fn: (item: T, index: number) => Promise<R>): Promise<R[]> {
  const results: R[] = new Array(items.length);
  let next = 0;
  const worker = async (): Promise<void> => {
    for (;;) {
      const i = next++;
      if (i >= items.length) return;
      results[i] = await fn(items[i]!, i);
    }
  };
  const n = Math.max(1, Math.min(limit, items.length));
  await Promise.all(Array.from({ length: n }, () => worker()));
  return results;
}

function cmpStr(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0;
}

/**
 * Scan all configured repos (concurrently, bounded by `config.ingest.concurrency`) and
 * assemble the artifact. Deterministic for the same repos at the same commits.
 */
export async function ingest(
  config: ResolvedConfig,
  hooks: IngestHooks = {},
): Promise<{ data: ForgeData; blobs: Map<string, Buffer> }> {
  const siteWarnings: Warning[] = [];
  const opts = { maxBlobBytes: config.ingest.maxBlobBytes, maxCommits: config.ingest.maxCommits };

  const results = await pool(config.repos, config.ingest.concurrency, async (src) => {
    let slug: string;
    try {
      slug = slugFor(src.absPath, src.slug);
    } catch {
      slug = src.slug ?? path.basename(src.absPath);
    }
    hooks.onRepoStart?.(slug);
    const r = await scanRepo({ absPath: src.absPath, slug: src.slug, overrides: src.overrides }, opts);
    if (!('skipped' in r)) hooks.onRepoDone?.(r.repo);
    return r;
  });

  const scanned: Array<{ repo: Repo; blobs: Map<string, Buffer> }> = [];
  for (const r of results) {
    if ('skipped' in r) siteWarnings.push(r.warning);
    else scanned.push(r);
  }

  // slug collisions: later entries (config order) get -2, -3, …
  const taken = new Set<string>();
  for (const { repo } of scanned) {
    if (!taken.has(repo.slug)) {
      taken.add(repo.slug);
      continue;
    }
    const base = repo.slug;
    let n = 2;
    while (taken.has(`${base}-${n}`)) n++;
    const slug = `${base}-${n}`;
    taken.add(slug);
    siteWarnings.push({
      code: 'slug-collision',
      repo: slug,
      message: `slug '${base}' is used by more than one repo (${repo.source.path}); renamed to '${slug}'`,
    });
    repo.slug = slug;
    for (const w of repo.warnings) w.repo = slug;
  }

  scanned.sort((a, b) => cmpStr(a.repo.slug, b.repo.slug));

  const blobs = new Map<string, Buffer>();
  const warnings: Warning[] = [...siteWarnings];
  for (const { repo, blobs: b } of scanned) {
    for (const w of repo.warnings) warnings.push({ ...w, repo: repo.slug });
    for (const [sha, buf] of b) blobs.set(sha, buf);
  }

  const data: ForgeData = {
    schemaVersion: SCHEMA_VERSION,
    repos: scanned.map((s) => s.repo),
    warnings,
  };
  return { data, blobs };
}

/** Serialise the artifact exactly as written to disk. */
export function serializeForgeData(data: ForgeData): string {
  return JSON.stringify(data, null, 2) + '\n';
}

/**
 * Write `forge.json` and the blob store to `outDir`. The blob directory is made to mirror
 * the map exactly: missing/changed blobs are written, stale ones are deleted.
 * Throws if `data` does not validate (that is an ingest bug, not a warning).
 */
export async function writeArtifact(data: ForgeData, blobs: Map<string, Buffer>, outDir: string): Promise<void> {
  parseForgeData(data);
  const blobDir = path.join(outDir, BLOB_DIRNAME);
  await fs.mkdir(blobDir, { recursive: true });
  await fs.writeFile(path.join(outDir, ARTIFACT_FILENAME), serializeForgeData(data), 'utf8');

  const existing = new Set(await fs.readdir(blobDir));
  for (const [sha, buf] of blobs) {
    const file = path.join(blobDir, sha);
    if (existing.has(sha)) {
      const st = await fs.stat(file).catch(() => null);
      if (st && st.isFile() && st.size === buf.length) {
        existing.delete(sha);
        continue;
      }
    }
    await fs.writeFile(file, buf);
    existing.delete(sha);
  }
  for (const stale of existing) {
    await fs.rm(path.join(blobDir, stale), { recursive: true, force: true });
  }
}
