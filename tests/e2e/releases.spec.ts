/**
 * Phase 5 e2e: the releases UI against BOTH origins.
 *
 * The provider groups are data-driven: they read the built artifact and assert against
 * whichever repo carries provider-imported releases (`charlie`) and whichever provider repo
 * has published none (`delta`) — see tests/e2e/global-setup.ts. They skip themselves if the
 * fixture ever loses those repos rather than failing with a confusing selector error.
 */
import fs from 'node:fs';
import path from 'node:path';
import { expect, test } from '@playwright/test';
import type { Page } from '@playwright/test';

/* ---- fixture introspection ------------------------------------------------ */

interface FixtureAsset { name: string; url: string; size: number; contentType: string | null }
interface FixtureRelease { tag: string; name: string; body: string; url: string | null; prerelease: boolean; assets: FixtureAsset[] }
interface FixtureRepo {
  slug: string;
  source: { type: string };
  releaseMode: string;
  releases: FixtureRelease[];
  empty: boolean;
  gitTags: Array<{ annotated: boolean }>;
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
/** First fixture repo whose releases came from a provider API rather than from git tags. */
const providerRepo = repos.find((r) => !r.empty && Array.isArray(r.releases) && r.releases.length > 0) ?? null;

const PROVIDER_LABELS: Record<string, string> = { github: 'GitHub', gitlab: 'GitLab', gitea: 'Gitea', forgejo: 'Forgejo' };

/** Cards on the releases index, in document order. */
const cardsOf = (page: Page) => page.locator('.hf-release-list > li > .hf-release-card');

/* ---- tag-derived releases (what the fixture has today) -------------------- */

test.describe('releases from annotated tags', () => {
  test('index lists both tags newest first, as an ordered list, with a Latest badge', async ({ page }) => {
    await page.goto('/repos/alpha/releases/');

    // real list semantics, labelled for assistive tech
    const list = page.locator('.hf-release-list');
    await expect(list).toHaveAttribute('aria-label', /Releases of .*newest first/);
    expect(await list.evaluate((el) => el.tagName)).toBe('OL');

    const cards = cardsOf(page);
    await expect(cards).toHaveCount(2);
    await expect(cards.nth(0).locator('.hf-release-head h2')).toHaveText('v1.1.0');
    await expect(cards.nth(1).locator('.hf-release-head h2')).toHaveText('v1.0.0');

    // "Latest" only on the newest non-prerelease, never twice
    await expect(cards.nth(0).locator('.hf-release-latest')).toHaveText('Latest');
    await expect(page.locator('.hf-release-latest')).toHaveCount(1);
    await expect(page.locator('.hf-release-pre')).toHaveCount(0);

    // origin is spelled out on every card
    await expect(cards.nth(0).locator('.hf-release-origin')).toHaveText(/source: annotated tag/);
  });

  test('index summarises the notes as plain text, not rendered markdown', async ({ page }) => {
    await page.goto('/repos/alpha/releases/');
    const summary = cardsOf(page).nth(0).locator('.hf-release-summary');
    // the v1.1.0 tag message is "Second release / ## Highlights / - adds a *guide* ..."
    await expect(summary).toContainText('Second release');
    await expect(summary).toContainText('Highlights');
    await expect(summary).toContainText('adds a guide');
    const text = (await summary.textContent()) ?? '';
    expect(text).not.toContain('#');
    expect(text).not.toContain('*');
    expect(text.length).toBeLessThanOrEqual(201); // 200 chars + the ellipsis
    // markdown is rendered on the release page, not here
    await expect(cardsOf(page).nth(0).locator('.hf-release-notes')).toHaveCount(0);
  });

  test('a card links through to its release page', async ({ page }) => {
    await page.goto('/repos/alpha/releases/');
    await cardsOf(page).nth(1).locator('.hf-release-head h2 a').click();
    await expect(page).toHaveURL(/\/repos\/alpha\/releases\/v1\.0\.0\/$/);
    await expect(page.locator('.hf-release-head h1')).toHaveText('v1.0.0');
  });

  test('release page renders the tag message as markdown, with tagger, target and origin', async ({ page }) => {
    await page.goto('/repos/alpha/releases/v1.1.0/');
    await expect(page.locator('.hf-release-head h1')).toHaveText('v1.1.0');
    await expect(page.locator('.hf-release-latest')).toHaveText('Latest');
    await expect(page.locator('.hf-release-pre')).toHaveCount(0);

    // markdown, this time actually rendered
    // user markdown is demoted a level: `## Highlights` renders as an <h3>
    await expect(page.locator('.hf-release-notes h3')).toHaveText('Highlights');
    await expect(page.locator('.hf-release-notes em')).toHaveText('guide');
    await expect(page.locator('.hf-release-notes code')).toHaveText('extra');

    // tagger + target commit + browse link
    const meta = page.locator('.hf-release-meta');
    await expect(meta).toContainText('Fixture Author');
    const target = meta.locator('a[href^="/repos/alpha/commit/"]');
    await expect(target).toHaveCount(1);
    await expect(meta.locator('a', { hasText: 'Browse source' })).toHaveAttribute('href', '/repos/alpha/tree/v1.1.0/');

    // origin line, no outbound link for a tag-derived release
    const origin = page.locator('.hf-release-origin');
    await expect(origin).toHaveText(/source: annotated tag/);
    await expect(origin.locator('a')).toHaveCount(0);
  });

  test('downloads list offers the locally built source zip and it actually serves', async ({ page, request }) => {
    await page.goto('/repos/alpha/releases/v1.1.0/');
    await expect(page.locator('.hf-release-assets h2')).toHaveText('Downloads');

    const rows = page.locator('.hf-asset-list > li');
    await expect(rows).toHaveCount(1);
    const zip = rows.nth(0).locator('a.hf-asset');
    await expect(zip.locator('.hf-asset-name')).toHaveText('Source code (zip)');
    await expect(zip.locator('.hf-asset-size')).not.toHaveText('');

    const href = await zip.getAttribute('href');
    expect(href).toBe('/repos/alpha/archive/v1.1.0.zip');
    const res = await request.get(href!);
    expect(res.status()).toBe(200);
    expect(res.headers()['content-type']).toContain('application/zip');
    expect((await res.body()).byteLength).toBeGreaterThan(22); // more than an empty zip

    // a local archive is not an external link
    await expect(zip.locator('.hf-ext-mark')).toHaveCount(0);
  });

  test('tag-mode empty state teaches the git command', async ({ page }) => {
    await page.goto('/repos/bravo/releases/');
    const empty = page.locator('.hf-empty');
    await expect(empty).toContainText('No releases yet.');
    await expect(empty).toContainText('Releases are created from annotated tags');
    await expect(empty.locator('code')).toHaveText("git tag -a v1.0.0 -m '...'");
    // never the provider wording for a local repo
    await expect(empty).not.toContainText('No releases published on');
    await expect(page.locator('.hf-release-list')).toHaveCount(0);
  });

  test('release pages are keyboard reachable and focus is visible', async ({ page }) => {
    await page.goto('/repos/alpha/releases/v1.1.0/');
    const zip = page.locator('a.hf-asset').first();
    await zip.focus();
    const outline = await zip.evaluate((el) => getComputedStyle(el).outlineStyle);
    expect(outline).not.toBe('none');
  });
});

/* ---- provider-imported releases ------------------------------------------ */

test.describe('releases imported from a provider', () => {
  test.skip(
    providerRepo === null,
    'fixture has no repo with provider releases — see tests/e2e/releases.spec.ts header',
  );

  test('index shows the provider as the source and badges pre-releases', async ({ page }) => {
    const repo = providerRepo!;
    const label = PROVIDER_LABELS[repo.source.type] ?? 'provider';
    await page.goto(`/repos/${repo.slug}/releases/`);

    const cards = cardsOf(page);
    await expect(cards).toHaveCount(new Set(repo.releases.map((r) => r.tag)).size);
    await expect(cards.nth(0).locator('.hf-release-origin')).toHaveText(new RegExp(`source: ${label} release`));

    // exactly one Latest badge, and it is not on a pre-release
    const hasStable = repo.releases.some((r) => !r.prerelease);
    await expect(page.locator('.hf-release-latest')).toHaveCount(hasStable ? 1 : 0);
    if (hasStable) {
      const latestCard = cards.filter({ has: page.locator('.hf-release-latest') });
      await expect(latestCard.locator('.hf-release-pre')).toHaveCount(0);
    }
    const prereleases = repo.releases.filter((r) => r.prerelease).length;
    if (prereleases > 0) await expect(page.locator('.hf-release-pre').first()).toHaveText('Pre-release');
  });

  test('release page renders provider notes and lists assets as external downloads', async ({ page }) => {
    const repo = providerRepo!;
    const label = PROVIDER_LABELS[repo.source.type] ?? 'provider';
    const rel = repo.releases.find((r) => r.assets.length > 0) ?? repo.releases[0]!;
    await page.goto(`/repos/${repo.slug}/releases/${rel.tag.replace(/\//g, '~')}/`);

    const title = rel.name.trim() && rel.name !== rel.tag ? rel.name : rel.tag;
    await expect(page.locator('.hf-release-head h1')).toHaveText(title);
    if (title !== rel.tag) await expect(page.locator('.hf-release-tag')).toHaveText(rel.tag);
    await expect(page.locator('.hf-release-pre')).toHaveCount(rel.prerelease ? 1 : 0);

    const origin = page.locator('.hf-release-origin');
    await expect(origin).toHaveText(new RegExp(`source: ${label} release`));
    if (rel.url) await expect(origin.locator('a')).toHaveAttribute('href', rel.url);

    if (rel.body.trim()) await expect(page.locator('.hf-release-notes')).toBeVisible();

    for (const asset of rel.assets) {
      const row = page.locator('a.hf-asset', { hasText: asset.name });
      await expect(row).toHaveAttribute('href', asset.url);
      await expect(row).toHaveAttribute('rel', /noopener/);
      // leaving the static site must be signposted, visually and to a screen reader
      await expect(row.locator('.hf-ext-mark')).toHaveCount(1);
      await expect(row.locator('.hf-sr')).toHaveText('(external link)');
      if (asset.size > 0) await expect(row.locator('.hf-asset-size')).not.toHaveText('');
    }
    if (rel.assets.length === 0) await expect(page.locator('.hf-release-assets')).toContainText('No downloadable assets');
  });
});

/**
 * Release notes on an imported repo are written by whoever can publish on that forge. The
 * page emits them with `set:html`, so nothing in them may execute on this site's origin.
 */
const scriptedRelease =
  providerRepo?.releases.find((r) => r.body.includes('<script')) ?? null;

test.describe('imported release notes are not trusted', () => {
  test.skip(scriptedRelease === null, 'fixture has no release with HTML in its notes');

  test('script, event handlers and javascript: URLs never reach the page', async ({ page }) => {
    const repo = providerRepo!;
    const rel = scriptedRelease!;
    const pwned: string[] = [];
    page.on('pageerror', (e) => pwned.push(`pageerror: ${e.message}`));
    await page.goto(`/repos/${repo.slug}/releases/${rel.tag.replace(/\//g, '~')}/`);

    const notes = page.locator('.hf-release-notes');
    await expect(notes).toBeVisible();
    const html = (await notes.innerHTML()).toLowerCase();
    expect(html).not.toContain('<script');
    expect(html).not.toContain('onerror');
    expect(html).not.toContain('javascript:');
    // The legitimate part of the notes still renders.
    await expect(notes).toContainText('Not for production use');
    expect(await page.evaluate(() => (window as unknown as { __PWNED?: unknown }).__PWNED)).toBeUndefined();
    expect(pwned).toEqual([]);
  });
});

/**
 * Independent of the group above: a provider repo that has published nothing yet must not be
 * told to run `git tag -a`. Needs a remote-source fixture repo with an empty `releases[]`.
 */
const emptyProviderRepo =
  repos.find(
    (r) =>
      r.releaseMode === 'provider' &&
      r.releases.length === 0 &&
      r.source.type !== 'local' &&
      !r.empty &&
      // annotated tags would make `resolveReleases` fall back and render a list instead
      !r.gitTags.some((t) => t.annotated),
  ) ?? null;

test.describe('provider repo with no releases published', () => {
  test.skip(emptyProviderRepo === null, 'fixture has no provider repo with zero releases');

  test('empty state names the provider instead of teaching git tag', async ({ page }) => {
    const repo = emptyProviderRepo!;
    await page.goto(`/repos/${repo.slug}/releases/`);
    const box = page.locator('.hf-empty');
    await expect(box).toContainText(`No releases published on ${PROVIDER_LABELS[repo.source.type]}`);
    await expect(box).not.toContainText('git tag -a');
  });
});
