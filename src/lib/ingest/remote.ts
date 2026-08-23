/**
 * Remote sources (schema v3): turn a configured provider repo into something the existing
 * local scanner can read.
 *
 * Two halves, deliberately kept apart:
 *  - **metadata** comes from the provider REST API through an `Importer` (src/lib/importers);
 *  - **git** comes from a plain `git clone --mirror` into `<ingest.cacheDir>/…`, which
 *    `scanRepo` then reads exactly as it reads a local bare repo.
 *
 * Both halves are cached under `<ingest.cacheDir>`: the mirror as a bare repo, the importer's
 * normalised answers next to it as `<repo>.meta.json`. An offline build (or one whose provider
 * API is down) therefore keeps the repo's description, topics, links and releases instead of
 * silently publishing it stripped bare.
 *
 * Invariants:
 *  - A build must never fail because a forge is down, private or rate limited. Every failure
 *    becomes a `Warning` and, where a cache exists, the cached data is used instead.
 *  - Tokens come from the environment only and never reach disk, a log line, a warning or the
 *    artifact. The clone credential is passed per-invocation through the child's environment
 *    (see `authEnv`) — not on the remote URL, so it cannot land in `.git/config`, and not on
 *    argv, so it is not readable in the OS process list by other users on the machine.
 *  - Nothing volatile enters the artifact: mirror actions are reported back to the caller for
 *    console output only.
 */
import { spawn } from 'node:child_process';
import fs from 'node:fs/promises';
import path from 'node:path';
import {
  cachePathFor,
  defaultReleaseMode,
  sourceRepoName,
  type RemoteProviderName,
  type RemoteRepoSourceConfig,
  type RepoSourceConfig,
  type ResolvedConfig,
} from '../config/index';
import {
  ImporterError,
  createImporter as defaultCreateImporter,
  redactToken,
  resolveToken,
  tokenEnvFor,
  type ImportedRepoMeta,
  type Importer,
  type ImporterContext,
} from '../importers/index';
import { Release as ReleaseSchema, RepoMetaInput as RepoMetaInputSchema } from '../data/schema';
import type { Release, RepoLinks, RepoMetaInput, RepoSource, Warning } from '../data/schema';
import { isGitRepo } from './git';
import { MAX_DESCRIPTION, isDescriptionTooLong, slugify, truncateDescription } from './meta';
import type { ScanSource } from './scan';

/** Hard ceiling on a single network git invocation. */
export const DEFAULT_GIT_TIMEOUT_MS = 300_000;

/** What `ensureMirror` did (or could not do). */
export type MirrorAction = 'cloned' | 'fetched' | 'cached' | 'missing';

export interface GitRunResult {
  code: number | null;
  signal: NodeJS.Signals | null;
  stdout: string;
  stderr: string;
}

/** Everything a {@link GitRunner} needs besides the argv. */
export interface GitRunContext {
  cwd: string;
  timeoutMs: number;
  /** Extra environment for the child — this is where the clone credential travels. */
  env?: NodeJS.ProcessEnv;
}

/** Injectable `git` runner so tests can observe (or refuse) every invocation. */
export type GitRunner = (args: string[], opts: GitRunContext) => Promise<GitRunResult>;

export interface EnsureMirrorOptions {
  /** Network policy, from `ingest.fetch`. */
  fetch: 'auto' | 'never' | 'always';
  /** URL to clone from. Must not carry credentials — the token is sent as a header. */
  cloneUrl: string;
  /** API/clone token, already resolved from the environment. */
  token?: string | null;
  /** Per-invocation timeout; default `DEFAULT_GIT_TIMEOUT_MS`. */
  timeoutMs?: number;
  /** Injectable git runner (tests). */
  run?: GitRunner;
}

export interface EnsureMirrorResult {
  /** The mirror path (whether or not it exists). */
  path: string;
  action: MirrorAction;
  /** Why the mirror could not be refreshed/created. Already redacted; safe to show. */
  error?: ImporterError | Error;
}

/* ---- git plumbing -------------------------------------------------------- */

/**
 * Non-interactive environment for network git. `GIT_TERMINAL_PROMPT=0` plus an empty
 * askpass means a private repo without a usable token fails fast instead of blocking the
 * build on a credential prompt (a GUI helper would otherwise hang a CI run forever).
 */
function mirrorEnv(extra: NodeJS.ProcessEnv = {}): NodeJS.ProcessEnv {
  return {
    ...process.env,
    GIT_TERMINAL_PROMPT: '0',
    GIT_ASKPASS: '',
    SSH_ASKPASS: '',
    GCM_INTERACTIVE: 'Never',
    GIT_CONFIG_NOSYSTEM: '1',
    GIT_OPTIONAL_LOCKS: '0',
    LC_ALL: 'C',
    LANG: 'C',
    GIT_PAGER: 'cat',
    ...extra,
  };
}

const defaultGitRunner: GitRunner = (args, { cwd, timeoutMs, env }) =>
  new Promise((resolve, reject) => {
    const out: Buffer[] = [];
    const err: Buffer[] = [];
    let settled = false;
    const fail = (e: Error) => {
      if (settled) return;
      settled = true;
      reject(new Error(`failed to run git (is it installed and on PATH?): ${e.message}`));
    };

    let child;
    try {
      child = spawn('git', args, {
        cwd,
        env: mirrorEnv(env),
        stdio: ['ignore', 'pipe', 'pipe'],
        windowsHide: true,
        timeout: timeoutMs,
        killSignal: 'SIGKILL',
      });
    } catch (e) {
      // spawn can throw synchronously (bad cwd, argv too long); that must not escape either.
      fail(e instanceof Error ? e : new Error(String(e)));
      return;
    }

    child.stdout.on('data', (c: Buffer) => out.push(c));
    child.stderr.on('data', (c: Buffer) => err.push(c));
    // Without these, a failed spawn (an over-long destination path on Windows is the easy
    // repro) emits 'error' on an unlistened stdio socket, which Node turns into an *uncaught*
    // exception: the promise never settles, ensureMirror's error path never runs, and the
    // whole ingest dies over one unreachable remote.
    child.stdout.on('error', fail);
    child.stderr.on('error', fail);
    child.on('error', fail);
    child.on('close', (code, signal) => {
      if (settled) return;
      settled = true;
      resolve({
        code,
        signal,
        stdout: Buffer.concat(out).toString('utf8'),
        stderr: Buffer.concat(err).toString('utf8'),
      });
    });
  });

/**
 * Git configuration for one invocation, passed through the child's **environment**.
 *
 * Three things are being avoided at once:
 *  - a token in the remote URL (`https://token@host/…`) would be written into the mirror's
 *    `config` file and sit on disk forever;
 *  - a token in `-c http.extraheader=…` on argv is readable by every other process on the
 *    machine for the duration of the clone (`/proc/<pid>/cmdline`, `Get-CimInstance
 *    Win32_Process`), which matters on shared CI runners;
 *  - a configured credential helper could pop a GUI dialog and stall the build forever, so it
 *    is disabled with an empty `credential.helper` whether or not there is a token.
 *
 * `GIT_CONFIG_COUNT`/`GIT_CONFIG_KEY_n`/`GIT_CONFIG_VALUE_n` apply to this process and the
 * transport helpers it spawns, and are never persisted into the cloned repository.
 *
 * The username half of the basic credential is provider-specific: GitLab only accepts a
 * personal access token when the username is `oauth2`, while GitHub/Gitea/Forgejo accept the
 * conventional `x-access-token`.
 */
export function authEnv(provider: RemoteProviderName, token: string | null): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = {
    GIT_CONFIG_COUNT: '1',
    GIT_CONFIG_KEY_0: 'credential.helper',
    GIT_CONFIG_VALUE_0: '',
  };
  if (!token) return env;
  const user = provider === 'gitlab' ? 'oauth2' : 'x-access-token';
  const basic = Buffer.from(`${user}:${token}`, 'utf8').toString('base64');
  env.GIT_CONFIG_COUNT = '2';
  env.GIT_CONFIG_KEY_1 = 'http.extraheader';
  env.GIT_CONFIG_VALUE_1 = `Authorization: Basic ${basic}`;
  return env;
}

/** Strip anything credential-shaped out of text bound for a warning or a log line. */
function redactSecrets(text: string, token: string | null): string {
  let out = redactToken(text, token);
  if (token) out = out.split(Buffer.from(token, 'utf8').toString('base64')).join('***');
  // https://user:pass@host/… → https://***@host/…
  return out.replace(/([a-z][a-z0-9+.-]*:\/\/)[^/\s@]+@/gi, '$1***@');
}

/** Collapse git's stderr into one short, stable sentence. */
function gitStderrSummary(stderr: string, token: string | null): string {
  const line = stderr
    .split(/\r?\n/)
    .map((l) => l.trim())
    .filter(Boolean)
    .join('; ');
  const redacted = redactSecrets(line, token);
  return redacted.length > 300 ? `${redacted.slice(0, 297)}…` : redacted;
}

function gitFailure(label: string, r: GitRunResult, token: string | null, timeoutMs: number): Error {
  if (r.signal) return new Error(`${label} timed out after ${Math.round(timeoutMs / 1000)}s`);
  const detail = gitStderrSummary(r.stderr, token);
  return new Error(`${label} failed (exit ${r.code})${detail ? `: ${detail}` : ''}`);
}

async function pathExists(p: string): Promise<boolean> {
  try {
    await fs.stat(p);
    return true;
  } catch {
    return false;
  }
}

/**
 * Make sure `cachePath` holds a usable bare mirror of `source`, honouring the `fetch` policy:
 *
 *  - `'never'`  — never touches the network: the existing cache (`'cached'`) or `'missing'`.
 *  - `'auto'`   — `git clone --mirror` when the cache is absent, otherwise
 *                 `git remote update --prune`; a failed refresh falls back to the cache
 *                 (`'cached'`) with `error` set so the caller can warn.
 *  - `'always'` — same as `'auto'` today. The distinction is kept so a future freshness
 *                 heuristic can make `'auto'` skip a recent fetch without changing configs.
 *
 * Never throws for a network/auth problem: the error is returned, not raised.
 */
export function ensureMirror(
  source: RemoteRepoSourceConfig,
  cachePath: string,
  opts: EnsureMirrorOptions,
): Promise<EnsureMirrorResult> {
  // Serialised per destination: two sources resolving to one mirror would otherwise race, and
  // the loser's cleanup (`fs.rm` of a failed clone) would delete the winner's fresh mirror.
  // `cachePathFor` makes that near-impossible, but a hand-written duplicate config entry is
  // still legal and must not corrupt anything.
  return withMirrorLock(cachePath, () => ensureMirrorLocked(source, cachePath, opts));
}

/** In-flight `ensureMirror` calls, keyed by destination — see {@link ensureMirror}. */
const mirrorLocks = new Map<string, Promise<unknown>>();

function withMirrorLock<T>(key: string, fn: () => Promise<T>): Promise<T> {
  const previous = mirrorLocks.get(key) ?? Promise.resolve();
  const next = previous.then(fn, fn);
  const guard = next.then(
    () => undefined,
    () => undefined,
  );
  mirrorLocks.set(key, guard);
  // Drop the entry once nothing is queued behind it, so the map does not grow for the life
  // of the process.
  void guard.then(() => {
    if (mirrorLocks.get(key) === guard) mirrorLocks.delete(key);
  });
  return next;
}

async function ensureMirrorLocked(
  source: RemoteRepoSourceConfig,
  cachePath: string,
  opts: EnsureMirrorOptions,
): Promise<EnsureMirrorResult> {
  const run = opts.run ?? defaultGitRunner;
  const timeoutMs = opts.timeoutMs ?? DEFAULT_GIT_TIMEOUT_MS;
  const token = opts.token ?? null;
  const exists = await isGitRepo(cachePath);

  if (opts.fetch === 'never') {
    if (exists) return { path: cachePath, action: 'cached' };
    return {
      path: cachePath,
      action: 'missing',
      error: new Error(`no cached mirror at ${cachePath} and ingest.fetch is 'never'`),
    };
  }

  const env = authEnv(source.type, token);
  /** Run git, turning even a failed spawn into a result the caller can warn about. */
  const attempt = async (args: string[], cwd: string): Promise<GitRunResult | Error> => {
    try {
      return await run(args, { cwd, timeoutMs, env });
    } catch (e) {
      return e instanceof Error ? e : new Error(String(e));
    }
  };

  if (exists) {
    const r = await attempt(['-C', cachePath, 'remote', 'update', '--prune'], cachePath);
    if (r instanceof Error) return { path: cachePath, action: 'cached', error: r };
    if (r.code === 0) return { path: cachePath, action: 'fetched' };
    return { path: cachePath, action: 'cached', error: gitFailure('git remote update', r, token, timeoutMs) };
  }

  if (await pathExists(cachePath)) {
    return {
      path: cachePath,
      action: 'missing',
      error: new Error(`${cachePath} exists but is not a git repository; delete it and re-run`),
    };
  }

  const parent = path.dirname(cachePath);
  try {
    await fs.mkdir(parent, { recursive: true });
  } catch (e) {
    return { path: cachePath, action: 'missing', error: e instanceof Error ? e : new Error(String(e)) };
  }
  const r = await attempt(['clone', '--mirror', '--quiet', '--', opts.cloneUrl, cachePath], parent);
  if (!(r instanceof Error) && r.code === 0) return { path: cachePath, action: 'cloned' };
  // A half-written clone would make every later run fail the "exists but is not a repo" check.
  // Safe to remove: the lock plus the `pathExists` check above mean this call created it.
  await fs.rm(cachePath, { recursive: true, force: true }).catch(() => {});
  return {
    path: cachePath,
    action: 'missing',
    error: r instanceof Error ? r : gitFailure('git clone --mirror', r, token, timeoutMs),
  };
}

/* ---- provider URLs ------------------------------------------------------- */

function isHttpUrl(value: string | null | undefined): value is string {
  if (!value) return false;
  try {
    const u = new URL(value);
    return u.protocol === 'http:' || u.protocol === 'https:';
  } catch {
    return false;
  }
}

/** Human-facing base URL of the instance (the API base is not always browsable). */
function providerWebBase(source: RemoteRepoSourceConfig): string {
  const host = source.host.replace(/\/+$/, '');
  if (source.type !== 'github') return host;
  try {
    const u = new URL(host);
    if (u.hostname === 'api.github.com') return 'https://github.com';
    // GitHub Enterprise: https://git.example.com/api/v3 → https://git.example.com
    u.pathname = u.pathname.replace(/\/api\/v3\/?$/, '');
    return u.toString().replace(/\/+$/, '');
  } catch {
    return host;
  }
}

/** Best-effort repo page URL, used when the API could not be reached. */
function deriveWebUrl(source: RemoteRepoSourceConfig): string {
  const tail = source.type === 'gitlab' ? source.project : `${source.owner}/${source.repo}`;
  const encoded = tail.split('/').filter(Boolean).map(encodeURIComponent).join('/');
  return `${providerWebBase(source)}/${encoded}`;
}

function buildRepoSource(source: RemoteRepoSourceConfig, webUrl: string, cloneUrl: string): RepoSource {
  // Key order matches the schema declaration order (artifact determinism).
  switch (source.type) {
    case 'github':
      return { type: 'github', host: source.host, owner: source.owner, repo: source.repo, webUrl, cloneUrl };
    case 'gitlab':
      return { type: 'gitlab', host: source.host, project: source.project, webUrl, cloneUrl };
    case 'gitea':
      return { type: 'gitea', host: source.host, owner: source.owner, repo: source.repo, webUrl, cloneUrl };
    case 'forgejo':
      return { type: 'forgejo', host: source.host, owner: source.owner, repo: source.repo, webUrl, cloneUrl };
  }
}

/* ---- prepareRemote ------------------------------------------------------- */

export interface PrepareRemoteDeps {
  /** Importer factory; defaults to the real registry. */
  createImporter?: (source: RepoSourceConfig, ctx: ImporterContext) => Importer | null;
  /** Mirror driver; defaults to `ensureMirror`. */
  ensureMirror?: typeof ensureMirror;
  /** Injectable git runner, forwarded to `ensureMirror`. */
  git?: GitRunner;
  /** Injectable `fetch`, forwarded to the importer. */
  fetchImpl?: typeof fetch;
  /** Environment the token is read from; defaults to `process.env`. */
  env?: Record<string, string | undefined>;
  /** Clock (reserved for cache-freshness policies); injected so tests stay deterministic. */
  now?: () => Date;
  /** Per-invocation git timeout override. */
  timeoutMs?: number;
}

export interface PrepareRemoteResult {
  /** Ready to hand to `scanRepo` (only meaningful when `ready`). */
  scanSource: ScanSource;
  /** Provider metadata as a lowest-precedence metadata layer, or null when unavailable. */
  providerMeta: RepoMetaInput | null;
  /** Provider releases (empty in tag mode or when the call failed). */
  releases: Release[];
  /** Warnings with `repo: null` — the caller stamps the slug. */
  warnings: Warning[];
  /** What happened to the mirror. */
  action: MirrorAction;
  /** False when there is no mirror to scan; the caller must skip the repo. */
  ready: boolean;
}

/** A configured remote source, optionally carrying the resolved cache path from the config. */
export type RemoteRepoInput = RemoteRepoSourceConfig & { absPath?: string };

function warn(code: Warning['code'], message: string): Warning {
  return { code, repo: null, message };
}

/** Map an importer failure onto the right `remote-*` warning code. */
function importerWarning(
  error: unknown,
  what: string,
  source: RemoteRepoSourceConfig,
  token: string | null,
): Warning {
  const raw = error instanceof Error ? error.message : String(error);
  const message = `${what}: ${redactSecrets(raw, token)}`;
  if (error instanceof ImporterError) {
    if (error.kind === 'rate-limit') return warn('remote-rate-limited', message);
    if ((error.kind === 'auth' || error.kind === 'not-found') && !token) {
      return warn('remote-auth-missing', `${message} (no API token found in ${tokenEnvFor(source).join(' or ')})`);
    }
  }
  return warn('remote-fetch-failed', message);
}

/**
 * Provider metadata → the `RepoMetaInput` layer the scanner merges *below* the repo's own
 * `.frznforge.json`. Only fields the data model actually has survive; anything that would
 * fail schema validation (a non-URL homepage, an over-long description) is dropped or
 * truncated here rather than blowing up `writeArtifact`.
 *
 * The assembled layer is validated before it leaves, for the same reason `readRepoMetaFile`
 * validates the in-repo file: this is remote data, and one bad field must degrade to a
 * warning, never to a `ZodError` out of `parseForgeData` that kills the whole build.
 */
function metaLayer(meta: ImportedRepoMeta, warnings: Warning[]): RepoMetaInput {
  const layer: RepoMetaInput = {};
  if (meta.name) layer.name = meta.name;
  if (meta.description) {
    if (isDescriptionTooLong(meta.description)) {
      layer.description = truncateDescription(meta.description);
      warnings.push(
        warn('description-truncated', `provider description exceeded ${MAX_DESCRIPTION} characters and was truncated`),
      );
    } else {
      layer.description = meta.description;
    }
  }
  const links: RepoLinks = {};
  if (isHttpUrl(meta.homepage)) links.homepage = meta.homepage;
  if (isHttpUrl(meta.issuesUrl)) links.issues = meta.issuesUrl;
  if (isHttpUrl(meta.webUrl)) links.upstream = meta.webUrl;
  if (Object.keys(links).length > 0) layer.links = links;
  const topics = meta.topics.map((t) => t.trim()).filter(Boolean);
  if (topics.length > 0) layer.tags = topics;
  if (meta.template) layer.template = true;
  if (meta.license) layer.license = meta.license;
  return validateLayer(layer, warnings);
}

/** Drop whatever the schema rejects, with a warning naming the fields; never throws. */
function validateLayer(layer: RepoMetaInput, warnings: Warning[]): RepoMetaInput {
  const parsed = RepoMetaInputSchema.safeParse(layer);
  if (parsed.success) return parsed.data;
  const bad = [...new Set(parsed.error.issues.map((i) => String(i.path[0] ?? '(root)')))].sort();
  const rest: Record<string, unknown> = { ...layer };
  for (const key of bad) delete rest[key];
  warnings.push(
    warn('remote-fetch-failed', `provider metadata failed validation; dropped field(s): ${bad.join(', ')}`),
  );
  const retry = RepoMetaInputSchema.safeParse(rest);
  return retry.success ? retry.data : {};
}

/* ---- provider response cache -------------------------------------------- */

/** Bumped whenever the on-disk shape below changes; a mismatch is treated as "no cache". */
const PROVIDER_CACHE_VERSION = 1;

interface ProviderCache {
  version: number;
  meta: ImportedRepoMeta | null;
  releases: Release[];
}

/**
 * Where one source's importer answers are cached: next to its mirror, as
 * `<repo>-<digest>.meta.json`.
 *
 * Only normalised, non-volatile values are stored — the same fields a live call would have
 * produced — so serving a build from here yields a byte-identical artifact. No token, no
 * timestamps, no counters.
 */
export function providerCachePathFor(cachePath: string): string {
  const base = path.basename(cachePath).replace(/\.git$/i, '');
  return path.join(path.dirname(cachePath), `${base}.meta.json`);
}

async function readProviderCache(file: string): Promise<ProviderCache | null> {
  let parsed: unknown;
  try {
    parsed = JSON.parse(await fs.readFile(file, 'utf8'));
  } catch {
    return null; // absent, unreadable or corrupt — all mean "nothing cached"
  }
  if (!parsed || typeof parsed !== 'object') return null;
  const raw = parsed as Partial<ProviderCache>;
  if (raw.version !== PROVIDER_CACHE_VERSION) return null;
  const releases = Array.isArray(raw.releases)
    ? raw.releases.filter((r): r is Release => ReleaseSchema.safeParse(r).success)
    : [];
  return { version: PROVIDER_CACHE_VERSION, meta: raw.meta ?? null, releases };
}

/** Best-effort write: a read-only or full cache directory must not fail a build. */
async function writeProviderCache(file: string, data: ProviderCache): Promise<void> {
  try {
    await fs.mkdir(path.dirname(file), { recursive: true });
    await fs.writeFile(file, `${JSON.stringify(data, null, 2)}\n`, 'utf8');
  } catch {
    /* the cache is an optimisation, never a requirement */
  }
}

/**
 * Resolve one remote source into a `ScanSource` pointing at a local mirror.
 *
 * Order matters: metadata first (it supplies the clone URL), then the mirror. A failed API
 * call is not fatal — the mirror is still refreshed from the derived clone URL, the provider
 * data falls back to the on-disk cache written by the last successful build, and a repo that
 * has been ingested before keeps rendering exactly as it did.
 */
export async function prepareRemote(
  source: RemoteRepoInput,
  cfg: ResolvedConfig,
  deps: PrepareRemoteDeps = {},
): Promise<PrepareRemoteResult> {
  const warnings: Warning[] = [];
  const cachePath = source.absPath ?? cachePathFor(cfg.cacheDir, source)!;
  const token = resolveToken(source, deps.env ?? process.env);
  const makeImporter = deps.createImporter ?? defaultCreateImporter;
  const mirror = deps.ensureMirror ?? ensureMirror;

  // The in-repo .frznforge.json is only readable once the mirror exists, so the decision to
  // call the releases endpoint uses the config layers alone; scanRepo still has the last word
  // on `releaseMode` and drops the imported list when the repo asks for tag mode.
  const wantProviderReleases = (source.overrides?.releaseMode ?? defaultReleaseMode(source)) === 'provider';

  const cacheFile = providerCachePathFor(cachePath);
  const cached = await readProviderCache(cacheFile);
  const importer = makeImporter(source, { fetchImpl: deps.fetchImpl, token });

  let providerMeta: ImportedRepoMeta | null = null;
  let releases: Release[] = [];
  let freshMeta = false;
  let freshReleases = false;

  if (cfg.ingest.fetch === 'never') {
    warnings.push(
      warn(
        'remote-cache-stale',
        cached
          ? `ingest.fetch is 'never'; provider metadata and releases came from the cache and were not refreshed`
          : `ingest.fetch is 'never' and nothing is cached; this build has no provider description, topics, links or releases for this repo`,
      ),
    );
  } else if (importer) {
    try {
      providerMeta = await importer.fetchMeta();
      freshMeta = true;
    } catch (e) {
      warnings.push(importerWarning(e, 'provider metadata', source, token));
    }
    if (wantProviderReleases) {
      try {
        const imported = await importer.fetchReleases();
        releases = imported.releases;
        freshReleases = true;
        if (imported.truncated) {
          warnings.push(
            warn(
              'remote-fetch-failed',
              `the provider has more releases than one build will page through; the oldest are missing (kept ${releases.length})`,
            ),
          );
        }
      } catch (e) {
        warnings.push(importerWarning(e, 'provider releases', source, token));
      }
    }
  }

  // Fall back to the last successful build's answers rather than publishing the repo stripped
  // of its description, topics, links and releases because the API blipped.
  const usedCache: string[] = [];
  if (!freshMeta && cached?.meta) {
    providerMeta = cached.meta;
    usedCache.push('metadata');
  }
  if (wantProviderReleases && !freshReleases && cached && cached.releases.length > 0) {
    releases = cached.releases;
    usedCache.push('releases');
  }
  if (usedCache.length > 0 && cfg.ingest.fetch !== 'never') {
    warnings.push(warn('remote-cache-stale', `cached provider ${usedCache.join(' and ')} were used`));
  }

  if (freshMeta || freshReleases) {
    await writeProviderCache(cacheFile, {
      version: PROVIDER_CACHE_VERSION,
      meta: freshMeta ? providerMeta : (cached?.meta ?? null),
      releases: freshReleases ? releases : (cached?.releases ?? []),
    });
  }

  const webUrl = isHttpUrl(providerMeta?.webUrl) ? providerMeta.webUrl : deriveWebUrl(source);
  const cloneUrl = isHttpUrl(providerMeta?.cloneUrl) ? providerMeta.cloneUrl : `${webUrl}.git`;

  const result = await mirror(source, cachePath, {
    fetch: cfg.ingest.fetch,
    cloneUrl,
    token,
    ...(deps.timeoutMs !== undefined ? { timeoutMs: deps.timeoutMs } : {}),
    ...(deps.git ? { run: deps.git } : {}),
  });

  if (result.error) {
    warnings.push(warn('remote-fetch-failed', redactSecrets(result.error.message, token)));
  }
  if (result.action === 'cached' && cfg.ingest.fetch !== 'never') {
    warnings.push(warn('remote-cache-stale', 'the mirror could not be refreshed; the cached clone was used'));
  }

  const layer = providerMeta ? metaLayer(providerMeta, warnings) : null;

  const scanSource: ScanSource = {
    absPath: result.path,
    // Both derived from the config, never from the mirror's directory name: that name is a
    // sanitised, lower-cased, hash-suffixed filesystem key, not something to show a visitor.
    slug: source.slug ?? slugify(sourceRepoName(source)),
    defaultName: sourceRepoName(source),
    ...(source.overrides !== undefined ? { overrides: source.overrides } : {}),
    providerMeta: layer,
    source: buildRepoSource(source, webUrl, cloneUrl),
    releaseMode: defaultReleaseMode(source),
    releases,
  };

  return {
    scanSource,
    providerMeta: layer,
    releases,
    warnings,
    action: result.action,
    ready: result.action !== 'missing',
  };
}
