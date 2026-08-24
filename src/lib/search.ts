/**
 * Search index + scoring for the Ctrl+K command palette (Phase 4). Pure + browser-safe:
 * the index is built at build time (endpoint /search-index.json) and scored in the browser.
 */
import type { ForgeData } from './data/schema';
import { NOTES_INDEX_URL, ORGS_INDEX_URL, blobUrl, noteUrl, orgUrl, repoUrl } from './routes';

export interface SearchDoc {
  /** 'repo' | 'file' | 'note' | 'org' | 'page' | 'action' */
  kind: 'repo' | 'file' | 'note' | 'org' | 'page' | 'action';
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

/**
 * Build the index: static pages, repos, default-branch file paths, notes, organizations.
 * Actions are added client-side (they depend on the page the palette was opened from).
 *
 * Doc order is a pure function of the artifact — pages, then each repo followed by its files
 * in artifact order, then notes, then organizations — so `/search-index.json` is byte-stable
 * across builds like the artifact itself.
 *
 * The Notes / Organizations index pages are only listed when the artifact has any, mirroring
 * the sidebar: the pages are always built, but offering a visitor an empty section is noise.
 */
export function buildSearchIndex(data: ForgeData): SearchIndex {
  const docs: SearchDoc[] = [
    { kind: 'page', title: 'Overview', detail: 'Profile page', url: '/' },
    { kind: 'page', title: 'Repositories', detail: 'All repositories', url: '/repos/' },
  ];
  if (data.notes.length > 0) docs.push({ kind: 'page', title: 'Notes', detail: 'All notes', url: NOTES_INDEX_URL });
  if (data.organizations.length > 0) {
    docs.push({ kind: 'page', title: 'Organizations', detail: 'All organizations', url: ORGS_INDEX_URL });
  }
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
  // Notes: a file name inside a note must find the note, so every file path is a keyword.
  for (const note of data.notes) {
    docs.push({
      kind: 'note',
      title: note.title,
      detail: note.description ?? noteFallbackDetail(note.files.length, note.files[0]?.name),
      url: noteUrl(note.slug),
      keywords: [note.slug, ...note.tags, ...note.files.map((f) => f.path)].join(' '),
      date: note.date,
    });
  }
  // Organizations: searchable by their own name/description and by any member repo slug.
  for (const org of data.organizations) {
    docs.push({
      kind: 'org',
      title: org.name,
      detail: org.description ?? orgFallbackDetail(org.repos.length),
      url: orgUrl(org.slug),
      keywords: [org.slug, ...org.repos].join(' '),
    });
  }
  return { version: 1, docs };
}

/** Secondary line for a note with no description: its single file's name, or a file count. */
function noteFallbackDetail(fileCount: number, firstName: string | undefined): string {
  if (fileCount === 1 && firstName) return firstName;
  return `${fileCount} file${fileCount === 1 ? '' : 's'}`;
}

/** Secondary line for an organization with no description: how many repos it holds. */
function orgFallbackDetail(repoCount: number): string {
  return `${repoCount} ${repoCount === 1 ? 'repository' : 'repositories'}`;
}

/* ---- scoring -------------------------------------------------------------- */

export interface ScoredDoc { doc: SearchDoc; score: number }

/**
 * Per-kind ranking bonus, applied once. Repos are the site's primary objects and outrank
 * everything; notes and organizations sit with pages, above raw file paths.
 */
const KIND_BONUS: Record<SearchDoc['kind'], number> = {
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
  score += KIND_BONUS[doc.kind];
  score += Math.max(0, 2 - title.length / 40); // shorter titles edge ahead
  // exact repo-name match wins outright (max title-length bonus is 2, so 5 clears any tie)
  if (doc.kind === 'repo' && title === query.trim().toLowerCase()) score += 5;
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
