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
import { resolveHosting } from './hosting';
import { slugFor, slugify } from './meta';
import { collectNotes } from './notes';
import { resolveOrganizations, type OrgRepoInput } from './orgs';
import { prepareRemote, type MirrorAction, type PrepareRemoteDeps } from './remote';
import {
  configHashFor,
  readRunLog,
  readScanCache,
  rehydrateScan,
  scanCachePathFor,
  scanInputDigest,
  withinFreshWindow,
  writeRunLog,
  writeScanCache,
  type RunLogEntry,
} from './reuse';
import { scanRepo, type ScanResult, type ScanSource } from './scan';

export { scanRepo } from './scan';
export type { ScanOptions, ScanResult, ScanSource } from './scan';
export { ensureMirror, prepareRemote } from './remote';
export type { EnsureMirrorOptions, EnsureMirrorResult, GitRunner, MirrorAction, PrepareRemoteDeps, PrepareRemoteOptions } from './remote';
export { parseIngestArgs, readRunLog, runLogPathFor, scanCachePathFor, withinFreshWindow } from './reuse';
export type { IngestArgs, RunLog, RunLogEntry } from './reuse';
export { HOSTED_BRANCH_FALLBACKS, resolveHostedBranch, resolveHosting } from './hosting';
export type { ResolveHostingResult } from './hosting';
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
  /**
   * `--no-cache`: read nothing from the cross-run caches — no provider `.meta.json`
   * fallback, no freshness window, no scan-cache replay. Fresh results are still recorded,
   * so the next ordinary run benefits from this one.
   */
  noCache?: boolean;
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
    maxCommitAgeDays: config.ingest.maxCommitAgeDays,
    tagTrees: config.ingest.tagTrees,
    branchTrees: config.ingest.branchTrees,
    archives: config.ingest.archives,
    insights: config.ingest.insights,
    hostedMaxFileBytes: config.hosting.maxFileBytes,
  };

  // Cross-run reuse (`ingest.reuse`): reads are disabled by `--no-cache`, writes are not —
  // a `--no-cache` run is maximally fresh and the next ordinary run should benefit from it.
  // The clock is the injectable one remote tests already use; nothing derived from it can
  // reach the artifact (it only ever lands in the cacheDir run log).
  const reuse = config.ingest.reuse;
  const noCache = options.noCache ?? false;
  const reuseReads = reuse.enabled && !noCache;
  const now = () => options.remote?.now?.() ?? new Date();
  // Hashed over the CALLER's config, before any --no-cache override — the run log this run
  // writes must be readable by the next ordinary run of the same config, or "--no-cache
  // still records its fresh results" would be a dead letter for the freshness window.
  const cfgHash = configHashFor(config);
  const prevLog = reuseReads ? await readRunLog(config.cacheDir) : null;
  const prevRemotes = prevLog && prevLog.configHash === cfgHash ? prevLog.remotes : null;
  // `--no-cache` forces a full fetch without leaking into the hashed config above.
  const fetchMode = noCache ? ('always' as const) : config.ingest.fetch;
  const remoteConfig = fetchMode === config.ingest.fetch ? config : { ...config, ingest: { ...config.ingest, fetch: fetchMode } };

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

    // Hosting (schema v7): branches this repo must serve, matched on the pre-collision
    // slug (the scan needs to know before renaming; a collision loser's forced trees are
    // harmless, and the final binding happens post-rename in `resolveHosting`).
    const hostedRequests = config.hosting.sites.filter((s) => s.repo === slug).map((s) => s.branch);

    let scanSource: ScanSource = {
      absPath: src.absPath,
      slug: src.slug,
      overrides: src.overrides,
      ...(hostedRequests.length > 0 ? { hostedRequests } : {}),
    };
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
      remoteAbsPath: src.absPath,
      org,
    });

    if (isRemoteSourceConfig(src)) {
      // Freshness window: only for `'auto'` — `'always'` is an explicit ask to fetch, and
      // `'never'` must keep emitting its stale-cache warnings (a window-skip suppressing
      // them would make the same commits produce different bytes depending on timing).
      const skipFetch =
        fetchMode === 'auto' &&
        prevRemotes !== null &&
        withinFreshWindow(prevRemotes[src.absPath], now(), reuse.maxAgeMinutes);

      // One unreachable forge must never take the build down: prepareRemote turns every
      // failure into a warning, and anything unexpected is caught here as one too.
      let prepared;
      try {
        prepared = await prepareRemote(src, remoteConfig, options.remote ?? {}, { skipFetch, noCacheReads: noCache });
      } catch (e) {
        remote = { slug, provider: src.type, action: 'missing', skipped: true };
        hooks.onRemote?.(remote);
        return skipRemote(`${src.type} import failed (${e instanceof Error ? e.message : String(e)}); repo skipped`);
      }
      remoteWarnings = prepared.warnings.map((w) => ({ ...w, repo: slug }));
      remote = { slug, provider: src.type, action: prepared.action, skipped: !prepared.ready };
      hooks.onRemote?.(remote);
      if (!prepared.ready) return skipRemote(`no usable mirror for ${src.type} repo '${slug}'; repo skipped`);
      scanSource = { ...prepared.scanSource, ...(hostedRequests.length > 0 ? { hostedRequests } : {}) };
    }

    // Scan cache: replay the recorded result when every input is unchanged. The digest is
    // computed over the refs, HEAD, the scan source (provider metadata included) and the
    // options; a hit rehydrates blob/archive bytes from `outDir` so `writeArtifact`'s
    // mirror-and-prune pass sees the full maps. Anything missing → quiet fallback to a
    // real scan. The cache entry is written before assembly mutates the repo (slug
    // renames, remote-warning stamping), so what is stored is exactly a fresh scan.
    let r: ScanResult | null = null;
    const digest = reuse.enabled ? await scanInputDigest(scanSource, opts) : null;
    const scanCacheFile = digest !== null ? scanCachePathFor(config.cacheDir, scanSource.absPath) : null;
    if (digest !== null && scanCacheFile !== null && reuseReads) {
      const entry = await readScanCache(scanCacheFile);
      if (entry && entry.inputDigest === digest) r = await rehydrateScan(entry, config.outDir);
    }
    const fresh = r === null;
    if (r === null) r = await scanRepo(scanSource, opts);
    if (fresh && digest !== null && scanCacheFile !== null && !('skipped' in r)) {
      await writeScanCache(scanCacheFile, digest, r);
    }
    if (!('skipped' in r)) hooks.onRepoDone?.(r.repo);
    return { result: r, remoteWarnings, remote, remoteAbsPath: isRemoteSourceConfig(src) ? src.absPath : null, org };
  });

  const scanned: Array<{
    repo: Repo;
    blobs: Map<string, Buffer>;
    archives: Array<{ file: string; data: Buffer }>;
    org: string | null;
  }> = [];
  const remotes: RemoteStatus[] = [];
  const remoteRuns: Array<{ absPath: string; action: MirrorAction; skipped: boolean; warnings: Warning[] }> = [];
  for (const { result, remoteWarnings, remote, remoteAbsPath, org } of results) {
    if (remote) remotes.push(remote);
    if (remote && remoteAbsPath) {
      remoteRuns.push({ absPath: remoteAbsPath, action: remote.action, skipped: remote.skipped, warnings: remoteWarnings });
    }
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

  // Hosting (schema v7): resolved against the final slugs, like organizations. Runs before
  // the warning mirror below because it pushes repo-scoped warnings onto matched repos.
  const hostingRes = resolveHosting(config, scanned.map((s) => s.repo));

  const blobs = new Map<string, Buffer>();
  const archives = new Map<string, Buffer>();
  // Fixed warning order: site-level, then notes, then organizations, then hosting, then
  // per repo in slug order.
  const warnings: Warning[] = [...siteWarnings, ...noteRes.warnings, ...orgRes.warnings, ...hostingRes.warnings];
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
    hosting: hostingRes.hosting,
    warnings,
  };

  // Record this run for the next one's freshness window. A window-skipped source keeps its
  // previous stamp — the window must not extend itself, or a build every minute would never
  // refresh anything. A degraded fetch records `fresh: false`, so it is always re-attempted.
  if (reuse.enabled) {
    const DEGRADED = new Set(['remote-fetch-failed', 'remote-rate-limited', 'remote-auth-missing', 'remote-cache-stale']);
    const entries: Record<string, RunLogEntry> = {};
    for (const run of remoteRuns) {
      if (run.action === 'reused') {
        const prev = prevRemotes?.[run.absPath];
        if (prev) entries[run.absPath] = prev;
        continue;
      }
      entries[run.absPath] = {
        fetchedAt: now().toISOString(),
        fresh: !run.skipped && !run.warnings.some((w) => DEGRADED.has(w.code)),
      };
    }
    await writeRunLog(config.cacheDir, { configHash: cfgHash, remotes: entries });
  }

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
