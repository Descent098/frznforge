/**
 * Repo listing: filter / sort / search / paginate. Pure functions so they can be unit-tested
 * and shared between the build-time first render and the Svelte island in the browser.
 */
import type { LanguageStat, Repo } from './data/schema';

/** Client-safe projection of a Repo for cards and filtering (no commits/tree/files). */
export interface RepoSummary {
  slug: string;
  name: string;
  description: string | null;
  tags: string[];
  template: boolean;
  empty: boolean;
  /** All languages (name + percent + color), ordered by bytes desc. Cards show the top 3. */
  languages: Array<Pick<LanguageStat, 'name' | 'percent' | 'color'>>;
  commitCount: number;
  createdAt: string | null;
  updatedAt: string | null;
}

export function summarize(repo: Repo): RepoSummary {
  return {
    slug: repo.slug,
    name: repo.name,
    description: repo.description,
    tags: repo.tags,
    template: repo.template,
    empty: repo.empty,
    languages: repo.languages.map((l) => ({ name: l.name, percent: l.percent, color: l.color })),
    commitCount: repo.commitCount,
    createdAt: repo.createdAt,
    updatedAt: repo.updatedAt,
  };
}

export const SORT_KEYS = ['updated-desc', 'updated-asc', 'created-desc', 'created-asc', 'name-asc', 'name-desc'] as const;
export type SortKey = (typeof SORT_KEYS)[number];
export const SORT_LABELS: Record<SortKey, string> = {
  'updated-desc': 'Recently updated',
  'updated-asc': 'Least recently updated',
  'created-desc': 'Newest',
  'created-asc': 'Oldest',
  'name-asc': 'Name A→Z',
  'name-desc': 'Name Z→A',
};

export type KindFilter = 'all' | 'template' | 'normal';

export interface ListingQuery {
  q: string;
  sort: SortKey;
  languages: string[];
  tags: string[];
  kind: KindFilter;
  page: number; // 1-based
  pageSize: number;
}

export function defaultQuery(pageSize: number): ListingQuery {
  return { q: '', sort: 'updated-desc', languages: [], tags: [], kind: 'all', page: 1, pageSize };
}

export interface ListingResult {
  items: RepoSummary[];
  total: number;
  page: number;
  pageCount: number;
}

const norm = (s: string) => s.toLowerCase().trim();

/** Does the repo match the free-text query? (name, description, tags, languages; all terms must match) */
export function matchesQuery(repo: RepoSummary, q: string): boolean {
  const terms = norm(q).split(/\s+/).filter(Boolean);
  if (terms.length === 0) return true;
  const hay = [repo.name, repo.slug, repo.description ?? '', ...repo.tags, ...repo.languages.map((l) => l.name)]
    .join(' ')
    .toLowerCase();
  return terms.every((t) => hay.includes(t));
}

export function matchesFilters(repo: RepoSummary, query: ListingQuery): boolean {
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

const byDate = (a: string | null, b: string | null) => (a ?? '').localeCompare(b ?? '');

export function sortRepos(repos: RepoSummary[], sort: SortKey): RepoSummary[] {
  const out = [...repos];
  const nameAsc = (a: RepoSummary, b: RepoSummary) => a.name.localeCompare(b.name, 'en', { sensitivity: 'base' }) || a.slug.localeCompare(b.slug);
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

/** Filter + sort + paginate. Page is clamped into range; pageCount is at least 1. */
export function applyListing(repos: RepoSummary[], query: ListingQuery): ListingResult {
  const filtered = sortRepos(repos.filter((r) => matchesFilters(r, query)), query.sort);
  const pageSize = Math.max(1, query.pageSize);
  const pageCount = Math.max(1, Math.ceil(filtered.length / pageSize));
  const page = Math.min(Math.max(1, query.page), pageCount);
  const start = (page - 1) * pageSize;
  return { items: filtered.slice(start, start + pageSize), total: filtered.length, page, pageCount };
}

export interface Facet { name: string; count: number }

/** Distinct languages and tags with repo counts, sorted by count desc then name. */
export function facets(repos: RepoSummary[]): { languages: Facet[]; tags: Facet[]; templates: number } {
  const langs = new Map<string, number>();
  const tags = new Map<string, number>();
  let templates = 0;
  for (const r of repos) {
    for (const l of r.languages) langs.set(l.name, (langs.get(l.name) ?? 0) + 1);
    for (const t of r.tags) tags.set(t, (tags.get(t) ?? 0) + 1);
    if (r.template) templates++;
  }
  const toFacets = (m: Map<string, number>) =>
    [...m.entries()].map(([name, count]) => ({ name, count })).sort((a, b) => b.count - a.count || a.name.localeCompare(b.name));
  return { languages: toFacets(langs), tags: toFacets(tags), templates };
}

/* ---- URL state ----------------------------------------------------------- */

/** Parse `?q=&sort=&lang=a&lang=b&tag=x&kind=&page=` into a query (unknown values fall back). */
export function parseQuery(params: URLSearchParams, pageSize: number): ListingQuery {
  const d = defaultQuery(pageSize);
  const sort = params.get('sort');
  const kind = params.get('kind');
  const page = Number.parseInt(params.get('page') ?? '1', 10);
  return {
    q: params.get('q') ?? d.q,
    sort: (SORT_KEYS as readonly string[]).includes(sort ?? '') ? (sort as SortKey) : d.sort,
    languages: params.getAll('lang').filter(Boolean),
    tags: params.getAll('tag').filter(Boolean),
    kind: kind === 'template' || kind === 'normal' ? kind : 'all',
    page: Number.isFinite(page) && page > 0 ? page : 1,
    pageSize,
  };
}

/** Serialise a query to URL params, omitting defaults so clean URLs stay clean. */
export function toSearchParams(query: ListingQuery): URLSearchParams {
  const p = new URLSearchParams();
  if (query.q) p.set('q', query.q);
  if (query.sort !== 'updated-desc') p.set('sort', query.sort);
  for (const l of query.languages) p.append('lang', l);
  for (const t of query.tags) p.append('tag', t);
  if (query.kind !== 'all') p.set('kind', query.kind);
  if (query.page > 1) p.set('page', String(query.page));
  return p;
}
