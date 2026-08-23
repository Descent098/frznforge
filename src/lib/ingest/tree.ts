/**
 * Default-branch HEAD tree: flat listing (all depths), per-path last commit, per-file info
 * and blob contents for stored files.
 */
import type { FileInfo, TreeEntry } from '../data/schema';
import { git, gitBuffer, looksBinary, readBlobPrefix, readBlobs, splitNul } from './git';
import { detectLanguage } from './languages';

export interface RawTreeEntry {
  mode: string;
  type: 'blob' | 'tree' | 'commit' | 'symlink';
  sha: string;
  size: number | null;
  path: string;
  name: string;
}

function cmpPath(a: { path: string }, b: { path: string }): number {
  return a.path < b.path ? -1 : a.path > b.path ? 1 : 0;
}

function parseLsTree(out: Buffer): RawTreeEntry[] {
  const entries: RawTreeEntry[] = [];
  for (const rec of splitNul(out)) {
    const tab = rec.indexOf('\t');
    if (tab === -1) continue;
    const m = /^(\d+) (\w+) ([0-9a-f]{40}) +(\S+)$/.exec(rec.slice(0, tab));
    if (!m) continue;
    const [, mode, gitType, sha, sizeStr] = m as unknown as [string, string, string, string, string];
    const path = rec.slice(tab + 1);
    let type: RawTreeEntry['type'];
    if (mode === '160000' || gitType === 'commit') type = 'commit';
    else if (mode === '120000') type = 'symlink';
    else if (gitType === 'tree') type = 'tree';
    else type = 'blob';
    const size = type === 'tree' || type === 'commit' || sizeStr === '-' ? null : Number.parseInt(sizeStr, 10);
    const slash = path.lastIndexOf('/');
    entries.push({ mode, type, sha, size, path, name: slash === -1 ? path : path.slice(slash + 1) });
  }
  entries.sort(cmpPath);
  return entries;
}

/** `ls-tree -r -l -t -z <treeish>` → all entries at all depths (trees included), sorted by path. */
export async function listTree(repo: string, treeish: string): Promise<RawTreeEntry[]> {
  return parseLsTree(await gitBuffer(repo, ['ls-tree', '-r', '-l', '-t', '-z', treeish, '--']));
}

/** Root-level entries only (no recursion), sorted by path. */
export async function listRootTree(repo: string, treeish: string): Promise<RawTreeEntry[]> {
  return parseLsTree(await gitBuffer(repo, ['ls-tree', '-l', '-z', treeish, '--']));
}

/**
 * Map path → sha of the newest commit (walking `rev` newest-first) that touched it.
 * Directories get the newest of their descendants. Paths never seen are absent.
 */
export async function lastCommitByPath(repo: string, rev: string, paths: Iterable<string>): Promise<Map<string, string>> {
  const wanted = new Set(paths);
  const result = new Map<string, string>();
  if (wanted.size === 0) return result;
  const out = await git(repo, ['log', '--format=%x1e%H', '--name-only', '-z', rev, '--']);
  let remaining = wanted.size;
  for (const chunk of out.split('\x1e')) {
    if (!chunk) continue;
    const nul = chunk.indexOf('\0');
    const sha = (nul === -1 ? chunk : chunk.slice(0, nul)).trim();
    if (!/^[0-9a-f]{40}$/.test(sha)) continue;
    if (nul === -1) continue;
    for (const raw of chunk.slice(nul + 1).split('\0')) {
      const p = raw.replace(/^\n+|\n+$/g, '');
      if (!p) continue;
      // the path itself and every ancestor directory
      let cur = p;
      for (;;) {
        if (wanted.has(cur) && !result.has(cur)) {
          result.set(cur, sha);
          remaining--;
        }
        const i = cur.lastIndexOf('/');
        if (i === -1) break;
        cur = cur.slice(0, i);
      }
    }
    if (remaining <= 0) break;
  }
  return result;
}

export interface TreeScanResult {
  tree: TreeEntry[];
  files: Record<string, FileInfo>;
  blobs: Map<string, Buffer>;
  /** sha → content for every blob that was read (stored files), for readme/meta reuse. */
  contents: Map<string, Buffer>;
}

/**
 * Full tree scan of `head` (a commit sha): tree entries with lastCommit, file info with
 * binary/size classification, and contents of stored blobs.
 */
export async function scanTree(
  repo: string,
  head: string,
  opts: { maxBlobBytes: number },
): Promise<TreeScanResult> {
  const raw = await listTree(repo, head);
  const last = await lastCommitByPath(repo, head, raw.map((e) => e.path));

  const tree: TreeEntry[] = raw.map((e) => ({
    path: e.path,
    name: e.name,
    type: e.type,
    mode: e.mode,
    sha: e.sha,
    size: e.size,
    lastCommit: last.get(e.path) ?? head,
  }));

  const fileEntries = raw.filter((e) => e.type === 'blob' || e.type === 'symlink');
  const small = fileEntries.filter((e) => (e.size ?? 0) <= opts.maxBlobBytes);
  const contents = await readBlobs(repo, small.map((e) => e.sha));

  // binary sniff for oversized blobs (only the first 8000 bytes are read)
  const bigBinary = new Map<string, boolean>();
  for (const e of fileEntries) {
    if ((e.size ?? 0) > opts.maxBlobBytes && !bigBinary.has(e.sha)) {
      bigBinary.set(e.sha, looksBinary(await readBlobPrefix(repo, e.sha, 8000)));
    }
  }

  const files: Record<string, FileInfo> = {};
  const blobs = new Map<string, Buffer>();
  for (const e of fileEntries) {
    const size = e.size ?? 0;
    const tooLarge = size > opts.maxBlobBytes;
    const content = contents.get(e.sha);
    const binary = tooLarge ? (bigBinary.get(e.sha) ?? false) : content ? looksBinary(content) : false;
    // schema v2: any file within the size cap is stored, binary included (raw serving / previews)
    const stored = !tooLarge && content !== undefined;
    files[e.path] = {
      path: e.path,
      sha: e.sha,
      size,
      binary,
      tooLarge,
      stored,
      language: detectLanguage(e.path),
    };
    if (stored) blobs.set(e.sha, content!);
  }

  return { tree, files, blobs, contents };
}
