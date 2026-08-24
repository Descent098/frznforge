/**
 * Phase 7 e2e: the per-repo Insights page (schema v5).
 *
 * Data-driven, like the releases spec: the expectations are read out of the built fixture
 * artifact rather than hard-coded, so the assertions stay true when the fixture repos gain
 * history. The whole suite skips itself when no fixture repo carries insights (ingest with
 * `insights.enabled: false`, or an artifact built before the series existed) instead of
 * failing with a confusing selector error.
 */
import fs from 'node:fs';
import path from 'node:path';
import { expect, test } from '@playwright/test';

/* ---- fixture introspection ------------------------------------------------ */

interface FixtureCommitPoint { month: string; commits: number; contributors: number }
interface FixtureCodePoint { month: string; bytes: number; lines: number | null }
interface FixtureInsights {
  commits: FixtureCommitPoint[];
  codeSize: FixtureCodePoint[];
  sampled: boolean;
  sampleCount: number;
  approximate: boolean;
}
interface FixtureRepo {
  slug: string;
  name: string;
  empty: boolean;
  defaultBranch: string | null;
  contributors: Array<{ name: string }>;
  /** Optional, not just nullable: an artifact built before schema v5 has no such key at all. */
  insights?: FixtureInsights | null;
}

const ARTIFACT = path.resolve(import.meta.dirname, '..', '.tmp', 'e2e', 'data', 'forge.json');

function readRepos(): FixtureRepo[] {
  try {
    return (JSON.parse(fs.readFileSync(ARTIFACT, 'utf8')) as { repos: FixtureRepo[] }).repos;
  } catch {
    return [];
  }
}

const repos = readRepos();
/** Same predicate as `hasInsights()` in src/lib/routes.ts — the page exists exactly here. */
const withInsights = repos.filter((r) => !r.empty && (r.insights?.commits.length ?? 0) > 0);
/** The richest fixture repo, so the thinning / multi-point paths get exercised when they can. */
const subject =
  [...withInsights].sort((a, b) => b.insights!.commits.length - a.insights!.commits.length)[0] ?? null;
const emptyRepo = repos.find((r) => r.empty) ?? null;
/** Built but without insights (e.g. a repo whose series came back empty) — must hide the tab. */
const withoutInsights = repos.find((r) => !r.empty && (r.insights?.commits.length ?? 0) === 0) ?? null;

const en = (n: number) => n.toLocaleString('en');
const only = (n: number, one: string) => `${en(n)} ${n === 1 ? one : `${one}s`}`;
function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

/* Collection-time safe: the whole suite skips when there is no subject, but these constants
 * are still evaluated while the file loads, so they must not dereference a null repo. */
const EMPTY: FixtureInsights = { commits: [], codeSize: [], sampled: false, sampleCount: 0, approximate: false };
const repo: FixtureRepo = subject ?? { slug: 'none', name: 'none', empty: true, defaultBranch: null, contributors: [], insights: null };
const ins: FixtureInsights = subject?.insights ?? EMPTY;
const url = `/repos/${repo.slug}/insights/`;
const totalCommits = ins.commits.reduce((sum, p) => sum + p.commits, 0);
const hasLines = ins.codeSize.length > 0 && ins.codeSize.every((p) => p.lines !== null);

test.describe('repo insights', () => {
  test.skip(subject === null, 'no fixture repo has insights (ingest.insights disabled?)');

  test('the Insights tab is on the repo and leads to the page', async ({ page }) => {
    await page.goto(`/repos/${repo.slug}/`);
    const tab = page.locator('.hf-tabs a', { hasText: 'Insights' });
    await expect(tab).toHaveCount(1);
    await expect(tab).toHaveAttribute('href', url);

    await tab.click();
    await expect(page).toHaveURL(new RegExp(`${url}$`));
    await expect(page.locator('h1')).toHaveText('Insights');
    // the tab marks itself current on its own page
    await expect(page.locator('.hf-tabs a[aria-current="page"]')).toHaveText(/Insights/);
  });

  test('every series renders as a figure with a non-empty SVG', async ({ page }) => {
    await page.goto(url);

    // commits + contributors always; code size only when ingest sampled any checkpoint
    const expected = 2 + (ins.codeSize.length > 0 ? 1 : 0);
    const figures = page.locator('.hf-insights figure.hf-chart');
    await expect(figures).toHaveCount(expected);

    for (const id of ['chart-commits', 'chart-contributors', ...(ins.codeSize.length > 0 ? ['chart-size'] : [])]) {
      const fig = page.locator(`#${id}`);
      await expect(fig).toBeVisible();
      // a caption names the figure, and the SVG is an image with a summarising label
      await expect(fig.locator('figcaption h2')).not.toBeEmpty();
      const svg = fig.locator('svg.hf-chart-svg');
      await expect(svg).toBeVisible();
      await expect(svg).toHaveAttribute('role', 'img');
      const label = await svg.getAttribute('aria-label');
      expect(label ?? '').not.toBe('');
      // one mark per data point, each carrying its own value
      const marks = fig.locator('svg.hf-chart-svg .hf-chart-bar, svg.hf-chart-svg .hf-chart-dot');
      const points = id === 'chart-size' ? ins.codeSize.length : ins.commits.length;
      await expect(marks).toHaveCount(points);
      // real axes: gridlines and at least two labelled ticks
      expect(await fig.locator('svg.hf-chart-svg .hf-chart-grid').count()).toBeGreaterThanOrEqual(2);
      expect(await fig.locator('svg.hf-chart-svg .hf-chart-tick').count()).toBeGreaterThanOrEqual(2);
      expect(await fig.locator('svg.hf-chart-svg .hf-chart-xlab').count()).toBeGreaterThanOrEqual(1);
    }
  });

  test('the hidden data table repeats every value the chart shows on hover', async ({ page }) => {
    await page.goto(url);

    for (const id of ['chart-commits', 'chart-contributors']) {
      const fig = page.locator(`#${id}`);
      const titles = await fig.locator('svg.hf-chart-svg title').allTextContents();
      expect(titles.length).toBe(ins.commits.length);

      const rows = fig.locator(`#${id}-table tbody tr`);
      await expect(rows).toHaveCount(ins.commits.length);
      const fromTable: string[] = [];
      for (let i = 0; i < ins.commits.length; i++) {
        const row = rows.nth(i);
        // textContent, not innerText: the table is clipped to 1px, so rendered-text APIs
        // are unreliable on it — the point is that the DATA is in the DOM.
        const month = (await row.locator('th').textContent())?.trim() ?? '';
        const value = (await row.locator('td').textContent())?.trim() ?? '';
        fromTable.push(`${month}: ${value}`);
      }
      expect(titles.map((t) => t.trim())).toEqual(fromTable.map((t) => t.trim()));
    }

    // and the numbers are the artifact's, not decoration
    const expectedCommitTitles = ins.commits.map((p) => `${p.month}: ${only(p.commits, 'commit')}`);
    const actual = (await page.locator('#chart-commits svg.hf-chart-svg title').allTextContents()).map((t) => t.trim());
    expect(actual).toEqual(expectedCommitTitles);
  });

  test('the data table is visually hidden but present in the DOM', async ({ page }) => {
    await page.goto(url);
    const table = page.locator('#chart-commits-table');
    await expect(table).toHaveCount(1);
    await expect(table).toHaveClass(/hf-sr/);
    const box = await table.boundingBox();
    expect(box === null || box.width <= 2).toBeTruthy();
  });

  test('the summary tiles carry the totals the artifact holds', async ({ page }) => {
    await page.goto(url);
    const tiles = page.locator('.hf-insight-kpis .hf-kpi');
    await expect(tiles).toHaveCount(4);

    await expect(tiles.nth(0).locator('.hf-kpi-value')).toHaveText(en(totalCommits));
    await expect(tiles.nth(1).locator('.hf-kpi-value')).toHaveText(en(repo.contributors.length));
    await expect(tiles.nth(2).locator('.hf-kpi-value')).toHaveText(en(ins.commits.length));

    const latest = ins.codeSize.length ? ins.codeSize[ins.codeSize.length - 1]! : null;
    await expect(tiles.nth(3).locator('.hf-kpi-value')).toHaveText(latest ? formatBytes(latest.bytes) : '—');

    // plausibility, independent of the fixture's exact numbers
    expect(totalCommits).toBeGreaterThan(0);
    expect(ins.commits.length).toBeGreaterThan(0);
  });

  test('a bytes-only code-size series says so instead of pretending to count lines', async ({ page }) => {
    test.skip(ins.codeSize.length === 0, 'this fixture has no code-size checkpoints');
    await page.goto(url);
    const fig = page.locator('#chart-size');
    if (hasLines) {
      await expect(fig.locator('figcaption h2')).toHaveText(/Lines of code/i);
    } else {
      await expect(fig.locator('figcaption h2')).toHaveText(/Code size/i);
      await expect(fig.locator('.hf-chart-note')).toContainText(/sampled by size/i);
    }
  });

  test('one h1, and nothing overflows the page at 500px', async ({ page }) => {
    await page.setViewportSize({ width: 500, height: 900 });
    await page.goto(url);

    await expect(page.locator('h1')).toHaveCount(1);

    const overflow = await page.evaluate(() => ({
      scroll: document.documentElement.scrollWidth,
      client: document.documentElement.clientWidth,
    }));
    expect(overflow.scroll).toBeLessThanOrEqual(overflow.client);

    // the wide chart scrolls inside its own box rather than pushing the page out
    const scroller = page.locator('#chart-commits .hf-chart-scroll');
    const canScroll = await scroller.evaluate((el) => el.scrollWidth > el.clientWidth);
    if (canScroll) {
      expect(await scroller.evaluate((el) => getComputedStyle(el).overflowX)).toBe('auto');
      // keyboard users can reach it (WCAG 2.1.1)
      await expect(scroller).toHaveAttribute('tabindex', '0');
    }
  });

  test('an empty repo offers no Insights tab and no insights page', async ({ page }) => {
    test.skip(emptyRepo === null, 'the fixture has no empty repo');
    await page.goto(`/repos/${emptyRepo!.slug}/`);
    await expect(page.locator('.hf-tabs a', { hasText: 'Insights' })).toHaveCount(0);

    const res = await page.goto(`/repos/${emptyRepo!.slug}/insights/`);
    expect(res?.status()).toBe(404);
  });

  test('a repo without an insights series hides the tab too', async ({ page }) => {
    test.skip(withoutInsights === null, 'every non-empty fixture repo has insights');
    await page.goto(`/repos/${withoutInsights!.slug}/`);
    await expect(page.locator('.hf-tabs a', { hasText: 'Insights' })).toHaveCount(0);
  });
});
