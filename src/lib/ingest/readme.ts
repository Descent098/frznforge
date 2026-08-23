/**
 * README discovery in the root of the default-branch tree.
 */
import type { RawTreeEntry } from './tree';

/** Rank for README candidates: lower is better; null ⇒ not a readme. */
export function readmeRank(name: string): number | null {
  const l = name.toLowerCase();
  if (l === 'readme.md') return 0;
  if (l === 'readme.markdown' || l === 'readme.mdown') return 1;
  if (l === 'readme') return 2;
  if (l === 'readme.txt') return 3;
  if (l === 'readme.rst' || l === 'readme.adoc' || l === 'readme.org') return 4;
  if (l.startsWith('readme.')) return 5;
  return null;
}

/** Pick the README entry among root entries (prefers README.md). */
export function findReadmeEntry(rootEntries: RawTreeEntry[]): RawTreeEntry | null {
  const candidates = rootEntries
    .filter((e) => e.type === 'blob' && readmeRank(e.name) !== null)
    .sort((a, b) => readmeRank(a.name)! - readmeRank(b.name)! || (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
  return candidates[0] ?? null;
}
