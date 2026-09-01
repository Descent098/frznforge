/**
 * Organizations (schema v4) end-to-end.
 *
 * Expectations are read from the artifact the global setup built
 * (`tests/.tmp/e2e/data/forge.json`) rather than hard-coded: the fixture repo set and the
 * shipped `frznforge.config.ts` organizations change independently, and what must hold is
 * "the pages agree with the artifact", not "there are exactly N members". The artifact is
 * read *inside* the tests, never at module load — Playwright collects test files before
 * globalSetup runs, so at collection time that file may not exist yet.
 *
 * The markdown half of an organization (`content/orgs/<slug>.md`) is real repo content and is
 * read by the build regardless of the fixture artifact, so the `canadian-coding` body/links
 * assertions are pinned to that file.
 */
import fs from 'node:fs';
import path from 'node:path';
import { expect, test, type Page } from '@playwright/test';
import { waitForIslands } from './helpers';

interface ArtifactOrg { slug: string; name: string; description: string | null; repos: string[] }

const ARTIFACT = path.join(import.meta.dirname, '..', '.tmp', 'e2e', 'data', 'forge.json');

/** Organizations in the fixture artifact, in artifact order (slug asc). */
function artifactOrgs(): ArtifactOrg[] {
  return JSON.parse(fs.readFileSync(ARTIFACT, 'utf8')).organizations as ArtifactOrg[];
}

/** Slugs of the repo cards rendered on the current page, in DOM order. */
function cardSlugs(page: Page): Promise<string[]> {
  return page.locator('.hf-repo-card').evaluateAll((els) => els.map((e) => e.getAttribute('data-slug') ?? ''));
}

test.describe('organizations', () => {
  test('an organization renders its configured picture (schema v8)', async ({ page }) => {
    await page.goto('/orgs/canadian-coding/');
    // the picture occupies exactly the 128px box the initials block used to
    const hero = page.locator('.hf-hero img.hf-avatar--img');
    await expect(hero).toHaveAttribute('src', '/logo.png');
    const box = (await hero.boundingBox())!;
    expect(Math.round(box.width)).toBe(128);
    await expect(hero).toHaveJSProperty('naturalWidth', 512); // it actually decodes

    // `owner.avatar` deliberately has NO e2e assertion: the site build reads the checked-in
    // frznforge.config.ts (only outDir/cacheDir/base have env overrides), so an owner value
    // set on the fixture config object never reaches the rendered page — the same limitation
    // recorded for `theme.heat` in 0.2.0. Organization and contributor avatars DO reach it,
    // because those travel in the artifact. The owner path is covered by the unit tests and
    // was verified by hand in a real browser.
  });

  test('an avatar-less organization still falls back to initials', async ({ page }) => {
    await page.goto('/orgs/');
    // every card either has a picture or the initials block — never neither
    const cards = page.locator('.hf-org-card .hf-org-top');
    const n = await cards.count();
    for (let i = 0; i < n; i += 1) {
      const card = cards.nth(i);
      const hasImg = await card.locator('img.hf-avatar--img').count();
      const hasInitials = await card.locator('span.hf-avatar').count();
      expect(hasImg + hasInitials).toBeGreaterThan(0);
    }
  });

  test('the index lists every organization in the artifact with its member count', async ({ page }) => {
    const orgs = artifactOrgs();
    await page.goto('/orgs/');
    await expect(page.getByRole('heading', { level: 1, name: 'Organizations' })).toBeVisible();

    await expect(page.locator('.hf-org-card')).toHaveCount(orgs.length);
    if (orgs.length === 0) {
      await expect(page.locator('.hf-empty')).toContainText('No organizations yet.');
      return;
    }
    for (const org of orgs) {
      const card = page.locator(`.hf-org-card[data-slug="${org.slug}"]`);
      await expect(card.getByRole('heading', { level: 2 })).toHaveText(org.name);
      await expect(card).toContainText(`${org.repos.length} repositor`);
      await expect(card.getByRole('link', { name: org.name, exact: true })).toHaveAttribute('href', `/orgs/${org.slug}/`);
      // WCAG 2.5.3: the accessible name has to contain the visible text, so the org name is
      // appended rather than spliced into the middle of it.
      const listingLink = card.getByRole('link', { name: `Browse the listing for ${org.name}` });
      await expect(listingLink).toHaveAttribute('href', `/orgs/${org.slug}/repos/`);
      expect((await listingLink.getAttribute('aria-label'))!).toContain('Browse the listing');
    }
  });

  test('every organization overview mirrors the profile page and agrees with the artifact', async ({ page }) => {
    const orgs = artifactOrgs();
    test.skip(orgs.length === 0, 'no organizations in this build');
    for (const org of orgs) {
      await page.goto(`/orgs/${org.slug}/`);
      await expect(page.getByRole('heading', { level: 1, name: org.name })).toBeVisible();
      await expect(page.locator('.hf-hero .hf-handle')).toHaveText(`@${org.slug}`);

      const repoKpi = page.locator('.hf-kpi', { hasText: 'Repositories' });
      await expect(repoKpi.locator('.hf-kpi-value')).toHaveText(String(org.repos.length));

      const shown = await cardSlugs(page);
      for (const slug of shown) expect(org.repos).toContain(slug);
      if (org.repos.length === 0) {
        // An org with no members explains how to add one instead of rendering an empty grid.
        await expect(page.locator('.hf-empty')).toBeVisible();
        expect(shown).toEqual([]);
      } else {
        expect(shown.length).toBeGreaterThan(0);
        await expect(page.getByRole('link', { name: /View all \d+ repositor/ })).toHaveAttribute('href', `/orgs/${org.slug}/repos/`);
      }
    }
  });

  test('every scoped listing shows exactly the member repos and links back to the org', async ({ page }) => {
    const orgs = artifactOrgs();
    test.skip(orgs.length === 0, 'no organizations in this build');
    for (const org of orgs) {
      await page.goto(`/orgs/${org.slug}/repos/`);
      await expect(page.getByRole('heading', { level: 1, name: `${org.name} repositories` })).toBeVisible();
      await expect(page.locator('.hf-org-back')).toHaveAttribute('href', `/orgs/${org.slug}/`);
      expect((await cardSlugs(page)).sort()).toEqual([...org.repos].sort());
    }
  });

  test('the scoped listing keeps the Phase 2 filters working', async ({ page }) => {
    const org = artifactOrgs().find((o) => o.repos.length > 0);
    test.skip(!org, 'no organization has members in this build');
    await page.goto(`/orgs/${org!.slug}/repos/`);
    const cards = page.locator('.hf-repo-card');
    await expect(cards).toHaveCount(org!.repos.length);

    await waitForIslands(page); // typing into the SSR'd input is a no-op until it hydrates
    await page.getByRole('searchbox', { name: /search repositories/i }).fill('zzz-no-such-repo');
    await expect(cards).toHaveCount(0);
    await expect(page.locator('.hf-empty')).toContainText('Nothing matches');
    // exact: the empty state also offers a "Clear filters" button, whose name contains this one.
    await page.getByRole('button', { name: 'Clear', exact: true }).click();
    await expect(cards).toHaveCount(org!.repos.length);
  });

  test('the scoped listing never links out of the organization', async ({ page }) => {
    const org = artifactOrgs().find((o) => o.repos.length > 0);
    test.skip(!org, 'no organization has members in this build');
    const base = `/orgs/${org!.slug}/repos/`;
    await page.goto(base);

    // Tag chips used to be hardcoded to the site-wide listing, which silently dropped a
    // visitor out of the org they were browsing.
    const chips = page.locator('.hf-repo-card .hf-tag[href]');
    const hrefs = await chips.evaluateAll((els) => els.map((e) => e.getAttribute('href') ?? ''));
    test.skip(hrefs.length === 0, 'no member repo carries a tag in this build');
    for (const href of hrefs) expect(href.startsWith(`${base}?tag=`)).toBe(true);

    // and clicking one keeps the scope
    await chips.first().click();
    await expect(page).toHaveURL(new RegExp(`${base}\\?tag=`));
    await expect(page.getByRole('heading', { level: 1, name: `${org!.name} repositories` })).toBeVisible();
    for (const slug of await cardSlugs(page)) expect(org!.repos).toContain(slug);
  });

  test('an org page renders the body and links from content/orgs/canadian-coding.md', async ({ page }) => {
    test.skip(!artifactOrgs().some((o) => o.slug === 'canadian-coding'), 'canadian-coding is not configured in this build');
    await page.goto('/orgs/canadian-coding/');
    // body of the markdown file, rendered through the content collection
    await expect(page.locator('.hf-readme .hf-md')).toBeVisible();
    await expect(page.locator('.hf-readme .hf-md')).not.toBeEmpty();
    // frontmatter: sites[] as pills, links{} including a mailto:
    await expect(page.locator('.hf-hero-links a', { hasText: 'canadiancoding.ca' })).toHaveAttribute('href', 'https://canadiancoding.ca');
    await expect(page.locator('.hf-hero-links a', { hasText: 'Email' })).toHaveAttribute('href', /^mailto:/);
    // frontmatter description feeds the hero blurb
    await expect(page.locator('.hf-hero-bio')).not.toBeEmpty();
  });
});
