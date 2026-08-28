import { describe, expect, it } from 'vitest';
import { DEFAULT_HEAT, aggregateLanguages, commitsSince, heatFor, initials, prettyUrl, relativeTime, yearsSince } from '../../src/lib/format';
import type { Repo } from '../../src/lib/data/schema';

const now = new Date('2026-08-23T12:00:00Z');
const ago = (days: number) => new Date(now.getTime() - days * 86_400_000).toISOString().replace(/\.\d{3}Z$/, 'Z');

describe('heatFor', () => {
  it('buckets by age', () => {
    expect(heatFor(ago(1), now)).toBe('hot');
    expect(heatFor(ago(10), now)).toBe('warm');
    expect(heatFor(ago(100), now)).toBe('neutral');
    expect(heatFor(ago(200), now)).toBe('cool');
    expect(heatFor(ago(400), now)).toBe('cold');
    expect(heatFor(null, now)).toBe('cold');
  });

  it('takes custom thresholds (theme.heat) and defaults to the stock boundaries', () => {
    const t = { hot: 2, warm: 10, neutral: 50, cool: 100 };
    expect(heatFor(ago(1), now, t)).toBe('hot');
    expect(heatFor(ago(3), now, t)).toBe('warm');
    expect(heatFor(ago(20), now, t)).toBe('neutral');
    expect(heatFor(ago(60), now, t)).toBe('cool');
    expect(heatFor(ago(150), now, t)).toBe('cold');
    expect(heatFor(null, now, t)).toBe('cold');
    // the exported default is what every call without the argument uses
    expect(DEFAULT_HEAT).toEqual({ hot: 7, warm: 30, neutral: 180, cool: 365 });
    expect(heatFor(ago(10), now, DEFAULT_HEAT)).toBe(heatFor(ago(10), now));
  });
});

describe('relativeTime', () => {
  it('formats human ages', () => {
    expect(relativeTime(ago(0), now)).toBe('just now');
    expect(relativeTime(new Date(now.getTime() - 2 * 3_600_000), now)).toBe('2 hours ago');
    expect(relativeTime(ago(1), now)).toBe('1 day ago');
    expect(relativeTime(ago(21), now)).toBe('3 weeks ago');
    expect(relativeTime(ago(120), now)).toBe('3 months ago');
    expect(relativeTime(ago(800), now)).toBe('2 years ago');
  });
});

describe('misc', () => {
  it('initials / prettyUrl / yearsSince', () => {
    expect(initials('Kieran Wood')).toBe('KW');
    expect(initials('claude')).toBe('C');
    expect(prettyUrl('https://kieranwood.ca/')).toBe('kieranwood.ca');
    expect(yearsSince(ago(800), now)).toBe(2);
    expect(yearsSince(null, now)).toBe(0);
  });
});

describe('aggregates', () => {
  const repo = (langs: Array<[string, number]>, dates: string[]): Repo =>
    ({
      languages: langs.map(([name, bytes]) => ({ name, bytes, percent: 0, color: null })),
      commits: Object.fromEntries(dates.map((d, i) => [String(i).padStart(40, '0'), { commitDate: d }])),
    }) as unknown as Repo;
  it('aggregateLanguages sums bytes and buckets the tail into Other', () => {
    const out = aggregateLanguages([repo([['TS', 80], ['Go', 10], ['C', 5], ['D', 3], ['E', 1], ['F', 1]], [])], 2);
    expect(out.map((l) => l.name)).toEqual(['TS', 'Go', 'Other']);
    expect(out.map((l) => l.percent)).toEqual([80, 10, 10]);
    expect(aggregateLanguages([], 5)).toEqual([]);
  });
  it('commitsSince counts within window', () => {
    expect(commitsSince([repo([], [ago(1), ago(5), ago(50)])], 7, now)).toBe(2);
  });
});
