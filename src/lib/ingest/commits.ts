/**
 * Commit objects for a set of shas, loaded in one `git log --stdin --no-walk` call, plus
 * per-commit file changes from a second `git log --numstat` call.
 */
import type { Commit, CommitFileChange } from '../data/schema';
import { git, toIsoUtc } from './git';

const FIELD = '%x1f';
const FORMAT = ['%H', '%P', '%an', '%ae', '%aI', '%cn', '%ce', '%cI', '%s', '%b'].join(FIELD);

/** Parse one `-z` record produced by FORMAT. */
export function parseCommitRecord(rec: string): Omit<Commit, 'files' | 'stats'> | null {
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

/** Sums over the file changes; binary files (null counts) add to filesChanged only. */
export function statsFor(files: CommitFileChange[]): Commit['stats'] {
  let additions = 0;
  let deletions = 0;
  for (const f of files) {
    additions += f.additions ?? 0;
    deletions += f.deletions ?? 0;
  }
  return { filesChanged: files.length, additions, deletions };
}

/**
 * Per-commit file changes from `git log --numstat -z` (diff vs the first parent; root
 * commits count every file as added). With plain `git log` (no `-m`/`--first-parent` diff
 * options) merge commits emit no numstat records, so merges map to an empty array.
 * Binary files appear as `-`/`-` → null counts.
 */
export async function loadNumstats(repo: string, shas: readonly string[]): Promise<Map<string, CommitFileChange[]>> {
  const result = new Map<string, CommitFileChange[]>();
  if (shas.length === 0) return result;
  const out = await git(repo, ['log', '--no-walk=unsorted', '--stdin', '-z', '--format=%x1e%H', '--numstat', '--'], {
    input: shas.join('\n') + '\n',
  });
  for (const chunk of out.split('\x1e')) {
    if (!chunk) continue;
    const fields = chunk.split('\0');
    const sha = (fields[0] ?? '').trim();
    if (!/^[0-9a-f]{40}$/.test(sha)) continue;
    const files: CommitFileChange[] = [];
    for (let i = 1; i < fields.length; i++) {
      const rec = fields[i]!.replace(/^\n+/, '');
      const m = /^(\d+|-)\t(\d+|-)\t(.*)$/s.exec(rec);
      if (!m) continue;
      const [, adds, dels, inlinePath] = m as unknown as [string, string, string, string];
      let path = inlinePath;
      if (path === '') {
        // rename record: `adds\tdels\t` then two NUL-separated fields (old, new)
        path = fields[i + 2] ?? '';
        i += 2;
        if (!path) continue;
      }
      files.push({
        path,
        additions: adds === '-' ? null : Number.parseInt(adds, 10),
        deletions: dels === '-' ? null : Number.parseInt(dels, 10),
      });
    }
    result.set(sha, files);
  }
  return result;
}

/**
 * Load the given commits keyed by sha. Keys are inserted in ascending sha order so the
 * serialised record is deterministic.
 */
export async function loadCommits(repo: string, shas: Iterable<string>): Promise<Record<string, Commit>> {
  const list = Array.from(new Set(shas)).sort();
  const result: Record<string, Commit> = {};
  if (list.length === 0) return result;
  const [out, numstats] = await Promise.all([
    git(repo, ['log', '--no-walk=unsorted', '--stdin', '-z', `--format=${FORMAT}`, '--'], {
      input: list.join('\n') + '\n',
    }),
    loadNumstats(repo, list),
  ]);
  const bySha = new Map<string, Omit<Commit, 'files' | 'stats'>>();
  for (const rec of out.split('\0')) {
    const trimmed = rec.replace(/^\n+/, '');
    if (!trimmed) continue;
    const c = parseCommitRecord(trimmed);
    if (c) bySha.set(c.sha, c);
  }
  for (const sha of list) {
    const c = bySha.get(sha);
    if (!c) continue;
    const files = numstats.get(sha) ?? [];
    result[sha] = { ...c, files, stats: statsFor(files) };
  }
  return result;
}
