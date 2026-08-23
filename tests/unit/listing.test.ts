import { describe, expect, it } from 'vitest';
import {
  applyListing,
  defaultQuery,
  facets,
  matchesQuery,
  parseQuery,
  sortRepos,
  toSearchParams,
  type RepoSummary,
} from '../../src/lib/listing';

const mk = (over: Partial<RepoSummary> & { slug: string }): RepoSummary => ({
  name: over.slug,
  description: null,
  tags: [],
  template: false,
  empty: false,
  languages: [],
  commitCount: 1,
  createdAt: '2020-01-01T00:00:00Z',
  updatedAt: '2021-01-01T00:00:00Z',
  ...over,
});

const repos: RepoSummary[] = [
  mk({ slug: 'alpha', name: 'Alpha', description: 'A static site generator', tags: ['ssg', 'astro'], languages: [{ name: 'TypeScript', percent: 80, color: null }, { name: 'CSS', percent: 20, color: null }], updatedAt: '2026-08-20T00:00:00Z', createdAt: '2024-01-01T00:00:00Z' }),
  mk({ slug: 'bravo', name: 'bravo', description: 'Go CLI for deploys', tags: ['cli'], languages: [{ name: 'Go', percent: 100, color: null }], updatedAt: '2025-01-01T00:00:00Z', createdAt: '2019-06-01T00:00:00Z' }),
  mk({ slug: 'charlie', name: 'Charlie', template: true, tags: ['astro'], languages: [{ name: 'TypeScript', percent: 100, color: null }], updatedAt: '2023-01-01T00:00:00Z', createdAt: '2023-01-01T00:00:00Z' }),
  mk({ slug: 'empty', name: 'empty', empty: true, updatedAt: null, createdAt: null, commitCount: 0 }),
];

describe('matchesQuery', () => {
  it('matches name, description, tags and languages, all terms required', () => {
    expect(matchesQuery(repos[0]!, 'static')).toBe(true);
    expect(matchesQuery(repos[0]!, 'ALPHA ssg')).toBe(true);
    expect(matchesQuery(repos[0]!, 'typescript')).toBe(true);
    expect(matchesQuery(repos[0]!, 'alpha go')).toBe(false);
    expect(matchesQuery(repos[0]!, '   ')).toBe(true);
  });
});

describe('sortRepos', () => {
  it('sorts by updated desc by default with nulls last', () => {
    expect(sortRepos(repos, 'updated-desc').map((r) => r.slug)).toEqual(['alpha', 'bravo', 'charlie', 'empty']);
  });
  it('sorts by created asc / name', () => {
    expect(sortRepos(repos, 'created-asc').map((r) => r.slug)).toEqual(['empty', 'bravo', 'charlie', 'alpha']);
    expect(sortRepos(repos, 'name-asc').map((r) => r.slug)).toEqual(['alpha', 'bravo', 'charlie', 'empty']);
    expect(sortRepos(repos, 'name-desc').map((r) => r.slug)).toEqual(['empty', 'charlie', 'bravo', 'alpha']);
  });
});

describe('applyListing', () => {
  it('filters by language (AND), tag, kind and query', () => {
    const q = defaultQuery(50);
    expect(applyListing(repos, { ...q, languages: ['TypeScript'] }).items.map((r) => r.slug)).toEqual(['alpha', 'charlie']);
    expect(applyListing(repos, { ...q, languages: ['TypeScript', 'CSS'] }).items.map((r) => r.slug)).toEqual(['alpha']);
    expect(applyListing(repos, { ...q, tags: ['astro'] }).total).toBe(2);
    expect(applyListing(repos, { ...q, kind: 'template' }).items.map((r) => r.slug)).toEqual(['charlie']);
    expect(applyListing(repos, { ...q, kind: 'normal' }).total).toBe(3);
    expect(applyListing(repos, { ...q, q: 'deploys' }).items.map((r) => r.slug)).toEqual(['bravo']);
  });
  it('paginates and clamps the page', () => {
    const q = { ...defaultQuery(2), sort: 'name-asc' as const };
    const p1 = applyListing(repos, { ...q, page: 1 });
    expect(p1.items.map((r) => r.slug)).toEqual(['alpha', 'bravo']);
    expect(p1.pageCount).toBe(2);
    expect(applyListing(repos, { ...q, page: 2 }).items.map((r) => r.slug)).toEqual(['charlie', 'empty']);
    expect(applyListing(repos, { ...q, page: 99 }).page).toBe(2);
    expect(applyListing(repos, { ...q, page: 0 }).page).toBe(1);
    expect(applyListing([], q).pageCount).toBe(1);
  });
});

describe('facets', () => {
  it('counts languages, tags, templates', () => {
    const f = facets(repos);
    expect(f.languages).toEqual([{ name: 'TypeScript', count: 2 }, { name: 'CSS', count: 1 }, { name: 'Go', count: 1 }]);
    expect(f.tags[0]).toEqual({ name: 'astro', count: 2 });
    expect(f.templates).toBe(1);
  });
});

describe('URL state', () => {
  it('round-trips and omits defaults', () => {
    const q = { ...defaultQuery(50), q: 'x y', sort: 'name-asc' as const, languages: ['Go', 'C++'], tags: ['cli'], kind: 'template' as const, page: 3 };
    const params = toSearchParams(q);
    expect(params.toString()).toBe('q=x+y&sort=name-asc&lang=Go&lang=C%2B%2B&tag=cli&kind=template&page=3');
    expect(parseQuery(params, 50)).toEqual(q);
    expect(toSearchParams(defaultQuery(50)).toString()).toBe('');
  });
  it('falls back on garbage', () => {
    const q = parseQuery(new URLSearchParams('sort=bogus&kind=nope&page=-4'), 10);
    expect(q.sort).toBe('updated-desc');
    expect(q.kind).toBe('all');
    expect(q.page).toBe(1);
    expect(q.pageSize).toBe(10);
  });
});
