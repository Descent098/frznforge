import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import { loadCommits } from '../../src/lib/ingest/commits';
import {
  computeInsights,
  DEFAULT_INSIGHTS_OPTIONS,
  type InsightsOptions,
} from '../../src/lib/ingest/insights';
import type { Commit } from '../../src/lib/data/schema';
// The site's own line-counting rule: insights must agree with what a blob page prints.
import { countLines } from '../../src/lib/highlight';
import { FixtureRepo } from './helpers/fixture-repo';

/** Insights options with the shipped defaults, overridden per test. */
function opts(over: Partial<InsightsOptions> = {}): InsightsOptions {
  return { ...DEFAULT_INSIGHTS_OPTIONS, ...over };
}

/** A blob with a NUL in the first 8000 bytes — git's own "binary" heuristic. */
const PNG = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x00, 0x00, 0x01, 0x02, 0x00, 0xff]);

describe('insights', () => {
  let repo: FixtureRepo;
  let commits: Record<string, Commit>;
  /** Newest first, exactly like `Branch.commits`. */
  let branchCommits: string[];
  let head: string;

  beforeAll(async () => {
    repo = FixtureRepo.create('insights');

    // 2024-01 — two commits, two distinct authors
    const c1 = repo.writeAndCommit({ 'src/a.txt': 'one\ntwo\n' }, 'a', { date: '2024-01-05T12:00:00Z' });
    const c2 = repo.writeAndCommit(
      {
        'src/b.txt': 'x\n',
        'assets/logo.png': PNG,
        'node_modules/pkg/index.js': 'vendored\n',
      },
      'b',
      { date: '2024-01-20T12:00:00Z', author: { name: 'Alice', email: 'alice@example.com' } },
    );
    // 2024-02 — deliberately empty, to prove the gap survives bucketing
    // 2024-03 — two commits, one author under two spellings of the same address
    const c3 = repo.writeAndCommit({ 'src/c.txt': 'a\nb\nc\n' }, 'c', { date: '2024-03-10T12:00:00Z' });
    const c4 = repo.writeAndCommit({ 'src/d.txt': 'd\n' }, 'd', {
      date: '2024-03-25T12:00:00Z',
      author: { name: 'Test User', email: 'TEST@Example.com' },
    });
    // 2024-04
    const c5 = repo.writeAndCommit({ 'README.md': '# hi\n' }, 'e', { date: '2024-04-02T12:00:00Z' });

    head = c5;
    branchCommits = [c5, c4, c3, c2, c1];
    commits = await loadCommits(repo.dir, branchCommits);
  });
  afterAll(() => repo.cleanup());

  const args = (over: Partial<InsightsOptions> = {}) => ({
    commits,
    branchCommits,
    head,
    options: opts(over),
  });

  it('buckets commits by UTC month, oldest first, with quiet months kept as zeros', async () => {
    const { insights } = await computeInsights(repo.dir, args());
    expect(insights).not.toBeNull();
    expect(insights!.commits).toEqual([
      { month: '2024-01', commits: 2, contributors: 2 },
      { month: '2024-02', commits: 0, contributors: 0 },
      { month: '2024-03', commits: 2, contributors: 1 },
      { month: '2024-04', commits: 1, contributors: 1 },
    ]);
  });

  it('counts contributors as distinct case-insensitive author emails per month', async () => {
    const { insights } = await computeInsights(repo.dir, args());
    const january = insights!.commits.find((p) => p.month === '2024-01')!;
    const march = insights!.commits.find((p) => p.month === '2024-03')!;
    // test@example.com + alice@example.com
    expect(january.contributors).toBe(2);
    // test@example.com twice (once as TEST@Example.com) — one person
    expect(march.commits).toBe(2);
    expect(march.contributors).toBe(1);
  });

  it('samples one checkpoint per month with commits, first and last included', async () => {
    const { insights, warnings } = await computeInsights(repo.dir, args());
    expect(warnings).toEqual([]);
    expect(insights!.codeSize.map((p) => p.month)).toEqual(['2024-01', '2024-03', '2024-04']);
    expect(insights!.sampled).toBe(false);
    expect(insights!.sampleCount).toBe(3);
    expect(insights!.approximate).toBe(false);
    // 2024-02 has no commits, so it has no tree to measure — it appears in `commits` only.
    expect(insights!.commits.map((p) => p.month)).toContain('2024-02');
    expect(insights!.codeSize.map((p) => p.month)).not.toContain('2024-02');
  });

  it('sums bytes over non-binary, non-vendored blobs and counts their newlines', async () => {
    const { insights } = await computeInsights(repo.dir, args());
    const [jan, mar, apr] = insights!.codeSize;
    // 2024-01 checkpoint is c2: src/a.txt (8B, 2 lines) + src/b.txt (2B, 1 line).
    // assets/logo.png is binary and node_modules/pkg/index.js is vendored — both excluded.
    expect(jan).toEqual({ month: '2024-01', bytes: 10, lines: 3 });
    // + src/c.txt (6B, 3 lines) + src/d.txt (2B, 1 line)
    expect(mar).toEqual({ month: '2024-03', bytes: 18, lines: 7 });
    // + README.md (5B, 1 line)
    expect(apr).toEqual({ month: '2024-04', bytes: 23, lines: 8 });
  });

  it('thins checkpoints to `samples`, keeping the oldest and the newest', async () => {
    const { insights } = await computeInsights(repo.dir, args({ samples: 2 }));
    expect(insights!.codeSize.map((p) => p.month)).toEqual(['2024-01', '2024-04']);
    expect(insights!.codeSize).toHaveLength(2);
    expect(insights!.sampleCount).toBe(2);
    expect(insights!.sampled).toBe(true);
    // the two it did measure are the same numbers the unthinned run produced
    expect(insights!.codeSize[0]).toEqual({ month: '2024-01', bytes: 10, lines: 3 });
    expect(insights!.codeSize[1]).toEqual({ month: '2024-04', bytes: 23, lines: 8 });
  });

  it('leaves the commit series untouched when code-size checkpoints are thinned', async () => {
    const full = await computeInsights(repo.dir, args());
    const thinned = await computeInsights(repo.dir, args({ samples: 1 }));
    expect(thinned.insights!.commits).toEqual(full.insights!.commits);
    expect(thinned.insights!.codeSize.map((p) => p.month)).toEqual(['2024-04']);
  });

  it('drops line counts and warns when a checkpoint exceeds maxBytesPerSample', async () => {
    const { insights, warnings } = await computeInsights(repo.dir, args({ maxBytesPerSample: 1 }));
    expect(insights!.approximate).toBe(true);
    expect(insights!.codeSize.every((p) => p.lines === null)).toBe(true);
    // nothing could be read, so nothing could be classified: every candidate blob counts,
    // the binary one included (vendored paths are still excluded — that needs no content)
    expect(insights!.codeSize.at(-1)).toEqual({ month: '2024-04', bytes: 33, lines: null });
    expect(warnings).toHaveLength(1);
    expect(warnings[0]!.code).toBe('insights-approximate');
    expect(warnings[0]!.repo).toBeNull();
    expect(warnings[0]!.message).toContain('maxBytesPerSample');
    expect(warnings[0]!.message).toContain('2024-01');
  });

  it('keeps exact line counts when the budget covers the checkpoint', async () => {
    const { insights, warnings } = await computeInsights(repo.dir, args({ maxBytesPerSample: 1024 }));
    expect(insights!.approximate).toBe(false);
    expect(warnings).toEqual([]);
    expect(insights!.codeSize.at(-1)).toEqual({ month: '2024-04', bytes: 23, lines: 8 });
  });

  it('is deterministic: two runs produce identical output', async () => {
    const first = await computeInsights(repo.dir, args());
    const second = await computeInsights(repo.dir, args());
    expect(JSON.stringify(second)).toBe(JSON.stringify(first));
  });

  /* ---- history order (regression) ------------------------------------------
   * Checkpoints used to be bucketed and ranked by authorDate, which a rebase or a
   * cherry-pick leaves in the past while the tree it produces is brand new. The series then
   * plotted a tree state in a month where it never existed, and could shrink going forward
   * in time. Checkpoints are now bucketed by commitDate and forced into history order.
   */
  it('keeps the code-size series in history order across a cherry-pick', async () => {
    const cherry = FixtureRepo.create('cherry');
    try {
      const base = cherry.writeAndCommit({ 'a.txt': 'a\n' }, 'base', { date: '2025-01-10T00:00:00Z' });
      const grow = cherry.writeAndCommit({ 'a.txt': 'a\n'.repeat(501) }, 'grow', { date: '2025-03-10T00:00:00Z' });
      // an older patch replayed on top of `grow`: February author date, April committer date
      const replayed = cherry.writeAndCommit({ 'a.txt': 'a\n'.repeat(502) }, 'cherry', {
        date: '2025-02-15T00:00:00Z',
        committerDate: '2025-04-01T00:00:00Z',
      });
      const branch = [replayed, grow, base];
      const { insights } = await computeInsights(cherry.dir, {
        commits: await loadCommits(cherry.dir, branch),
        branchCommits: branch,
        head: replayed,
        options: opts(),
      });
      // The tree only ever grew, so the series only ever grows — and the newest checkpoint is
      // the head, not the commit with the newest author date.
      expect(insights!.codeSize).toEqual([
        { month: '2025-01', bytes: 2, lines: 1 },
        { month: '2025-03', bytes: 1002, lines: 501 },
        { month: '2025-04', bytes: 1004, lines: 502 },
      ]);
      const sizes = insights!.codeSize.map((p) => p.bytes);
      expect([...sizes].sort((a, b) => a - b)).toEqual(sizes);
      // the commit series still reads the AUTHOR clock: February is where that patch was written
      expect(insights!.commits.map((p) => p.month)).toEqual(['2025-01', '2025-02', '2025-03']);
    } finally {
      cherry.cleanup();
    }
  });

  /* ---- line counting (regression) ------------------------------------------
   * `lines` used to count newline characters, so every file without a trailing newline was
   * one short of the "N lines" a blob page prints for the same file (`countLines()` in
   * src/lib/highlight.ts). The two must agree.
   */
  it('counts a file with no trailing newline the way the blob page does', async () => {
    const noeol = FixtureRepo.create('noeol');
    try {
      const sha = noeol.writeAndCommit(
        { 'with-eol.txt': 'alpha\nbeta\n', 'no-eol.txt': 'alpha\nbeta' },
        'both',
        { date: '2025-07-01T00:00:00Z' },
      );
      const { insights } = await computeInsights(noeol.dir, {
        commits: await loadCommits(noeol.dir, [sha]),
        branchCommits: [sha],
        head: sha,
        options: opts(),
      });
      // countLines('alpha\nbeta\n') === 2 and countLines('alpha\nbeta') === 2
      expect(countLines('alpha\nbeta\n') + countLines('alpha\nbeta')).toBe(4);
      expect(insights!.codeSize).toEqual([{ month: '2025-07', bytes: 21, lines: 4 }]);
    } finally {
      noeol.cleanup();
    }
  });

  it('returns null when disabled', async () => {
    expect(await computeInsights(repo.dir, args({ enabled: false }))).toEqual({ insights: null, warnings: [] });
  });

  it('returns null for an empty repo', async () => {
    const empty = FixtureRepo.create('empty');
    try {
      expect(
        await computeInsights(empty.dir, { commits: {}, branchCommits: null, head: null, options: opts() }),
      ).toEqual({ insights: null, warnings: [] });
      expect(
        await computeInsights(empty.dir, { commits: {}, branchCommits: [], head: null, options: opts() }),
      ).toEqual({ insights: null, warnings: [] });
    } finally {
      empty.cleanup();
    }
  });

  it('handles a single-commit repo', async () => {
    const solo = FixtureRepo.create('solo');
    try {
      const only = solo.writeAndCommit({ 'main.py': 'print(1)\n' }, 'only', { date: '2025-06-09T08:00:00Z' });
      const soloCommits = await loadCommits(solo.dir, [only]);
      const { insights, warnings } = await computeInsights(solo.dir, {
        commits: soloCommits,
        branchCommits: [only],
        head: only,
        options: opts(),
      });
      expect(warnings).toEqual([]);
      expect(insights).toEqual({
        commits: [{ month: '2025-06', commits: 1, contributors: 1 }],
        codeSize: [{ month: '2025-06', bytes: 9, lines: 1 }],
        sampled: false,
        sampleCount: 1,
        approximate: false,
      });
    } finally {
      solo.cleanup();
    }
  });

  it('handles a repo whose head tree is empty', async () => {
    const wiped = FixtureRepo.create('wiped');
    try {
      const a = wiped.writeAndCommit({ 'gone.txt': 'bye\n' }, 'add', { date: '2025-01-01T00:00:00Z' });
      wiped.rm('gone.txt');
      const b = wiped.commit('remove everything', { date: '2025-02-01T00:00:00Z' });
      const wipedCommits = await loadCommits(wiped.dir, [b, a]);
      const { insights } = await computeInsights(wiped.dir, {
        commits: wipedCommits,
        branchCommits: [b, a],
        head: b,
        options: opts(),
      });
      expect(insights!.codeSize).toEqual([
        { month: '2025-01', bytes: 4, lines: 1 },
        { month: '2025-02', bytes: 0, lines: 0 },
      ]);
    } finally {
      wiped.cleanup();
    }
  });
});
