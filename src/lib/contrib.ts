/**
 * Contribution graph data (Phase 4). Pure + browser-safe.
 * Buckets the owner's commits by UTC day over the last 52 weeks and produces the cell grid
 * the profile renders: intensity from commit count (quartiles), hue from recency (fire→ice),
 * exactly like the Hearth design exploration.
 */
import type { Repo } from './data/schema';
import { DEFAULT_HEAT, type Heat, type HeatThresholds, heatFor } from './format';

const DAY = 86_400_000;

export interface ContribCell {
  /** ISO day, e.g. "2026-08-23". */
  day: string;
  count: number;
  /** 0 = none … 4 = highest quartile. */
  level: 0 | 1 | 2 | 3 | 4;
  heat: Heat;
}

export interface ContribGraph {
  /** 7×N column-major weeks: weeks[w][d], d 0 = Sunday. Cells before the window are null. */
  weeks: Array<Array<ContribCell | null>>;
  /** Month labels: index of the week where each month starts. */
  months: Array<{ week: number; label: string }>;
  total: number;
  /** Longest run of consecutive days with commits. */
  longestStreak: number;
  busiestDay: ContribCell | null;
}

/**
 * Count commits per UTC day. `identities`: author emails (lowercase) to count; empty = all.
 * Commits are deduped by sha across repos.
 */
export function commitsByDay(repos: Repo[], identities: string[] = []): Map<string, number> {
  const ids = new Set(identities.map((e) => e.toLowerCase()));
  const seen = new Set<string>();
  const days = new Map<string, number>();
  for (const repo of repos) {
    for (const [sha, c] of Object.entries(repo.commits)) {
      if (seen.has(sha)) continue;
      seen.add(sha);
      if (ids.size && !ids.has(c.author.email.toLowerCase())) continue;
      const day = c.authorDate.slice(0, 10);
      days.set(day, (days.get(day) ?? 0) + 1);
    }
  }
  return days;
}

const isoDay = (t: number) => new Date(t).toISOString().slice(0, 10);

/** Build the 52-week graph ending on `now`'s UTC day. */
export function buildContribGraph(
  repos: Repo[],
  identities: string[] = [],
  now: Date = new Date(),
  heat: HeatThresholds = DEFAULT_HEAT,
): ContribGraph {
  const counts = commitsByDay(repos, identities);
  const today = Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate());
  const end = today + (6 - new Date(today).getUTCDay()) * DAY; // end of current week (Saturday)
  const start = end - (52 * 7 - 1) * DAY;

  // quartile thresholds over non-zero days inside the window
  const window: number[] = [];
  for (let t = start; t <= today; t += DAY) {
    const c = counts.get(isoDay(t)) ?? 0;
    if (c > 0) window.push(c);
  }
  window.sort((a, b) => a - b);
  const q = (p: number) => (window.length ? window[Math.min(window.length - 1, Math.floor(p * window.length))]! : 1);
  const t1 = q(0.25), t2 = q(0.5), t3 = q(0.75);
  const levelOf = (c: number): 0 | 1 | 2 | 3 | 4 => (c === 0 ? 0 : c <= t1 ? 1 : c <= t2 ? 2 : c <= t3 ? 3 : 4);

  const weeks: Array<Array<ContribCell | null>> = [];
  const months: Array<{ week: number; label: string }> = [];
  let total = 0;
  let streak = 0, longestStreak = 0;
  let busiest: ContribCell | null = null;
  let lastMonth = '';

  for (let w = 0; w < 52; w++) {
    const col: Array<ContribCell | null> = [];
    for (let d = 0; d < 7; d++) {
      const t = start + (w * 7 + d) * DAY;
      if (t > today) { col.push(null); continue; }
      const day = isoDay(t);
      const count = counts.get(day) ?? 0;
      const cell: ContribCell = { day, count, level: levelOf(count), heat: heatFor(new Date(t), now, heat) };
      col.push(cell);
      total += count;
      if (count > 0) { streak++; longestStreak = Math.max(longestStreak, streak); } else streak = 0;
      if (count > 0 && (!busiest || count > busiest.count)) busiest = cell;
    }
    weeks.push(col);
    // month label when this week's first day enters a new month
    const wt = start + w * 7 * DAY;
    const month = isoDay(wt).slice(0, 7);
    if (month !== lastMonth) {
      lastMonth = month;
      months.push({ week: w, label: new Date(wt).toLocaleDateString('en', { month: 'short', timeZone: 'UTC' }) });
    }
  }
  // drop a leading month label crowded by the next one
  if (months.length > 1 && months[1]!.week - months[0]!.week < 2) months.shift();
  return { weeks, months, total, longestStreak, busiestDay: busiest };
}
