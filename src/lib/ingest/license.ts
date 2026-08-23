/**
 * License file discovery + simple SPDX detection by keyword matching.
 */
import type { License } from '../data/schema';
import type { RawTreeEntry } from './tree';

/** Root-level file names that hold a license, case-insensitive. */
export function isLicenseFilename(name: string): boolean {
  const n = name.toLowerCase();
  return (
    n === 'license' ||
    n.startsWith('license.') ||
    n.startsWith('license-') ||
    n === 'licence' ||
    n.startsWith('licence.') ||
    n.startsWith('licence-') ||
    n === 'copying' ||
    n.startsWith('copying.') ||
    n === 'unlicense' ||
    n === 'unlicense.txt'
  );
}

/** Pick the best license file among root entries (plain LICENSE first, then alphabetical). */
export function findLicenseEntry(rootEntries: RawTreeEntry[]): RawTreeEntry | null {
  const candidates = rootEntries.filter((e) => e.type === 'blob' && isLicenseFilename(e.name));
  if (candidates.length === 0) return null;
  const rank = (n: string): number => {
    const l = n.toLowerCase();
    if (l === 'license' || l === 'license.md' || l === 'license.txt') return 0;
    if (l.startsWith('license')) return 1;
    if (l.startsWith('licence')) return 2;
    if (l.startsWith('copying')) return 3;
    return 4;
  };
  candidates.sort((a, b) => rank(a.name) - rank(b.name) || (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
  return candidates[0]!;
}

/** SPDX id from the first ~40 lines of a license text, or null. */
export function detectSpdx(text: string): string | null {
  const head = text.split(/\r?\n/).slice(0, 40).join('\n');
  const t = head.toLowerCase().replace(/\s+/g, ' ');
  if (/\bzero-clause bsd\b|\b0bsd\b|\bbsd zero clause\b/.test(t)) return '0BSD';
  if (/\bmit license\b|\bmit licence\b|permission is hereby granted, free of charge, to any person obtaining a copy/.test(t)) return 'MIT';
  if (/\bisc license\b|\bisc licence\b|permission to use, copy, modify, and\/or distribute this software for any purpose with or without fee/.test(t)) return 'ISC';
  if (/apache license.*version 2\.0|apache-2\.0|apache license 2\.0/.test(t)) return 'Apache-2.0';
  if (/mozilla public license.*(version 2\.0|v\. 2\.0|2\.0)/.test(t)) return 'MPL-2.0';
  if (/gnu affero general public license.*version 3|agpl-3\.0|agplv3/.test(t)) return 'AGPL-3.0-only';
  if (/gnu lesser general public license.*version 3|lgpl-3\.0|lgplv3/.test(t)) return 'LGPL-3.0-only';
  if (/gnu lesser general public license.*version 2\.1|lgpl-2\.1|lgplv2\.1/.test(t)) return 'LGPL-2.1-only';
  if (/gnu general public license.*version 3|gpl-3\.0|gplv3/.test(t)) return 'GPL-3.0-only';
  if (/gnu general public license.*version 2|gpl-2\.0|gplv2/.test(t)) return 'GPL-2.0-only';
  if (/\bthe unlicense\b|this is free and unencumbered software released into the public domain/.test(t)) return 'Unlicense';
  if (/cc0 1\.0|cc0-1\.0|creative commons zero|creative commons legal code cc0/.test(t)) return 'CC0-1.0';
  if (/redistribution and use in source and binary forms/.test(t)) {
    if (/neither the name of .* nor the names of its contributors|names of its contributors may be used to endorse/.test(t)) return 'BSD-3-Clause';
    if (/bsd 3-clause|bsd-3-clause|3-clause bsd/.test(t)) return 'BSD-3-Clause';
    return 'BSD-2-Clause';
  }
  if (/bsd 3-clause|bsd-3-clause|3-clause bsd/.test(t)) return 'BSD-3-Clause';
  if (/bsd 2-clause|bsd-2-clause|2-clause bsd|simplified bsd/.test(t)) return 'BSD-2-Clause';
  return null;
}

/**
 * Build the License record: config override wins for the SPDX id; the detected file (if
 * any) is still recorded.
 */
export function resolveLicense(
  detected: { file: string; spdx: string | null } | null,
  override: string | null,
): License | null {
  if (override) return { spdx: override, file: detected?.file ?? null, source: 'config' };
  if (!detected) return null;
  return { spdx: detected.spdx, file: detected.file, source: 'file' };
}
