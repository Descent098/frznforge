/**
 * `site.base` (0.2.0): the SAME fixture artifact built again under `/mysite` (see
 * global-setup) and served by serve.ts in prefix mode on port 4398 — requests outside the
 * prefix 404, exactly like a real sub-path deploy, so any root-absolute link that survived
 * the sweep dies loudly here rather than quietly in production.
 */
import fs from 'node:fs';
import path from 'node:path';
import { expect, test } from '@playwright/test';

const ORIGIN = 'http://localhost:4398';
const BASE = `${ORIGIN}/mysite`;
const DIST_BASE = path.resolve(import.meta.dirname, '..', '.tmp', 'e2e', 'dist-base');

test.describe('base path', () => {
  test('the site works end to end under the prefix', async ({ page }) => {
    await page.goto(`${BASE}/`);
    // sidebar nav carries the base
    await expect(page.locator('.hf-nav a', { hasText: 'Repositories' })).toHaveAttribute('href', '/mysite/repos/');
    // navigate through real links: listing → repo → file browser
    await page.locator('.hf-nav a', { hasText: 'Repositories' }).click();
    await expect(page).toHaveURL(`${BASE}/repos/`);
    await page.locator('.hf-repo-card[data-slug="alpha"] .hf-repo-name').click();
    await expect(page).toHaveURL(`${BASE}/repos/alpha/`);
    // a raw link resolves (the file table links are helper-built)
    await page.goto(`${BASE}/repos/alpha/blob/main/README.md/`);
    const rawHref = await page.locator('a', { hasText: 'Raw' }).first().getAttribute('href');
    expect(rawHref).toMatch(/^\/mysite\//);
    expect((await page.request.get(`${ORIGIN}${rawHref}`)).status()).toBe(200);
    // icons too
    expect((await page.request.get(`${BASE}/logo.png`)).status()).toBe(200);
    expect((await page.request.get(`${BASE}/favicon.ico`)).status()).toBe(200);
  });

  test('the command palette fetches its index and jumps under the prefix', async ({ page }) => {
    await page.goto(`${BASE}/`);
    // open the palette, retrying Ctrl+K until the island has hydrated (phase4.spec pattern)
    await expect(async () => {
      await page.keyboard.press('Control+k');
      await expect(page.getByRole('dialog', { name: 'Command palette' })).toBeVisible({ timeout: 500 });
    }).toPass({ timeout: 10_000 });
    const palette = page.getByRole('dialog', { name: 'Command palette' });
    await palette.getByRole('textbox').fill('bravo');
    await expect(palette.locator('.hf-palette-item').first()).toContainText('bravo');
    await page.keyboard.press('Enter');
    await expect(page).toHaveURL(`${BASE}/repos/bravo/`);
  });

  test('nothing outside the prefix is served', async ({ page }) => {
    expect((await page.request.get(`${ORIGIN}/`)).status()).toBe(404);
    expect((await page.request.get(`${ORIGIN}/repos/`)).status()).toBe(404);
  });

  test('the built dist carries zero root-absolute leaks', async () => {
    // Every internal href/src/action in every HTML file must start with /mysite (or be a
    // fragment / external / data: URL). This is the backstop behind the hand-maintained
    // literal sweep — a new hardcoded '/...' link fails here before it ships.
    const offenders: string[] = [];
    const walk = (dir: string): void => {
      for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
        const p = path.join(dir, e.name);
        if (e.isDirectory()) walk(p);
        else if (e.name.endsWith('.html')) {
          const html = fs.readFileSync(p, 'utf8');
          for (const m of html.matchAll(/\b(?:href|src|action)="(\/[^"]*)"/g)) {
            const url = m[1]!;
            if (url === '/mysite' || url.startsWith('/mysite/')) continue;
            offenders.push(`${path.relative(DIST_BASE, p)} → ${url}`);
          }
        }
      }
    };
    walk(DIST_BASE);
    expect(offenders.slice(0, 20), `${offenders.length} root-absolute URL(s) escaped the base`).toEqual([]);
  });
});
