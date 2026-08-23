/**
 * Schema v2: per-ref trees (`refTrees`) and zip source archives (`archives`) from scanRepo.
 */
import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import { refSlug, scanRepo } from '../../src/lib/ingest/scan';
import { FixtureRepo, at } from './helpers/fixture-repo';

const base = { maxBlobBytes: 1024 * 1024, maxCommits: null };

async function scan(r: FixtureRepo, opts: Partial<typeof base> & { tagTrees?: number; archives?: boolean } = {}) {
  const res = await scanRepo({ absPath: r.dir }, { ...base, ...opts });
  if ('skipped' in res) throw new Error(`unexpected skip: ${res.warning.message}`);
  return res;
}

describe('refTrees + archives', () => {
  let r: FixtureRepo;
  let c1: string; // tagged v1 + rel/1.0
  let c2: string; // main head
  let cf: string; // feature head

  beforeAll(() => {
    r = FixtureRepo.create('refs');
    c1 = r.writeAndCommit({ 'shared.txt': 'shared\n', 'main-only.txt': 'v1\n' }, 'base', { date: at(0) });
    r.tag('v1', { annotated: true, message: 'one', date: at(30) });
    r.tag('rel/1.0', { date: at(45) });
    r.branch('feature', c1);
    c2 = r.writeAndCommit({ 'main-only.txt': 'v2\n' }, 'main work', { date: at(60) });
    r.checkout('feature');
    cf = r.writeAndCommit({ 'feature-only.ts': 'export const f = 1;\n' }, 'feature work', { date: at(120) });
    r.checkout('main');
  });
  afterAll(() => r.cleanup());

  it('builds trees for non-default branches and tags, never for the default branch', async () => {
    const { repo, blobs } = await scan(r);
    expect(Object.keys(repo.refTrees).sort()).toEqual(['feature', 'rel/1.0', 'v1']);
    expect(repo.refTrees['main']).toBeUndefined();

    const feature = repo.refTrees['feature']!;
    expect(feature).toMatchObject({ kind: 'branch', name: 'feature', commit: cf });
    expect(Object.keys(feature.files).sort()).toEqual(['feature-only.ts', 'main-only.txt', 'shared.txt']);
    // per-path lastCommit on the feature branch, not the default branch
    expect(feature.tree.find((e) => e.path === 'feature-only.ts')!.lastCommit).toBe(cf);
    expect(feature.tree.find((e) => e.path === 'shared.txt')!.lastCommit).toBe(c1);
    // the branch-only blob is in the shared blob store
    const fSha = feature.files['feature-only.ts']!.sha;
    expect(blobs.get(fSha)!.toString('utf8')).toBe('export const f = 1;\n');

    // both tags peel to c1 and share its tree
    for (const name of ['v1', 'rel/1.0']) {
      expect(repo.refTrees[name]).toMatchObject({ kind: 'tag', name, commit: c1 });
      expect(Object.keys(repo.refTrees[name]!.files).sort()).toEqual(['main-only.txt', 'shared.txt']);
    }
    expect(repo.warnings.map((w) => w.code)).not.toContain('tag-trees-capped');
  });

  it('tagTrees: 0 disables tag trees (branches keep theirs)', async () => {
    const { repo } = await scan(r, { tagTrees: 0 });
    expect(Object.keys(repo.refTrees)).toEqual(['feature']);
    expect(repo.archives.map((a) => a.ref)).toEqual(['main']); // only treed tags get archives
  });

  it('caps tag trees at the newest N and warns once', async () => {
    const { repo } = await scan(r, { tagTrees: 1 });
    // v1 (annotated, tagger date at(30)) beats rel/1.0 (lightweight ⇒ target commit date at(0))
    expect(Object.keys(repo.refTrees).sort()).toEqual(['feature', 'v1']);
    const capped = repo.warnings.filter((w) => w.code === 'tag-trees-capped');
    expect(capped).toHaveLength(1);
    expect(capped[0]!.message).toContain('1 of 2 tags');
  });

  it('produces zip archives for the default branch + treed tags', async () => {
    const { repo, archives } = await scan(r);
    expect(repo.archives.map((a) => [a.ref, a.kind, a.file])).toEqual([
      ['main', 'branch', 'archives/refs/main.zip'],
      ['rel/1.0', 'tag', 'archives/refs/rel~1.0.zip'], // '/' → '~'
      ['v1', 'tag', 'archives/refs/v1.zip'],
    ]);
    expect(repo.archives.find((a) => a.ref === 'main')!.commit).toBe(c2);
    const byFile = new Map(archives.map((a) => [a.file, a.data]));
    for (const a of repo.archives) {
      expect(a.bytes).toBeGreaterThan(22); // bigger than an empty zip
      const data = byFile.get(a.file)!;
      expect(data.length).toBe(a.bytes);
      expect(data.subarray(0, 2).toString('latin1')).toBe('PK'); // zip magic
    }
  });

  it('archives: false produces none', async () => {
    const { repo, archives } = await scan(r, { archives: false });
    expect(repo.archives).toEqual([]);
    expect(archives).toEqual([]);
    expect(Object.keys(repo.refTrees).length).toBeGreaterThan(0); // trees unaffected
  });

  it('empty repos get empty refTrees and archives', async () => {
    const e = FixtureRepo.create('empty-refs');
    try {
      const { repo, archives } = await scan(e);
      expect(repo.refTrees).toEqual({});
      expect(repo.archives).toEqual([]);
      expect(archives).toEqual([]);
    } finally {
      e.cleanup();
    }
  });

  it('refSlug replaces every slash', () => {
    expect(refSlug('main')).toBe('main');
    expect(refSlug('feat/a/b')).toBe('feat~a~b');
  });
});

describe('binary blob storage (schema v2)', () => {
  it('stores small binaries byte-identical; oversized ones stay out', async () => {
    const r = FixtureRepo.create('bin-store');
    try {
      const small = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d]);
      const big = Buffer.alloc(64, 0);
      r.writeAndCommit({ 'small.png': small, 'big.png': big }, 'init', { date: at(0) });
      const { repo, blobs } = await scan(r, { maxBlobBytes: 32 });
      const s = repo.files['small.png']!;
      expect(s).toMatchObject({ binary: true, tooLarge: false, stored: true });
      expect(blobs.get(s.sha)).toEqual(small);
      const b = repo.files['big.png']!;
      expect(b).toMatchObject({ binary: true, tooLarge: true, stored: false });
      expect(blobs.has(b.sha)).toBe(false);
    } finally {
      r.cleanup();
    }
  });
});
