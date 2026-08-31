/**
 * The cross-run highlight memo (0.2.0).
 *
 * The whole design rests on one property: **a hit and a miss are indistinguishable in the
 * output**. Highlighting is 84% of this site's render, so it is memoized between builds — but
 * a memo that could ever return different bytes than a fresh render would be the
 * "silently wrong page" failure performance.md rejects skip-unchanged-pages over. These tests
 * hold that line, plus the invalidation rules that keep it true across a Shiki upgrade.
 *
 * `FRZNFORGE_CACHE_DIR` points the cache at a temp dir (the same override the e2e harness
 * uses), so nothing here touches the project's real cache.
 */
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { gzipSync } from 'node:zlib';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { highlightToHtml } from '../../src/lib/highlight';
import { highlightKey, memoizeHighlight, resetHighlightCache } from '../../src/lib/highlight-cache';

const SOURCE = [
  'export function add(a: number, b: number): number {',
  '  // a comment, so the theme has something to colour',
  '  return a + b;',
  '}',
  '',
].join('\n');

let tmp: string;
let previousCacheDir: string | undefined;
let previousDisable: string | undefined;

beforeEach(async () => {
  tmp = await fs.mkdtemp(path.join(os.tmpdir(), 'frznforge-hl-'));
  previousCacheDir = process.env.FRZNFORGE_CACHE_DIR;
  previousDisable = process.env.FRZNFORGE_NO_HL_CACHE;
  process.env.FRZNFORGE_CACHE_DIR = tmp;
  delete process.env.FRZNFORGE_NO_HL_CACHE;
  resetHighlightCache();
});

afterEach(async () => {
  if (previousCacheDir === undefined) delete process.env.FRZNFORGE_CACHE_DIR;
  else process.env.FRZNFORGE_CACHE_DIR = previousCacheDir;
  if (previousDisable === undefined) delete process.env.FRZNFORGE_NO_HL_CACHE;
  else process.env.FRZNFORGE_NO_HL_CACHE = previousDisable;
  resetHighlightCache();
  await fs.rm(tmp, { recursive: true, force: true });
});

/** Entries written so far (`<key>.gz`), ignoring any in-flight temp files. */
async function entries(): Promise<string[]> {
  const dir = path.join(tmp, 'highlight');
  const names = await fs.readdir(dir).catch(() => [] as string[]);
  return names.filter((n) => n.endsWith('.gz')).sort();
}

describe('the highlight memo', () => {
  it('serves a cached result identical to the freshly rendered one', async () => {
    const fresh = await highlightToHtml(SOURCE, 'TypeScript', 'src/add.ts');
    expect(await entries()).toHaveLength(1);

    // Drop the in-process memo but keep the file: the next call must come off disk.
    resetHighlightCache();
    process.env.FRZNFORGE_CACHE_DIR = tmp;
    const cached = await highlightToHtml(SOURCE, 'TypeScript', 'src/add.ts');

    expect(cached).toBe(fresh);
    expect(cached).toContain('shiki');
    expect(await entries()).toHaveLength(1); // a hit writes nothing new
  });

  /**
   * The test that actually proves the *cross-run* half works. Everything else in this file is
   * satisfied by a pure re-render, so all of it would still pass if the disk read were deleted;
   * this one cannot be, because it plants a value that no render could produce.
   */
  it('really reads from disk — a planted entry is served verbatim', async () => {
    const real = await highlightToHtml(SOURCE, 'TypeScript', 'src/add.ts');
    const [only] = await entries();
    const sentinel = '<pre class="shiki">PLANTED — only a disk read can return this</pre>';
    await fs.writeFile(path.join(tmp, 'highlight', only!), gzipSync(Buffer.from(sentinel, 'utf8')));

    resetHighlightCache();
    process.env.FRZNFORGE_CACHE_DIR = tmp;
    const served = await highlightToHtml(SOURCE, 'TypeScript', 'src/add.ts');
    expect(served).toBe(sentinel);
    expect(served).not.toBe(real);
  });

  it('is fully off when disabled — no disk, and no in-process reuse either', async () => {
    // The flag is the control used to prove the memo changes nothing, so it has to disable
    // *everything*: an in-process memo left running would make the "uncached" build not one.
    process.env.FRZNFORGE_NO_HL_CACHE = '1';
    resetHighlightCache();
    let renders = 0;
    const count = (): string => {
      renders += 1;
      return '<pre>x</pre>';
    };
    await memoizeHighlight('same-key', count);
    await memoizeHighlight('same-key', count);
    expect(renders).toBe(2); // nothing was remembered between the two calls
    expect(await entries()).toHaveLength(0);
  });

  it('honours ingest.reuse.enabled: false, not just the env flag', async () => {
    // Two independent off-switches. Only the env one is reachable from this process, so the
    // config gate is exercised against a stubbed loader on a freshly imported module.
    vi.resetModules();
    vi.doMock('../../src/lib/config/index', () => ({
      loadConfig: async () => ({ cacheDir: tmp, ingest: { reuse: { enabled: false } } }),
    }));
    try {
      const fresh = await import('../../src/lib/highlight-cache');
      let renders = 0;
      const render = (): string => {
        renders += 1;
        return '<pre>gated</pre>';
      };
      await fresh.memoizeHighlight('gated-key', render);
      await fresh.memoizeHighlight('gated-key', render);
      expect(renders).toBe(2);
      expect(await entries()).toHaveLength(0);
    } finally {
      vi.doUnmock('../../src/lib/config/index');
      vi.resetModules();
    }
  });

  it('writes nothing and still highlights correctly when disabled', async () => {
    process.env.FRZNFORGE_NO_HL_CACHE = '1';
    resetHighlightCache();
    const off = await highlightToHtml(SOURCE, 'TypeScript', 'src/add.ts');
    expect(await entries()).toHaveLength(0);

    delete process.env.FRZNFORGE_NO_HL_CACHE;
    resetHighlightCache();
    const on = await highlightToHtml(SOURCE, 'TypeScript', 'src/add.ts');
    expect(on).toBe(off); // the cache is invisible in the output, both ways
  });

  it('recomputes rather than serving a corrupt entry', async () => {
    const fresh = await highlightToHtml(SOURCE, 'TypeScript', 'src/add.ts');
    const [only] = await entries();
    await fs.writeFile(path.join(tmp, 'highlight', only!), 'not gzip at all', 'utf8');

    resetHighlightCache();
    process.env.FRZNFORGE_CACHE_DIR = tmp;
    expect(await highlightToHtml(SOURCE, 'TypeScript', 'src/add.ts')).toBe(fresh);
  });

  it('keys on the line-id prefix, which changes the emitted ids', async () => {
    const plain = await highlightToHtml(SOURCE, 'TypeScript', 'src/add.ts');
    const prefixed = await highlightToHtml(SOURCE, 'TypeScript', 'src/add.ts', 'f-add-ts-');
    expect(plain).not.toBe(prefixed);
    expect(plain).toContain('id="L1"');
    expect(prefixed).toContain('id="f-add-ts-L1"');
    expect(await entries()).toHaveLength(2);
  });

  it('keys on the language, so the same bytes in two languages do not collide', async () => {
    const ts = await highlightToHtml(SOURCE, 'TypeScript', 'src/add.ts');
    const plain = await highlightToHtml(SOURCE, null, 'src/add.unknown');
    expect(ts).not.toBe(plain);
    expect(await entries()).toHaveLength(2);
  });

  it('reuses one entry for the same file highlighted under several refs', async () => {
    // The multiplier this memo exists for: one file × N browsable refs = N identical calls.
    const first = await highlightToHtml(SOURCE, 'TypeScript', 'src/add.ts');
    for (let i = 0; i < 5; i += 1) {
      expect(await highlightToHtml(SOURCE, 'TypeScript', 'src/add.ts')).toBe(first);
    }
    expect(await entries()).toHaveLength(1);
  });
});

describe('cache keys', () => {
  it('change with every input, so a Shiki upgrade cannot serve last version’s colours', () => {
    const base = highlightKey('fingerprint-a', 'typescript', '', SOURCE);
    expect(highlightKey('fingerprint-b', 'typescript', '', SOURCE)).not.toBe(base); // shiki/theme
    expect(highlightKey('fingerprint-a', 'javascript', '', SOURCE)).not.toBe(base); // language
    expect(highlightKey('fingerprint-a', 'typescript', 'p-', SOURCE)).not.toBe(base); // id prefix
    expect(highlightKey('fingerprint-a', 'typescript', '', `${SOURCE} `)).not.toBe(base); // source
    expect(highlightKey('fingerprint-a', 'typescript', '', SOURCE)).toBe(base); // stable
  });

  it('cannot be forged by moving a separator between fields', () => {
    // The real repartition: with a *space* separator these two join to the identical string
    // ("f ts x  SRC") and collide — two different line-id prefixes sharing one entry. Only a
    // separator that cannot occur inside a field keeps them apart.
    expect(highlightKey('f', 'ts', 'x ', 'SRC')).not.toBe(highlightKey('f', 'ts', 'x', ' SRC'));
    // The same shape one field to the left.
    expect(highlightKey('f', 'ts ', 'x', 'SRC')).not.toBe(highlightKey('f', 'ts', ' x', 'SRC'));
  });
});

describe('memoizeHighlight', () => {
  it('computes once per key and reuses the result', async () => {
    let calls = 0;
    const render = (): string => {
      calls += 1;
      return `<pre>${calls}</pre>`;
    };
    const a = await memoizeHighlight('key-one', render);
    const b = await memoizeHighlight('key-one', render);
    expect(a).toBe(b);
    expect(calls).toBe(1);

    await memoizeHighlight('key-two', render);
    expect(calls).toBe(2);
  });

  it('forgets a failed render instead of replaying the failure all build', async () => {
    let attempt = 0;
    const flaky = (): string => {
      attempt += 1;
      if (attempt === 1) throw new Error('shiki blew up');
      return '<pre>recovered</pre>';
    };
    await expect(memoizeHighlight('flaky', flaky)).rejects.toThrow('shiki blew up');
    // A later page sharing the key must get a real attempt, not the cached rejection.
    expect(await memoizeHighlight('flaky', flaky)).toBe('<pre>recovered</pre>');
    expect(attempt).toBe(2);
  });

  it('does not interleave two concurrent renders of the same key', async () => {
    let calls = 0;
    const render = (): string => {
      calls += 1;
      return '<pre>x</pre>';
    };
    const [a, b] = await Promise.all([memoizeHighlight('same', render), memoizeHighlight('same', render)]);
    expect(a).toBe(b);
    expect(calls).toBe(1);
  });
});
