import { expect, test } from '@playwright/test';

test.describe('profile page', () => {
  test('renders owner, stats, README and pinned/fallback repos', async ({ page }) => {
    await page.goto('/');
    await expect(page.getByRole('heading', { level: 1, name: 'Kieran Wood' })).toBeVisible();
    // stats reflect the fixture artifact: 5 repos (3 local + 2 provider-imported)
    const repoKpi = page.locator('.hf-kpi', { hasText: 'Repositories' });
    await expect(repoKpi.locator('.hf-kpi-value')).toHaveText('5');
    // profile README body rendered from content/profile.md
    // user markdown is demoted a level so the page chrome keeps the only <h1>
    await expect(page.locator('.hf-readme .hf-md h2')).toContainText(/Hi, I.m Kieran/);
    // frontmatter links
    await expect(page.locator('.hf-hero-links a', { hasText: 'kieranwood.ca' })).toHaveAttribute('href', 'https://kieranwood.ca');
    // pinned slug 'frznforge' is not in the fixture → falls back to listing first repos
    await expect(page.locator('.hf-repo-card')).toHaveCount(5);
    await expect(page.locator('.hf-repo-card', { hasText: 'alpha' })).toBeVisible();
  });

  test('brand icons resolve and the sidebar carries the anvil mark', async ({ page }) => {
    // Base.astro's two icon links must point at real files in the built dist (they are
    // base-path-relevant later, and the 0.2.0 logo refresh regenerated both), and the
    // sidebar brand tile now uses the frozen-anvil sprite symbol.
    await page.goto('/');
    for (const href of ['/logo.png', '/favicon.ico']) {
      const res = await page.request.get(href);
      expect(res.status(), href).toBe(200);
      expect(Number(res.headers()['content-length'] ?? '0'), `${href} size`).toBeLessThan(100 * 1024);
    }
    await expect(page.locator('.hf-brand-mark use')).toHaveAttribute('href', '#i-anvil');
  });

  test('sidebar shows repo count and theme toggle flips data-theme', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('.hf-nav a', { hasText: 'Repositories' }).locator('.hf-count')).toHaveText('5');
    const before = await page.evaluate(() => document.documentElement.getAttribute('data-theme'));
    await page.getByRole('button', { name: /^Theme/ }).click();
    const after = await page.evaluate(() => document.documentElement.getAttribute('data-theme'));
    expect(after).toMatch(/light|dark/);
    expect(after).not.toBe(before);
  });
});

test.describe('repo listing', () => {
  test('repo cards carry the recency heat classes end to end (theme.heat pipeline)', async ({ page }) => {
    // alpha's newest commit is authored 2 days before the build (inside `theme.heat.hot`),
    // bravo's is fixed at 2024-08 (far past `cool`). This walks the whole pipeline —
    // config → page frontmatter → island prop → RepoCard class — on both the card rail
    // (`heat-*`) and the age label (`t-*`). A non-default cutoff cannot reach this build
    // (the site reads the checked-in frznforge.config.ts by design), so the configured
    // flip itself is pinned by the unit tests in format.test.ts / config-knobs.test.ts.
    await page.goto('/repos/');
    const alpha = page.locator('.hf-repo-card[data-slug="alpha"]');
    await expect(alpha).toHaveClass(/heat-hot/);
    await expect(alpha.locator('.hf-age')).toHaveClass(/t-hot/);
    const bravo = page.locator('.hf-repo-card[data-slug="bravo"]');
    await expect(bravo).toHaveClass(/heat-cold/);
    await expect(bravo.locator('.hf-age')).toHaveClass(/t-cold/);
  });

  test('lists all repos with JS, filters by language/tag/kind, searches, sorts, syncs URL', async ({ page }) => {
    await page.goto('/repos/');
    const cards = page.locator('.hf-repo-card');
    await expect(cards).toHaveCount(5);

    // language facet
    await page.getByRole('button', { name: /^Go\b/ }).click();
    await expect(cards).toHaveCount(1);
    await expect(cards.first()).toContainText('bravo');
    await expect(page).toHaveURL(/lang=Go/);
    await page.getByRole('button', { name: /^Go\b/ }).click();
    await expect(cards).toHaveCount(5);

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
    await expect(cards).toHaveCount(5);

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
    await expect(page.locator('.hf-repo-card')).toHaveCount(5);
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
    await expect(page.locator('.hf-readme-card .hf-md h2')).toHaveText('Alpha');
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

  test('a recognised license links to its canonical page, in the header and the About panel', async ({ page }) => {
    await page.goto('/repos/alpha/');
    const href = 'https://choosealicense.com/licenses/mit/';
    await expect(page.locator('.hf-badges a.hf-badge', { hasText: 'MIT' })).toHaveAttribute('href', href);
    await expect(page.locator('.hf-about-meta a', { hasText: 'MIT' })).toHaveAttribute('href', href);
  });

  test('a provider-supplied license id links too, not just a detected one', async ({ page }) => {
    // charlie's Apache-2.0 comes from the provider metadata layer, alpha's MIT from
    // reading the LICENSE file — both reach the same badge, so both must link.
    await page.goto('/repos/charlie/');
    await expect(page.locator('.hf-badges a.hf-badge', { hasText: 'Apache-2.0' })).toHaveAttribute(
      'href',
      'https://choosealicense.com/licenses/apache-2.0/',
    );
  });

  test('the clone popup is sized by its content, not squeezed to the button', async ({ page }) => {
    // Regression: `.hf-clone-pop`'s `max-width: 100%` resolved against the shrink-wrapped
    // <details>, clamping the 360px panel to the button width and spilling its contents.
    await page.goto('/repos/alpha/');
    await page.locator('.hf-clone summary').click();
    const pop = page.locator('.hf-clone-pop');
    await expect(pop).toBeVisible();
    const popBox = (await pop.boundingBox())!;
    const btnBox = (await page.locator('.hf-clone summary').boundingBox())!;
    expect(popBox.width).toBeGreaterThan(btnBox.width + 100);
    // and its content sits inside it
    const urlBox = (await page.locator('.hf-clone-url').boundingBox())!;
    expect(urlBox.x).toBeGreaterThanOrEqual(popBox.x - 1);
    expect(urlBox.x + urlBox.width).toBeLessThanOrEqual(popBox.x + popBox.width + 1);
  });

  test('the clone popup stays inside a phone viewport without collapsing to the button', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 800 });
    await page.goto('/repos/alpha/');
    await page.locator('.hf-clone summary').click();
    const box = (await page.locator('.hf-clone-pop').boundingBox())!;
    const btn = (await page.locator('.hf-clone summary').boundingBox())!;
    // inside both edges...
    expect(box.x).toBeGreaterThanOrEqual(-1);
    expect(box.x + box.width).toBeLessThanOrEqual(375 + 1);
    // ...and still a real panel. Containment alone passed while the popup was squeezed to
    // the button, which is the very bug this pair of tests exists to catch.
    expect(box.width).toBeGreaterThan(btn.width + 100);
    // the page itself must not gain a sideways scroll because of it
    const docScrolls = await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth);
    expect(docScrolls).toBe(false);
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
