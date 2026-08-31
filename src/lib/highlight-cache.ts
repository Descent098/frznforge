/**
 * Cross-run memo for Shiki output (0.2.0).
 *
 * Highlighting dominates this build. Measured on frznforge's own site: **21.4 s of a 25.5 s
 * static-route phase — 84%** — because Shiki tokenizes every byte of every stored file, once
 * per browsable ref that file appears in, on every single build. Nothing about that work
 * changes between two builds of the same content.
 *
 * `highlightToHtml` is a *pure function*: the same source, in the same language, with the same
 * themes and the same line-id prefix, always produces the same HTML. So its result is
 * remembered here between runs, keyed by everything that goes into it.
 *
 * **This is not "skip unchanged pages."** That idea — remember which pages were rendered and
 * copy the unchanged ones forward — is rejected in [performance.md](../../docs/dev/performance.md)
 * and stays rejected: it needs a dependency graph from artifact fields to output pages, and a
 * miss there is a silently wrong page in a build that reports success. Nothing is skipped here.
 * Every page still renders in full, from the artifact, every build; only one deterministic
 * function call inside those renders is memoized, and a hit returns bytes *identical* to what a
 * miss computes. The stale-page failure mode cannot arise, because the key covers every input:
 * change the file, the language, the prefix, the themes or Shiki itself, and the key changes.
 *
 * The `fingerprint` folds in Shiki's package version *and* a canary render of its output, so a
 * dependency upgrade or a theme edit invalidates every entry rather than serving last version's
 * colours — the one hazard a naive content hash would miss.
 *
 * Entries are content-addressed like the blob store, gzipped (Shiki's markup runs ~10× the
 * source and compresses roughly 15:1), and live in `<ingest.cacheDir>/highlight/`. The whole
 * directory is disposable: delete it and the next build simply recomputes. Reuse is governed by
 * `ingest.reuse.enabled`, and `FRZNFORGE_NO_HL_CACHE=1` bypasses it for one run.
 *
 * **Growth is cumulative, and nothing prunes it.** Unlike `blobs/` and `archives/` — which
 * `writeArtifact` mirrors against the current artifact and prunes — this directory only ever
 * gains entries: every edited line of every file leaves its predecessor behind, and a Shiki
 * upgrade orphans the entire contents at once (the fingerprint is part of every key). That is
 * a deliberate trade rather than an oversight: the alternative is tracking liveness across
 * runs, which would mean this cache owning a notion of "the current build's set" — the kind of
 * cross-run bookkeeping whose invalidation bugs are exactly what this design avoids. The cost
 * is disk in a directory already documented as safe to delete, and the number is small (2.5 MB
 * for this project's own site); a long-lived CI cache wanting a floor should delete the
 * directory periodically rather than have it guess. Interrupted `.tmp` fragments *are* swept,
 * since no run will ever read them.
 */
import { createHash } from 'node:crypto';
import fs from 'node:fs/promises';
import { createRequire } from 'node:module';
import path from 'node:path';
import { promisify } from 'node:util';
import { gunzip, gzip } from 'node:zlib';
import { loadConfig } from './config/index';

const gzipAsync = promisify(gzip);
const gunzipAsync = promisify(gunzip);

/**
 * Bump when the *cache file format* changes. Shiki version and theme changes are covered by
 * the fingerprint instead, so this only moves for a change in how entries are stored.
 */
const CACHE_FORMAT = 1;

const sha256 = (value: string): string => createHash('sha256').update(value, 'utf8').digest('hex');

/* ------------------------------------------------------------------ where entries live */

let dirPromise: Promise<string | null> | null = null;

/**
 * `<ingest.cacheDir>/highlight`, created on first use — or `null` when caching is off
 * (`ingest.reuse.enabled: false`, `FRZNFORGE_NO_HL_CACHE`, or no loadable config, which is the
 * case in unit tests that import the highlighter directly). `null` is not an error: the caller
 * just highlights.
 */
function cacheDir(): Promise<string | null> {
  return (dirPromise ??= (async () => {
    if (process.env.FRZNFORGE_NO_HL_CACHE) return null;
    try {
      const cfg = await loadConfig();
      if (!cfg.ingest.reuse.enabled) return null;
      const dir = path.join(cfg.cacheDir, 'highlight');
      await fs.mkdir(dir, { recursive: true });
      await sweepTemp(dir);
      return dir;
    } catch {
      return null;
    }
  })());
}

/* ------------------------------------------------------------------ fingerprint */

let fingerprintPromise: Promise<string> | null = null;

/**
 * Identity of the *highlighter*, as opposed to the content: Shiki's installed version plus a
 * hash of `canary()` — one small render through the real code path, which captures the themes,
 * the transformer and any change in Shiki's emitted markup. Both halves matter: the version
 * catches grammar updates the canary's tiny sample would not exercise, and the canary catches a
 * local theme edit that leaves the version untouched.
 */
export function highlightFingerprint(canary: () => string): Promise<string> {
  return (fingerprintPromise ??= (async () => {
    let version = 'unknown';
    try {
      version = (createRequire(import.meta.url)('shiki/package.json') as { version?: string }).version ?? 'unknown';
    } catch {
      /* keep 'unknown' — the canary still fingerprints the output shape */
    }
    return sha256(`${CACHE_FORMAT}\u0000${version}\u0000${canary()}`).slice(0, 32);
  })());
}

/* ------------------------------------------------------------------ the memo */

/**
 * In-flight renders only — **not** a second cache.
 *
 * Astro renders pages concurrently and the same file is highlighted once per ref it appears in,
 * so two pages can ask for the same key at the same moment; this coalesces them into one render
 * and one write. Entries are dropped as soon as they settle, which is what keeps the map bounded
 * by concurrency rather than by site size: holding every distinct page's HTML for the life of
 * the build would retain hundreds of MB on a large corpus (blob pages run to a 2.8 MB max), for
 * a saving the disk cache already provides at ~0.2 ms a read. It also keeps
 * `FRZNFORGE_NO_HL_CACHE` honest — with caching off, nothing is remembered at all, so a build
 * with the flag set is a true uncached control.
 */
const inFlight = new Map<string, Promise<string>>();

let tmpCounter = 0;

async function readEntry(dir: string, key: string): Promise<string | null> {
  try {
    return (await gunzipAsync(await fs.readFile(path.join(dir, `${key}.gz`)))).toString('utf8');
  } catch {
    // Missing, truncated or not gzip — treat every failure as a miss and recompute. A cache
    // that can only ever cost time, never correctness.
    return null;
  }
}

/**
 * Write through a temp file + rename so a reader never sees a half-written entry, and so two
 * concurrent renders of the same content (Astro renders pages in parallel) cannot interleave
 * into one file. Losing the race is fine — the winner's bytes are identical.
 *
 * A build killed between the write and the rename leaves its `.tmp` behind; `sweepTemp` below
 * collects those on the next run, since nothing else ever looks at this directory.
 */
async function writeEntry(dir: string, key: string, html: string): Promise<void> {
  const file = path.join(dir, `${key}.gz`);
  const tmp = `${file}.${process.pid}.${(tmpCounter += 1)}.tmp`;
  try {
    await fs.writeFile(tmp, await gzipAsync(Buffer.from(html, 'utf8')));
    await fs.rename(tmp, file);
  } catch {
    await fs.rm(tmp, { force: true }).catch(() => undefined);
  }
}

/**
 * Delete `.tmp` files left by an interrupted earlier build. Runs once per process, after the
 * directory is known; anything still being written by a *concurrent* build is left alone,
 * because its name carries that process's pid.
 *
 * Entries themselves are deliberately not pruned — see the note on growth in the module
 * docblock. This only reclaims the fragments no run will ever look at again.
 */
async function sweepTemp(dir: string): Promise<void> {
  try {
    const names = await fs.readdir(dir);
    await Promise.all(
      names
        .filter((n) => n.endsWith('.tmp') && !n.includes(`.${process.pid}.`))
        .map((n) => fs.rm(path.join(dir, n), { force: true }).catch(() => undefined)),
    );
  } catch {
    /* a cache we cannot list is a cache we simply do not tidy */
  }
}

/**
 * Return the memoized result for `key`, computing it with `render()` on a miss. `render` must
 * be pure: the whole design rests on a hit and a miss being indistinguishable in the output.
 * It may be async so the caller can defer setup work — loading a Shiki grammar, say — to the
 * miss path, where it is actually needed.
 */
export async function memoizeHighlight(key: string, render: () => string | Promise<string>): Promise<string> {
  const running = inFlight.get(key);
  if (running) return running;

  const task = (async (): Promise<string> => {
    const dir = await cacheDir();
    if (dir !== null) {
      const hit = await readEntry(dir, key);
      if (hit !== null) return hit;
    }
    const html = await render();
    if (dir !== null) await writeEntry(dir, key, html);
    return html;
  })();

  inFlight.set(key, task);
  // Drop the entry once it settles — on success to keep the map bounded, and on failure so one
  // bad render is not handed to every later page sharing the key. Attaching a handler here also
  // marks a rejection as observed; the caller still sees it through `task`.
  const forget = (): void => {
    if (inFlight.get(key) === task) inFlight.delete(key);
  };
  task.then(forget, forget);
  return task;
}

/**
 * Cache key for one highlight call: the highlighter's identity plus every rendering input.
 *
 * Fields are joined with NUL, which cannot occur in any of them, so the split between fields
 * is unambiguous. A printable separator would not be: with a space, (lang `ts`, prefix `x `)
 * and (lang `ts`, prefix `x`) over a source beginning with a space join to the same string and
 * so to the same key — two different renders sharing one entry. Today's callers happen never to
 * put a space in either field, but that is a property of the callers, and this is the function
 * that must not depend on it.
 */
export function highlightKey(fingerprint: string, lang: string, idPrefix: string, source: string): string {
  return sha256([fingerprint, lang, idPrefix, source].join('\u0000'));
}

/** Test seam: drop in-flight state, the resolved cache directory and the fingerprint. */
export function resetHighlightCache(): void {
  inFlight.clear();
  dirPromise = null;
  fingerprintPromise = null;
}
