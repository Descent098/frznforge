/**
 * Search index for the Ctrl+K command palette (Phase 4). The index is built here at build
 * time and emitted as `/search-index.json`; the ranking that consumes it lives in
 * `web/js/search.js`, because the browser runs it too. `SearchDoc` — the shape the two halves
 * agree on — is declared there and re-exported here.
 */
import { withBase } from './base';
import type { ForgeData } from './data/schema';
import { blobUrl, notesIndexUrl, noteUrl, orgsIndexUrl, orgUrl, repoUrl } from './routes';
import type { SearchDoc } from '../../web/js/search.js';

export type { SearchDoc };

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
    { kind: 'page', title: 'Overview', detail: 'Profile page', url: withBase('/') },
    { kind: 'page', title: 'Repositories', detail: 'All repositories', url: withBase('/repos/') },
  ];
  if (data.notes.length > 0) docs.push({ kind: 'page', title: 'Notes', detail: 'All notes', url: notesIndexUrl() });
  if (data.organizations.length > 0) {
    docs.push({ kind: 'page', title: 'Organizations', detail: 'All organizations', url: orgsIndexUrl() });
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

/**
 * Ranking lives in `web/js/search.js` — the browser scores the index it fetches, the unit
 * tests score fixtures, and one implementation serves both. Re-exported here so importers
 * keep a single entry point for "search".
 */
export { scoreDoc, search } from '../../web/js/search.js';
export type { ScoredDoc } from '../../web/js/search.js';
