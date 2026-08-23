/**
 * Recent-activity event log (Phase 4). Pure + browser-safe.
 * Derives events from the artifact: pushes (commits grouped per repo/branch/UTC-day) and
 * tags. Newest first, capped by the caller.
 */
import type { Repo } from './data/schema';

export type ActivityEvent =
  | { type: 'push'; repo: string; branch: string; count: number; date: string; subject: string }
  | { type: 'tag'; repo: string; tag: string; date: string; annotated: boolean };

/** Build the merged event list, newest first (at most `limit`). */
export function buildActivity(repos: Repo[], limit = 10): ActivityEvent[] {
  const events: ActivityEvent[] = [];
  for (const repo of repos) {
    for (const branch of repo.branches) {
      // group this branch's commits by UTC day → one "push" event per day
      const byDay = new Map<string, { count: number; date: string; subject: string }>();
      for (const sha of branch.commits) {
        const c = repo.commits[sha];
        if (!c) continue;
        const day = c.commitDate.slice(0, 10);
        const cur = byDay.get(day);
        if (cur) {
          cur.count++;
          if (c.commitDate > cur.date) { cur.date = c.commitDate; cur.subject = c.subject; }
        } else {
          byDay.set(day, { count: 1, date: c.commitDate, subject: c.subject });
        }
      }
      for (const g of byDay.values()) {
        events.push({ type: 'push', repo: repo.slug, branch: branch.name, count: g.count, date: g.date, subject: g.subject });
      }
    }
    for (const t of repo.gitTags) {
      events.push({ type: 'tag', repo: repo.slug, tag: t.name, date: t.date, annotated: t.annotated });
    }
  }
  events.sort((a, b) => b.date.localeCompare(a.date));
  // dedupe pushes that are shadowed by another branch containing the same day's commits
  // (e.g. feature merged into main shows twice) — keep the first (newest branch wins).
  const seen = new Set<string>();
  const out: ActivityEvent[] = [];
  for (const e of events) {
    const key = e.type === 'push' ? `p:${e.repo}:${e.date.slice(0, 10)}:${e.count}:${e.subject}` : `t:${e.repo}:${e.tag}`;
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(e);
    if (out.length >= limit) break;
  }
  return out;
}
