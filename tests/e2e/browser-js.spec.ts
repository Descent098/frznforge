/**
 * The browser half of the site, tested in a browser.
 *
 * `web/js/search.js` (command-palette ranking) and `web/js/listing.js` (the `?q=&sort=&lang=`
 * round-trip) are shipped to visitors verbatim — no transpile, no bundler — and both survive
 * the 0.4.0 rewrite that deletes the TypeScript and, with it, vitest. Nothing on the Go side
 * replaces them: the Go tests cover the search index the browser *downloads*, not the query run
 * against it, and there is no Go equivalent of a URL query string at all. So Playwright is the
 * last runner standing, and the assertions that lived in `tests/unit/search.test.ts` and
 * `tests/unit/listing.test.ts` live here instead, against the exact files the server serves.
 *
 * How it works: `beforeEach` loads a page of the site and imports the two modules onto
 * `window`; each test then calls the real functions inside the page and returns plain JSON for
 * Node to assert on. Nothing is stubbed or re-implemented — if the ranking here is wrong, the
 * palette a visitor opens with Ctrl+K is wrong in exactly the same way.
 *
 * The `SearchDoc` fixtures below are the index `buildSearchIndex` used to build from the old
 * unit fixtures, transcribed by hand. The builder is Go now (`internal/build/search_index.go`,
 * covered by its own tests) and the browser only ever sees the JSON it emits, so a literal
 * index is both honest about that boundary and immune to the builder moving again.
 */
import { expect, test, type Page } from '@playwright/test';

/* ---- the shapes that cross the boundary ------------------------------------ */

/** Mirrors the `SearchDoc` typedef in web/js/search.js: what the index is made of. */
interface SearchDoc {
  kind: 'repo' | 'file' | 'note' | 'org' | 'page' | 'action';
  title: string;
  detail: string;
  url: string;
  keywords?: string;
  date?: string | null;
}

/** Mirrors the `ListingQuery` typedef in web/js/listing.js. */
interface ListingQuery {
  q: string;
  sort: string;
  languages: string[];
  tags: string[];
  kind: string;
  page: number;
  pageSize: number;
}

/** The two modules, parked on `window` by `loadModules`. */
interface HfWindow {
  __hfSearch: {
    scoreDoc: (doc: SearchDoc, query: string) => number;
    search: (docs: SearchDoc[], query: string, limit?: number) => Array<{ doc: SearchDoc; score: number }>;
  };
  __hfListing: {
    defaultQuery: (pageSize: number) => ListingQuery;
    parseQuery: (params: URLSearchParams, pageSize: number) => ListingQuery;
    toSearchParams: (query: ListingQuery) => URLSearchParams;
  };
}

/* ---- fixtures --------------------------------------------------------------- */

/**
 * The index for an artifact with two repos, four notes and two organizations — the shape
 * Phase 6 added notes and orgs to. `alpha` exists three times over (a repo, a note titled
 * exactly like it, an organization named exactly like it) because that collision is the whole
 * point of the exact-repo-name bonus: someone typing a repo's name means the repo.
 */
const INDEX: SearchDoc[] = [
  { kind: 'page', title: 'Overview', detail: 'Profile page', url: '/' },
  { kind: 'page', title: 'Repositories', detail: 'All repositories', url: '/repos/' },
  { kind: 'page', title: 'Notes', detail: 'All notes', url: '/notes/' },
  { kind: 'page', title: 'Organizations', detail: 'All organizations', url: '/orgs/' },
  {
    kind: 'repo',
    title: 'alpha',
    detail: 'Alpha repo',
    url: '/repos/alpha/',
    keywords: 'alpha ssg TypeScript',
    date: '2026-08-20T10:00:00Z',
  },
  { kind: 'file', title: 'README.md', detail: 'alpha', url: '/repos/alpha/blob/main/README.md/' },
  {
    kind: 'repo',
    title: 'beacon',
    detail: 'Beacon repo',
    url: '/repos/beacon/',
    keywords: 'beacon TypeScript',
    date: '2026-08-20T10:00:00Z',
  },
  { kind: 'file', title: 'README.md', detail: 'beacon', url: '/repos/beacon/blob/main/README.md/' },
  {
    kind: 'note',
    title: 'Deploying a frozen forge',
    detail: 'Three ways to put a fully static forge online.',
    url: '/notes/deploying-a-frozen-forge/',
    // every file path inside the note is a keyword: a reader who remembers a file name, not
    // the note's title, must still land on the note
    keywords: 'deploying-a-frozen-forge deployment static index.md deploy.sh ci/pages.yml',
    date: '2026-07-11T00:00:00Z',
  },
  {
    kind: 'note',
    title: 'Heat buckets',
    detail: 'Why frznforge colours things fire-to-ice by age.',
    url: '/notes/heat-buckets/',
    keywords: 'heat-buckets design css heat-buckets.md',
    date: '2026-06-02T00:00:00Z',
  },
  {
    kind: 'note',
    title: 'alpha',
    detail: 'A note that shares a repo name.',
    url: '/notes/alpha-note/',
    keywords: 'alpha-note alpha-note.md',
    date: null,
  },
  {
    kind: 'note',
    title: 'Static host configs',
    detail: 'vercel.json',
    url: '/notes/static-host-configs/',
    keywords: 'static-host-configs vercel.json',
    date: null,
  },
  { kind: 'org', title: 'alpha', detail: '0 repositories', url: '/orgs/alpha-org/', keywords: 'alpha-org' },
  {
    kind: 'org',
    title: 'Canadian Coding',
    detail: 'Small, sturdy, source-available tools.',
    url: '/orgs/canadian-coding/',
    keywords: 'canadian-coding alpha beacon',
  },
];

/**
 * A one-repo index with two file docs, for the repo/file half of the ranking (the Phase 3/4
 * assertions, which lose their runner along with everything else in tests/unit).
 */
const REPO_INDEX: SearchDoc[] = [
  { kind: 'page', title: 'Overview', detail: 'Profile page', url: '/' },
  { kind: 'page', title: 'Repositories', detail: 'All repositories', url: '/repos/' },
  {
    kind: 'repo',
    title: 'alpha',
    detail: 'Alpha repo',
    url: '/repos/alpha/',
    keywords: 'alpha ssg TypeScript',
    date: '2026-08-21T10:00:00Z',
  },
  { kind: 'file', title: 'src/a.ts', detail: 'alpha', url: '/repos/alpha/blob/main/src/a.ts/' },
  { kind: 'file', title: 'README.md', detail: 'alpha', url: '/repos/alpha/blob/main/README.md/' },
];

/** What an artifact with no repos, notes or organizations indexes: the two pages that always exist. */
const EMPTY_INDEX: SearchDoc[] = [
  { kind: 'page', title: 'Overview', detail: 'Profile page', url: '/' },
  { kind: 'page', title: 'Repositories', detail: 'All repositories', url: '/repos/' },
];

/* ---- harness ---------------------------------------------------------------- */

/**
 * Load a page of the site and pull the two modules onto `window`.
 *
 * The import is handed over as a source *string* deliberately. Playwright ships an evaluate
 * callback to the browser by stringifying the transpiled function, and a transpiler that
 * rewrites `import()` for CommonJS would hand the browser a `require` it cannot run; a string
 * is passed through untouched. The page itself is only a host — these are module tests, so any
 * URL on the origin would serve.
 */
async function loadModules(page: Page) {
  await page.goto('/');
  await page.evaluate(
    `Promise.all([import('/js/search.js'), import('/js/listing.js')]).then(([s, l]) => {
       window.__hfSearch = s;
       window.__hfListing = l;
     })`,
  );
}

/** Rank `docs` for `query` in the page, projected to plain data. */
async function rank(page: Page, docs: SearchDoc[], query: string, limit?: number) {
  return page.evaluate(
    (arg) => {
      const { search } = (window as unknown as HfWindow).__hfSearch;
      const hits = arg.limit === undefined ? search(arg.docs, arg.query) : search(arg.docs, arg.query, arg.limit);
      return hits.map((h) => ({ kind: h.doc.kind, title: h.doc.title, url: h.doc.url, score: h.score }));
    },
    { docs, query, limit },
  );
}

/** Score each doc against `query` in the page. */
async function scoreAll(page: Page, docs: SearchDoc[], query: string): Promise<number[]> {
  return page.evaluate(
    (arg) => {
      const { scoreDoc } = (window as unknown as HfWindow).__hfSearch;
      return arg.docs.map((doc) => scoreDoc(doc, arg.query));
    },
    { docs, query },
  );
}

/** Parse a raw query string the way a deep-linked listing page does on load. */
async function parseSearch(page: Page, search: string, pageSize: number): Promise<ListingQuery> {
  return page.evaluate(
    (arg) => {
      const { parseQuery } = (window as unknown as HfWindow).__hfListing;
      return parseQuery(new URLSearchParams(arg.search), arg.pageSize);
    },
    { search, pageSize },
  );
}

const urls = (hits: Array<{ url: string }>) => hits.map((h) => h.url);
const titles = (hits: Array<{ title: string }>) => hits.map((h) => h.title);

test.beforeEach(async ({ page }) => {
  await loadModules(page);
});

/* ---- web/js/search.js: what the palette finds ------------------------------- */

test.describe('command palette ranking (web/js/search.js)', () => {
  test('finds a note by title', async ({ page }) => {
    expect((await rank(page, INDEX, 'heat buckets'))[0]!.url).toBe('/notes/heat-buckets/');
  });

  test('finds a note by tag', async ({ page }) => {
    // Tags are the only handle a reader has on a note they half-remember, so a tag must
    // resolve to its note and to nothing else.
    const hits = (await rank(page, INDEX, 'deployment')).filter((h) => h.kind === 'note');
    expect(titles(hits)).toEqual(['Deploying a frozen forge']);
    const cssHits = (await rank(page, INDEX, 'css')).filter((h) => h.kind === 'note');
    expect(titles(cssHits)).toEqual(['Heat buckets']);
  });

  test('finds a note by the name of a file inside it, including a nested path', async ({ page }) => {
    // A note's files have no pages of their own; the note is the only destination, so the
    // file name has to lead there rather than dead-ending.
    expect((await rank(page, INDEX, 'deploy.sh'))[0]!.url).toBe('/notes/deploying-a-frozen-forge/');
    expect((await rank(page, INDEX, 'pages.yml'))[0]!.url).toBe('/notes/deploying-a-frozen-forge/');
    expect((await rank(page, INDEX, 'vercel.json'))[0]!.url).toBe('/notes/static-host-configs/');
  });

  test('finds a note by slug even when the title is worded differently', async ({ page }) => {
    // The slug is what appears in the URL bar, so it is what someone types to get back.
    expect((await rank(page, INDEX, 'alpha-note'))[0]!.url).toBe('/notes/alpha-note/');
  });

  test('finds an organization by name and by description', async ({ page }) => {
    expect((await rank(page, INDEX, 'canadian'))[0]!.url).toBe('/orgs/canadian-coding/');
    expect(urls(await rank(page, INDEX, 'source-available'))).toContain('/orgs/canadian-coding/');
  });

  test('finds an organization by a member repo slug', async ({ page }) => {
    // "which org was beacon under?" is a real question the palette should answer; the org
    // that holds nothing must not be dragged along for the ride.
    const hits = (await rank(page, INDEX, 'beacon')).filter((h) => h.kind === 'org');
    expect(urls(hits)).toEqual(['/orgs/canadian-coding/']);
    expect(urls(hits)).not.toContain('/orgs/alpha-org/');
  });

  test('an exact repo-name match still wins over an identically titled note and org', async ({ page }) => {
    // Typing a repo's name exactly is unambiguous. If a note with the same title can take the
    // first row, Enter opens the wrong page — the palette's one unforgivable failure.
    const results = await rank(page, INDEX, 'alpha');
    expect(results[0]!.kind).toBe('repo');
    expect(results[0]!.url).toBe('/repos/alpha/');
    // and the same-named note/org are still findable, just below it
    expect(urls(results)).toEqual(expect.arrayContaining(['/notes/alpha-note/', '/orgs/alpha-org/']));
  });

  test('a repo outranks a note that merely mentions it', async ({ page }) => {
    expect((await rank(page, INDEX, 'beacon'))[0]!.url).toBe('/repos/beacon/');
  });

  test('kind bonuses order repo > note = org = page = action > file for an otherwise identical match', async ({ page }) => {
    // The tiebreaker between kinds when the text match is identical: repos are the site's
    // primary objects, raw file paths are the noisiest, everything else sits between.
    const kinds: Array<SearchDoc['kind']> = ['repo', 'note', 'org', 'page', 'action', 'file'];
    const docs = kinds.map((kind): SearchDoc => ({ kind, title: 'widget', detail: '', url: '/x/' }));
    const [repo, note, org, pageDoc, action, file] = await scoreAll(page, docs, 'widg');
    expect(repo!).toBeGreaterThan(note!);
    expect(note!).toBe(org!);
    expect(org!).toBe(pageDoc!);
    expect(pageDoc!).toBe(action!);
    expect(note!).toBeGreaterThan(file!);
  });

  test('all terms must still match', async ({ page }) => {
    // AND, not OR: adding a word narrows the list. An OR palette gets less useful the more
    // you type, which is the opposite of what typing feels like it should do.
    expect(await rank(page, INDEX, 'heat nomatchterm')).toHaveLength(0);
  });

  test('caps the result list at the limit it is given', async ({ page }) => {
    // The palette renders what it is handed; without the cap a common term would paint the
    // whole index into a dropdown.
    expect(await rank(page, INDEX, 'alpha', 2)).toHaveLength(2);
    expect((await rank(page, INDEX, 'alpha')).length).toBeGreaterThan(2);
  });

  test('ranks a title prefix over a substring, repos over files, and matches file basenames', async ({ page }) => {
    // The Phase 3/4 ranking rules, on an artifact of repos and files only.
    expect((await rank(page, REPO_INDEX, 'alpha'))[0]!.kind).toBe('repo');
    // a bare file name finds the file whose *basename* it is, not merely something containing it
    expect((await rank(page, REPO_INDEX, 'a.ts'))[0]!.title).toBe('src/a.ts');
    expect(await rank(page, REPO_INDEX, 'alpha nomatchterm')).toHaveLength(0);
  });

  test('matches a repo by its keywords: tags and languages', async ({ page }) => {
    // Tags and languages are not shown in the palette row, but they are how people search:
    // "the Go one", "the ssg one".
    expect((await rank(page, REPO_INDEX, 'ssg'))[0]!.kind).toBe('repo');
    expect((await rank(page, REPO_INDEX, 'typescript')).some((h) => h.kind === 'repo')).toBe(true);
  });

  test('an empty query and an empty index both return nothing rather than throwing', async ({ page }) => {
    // An empty query scores nothing, which is what lets the palette show its curated default
    // menu instead of the entire index; an artifact with no repos at all must not crash the
    // palette on the way there.
    expect(await scoreAll(page, [INDEX[0]!], '')).toEqual([0]);
    expect(await rank(page, EMPTY_INDEX, 'anything')).toEqual([]);
    expect(await rank(page, EMPTY_INDEX, '')).toEqual([]);
  });
});

/* ---- web/js/listing.js: the URL round-trip ---------------------------------- */

test.describe('listing URL state (web/js/listing.js)', () => {
  test('round-trips a query through the URL and omits defaults', async ({ page }) => {
    // Every filter the listing offers has to survive being copied out of the address bar and
    // pasted back in — that link is how someone shares "my Go templates". The other half
    // matters just as much: a default-state listing must produce a bare `/repos/`, not a URL
    // full of noise.
    const base = await page.evaluate(() => {
      const { defaultQuery, toSearchParams } = (window as unknown as HfWindow).__hfListing;
      const d = defaultQuery(50);
      return { defaults: d, serialisedDefaults: toSearchParams(d).toString() };
    });
    expect(base.serialisedDefaults).toBe('');

    const query: ListingQuery = {
      ...base.defaults,
      q: 'x y',
      sort: 'name-asc',
      languages: ['Go', 'C++'],
      tags: ['cli'],
      kind: 'template',
      page: 3,
    };
    const trip = await page.evaluate((q: ListingQuery) => {
      const { parseQuery, toSearchParams } = (window as unknown as HfWindow).__hfListing;
      const params = toSearchParams(q);
      return { serialised: params.toString(), parsed: parseQuery(params, q.pageSize) };
    }, query);
    expect(trip.serialised).toBe('q=x+y&sort=name-asc&lang=Go&lang=C%2B%2B&tag=cli&kind=template&page=3');
    expect(trip.parsed).toEqual(query);
  });

  test('falls back on garbage', async ({ page }) => {
    // A hand-edited or truncated URL is a page load like any other: it renders the default
    // listing, it does not render an empty one or throw inside the island's first paint.
    const q = await parseSearch(page, 'sort=bogus&kind=nope&page=-4', 10);
    expect(q.sort).toBe('updated-desc');
    expect(q.kind).toBe('all');
    expect(q.page).toBe(1);
    expect(q.pageSize).toBe(10);
  });

  test('drops empty repeated params and unparseable pages', async ({ page }) => {
    // The same tolerance one level down: a stray `lang=` (what a form submit leaves behind)
    // must not become a filter that matches no repo and shows the visitor an empty grid.
    const q = await parseSearch(page, 'lang=&lang=Go&tag=&page=abc', 24);
    expect(q.languages).toEqual(['Go']);
    expect(q.tags).toEqual([]);
    expect(q.page).toBe(1);
    expect(q.q).toBe('');
    expect(q.pageSize).toBe(24);
  });
});

/* ---- the shipped index --------------------------------------------------------- */

/**
 * The ranking, run against the index the build actually emitted.
 *
 * Every test above hands `search` a hand-written array. That is the right way to pin an exact
 * ranking — the fixture is stable and the expected order can be reasoned about — but it means
 * nothing in the suite connects the index PRODUCER to the index CONSUMER. Under vitest that gap
 * did not exist: `buildSearchIndex` and `scoreDoc` were two functions in one language, and the
 * test called both. Since 0.4.0 the producer is Go (`internal/build/search_index.go`) and the
 * consumer is this JavaScript, and nothing had been standing between them.
 *
 * So these tests fetch `/search-index.json` from the running site — the same file the palette
 * downloads — and assert the properties that have to hold for ANY index, whatever the fixture
 * repositories happen to contain. They cannot pin a specific ordering, and they are not trying
 * to; they are the coupling, and the tests above are the contract.
 */
test.describe('the ranking against the shipped index', () => {
  test('every emitted document is rankable', async ({ page }) => {
    // The failure this exists for: `scoreDoc` adds `KIND_BONUS[doc.kind]`, and an unlisted kind
    // made that `undefined`, so the score became NaN — and because `search` keeps a doc only
    // when `score > 0`, and NaN fails every comparison, the document did not rank last. It
    // disappeared, silently, with nothing logged. A kind added on the Go side and not here is
    // exactly how that would happen now that the two are in different languages.
    const result = await page.evaluate(async () => {
      const { scoreDoc } = (window as unknown as HfWindow).__hfSearch;
      const index = await fetch('/search-index.json').then((r) => r.json());
      const docs: SearchDoc[] = index.docs;
      const bad: Array<{ kind: string; title: string; score: number }> = [];
      for (const doc of docs) {
        // A term drawn from the document's own title must match it. If it does not, the doc is
        // unreachable from the palette however it is spelled.
        const term = doc.title.split(/[\s\/]+/).filter(Boolean).pop() ?? doc.title;
        const score = scoreDoc(doc, term);
        if (!Number.isFinite(score) || score <= 0) bad.push({ kind: doc.kind, title: doc.title, score });
      }
      return { total: docs.length, kinds: [...new Set(docs.map((d) => d.kind))].sort(), bad };
    });

    // A vacuous pass is the real risk here: an empty index satisfies every loop below it.
    expect(result.total).toBeGreaterThan(10);
    expect(result.kinds.length).toBeGreaterThan(2);
    expect(result.bad).toEqual([]);
  });

  test('a repository is findable by its own name, and comes first', async ({ page }) => {
    // The one ranking promise the palette makes to someone who knows what they are looking for.
    // Asserted against whatever the fixture site contains rather than a chosen fixture, so it
    // keeps holding as the corpus changes.
    const result = await page.evaluate(async () => {
      const { search } = (window as unknown as HfWindow).__hfSearch;
      const index = await fetch('/search-index.json').then((r) => r.json());
      const docs: SearchDoc[] = index.docs;
      const repos = docs.filter((d) => d.kind === 'repo');
      return {
        repoCount: repos.length,
        misses: repos
          .map((repo) => ({ want: repo.title, got: search(docs, repo.title)[0]?.doc.title ?? null }))
          .filter((r) => r.got !== r.want),
      };
    });

    expect(result.repoCount).toBeGreaterThan(0);
    expect(result.misses).toEqual([]);
  });

  test('every result the palette would show points at a page that exists', async ({ page }) => {
    // This was red against the Astro engine, on exactly `docs/c#-tips.md` and
    // `docs/50% off.txt`: the TypeScript index listed every blob in the tree, including paths
    // that can never have a static page. That is the palette-404 bug fixed in the Go build and
    // recorded in the changelog, and this test rediscovering it from the browser's side is the
    // test working. It was left failing rather than weakened to accommodate an engine that was
    // being deleted; Phase 9 deleted it, and there is only one engine now.
    //
    // The palette is how this site is navigated, so a result that 404s is worse than no result.
    // The Go build already excludes unservable paths from the index
    // (internal/build/search_index_test.go); this checks the claim from the browser's side,
    // against the served files rather than the emitted tree.
    const dead = await page.evaluate(async () => {
      const index = await fetch('/search-index.json').then((r) => r.json());
      const docs: Array<{ url: string; kind: string }> = index.docs;
      // Sampled, not exhaustive: a large corpus makes one request per file document and the
      // suite has a 60s budget. Every non-file kind is checked in full — those are the pages a
      // rename breaks — and the files are sampled evenly across the list.
      const files = docs.filter((d) => d.kind === 'file');
      const others = docs.filter((d) => d.kind !== 'file' && !d.url.startsWith('#'));
      const step = Math.max(1, Math.ceil(files.length / 25));
      const sample = [...others, ...files.filter((_, i) => i % step === 0)];
      const bad: string[] = [];
      for (const doc of sample) {
        const res = await fetch(doc.url, { method: 'GET' });
        if (!res.ok) bad.push(`${doc.url} -> ${res.status}`);
      }
      return bad;
    });

    expect(dead).toEqual([]);
  });
});

/* ---- the regression the shipped-index tests were written for -------------------- */

test('an unknown kind ranks last instead of disappearing', async ({ page }) => {
  // The unit-level statement of the bug above, so the fix is pinned by something that does not
  // depend on the fixture site ever containing an unknown kind — which, being a bug, it should
  // not. `future` is deliberately not in KIND_BONUS.
  const hits = await rank(
    page,
    [
      { kind: 'future' as SearchDoc['kind'], title: 'alpha thing', detail: 'from a newer index', url: '/future/' },
      { kind: 'note', title: 'alpha note', detail: 'a note', url: '/notes/alpha/' },
    ],
    'alpha',
  );

  expect(urls(hits)).toContain('/future/');
  // Last, not first: an unrecognised kind earns no bonus, which is the whole point.
  expect(urls(hits)[urls(hits).length - 1]).toBe('/future/');
  expect(hits.every((h) => Number.isFinite(h.score))).toBe(true);
});
