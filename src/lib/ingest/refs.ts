/**
 * Refs: default branch detection, branches (with per-branch commit lists) and tags.
 */
import type { Branch, Tag, Warning } from '../data/schema';
import { git, gitMaybe, stripAngleBrackets, toIsoUtc } from './git';

export interface BranchRef {
  name: string;
  head: string;
  /** Commit date of the head commit, ISO UTC. */
  headDate: string;
}

/** `for-each-ref refs/heads` — every local branch that has at least one commit. */
export async function listBranchRefs(repo: string): Promise<BranchRef[]> {
  const out = await git(repo, [
    'for-each-ref',
    '--format=%(refname)%00%(objectname)%00%(committerdate:iso-strict)%1e',
    'refs/heads',
  ]);
  const refs: BranchRef[] = [];
  for (const rec of out.split('\x1e')) {
    const r = rec.replace(/^\s+/, '');
    if (!r) continue;
    const [refname, sha, date] = r.split('\0');
    if (!refname || !sha || !date) continue;
    refs.push({ name: refname.replace(/^refs\/heads\//, ''), head: sha, headDate: toIsoUtc(date) });
  }
  refs.sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
  return refs;
}

export interface DefaultBranchResult {
  /** null ⇒ no branch has any commit (repo is empty). */
  name: string | null;
  warnings: Warning[];
}

/**
 * Default branch: HEAD's branch when it has commits; otherwise `main`, `master`, then the
 * branch with the most recent commit (with a `default-branch-fallback` warning).
 */
export async function detectDefaultBranch(repo: string, branches: BranchRef[]): Promise<DefaultBranchResult> {
  const warnings: Warning[] = [];
  const headRef = (await gitMaybe(repo, ['symbolic-ref', '--quiet', 'HEAD']))?.trim() ?? null;
  const headBranch = headRef && headRef.startsWith('refs/heads/') ? headRef.slice('refs/heads/'.length) : null;

  if (headBranch && branches.some((b) => b.name === headBranch)) {
    return { name: headBranch, warnings };
  }
  if (branches.length === 0) return { name: null, warnings };

  let pick = branches.find((b) => b.name === 'main') ?? branches.find((b) => b.name === 'master');
  if (!pick) {
    pick = [...branches].sort((a, b) =>
      a.headDate > b.headDate ? -1 : a.headDate < b.headDate ? 1 : a.name < b.name ? -1 : 1,
    )[0];
  }
  const why = headBranch
    ? `HEAD points at '${headBranch}' which has no commits`
    : 'HEAD is detached or unreadable';
  warnings.push({
    code: 'default-branch-fallback',
    repo: null,
    message: `${why}; using '${pick!.name}' as the default branch`,
  });
  return { name: pick!.name, warnings };
}

export interface BranchesResult {
  branches: Branch[];
  /** Union of every sha listed in any branch (after capping). */
  shas: Set<string>;
  warnings: Warning[];
}

/**
 * Per-branch commit lists (topological, newest first), capped at `maxCommits`
 * (one `commits-capped` warning per repo).
 */
export async function loadBranches(
  repo: string,
  refs: BranchRef[],
  maxCommits: number | null,
): Promise<BranchesResult> {
  const branches: Branch[] = [];
  const shas = new Set<string>();
  let capped = false;
  for (const ref of refs) {
    const args = ['rev-list', '--topo-order'];
    if (maxCommits !== null) args.push(`--max-count=${maxCommits + 1}`);
    args.push(ref.head, '--');
    const out = await git(repo, args);
    let commits = out.split(/\r?\n/).filter((s) => s.length > 0);
    if (maxCommits !== null && commits.length > maxCommits) {
      commits = commits.slice(0, maxCommits);
      capped = true;
    }
    for (const s of commits) shas.add(s);
    branches.push({ name: ref.name, head: ref.head, commits, lastCommitDate: ref.headDate });
  }
  const warnings: Warning[] = [];
  if (capped) {
    warnings.push({
      code: 'commits-capped',
      repo: null,
      message: `commit lists were capped at ${maxCommits} per branch (ingest.maxCommits)`,
    });
  }
  return { branches, shas, warnings };
}

/** `for-each-ref refs/tags` — annotated and lightweight tags, sorted by name. */
export async function loadTags(repo: string): Promise<Tag[]> {
  const fields = [
    '%(objecttype)',
    '%(objectname)',
    '%(*objectname)',
    '%(*objecttype)',
    '%(refname)',
    '%(taggername)',
    '%(taggeremail)',
    '%(taggerdate:iso-strict)',
    '%(committerdate:iso-strict)',
    '%(*committerdate:iso-strict)',
    '%(contents)',
  ];
  const out = await git(repo, ['for-each-ref', `--format=${fields.join('%00')}%1e`, 'refs/tags']);
  const tags: Tag[] = [];
  for (const rec of out.split('\x1e')) {
    const r = rec.replace(/^\s+/, '');
    if (!r) continue;
    const parts = r.split('\0');
    if (parts.length < fields.length) continue;
    const [objType, objName, peeledName, peeledType, refname, taggerName, taggerEmail, taggerDate, commitDate, peeledCommitDate] =
      parts as [string, string, string, string, string, string, string, string, string, string, ...string[]];
    const contents = parts.slice(fields.length - 1).join('\0');
    const name = refname.replace(/^refs\/tags\//, '');
    if (objType === 'tag') {
      if (peeledType !== 'commit' || !peeledName) continue; // tag → tree/blob: not representable
      const tagger = taggerName || taggerEmail ? { name: taggerName, email: stripAngleBrackets(taggerEmail) } : null;
      const date = taggerDate ? toIsoUtc(taggerDate) : toIsoUtc(peeledCommitDate);
      tags.push({ name, target: peeledName, annotated: true, message: contents.trim(), tagger, date });
    } else if (objType === 'commit') {
      tags.push({ name, target: objName, annotated: false, message: null, tagger: null, date: toIsoUtc(commitDate) });
    }
    // lightweight tags pointing at trees/blobs are skipped
  }
  tags.sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
  return tags;
}
