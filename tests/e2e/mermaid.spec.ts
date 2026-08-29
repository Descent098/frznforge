/**
 * ```mermaid fences (0.2.0). The build emits the diagram SOURCE in an `hf-mermaid`
 * container (the no-JS fallback is an honest code block); `MermaidRenderer.astro` lazily
 * loads a locally bundled mermaid and swaps in the SVG — only on pages that actually hold
 * a diagram, only once one nears the viewport. Imported (untrusted) content never renders:
 * fixture charlie's release body carries a fence next to its XSS payloads for exactly that
 * assertion.
 */
import { expect, test } from '@playwright/test';

test.describe('mermaid diagrams', () => {
  test('trusted fences render as SVGs, lazily, with unique ids, and re-render on theme toggle', async ({ page }) => {
    await page.goto('/repos/alpha/');
    const containers = page.locator('pre.hf-mermaid');
    await expect(containers).toHaveCount(2);
    // no-JS fallback shape: the source ships as a code block
    await expect(containers.first().locator('code.language-mermaid, svg')).toHaveCount(1);

    await containers.first().scrollIntoViewIfNeeded();
    const rendered = page.locator('pre.hf-mermaid--rendered svg');
    await expect(rendered).toHaveCount(2);
    await expect(page.locator('pre.hf-mermaid--rendered').first()).toHaveAttribute('role', 'img');

    // ids are seeded per container, so two diagrams on one page never collide
    const ids = await page.locator('pre.hf-mermaid svg').evaluateAll((els) => els.map((e) => e.id));
    expect(new Set(ids).size).toBe(ids.length);

    // the theme toggle re-renders the SVG with the other mermaid theme
    const before = await rendered.first().evaluate((e) => e.outerHTML);
    await page.getByRole('button', { name: /^Theme/ }).click();
    await expect
      .poll(async () => rendered.first().evaluate((e) => e.outerHTML))
      .not.toBe(before);
    await expect(rendered).toHaveCount(2);
  });

  test('imported content keeps the plain code block, always', async ({ page }) => {
    await page.goto('/repos/charlie/releases/v2.2.0-rc.1/');
    await expect(page.locator('pre.hf-mermaid')).toHaveCount(0);
    await expect(page.locator('.hf-release-notes code.language-mermaid')).toHaveCount(1);
    await expect(page.locator('.hf-release-notes svg')).toHaveCount(0);
  });

  test('an imported README cannot execute or render diagrams either', async ({ page }) => {
    // Guards the page-level `trusted: isTrustedSource(repo.source)` flags on the repo
    // overview and the blob markdown preview — deleting either would pass every unit test
    // (they cover the library, not its call sites), so the fixture README carries the same
    // payloads as the release body.
    await page.goto('/repos/charlie/');
    expect(await page.evaluate(() => (window as unknown as { __PWNED?: number }).__PWNED)).toBeUndefined();
    await expect(page.locator('pre.hf-mermaid')).toHaveCount(0);
    await expect(page.locator('.hf-md code.language-mermaid')).toHaveCount(1);

    await page.goto('/repos/charlie/blob/main/README.md/');
    expect(await page.evaluate(() => (window as unknown as { __PWNED?: number }).__PWNED)).toBeUndefined();
    await expect(page.locator('pre.hf-mermaid')).toHaveCount(0);
  });

  test('diagram-free pages load none of the mermaid code', async ({ page }) => {
    const mermaidRequests: string[] = [];
    page.on('request', (r) => {
      if (/mermaid/i.test(r.url())) mermaidRequests.push(r.url());
    });
    await page.goto('/repos/bravo/');
    await page.waitForLoadState('networkidle');
    expect(mermaidRequests).toEqual([]);
    await expect(page.locator('pre.hf-mermaid')).toHaveCount(0);
  });
});
