/**
 * The accessibility and responsive gate, run over EVERY page type in both themes.
 *
 * Why it is hand-rolled rather than axe-core: this project ships no dependency it does not
 * need, and the four rules that actually broke here are cheap to assert directly.
 *
 * Why it pins `data-theme` instead of trusting the browser: the light palette failed WCAG
 * contrast on all 35 page types for six phases and nothing noticed, because Lighthouse (via
 * chrome-launcher) inherits the host OS colour scheme — every audit ever run on this
 * dark-mode machine rendered the DARK palette. A gate that lets the machine choose the theme
 * is a gate that tests half the site. Both themes are forced, explicitly, here.
 *
 * The complementary half lives in `tests/unit/contrast.test.ts`, which reads the token values
 * straight out of the stylesheet and needs no browser at all.
 */
import fs from 'node:fs';
import path from 'node:path';
import { expect, test, type Page } from '@playwright/test';

/* ---- the page inventory, derived from the built fixture -------------------- */

interface FixtureTreeEntry { path: string; type: string }
interface FixtureRepo {
  slug: string;
  empty: boolean;
  defaultBranch: string | null;
  branches: Array<{ name: string; commits: string[] }>;
  gitTags: Array<{ name: string; annotated: boolean; message: string | null }>;
  releases: Array<{ tag: string; body: string }>;
  tree: FixtureTreeEntry[];
  files: Record<string, { binary: boolean; stored: boolean }>;
  insights: unknown | null;
}
interface FixtureNote { slug: string; files: Array<{ path: string }> }
interface FixtureOrg { slug: string }

const ARTIFACT = path.resolve(import.meta.dirname, '..', '.tmp', 'e2e', 'data', 'forge.json');
const artifact = JSON.parse(fs.readFileSync(ARTIFACT, 'utf8')) as {
  repos: FixtureRepo[];
  notes: FixtureNote[];
  organizations: FixtureOrg[];
};

/** `feat/x` → `feat~x`, matching `refSlug()` in src/lib/routes.ts. */
const refSlug = (ref: string) => ref.replaceAll('/', '~');
const seg = (p: string) => p.split('/').map(encodeURIComponent).join('/');

/**
 * One URL per PAGE TYPE, not one per page: the point is coverage of distinct templates and
 * distinct content shapes (a markdown blob and a binary blob exercise different branches of
 * the same template), not a crawl of 3,000 routes.
 */
function pageInventory(): Array<{ name: string; url: string }> {
  const pages: Array<{ name: string; url: string }> = [
    { name: 'profile', url: '/' },
    { name: 'repo-listing', url: '/repos/' },
    { name: 'notes-index', url: '/notes/' },
    { name: 'orgs-index', url: '/orgs/' },
  ];

  const withCommits = artifact.repos.filter((r) => !r.empty && r.defaultBranch);
  const rich = withCommits.find((r) => r.tree.some((e) => /\.(ts|js|go|css)$/.test(e.path))) ?? withCommits[0]!;
  const empty = artifact.repos.find((r) => r.empty);

  pages.push({ name: 'repo-overview', url: `/repos/${rich.slug}/` });
  if (empty) pages.push({ name: 'repo-overview (empty repo)', url: `/repos/${empty.slug}/` });

  const branch = rich.defaultBranch!;
  const ref = refSlug(branch);
  pages.push(
    { name: 'branches', url: `/repos/${rich.slug}/branches/` },
    { name: 'tags', url: `/repos/${rich.slug}/tags/` },
    { name: 'commits', url: `/repos/${rich.slug}/commits/${ref}/` },
    { name: 'releases-index', url: `/repos/${rich.slug}/releases/` },
    { name: 'tree (root)', url: `/repos/${rich.slug}/tree/${ref}/` },
  );

  const head = rich.branches.find((b) => b.name === branch)?.commits[0];
  if (head) pages.push({ name: 'single commit', url: `/repos/${rich.slug}/commit/${head}/` });
  if (rich.insights) pages.push({ name: 'insights', url: `/repos/${rich.slug}/insights/` });

  const dir = rich.tree.find((e) => e.type === 'tree' && !e.path.includes('/'));
  if (dir) pages.push({ name: 'tree (subdir)', url: `/repos/${rich.slug}/tree/${ref}/${seg(dir.path)}/` });

  const blobOf = (match: (p: string) => boolean) =>
    rich.tree.find((e) => e.type === 'blob' && match(e.path) && rich.files[e.path]?.stored)?.path;
  const code = blobOf((p) => /\.(ts|js|go|css)$/.test(p));
  const md = blobOf((p) => /\.md$/i.test(p));
  const bin = rich.tree.find((e) => e.type === 'blob' && rich.files[e.path]?.binary)?.path;
  if (code) pages.push({ name: 'blob (code)', url: `/repos/${rich.slug}/blob/${ref}/${seg(code)}/` });
  if (md) pages.push({ name: 'blob (markdown)', url: `/repos/${rich.slug}/blob/${ref}/${seg(md)}/` });
  if (bin) pages.push({ name: 'blob (binary)', url: `/repos/${rich.slug}/blob/${ref}/${seg(bin)}/` });

  // Prefer a release whose NOTES contain markdown headings: release bodies routinely open at
  // `##`, which renders as an h3 and used to step straight from the page's h1.
  const releases = [
    ...artifact.repos.flatMap((r) =>
      r.gitTags.filter((t) => t.annotated).map((t) => ({ slug: r.slug, tag: t.name, body: t.message ?? '' })),
    ),
    ...artifact.repos.flatMap((r) => r.releases.map((rel) => ({ slug: r.slug, tag: rel.tag, body: rel.body ?? '' }))),
  ];
  const release = releases.find((r) => /^#{1,3} /m.test(r.body)) ?? releases[0];
  if (release) pages.push({ name: 'release', url: `/repos/${release.slug}/releases/${seg(release.tag)}/` });

  const multiFileNote = artifact.notes.find((n) => n.files.length > 1);
  const singleNote = artifact.notes.find((n) => n.files.length === 1);
  if (singleNote) pages.push({ name: 'note', url: `/notes/${singleNote.slug}/` });
  if (multiFileNote) pages.push({ name: 'note (multi-file)', url: `/notes/${multiFileNote.slug}/` });

  const org = artifact.organizations[0];
  if (org) {
    pages.push({ name: 'org', url: `/orgs/${org.slug}/` }, { name: 'org repos', url: `/orgs/${org.slug}/repos/` });
  }
  return pages;
}

const PAGES = pageInventory();
const THEMES = ['light', 'dark'] as const;

/**
 * Load a page with the theme forced, both ways.
 *
 * `colorScheme` sets `prefers-color-scheme` and the `data-theme` attribute overrides it, so
 * setting both means the page renders that palette whatever the machine, the OS, or a
 * previously-stored preference says.
 */
async function open(page: Page, url: string, theme: (typeof THEMES)[number]): Promise<void> {
  await page.emulateMedia({ colorScheme: theme });
  await page.addInitScript((t) => {
    document.documentElement.setAttribute('data-theme', t as string);
    // the theme script runs on DOMContentLoaded and may re-read localStorage
    try { localStorage.setItem('frznforge-theme', t as string); } catch { /* ignore */ }
  }, theme);
  await page.goto(url);
  await page.evaluate((t) => document.documentElement.setAttribute('data-theme', t as string), theme);
}

/* ---- in-page probes -------------------------------------------------------
 * These run in the browser, so they see the CASCADE, not the stylesheet: composited
 * translucent backgrounds, inherited colours, `opacity`, the lot.
 */

/** WCAG 1.4.3 AA over every visible text node. Returns the failures, worst first. */
const CONTRAST_PROBE = `(() => {
  const parse = (value) => {
    const m = /rgba?\\(([^)]+)\\)/.exec(value);
    if (!m) return null;
    const [r, g, b, a] = m[1].split(/[,\\s\\/]+/).map(Number);
    return { r, g, b, a: a === undefined ? 1 : a };
  };
  const flatten = (fg, bg) => ({
    r: fg.a * fg.r + (1 - fg.a) * bg.r,
    g: fg.a * fg.g + (1 - fg.a) * bg.g,
    b: fg.a * fg.b + (1 - fg.a) * bg.b,
    a: 1,
  });
  const lum = ({ r, g, b }) => {
    const c = (v) => { const s = v / 255; return s <= 0.04045 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4); };
    return 0.2126 * c(r) + 0.7152 * c(g) + 0.0722 * c(b);
  };
  const ratio = (x, y) => {
    const [a, b] = [lum(x), lum(y)];
    return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
  };
  /** The colour actually painted behind an element: walk up, compositing as we go. */
  const backgroundOf = (el) => {
    const layers = [];
    for (let cur = el; cur; cur = cur.parentElement) {
      const style = getComputedStyle(cur);
      if (style.backgroundImage !== 'none') return null; // gradient or image: not measurable
      const bg = parse(style.backgroundColor);
      if (!bg || bg.a === 0) continue;
      layers.push(bg);
      if (bg.a >= 1) break;
    }
    let base = { r: 255, g: 255, b: 255, a: 1 };
    for (let i = layers.length - 1; i >= 0; i--) base = flatten(layers[i], base);
    return base;
  };
  /** Product of every opacity from the element up to the root. */
  const effectiveOpacity = (el) => {
    let o = 1;
    for (let cur = el; cur; cur = cur.parentElement) o *= Number(getComputedStyle(cur).opacity || '1');
    return o;
  };

  const failures = [];
  for (const el of document.querySelectorAll('body *')) {
    if (el.closest('[aria-hidden="true"], .hf-sr, svg, script, style')) continue;
    // only elements that hold text of their own
    const own = Array.from(el.childNodes).some((n) => n.nodeType === 3 && n.textContent.trim().length > 0);
    if (!own) continue;
    const rect = el.getBoundingClientRect();
    if (rect.width === 0 || rect.height === 0) continue;
    const style = getComputedStyle(el);
    if (style.visibility === 'hidden' || style.display === 'none') continue;

    const bg = backgroundOf(el);
    if (bg === null) continue;
    const colour = parse(style.color);
    if (!colour) continue;
    const opacity = effectiveOpacity(el);
    if (opacity === 0) continue;
    const fg = flatten({ ...colour, a: colour.a * opacity }, bg);

    const size = Number.parseFloat(style.fontSize);
    const weight = Number(style.fontWeight) || 400;
    // WCAG "large text": >=24px, or >=18.66px at 700+
    const large = size >= 24 || (size >= 18.66 && weight >= 700);
    const need = large ? 3 : 4.5;
    const got = ratio(fg, bg);
    if (got + 0.005 < need) {
      failures.push({
        selector: el.tagName.toLowerCase() + (el.className && typeof el.className === 'string' ? '.' + el.className.trim().split(/\\s+/).join('.') : ''),
        text: (el.textContent || '').trim().slice(0, 40),
        got: Number(got.toFixed(2)),
        need,
        fg: style.color,
        size,
      });
    }
  }
  failures.sort((a, b) => a.got - b.got);
  return failures.slice(0, 12);
})()`;

/** Heading levels in document order, e.g. ['h1', 'h2', 'h2', 'h3']. */
const HEADINGS_PROBE = `(() => Array.from(document.querySelectorAll('h1,h2,h3,h4,h5,h6'))
  .filter((h) => !h.closest('[aria-hidden="true"]'))
  .map((h) => ({ level: Number(h.tagName.slice(1)), text: (h.textContent || '').trim().slice(0, 40) })))()`;

/** Every id that appears more than once. */
const DUPLICATE_IDS_PROBE = `(() => {
  const seen = new Map();
  for (const el of document.querySelectorAll('[id]')) seen.set(el.id, (seen.get(el.id) || 0) + 1);
  return Array.from(seen).filter(([, n]) => n > 1).map(([id, n]) => id + ' x' + n);
})()`;

/** Scrollable boxes that no keyboard user can reach (axe `scrollable-region-focusable`). */
const UNFOCUSABLE_SCROLLERS_PROBE = `(() => {
  const out = [];
  for (const el of document.querySelectorAll('body *')) {
    const style = getComputedStyle(el);
    const scrolls =
      (/(auto|scroll)/.test(style.overflowX) && el.scrollWidth > el.clientWidth + 1) ||
      (/(auto|scroll)/.test(style.overflowY) && el.scrollHeight > el.clientHeight + 1);
    if (!scrolls) continue;
    if (el.tabIndex >= 0) continue;
    if (el.closest('[tabindex]')) continue;
    if (el.querySelector('a[href], button, input, select, textarea, [tabindex]')) continue;
    out.push(el.tagName.toLowerCase() + (typeof el.className === 'string' && el.className ? '.' + el.className.trim().split(/\\s+/)[0] : ''));
  }
  return out;
})()`;

/* ---- the gate -------------------------------------------------------------- */

for (const theme of THEMES) {
  test.describe(`${theme} theme`, () => {
    for (const { name, url } of PAGES) {
      test(`${name} has no contrast, heading or id defects`, async ({ page }) => {
        await page.setViewportSize({ width: 1400, height: 1000 });
        await open(page, url, theme);

        // 1. WCAG 1.4.3 — the failure that shipped on every page for six phases.
        const contrast = await page.evaluate(CONTRAST_PROBE);
        expect(contrast, `${url} (${theme}) contrast failures`).toEqual([]);

        // 2. Exactly one h1. Thirteen page types used to have none — the repo name was a
        //    <strong> in a breadcrumb — and the profile had two.
        const headings = (await page.evaluate(HEADINGS_PROBE)) as Array<{ level: number; text: string }>;
        const h1s = headings.filter((h) => h.level === 1);
        expect(h1s.map((h) => h.text), `${url} (${theme}) <h1> count`).toHaveLength(1);

        // 3. heading-order: never skip a level going down.
        const skips = headings
          .map((h, i) => ({ from: headings[i - 1], to: h }))
          .filter((step) => step.from && step.to.level > step.from.level + 1)
          .map((step) => `h${step.from!.level} "${step.from!.text}" -> h${step.to.level} "${step.to.text}"`);
        expect(skips, `${url} (${theme}) heading-order`).toEqual([]);

        // 4. Duplicate ids — a multi-file note used to emit id="L1" once per file, so the
        //    documented #L12 anchors could only ever reach the first one.
        expect(await page.evaluate(DUPLICATE_IDS_PROBE), `${url} (${theme}) duplicate ids`).toEqual([]);

        // 5. Keyboard-reachable scrollers (Firefox and Safari do not focus them for you).
        expect(
          await page.evaluate(UNFOCUSABLE_SCROLLERS_PROBE),
          `${url} (${theme}) unreachable scroll regions`,
        ).toEqual([]);
      });

      test(`${name} does not scroll sideways on a phone`, async ({ page }) => {
        // 380px: a stock iPhone/Android viewport. The repo overview's commit bar used to push
        // the document to 433px here, so the whole page scrolled horizontally.
        await page.setViewportSize({ width: 380, height: 780 });
        await open(page, url, theme);
        const overflow = await page.evaluate(() => ({
          scrollWidth: document.documentElement.scrollWidth,
          clientWidth: document.documentElement.clientWidth,
          culprits: Array.from(document.querySelectorAll('body *'))
            .filter((el) => {
              const r = el.getBoundingClientRect();
              return r.width > 0 && r.right > document.documentElement.clientWidth + 1 && !el.closest('[style*="overflow"], .hf-code, .hf-chart-scroll, .hf-contrib-scroll');
            })
            .slice(0, 5)
            .map((el) => el.tagName.toLowerCase() + (typeof el.className === 'string' && el.className ? '.' + el.className.trim().split(/\s+/)[0] : '')),
        }));
        expect(
          overflow.scrollWidth,
          `${url} (${theme}) overflows by ${overflow.scrollWidth - overflow.clientWidth}px — ${overflow.culprits.join(', ')}`,
        ).toBeLessThanOrEqual(overflow.clientWidth);
      });
    }
  });
}

/**
 * Closing the palette has to hand focus back to whatever opened it. It used to drop focus on
 * `<body>`, so a keyboard user forty tab stops into a commits page who opened the palette and
 * changed their mind restarted from the top of the document (WCAG 2.4.3).
 */
test('the command palette returns focus to the control that opened it', async ({ page }) => {
  await page.goto('/repos/');
  await page.waitForFunction(() => Array.from(document.querySelectorAll('astro-island')).every((el) => !el.hasAttribute('ssr')));

  await page.locator('.hf-search').first().focus();
  const before = await page.evaluate(() => document.activeElement?.className ?? '');
  expect(before).toContain('hf-search');

  await page.keyboard.press('Control+K');
  await expect(page.locator('.hf-palette')).toBeVisible();
  // focus moves after a tick, once the dialog is in the DOM
  await expect.poll(() => page.evaluate(() => document.activeElement?.tagName)).toBe('INPUT');

  await page.keyboard.press('Escape');
  await expect(page.locator('.hf-palette')).toHaveCount(0);
  await expect
    .poll(() => page.evaluate(() => document.activeElement?.className ?? ''))
    .toContain('hf-search');
});

/**
 * `ingest.branchTrees` caps how many branches get a file browser, so the branches page must
 * not link a branch whose tree page was never generated.
 */
test('every branch link on /branches/ resolves', async ({ page }) => {
  const repo = artifact.repos.find((r) => !r.empty && r.branches.length > 0)!;
  await page.goto(`/repos/${repo.slug}/branches/`);
  const hrefs = await page.locator('.hf-reflist a[href*="/tree/"]').evaluateAll((els) =>
    els.map((el) => (el as HTMLAnchorElement).getAttribute('href')!),
  );
  expect(hrefs.length).toBeGreaterThan(0);
  for (const href of hrefs) {
    const res = await page.request.get(href);
    expect(res.status(), `${href} is linked from /branches/ but was never built`).toBe(200);
  }
});
