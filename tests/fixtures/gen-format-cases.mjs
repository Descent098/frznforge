/**
 * Generate tests/fixtures/format-cases.json — the shared golden that pins the two
 * implementations of the display helpers to each other.
 *
 * `web/js/format.js` runs in the browser (the listing rebuilds cards there);
 * `internal/render/format.go` runs at build time. They have to produce identical strings or a
 * card changes the instant it is re-rendered. This file is generated FROM the JavaScript, so
 * the JavaScript is the reference and Go is checked against it.
 *
 * Run:  node tests/fixtures/gen-format-cases.mjs
 */
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import {
  DEFAULT_HEAT,
  formatBytes,
  formatInt,
  heatFor,
  initials,
  isoDay,
  licenseUrl,
  monthYear,
  prettyUrl,
  relativeTime,
  relativeTimeShort,
} from '../../web/js/format.js';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');

/** A fixed reference instant, so every case is reproducible. */
const NOW = '2026-09-06T12:00:00Z';
const now = new Date(NOW);

/** Offsets from NOW, in seconds, chosen to land on both sides of every unit boundary. */
const OFFSETS = [
  0, 1, 30, 59, 60, 61, 119, 3599, 3600, 3601, 7200, 86399, 86400, 86401,
  172800, 604799, 604800, 604801, 1209600, 2629745, 2629746, 5259492,
  31556952, 31556953, 63113904, 315569520,
  // a couple of exact heat-threshold ages (7d, 30d, 180d, 365d)
  604800, 2592000, 15552000, 31536000,
];

const dates = OFFSETS.map((s) => new Date(now.getTime() - s * 1000).toISOString().replace(/\.\d{3}Z$/, 'Z'));

const cases = {
  now: NOW,
  heat: DEFAULT_HEAT,
  relativeTime: dates.map((d) => ({ in: d, out: relativeTime(d, now) })),
  relativeTimeShort: dates.map((d) => ({ in: d, out: relativeTimeShort(d, now) })),
  heatFor: dates.map((d) => ({ in: d, out: heatFor(d, now, DEFAULT_HEAT) })),
  formatInt: [0, 1, 12, 999, 1000, 1001, 12345, 999999, 1000000, 1234567890].map((n) => ({
    in: n,
    out: formatInt(n),
  })),
  formatBytes: [0, 1, 512, 1023, 1024, 1536, 10240, 1048575, 1048576, 5333293, 65622500].map((n) => ({
    in: n,
    out: formatBytes(n),
  })),
  initials: [
    'Kieran Wood',
    'kieran',
    'Kieran  Middle   Wood',
    '  spaced  ',
    'X',
    'ünicode nÄme',
    '日本 語',
  ].map((s) => ({ in: s, out: initials(s) })),
  licenseUrl: [
    'MIT',
    'mit',
    'Apache-2.0',
    'GPL-3.0-only',
    'GPL-3.0-or-later',
    'AGPL-3.0-only',
    'LGPL-2.1-only',
    'CC0-1.0',
    'CC-BY-4.0',
    'CC-BY-NC-SA-4.0',
    'BSD-3-Clause',
    'Unlicense',
    'Custom',
    'NOT-A-LICENSE',
    '',
  ].map((s) => ({ in: s, out: licenseUrl(s) })),
  prettyUrl: [
    'https://kieranwood.ca/',
    'http://example.com',
    'https://example.com/path/',
    'ftp://files.example.com/',
    'example.com',
  ].map((s) => ({ in: s, out: prettyUrl(s) })),
  monthYear: ['2020-09-01T00:00:00Z', '2026-01-31T23:59:59Z', '2026-12-01T00:00:00Z', ''].map((s) => ({
    in: s,
    out: monthYear(s),
  })),
  isoDay: ['2026-08-23T14:02:11Z', '2026-08-23', ''].map((s) => ({ in: s, out: isoDay(s) })),
};

const out = path.join(ROOT, 'tests', 'fixtures', 'format-cases.json');
fs.writeFileSync(out, JSON.stringify(cases, null, 2) + '\n');
const total = Object.values(cases).filter(Array.isArray).reduce((a, b) => a + b.length, 0);
console.log(`wrote ${path.relative(ROOT, out)} — ${total} cases`);
