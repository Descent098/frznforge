/**
 * Search index + scoring for the Ctrl+K command palette (Phase 4). Pure + browser-safe:
 * the index is built at build time (endpoint /search-index.json) and scored in the browser.
 */
import type { ForgeData } from './data/schema';
import { blobUrl, repoUrl } from './routes';

export interface SearchDoc {
  /** 'repo' | 'file' | 'page' | 'action' */
  kind: 'repo' | 'file' | 'page' | 'action';
  /** Primary display text (repo name, file path, page title). */
  title: string;
  /** Secondary text (description, repo slug, hint). */
  detail: string;
  url: string;
  /** Extra matchable text (tags, languages). */
  keywords?: string;
  /** ISO date for recency tie-breaks (repos). */
  date?: string | null;
}

export interface SearchIndex {
  version: 1;
  docs: SearchDoc[];
}

/** Build the index: repos, default-branch file paths, static pages. Actions are added client-side. */
export function buildSearchIndex(data: ForgeData): SearchIndex {
  const docs: SearchDoc[] = [
    { kind: 'page', title: 'Overview', detail: 'Profile page', url: '/' },
    { kind: 'page', title: 'Repositories', detail: 'All repositories', url: '/repos/' },
  ];
  for (const repo of data.repos) {
    docs.push({
      kind: 'repo',
      title: repo.name,
      detail: repo.description ?? (repo.empty ? 'Empty repository' : ''),
      url: repoUrl(repo.slug),
      keywords: [repo.slug, ...repo.tags, ...repo.languages.map((l) => l.name)].join(' '),
      date: repo.updatedAt,
    });
    if (repo.defaultBranch) {
      for (const e of repo.tree) {
        if (e.type !== 'blob' && e.type !== 'symlink') continue;
        docs.push({ kind: 'file', title: e.path, detail: repo.slug, url: blobUrl(repo.slug, repo.defaultBranch, e.path) });
      }
    }
  }
  return { version: 1, docs };
}

/* ---- scoring -------------------------------------------------------------- */

export interface ScoredDoc { doc: SearchDoc; score: number }

/**
 * Score `query` against a doc. 0 = no match. All whitespace-separated terms must match
 * title/detail/keywords (case-insensitive). Bonuses: prefix > word/segment boundary >
 * substring; shorter titles rank higher; repos outrank files at equal score.
 */
export function scoreDoc(doc: SearchDoc, query: string): number {
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
    else if (idx > 0 && /[\s\/\-_.]/.test(title[idx - 1]!)) s = 5; // boundary in title (path segment, word)
    else if (idx > 0) s = 3; // substring in title
    // basename bonus for files: query matches the file name itself
    if (doc.kind === 'file') {
      const base = title.slice(title.lastIndexOf('/') + 1);
      if (base.startsWith(term)) s = Math.max(s, 7);
    }
    score += s;
  }
  score += doc.kind === 'repo' ? 2 : doc.kind === 'page' || doc.kind === 'action' ? 1 : 0;
  score += Math.max(0, 2 - title.length / 40); // shorter titles edge ahead
  return score;
}

/** Rank docs for a query; ties broken by recency (repos) then title. */
export function search(docs: SearchDoc[], query: string, limit = 12): ScoredDoc[] {
  const out: ScoredDoc[] = [];
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
