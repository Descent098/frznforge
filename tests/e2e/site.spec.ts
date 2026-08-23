import { expect, test } from '@playwright/test';

test.describe('profile page', () => {
  test('renders owner, stats, README and pinned/fallback repos', async ({ page }) => {
    await page.goto('/');
    await expect(page.getByRole('heading', { level: 1, name: 'Kieran Wood' })).toBeVisible();
    // stats reflect the fixture artifact: 3 repos
    const repoKpi = page.locator('.hf-kpi', { hasText: 'Repositories' });
    await expect(repoKpi.locator('.hf-kpi-value')).toHaveText('3');
    // profile README body rendered from content/profile.md
    await expect(page.locator('.hf-readme .hf-md h1')).toContainText(/Hi, I.m Kieran/);
    // frontmatter links
    await expect(page.locator('.hf-hero-links a', { hasText: 'kieranwood.ca' })).toHaveAttribute('href', 'https://kieranwood.ca');
    // pinned slug 'frznforge' is not in the fixture → falls back to listing first repos
    await expect(page.locator('.hf-repo-card')).toHaveCount(3);
    await expect(page.locator('.hf-repo-card', { hasText: 'alpha' })).toBeVisible();
  });

  test('sidebar shows repo count and theme toggle flips data-theme', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('.hf-nav a', { hasText: 'Repositories' }).locator('.hf-count')).toHaveText('3');
    const before = await page.evaluate(() => document.documentElement.getAttribute('data-theme'));
    await page.getByRole('button', { name: /^Theme/ }).click();
    const after = await page.evaluate(() => document.documentElement.getAttribute('data-theme'));
    expect(after).toMatch(/light|dark/);
    expect(after).not.toBe(before);
  });
});

test.describe('repo listing', () => {
  test('lists all repos with JS, filters by language/tag/kind, searches, sorts, syncs URL', async ({ page }) => {
    await page.goto('/repos/');
    const cards = page.locator('.hf-repo-card');
    await expect(cards).toHaveCount(3);

    // language facet
    await page.getByRole('button', { name: /^Go\b/ }).click();
    await expect(cards).toHaveCount(1);
    await expect(cards.first()).toContainText('bravo');
    await expect(page).toHaveURL(/lang=Go/);
    await page.getByRole('button', { name: /^Go\b/ }).click();
    await expect(cards).toHaveCount(3);

    // kind = template
    await page.getByRole('button', { name: /^Templates/ }).click();
    await expect(cards).toHaveCount(1);
    await expect(cards.first()).toContainText('Template');
    await page.getByRole('button', { name: 'All', exact: true }).click();

    // search
    await page.getByRole('searchbox', { name: /search repositories/i }).fill('static site');
    await expect(cards).toHaveCount(1);
    await expect(cards.first()).toContainText('alpha');
    await expect(page).toHaveURL(/q=static\+site/);
    await page.getByRole('button', { name: 'Clear' }).click();
    await expect(cards).toHaveCount(3);

    // sort by name desc
    await page.getByRole('combobox', { name: /sort repositories/i }).selectOption('name-desc');
    await expect(cards.first()).toContainText('empty');
    await expect(page).toHaveURL(/sort=name-desc/);
  });

  test('deep link with filters is honoured on load', async ({ page }) => {
    await page.goto('/repos/?tag=cli');
    await expect(page.locator('.hf-repo-card')).toHaveCount(1);
    await expect(page.locator('.hf-repo-card').first()).toContainText('bravo');
  });

  test('renders the full list without JavaScript', async ({ browser }) => {
    const ctx = await browser.newContext({ javaScriptEnabled: false });
    const page = await ctx.newPage();
    await page.goto('/repos/');
    await expect(page.locator('.hf-repo-card')).toHaveCount(3);
    await expect(page.locator('.hf-repo-card a.hf-repo-name', { hasText: 'alpha' })).toHaveAttribute('href', '/repos/alpha/');
    await ctx.close();
  });
});

test.describe('repo overview', () => {
  test('shows metadata, README, languages, contributors, clone URL from the artifact', async ({ page }) => {
    await page.goto('/repos/alpha/');
    await expect(page.locator('.hf-crumb strong')).toHaveText('alpha');
    await expect(page.locator('.hf-repo-summary')).toContainText('Alpha fixture');
    // README rendered from the COMMITTED content, not the modified working copy
    await expect(page.locator('.hf-readme-card .hf-md h1')).toHaveText('Alpha');
    await expect(page.locator('.hf-readme-card')).not.toContainText('MODIFIED BUT NOT COMMITTED');
    // uncommitted/untracked file never appears
    await expect(page.locator('.hf-files')).not.toContainText('UNTRACKED-SECRET');
    await expect(page.locator('.hf-files')).toContainText('src');
    // about panel
    await expect(page.locator('.hf-about')).toContainText('MIT');
    await expect(page.locator('.hf-about .hf-lang-legend')).toContainText('TypeScript');
    await expect(page.locator('.hf-about .hf-contributors')).toContainText('Fixture Author');
    await expect(page.locator('.hf-about-links a', { hasText: 'example.com/alpha' })).toBeVisible();
    // tags link to the filtered listing
    await expect(page.locator('.hf-about-tags a', { hasText: 'ssg' })).toHaveAttribute('href', '/repos/?tag=ssg');
    // clone panel derived from upstream
    await page.locator('.hf-clone summary').click();
    await expect(page.locator('.hf-clone-url code')).toHaveText('https://github.com/example/alpha.git');
    // latest commit bar
    await expect(page.locator('.hf-commit-bar')).toContainText('bump the answer');
  });

  test('template repo shows the template banner', async ({ page }) => {
    await page.goto('/repos/bravo/');
    await expect(page.locator('.hf-banner--template')).toContainText('template repository');
  });

  test('empty repo still has a page that says so', async ({ page }) => {
    await page.goto('/repos/empty/');
    await expect(page.locator('.hf-empty')).toContainText('This repository is empty');
  });
});

test('404 page', async ({ page }) => {
  const res = await page.goto('/repos/does-not-exist/');
  expect(res?.status()).toBe(404);
  await expect(page.locator('.hf-404-code')).toHaveText('404');
});
