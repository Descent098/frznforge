import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import { detectDefaultBranch, listBranchRefs, loadBranches, loadTags } from '../../src/lib/ingest/refs';
import { FixtureRepo, at } from './helpers/fixture-repo';

describe('refs', () => {
  let repo: FixtureRepo;
  let c1: string;
  let c2: string;
  let c3: string;

  beforeAll(() => {
    repo = FixtureRepo.create('refs');
    c1 = repo.writeAndCommit({ 'a.txt': 'one\n' }, 'first', { date: at(0) });
    c2 = repo.writeAndCommit({ 'b.txt': 'two\n' }, 'second', { date: at(60) });
    repo.branch('feature');
    repo.checkout('feature');
    c3 = repo.writeAndCommit({ 'c.txt': 'three\n' }, 'third', { date: at(120) });
    repo.checkout('main');
    repo.tag('v1.0.0', { annotated: true, message: 'Release 1.0.0\n\nNotes here.\n', date: at(300) });
    repo.tag('lightweight');
  });
  afterAll(() => repo.cleanup());

  it('lists branches sorted by name with head sha and date', async () => {
    const refs = await listBranchRefs(repo.dir);
    expect(refs.map((r) => r.name)).toEqual(['feature', 'main']);
    expect(refs.find((r) => r.name === 'main')).toEqual({ name: 'main', head: c2, headDate: at(60) });
    expect(refs.find((r) => r.name === 'feature')!.head).toBe(c3);
  });

  it('detects the default branch from HEAD', async () => {
    const refs = await listBranchRefs(repo.dir);
    const def = await detectDefaultBranch(repo.dir, refs);
    expect(def).toEqual({ name: 'main', warnings: [] });
  });

  it('loads per-branch commit lists newest first', async () => {
    const refs = await listBranchRefs(repo.dir);
    const res = await loadBranches(repo.dir, refs, null);
    const main = res.branches.find((b) => b.name === 'main')!;
    const feature = res.branches.find((b) => b.name === 'feature')!;
    expect(main.commits).toEqual([c2, c1]);
    expect(main.lastCommitDate).toBe(at(60));
    expect(feature.commits).toEqual([c3, c2, c1]);
    expect(res.shas).toEqual(new Set([c1, c2, c3]));
    expect(res.warnings).toEqual([]);
  });

  it('parses annotated and lightweight tags', async () => {
    const tags = await loadTags(repo.dir);
    expect(tags.map((t) => t.name)).toEqual(['lightweight', 'v1.0.0']);
    const ann = tags[1]!;
    expect(ann).toEqual({
      name: 'v1.0.0',
      target: c2,
      annotated: true,
      message: 'Release 1.0.0\n\nNotes here.',
      tagger: { name: 'Test User', email: 'test@example.com' },
      date: at(300),
    });
    const lw = tags[0]!;
    expect(lw).toEqual({ name: 'lightweight', target: c2, annotated: false, message: null, tagger: null, date: at(60) });
  });

  it('falls back when HEAD is an unborn branch', async () => {
    const r = FixtureRepo.create('unborn');
    try {
      r.checkout('feature', true);
      r.writeAndCommit({ 'x.txt': 'x\n' }, 'on feature');
      r.setHead('main');
      const refs = await listBranchRefs(r.dir);
      const def = await detectDefaultBranch(r.dir, refs);
      expect(def.name).toBe('feature');
      expect(def.warnings.map((w) => w.code)).toEqual(['default-branch-fallback']);
    } finally {
      r.cleanup();
    }
  });

  it('prefers main, then master, then the most recent branch when HEAD is unusable', async () => {
    const r = FixtureRepo.create('detached');
    try {
      r.writeAndCommit({ 'x.txt': 'x\n' }, 'init', { date: at(0) });
      r.git('branch', '-m', 'main', 'zeta');
      r.branch('alpha');
      r.checkout('alpha');
      r.writeAndCommit({ 'y.txt': 'y\n' }, 'newer', { date: at(600) });
      r.setHead('nope');
      let def = await detectDefaultBranch(r.dir, await listBranchRefs(r.dir));
      expect(def.name).toBe('alpha'); // most recent commit
      r.branch('master', 'zeta');
      def = await detectDefaultBranch(r.dir, await listBranchRefs(r.dir));
      expect(def.name).toBe('master');
      r.branch('main', 'zeta');
      def = await detectDefaultBranch(r.dir, await listBranchRefs(r.dir));
      expect(def.name).toBe('main');
      expect(def.warnings[0]!.code).toBe('default-branch-fallback');
    } finally {
      r.cleanup();
    }
  });
});
