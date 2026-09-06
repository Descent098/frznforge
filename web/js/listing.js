/**
 * Repo listing: filter / sort / search / paginate. Pure functions, shared by the build-time
 * first render and by the `<hf-repo-listing>` element in the browser — one implementation,
 * not a server copy and a client copy that drift.
 *
 * `src/lib/listing.ts` re-exports everything here and adds `summarize()`, which takes a full
 * `Repo` and so is server-only.
 *
 * See `web/js/format.js` for the rules this folder follows.
 */

/**
 * Client-safe projection of a Repo for cards and filtering (no commits/tree/files).
 *
 * @typedef {object} RepoSummary
 * @property {string} slug
 * @property {string} name
 * @property {string | null} description
 * @property {string[]} tags
 * @property {boolean} template
 * @property {boolean} empty
 * @property {Array<{ name: string, percent: number, color: string | null }>} languages
 *   All languages (name + percent + color), ordered by bytes desc. Cards show the top 3.
 * @property {number} commitCount
 * @property {string | null} createdAt
 * @property {string | null} updatedAt
 */

/** @typedef {'updated-desc' | 'updated-asc' | 'created-desc' | 'created-asc' | 'name-asc' | 'name-desc'} SortKey */
/** @typedef {'all' | 'template' | 'normal'} KindFilter */

/**
 * @typedef {object} ListingQuery
 * @property {string} q
 * @property {SortKey} sort
 * @property {string[]} languages
 * @property {string[]} tags
 * @property {KindFilter} kind
 * @property {number} page 1-based
 * @property {number} pageSize
 */

/**
 * @typedef {object} ListingResult
 * @property {RepoSummary[]} items
 * @property {number} total
 * @property {number} page
 * @property {number} pageCount
 */

/** @typedef {{ name: string, count: number }} Facet */

/** @type {readonly SortKey[]} */
export const SORT_KEYS = ['updated-desc', 'updated-asc', 'created-desc', 'created-asc', 'name-asc', 'name-desc'];

/** @type {Record<SortKey, string>} */
export const SORT_LABELS = {
  'updated-desc': 'Recently updated',
  'updated-asc': 'Least recently updated',
  'created-desc': 'Newest',
  'created-asc': 'Oldest',
  'name-asc': 'Name A→Z',
  'name-desc': 'Name Z→A',
};

/**
 * @param {number} pageSize
 * @returns {ListingQuery}
 */
export function defaultQuery(pageSize) {
  return { q: '', sort: 'updated-desc', languages: [], tags: [], kind: 'all', page: 1, pageSize };
}

/** @param {string} s */
const norm = (s) => s.toLowerCase().trim();

/**
 * Does the repo match the free-text query? (name, description, tags, languages; all terms must match)
 * @param {RepoSummary} repo
 * @param {string} q
 * @returns {boolean}
 */
export function matchesQuery(repo, q) {
  const terms = norm(q).split(/\s+/).filter(Boolean);
  if (terms.length === 0) return true;
  const hay = [repo.name, repo.slug, repo.description ?? '', ...repo.tags, ...repo.languages.map((l) => l.name)]
    .join(' ')
    .toLowerCase();
  return terms.every((t) => hay.includes(t));
}

/**
 * @param {RepoSummary} repo
 * @param {ListingQuery} query
 * @returns {boolean}
 */
export function matchesFilters(repo, query) {
  if (query.kind === 'template' && !repo.template) return false;
  if (query.kind === 'normal' && repo.template) return false;
  if (query.languages.length) {
    const have = new Set(repo.languages.map((l) => l.name));
    if (!query.languages.every((l) => have.has(l))) return false;
  }
  if (query.tags.length) {
    const have = new Set(repo.tags);
    if (!query.tags.every((t) => have.has(t))) return false;
  }
  return matchesQuery(repo, query.q);
}

/**
 * @param {string | null} a
 * @param {string | null} b
 */
const byDate = (a, b) => (a ?? '').localeCompare(b ?? '');

/**
 * @param {RepoSummary[]} repos
 * @param {SortKey} sort
 * @returns {RepoSummary[]}
 */
export function sortRepos(repos, sort) {
  const out = [...repos];
  /**
   * @param {RepoSummary} a
   * @param {RepoSummary} b
   */
  const nameAsc = (a, b) => a.name.localeCompare(b.name, 'en', { sensitivity: 'base' }) || a.slug.localeCompare(b.slug);
  switch (sort) {
    case 'updated-desc': out.sort((a, b) => byDate(b.updatedAt, a.updatedAt) || nameAsc(a, b)); break;
    case 'updated-asc': out.sort((a, b) => byDate(a.updatedAt, b.updatedAt) || nameAsc(a, b)); break;
    case 'created-desc': out.sort((a, b) => byDate(b.createdAt, a.createdAt) || nameAsc(a, b)); break;
    case 'created-asc': out.sort((a, b) => byDate(a.createdAt, b.createdAt) || nameAsc(a, b)); break;
    case 'name-asc': out.sort(nameAsc); break;
    case 'name-desc': out.sort((a, b) => nameAsc(b, a)); break;
  }
  return out;
}

/**
 * Filter + sort + paginate. Page is clamped into range; pageCount is at least 1.
 * @param {RepoSummary[]} repos
 * @param {ListingQuery} query
 * @returns {ListingResult}
 */
export function applyListing(repos, query) {
  const filtered = sortRepos(repos.filter((r) => matchesFilters(r, query)), query.sort);
  const pageSize = Math.max(1, query.pageSize);
  const pageCount = Math.max(1, Math.ceil(filtered.length / pageSize));
  const page = Math.min(Math.max(1, query.page), pageCount);
  const start = (page - 1) * pageSize;
  return { items: filtered.slice(start, start + pageSize), total: filtered.length, page, pageCount };
}

/**
 * Distinct languages and tags with repo counts, sorted by count desc then name.
 * @param {RepoSummary[]} repos
 * @returns {{ languages: Facet[], tags: Facet[], templates: number }}
 */
export function facets(repos) {
  /** @type {Map<string, number>} */
  const langs = new Map();
  /** @type {Map<string, number>} */
  const tags = new Map();
  let templates = 0;
  for (const r of repos) {
    for (const l of r.languages) langs.set(l.name, (langs.get(l.name) ?? 0) + 1);
    for (const t of r.tags) tags.set(t, (tags.get(t) ?? 0) + 1);
    if (r.template) templates++;
  }
  /** @param {Map<string, number>} m */
  const toFacets = (m) =>
    [...m.entries()].map(([name, count]) => ({ name, count })).sort((a, b) => b.count - a.count || a.name.localeCompare(b.name));
  return { languages: toFacets(langs), tags: toFacets(tags), templates };
}

/* ---- URL state ----------------------------------------------------------- */

/**
 * Parse `?q=&sort=&lang=a&lang=b&tag=x&kind=&page=` into a query (unknown values fall back).
 * @param {URLSearchParams} params
 * @param {number} pageSize
 * @returns {ListingQuery}
 */
export function parseQuery(params, pageSize) {
  const d = defaultQuery(pageSize);
  const sort = params.get('sort');
  const kind = params.get('kind');
  const page = Number.parseInt(params.get('page') ?? '1', 10);
  return {
    q: params.get('q') ?? d.q,
    sort: /** @type {readonly string[]} */ (SORT_KEYS).includes(sort ?? '') ? /** @type {SortKey} */ (sort) : d.sort,
    languages: params.getAll('lang').filter(Boolean),
    tags: params.getAll('tag').filter(Boolean),
    kind: kind === 'template' || kind === 'normal' ? kind : 'all',
    page: Number.isFinite(page) && page > 0 ? page : 1,
    pageSize,
  };
}

/**
 * Serialise a query to URL params, omitting defaults so clean URLs stay clean.
 * @param {ListingQuery} query
 * @returns {URLSearchParams}
 */
export function toSearchParams(query) {
  const p = new URLSearchParams();
  if (query.q) p.set('q', query.q);
  if (query.sort !== 'updated-desc') p.set('sort', query.sort);
  for (const l of query.languages) p.append('lang', l);
  for (const t of query.tags) p.append('tag', t);
  if (query.kind !== 'all') p.set('kind', query.kind);
  if (query.page > 1) p.set('page', String(query.page));
  return p;
}
