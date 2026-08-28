/**
 * `theme.heat` threading guard (0.2.0). The configured recency boundaries reach rendered
 * output only through explicit threading — `getConfig().theme.heat` in .astro frontmatter,
 * a `heatDays` prop into the Svelte islands — and every link in that chain has a silent
 * `DEFAULT_HEAT` fallback, so dropping one is invisible to the e2e suite (its fixture site
 * builds with the checked-in config, which uses default boundaries). Like
 * `contrast.test.ts`, this test reads the SOURCE and pins the threading itself: every
 * rendered `heatFor(`/`buildContribGraph(` call site must pass a thresholds value, and
 * every `<RepoCard`/`<RepoListing` usage must pass `heatDays`.
 */
import fs from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';

const SRC = path.resolve(import.meta.dirname, '..', '..', 'src');
const SCAN_DIRS = ['pages', 'components', 'layouts'].map((d) => path.join(SRC, d));

function sourceFiles(): string[] {
  const out: string[] = [];
  for (const dir of SCAN_DIRS) {
    for (const entry of fs.readdirSync(dir, { withFileTypes: true, recursive: true })) {
      if (!entry.isFile()) continue;
      if (!/\.(astro|svelte)$/.test(entry.name)) continue;
      out.push(path.join(entry.parentPath, entry.name));
    }
  }
  return out.sort();
}

interface Site {
  file: string;
  line: number;
  text: string;
}

function callSites(pattern: RegExp): Site[] {
  const sites: Site[] = [];
  for (const file of sourceFiles()) {
    const lines = fs.readFileSync(file, 'utf8').split(/\r?\n/);
    lines.forEach((text, i) => {
      if (pattern.test(text)) sites.push({ file: path.relative(SRC, file), line: i + 1, text: text.trim() });
    });
  }
  return sites;
}

describe('theme.heat threading', () => {
  it('every rendered heatFor / buildContribGraph call passes the configured thresholds', () => {
    const sites = callSites(/\b(heatFor|buildContribGraph)\(/);
    // The threading is real only if these call sites exist at all — a rename or a glob
    // failure must not turn this test vacuous.
    expect(sites.length).toBeGreaterThanOrEqual(15);
    const bare = sites.filter((s) => !/heatDays|theme\.heat/.test(s.text));
    expect(
      bare.map((s) => `${s.file}:${s.line}  ${s.text}`),
      'call sites relying on DEFAULT_HEAT instead of the configured theme.heat',
    ).toEqual([]);
  });

  it('every RepoCard / RepoListing usage passes heatDays', () => {
    const sites = callSites(/<Repo(Card|Listing)[\s>]/);
    expect(sites.length).toBeGreaterThanOrEqual(6);
    const bare = sites.filter((s) => !/heatDays/.test(s.text));
    expect(
      bare.map((s) => `${s.file}:${s.line}  ${s.text}`),
      'island usages whose cards would silently fall back to DEFAULT_HEAT',
    ).toEqual([]);
  });
});
