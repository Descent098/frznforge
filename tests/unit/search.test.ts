/**
 * Search index + ranking for the schema-v4 kinds (Phase 6): notes and organizations join the
 * Ctrl+K palette. Hand-built artifacts, so these tests pin the *contract* of `buildSearchIndex`
 * — which docs exist, what is matchable, and what outranks what — without touching git.
 *
 * The Phase 3/4 search tests in `phase34-libs.test.ts` still cover repos/files/pages; nothing
 * here re-tests those, it only guards that adding notes and orgs did not disturb them.
 */
import { describe, expect, it } from 'vitest';
import { emptyForgeData, type ForgeData, type Note, type NoteFile, type Organization, type Repo } from '../../src/lib/data/schema';
import { buildSearchIndex, scoreDoc, search, type SearchDoc } from '../../src/lib/search';

/* ---- fixtures ------------------------------------------------------------- */

const sha = (n: number) => n.toString(16).padStart(40, '0');

/** A minimally-populated repo: the search index only reads name/description/slug/tags/languages/tree. */
function repo(slug: string, name: string, description: string | null, tags: string[] = []): Repo {
  return {
    slug,
    name,
    description,
    source: { type: 'local', path: `/tmp/${slug}` },
    links: {},
    tags,
    template: false,
    license: null,
    releaseMode: 'tags',
    releases: [],
    empty: false,
    defaultBranch: 'main',
    branches: [{ name: 'main', head: sha(1), commits: [sha(1)], lastCommitDate: '2026-08-20T10:00:00Z' }],
    gitTags: [],
    commits: {},
    commitCount: 1,
    tree: [
      { path: 'README.md', name: 'README.md', type: 'blob', mode: '100644', sha: sha(2), size: 10, lastCommit: sha(1) },
    ],
    files: {
      'README.md': { path: 'README.md', sha: sha(2), size: 10, binary: false, tooLarge: false, stored: true, language: 'Markdown' },
    },
    refTrees: {},
    archives: [],
    languages: [{ name: 'TypeScript', bytes: 10, percent: 100, color: '#3178c6' }],
    contributors: [],
    insights: null,
    readme: null,
    createdAt: '2026-08-19T09:00:00Z',
    updatedAt: '2026-08-20T10:00:00Z',
    warnings: [],
  };
}

function noteFile(path: string, language: string | null, markdown = false): NoteFile {
  return {
    name: path.split('/').pop()!,
    path,
    sha: sha(path.length + 100),
    size: 32,
    binary: false,
    tooLarge: false,
    stored: true,
    language,
    markdown,
  };
}

function note(partial: Partial<Note> & Pick<Note, 'slug' | 'title'>): Note {
  const files = partial.files ?? [noteFile(`${partial.slug}.md`, 'Markdown', true)];
  return {
    description: null,
    tags: [],
    date: null,
    kind: 'file',
    totalBytes: files.reduce((n, f) => n + f.size, 0),
    ...partial,
    files,
  };
}

const notes: Note[] = [
  note({
    slug: 'deploying-a-frozen-forge',
    title: 'Deploying a frozen forge',
    description: 'Three ways to put a fully static forge online.',
    tags: ['deployment', 'static'],
    date: '2026-07-11T00:00:00Z',
    kind: 'folder',
    files: [noteFile('index.md', 'Markdown', true), noteFile('deploy.sh', 'Shell'), noteFile('ci/pages.yml', 'YAML')],
  }),
  note({
    slug: 'heat-buckets',
    title: 'Heat buckets',
    description: 'Why frznforge colours things fire-to-ice by age.',
    tags: ['design', 'css'],
    date: '2026-06-02T00:00:00Z',
  }),
  // deliberately titled exactly like the `alpha` repo — the repo must still win (see ranking test)
  note({ slug: 'alpha-note', title: 'alpha', description: 'A note that shares a repo name.' }),
  note({ slug: 'static-host-configs', title: 'Static host configs', kind: 'folder', files: [noteFile('vercel.json', 'JSON')] }),
];

const organizations: Organization[] = [
  { slug: 'alpha-org', name: 'alpha', description: null, repos: [] },
  { slug: 'canadian-coding', name: 'Canadian Coding', description: 'Small, sturdy, source-available tools.', repos: ['alpha', 'beacon'] },
];

const data: ForgeData = {
  schemaVersion: 5,
  repos: [repo('alpha', 'alpha', 'Alpha repo', ['ssg']), repo('beacon', 'beacon', 'Beacon repo')],
  notes,
  organizations,
  warnings: [],
};

const index = buildSearchIndex(data);
const byKind = (kind: SearchDoc['kind']) => index.docs.filter((d) => d.kind === kind);
const titles = (docs: { doc: SearchDoc }[]) => docs.map((s) => s.doc.title);

/* ---- index shape ---------------------------------------------------------- */

describe('search index: notes and organizations (schema v4)', () => {
  it('indexes every note and every organization with its own kind and URL', () => {
    expect(byKind('note').map((d) => d.url)).toEqual([
      '/notes/deploying-a-frozen-forge/',
      '/notes/heat-buckets/',
      '/notes/alpha-note/',
      '/notes/static-host-configs/',
    ]);
    expect(byKind('org').map((d) => d.url)).toEqual(['/orgs/alpha-org/', '/orgs/canadian-coding/']);
    // artifact order is preserved: notes stay in `compareNotes` order, orgs in slug order
    expect(byKind('note').map((d) => d.title)).toEqual(data.notes.map((n) => n.title));
    expect(byKind('org').map((d) => d.title)).toEqual(data.organizations.map((o) => o.name));
  });

  it('lists the Notes and Organizations index pages alongside the existing pages', () => {
    expect(byKind('page').map((d) => d.url)).toEqual(['/', '/repos/', '/notes/', '/orgs/']);
  });

  it('falls back to a file count / repo count when there is no description', () => {
    expect(byKind('note').find((d) => d.title === 'Static host configs')!.detail).toBe('vercel.json');
    expect(byKind('org').find((d) => d.title === 'alpha')!.detail).toBe('0 repositories');
    // a described note keeps its description
    expect(byKind('note').find((d) => d.title === 'Heat buckets')!.detail).toMatch(/fire-to-ice/);
  });

  it('does not disturb the repo/file/page docs', () => {
    expect(byKind('repo').map((d) => d.title)).toEqual(['alpha', 'beacon']);
    expect(byKind('file').map((d) => d.url)).toEqual(['/repos/alpha/blob/main/README.md/', '/repos/beacon/blob/main/README.md/']);
  });

  it('is deterministic: the same artifact yields an identical index', () => {
    expect(buildSearchIndex(data)).toEqual(index);
  });
});

/* ---- finding things -------------------------------------------------------- */

describe('search: finding notes', () => {
  it('finds a note by title', () => {
    expect(search(index.docs, 'heat buckets')[0]!.doc.url).toBe('/notes/heat-buckets/');
  });

  it('finds a note by tag', () => {
    const hits = search(index.docs, 'deployment').filter((s) => s.doc.kind === 'note');
    expect(titles(hits)).toEqual(['Deploying a frozen forge']);
    // a tag shared by nothing else still resolves to exactly its note
    expect(titles(search(index.docs, 'css').filter((s) => s.doc.kind === 'note'))).toEqual(['Heat buckets']);
  });

  it('finds a note by the name of a file inside it, including a nested path', () => {
    expect(search(index.docs, 'deploy.sh')[0]!.doc.url).toBe('/notes/deploying-a-frozen-forge/');
    expect(search(index.docs, 'pages.yml')[0]!.doc.url).toBe('/notes/deploying-a-frozen-forge/');
    expect(search(index.docs, 'vercel.json')[0]!.doc.url).toBe('/notes/static-host-configs/');
  });

  it('finds a note by slug even when the title is worded differently', () => {
    expect(search(index.docs, 'alpha-note')[0]!.doc.url).toBe('/notes/alpha-note/');
  });
});

describe('search: finding organizations', () => {
  it('finds an organization by name and by description', () => {
    expect(search(index.docs, 'canadian')[0]!.doc.url).toBe('/orgs/canadian-coding/');
    expect(search(index.docs, 'source-available').map((s) => s.doc.url)).toContain('/orgs/canadian-coding/');
  });

  it('finds an organization by a member repo slug', () => {
    const hits = search(index.docs, 'beacon').filter((s) => s.doc.kind === 'org');
    expect(hits.map((s) => s.doc.url)).toEqual(['/orgs/canadian-coding/']);
    // the org with no members is not dragged in
    expect(hits.map((s) => s.doc.url)).not.toContain('/orgs/alpha-org/');
  });
});

/* ---- ranking sanity --------------------------------------------------------- */

describe('search ranking with notes and organizations present', () => {
  it('an exact repo-name match still wins over an identically titled note and org', () => {
    const results = search(index.docs, 'alpha');
    expect(results[0]!.doc.kind).toBe('repo');
    expect(results[0]!.doc.url).toBe('/repos/alpha/');
    // and the same-named note/org are still findable, just below it
    expect(results.map((s) => s.doc.url)).toEqual(expect.arrayContaining(['/notes/alpha-note/', '/orgs/alpha-org/']));
  });

  it('a repo outranks a note that merely mentions it', () => {
    const results = search(index.docs, 'beacon');
    expect(results[0]!.doc.url).toBe('/repos/beacon/');
  });

  it('kind bonuses order repo > note = org = page > file for an otherwise identical match', () => {
    const same = (kind: SearchDoc['kind']): SearchDoc => ({ kind, title: 'widget', detail: '', url: '/x/' });
    const s = (kind: SearchDoc['kind']) => scoreDoc(same(kind), 'widg');
    expect(s('repo')).toBeGreaterThan(s('note'));
    expect(s('note')).toBe(s('org'));
    expect(s('org')).toBe(s('page'));
    expect(s('note')).toBeGreaterThan(s('file'));
  });

  it('all terms must still match', () => {
    expect(search(index.docs, 'heat nomatchterm')).toHaveLength(0);
  });
});

/* ---- degenerate artifacts ---------------------------------------------------- */

describe('search on an artifact with no notes and no organizations', () => {
  const emptyIndex = buildSearchIndex(emptyForgeData());

  it('builds without crashing and lists only the two always-present pages', () => {
    expect(emptyIndex.docs.map((d) => d.url)).toEqual(['/', '/repos/']);
    expect(emptyIndex.docs.some((d) => d.kind === 'note' || d.kind === 'org')).toBe(false);
  });

  it('searching it returns nothing rather than throwing', () => {
    expect(search(emptyIndex.docs, 'anything')).toEqual([]);
    expect(search(emptyIndex.docs, '')).toEqual([]);
  });

  it('repos-only artifacts do not gain a Notes or Organizations page entry', () => {
    const reposOnly = buildSearchIndex({ ...emptyForgeData(), repos: [repo('alpha', 'alpha', null)] });
    expect(reposOnly.docs.filter((d) => d.kind === 'page').map((d) => d.url)).toEqual(['/', '/repos/']);
  });
});
