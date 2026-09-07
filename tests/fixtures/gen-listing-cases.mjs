/**
 * Generate tests/fixtures/listing-cases.json — the shared golden pinning the two
 * implementations of the listing query.
 *
 * `web/js/listing.js` runs in the browser (it rebuilds the grid when a filter is touched);
 * `internal/render/listing.go` runs at build time (it renders page one). If they disagree, the
 * listing changes the instant a visitor interacts with it — the kind of bug that looks like a
 * flicker and gets ignored. Generated FROM the JavaScript, so the JavaScript is the reference.
 *
 * Run:  node tests/fixtures/gen-listing-cases.mjs
 */
import crypto from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { applyListing, defaultQuery, facets, matchesFilters, matchesQuery, sortRepos } from '../../web/js/listing.js';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');

/**
 * The fixture records the sha256 of the source it was generated from.
 *
 * Without it the golden is a snapshot with no expiry: change web/js/listing.js, forget to re-run this
 * script, and the Go test keeps passing against the OLD behaviour while the browser ships the
 * new one — the exact divergence the fixture exists to prevent, made invisible by the fixture
 * itself. The Go side re-hashes the file and fails loudly when the two disagree.
 */
function sourceStamp(rel) {
  const abs = path.join(ROOT, rel);
  return { file: rel, sha256: crypto.createHash('sha256').update(fs.readFileSync(abs)).digest('hex') };
}


/** A corpus chosen to exercise every tiebreak: same dates, same names, mixed case, nulls. */
const repos = [
  { slug: 'alpha', name: 'alpha', description: 'A static site generator', tags: ['ssg', 'cli'], template: false, empty: false, languages: [{ name: 'Go', percent: 80, color: '#00ADD8' }, { name: 'CSS', percent: 20, color: null }], commitCount: 12, createdAt: '2024-01-01T00:00:00Z', updatedAt: '2026-09-01T00:00:00Z' },
  { slug: 'bravo', name: 'Bravo', description: null, tags: ['cli'], template: false, empty: false, languages: [{ name: 'Go', percent: 100, color: '#00ADD8' }], commitCount: 3, createdAt: '2023-06-01T00:00:00Z', updatedAt: '2025-01-01T00:00:00Z' },
  { slug: 'charlie', name: 'charlie', description: 'Templates and things', tags: ['template'], template: true, empty: false, languages: [{ name: 'TypeScript', percent: 100, color: '#3178c6' }], commitCount: 40, createdAt: '2025-01-01T00:00:00Z', updatedAt: '2026-09-01T00:00:00Z' },
  { slug: 'delta', name: 'Delta', description: 'no languages here', tags: [], template: false, empty: false, languages: [], commitCount: 1, createdAt: null, updatedAt: null },
  { slug: 'echo', name: 'alpha', description: 'duplicate display name', tags: ['ssg'], template: false, empty: true, languages: [], commitCount: 0, createdAt: '2022-01-01T00:00:00Z', updatedAt: '2026-09-01T00:00:00Z' },
  { slug: 'foxtrot', name: 'FOXTROT', description: 'SHOUTING', tags: ['cli', 'ssg'], template: true, empty: false, languages: [{ name: 'Rust', percent: 55.5, color: '#dea584' }, { name: 'Go', percent: 44.5, color: '#00ADD8' }], commitCount: 7, createdAt: '2024-06-01T00:00:00Z', updatedAt: '2024-06-02T00:00:00Z' },
];

const queries = [
  { name: 'default', q: defaultQuery(50) },
  { name: 'page-size-2', q: { ...defaultQuery(2) } },
  { name: 'page-2-of-2', q: { ...defaultQuery(2), page: 2 } },
  { name: 'page-clamped-high', q: { ...defaultQuery(2), page: 99 } },
  { name: 'page-clamped-low', q: { ...defaultQuery(2), page: 0 } },
  { name: 'sort-updated-asc', q: { ...defaultQuery(50), sort: 'updated-asc' } },
  { name: 'sort-created-desc', q: { ...defaultQuery(50), sort: 'created-desc' } },
  { name: 'sort-created-asc', q: { ...defaultQuery(50), sort: 'created-asc' } },
  { name: 'sort-name-asc', q: { ...defaultQuery(50), sort: 'name-asc' } },
  { name: 'sort-name-desc', q: { ...defaultQuery(50), sort: 'name-desc' } },
  { name: 'kind-template', q: { ...defaultQuery(50), kind: 'template' } },
  { name: 'kind-normal', q: { ...defaultQuery(50), kind: 'normal' } },
  { name: 'lang-go', q: { ...defaultQuery(50), languages: ['Go'] } },
  { name: 'lang-two', q: { ...defaultQuery(50), languages: ['Go', 'CSS'] } },
  { name: 'tag-cli', q: { ...defaultQuery(50), tags: ['cli'] } },
  { name: 'tag-two', q: { ...defaultQuery(50), tags: ['cli', 'ssg'] } },
  { name: 'text-single', q: { ...defaultQuery(50), q: 'static' } },
  { name: 'text-multi', q: { ...defaultQuery(50), q: 'static site' } },
  { name: 'text-case', q: { ...defaultQuery(50), q: 'SHOUTING' } },
  { name: 'text-by-slug', q: { ...defaultQuery(50), q: 'foxtrot' } },
  { name: 'text-by-language', q: { ...defaultQuery(50), q: 'rust' } },
  { name: 'text-no-match', q: { ...defaultQuery(50), q: 'zzz-nothing' } },
  { name: 'combined', q: { ...defaultQuery(50), q: 'a', languages: ['Go'], tags: ['cli'], kind: 'normal' } },
];

const [languages, tags] = (() => {
  const f = facets(repos);
  return [f.languages, f.tags];
})();

const out = {
  source: sourceStamp('web/js/listing.js'),
  repos,
  facets: { languages, tags, templates: facets(repos).templates },
  sorts: Object.fromEntries(
    ['updated-desc', 'updated-asc', 'created-desc', 'created-asc', 'name-asc', 'name-desc'].map((s) => [
      s,
      sortRepos(repos, s).map((r) => r.slug),
    ]),
  ),
  matches: repos.map((r) => ({
    slug: r.slug,
    query: matchesQuery(r, 'a'),
    filters: matchesFilters(r, { ...defaultQuery(50), languages: ['Go'] }),
  })),
  // A query of nothing but whitespace matches everything: it has no terms, and a listing that
  // hid every repo the moment someone leant on the space bar would read as the site breaking.
  // Both sides tokenise before they decide, and this is the only case that proves it — neither
  // an empty string nor a real word exercises the same branch.
  blankQueries: ['', ' ', '   ', '\t\n'].map((q) => ({
    query: q,
    matchesAll: repos.every((r) => matchesQuery(r, q)),
    kept: applyListing(repos, { ...defaultQuery(50), q }).total,
  })),
  listings: queries.map(({ name, q }) => {
    const result = applyListing(repos, q);
    return { name, query: q, items: result.items.map((r) => r.slug), total: result.total, page: result.page, pageCount: result.pageCount };
  }),
};

const dest = path.join(ROOT, 'tests', 'fixtures', 'listing-cases.json');
fs.writeFileSync(dest, JSON.stringify(out, null, 2) + '\n');
console.log(`wrote ${path.relative(ROOT, dest)} — ${out.listings.length} listings, ${Object.keys(out.sorts).length} sorts`);
