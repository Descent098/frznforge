import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import { lastCommitByPath, listRootTree, listTree, scanTree } from '../../src/lib/ingest/tree';
import { FixtureRepo, at } from './helpers/fixture-repo';

describe('tree', () => {
  let repo: FixtureRepo;
  let c1: string;
  let c2: string;
  const png = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x00, 0x00, 0x01, 0x02, 0x00, 0xff]);

  beforeAll(() => {
    repo = FixtureRepo.create('tree');
    c1 = repo.writeAndCommit(
      {
        'README.md': '# Tree\n',
        'src/index.ts': 'export const a = 1;\n',
        'src/lib/util.ts': 'export const b = 2;\n',
        'assets/logo.png': png,
        'big.txt': 'x'.repeat(200) + '\n',
      },
      'initial',
      { date: at(0) },
    );
    c2 = repo.writeAndCommit({ 'src/lib/util.ts': 'export const b = 3;\n' }, 'touch util', { date: at(60) });
  });
  afterAll(() => repo.cleanup());

  it('lists nested entries sorted by path with sizes', async () => {
    const entries = await listTree(repo.dir, 'main');
    expect(entries.map((e) => e.path)).toEqual([
      'README.md',
      'assets',
      'assets/logo.png',
      'big.txt',
      'src',
      'src/index.ts',
      'src/lib',
      'src/lib/util.ts',
    ]);
    const util = entries.find((e) => e.path === 'src/lib/util.ts')!;
    expect(util).toMatchObject({ type: 'blob', mode: '100644', name: 'util.ts', size: 20 });
    const src = entries.find((e) => e.path === 'src')!;
    expect(src).toMatchObject({ type: 'tree', mode: '040000', size: null });
    const root = await listRootTree(repo.dir, 'main');
    expect(root.map((e) => e.path)).toEqual(['README.md', 'assets', 'big.txt', 'src']);
  });

  it('assigns lastCommit per path, directories take the newest child', async () => {
    const paths = ['README.md', 'src', 'src/index.ts', 'src/lib', 'src/lib/util.ts', 'assets', 'assets/logo.png'];
    const last = await lastCommitByPath(repo.dir, 'main', paths);
    expect(last.get('src/lib/util.ts')).toBe(c2);
    expect(last.get('src/lib')).toBe(c2);
    expect(last.get('src')).toBe(c2);
    expect(last.get('src/index.ts')).toBe(c1);
    expect(last.get('README.md')).toBe(c1);
    expect(last.get('assets')).toBe(c1);
  });

  it('scans files: binary detection, tooLarge, stored, languages, blobs', async () => {
    const res = await scanTree(repo.dir, repo.head(), { maxBlobBytes: 100 });
    expect(res.tree.map((e) => e.path)).toHaveLength(8);
    expect(res.tree.find((e) => e.path === 'src/lib/util.ts')!.lastCommit).toBe(c2);
    expect(res.tree.find((e) => e.path === 'src/index.ts')!.lastCommit).toBe(c1);

    expect(Object.keys(res.files)).toEqual(['README.md', 'assets/logo.png', 'big.txt', 'src/index.ts', 'src/lib/util.ts']);
    expect(res.files['assets/logo.png']).toMatchObject({ binary: true, tooLarge: false, stored: false, language: null, size: 10 });
    expect(res.files['big.txt']).toMatchObject({ binary: false, tooLarge: true, stored: false, language: 'Text', size: 201 });
    expect(res.files['src/index.ts']).toMatchObject({ binary: false, tooLarge: false, stored: true, language: 'TypeScript' });
    expect(res.files['README.md']).toMatchObject({ stored: true, language: 'Markdown' });

    const storedShas = Object.values(res.files).filter((f) => f.stored).map((f) => f.sha).sort();
    expect([...res.blobs.keys()].sort()).toEqual(storedShas);
    expect(res.blobs.get(res.files['src/index.ts']!.sha)!.toString('utf8')).toBe('export const a = 1;\n');
    expect(res.blobs.has(res.files['assets/logo.png']!.sha)).toBe(false);
    expect(res.blobs.has(res.files['big.txt']!.sha)).toBe(false);
  });

  it('detects binary for oversized files by sniffing a prefix', async () => {
    const r = FixtureRepo.create('bigbin');
    try {
      const big = Buffer.alloc(5000, 0x41);
      big[10] = 0;
      r.writeAndCommit({ 'blob.bin': big, 'text.txt': 'hello\n' }, 'init');
      const res = await scanTree(r.dir, r.head(), { maxBlobBytes: 10 });
      expect(res.files['blob.bin']).toMatchObject({ binary: true, tooLarge: true, stored: false });
      expect(res.files['text.txt']).toMatchObject({ binary: false, tooLarge: false, stored: true });
    } finally {
      r.cleanup();
    }
  });
});
