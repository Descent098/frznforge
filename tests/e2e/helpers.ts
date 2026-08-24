import type { Page } from '@playwright/test';

/**
 * Wait until every Astro island on the page has hydrated.
 *
 * Islands are server-rendered first, so their markup is present and clickable before any
 * handler exists: a `fill()` on the listing's search box or a `click()` on the palette
 * trigger can land on inert HTML and be silently lost. Astro's client runtime drops the
 * `ssr` attribute from `<astro-island>` once a component is hydrated, which is the signal
 * this waits on. Cheap when there are no islands (the predicate is immediately true).
 *
 * Only needed before the FIRST interaction with an island on a freshly loaded page.
 */
export async function waitForIslands(page: Page, timeout = 10_000): Promise<void> {
  await page.waitForFunction(
    () => Array.from(document.querySelectorAll('astro-island')).every((el) => !el.hasAttribute('ssr')),
    undefined,
    { timeout },
  );
}
