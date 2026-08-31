/**
 * Pure, browser-safe helpers: temperature (fire = recent, ice = old), relative time, number
 * formatting, aggregates over artifact data. No node imports — Svelte islands import this.
 */
import type { Commit, LanguageStat, Repo } from './data/schema';

/* ---- temperature --------------------------------------------------------- */

export type Heat = 'hot' | 'warm' | 'neutral' | 'cool' | 'cold';

const DAY = 86_400_000;

/**
 * Day boundaries for the heat buckets, ascending: age < `hot` days ⇒ 'hot', < `warm` ⇒
 * 'warm', < `neutral` ⇒ 'neutral', < `cool` ⇒ 'cool', else 'cold'. Configurable via
 * `theme.heat` in frznforge.config.ts; this module must stay browser-safe, so the values
 * arrive as a plain argument (from `getConfig()` in .astro frontmatter, as an island prop
 * for Svelte) — never from a config import.
 */
export interface HeatThresholds {
  hot: number;
  warm: number;
  neutral: number;
  cool: number;
}

/** The stock boundaries, used when no `theme.heat` is configured. */
export const DEFAULT_HEAT: HeatThresholds = { hot: 7, warm: 30, neutral: 180, cool: 365 };

/**
 * Map an age to a heat bucket. Default boundaries: < 7d hot, < 30d warm, < 180d neutral,
 * < 365d cool, else cold. `now` is injectable for tests and deterministic builds.
 */
export function heatFor(
  date: string | Date | null | undefined,
  now: Date = new Date(),
  thresholds: HeatThresholds = DEFAULT_HEAT,
): Heat {
  if (!date) return 'cold';
  const d = typeof date === 'string' ? new Date(date) : date;
  const age = now.getTime() - d.getTime();
  if (age < thresholds.hot * DAY) return 'hot';
  if (age < thresholds.warm * DAY) return 'warm';
  if (age < thresholds.neutral * DAY) return 'neutral';
  if (age < thresholds.cool * DAY) return 'cool';
  return 'cold';
}

/** CSS utility class for a heat bucket (see global.css `.t-*`). */
export function heatClass(heat: Heat): string {
  return `t-${heat}`;
}

/** "2 hours ago", "3 weeks ago", "2 years ago". */
export function relativeTime(date: string | Date | null | undefined, now: Date = new Date()): string {
  if (!date) return '—';
  const d = typeof date === 'string' ? new Date(date) : date;
  const s = Math.max(0, Math.round((now.getTime() - d.getTime()) / 1000));
  const units: Array<[number, string]> = [
    [60, 'second'],
    [60, 'minute'],
    [24, 'hour'],
    [7, 'day'],
    [4.345, 'week'],
    [12, 'month'],
    [Infinity, 'year'],
  ];
  let value = s;
  let name = 'second';
  for (const [div, unit] of units) {
    name = unit;
    if (value < div) break;
    value = value / div;
  }
  const n = Math.floor(value);
  if (name === 'second') return 'just now';
  return `${n} ${name}${n === 1 ? '' : 's'} ago`;
}

/** Compact age: "2h", "3w", "4mo", "1y". */
export function relativeTimeShort(date: string | Date | null | undefined, now: Date = new Date()): string {
  if (!date) return '—';
  const d = typeof date === 'string' ? new Date(date) : date;
  const s = Math.max(0, Math.round((now.getTime() - d.getTime()) / 1000));
  if (s < 60) return 'now';
  const m = s / 60;
  if (m < 60) return `${Math.floor(m)}m`;
  const h = m / 60;
  if (h < 24) return `${Math.floor(h)}h`;
  const days = h / 24;
  if (days < 7) return `${Math.floor(days)}d`;
  if (days < 30) return `${Math.floor(days / 7)}w`;
  if (days < 365) return `${Math.floor(days / 30.44)}mo`;
  return `${Math.floor(days / 365.25)}y`;
}

/** "Sep 2020" */
export function monthYear(date: string | null | undefined): string {
  if (!date) return '—';
  return new Date(date).toLocaleDateString('en', { month: 'short', year: 'numeric', timeZone: 'UTC' });
}

/** "2026-08-23" */
export function isoDay(date: string | null | undefined): string {
  return date ? date.slice(0, 10) : '—';
}

export function formatInt(n: number): string {
  return n.toLocaleString('en');
}

/**
 * A byte count for humans: `947 B`, `12.4 KB`, `3.1 MB` (binary units, one decimal).
 *
 * Deliberately not `toLocaleString`-based: this string is baked into static HTML, so it must
 * not vary with the build machine's locale the way `formatInt` may.
 *
 * @param n Size in bytes.
 */
export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

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

/* ---- misc ---------------------------------------------------------------- */

/** Initials for the avatar block, e.g. "Kieran Wood" → "KW". */
export function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  const s = (parts[0]?.[0] ?? '') + (parts.length > 1 ? parts[parts.length - 1]![0] ?? '' : '');
  return (s || name.slice(0, 2)).toUpperCase();
}

/**
 * SPDX ids that choosealicense.com publishes a page for, keyed by their URL slug.
 *
 * The slug is the SPDX id lowercased *after* dropping the GNU disambiguation suffix —
 * our own detector emits `GPL-3.0-only` / `AGPL-3.0-only` / `LGPL-2.1-only`
 * (see `src/lib/ingest/license.ts`), while choosealicense serves `/licenses/gpl-3.0/`.
 * Lowercasing alone would link every GNU license to a 404.
 */
const CHOOSEALICENSE_SLUGS = new Set([
  '0bsd', 'afl-3.0', 'agpl-3.0', 'apache-2.0', 'artistic-2.0', 'bsd-2-clause',
  'bsd-3-clause', 'bsd-3-clause-clear', 'bsd-4-clause', 'bsl-1.0', 'cecill-2.1',
  'ecl-2.0', 'epl-1.0', 'epl-2.0', 'eupl-1.1', 'eupl-1.2', 'gfdl-1.3', 'gpl-2.0',
  'gpl-3.0', 'isc', 'lgpl-2.1', 'lgpl-3.0', 'lppl-1.3c', 'mit', 'mit-0', 'mpl-2.0',
  'ms-pl', 'ms-rl', 'mulanpsl-2.0', 'ncsa', 'odbl-1.0', 'ofl-1.1', 'osl-3.0',
  'postgresql', 'unlicense', 'upl-1.0', 'vim', 'wtfpl', 'zlib',
]);

/**
 * The canonical human-readable page for a license, or null when we don't recognise it.
 *
 * Creative Commons licenses go to creativecommons.org (the canonical deed); everything
 * else that has a page goes to choosealicense.com. An unknown id — including a
 * provider's passed-through oddity and the `Custom` placeholder we render for a license
 * file with no detected id — returns null, and the caller renders plain text.
 */
export function licenseUrl(spdx: string | null | undefined): string | null {
  if (!spdx) return null;
  // GPL-3.0-only / GPL-3.0-or-later both describe the same license document.
  const key = spdx.trim().toLowerCase().replace(/-(only|or-later)$/, '');
  if (!key) return null;

  if (key === 'cc0-1.0') return 'https://creativecommons.org/publicdomain/zero/1.0/';
  const cc = /^cc-(by(?:-nc)?(?:-sa|-nd)?)-(\d+\.\d+)$/.exec(key);
  if (cc) return `https://creativecommons.org/licenses/${cc[1]}/${cc[2]}/`;

  if (CHOOSEALICENSE_SLUGS.has(key)) return `https://choosealicense.com/licenses/${key}/`;
  return null;
}

/** Display a URL without protocol / trailing slash: "https://kieranwood.ca/" → "kieranwood.ca". */
export function prettyUrl(url: string): string {
  return url.replace(/^[a-z]+:\/\//i, '').replace(/\/$/, '');
}

/** Top N languages of a repo for a card footer. */
export function topLanguages(repo: Repo, n = 3): LanguageStat[] {
  return repo.languages.slice(0, n);
}
