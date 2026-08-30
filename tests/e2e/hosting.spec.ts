/**
 * Hosted static sites (schema v7): alpha's gh-pages branch (resolved automatically — the
 * fixture configures no branch) serves at /alpha-site/ while the normal forge view of the
 * same repo keeps working, gh-pages included as a browsable ref.
 *
 * Hosted pages are deliberately NOT in the a11y sweep's inventory: they are arbitrary user
 * content, not frznforge chrome, and holding a user's own site to the forge's contrast and
 * heading gates would fail spuriously (see a11y.spec.ts).
 */
import { expect, test } from '@playwright/test';

test.describe('hosted static sites', () => {
  test('the site serves at its slug with real content types', async ({ page }) => {
    const index = await page.request.get('/alpha-site/');
    expect(index.status()).toBe(200);
    expect(index.headers()['content-type']).toContain('text/html');

    await page.goto('/alpha-site/');
    await expect(page.getByRole('heading', { name: 'built by alpha' })).toBeVisible();
    // the site's own script ran — hosted JS is the user's own content, served verbatim
    await expect(page).toHaveTitle(/alpha site ✓/);

    const css = await page.request.get('/alpha-site/style.css');
    expect(css.status()).toBe(200);
    expect(css.headers()['content-type']).toContain('text/css');
    const js = await page.request.get('/alpha-site/app.js');
    expect(js.status()).toBe(200);
    expect(js.headers()['content-type']).toContain('javascript');
  });

  test('the forge view of the same repo coexists, gh-pages browsable', async ({ page }) => {
    await page.goto('/repos/alpha/');
    await expect(page.getByRole('heading', { level: 1, name: 'alpha' })).toBeVisible();
    await page.goto('/repos/alpha/tree/gh-pages/');
    await expect(page.locator('.hf-files')).toContainText('index.html');
    await page.goto('/repos/alpha/blob/gh-pages/style.css/');
    await expect(page.locator('.hf-code').first()).toContainText('rebeccapurple');
  });
});
