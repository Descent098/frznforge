/**
 * Phase 3 e2e: file browser (tree/blob/raw), ref switcher, commits, branches, tags,
 * releases and archives, against the fixture repos built in global-setup.
 */
import { expect, test } from '@playwright/test';

test.describe('file browser', () => {
  test('overview → src dir → file with highlighted code, gutter and line anchors', async ({ page }) => {
    await page.goto('/repos/alpha/');
    await page.locator('.hf-files a.hf-fname', { hasText: 'src' }).click();
    await expect(page).toHaveURL(/\/repos\/alpha\/tree\/main\/src\/$/);
    // directory listing with a ".." row and file links
    await expect(page.locator('.hf-files .hf-parent a')).toHaveAttribute('href', '/repos/alpha/tree/main/');
    await page.locator('.hf-files a.hf-fname', { hasText: 'index.ts' }).click();
    await expect(page).toHaveURL(/\/repos\/alpha\/blob\/main\/src\/index\.ts\/$/);

    // highlighted code with line ids emitted by shiki
    await expect(page.locator('.hf-code .shiki')).toBeVisible();
    await expect(page.locator('.hf-code .line#L1')).toBeVisible();
    await expect(page.locator('.hf-blob-meta')).toContainText('20 lines');
    await expect(page.locator('.hf-lang-badge')).toHaveText('TypeScript');

    // CSS-only gutter: counters render in ::before
    const gutter = await page.locator('.hf-code .line#L2').evaluate((el) => getComputedStyle(el, '::before').content);
    expect(gutter).toContain('counter');

    // #L2 targets line 2 via pure CSS
    await page.goto('/repos/alpha/blob/main/src/index.ts/#L2');
    const targetBg = await page.locator('#L2').evaluate((el) => getComputedStyle(el).backgroundColor);
    expect(targetBg).not.toBe('rgba(0, 0, 0, 0)');
    const otherBg = await page.locator('#L3').evaluate((el) => getComputedStyle(el).backgroundColor);
    expect(otherBg).toBe('rgba(0, 0, 0, 0)');
  });

  test('raw view serves the exact committed bytes as text/plain', async ({ page, request }) => {
    await page.goto('/repos/alpha/blob/main/src/index.ts/');
    const rawHref = await page.locator('.hf-blob-actions a', { hasText: /^Raw$/ }).getAttribute('href');
    expect(rawHref).toBe('/repos/alpha/raw/main/src/index.ts');
    const res = await request.get(rawHref!);
    expect(res.status()).toBe(200);
    expect(res.headers()['content-type']).toContain('text/plain');
    expect(await res.text()).toBe('export const answer: number = 43;\n'.repeat(20));
  });

  test('markdown blob shows Preview by default and Source when toggled', async ({ page }) => {
    await page.goto('/repos/alpha/blob/main/docs/guide.md/');
    // preview rendered by default
    await expect(page.locator('.hf-mdview-preview .hf-md h1')).toHaveText('Guide');
    await expect(page.locator('.hf-mdview-preview .hf-md strong')).toHaveText('bold');
    await expect(page.locator('.hf-mdview-source')).toBeHidden();
    // toggle to source (radio-input CSS trick, no JS involved)
    await page.locator('label[for="hf-md-source"]').click();
    await expect(page.locator('.hf-mdview-preview')).toBeHidden();
    await expect(page.locator('.hf-mdview-source .shiki')).toBeVisible();
    await expect(page.locator('.hf-mdview-source')).toContainText('# Guide');
  });

  test('image blob renders an <img> whose raw URL serves image/png', async ({ page, request }) => {
    await page.goto('/repos/alpha/blob/main/assets/dot.png/');
    const img = page.locator('.hf-blob-image img');
    await expect(img).toBeVisible();
    const src = await img.getAttribute('src');
    expect(src).toBe('/repos/alpha/raw/main/assets/dot.png');
    const res = await request.get(src!);
    expect(res.status()).toBe(200);
    expect(res.headers()['content-type']).toContain('image/png');
  });

  test('ref switcher jumps to the feature branch and shows the extra file', async ({ page }) => {
    await page.goto('/repos/alpha/tree/main/src/');
    await page.locator('.hf-refsw summary').click();
    await page.locator('.hf-refsw-pop a', { hasText: 'feature/extra' }).click();
    // same path exists on the feature branch → lands on its src dir
    await expect(page).toHaveURL(/\/repos\/alpha\/tree\/feature~extra\/src\/$/);
    await expect(page.locator('.hf-files a.hf-fname', { hasText: 'extra.ts' })).toBeVisible();
    // and the extra file does NOT exist on main
    await page.goto('/repos/alpha/tree/main/src/');
    await expect(page.locator('.hf-files')).not.toContainText('extra.ts');
  });

  test('404 for a bogus blob path', async ({ page }) => {
    const res = await page.goto('/repos/alpha/blob/main/nope/nada.ts/');
    expect(res?.status()).toBe(404);
  });
});

test.describe('history', () => {
  test('commits page groups by day, shows stats and links to the commit page', async ({ page }) => {
    await page.goto('/repos/alpha/commits/main/');
    await expect(page.locator('.hf-commit-day')).toHaveCount(2); // Jan 1 + Feb 1 fixture days
    const row = page.locator('.hf-commit-row', { hasText: 'bump the answer' });
    await expect(row).toBeVisible();
    await expect(row.locator('.hf-adds')).toHaveText('+20');
    await expect(row.locator('.hf-dels')).toHaveText('−20');
    await expect(row.locator('.hf-commit-sha code')).toHaveText(/^[0-9a-f]{7}$/);
    await row.locator('a.hf-commit-subject').click();
    await expect(page).toHaveURL(/\/repos\/alpha\/commit\/[0-9a-f]{40}\/$/);
    await expect(page.locator('.hf-commit-page-head h1')).toHaveText('bump the answer');
    await expect(page.locator('.hf-commit-page-stats')).toContainText('1 file changed');
    const fileRow = page.locator('.hf-commit-files tbody tr', { hasText: 'src/index.ts' });
    await expect(fileRow.locator('.hf-adds')).toHaveText('+20');
    await expect(fileRow.locator('.hf-dels')).toHaveText('−20');
  });

  test('branches page marks the default branch', async ({ page }) => {
    await page.goto('/repos/alpha/branches/');
    const main = page.locator('.hf-reflist tbody tr', { hasText: 'main' }).first();
    await expect(main.locator('.hf-ref-default')).toHaveText('default');
    await expect(page.locator('.hf-reflist tbody tr', { hasText: 'feature/extra' })).toBeVisible();
  });

  test('tags page lists all three tags with their kinds', async ({ page }) => {
    await page.goto('/repos/alpha/tags/');
    await expect(page.locator('.hf-taglist tbody tr')).toHaveCount(3);
    await expect(page.locator('.hf-taglist tr', { hasText: 'v1.1.0' }).locator('.hf-tagkind--annotated')).toHaveText('annotated');
    await expect(page.locator('.hf-taglist tr', { hasText: 'v1.0.0' }).locator('.hf-tagkind--annotated')).toHaveText('annotated');
    await expect(page.locator('.hf-taglist tr', { hasText: 'light' }).locator('.hf-tagkind--light')).toHaveText('lightweight');
  });
});

test.describe('releases', () => {
  test('index shows v1.1.0 as Latest; release page renders markdown and a working zip', async ({ page, request }) => {
    await page.goto('/repos/alpha/releases/');
    const cards = page.locator('.hf-release-card');
    await expect(cards).toHaveCount(2);
    await expect(cards.first()).toContainText('v1.1.0');
    await expect(cards.first().locator('.hf-release-latest')).toHaveText('Latest');

    await page.goto('/repos/alpha/releases/v1.1.0/');
    await expect(page.locator('.hf-release-head h1')).toHaveText('v1.1.0');
    // tag message body rendered as markdown
    await expect(page.locator('.hf-release-notes h2')).toHaveText('Highlights');
    await expect(page.locator('.hf-release-notes em')).toHaveText('guide');

    const zipHref = await page.locator('.hf-release-assets a', { hasText: 'Source code (zip)' }).getAttribute('href');
    expect(zipHref).toBe('/repos/alpha/archive/v1.1.0.zip');
    const res = await request.get(zipHref!);
    expect(res.status()).toBe(200);
    expect(res.headers()['content-type']).toContain('application/zip');
    expect((await res.body()).byteLength).toBeGreaterThan(22); // more than an empty zip
  });

  test('repo with no releases shows the empty state', async ({ page }) => {
    await page.goto('/repos/bravo/releases/');
    await expect(page.locator('.hf-empty')).toContainText('Releases are created from annotated tags');
  });
});
