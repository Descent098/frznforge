import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import type { Repo } from '../../src/lib/data/schema';
import { scanRepo } from '../../src/lib/ingest/scan';
import { FixtureRepo, at } from './helpers/fixture-repo';

describe('uncommitted changes never leak', () => {
  let repo: FixtureRepo;
  let clean: Repo;
  let dirty: Repo;
  let dirtyBlobs: Map<string, Buffer>;

  beforeAll(async () => {
    repo = FixtureRepo.create('dirty');
    repo.writeAndCommit({ 'README.md': '# clean\n', 'src/app.ts': 'export const v = 1;\n', 'LICENSE': 'MIT License\n' }, 'init', {
      date: at(0),
    });
    const before = await scanRepo({ absPath: repo.dir }, { maxBlobBytes: 1024 * 1024, maxCommits: null });
    if ('skipped' in before) throw new Error('skipped');
    clean = before.repo;

    // untracked file
    repo.write({ 'src/untracked.py': 'print(1)\n' });
    // modified tracked file (not staged)
    repo.write({ 'src/app.ts': 'export const v = 2; // modified but not committed\n'.repeat(50) });
    // staged but not committed
    repo.write({ 'src/staged.go': 'package main\n' });
    repo.add('src/staged.go');
    // also: modified readme + license on disk, and a fake metadata file
    repo.write({ 'README.md': '# DIRTY\n', LICENSE: 'Apache License Version 2.0\n', '.frznforge.json': '{"name":"DIRTY"}' });

    const after = await scanRepo({ absPath: repo.dir }, { maxBlobBytes: 1024 * 1024, maxCommits: null });
    if ('skipped' in after) throw new Error('skipped');
    dirty = after.repo;
    dirtyBlobs = after.blobs;
  });
  afterAll(() => repo.cleanup());

  it('untracked and staged paths are absent from tree/files', () => {
    const paths = dirty.tree.map((e) => e.path);
    expect(paths).not.toContain('src/untracked.py');
    expect(paths).not.toContain('src/staged.go');
    expect(paths).not.toContain('.frznforge.json');
    expect(Object.keys(dirty.files)).toEqual(['LICENSE', 'README.md', 'src/app.ts']);
  });

  it('modified file keeps its committed content, size and sha', () => {
    const f = dirty.files['src/app.ts']!;
    expect(f.sha).toBe(clean.files['src/app.ts']!.sha);
    expect(f.size).toBe('export const v = 1;\n'.length);
    expect(dirtyBlobs.get(f.sha)!.toString('utf8')).toBe('export const v = 1;\n');
  });

  it('readme, license, metadata and languages reflect committed state only', () => {
    expect(dirty.readme!.content).toBe('# clean\n');
    expect(dirty.license).toEqual({ spdx: 'MIT', file: 'LICENSE', source: 'file' });
    expect(dirty.name).toBe('dirty');
    expect(dirty.languages).toEqual(clean.languages);
    expect(dirty.languages.map((l) => l.name)).toEqual(['TypeScript']);
  });

  it('the whole artifact is identical before and after dirtying the work tree', () => {
    expect(JSON.stringify(dirty)).toBe(JSON.stringify(clean));
  });
});
