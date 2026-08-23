import { describe, expect, it } from 'vitest';
import type { Commit } from '../../src/lib/data/schema';
import { contributorsFromCommits } from '../../src/lib/ingest/contributors';

function commit(sha: string, name: string, email: string, date: string): Commit {
  return {
    sha: sha.padEnd(40, '0'),
    parents: [],
    author: { name, email },
    authorDate: date,
    committer: { name: 'C', email: 'c@example.com' },
    commitDate: date,
    subject: 's',
    body: '',
  };
}

describe('contributors', () => {
  it('groups by lower-cased email with most recent name, sorted by commits then name', () => {
    const res = contributorsFromCommits([
      commit('a', 'Alice', 'alice@example.com', '2024-01-01T00:00:00Z'),
      commit('b', 'Alice Smith', 'Alice@Example.com', '2024-01-03T00:00:00Z'),
      commit('c', 'Bob', 'bob@example.com', '2024-01-02T00:00:00Z'),
      commit('d', 'Alice S.', 'alice@example.com', '2024-01-02T00:00:00Z'),
      commit('e', 'Carol', 'carol@example.com', '2024-01-05T00:00:00Z'),
    ]);
    expect(res).toEqual([
      { name: 'Alice Smith', email: 'alice@example.com', commits: 3, firstCommit: '2024-01-01T00:00:00Z', lastCommit: '2024-01-03T00:00:00Z' },
      { name: 'Bob', email: 'bob@example.com', commits: 1, firstCommit: '2024-01-02T00:00:00Z', lastCommit: '2024-01-02T00:00:00Z' },
      { name: 'Carol', email: 'carol@example.com', commits: 1, firstCommit: '2024-01-05T00:00:00Z', lastCommit: '2024-01-05T00:00:00Z' },
    ]);
  });

  it('is empty for no commits', () => {
    expect(contributorsFromCommits([])).toEqual([]);
  });
});
