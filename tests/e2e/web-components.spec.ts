import { expect, test } from '@playwright/test';

/**
 * The web components (0.4.0), and specifically the seam they created.
 *
 * A repo card now has two producers: `RepoCard.astro` renders it on the server, and
 * `<hf-repo-listing>` rebuilds it in the browser by cloning the `<template>` that
 * `RepoListing.astro` emits. Nothing else in the suite compares those two against each
 * other — every existing assertion happens to run against whichever one the page put there —
 * so a class, an href or a whole element could quietly go missing from the client half and
 * every other test would still pass.
 *
 * These tests do the comparison directly, plus the two no-JS guarantees the phase promises.
 */

/**
 * A structural signature of an element tree: tag, class, and the attributes that carry
 * meaning, depth-first. Deliberately NOT `outerHTML` — attribute order and the exact
 * serialization of `style` differ between an HTML parser and `element.style.background`,
 * and neither difference is one a reader would ever see.
 */
const SIGNATURE = `(root) => {
  const walk = (el, depth) => {
    const attrs = ['href', 'title', 'data-slug', 'aria-pressed']
      .map((a) => (el.hasAttribute(a) ? a + '=' + el.getAttribute(a) : null))
      .filter(Boolean)
      .join(' ');
    const own = Array.from(el.childNodes)
      .filter((n) => n.nodeType === 3)
      .map((n) => n.textContent.replace(/\\s+/g, ' ').trim())
      .filter(Boolean)
      .join(' ');
    const line = '  '.repeat(depth) + el.tagName.toLowerCase() +
      (el.className && typeof el.className === 'string' ? '.' + el.className.trim().split(/\\s+/).join('.') : '') +
      (attrs ? ' [' + attrs + ']' : '') + (own ? ' "' + own + '"' : '');
    return [line, ...Array.from(el.children).flatMap((c) => walk(c, depth + 1))];
  };
  return walk(root, 0).join('\\n');
}`;

test.describe('repo card: server render vs client render', () => {
  test('the client rebuilds a card identically to the server', async ({ page }) => {
    await page.goto('/repos/');

    const card = page.locator('.hf-repo-card[data-slug="alpha"]');
    await expect(card).toBeVisible();
    const server = await card.evaluate(new Function('return ' + SIGNATURE)() as (el: Element) => string);

    // Force the element to re-render that same card from its <template>: filter everything
    // out, then clear. The card that comes back is built entirely by the browser.
    await page.getByRole('searchbox', { name: /search repositories/i }).fill('zzz-no-such-repo');
    await expect(page.locator('.hf-repo-card')).toHaveCount(0);
    await page.getByRole('button', { name: 'Clear', exact: true }).click();
    await expect(card).toBeVisible();

    const client = await card.evaluate(new Function('return ' + SIGNATURE)() as (el: Element) => string);
    expect(client, 'client-built card differs from the server-rendered one').toBe(server);
  });

  test('a template repo keeps its badge when the client rebuilds it', async ({ page }) => {
    await page.goto('/repos/');
    const badge = page.locator('.hf-repo-card .hf-tag--template');
    const before = await badge.count();
    expect(before).toBeGreaterThan(0);

    await page.getByRole('button', { name: /^Templates/ }).click();
    await expect(page.locator('.hf-repo-card')).toHaveCount(before);
    // The badge is removed from the clone for non-template repos, so it has to survive here.
    await expect(page.locator('.hf-repo-card .hf-tag--template')).toHaveCount(before);
  });
});

test.describe('without JavaScript', () => {
  test.use({ javaScriptEnabled: false });

  test('the palette is absent rather than broken, and the search control still navigates', async ({ page }) => {
    await page.goto('/repos/');
    // The custom element is in the markup but never upgrades: it must contribute nothing.
    const box = await page.locator('hf-command-palette').boundingBox();
    expect(box === null || box.height === 0, 'un-upgraded palette element takes up space').toBeTruthy();
    await expect(page.locator('.hf-palette')).toHaveCount(0);

    // The sidebar control is a real link first and a palette trigger second, so with no JS
    // it still takes a reader somewhere useful.
    await page.locator('#hf-search-open').click();
    await expect(page).toHaveURL(/\/repos\/$/);
    await expect(page.locator('.hf-repo-card').first()).toBeVisible();
  });

  test('the listing renders its chrome without the element upgrading', async ({ page }) => {
    await page.goto('/repos/');
    await expect(page.getByRole('searchbox', { name: /search repositories/i })).toBeVisible();
    await expect(page.getByRole('combobox', { name: /sort repositories/i })).toBeVisible();
    // State blocks the element toggles must start hidden, or a populated listing shows an
    // empty state under it. `[hidden]` only wins because global.css forces it to.
    await expect(page.locator('[data-hf-empty]')).toBeHidden();
    await expect(page.locator('.hf-listing-toolbar [data-hf-clear]')).toBeHidden();
  });
});

test('no framework runtime or bundled diagram library ships', async ({ page }) => {
  const scripts: string[] = [];
  page.on('request', (r) => {
    if (r.resourceType() === 'script') scripts.push(new URL(r.url()).pathname);
  });
  await page.goto('/repos/');
  await page.waitForLoadState('networkidle');

  expect(scripts.filter((s) => s.includes('/_astro/')), 'Astro bundled a script').toEqual([]);
  expect(scripts.filter((s) => s.includes('mermaid')), 'mermaid loaded on a diagram-free page').toEqual([]);
  // Everything that does load is a plain file we wrote, served verbatim from web/.
  for (const s of scripts) expect(s).toMatch(/\/js\/[a-z-]+\.js$/);
});
