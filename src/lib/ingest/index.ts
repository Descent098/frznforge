/**
 * Ingest public API: scan every configured repo — a local directory, or a provider repo
 * mirror-cloned into the ingest cache — into a `ForgeData` artifact plus a
 * content-addressed blob store, and write both to disk.
 */
import fs from 'node:fs/promises';
import path from 'node:path';
import { isRemoteSourceConfig, sourceRepoName, type RemoteProviderName, type ResolvedConfig } from '../config/index';
import { ARTIFACT_FILENAME, BLOB_DIRNAME } from '../data/load';
import {
  SCHEMA_VERSION,
  parseForgeData,
  repoSourceLabel,
  type ForgeData,
  type Repo,
  type Warning,
} from '../data/schema';
import { slugFor, slugify } from './meta';
import { collectNotes } from './notes';
import { resolveOrganizations, type OrgRepoInput } from './orgs';
import { prepareRemote, type MirrorAction, type PrepareRemoteDeps } from './remote';
import { scanRepo, type ScanResult, type ScanSource } from './scan';

export { scanRepo } from './scan';
export type { ScanOptions, ScanResult, ScanSource } from './scan';
export { ensureMirror, prepareRemote } from './remote';
export type { EnsureMirrorOptions, EnsureMirrorResult, GitRunner, MirrorAction, PrepareRemoteDeps } from './remote';
export { collectNotes } from './notes';
export type { CollectNotesOptions, CollectNotesResult } from './notes';
export { resolveOrganizations } from './orgs';
export type { OrgRepoInput, ResolveOrganizationsResult } from './orgs';

/** What ingest did with one remote source. Reporting only — never enters the artifact. */
export interface RemoteStatus {
  slug: string;
  provider: RemoteProviderName;
  action: MirrorAction;
  /** True when there was no mirror to scan and the repo was left out of the artifact. */
  skipped: boolean;
}

export interface IngestHooks {
  onRepoStart?(slug: string): void;
  onRepoDone?(repo: Repo): void;
  /** Fired once per remote source, after its mirror has been resolved. */
  onRemote?(status: RemoteStatus): void;
}

export interface IngestOptions {
  /** Injected dependencies for remote sources (importer factory, git runner, clock, env). */
  remote?: PrepareRemoteDeps;
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
  options: IngestOptions = {},
): Promise<{
  data: ForgeData;
  blobs: Map<string, Buffer>;
  archives: Map<string, Buffer>;
  remotes: RemoteStatus[];
}> {
  const siteWarnings: Warning[] = [];
  const opts = {
    maxBlobBytes: config.ingest.maxBlobBytes,
    maxCommits: config.ingest.maxCommits,
    tagTrees: config.ingest.tagTrees,
    archives: config.ingest.archives,
  };

  const results = await pool(config.repos, config.ingest.concurrency, async (src) => {
    // Must match what prepareRemote/scanRepo will settle on, or the warnings raised before
    // the scan get stamped with a slug no repo has. For a remote source that means the
    // configured name, never the mirror directory (which carries the cache-key digest).
    let slug: string;
    try {
      slug = isRemoteSourceConfig(src)
        ? (src.slug ?? slugify(sourceRepoName(src)))
        : slugFor(src.absPath, src.slug);
    } catch {
      slug = src.slug ?? path.basename(src.absPath);
    }
    hooks.onRepoStart?.(slug);

    // Carried alongside the scan result so organization membership can be resolved against the
    // *final* slug (after collision renaming) without putting a site-config concern on `Repo`.
    const org = src.org ?? null;

    let scanSource: ScanSource = { absPath: src.absPath, slug: src.slug, overrides: src.overrides };
    // Remote warnings are raised with `repo: null`; the slug is stamped on here so they read
    // the same as scanner warnings and follow the repo through a slug-collision rename.
    let remoteWarnings: Warning[] = [];
    let remote: RemoteStatus | null = null;

    const skipRemote = (message: string) => ({
      result: {
        skipped: true as const,
        warning: { code: 'remote-fetch-failed' as const, repo: slug, message },
      },
      remoteWarnings,
      remote,
      org,
    });

    if (isRemoteSourceConfig(src)) {
      // One unreachable forge must never take the build down: prepareRemote turns every
      // failure into a warning, and anything unexpected is caught here as one too.
      let prepared;
      try {
        prepared = await prepareRemote(src, config, options.remote ?? {});
      } catch (e) {
        remote = { slug, provider: src.type, action: 'missing', skipped: true };
        hooks.onRemote?.(remote);
        return skipRemote(`${src.type} import failed (${e instanceof Error ? e.message : String(e)}); repo skipped`);
      }
      remoteWarnings = prepared.warnings.map((w) => ({ ...w, repo: slug }));
      remote = { slug, provider: src.type, action: prepared.action, skipped: !prepared.ready };
      hooks.onRemote?.(remote);
      if (!prepared.ready) return skipRemote(`no usable mirror for ${src.type} repo '${slug}'; repo skipped`);
      scanSource = prepared.scanSource;
    }

    const r = await scanRepo(scanSource, opts);
    if (!('skipped' in r)) hooks.onRepoDone?.(r.repo);
    return { result: r, remoteWarnings, remote, org };
  });

  const scanned: Array<{
    repo: Repo;
    blobs: Map<string, Buffer>;
    archives: Array<{ file: string; data: Buffer }>;
    org: string | null;
  }> = [];
  const remotes: RemoteStatus[] = [];
  for (const { result, remoteWarnings, remote, org } of results) {
    if (remote) remotes.push(remote);
    const r: ScanResult = result;
    if ('skipped' in r) {
      siteWarnings.push(...remoteWarnings, r.warning);
    } else {
      // Carried on the repo so they show on its page and get renamed with it.
      r.repo.warnings.unshift(...remoteWarnings);
      scanned.push({ ...r, org });
    }
  }

  // slug collisions: later entries (config order) get -2, -3, …
  const taken = new Set<string>();
  for (const entry of scanned) {
    const { repo } = entry;
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
      message: `slug '${base}' is used by more than one repo (${repoSourceLabel(repo.source)}); renamed to '${slug}'`,
    });
    repo.slug = slug;
    for (const w of repo.warnings) w.repo = slug;
    // archive paths embed the slug — move them under the renamed one
    const move = (file: string) => file.replace(`archives/${base}/`, `archives/${slug}/`);
    for (const a of repo.archives) a.file = move(a.file);
    for (const a of entry.archives) a.file = move(a.file);
  }

  scanned.sort((a, b) => cmpStr(a.repo.slug, b.repo.slug));

  // Notes: a plain folder on disk, not a git repo — see src/lib/ingest/notes.ts. Their content
  // goes into the very same content-addressed map `writeArtifact` persists to `blobs/`, so the
  // note viewer reads them with the same `readBlob(outDir, sha)` the file viewer uses.
  const noteRes = await collectNotes(config);

  // Organizations: resolved after the collision pass, so membership names the slugs that
  // actually reach the artifact. `scanned` is already slug-sorted, which makes the resolver's
  // repo-side iteration order deterministic.
  const orgInputs: OrgRepoInput[] = scanned.map((s) => ({ slug: s.repo.slug, org: s.org }));
  const orgRes = resolveOrganizations(config, orgInputs);

  const blobs = new Map<string, Buffer>();
  const archives = new Map<string, Buffer>();
  // Fixed warning order: site-level, then notes, then organizations, then per repo in slug order.
  const warnings: Warning[] = [...siteWarnings, ...noteRes.warnings, ...orgRes.warnings];
  for (const { repo, blobs: b, archives: a } of scanned) {
    for (const w of repo.warnings) warnings.push({ ...w, repo: repo.slug });
    for (const [sha, buf] of b) blobs.set(sha, buf);
    for (const { file, data } of a) archives.set(file, data);
  }
  for (const [sha, buf] of noteRes.blobs) blobs.set(sha, buf);

  // Key order here is the key order in forge.json — keep it identical to `ForgeData`.
  const data: ForgeData = {
    schemaVersion: SCHEMA_VERSION,
    repos: scanned.map((s) => s.repo),
    notes: noteRes.notes,
    organizations: orgRes.organizations,
    warnings,
  };
  return { data, blobs, archives, remotes };
}

/** Serialise the artifact exactly as written to disk. */
export function serializeForgeData(data: ForgeData): string {
  return JSON.stringify(data, null, 2) + '\n';
}

const ARCHIVE_DIRNAME = 'archives';

/** All files under `dir` (recursive), as forward-slash paths relative to `dir`. */
async function listFilesRecursive(dir: string): Promise<string[]> {
  const entries = await fs.readdir(dir, { withFileTypes: true, recursive: true }).catch(() => []);
  return entries
    .filter((e) => e.isFile())
    .map((e) => path.relative(dir, path.join(e.parentPath, e.name)).replace(/\\/g, '/'));
}

/**
 * Write `forge.json`, the blob store and the archive store to `outDir`. The `blobs/` and
 * `archives/` directories are made to mirror their maps exactly: missing/changed files are
 * written, stale ones are deleted. Archive map keys are outDir-relative paths
 * (`archives/<slug>/<ref-slug>.zip`). Throws if `data` does not validate (that is an
 * ingest bug, not a warning).
 */
export async function writeArtifact(
  data: ForgeData,
  blobs: Map<string, Buffer>,
  archives: Map<string, Buffer>,
  outDir: string,
): Promise<void> {
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

  // archives/ mirrors the map (keys are outDir-relative, "archives/..." included)
  const archiveDir = path.join(outDir, ARCHIVE_DIRNAME);
  const wanted = new Map<string, Buffer>();
  for (const [rel, buf] of archives) {
    const norm = rel.replace(/\\/g, '/');
    if (!norm.startsWith(`${ARCHIVE_DIRNAME}/`)) throw new Error(`archive path outside ${ARCHIVE_DIRNAME}/: ${rel}`);
    wanted.set(norm.slice(ARCHIVE_DIRNAME.length + 1), buf);
  }
  const existingArchives = new Set(await listFilesRecursive(archiveDir));
  for (const [rel, buf] of wanted) {
    const file = path.join(archiveDir, rel);
    if (existingArchives.has(rel)) {
      const st = await fs.stat(file).catch(() => null);
      if (st && st.isFile() && st.size === buf.length) {
        existingArchives.delete(rel);
        continue;
      }
    }
    await fs.mkdir(path.dirname(file), { recursive: true });
    await fs.writeFile(file, buf);
    existingArchives.delete(rel);
  }
  for (const stale of existingArchives) {
    await fs.rm(path.join(archiveDir, stale), { force: true });
  }
  // drop now-empty per-repo directories
  for (const entry of await fs.readdir(archiveDir, { withFileTypes: true }).catch(() => [])) {
    if (!entry.isDirectory()) continue;
    const sub = path.join(archiveDir, entry.name);
    if ((await fs.readdir(sub)).length === 0) await fs.rm(sub, { recursive: true, force: true });
  }
}
