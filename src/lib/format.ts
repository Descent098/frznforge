/**
 * Pure, browser-safe helpers: temperature (fire = recent, ice = old), relative time, number
 * formatting, aggregates over artifact data.
 *
 * The display primitives — heat, relative time, byte/int formatting, initials, license URLs —
 * live in `web/js/format.js`, because the browser needs them too and one implementation
 * cannot drift from itself. This module re-exports them and adds the helpers that take
 * artifact types (`Repo`, `Commit`), which only the build ever calls. Import from here
 * anywhere in `src/`; the split is an implementation detail.
 */
import type { Commit, LanguageStat, Repo } from './data/schema';

export * from '../../web/js/format.js';

const DAY = 86_400_000;

/**
 * Look a commit up for DISPLAY: the kept history first, then the display-support map
 * (schema v6 `extraCommits` — per-path last commits and tag targets that
 * `ingest.maxCommits` / `ingest.maxCommitAgeDays` dropped from `commits`). Aggregates must
 * NOT use this — they iterate `commits` alone so the narrowing knobs keep their meaning.
 */
export function commitFor(repo: Repo, sha: string | null | undefined): Commit | null {
  if (!sha) return null;
  return repo.commits[sha] ?? repo.extraCommits[sha] ?? null;
}

/* ---- aggregates ---------------------------------------------------------- */

/** Aggregate language stats across repos (bytes summed; percents recomputed). */
export function aggregateLanguages(repos: Repo[], top = 5): LanguageStat[] {
  const bytes = new Map<string, { bytes: number; color: string | null }>();
  for (const r of repos) {
    for (const l of r.languages) {
      const cur = bytes.get(l.name) ?? { bytes: 0, color: l.color };
      cur.bytes += l.bytes;
      bytes.set(l.name, cur);
    }
  }
  const total = [...bytes.values()].reduce((a, b) => a + b.bytes, 0);
  if (total === 0) return [];
  const sorted = [...bytes.entries()]
    .map(([name, v]) => ({ name, bytes: v.bytes, color: v.color, percent: 0 }))
    .sort((a, b) => b.bytes - a.bytes || a.name.localeCompare(b.name));
  const head = sorted.slice(0, top);
  const rest = sorted.slice(top);
  if (rest.length) {
    head.push({ name: 'Other', bytes: rest.reduce((a, b) => a + b.bytes, 0), color: null, percent: 0 });
  }
  for (const l of head) l.percent = Math.round((l.bytes / total) * 1000) / 10;
  return head;
}

/** Commits (across all repos, deduped by sha) whose commit date is within the last `days`. */
export function commitsSince(repos: Repo[], days: number, now: Date = new Date()): number {
  const cutoff = now.getTime() - days * DAY;
  let n = 0;
  for (const r of repos) {
    for (const c of Object.values(r.commits)) {
      if (new Date(c.commitDate).getTime() >= cutoff) n++;
    }
  }
  return n;
}

/** Earliest createdAt across repos, or null. */
export function earliestCommit(repos: Repo[]): string | null {
  let min: string | null = null;
  for (const r of repos) if (r.createdAt && (!min || r.createdAt < min)) min = r.createdAt;
  return min;
}

/** Whole years between `from` and now (floored, min 0). */
export function yearsSince(from: string | null, now: Date = new Date()): number {
  if (!from) return 0;
  return Math.max(0, Math.floor((now.getTime() - new Date(from).getTime()) / (365.25 * DAY)));
}

/** Top N languages of a repo for a card footer. */
export function topLanguages(repo: Repo, n = 3): LanguageStat[] {
  return repo.languages.slice(0, n);
}
