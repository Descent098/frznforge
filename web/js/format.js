/**
 * Pure display helpers, shared by the server render and the browser.
 *
 * This file is **the** implementation — `src/lib/format.ts` re-exports it and adds the
 * helpers that take artifact types (`Repo`, `Commit`), which the browser never needs. It is
 * plain JavaScript with JSDoc types so exactly one copy exists: Vite bundles it for the
 * server render, and the browser loads the same bytes over `/js/format.js` with no build
 * step of any kind.
 *
 * Rules for everything in this folder:
 *  - no imports of anything outside `web/`, no node built-ins, no bare specifiers;
 *  - relative imports carry their `.js` extension, because a browser resolves them literally;
 *  - no syntax a current browser cannot run directly.
 */

/* ---- temperature --------------------------------------------------------- */

/** @typedef {'hot' | 'warm' | 'neutral' | 'cool' | 'cold'} Heat */

/**
 * Day boundaries for the heat buckets, ascending: age < `hot` days ⇒ 'hot', < `warm` ⇒
 * 'warm', < `neutral` ⇒ 'neutral', < `cool` ⇒ 'cool', else 'cold'. Configurable via
 * `theme.heat` in the site config; the values arrive as a plain argument — from
 * `getConfig()` in .astro frontmatter, from the JSON payload in the browser — never from a
 * config import, so this stays loadable anywhere.
 *
 * @typedef {{ hot: number, warm: number, neutral: number, cool: number }} HeatThresholds
 */

const DAY = 86_400_000;

/**
 * The stock boundaries, used when no `theme.heat` is configured.
 * @type {HeatThresholds}
 */
export const DEFAULT_HEAT = { hot: 7, warm: 30, neutral: 180, cool: 365 };

/**
 * Map an age to a heat bucket. Default boundaries: < 7d hot, < 30d warm, < 180d neutral,
 * < 365d cool, else cold. `now` is injectable for tests and deterministic builds.
 *
 * @param {string | Date | null | undefined} date
 * @param {Date} [now]
 * @param {HeatThresholds} [thresholds]
 * @returns {Heat}
 */
export function heatFor(date, now = new Date(), thresholds = DEFAULT_HEAT) {
  if (!date) return 'cold';
  const d = typeof date === 'string' ? new Date(date) : date;
  const age = now.getTime() - d.getTime();
  if (age < thresholds.hot * DAY) return 'hot';
  if (age < thresholds.warm * DAY) return 'warm';
  if (age < thresholds.neutral * DAY) return 'neutral';
  if (age < thresholds.cool * DAY) return 'cool';
  return 'cold';
}

/**
 * CSS utility class for a heat bucket (see global.css `.t-*`).
 * @param {Heat} heat
 * @returns {string}
 */
export function heatClass(heat) {
  return `t-${heat}`;
}

/**
 * "2 hours ago", "3 weeks ago", "2 years ago".
 * @param {string | Date | null | undefined} date
 * @param {Date} [now]
 * @returns {string}
 */
export function relativeTime(date, now = new Date()) {
  if (!date) return '—';
  const d = typeof date === 'string' ? new Date(date) : date;
  const s = Math.max(0, Math.round((now.getTime() - d.getTime()) / 1000));
  /** @type {Array<[number, string]>} */
  const units = [
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

/**
 * Compact age: "2h", "3w", "4mo", "1y".
 * @param {string | Date | null | undefined} date
 * @param {Date} [now]
 * @returns {string}
 */
export function relativeTimeShort(date, now = new Date()) {
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

/**
 * "Sep 2020"
 * @param {string | null | undefined} date
 * @returns {string}
 */
export function monthYear(date) {
  if (!date) return '—';
  return new Date(date).toLocaleDateString('en', { month: 'short', year: 'numeric', timeZone: 'UTC' });
}

/**
 * "2026-08-23"
 * @param {string | null | undefined} date
 * @returns {string}
 */
export function isoDay(date) {
  return date ? date.slice(0, 10) : '—';
}

/**
 * @param {number} n
 * @returns {string}
 */
export function formatInt(n) {
  return n.toLocaleString('en');
}

/**
 * A byte count for humans: `947 B`, `12.4 KB`, `3.1 MB` (binary units, one decimal).
 *
 * Deliberately not `toLocaleString`-based: this string is baked into static HTML, so it must
 * not vary with the build machine's locale the way `formatInt` may.
 *
 * @param {number} n Size in bytes.
 * @returns {string}
 */
export function formatBytes(n) {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

/* ---- misc ---------------------------------------------------------------- */

/**
 * Initials for the avatar block, e.g. "Kieran Wood" → "KW".
 * @param {string} name
 * @returns {string}
 */
export function initials(name) {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  const first = parts[0] ? parts[0][0] : '';
  const last = parts.length > 1 ? parts[parts.length - 1][0] ?? '' : '';
  const s = first + last;
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
 *
 * @param {string | null | undefined} spdx
 * @returns {string | null}
 */
export function licenseUrl(spdx) {
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

/**
 * Display a URL without protocol / trailing slash: "https://kieranwood.ca/" → "kieranwood.ca".
 * @param {string} url
 * @returns {string}
 */
export function prettyUrl(url) {
  return url.replace(/^[a-z]+:\/\//i, '').replace(/\/$/, '');
}
