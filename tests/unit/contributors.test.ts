import { describe, expect, it } from 'vitest';
import type { Commit } from '../../src/lib/data/schema';
import { contributorIndex, contributorsFromCommits, unmatchedContributors } from '../../src/lib/ingest/contributors';
import { PublicPath, type ContributorConfig } from '../../src/lib/config/schema';

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
    files: [],
    stats: { filesChanged: 0, additions: 0, deletions: 0 },
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
      { name: 'Alice Smith', email: 'alice@example.com', commits: 3, firstCommit: '2024-01-01T00:00:00Z', lastCommit: '2024-01-03T00:00:00Z', avatar: null, description: null, url: null },
      { name: 'Bob', email: 'bob@example.com', commits: 1, firstCommit: '2024-01-02T00:00:00Z', lastCommit: '2024-01-02T00:00:00Z', avatar: null, description: null, url: null },
      { name: 'Carol', email: 'carol@example.com', commits: 1, firstCommit: '2024-01-05T00:00:00Z', lastCommit: '2024-01-05T00:00:00Z', avatar: null, description: null, url: null },
    ]);
  });

  it('is empty for no commits', () => {
    expect(contributorsFromCommits([])).toEqual([]);
  });
});

describe('configured contributors (schema v8)', () => {
  const kieran: ContributorConfig = {
    name: 'Kieran Wood',
    emails: ['work@example.com', 'Personal@Example.com'],
    avatar: '/images/kieran.png',
    description: 'maintainer',
    url: 'https://kieranwood.ca',
  };

  it('decorates a matching contributor without touching the others', () => {
    const res = contributorsFromCommits(
      [
        commit('a', 'kwood', 'work@example.com', '2024-01-01T00:00:00Z'),
        commit('b', 'Someone Else', 'other@example.com', '2024-01-02T00:00:00Z'),
      ],
      contributorIndex([kieran]),
    );
    const [decorated, plain] = [res.find((c) => c.email === 'work@example.com')!, res.find((c) => c.email === 'other@example.com')!];
    expect(decorated).toMatchObject({
      name: 'Kieran Wood', // the configured name beats git's
      avatar: '/images/kieran.png',
      description: 'maintainer',
      url: 'https://kieranwood.ca',
    });
    // an unconfigured person is exactly what they were before v8
    expect(plain).toMatchObject({ name: 'Someone Else', avatar: null, description: null, url: null });
  });

  it('merges every address one entry claims into a single contributor', () => {
    // The whole reason `emails` is a list: without merging, one person who commits from two
    // machines shows up twice with the same face, which reads as a bug.
    const res = contributorsFromCommits(
      [
        commit('a', 'kwood', 'work@example.com', '2024-01-01T00:00:00Z'),
        commit('b', 'Kieran', 'personal@example.com', '2024-03-01T00:00:00Z'),
        commit('c', 'kwood', 'WORK@example.com', '2024-02-01T00:00:00Z'), // case-insensitive
      ],
      contributorIndex([kieran]),
    );
    expect(res).toHaveLength(1);
    expect(res[0]).toMatchObject({
      name: 'Kieran Wood',
      email: 'work@example.com', // the FIRST configured address is canonical
      commits: 3,
      firstCommit: '2024-01-01T00:00:00Z',
      lastCommit: '2024-03-01T00:00:00Z', // the window widens across both addresses
    });
  });

  it('sorts on the MERGED commit count, not the pre-merge one', () => {
    // Merging changes the ranking, so it has to happen before the sort: 2 + 1 beats 2.
    const res = contributorsFromCommits(
      [
        commit('a', 'K', 'work@example.com', '2024-01-01T00:00:00Z'),
        commit('b', 'K', 'work@example.com', '2024-01-02T00:00:00Z'),
        commit('c', 'K', 'personal@example.com', '2024-01-03T00:00:00Z'),
        commit('d', 'Rival', 'rival@example.com', '2024-01-01T00:00:00Z'),
        commit('e', 'Rival', 'rival@example.com', '2024-01-02T00:00:00Z'),
      ],
      contributorIndex([kieran]),
    );
    expect(res.map((c) => [c.name, c.commits])).toEqual([
      ['Kieran Wood', 3],
      ['Rival', 2],
    ]);
  });

  it('is deterministic when two entries claim the same address', () => {
    const first: ContributorConfig = { name: 'First', emails: ['dup@example.com'] };
    const second: ContributorConfig = { name: 'Second', emails: ['dup@example.com'] };
    const index = contributorIndex([first, second]);
    expect(index.get('dup@example.com')!.name).toBe('First'); // config order wins, not Map order
  });

  it('changes nothing at all when no entry matches', () => {
    const commits = [commit('a', 'Solo', 'solo@example.com', '2024-01-01T00:00:00Z')];
    expect(contributorsFromCommits(commits, contributorIndex([kieran]))).toEqual(
      contributorsFromCommits(commits),
    );
  });

  it('reports entries that decorate nobody, and only those', () => {
    const ghost: ContributorConfig = { name: 'Ghost', emails: ['ghost@example.com'] };
    const seen = new Set(['work@example.com']);
    expect(unmatchedContributors([kieran, ghost], seen).map((e) => e.name)).toEqual(['Ghost']);
    // one matching address out of several is enough to count as matched
    expect(unmatchedContributors([kieran], new Set(['personal@example.com']))).toEqual([]);
  });
});

describe('PublicPath (avatar paths)', () => {
  const parse = (v: string) => PublicPath.parse(v);

  it('normalises to a leading slash', () => {
    expect(parse('images/owner.png')).toBe('/images/owner.png');
    expect(parse('/images/owner.png')).toBe('/images/owner.png');
    expect(parse('images/orgs/acme.png')).toBe('/images/orgs/acme.png');

  });

  it('refuses anything that is not a path inside public/', () => {
    // The published site loads no third-party assets; an avatar must not be the exception.
    expect(() => parse('https://github.com/u.png')).toThrow();
    expect(() => parse('data:image/png;base64,AAAA')).toThrow();
    expect(() => parse('//evil.example.com/u.png')).toThrow();
    // any leading `//` is protocol-relative and therefore off-site, however it is spelled
    expect(() => parse('///images/owner.png')).toThrow();
    // ...and it must not be able to point outside public/
    expect(() => parse('../../../etc/passwd')).toThrow();
    expect(() => parse('images/../../secret.png')).toThrow();
    expect(() => parse('images\\owner.png')).toThrow();
    expect(() => parse('')).toThrow();
  });
});
