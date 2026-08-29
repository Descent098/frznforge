/**
 * Unit tests for the Phase 3/4 pure libs: ref slugs + route mapping, highlight language
 * mapping, contribution bucketing, activity events, search index + ranking.
 */
import { describe, expect, it } from 'vitest';
import type { Repo } from '../../src/lib/data/schema';
import {
  archiveUrl,
  blobRoutes,
  blobUrl,
  browsableRefs,
  commitsPageCount,
  commitsUrl,
  findRef,
  rawRoutes,
  refFromSlug,
  refSlug,
  releasesOf,
  repoRoutes,
  resolveReleases,
  treeRoutes,
  treeUrl,
} from '../../src/lib/routes';
import { shikiLang, countLines, highlightToHtml } from '../../src/lib/highlight';
import { buildContribGraph, commitsByDay } from '../../src/lib/contrib';
import { buildActivity } from '../../src/lib/activity';
import { buildSearchIndex, scoreDoc, search } from '../../src/lib/search';

/* ---- fixture repo (hand-built, schema-shaped) ----------------------------- */

const sha = (n: number) => n.toString(16).padStart(40, '0');
const commit = (n: number, date: string, subject = `c${n}`, email = 'kieran@example.com') => ({
  sha: sha(n),
  parents: n > 1 ? [sha(n - 1)] : [],
  author: { name: 'Kieran', email },
  authorDate: date,
  committer: { name: 'Kieran', email },
  commitDate: date,
  subject,
  body: '',
  files: [{ path: 'a.ts', additions: 1, deletions: 0 }],
  stats: { filesChanged: 1, additions: 1, deletions: 0 },
});
const entry = (path: string, type: 'blob' | 'tree', last = 1) => ({
  path,
  name: path.split('/').pop()!,
  type,
  mode: type === 'tree' ? '040000' : '100644',
  sha: sha(90 + path.length),
  size: type === 'tree' ? null : 10,
  lastCommit: sha(last),
});
const file = (path: string, stored = true) => ({
  path,
  sha: sha(90 + path.length),
  size: 10,
  binary: false,
  tooLarge: false,
  stored,
  language: 'TypeScript',
});

const repo: Repo = {
  slug: 'alpha',
  name: 'alpha',
  description: 'Alpha repo',
  source: { type: 'local', path: '/x' },
  links: {},
  tags: ['ssg'],
  template: false,
  license: null,
  releaseMode: 'tags',
  releases: [],
  empty: false,
  defaultBranch: 'main',
  branches: [
    { name: 'main', head: sha(2), commits: [sha(2), sha(1)], lastCommitDate: '2026-08-20T10:00:00Z' },
    { name: 'feat/zip', head: sha(3), commits: [sha(3), sha(2), sha(1)], lastCommitDate: '2026-08-21T10:00:00Z' },
  ],
  gitTags: [
    { name: 'v1.0.0', target: sha(1), annotated: true, message: 'First **release**', tagger: { name: 'K', email: 'k@x' }, date: '2026-08-19T00:00:00Z' },
    { name: 'light', target: sha(2), annotated: false, message: null, tagger: null, date: '2026-08-20T10:00:00Z' },
  ],
  commits: {
    [sha(1)]: commit(1, '2026-08-19T09:00:00Z', 'init'),
    [sha(2)]: commit(2, '2026-08-20T10:00:00Z', 'second'),
    [sha(3)]: commit(3, '2026-08-21T10:00:00Z', 'feature work'),
  },
  commitCount: 3,
  extraCommits: {},
  tree: [entry('src', 'tree'), entry('src/a.ts', 'blob', 2), entry('README.md', 'blob')],
  files: { 'src/a.ts': file('src/a.ts'), 'README.md': file('README.md', false) },
  refTrees: {
    'feat/zip': {
      kind: 'branch',
      name: 'feat/zip',
      commit: sha(3),
      tree: [entry('src', 'tree'), entry('src/a.ts', 'blob', 3), entry('src/b.ts', 'blob', 3)],
      files: { 'src/a.ts': file('src/a.ts'), 'src/b.ts': file('src/b.ts') },
    },
    'v1.0.0': {
      kind: 'tag',
      name: 'v1.0.0',
      commit: sha(1),
      tree: [entry('README.md', 'blob')],
      files: { 'README.md': file('README.md') },
    },
  },
  archives: [
    { ref: 'main', kind: 'branch', commit: sha(2), file: 'archives/alpha/main.zip', bytes: 1234 },
    { ref: 'v1.0.0', kind: 'tag', commit: sha(1), file: 'archives/alpha/v1.0.0.zip', bytes: 999 },
  ],
  languages: [{ name: 'TypeScript', bytes: 20, percent: 100, color: '#3178c6' }],
  contributors: [{ name: 'Kieran', email: 'kieran@example.com', commits: 3, firstCommit: '2026-08-19T09:00:00Z', lastCommit: '2026-08-21T10:00:00Z' }],
  insights: null,
  readme: null,
  createdAt: '2026-08-19T09:00:00Z',
  updatedAt: '2026-08-21T10:00:00Z',
  warnings: [],
};

describe('ref slugs', () => {
  it('round-trips slashes as ~', () => {
    expect(refSlug('feat/zip')).toBe('feat~zip');
    expect(refFromSlug('feat~zip')).toBe('feat/zip');
    expect(refSlug('main')).toBe('main');
  });
});

describe('routes', () => {
  it('browsableRefs: default first, then branches, then tags', () => {
    expect(browsableRefs(repo).map((r) => [r.name, r.kind, r.isDefault])).toEqual([
      ['main', 'branch', true],
      ['feat/zip', 'branch', false],
      ['v1.0.0', 'tag', false],
    ]);
    expect(findRef(repo, 'feat~zip')?.name).toBe('feat/zip');
    expect(findRef(repo, 'nope')).toBeUndefined();
  });

  it('URL builders encode refs', () => {
    expect(treeUrl('alpha', 'feat/zip', 'src')).toBe('/repos/alpha/tree/feat~zip/src/');
    expect(blobUrl('alpha', 'main', 'src/a.ts')).toBe('/repos/alpha/blob/main/src/a.ts/');
    expect(commitsUrl('alpha', 'main', 2)).toBe('/repos/alpha/commits/main/page/2/');
    expect(archiveUrl('alpha', 'feat/zip')).toBe('/repos/alpha/archive/feat~zip.zip');
  });

  it('tree/blob/raw routes cover every entry per ref; raw only for stored', () => {
    const trees = treeRoutes(repo);
    expect(trees.map((t) => t.url)).toContain('/repos/alpha/tree/main/');
    expect(trees.map((t) => t.url)).toContain('/repos/alpha/tree/feat~zip/src/');
    const blobs = blobRoutes(repo);
    expect(blobs.map((b) => b.url)).toContain('/repos/alpha/blob/feat~zip/src/b.ts/');
    const raws = rawRoutes(repo).map((r) => r.url);
    expect(raws).toContain('/repos/alpha/raw/main/src/a.ts');
    expect(raws).not.toContain('/repos/alpha/raw/main/README.md'); // not stored on main
  });

  it('repoRoutes covers commits pagination, commit pages, releases, archives', () => {
    const urls = repoRoutes(repo);
    expect(urls).toContain('/repos/alpha/commits/main/');
    expect(urls).toContain(`/repos/alpha/commit/${sha(3)}/`);
    expect(urls).toContain('/repos/alpha/releases/v1.0.0/');
    expect(urls).not.toContain('/repos/alpha/releases/light/'); // lightweight tag ≠ release
    expect(urls).toContain('/repos/alpha/archive/main.zip');
    expect(commitsPageCount(repo, 'main')).toBe(1);
  });

  it('releasesOf returns annotated tags newest first', () => {
    expect(releasesOf(repo).map((t) => t.name)).toEqual(['v1.0.0']);
  });

  it('orders releases by code point, matching the artifact and not the build machine’s locale', () => {
    // `localeCompare` puts 'v1.0.0' before 'V1.0.0'; the importers sort the artifact by code
    // point, which puts 'V1.0.0' first. Disagreeing means two machines with different ICU
    // data render the release list — and the "Latest" badge — in a different order.
    const mk = (tag: string) => ({
      tag,
      name: tag,
      body: '',
      url: null,
      prerelease: false,
      publishedAt: '2026-01-01T00:00:00Z',
      author: null,
      assets: [],
    });
    const tied = { ...repo, releases: [mk('v1.0.0'), mk('V1.0.0')], releaseMode: 'provider' as const };
    expect(resolveReleases(tied).map((r) => r.tag)).toEqual(['V1.0.0', 'v1.0.0']);
    expect('v1.0.0'.localeCompare('V1.0.0')).toBe(-1); // …which is what we are not doing
  });
});

describe('highlight helpers', () => {
  it('maps artifact language names and falls back to extension then text', () => {
    expect(shikiLang('TypeScript')).toBe('typescript');
    expect(shikiLang('Shell')).toBe('shellscript');
    expect(['rust', 'rs']).toContain(shikiLang(null, 'x/y.rs'));
    expect(shikiLang('NoSuchLang', 'file.unknownext')).toBe('text');
  });
  it('counts lines editor-style', () => {
    expect(countLines('')).toBe(0);
    expect(countLines('a')).toBe(1);
    expect(countLines('a\nb\n')).toBe(2);
  });

  /**
   * Per-line ids have to be unique across a whole PAGE, not just within one file.
   *
   * A multi-file note renders one highlighted card per file, and every card used to emit
   * `id="L1"…"L30"` again — so `#L5` resolved to the first file and the lines of every later
   * file were unlinkable no matter what you typed. `NoteFileView` now passes its section
   * anchor as the prefix.
   */
  it('namespaces per-line ids by the caller-supplied prefix', async () => {
    const source = 'const a = 1;\nconst b = 2;\nconst c = 3;\n';
    const first = await highlightToHtml(source, 'TypeScript', 'a.ts', 'f-a-ts-');
    const second = await highlightToHtml(source, 'TypeScript', 'b.ts', 'f-b-ts-');
    const ids = (html: string) => [...html.matchAll(/id="([^"]+)"/g)].map((m) => m[1]!);

    expect(ids(first)).toEqual(['f-a-ts-L1', 'f-a-ts-L2', 'f-a-ts-L3']);
    expect(ids(second)).toEqual(['f-b-ts-L1', 'f-b-ts-L2', 'f-b-ts-L3']);
    // disjoint: no id from one file can be reached by an anchor meant for the other
    expect(ids(first).filter((id) => ids(second).includes(id))).toEqual([]);
  });

  it('keeps the documented bare `L<n>` ids when no prefix is given', async () => {
    const html = await highlightToHtml('one\ntwo\n', null, 'notes.txt');
    expect([...html.matchAll(/id="([^"]+)"/g)].map((m) => m[1]!)).toEqual(['L1', 'L2']);
  });
});

describe('contributions', () => {
  const now = new Date('2026-08-23T12:00:00Z');
  it('buckets by day, dedupes shas, filters identities', () => {
    const days = commitsByDay([repo]);
    expect(days.get('2026-08-20')).toBe(1);
    expect(commitsByDay([repo], ['other@x.com']).size).toBe(0);
    expect(commitsByDay([repo], ['KIERAN@example.com']).size).toBe(3);
  });
  it('builds a 52-week grid with totals and streaks', () => {
    const g = buildContribGraph([repo], [], now);
    expect(g.weeks).toHaveLength(52);
    expect(g.total).toBe(3);
    expect(g.longestStreak).toBe(3); // 19th, 20th, 21st
    expect(g.busiestDay?.count).toBe(1);
    const cells = g.weeks.flat().filter((c) => c && c.count > 0);
    expect(cells).toHaveLength(3);
    expect(cells.every((c) => c!.level >= 1)).toBe(true); // equal counts all land in the same non-zero level
    expect(cells.every((c) => c!.heat === 'hot')).toBe(true);
    expect(g.months.length).toBeGreaterThan(8);
  });
});

describe('activity', () => {
  it('groups pushes per repo/branch/day, includes tags, newest first, dedupes shadowed pushes', () => {
    const events = buildActivity([repo], 10);
    expect(events[0]).toMatchObject({ type: 'push', branch: 'feat/zip', count: 1, subject: 'feature work' });
    const tagEvents = events.filter((e) => e.type === 'tag');
    expect(tagEvents.map((e) => e.type === 'tag' && e.tag).sort()).toEqual(['light', 'v1.0.0']);
    // main's aug-20 push and feat/zip's aug-20 push have identical day/count/subject → deduped
    const pushes20 = events.filter((e) => e.type === 'push' && e.date.startsWith('2026-08-20'));
    expect(pushes20).toHaveLength(1);
    expect([...events].sort((a, b) => b.date.localeCompare(a.date))).toEqual(events);
  });
  it('caps at limit', () => {
    expect(buildActivity([repo], 2)).toHaveLength(2);
  });
});

describe('search', () => {
  const data = { schemaVersion: 6 as const, repos: [repo], notes: [], organizations: [], warnings: [] };
  const index = buildSearchIndex(data);
  it('indexes pages, repos and default-branch files only', () => {
    const kinds = index.docs.map((d) => d.kind);
    expect(kinds.filter((k) => k === 'page')).toHaveLength(2);
    expect(kinds.filter((k) => k === 'repo')).toHaveLength(1);
    // default branch has 2 blobs; feat-only b.ts is not indexed
    expect(index.docs.filter((d) => d.kind === 'file').map((d) => d.title).sort()).toEqual(['README.md', 'src/a.ts']);
  });
  it('ranks title prefix over substring, repos over files, all terms required', () => {
    const r = search(index.docs, 'alpha');
    expect(r[0]!.doc.kind).toBe('repo');
    expect(search(index.docs, 'a.ts')[0]!.doc.title).toBe('src/a.ts');
    expect(search(index.docs, 'alpha nomatchterm')).toHaveLength(0);
    expect(scoreDoc(index.docs[0]!, '')).toBe(0);
  });
  it('matches keywords (tags/languages) for repos', () => {
    expect(search(index.docs, 'ssg')[0]!.doc.kind).toBe('repo');
    expect(search(index.docs, 'typescript').some((s) => s.doc.kind === 'repo')).toBe(true);
  });
});
