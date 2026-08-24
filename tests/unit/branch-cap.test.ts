/**
 * `ingest.branchTrees` — the Phase 7 performance cap (schema v5).
 *
 * Tree, blob and raw pages are generated **per browsable ref**, so every extra branch with a
 * tree multiplies the page count by the size of that branch's tree. Four real remote repos
 * (27+22+7+2 branches) produced 15,988 pages before this cap existed; see
 * `docs/dev/performance.md` for the measured numbers.
 *
 * These tests drive the real `scanRepo` against fixture git repos and assert both halves of
 * the fix: that the artifact keeps the right refs (default branch always, then most recently
 * updated, tie-broken by name so two machines agree), and that `repoRoutes()` — the single
 * source of truth for what the build emits — actually shrinks. The route assertion is the
 * point of the whole change; the rest is bookkeeping.
 */
import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import { scanRepo, type ScanOptions } from '../../src/lib/ingest/scan';
import type { Repo } from '../../src/lib/data/schema';
import { browsableRefs, blobRoutes, rawRoutes, repoRoutes, treeRoutes } from '../../src/lib/routes';
import { FixtureRepo, at } from './helpers/fixture-repo';

/** Archives are off everywhere here: `git archive` per ref is slow and proves nothing about the cap. */
const base: ScanOptions = { maxBlobBytes: 1024 * 1024, maxCommits: null, archives: false };

async function scan(r: FixtureRepo, opts: Partial<ScanOptions> = {}): Promise<Repo> {
  const res = await scanRepo({ absPath: r.dir }, { ...base, ...opts });
  if ('skipped' in res) throw new Error(`unexpected skip: ${res.warning.message}`);
  return res.repo;
}

const cappedWarnings = (repo: Repo) => repo.warnings.filter((w) => w.code === 'branch-trees-capped');

describe('ingest.branchTrees caps browsable branches', () => {
  let r: FixtureRepo;

  // main is deliberately the OLDEST branch: the default branch must survive the cap on
  // identity, not on recency. Every other branch forks from main's single commit, so all six
  // refs have the same four-entry tree and the page arithmetic below is exact.
  beforeAll(() => {
    r = FixtureRepo.create('branch-cap');
    const c0 = r.writeAndCommit(
      { 'README.md': '# cap\n', 'src/a.ts': 'export const a = 1;\n', 'src/b.ts': 'export const b = 2;\n' },
      'base',
      { date: at(0) },
    );
    for (const [name, seconds] of [
      ['beta', 100],
      ['gamma', 200],
      ['delta', 300],
      ['epsilon', 400],
      ['zeta', 500],
    ] as const) {
      r.branch(name, c0);
      r.checkout(name);
      r.writeAndCommit({ [`${name}.txt`]: `${name}\n` }, `${name} work`, { date: at(seconds) });
      r.checkout('main');
    }
  });
  afterAll(() => r.cleanup());

  it('keeps the default branch plus the N most recently updated', async () => {
    const repo = await scan(r, { branchTrees: 2 });
    // zeta (at 500) and epsilon (at 400) are the newest two; refTrees is emitted name-sorted.
    expect(Object.keys(repo.refTrees)).toEqual(['epsilon', 'zeta']);
    // The default branch is `tree`/`files`, not a refTree — browsable refs are what matters.
    expect(browsableRefs(repo).map((b) => b.name)).toEqual(['main', 'epsilon', 'zeta']);
    expect(browsableRefs(repo)[0]).toMatchObject({ name: 'main', isDefault: true });
    expect(repo.tree.length).toBeGreaterThan(0); // stalest branch, still fully browsable
  });

  it('warns once with the browsable/total counts and the configured cap', async () => {
    const repo = await scan(r, { branchTrees: 2 });
    const warns = cappedWarnings(repo);
    expect(warns).toHaveLength(1);
    expect(warns[0]!.repo).toBe('branch-cap');
    // 3 = the 2 capped branches + the default branch, which is browsable and so must count.
    expect(warns[0]!.message).toContain('3 of 6 branches have browsable trees');
    expect(warns[0]!.message).toContain('ingest.branchTrees = 2');
  });

  it("branchTrees: 'all' keeps every branch and does not warn", async () => {
    const repo = await scan(r, { branchTrees: 'all' });
    expect(Object.keys(repo.refTrees)).toEqual(['beta', 'delta', 'epsilon', 'gamma', 'zeta']);
    expect(browsableRefs(repo)).toHaveLength(6);
    expect(cappedWarnings(repo)).toEqual([]);
  });

  it('branchTrees: 0 keeps only the default branch', async () => {
    const repo = await scan(r, { branchTrees: 0 });
    expect(repo.refTrees).toEqual({});
    expect(browsableRefs(repo).map((b) => b.name)).toEqual(['main']);
    // Still fully browsable at HEAD of the default branch — the cap removes refs, not content.
    expect(Object.keys(repo.files).sort()).toEqual(['README.md', 'src/a.ts', 'src/b.ts']);
    expect(cappedWarnings(repo)[0]!.message).toContain('1 of 6 branches');
  });

  it('does not warn when the cap is wide enough to drop nothing', async () => {
    for (const branchTrees of [5, 6, 100, 'all' as const]) {
      const repo = await scan(r, { branchTrees });
      expect(cappedWarnings(repo), `branchTrees: ${branchTrees}`).toEqual([]);
      expect(browsableRefs(repo)).toHaveLength(6);
    }
  });

  it('defaults to 10, which leaves a six-branch repo untouched', async () => {
    const repo = await scan(r); // no branchTrees ⇒ ScanOptions default
    expect(browsableRefs(repo)).toHaveLength(6);
    expect(cappedWarnings(repo)).toEqual([]);
  });

  it('drops tree/blob/raw routes in proportion to the refs it drops', async () => {
    const all = await scan(r, { branchTrees: 'all' });
    const capped = await scan(r, { branchTrees: 2 });
    const zero = await scan(r, { branchTrees: 0 });

    // Per ref: 2 tree routes (root + `src/`), 3 blobs, 3 raws = 8 pages.
    const perRef = (repo: Repo) => {
      const refs = browsableRefs(repo).length;
      return {
        refs,
        tree: treeRoutes(repo).length / refs,
        blob: blobRoutes(repo).length / refs,
        raw: rawRoutes(repo).length / refs,
      };
    };
    // Every ref carries the same tree, so the per-ref cost is identical across the three runs
    // and the totals differ only by the ref count. (beta…zeta each add one extra file.)
    expect(perRef(zero)).toEqual({ refs: 1, tree: 2, blob: 3, raw: 3 });
    expect(treeRoutes(all).length).toBe(2 * 6);
    expect(treeRoutes(capped).length).toBe(2 * 3);
    expect(blobRoutes(all).length).toBe(3 + 4 * 5); // main has 3 files, the others 4 each
    expect(blobRoutes(capped).length).toBe(3 + 4 * 2);

    // The headline: fewer refs ⇒ fewer pages, monotonically.
    expect(repoRoutes(capped).length).toBeLessThan(repoRoutes(all).length);
    expect(repoRoutes(zero).length).toBeLessThan(repoRoutes(capped).length);
    // Nothing but the per-ref pages moved: commits, branch listings and the repo shell are
    // all still generated for every branch, capped or not.
    const perRefPages = (repo: Repo) => treeRoutes(repo).length + blobRoutes(repo).length + rawRoutes(repo).length;
    expect(repoRoutes(all).length - repoRoutes(capped).length).toBe(perRefPages(all) - perRefPages(capped));
  });
});

describe('branchTrees tie-breaking is deterministic', () => {
  let r: FixtureRepo;

  beforeAll(() => {
    r = FixtureRepo.create('branch-cap-ties');
    const c0 = r.writeAndCommit({ 'f.txt': 'x\n' }, 'base', { date: at(0) });
    // tie-a and tie-b share a head date to the second; only the name can separate them.
    for (const name of ['tie-b', 'tie-a']) {
      r.branch(name, c0);
      r.checkout(name);
      r.writeAndCommit({ [`${name}.txt`]: 'y\n' }, `${name} work`, { date: at(300) });
      r.checkout('main');
    }
  });
  afterAll(() => r.cleanup());

  it('breaks a shared head date by name, ascending', async () => {
    const repo = await scan(r, { branchTrees: 1 });
    expect(repo.branches.find((b) => b.name === 'tie-a')!.head).not.toBe(
      repo.branches.find((b) => b.name === 'tie-b')!.head,
    );
    expect(Object.keys(repo.refTrees)).toEqual(['tie-a']);
  });

  it('picks the same branch on a repeat scan', async () => {
    const a = await scan(r, { branchTrees: 1 });
    const b = await scan(r, { branchTrees: 1 });
    expect(Object.keys(a.refTrees)).toEqual(Object.keys(b.refTrees));
    expect(JSON.stringify(a.refTrees)).toBe(JSON.stringify(b.refTrees));
  });
});
