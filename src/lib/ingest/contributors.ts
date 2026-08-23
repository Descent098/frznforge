/**
 * Contributors: commits grouped by (lower-cased) author email.
 */
import type { Commit, Contributor } from '../data/schema';

function cmpStr(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0;
}

/**
 * Group commits by author email (case-insensitive); the display name is the one used on
 * the most recent commit. Sorted by commit count desc, then name, then email.
 */
export function contributorsFromCommits(commits: Iterable<Commit>): Contributor[] {
  const groups = new Map<
    string,
    { name: string; nameDate: string; nameSha: string; commits: number; first: string; last: string }
  >();
  for (const c of commits) {
    const email = c.author.email.trim().toLowerCase();
    const g = groups.get(email);
    if (!g) {
      groups.set(email, {
        name: c.author.name,
        nameDate: c.authorDate,
        nameSha: c.sha,
        commits: 1,
        first: c.authorDate,
        last: c.authorDate,
      });
      continue;
    }
    g.commits++;
    if (c.authorDate < g.first) g.first = c.authorDate;
    if (c.authorDate > g.last) g.last = c.authorDate;
    // most recent name wins; ties broken by sha so the result is deterministic
    if (c.authorDate > g.nameDate || (c.authorDate === g.nameDate && c.sha > g.nameSha)) {
      g.name = c.author.name;
      g.nameDate = c.authorDate;
      g.nameSha = c.sha;
    }
  }
  return Array.from(groups.entries())
    .map(([email, g]) => ({ name: g.name, email, commits: g.commits, firstCommit: g.first, lastCommit: g.last }))
    .sort((a, b) => b.commits - a.commits || cmpStr(a.name, b.name) || cmpStr(a.email, b.email));
}
