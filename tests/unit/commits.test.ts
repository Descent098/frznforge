import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import { loadCommits } from '../../src/lib/ingest/commits';
import { listBranchRefs, loadBranches } from '../../src/lib/ingest/refs';
import { scanRepo } from '../../src/lib/ingest/scan';
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
