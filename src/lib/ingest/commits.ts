/**
 * Commit objects for a set of shas, loaded in one `git log --stdin --no-walk` call.
 */
import type { Commit } from '../data/schema';
import { git, toIsoUtc } from './git';

const FIELD = '%x1f';
const FORMAT = ['%H', '%P', '%an', '%ae', '%aI', '%cn', '%ce', '%cI', '%s', '%b'].join(FIELD);

/** Parse one `-z` record produced by FORMAT. */
export function parseCommitRecord(rec: string): Commit | null {
  const parts = rec.split('\x1f');
  if (parts.length < 10) return null;
  const [sha, parents, an, ae, aI, cn, ce, cI, subject] = parts as [
    string, string, string, string, string, string, string, string, string, ...string[],
  ];
  // %b is last; a message containing \x1f would be split further — rejoin.
  const body = parts.slice(9).join('\x1f');
  return {
    sha: sha.trim(),
    parents: parents.trim().length ? parents.trim().split(/\s+/) : [],
    author: { name: an, email: ae },
    authorDate: toIsoUtc(aI),
    committer: { name: cn, email: ce },
    commitDate: toIsoUtc(cI),
    subject: subject.replace(/\r?\n$/, ''),
    body: body.trim(),
  };
}

/**
 * Load the given commits keyed by sha. Keys are inserted in ascending sha order so the
 * serialised record is deterministic.
 */
export async function loadCommits(repo: string, shas: Iterable<string>): Promise<Record<string, Commit>> {
  const list = Array.from(new Set(shas)).sort();
  const result: Record<string, Commit> = {};
  if (list.length === 0) return result;
  const out = await git(repo, ['log', '--no-walk=unsorted', '--stdin', '-z', `--format=${FORMAT}`, '--'], {
    input: list.join('\n') + '\n',
  });
  const bySha = new Map<string, Commit>();
  for (const rec of out.split('\0')) {
    const trimmed = rec.replace(/^\n+/, '');
    if (!trimmed) continue;
    const c = parseCommitRecord(trimmed);
    if (c) bySha.set(c.sha, c);
  }
  for (const sha of list) {
    const c = bySha.get(sha);
    if (c) result[sha] = c;
  }
  return result;
}
