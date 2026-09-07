/**
 * Scoring for the command palette. The index itself is built at build time
 * (`buildSearchIndex` in `src/lib/search.ts`, emitted as `/search-index.json`); the ranking
 * runs in the browser against that JSON, and in the unit tests against fixtures — one
 * implementation for both, since a ranking that differs between them is untestable.
 *
 * See `web/js/format.js` for the rules this folder follows.
 */

/**
 * @typedef {object} SearchDoc
 * @property {'repo' | 'file' | 'note' | 'org' | 'page' | 'action'} kind
 * @property {string} title Primary display text (repo name, file path, page title).
 * @property {string} detail Secondary text (description, repo slug, hint).
 * @property {string} url
 * @property {string} [keywords] Extra matchable text (tags, languages).
 * @property {string | null} [date] ISO date for recency tie-breaks (repos).
 */

/** @typedef {{ doc: SearchDoc, score: number }} ScoredDoc */

/**
 * Per-kind ranking bonus, applied once. Repos are the site's primary objects and outrank
 * everything; notes and organizations sit with pages, above raw file paths.
 *
 * @type {Record<SearchDoc['kind'], number>}
 */
const KIND_BONUS = {
  repo: 2,
  note: 1,
  org: 1,
  page: 1,
  action: 1,
  file: 0,
};

/**
 * Score `query` against a doc. 0 = no match. All whitespace-separated terms must match
 * title/detail/keywords (case-insensitive). Bonuses: prefix > word/segment boundary >
 * substring; shorter titles rank higher; repos outrank files at equal score.
 *
 * Typing a repository's name exactly is unambiguous, so that case gets a bonus large enough
 * that no note, organization or file can displace it however short its own title is.
 *
 * @param {SearchDoc} doc
 * @param {string} query
 * @returns {number}
 */
export function scoreDoc(doc, query) {
  const terms = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (terms.length === 0) return 0;
  const title = doc.title.toLowerCase();
  const hay = `${title} ${doc.detail.toLowerCase()} ${(doc.keywords ?? '').toLowerCase()}`;
  let score = 0;
  for (const term of terms) {
    if (!hay.includes(term)) return 0;
    let s = 1; // substring somewhere
    const idx = title.indexOf(term);
    if (idx === 0) s = 8; // title prefix
    else if (idx > 0 && /[\s\/\-_.]/.test(title[idx - 1] ?? '')) s = 5; // boundary in title (path segment, word)
    else if (idx > 0) s = 3; // substring in title
    // basename bonus for files: query matches the file name itself
    if (doc.kind === 'file') {
      const base = title.slice(title.lastIndexOf('/') + 1);
      if (base.startsWith(term)) s = Math.max(s, 7);
    }
    score += s;
  }
  // ?? 0, not a bare lookup. An unlisted kind makes the lookup undefined, which turns the whole
  // score into NaN — and `score > 0` is false for NaN, so the document would not rank last, it
  // would vanish from the palette with nothing logged anywhere. That used to be a closed loop:
  // one file built the index and this one read it. Since 0.4.0 the index is built in Go
  // (internal/build/search_index.go) and ranked here, so a kind added on that side and not on
  // this one is a live possibility rather than a typo.
  score += KIND_BONUS[doc.kind] ?? 0;
  score += Math.max(0, 2 - title.length / 40); // shorter titles edge ahead
  // exact repo-name match wins outright (max title-length bonus is 2, so 5 clears any tie)
  if (doc.kind === 'repo' && title === query.trim().toLowerCase()) score += 5;
  return score;
}

/**
 * Rank docs for a query; ties broken by recency (repos) then title.
 * @param {SearchDoc[]} docs
 * @param {string} query
 * @param {number} [limit]
 * @returns {ScoredDoc[]}
 */
export function search(docs, query, limit = 12) {
  /** @type {ScoredDoc[]} */
  const out = [];
  for (const doc of docs) {
    const score = scoreDoc(doc, query);
    if (score > 0) out.push({ doc, score });
  }
  out.sort(
    (a, b) =>
      b.score - a.score ||
      (b.doc.date ?? '').localeCompare(a.doc.date ?? '') ||
      a.doc.title.localeCompare(b.doc.title),
  );
  return out.slice(0, limit);
}
