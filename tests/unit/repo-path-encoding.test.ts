/**
 * Repo paths and ref names go into URLs verbatim unless they are encoded, and a committed
 * path may contain anything git allows. These tests pin both halves of the fix:
 *
 *  - percent-encoding, so `read me.md` yields a valid href instead of
 *    `href="/repos/x/blob/main/read me.md/"` (and so the build does not abort);
 *  - the `#`/`%` exclusion, because those two survive no static round-trip at all — the
 *    build writes `c%23-tips.md` to disk, so even a correctly encoded request 404s.
 *
 * The notes side of the same problem is covered in notes.test.ts (`isRawServable`).
 */
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import { resolveConfig } from '../../src/lib/config/index';
import type { ForgeData, Repo } from '../../src/lib/data/schema';
import { ingest } from '../../src/lib/ingest';
import {
  allRoutes,
  blobRoutes,
  blobUrl,
  encodePathSegments,
  isRawServable,
  rawRoutes,
  rawUrl,
  repoRoutes,
  treeRoutes,
  treeUrl,
} from '../../src/lib/routes';
import { FixtureRepo } from './helpers/fixture-repo';

/* ---- pure URL builders ---------------------------------------------------- */

describe('encodePathSegments', () => {
  it('encodes each segment but keeps the separators', () => {
    expect(encodePathSegments('read me.md')).toBe('read%20me.md');
    expect(encodePathSegments('dir with space/a b.ts')).toBe('dir%20with%20space/a%20b.ts');
    expect(encodePathSegments('a&b+c;d.txt')).toBe('a%26b%2Bc%3Bd.txt');
    expect(encodePathSegments('naïve/résumé.md')).toBe('na%C3%AFve/r%C3%A9sum%C3%A9.md');
  });

  it('leaves ordinary paths and ref slugs untouched', () => {
    expect(encodePathSegments('src/lib/index.ts')).toBe('src/lib/index.ts');
    // '~' is unreserved in RFC 3986, so the ref-slug scheme survives encoding unchanged.
    expect(encodePathSegments('feat~zip')).toBe('feat~zip');
  });

  it('round-trips: decoding a built URL gives the original path back', () => {
    for (const p of ['read me.md', 'a&b.ts', 'naïve.txt', 'x/y z/w.md']) {
      const built = blobUrl('r', 'main', p);
      const encoded = built.slice('/repos/r/blob/main/'.length, -1);
      expect(decodeURIComponent(encoded)).toBe(p);
    }
  });
});

describe('repo URL builders', () => {
  it('produce hrefs with no raw spaces (the reported invalid-attribute bug)', () => {
    for (const url of [
      treeUrl('x', 'main', 'dir with space'),
      blobUrl('x', 'main', 'read me.md'),
      rawUrl('x', 'main', 'read me.md'),
    ]) {
      expect(url).not.toMatch(/ /);
      expect(url).toContain('%20');
    }
  });

  it('encode the ref as well as the path', () => {
    expect(blobUrl('x', 'feat/a b', 'f.ts')).toBe('/repos/x/blob/feat~a%20b/f.ts/');
  });

  it('keep the existing shape for ordinary inputs', () => {
    expect(treeUrl('x', 'main')).toBe('/repos/x/tree/main/');
    expect(treeUrl('x', 'feat/zip', 'src')).toBe('/repos/x/tree/feat~zip/src/');
    expect(blobUrl('x', 'main', 'src/a.ts')).toBe('/repos/x/blob/main/src/a.ts/');
    expect(rawUrl('x', 'main', 'src/a.ts')).toBe('/repos/x/raw/main/src/a.ts');
  });
});

describe('isRawServable for repo paths', () => {
  it('accepts what encoding can rescue and rejects what it cannot', () => {
    expect(isRawServable('read me.md')).toBe(true);
    expect(isRawServable('a&b+c.ts')).toBe(true);
    expect(isRawServable('naïve.txt')).toBe(true);
    expect(isRawServable('50% off.txt')).toBe(false);
    expect(isRawServable('c#-tips.md')).toBe(false);
    expect(isRawServable('dir#1/a.md')).toBe(false);
  });
});

/* ---- through the real ingest ---------------------------------------------- */

describe('a repo with URL-hostile paths', () => {
  let fixture: FixtureRepo;
  let outDir: string;
  let data: ForgeData;
  let repo: Repo;

  const SPACED = 'docs/read me.md';
  const PERCENT = 'docs/50% off.txt';
  const HASH = 'docs/c#-tips.md';

  beforeAll(async () => {
    fixture = FixtureRepo.create('hostile');
    fixture.writeAndCommit(
      {
        'README.md': '# Hostile\n',
        'src/a.ts': 'export const a = 1;\n',
        [SPACED]: '# read me\n',
        [PERCENT]: 'fifty percent\n',
        [HASH]: '# c# tips\n',
        'dir with space/nested.ts': 'export const n = 1;\n',
      },
      'init',
    );

    outDir = fs.mkdtempSync(path.join(os.tmpdir(), 'frznforge-encode-'));
    const config = resolveConfig(
      {
        owner: { name: 'Tester', handle: 'tester' },
        repos: [{ type: 'local', path: fixture.dir }],
        ingest: { outDir, maxBlobBytes: 1024 * 1024, maxCommits: null, concurrency: 1 },
      },
      os.tmpdir(),
    );
    const res = await ingest(config);
    data = res.data;
    repo = data.repos[0]!;
  });

  afterAll(() => {
    fixture.cleanup();
    fs.rmSync(outDir, { recursive: true, force: true, maxRetries: 5 });
  });

  it('still ingests every path — the artifact is a faithful view of the commit', () => {
    const paths = repo.tree.map((e) => e.path);
    expect(paths).toContain(SPACED);
    expect(paths).toContain(PERCENT);
    expect(paths).toContain(HASH);
    expect(repo.files[SPACED]?.stored).toBe(true);
    expect(repo.files[PERCENT]?.stored).toBe(true);
  });

  it('warns once per unservable path, naming it, and not for the merely-spaced one', () => {
    const warned = repo.warnings.filter((w) => w.code === 'repo-path-unservable');
    expect(warned.length).toBeGreaterThanOrEqual(2);
    const text = warned.map((w) => w.message).join('\n');
    expect(text).toContain(PERCENT);
    expect(text).toContain(HASH);
    expect(text).not.toContain(SPACED);
    // repo-scoped, and mirrored into the site-level warning list like every other warning
    expect(warned.every((w) => w.repo === repo.slug)).toBe(true);
    expect(data.warnings.some((w) => w.code === 'repo-path-unservable')).toBe(true);
  });

  it('routes the spaced path (encoded) and omits the unservable ones entirely', () => {
    const blobs = blobRoutes(repo).map((b) => b.entry.path);
    expect(blobs).toContain(SPACED);
    expect(blobs).not.toContain(PERCENT);
    expect(blobs).not.toContain(HASH);

    const raws = rawRoutes(repo).map((r) => r.entry.path);
    expect(raws).toContain(SPACED);
    expect(raws).not.toContain(PERCENT);

    const trees = treeRoutes(repo).map((t) => t.path);
    expect(trees).toContain('dir with space');

    const urls = repoRoutes(repo);
    expect(urls).toContain(blobUrl(repo.slug, repo.defaultBranch!, SPACED));
    expect(urls).toContain('/repos/hostile/blob/main/docs/read%20me.md/');
    expect(urls.some((u) => u.includes('50%'))).toBe(false);
    expect(urls.some((u) => u.includes('c#'))).toBe(false);
  });

  it('emits no route that a static host could not serve', () => {
    for (const url of allRoutes(data)) {
      // A raw space or '#' in an href is either invalid markup or silently truncated by the
      // browser at the fragment; '%' that is not valid percent-encoding aborts the build.
      expect(url).not.toMatch(/ /);
      expect(url).not.toContain('#');
      expect(() => decodeURIComponent(url)).not.toThrow();
      expect(() => new URL(url, 'https://example.com')).not.toThrow();
    }
  });
});
