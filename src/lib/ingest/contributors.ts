/**
 * Contributors: commits grouped by (lower-cased) author email, optionally decorated and
 * merged by the site config's `contributors[]` entries (schema v8).
 */
import type { ContributorConfig } from '../config/schema';
import type { Commit, Contributor } from '../data/schema';

function cmpStr(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0;
}

/**
 * Email → the config entry that claims it, lower-cased.
 *
 * Built once per run rather than per repo. When two entries claim the same address the
 * FIRST one wins, so the result never depends on object iteration order; the duplicate is
 * reported by {@link unmatchedContributors} the same way an entry matching nobody is.
 */
export function contributorIndex(entries: readonly ContributorConfig[]): Map<string, ContributorConfig> {
  const index = new Map<string, ContributorConfig>();
  for (const entry of entries) {
    for (const email of entry.emails) {
      const key = email.trim().toLowerCase();
      if (key && !index.has(key)) index.set(key, entry);
    }
  }
  return index;
}

/**
 * Group commits by author email (case-insensitive); the display name is the one used on
 * the most recent commit. Sorted by commit count desc, then name, then email.
 *
 * When `configured` is given, every address a single entry claims collapses into ONE
 * contributor — commits summed, first/last widened — carrying the configured name, avatar,
 * blurb and link. That is the only sensible reading of "these addresses are the same
 * person", and it is why the merge happens here rather than as a post-pass: the merged
 * commit count and name are what the sort below has to order by.
 */
export function contributorsFromCommits(
  commits: Iterable<Commit>,
  configured: Map<string, ContributorConfig> = new Map(),
): Contributor[] {
  const groups = new Map<
    string,
    {
      name: string;
      nameDate: string;
      nameSha: string;
      email: string;
      commits: number;
      first: string;
      last: string;
      config: ContributorConfig | null;
    }
  >();
  for (const c of commits) {
    const email = c.author.email.trim().toLowerCase();
    const config = configured.get(email) ?? null;
    // A configured person is keyed by their FIRST listed address, so every address they
    // committed under lands in the same group.
    const key = config ? `cfg:${config.emails[0]!.trim().toLowerCase()}` : email;
    const g = groups.get(key);
    if (!g) {
      groups.set(key, {
        name: c.author.name,
        nameDate: c.authorDate,
        nameSha: c.sha,
        email: config ? config.emails[0]!.trim().toLowerCase() : email,
        commits: 1,
        first: c.authorDate,
        last: c.authorDate,
        config,
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
  return Array.from(groups.values())
    .map((g) => ({
      name: g.config?.name ?? g.name,
      email: g.email,
      commits: g.commits,
      firstCommit: g.first,
      lastCommit: g.last,
      avatar: g.config?.avatar ?? null,
      description: g.config?.description ?? null,
      url: g.config?.url ?? null,
    }))
    .sort((a, b) => b.commits - a.commits || cmpStr(a.name, b.name) || cmpStr(a.email, b.email));
}

/**
 * Configured contributors that decorated nobody in this build, in config order.
 *
 * Not an error: the repo that person contributed to may simply not be part of this build,
 * exactly like `org-unknown-repo`. The caller turns these into
 * `contributor-unknown-email` warnings.
 */
export function unmatchedContributors(
  entries: readonly ContributorConfig[],
  seenEmails: ReadonlySet<string>,
): ContributorConfig[] {
  return entries.filter((e) => !e.emails.some((email) => seenEmails.has(email.trim().toLowerCase())));
}
