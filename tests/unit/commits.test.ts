import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import { loadCommits } from '../../src/lib/ingest/commits';
import { listBranchRefs, loadBranches } from '../../src/lib/ingest/refs';
import { scanRepo } from '../../src/lib/ingest/scan';
import { commitUrl, repoRoutes } from '../../src/lib/routes';
import { FixtureRepo, at } from './helpers/fixture-repo';

describe('commits', () => {
  let repo: FixtureRepo;
  let c1: string;
  let c2: string;
  let c3: string;

  beforeAll(() => {
    repo = FixtureRepo.create('commits');
    c1 = repo.writeAndCommit({ 'a.txt': 'a\n' }, 'first commit', { date: '2024-03-01T12:00:00+02:00' });
    c2 = repo.writeAndCommit({ 'b.txt': 'b\n' }, 'Subject line\n\nBody paragraph one.\n\nBody paragraph two.\n', {
      date: at(60),
      author: { name: 'Alice Author', email: 'Alice@Example.com' },
    });
    c3 = repo.writeAndCommit({ 'c.txt': 'c\n' }, 'third', { date: at(120) });
  });
  afterAll(() => repo.cleanup());

  it('loads commits with parents, people, dates (UTC Z), subject and body', async () => {
    const commits = await loadCommits(repo.dir, [c1, c2, c3]);
    expect(Object.keys(commits).sort()).toEqual([c1, c2, c3].sort());
    expect(Object.keys(commits)).toEqual([...Object.keys(commits)].sort()); // sorted insertion

    const first = commits[c1]!;
    expect(first.parents).toEqual([]);
    expect(first.authorDate).toBe('2024-03-01T10:00:00Z'); // +02:00 normalised
    expect(first.commitDate).toBe('2024-03-01T10:00:00Z');
    expect(first.subject).toBe('first commit');
    expect(first.body).toBe('');
    expect(first.committer).toEqual({ name: 'Test User', email: 'test@example.com' });

    const second = commits[c2]!;
    expect(second.parents).toEqual([c1]);
    expect(second.author).toEqual({ name: 'Alice Author', email: 'Alice@Example.com' });
    expect(second.subject).toBe('Subject line');
    expect(second.body).toBe('Body paragraph one.\n\nBody paragraph two.');
    expect(second.authorDate).toBe(at(60));

    expect(commits[c3]!.parents).toEqual([c2]);

    expect(first.files).toEqual([{ path: 'a.txt', additions: 1, deletions: 0 }]);
    expect(first.stats).toEqual({ filesChanged: 1, additions: 1, deletions: 0 });
    expect(second.files).toEqual([{ path: 'b.txt', additions: 1, deletions: 0 }]);
  });

  it('returns an empty record for no shas', async () => {
    expect(await loadCommits(repo.dir, [])).toEqual({});
  });

  it('caps per-branch commit lists and warns once', async () => {
    const refs = await listBranchRefs(repo.dir);
    const res = await loadBranches(repo.dir, refs, 2);
    expect(res.branches[0]!.commits).toEqual([c3, c2]);
    expect(res.shas).toEqual(new Set([c2, c3]));
    expect(res.warnings.map((w) => w.code)).toEqual(['commits-capped']);

    const scanned = await scanRepo({ absPath: repo.dir }, { maxBlobBytes: 1024, maxCommits: 2 });
    if ('skipped' in scanned) throw new Error('unexpected skip');
    expect(scanned.repo.commitCount).toBe(2);
    expect(Object.keys(scanned.repo.commits).sort()).toEqual([c2, c3].sort());
    expect(scanned.repo.warnings.filter((w) => w.code === 'commits-capped')).toHaveLength(1);
  });

  it('does not cap when under the limit', async () => {
    const refs = await listBranchRefs(repo.dir);
    const res = await loadBranches(repo.dir, refs, 3);
    expect(res.branches[0]!.commits).toHaveLength(3);
    expect(res.warnings).toEqual([]);
  });
});

describe('maxCommitAgeDays (ingest timeframe limit)', () => {
  let r: FixtureRepo;
  let d0: string; // day 0
  let d10: string; // day 10
  let d40: string; // day 40 — newest, the anchor
  const day = (n: number) => at(n * 86_400);

  beforeAll(() => {
    r = FixtureRepo.create('aged');
    d0 = r.writeAndCommit({ 'a.txt': 'a\n' }, 'day zero', { date: day(0) });
    d10 = r.writeAndCommit({ 'b.txt': 'b\n' }, 'day ten', { date: day(10) });
    d40 = r.writeAndCommit({ 'c.txt': 'c\n' }, 'day forty', { date: day(40) });
    // a stale branch whose only commit predates any reasonable cutoff
    r.checkout('stale', true);
    r.git('reset', '-q', '--hard', d0);
    r.checkout('main');
  });
  afterAll(() => r.cleanup());

  it('keeps only commits inside the window, anchored to the newest commit, and warns once', async () => {
    const refs = await listBranchRefs(r.dir);
    const res = await loadBranches(r.dir, refs, null, 15); // cutoff = day 25
    const main = res.branches.find((b) => b.name === 'main')!;
    expect(main.commits).toEqual([d40]);
    expect(res.warnings.map((w) => w.code)).toEqual(['commits-aged-out']);
  });

  it('a branch whose commits all predate the cutoff keeps its head commit', async () => {
    const refs = await listBranchRefs(r.dir);
    const res = await loadBranches(r.dir, refs, null, 15);
    const stale = res.branches.find((b) => b.name === 'stale')!;
    expect(stale.commits).toEqual([d0]);
    expect(stale.lastCommitDate).toBe(day(0));
  });

  it('is deterministic across runs and does not warn when nothing is dropped', async () => {
    const refs = await listBranchRefs(r.dir);
    const wide = await loadBranches(r.dir, refs, null, 50); // cutoff before day 0
    expect(wide.branches.find((b) => b.name === 'main')!.commits).toEqual([d40, d10, d0]);
    expect(wide.warnings).toEqual([]);
    const again = await loadBranches(r.dir, refs, null, 50);
    expect(again).toEqual(wide);
    const narrow1 = await loadBranches(r.dir, refs, null, 35); // cutoff = day 5
    const narrow2 = await loadBranches(r.dir, refs, null, 35);
    expect(narrow1).toEqual(narrow2);
    expect(narrow1.branches.find((b) => b.name === 'main')!.commits).toEqual([d40, d10]);
  });

  it('composes with maxCommits (count cap wins; capped runs report commits-capped)', async () => {
    const refs = await listBranchRefs(r.dir);
    const res = await loadBranches(r.dir, refs, 1, 35); // filter keeps 2, cap keeps 1
    expect(res.branches.find((b) => b.name === 'main')!.commits).toEqual([d40]);
    expect(res.warnings.map((w) => w.code)).toContain('commits-capped');
  });

  it('narrows everything scanRepo derives from the commit list', async () => {
    const scanned = await scanRepo(
      { absPath: r.dir },
      { maxBlobBytes: 1024, maxCommits: null, maxCommitAgeDays: 35 },
    );
    if ('skipped' in scanned) throw new Error('unexpected skip');
    // d0 survives only through the stale branch's keep-the-head rule
    expect(Object.keys(scanned.repo.commits).sort()).toEqual([d0, d10, d40].sort());
    const main = scanned.repo.branches.find((b) => b.name === 'main')!;
    expect(main.commits).toEqual([d40, d10]);
    expect(scanned.repo.warnings.filter((w) => w.code === 'commits-aged-out')).toHaveLength(1);
    // insights bucket only the kept commits of the default branch: day 10 and day 40.
    // Per-month COUNTS, not just labels — d0 shares a month with d10, so an un-narrowed
    // bucketing would still produce the same first/last labels but report 2024-01 as 2.
    expect(scanned.repo.insights).not.toBeNull();
    expect(scanned.repo.insights!.commits.map((p) => [p.month, p.commits])).toEqual([
      [day(10).slice(0, 7), 1],
      [day(40).slice(0, 7), 1],
    ]);
  });

  it('keeps a clock-skewed head that the filter alone would drop', async () => {
    // A head whose COMMITTER date predates the cutoff while its parent sits inside the
    // window: `--since-as-filter` drops the head but keeps the parent, and without the
    // containment guard `Branch.head` would be missing from its own commit list (blank
    // branches-page cell; no head-commit bar when it is the default branch).
    r.checkout('skew', true);
    r.git('reset', '-q', '--hard', d10);
    r.writeAndCommit({ 'skew.txt': 's\n' }, 'skewed tip', { date: day(30), committerDate: day(1) });
    const skewHead = r.head();
    r.checkout('main');

    const refs = await listBranchRefs(r.dir);
    const res = await loadBranches(r.dir, refs, null, 35); // anchor day 40 → cutoff day 5
    const skew = res.branches.find((b) => b.name === 'skew')!;
    expect(skew.commits[0]).toBe(skewHead);
    expect(skew.commits).toContain(d10);
    expect(res.shas.has(skewHead)).toBe(true);
  });
});

describe('extraCommits (display-support commits, schema v6)', () => {
  let r: FixtureRepo;
  let old: string;
  let head: string;
  const day = (n: number) => at(n * 86_400);

  beforeAll(() => {
    r = FixtureRepo.create('extra');
    old = r.writeAndCommit({ 'old.txt': 'old\n' }, 'old file', { date: day(0) });
    r.tag('v-old', { annotated: true, message: 'ancient', date: day(0) });
    head = r.writeAndCommit({ 'new.txt': 'new\n' }, 'new file', { date: day(40) });
  });
  afterAll(() => r.cleanup());

  it('holds per-path last commits and tag targets the window dropped, without touching aggregates', async () => {
    const scanned = await scanRepo(
      { absPath: r.dir },
      { maxBlobBytes: 1024, maxCommits: null, maxCommitAgeDays: 15 }, // cutoff = day 25
    );
    if ('skipped' in scanned) throw new Error('unexpected skip');
    // kept history: the head only — and every aggregate derives from it alone
    expect(Object.keys(scanned.repo.commits)).toEqual([head]);
    expect(scanned.repo.commitCount).toBe(1);
    expect(scanned.repo.createdAt).toBe(day(40));
    // ...while old.txt's last commit (also the v-old tag target) stays resolvable for display
    expect(Object.keys(scanned.repo.extraCommits)).toEqual([old]);
    const oldEntry = scanned.repo.tree.find((e) => e.path === 'old.txt')!;
    expect(scanned.repo.extraCommits[oldEntry.lastCommit]).toBeTruthy();
    // and it gets a commit page, so the file-table and tag-row links resolve
    expect(repoRoutes(scanned.repo)).toContain(commitUrl(scanned.repo.slug, old));
  });

  it('is empty when nothing is out of reach (no limits, all tags on branch history)', async () => {
    const scanned = await scanRepo({ absPath: r.dir }, { maxBlobBytes: 1024, maxCommits: null });
    if ('skipped' in scanned) throw new Error('unexpected skip');
    expect(scanned.repo.extraCommits).toEqual({});
  });

  it('also carries a tag target no branch reaches, even with no limits configured', async () => {
    // A rebase-orphaned release tag is routine git state: commit on a branch, tag it,
    // delete the branch. `commits` only holds branch-reachable history, so without the
    // extras sweep this tag's target would have no commit data and no page.
    r.checkout('doomed', true);
    const orphan = r.writeAndCommit({ 'orphan.txt': 'o\n' }, 'orphaned work', { date: day(20) });
    r.tag('v-orphan', { annotated: true, message: 'tagged then orphaned', date: day(20) });
    r.checkout('main');
    r.git('branch', '-D', 'doomed');

    const scanned = await scanRepo({ absPath: r.dir }, { maxBlobBytes: 1024, maxCommits: null });
    if ('skipped' in scanned) throw new Error('unexpected skip');
    expect(scanned.repo.commits[orphan]).toBeUndefined();
    expect(scanned.repo.extraCommits[orphan]).toBeTruthy();
    expect(repoRoutes(scanned.repo)).toContain(commitUrl(scanned.repo.slug, orphan));
  });
});

describe('commit numstat (files + stats)', () => {
  let r: FixtureRepo;
  let root: string;
  let multi: string;
  let bin: string;
  let merge: string;

  beforeAll(() => {
    r = FixtureRepo.create('numstat');
    root = r.writeAndCommit(
      { 'a.txt': 'one\ntwo\n', 'b.txt': 'x\n', 'img.bin': Buffer.from([0, 1, 2]) },
      'root',
      { date: at(0) },
    );
    multi = r.writeAndCommit(
      { 'a.txt': 'one\nTWO\nthree\n', 'b.txt': 'x\ny\n' },
      'touch two files',
      { date: at(60) },
    );
    bin = r.writeAndCommit({ 'img.bin': Buffer.from([0, 9]) }, 'binary change', { date: at(120) });
    r.checkout('side', true);
    r.git('reset', '-q', '--hard', root);
    r.writeAndCommit({ 'side.txt': 'side\n' }, 'side work', { date: at(180) });
    r.checkout('main');
    r.gitWith({ GIT_AUTHOR_DATE: at(240), GIT_COMMITTER_DATE: at(240) }, 'merge', '--no-ff', '--no-edit', '-q', 'side');
    merge = r.head();
  });
  afterAll(() => r.cleanup());

  it('root commit counts every file as additions', async () => {
    const commits = await loadCommits(r.dir, [root]);
    const c = commits[root]!;
    expect(c.files).toEqual([
      { path: 'a.txt', additions: 2, deletions: 0 },
      { path: 'b.txt', additions: 1, deletions: 0 },
      { path: 'img.bin', additions: null, deletions: null },
    ]);
    expect(c.stats).toEqual({ filesChanged: 3, additions: 3, deletions: 0 });
  });

  it('multi-file commit has per-file +/- counts and summed stats', async () => {
    const commits = await loadCommits(r.dir, [multi]);
    const c = commits[multi]!;
    expect(c.files).toEqual([
      { path: 'a.txt', additions: 2, deletions: 1 },
      { path: 'b.txt', additions: 1, deletions: 0 },
    ]);
    expect(c.stats).toEqual({ filesChanged: 2, additions: 3, deletions: 1 });
  });

  it('binary changes get null counts but still count toward filesChanged', async () => {
    const commits = await loadCommits(r.dir, [bin]);
    const c = commits[bin]!;
    expect(c.files).toEqual([{ path: 'img.bin', additions: null, deletions: null }]);
    expect(c.stats).toEqual({ filesChanged: 1, additions: 0, deletions: 0 });
  });

  it('merge commits have no numstat records (documented)', async () => {
    const commits = await loadCommits(r.dir, [merge]);
    const c = commits[merge]!;
    expect(c.parents).toHaveLength(2);
    expect(c.files).toEqual([]);
    expect(c.stats).toEqual({ filesChanged: 0, additions: 0, deletions: 0 });
  });
});
