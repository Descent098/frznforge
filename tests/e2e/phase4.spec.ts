import { expect, test, type Page } from '@playwright/test';

/** Open the palette, retrying Ctrl+K until the island has hydrated. */
async function openPalette(page: Page) {
  await expect(async () => {
    await page.keyboard.press('Control+k');
    await expect(page.getByRole('dialog', { name: 'Command palette' })).toBeVisible({ timeout: 500 });
  }).toPass({ timeout: 10_000 });
}

test.describe('profile extras (phase 4)', () => {
  test('contribution graph renders cells and footer stats', async ({ page }) => {
    await page.goto('/');
    const grid = page.locator('.hf-contrib-grid');
    await expect(grid).toBeVisible();
    expect(await grid.locator('i').count()).toBe(52 * 7);
    // fixture commits exist → at least one coloured cell and a non-zero total
    await expect(page.locator('.hf-contrib-head h2')).not.toHaveText(/^0 /);
    expect(await grid.locator('i[class*="c-"]').count()).toBeGreaterThan(0);
  });

  test('recent activity lists pushes with repo links', async ({ page }) => {
    await page.goto('/');
    const list = page.locator('.hf-activity-list li');
    expect(await list.count()).toBeGreaterThan(0);
    await expect(list.first()).toContainText(/Pushed|Tagged/);
    const href = await list.first().locator('a').getAttribute('href');
    expect(href).toMatch(/^\/repos\//);
  });
});

test.describe('command palette', () => {
  test('opens with Ctrl+K, searches repos and files, navigates with keyboard', async ({ page }) => {
    await page.goto('/');
    await openPalette(page);
    const palette = page.getByRole('dialog', { name: 'Command palette' });

    // default view shows repos/pages/actions
    await expect(palette.locator('.hf-palette-item')).not.toHaveCount(0);

    // search a repo and open it with Enter
    await palette.getByRole('textbox').fill('alpha');
    await expect(palette.locator('.hf-palette-item').first()).toContainText('alpha');
    await page.keyboard.press('Enter');
    await expect(page).toHaveURL(/\/repos\/alpha\/$/);

    // file search
    await openPalette(page);
    await palette.getByRole('textbox').fill('index.ts');
    await expect(palette.locator('.hf-palette-item strong').first()).toContainText('index.ts');

    // arrow navigation moves the active row
    const active1 = await palette.locator('.hf-palette-item.is-active strong').textContent();
    await page.keyboard.press('ArrowDown');
    const active2 = await palette.locator('.hf-palette-item.is-active strong').textContent();
    expect(active2).not.toBe(active1);

    // Esc closes
    await page.keyboard.press('Escape');
    await expect(palette).toBeHidden();
  });

  test('sidebar search button opens the palette', async ({ page }) => {
    await page.goto('/repos/');
    await page.locator('#hf-search-open').click();
    await expect(page.getByRole('dialog', { name: 'Command palette' })).toBeVisible();
  });

  test('theme toggle action works from the palette', async ({ page }) => {
    await page.goto('/');
    const before = await page.evaluate(() => document.documentElement.getAttribute('data-theme'));
    await openPalette(page);
    const palette = page.getByRole('dialog', { name: 'Command palette' });
    await palette.getByRole('textbox').fill('theme');
    await palette.locator('.hf-palette-item', { hasText: 'Toggle theme' }).first().click();
    const after = await page.evaluate(() => document.documentElement.getAttribute('data-theme'));
    expect(after).not.toBe(before);
    await expect(palette).toBeHidden();
  });

  test('repo page exposes the copy-clone-url action', async ({ page }) => {
    await page.goto('/repos/alpha/');
    await openPalette(page);
    const palette = page.getByRole('dialog', { name: 'Command palette' });
    await palette.getByRole('textbox').fill('clone');
    await expect(palette.locator('.hf-palette-item', { hasText: 'Copy clone URL' })).toBeVisible();
  });

  test('slash opens palette; typing in the search input does not retrigger it', async ({ page }) => {
    await page.goto('/');
    await expect(async () => {
      await page.keyboard.press('/');
      await expect(page.getByRole('dialog', { name: 'Command palette' })).toBeVisible({ timeout: 500 });
    }).toPass({ timeout: 10_000 });
    await page.keyboard.press('Escape');
  });
});
