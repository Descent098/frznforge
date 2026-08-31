import { describe, expect, it } from 'vitest';
import { DEFAULT_HEAT, aggregateLanguages, commitsSince, heatFor, initials, licenseUrl, prettyUrl, relativeTime, yearsSince } from '../../src/lib/format';
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

describe('licenseUrl', () => {
  it('links the licenses choosealicense.com publishes', () => {
    expect(licenseUrl('MIT')).toBe('https://choosealicense.com/licenses/mit/');
    expect(licenseUrl('Apache-2.0')).toBe('https://choosealicense.com/licenses/apache-2.0/');
    expect(licenseUrl('BSD-3-Clause')).toBe('https://choosealicense.com/licenses/bsd-3-clause/');
    expect(licenseUrl('Unlicense')).toBe('https://choosealicense.com/licenses/unlicense/');
    expect(licenseUrl('0BSD')).toBe('https://choosealicense.com/licenses/0bsd/');
  });

  it('drops the GNU -only / -or-later suffix our detector emits', () => {
    // src/lib/ingest/license.ts returns GPL-3.0-only, not GPL-3.0 — lowercasing alone
    // would link every GNU license to a choosealicense 404.
    expect(licenseUrl('GPL-3.0-only')).toBe('https://choosealicense.com/licenses/gpl-3.0/');
    expect(licenseUrl('GPL-3.0-or-later')).toBe('https://choosealicense.com/licenses/gpl-3.0/');
    expect(licenseUrl('AGPL-3.0-only')).toBe('https://choosealicense.com/licenses/agpl-3.0/');
    expect(licenseUrl('LGPL-2.1-only')).toBe('https://choosealicense.com/licenses/lgpl-2.1/');
    expect(licenseUrl('GPL-2.0-only')).toBe('https://choosealicense.com/licenses/gpl-2.0/');
  });

  it('sends Creative Commons to creativecommons.org', () => {
    expect(licenseUrl('CC0-1.0')).toBe('https://creativecommons.org/publicdomain/zero/1.0/');
    expect(licenseUrl('CC-BY-4.0')).toBe('https://creativecommons.org/licenses/by/4.0/');
    expect(licenseUrl('CC-BY-SA-4.0')).toBe('https://creativecommons.org/licenses/by-sa/4.0/');
    expect(licenseUrl('CC-BY-NC-ND-4.0')).toBe('https://creativecommons.org/licenses/by-nc-nd/4.0/');
    expect(licenseUrl('CC-BY-3.0')).toBe('https://creativecommons.org/licenses/by/3.0/');
  });

  it('returns null for anything it does not recognise, so the caller renders plain text', () => {
    expect(licenseUrl(null)).toBeNull();
    expect(licenseUrl(undefined)).toBeNull();
    expect(licenseUrl('')).toBeNull();
    expect(licenseUrl('Custom')).toBeNull();          // the placeholder the pages render
    expect(licenseUrl('NOASSERTION')).toBeNull();
    expect(licenseUrl('SomeVendor-1.0')).toBeNull();  // a provider id passed through verbatim
  });

  it('every SPDX id the detector can emit resolves to a link', () => {
    // Keeps the map honest as detectSpdx grows: a new detected id with no URL is a miss.
    for (const spdx of [
      '0BSD', 'MIT', 'ISC', 'Apache-2.0', 'MPL-2.0', 'AGPL-3.0-only', 'LGPL-3.0-only',
      'LGPL-2.1-only', 'GPL-3.0-only', 'GPL-2.0-only', 'Unlicense', 'CC0-1.0',
      'BSD-3-Clause', 'BSD-2-Clause',
    ]) {
      expect(licenseUrl(spdx), spdx).toMatch(/^https:\/\//);
    }
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
