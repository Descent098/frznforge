/**
 * Cross-run reuse of ingest work (`ingest.reuse`, 0.2.0). Two mechanisms, both living
 * entirely in `ingest.cacheDir` sidecars so `forge.json` and the provider `.meta.json`
 * keep their documented no-timestamp promises:
 *
 *  - the **run log** (`<cacheDir>/last-run.json`): when and how well each remote source was
 *    last fetched. The freshness window reads it to skip re-fetching a source whose last
 *    fetch succeeded moments ago; a degraded fetch (failed, rate-limited, served stale) is
 *    never window-skipped, so a rate-limited refresh heals itself run by run.
 *  - the **scan cache** (`<cacheDir>/scan/<digest>.json`): one repo's complete `scanRepo`
 *    output, keyed by a digest over everything the scan reads — every branch and tag ref
 *    (names + object ids), HEAD, the scan source (slug, overrides, provider metadata layer,
 *    releases, artifact `source` value) and the `ScanOptions`. Provider metadata is part of
 *    the key on purpose: descriptions, links and releases change with no ref-head change,
 *    and a key without them would replay stale releases into a schema-valid artifact.
 *
 * Reuse must never change artifact bytes: a hit replays the recorded result — validated,
 * with blob and archive bytes read back from the content-addressed stores in `outDir` — or,
 * when anything is missing or invalid, quietly falls back to a real scan. Timestamps are
 * confined to the run log; clocks are injected (`PrepareRemoteDeps.now`) so tests stay
 * deterministic.
 */
import { createHash } from 'node:crypto';
import fs from 'node:fs/promises';
import path from 'node:path';
import { BLOB_DIRNAME } from '../data/load';
import { Repo as RepoSchema, type Repo } from '../data/schema';
import { git, gitMaybe, isGitRepo } from './git';
import type { ScanOptions, ScanResult, ScanSource } from './scan';

/* ---- shared -------------------------------------------------------------- */

/** JSON.stringify with recursively sorted object keys, so digests are order-independent. */
export function stableStringify(value: unknown): string {
  return JSON.stringify(sortValue(value));
}

function sortValue(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(sortValue);
  if (value && typeof value === 'object') {
    const out: Record<string, unknown> = {};
    for (const key of Object.keys(value as Record<string, unknown>).sort()) {
      const v = (value as Record<string, unknown>)[key];
      if (v !== undefined) out[key] = sortValue(v);
    }
    return out;
  }
  return value;
}

function sha256(text: string): string {
  return createHash('sha256').update(text, 'utf8').digest('hex');
}

/** Hash of the resolved config; a mismatch invalidates the whole run log. */
export function configHashFor(config: unknown): string {
  return sha256(stableStringify(config));
}

async function readJson(file: string): Promise<unknown> {
  try {
    return JSON.parse(await fs.readFile(file, 'utf8'));
  } catch {
    return null; // absent, unreadable or corrupt — all mean "nothing recorded"
  }
}

/** Best-effort write: a read-only or full cache directory must not fail a build. */
async function writeJson(file: string, value: unknown): Promise<void> {
  try {
    await fs.mkdir(path.dirname(file), { recursive: true });
    await fs.writeFile(file, `${JSON.stringify(value, null, 2)}\n`, 'utf8');
  } catch {
    /* the cache is an optimisation, never a requirement */
  }
}

/* ---- run log (freshness window) ------------------------------------------ */

const RUN_LOG_VERSION = 1;
const RUN_LOG_FILENAME = 'last-run.json';

export interface RunLogEntry {
  /** Wall-clock instant of the last real fetch attempt (never window-skipped runs). */
  fetchedAt: string;
  /** True when that fetch was fully fresh: mirror updated, no `remote-*` warnings. */
  fresh: boolean;
}

export interface RunLog {
  version: number;
  /** `configHashFor` of the config that produced the entries. */
  configHash: string;
  /** Keyed by the source's mirror path (`ResolvedConfig.repos[].absPath`) — machine-local. */
  remotes: Record<string, RunLogEntry>;
}

export function runLogPathFor(cacheDir: string): string {
  return path.join(cacheDir, RUN_LOG_FILENAME);
}

export async function readRunLog(cacheDir: string): Promise<RunLog | null> {
  const raw = (await readJson(runLogPathFor(cacheDir))) as Partial<RunLog> | null;
  if (!raw || raw.version !== RUN_LOG_VERSION) return null;
  if (typeof raw.configHash !== 'string' || !raw.remotes || typeof raw.remotes !== 'object') return null;
  return { version: RUN_LOG_VERSION, configHash: raw.configHash, remotes: raw.remotes };
}

export async function writeRunLog(cacheDir: string, log: Omit<RunLog, 'version'>): Promise<void> {
  await writeJson(runLogPathFor(cacheDir), { version: RUN_LOG_VERSION, ...log });
}

/** True when `entry` records a fully fresh fetch inside the window ending at `now`. */
export function withinFreshWindow(
  entry: RunLogEntry | undefined,
  now: Date,
  maxAgeMinutes: number,
): boolean {
  if (!entry || !entry.fresh) return false;
  const at = Date.parse(entry.fetchedAt);
  if (!Number.isFinite(at)) return false;
  const age = now.getTime() - at;
  return age >= 0 && age <= maxAgeMinutes * 60_000;
}

/* ---- scan cache ---------------------------------------------------------- */

/** Bumped whenever the on-disk shape below changes; a mismatch is treated as "no cache". */
const SCAN_CACHE_VERSION = 1;
const SCAN_CACHE_DIRNAME = 'scan';

interface ScanCacheEntry {
  version: number;
  /** `scanInputDigest` of the inputs that produced this result. */
  inputDigest: string;
  repo: Repo;
  /** Keys of the blob map at scan time; bytes rehydrate from `<outDir>/blobs/<sha>`. */
  blobShas: string[];
  /**
   * outDir-relative archive paths + a content hash; bytes rehydrate from `<outDir>/<file>`
   * and MUST match the hash. Unlike blobs, archive paths are not content-addressed — they
   * are keyed by slug + ref, and a slug-collision rename means the path recorded here can
   * hold a *different repo's* zip by the next run. Without the hash check a replay would
   * silently publish the colliding repo's source archive under this repo's URL.
   */
  archives: Array<{ file: string; sha256: string }>;
}

/** Where one repo's scan cache lives, keyed by its absolute path (machine-local). */
export function scanCachePathFor(cacheDir: string, absPath: string): string {
  return path.join(cacheDir, SCAN_CACHE_DIRNAME, `${sha256(absPath).slice(0, 16)}.json`);
}

/**
 * Digest over everything `scanRepo` reads: HEAD, every branch/tag ref with its object id,
 * the scan source and the options. `null` when `absPath` is not a git repository — the
 * caller then runs the real scan, which emits the `repo-not-found` skip itself.
 */
export async function scanInputDigest(source: ScanSource, opts: ScanOptions): Promise<string | null> {
  if (!(await isGitRepo(source.absPath))) return null;
  let refs: string;
  let head: string | null;
  try {
    refs = await git(source.absPath, [
      'for-each-ref',
      '--format=%(refname)%00%(objectname)',
      'refs/heads',
      'refs/tags',
    ]);
    head = await gitMaybe(source.absPath, ['symbolic-ref', '--quiet', 'HEAD']);
  } catch {
    return null;
  }
  return sha256(
    stableStringify({
      version: SCAN_CACHE_VERSION,
      head: head?.trim() ?? null,
      refs,
      source,
      opts,
    }),
  );
}

export async function readScanCache(file: string): Promise<ScanCacheEntry | null> {
  const raw = (await readJson(file)) as Partial<ScanCacheEntry> | null;
  if (!raw || raw.version !== SCAN_CACHE_VERSION) return null;
  if (typeof raw.inputDigest !== 'string') return null;
  if (!Array.isArray(raw.blobShas) || !raw.blobShas.every((s) => typeof s === 'string')) return null;
  if (
    !Array.isArray(raw.archives) ||
    !raw.archives.every((a) => a && typeof a.file === 'string' && typeof a.sha256 === 'string')
  ) {
    return null;
  }
  // A corrupt cached repo must degrade to a re-scan, never to a ZodError out of
  // `writeArtifact` that fails the whole build.
  const repo = RepoSchema.safeParse(raw.repo);
  if (!repo.success) return null;
  return {
    version: SCAN_CACHE_VERSION,
    inputDigest: raw.inputDigest,
    repo: repo.data,
    blobShas: raw.blobShas,
    archives: raw.archives,
  };
}

export async function writeScanCache(
  file: string,
  inputDigest: string,
  result: Extract<ScanResult, { repo: Repo }>,
): Promise<void> {
  const entry: ScanCacheEntry = {
    version: SCAN_CACHE_VERSION,
    inputDigest,
    repo: result.repo,
    blobShas: [...result.blobs.keys()],
    archives: result.archives.map((a) => ({
      file: a.file,
      sha256: createHash('sha256').update(a.data).digest('hex'),
    })),
  };
  await writeJson(file, entry);
}

/**
 * Replay a cached scan: bytes come back from the content-addressed stores under `outDir`.
 * Returns `null` — meaning "re-scan" — when any referenced blob or archive is missing
 * (someone deleted `data/`, or the last run wrote with a different `outDir`). This is what
 * keeps a hit safe against `writeArtifact`'s mirror-and-prune pass: a replay always
 * contributes its full buffer maps, so the prune never deletes a cached repo's files.
 */
export async function rehydrateScan(
  entry: ScanCacheEntry,
  outDir: string,
): Promise<Extract<ScanResult, { repo: Repo }> | null> {
  const blobs = new Map<string, Buffer>();
  for (const sha of entry.blobShas) {
    try {
      blobs.set(sha, await fs.readFile(path.join(outDir, BLOB_DIRNAME, sha)));
    } catch {
      return null;
    }
  }
  const archives: Array<{ file: string; data: Buffer }> = [];
  for (const { file, sha256: expected } of entry.archives) {
    let data: Buffer;
    try {
      data = await fs.readFile(path.join(outDir, file));
    } catch {
      return null;
    }
    // Archive paths are slug-keyed, not content-addressed: after a slug-collision rename,
    // this path can hold the colliding repo's zip. Wrong content → re-scan, never replay.
    if (createHash('sha256').update(data).digest('hex') !== expected) return null;
    archives.push({ file, data });
  }
  return { repo: entry.repo, blobs, archives };
}

/* ---- CLI ------------------------------------------------------------------ */

export interface IngestArgs {
  /** `--no-cache`: ignore the provider cache, the freshness window and the scan cache. */
  noCache: boolean;
}

/** Parse `npm run ingest -- <flags>`. Throws on anything unrecognised. */
export function parseIngestArgs(argv: string[]): IngestArgs {
  const args: IngestArgs = { noCache: false };
  for (const a of argv) {
    if (a === '--no-cache') args.noCache = true;
    else throw new Error(`unknown flag: ${a} (usage: npm run ingest [-- --no-cache])`);
  }
  return args;
}
