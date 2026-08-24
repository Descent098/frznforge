/**
 * Notes UI (phase 6) end-to-end.
 *
 * The e2e build spreads the real `frznforge.config.ts` (see tests/e2e/global-setup.ts), so
 * `notes.dir` resolves to the repo's own `content/notes/` — these assertions run against the
 * actual shipped notes, not a fixture. Expected artifact order (date desc, nulls last, then
 * title asc) is: deploying-a-frozen-forge, heat-buckets, git-plumbing-cheatsheet,
 * check-determinism, static-host-configs.
 */
import fs from 'node:fs';
import path from 'node:path';
import { expect, test, type Page } from '@playwright/test';

const ROOT = path.resolve(import.meta.dirname, '..', '..');
const NOTES_DIR = path.join(ROOT, 'content', 'notes');

/** Slug → the title the collector must produce (frontmatter, else H1, else humanised name). */
const EXPECTED = [
  { slug: 'deploying-a-frozen-forge', title: 'Deploying a frozen forge' },
  { slug: 'heat-buckets', title: 'Heat buckets' },
  { slug: 'git-plumbing-cheatsheet', title: 'Git plumbing' },
  { slug: 'check-determinism', title: 'Check determinism' },
  { slug: 'static-host-configs', title: 'Static host configs' },
];

/** Every heading/label/anchor a11y expectation the notes pages must keep meeting. */
async function expectBasicA11y(page: Page) {
  await expect(page.locator('h1')).toHaveCount(1);
  for (const img of await page.locator('img').all()) {
    // decorative images must be explicitly empty-alt, informative ones must say something
    expect(await img.getAttribute('alt')).not.toBeNull();
  }
  // every in-page anchor resolves to an element that exists
  for (const link of await page.locator('a[href^="#"]').all()) {
    const href = (await link.getAttribute('href'))!;
    if (href === '#') continue;
    await expect(page.locator(href)).toHaveCount(1);
  }
}

test.describe('notes index', () => {
  test('lists every note, newest first, with tags, kind and file names', async ({ page }) => {
    await page.goto('/notes/');
    await expect(page.locator('h1')).toHaveText('Notes');

    const cards = page.locator('.hf-note-card');
    await expect(cards).toHaveCount(EXPECTED.length);

    // one card per note, in artifact order, each linking to its own page
    for (const [i, note] of EXPECTED.entries()) {
      const card = cards.nth(i);
      await expect(card.locator('.hf-note-card-title')).toContainText(note.title);
      await expect(card.locator('.hf-note-card-title a')).toHaveAttribute('href', `/notes/${note.slug}/`);
    }

    // tags render as labels (the markdown notes all carry frontmatter tags)
    await expect(page.locator('.hf-note-card', { hasText: 'Heat buckets' }).locator('.hf-tag')).toContainText(['design']);

    // the folder note names its files and says how many there are
    const folder = page.locator('.hf-note-card', { hasText: 'Deploying a frozen forge' });
    await expect(folder.locator('.hf-note-kind')).toContainText('4 files');
    await expect(folder.locator('.hf-note-files code')).toContainText(['index.md']);

    // the undated notes still get a heat class and an explicit "undated" marker
    const undated = page.locator('.hf-note-card', { hasText: 'Static host configs' });
    await expect(undated.locator('.hf-age')).toHaveText('undated');

    await expectBasicA11y(page);
  });

  test('empty-state copy is absent when there are notes', async ({ page }) => {
    await page.goto('/notes/');
    await expect(page.locator('.hf-empty')).toHaveCount(0);
  });
});

test.describe('one note', () => {
  test('single-file markdown note renders markdown and toggles to source', async ({ page }) => {
    await page.goto('/notes/git-plumbing-cheatsheet/');
    await expect(page.locator('h1')).toContainText('Git plumbing');

    // header facts
    await expect(page.locator('.hf-note-meta')).toContainText('1 file');
    await expect(page.locator('.hf-note-tags .hf-tag')).toContainText(['git']);

    // exactly one file card, no table of contents for a single-file note
    await expect(page.locator('.hf-note-file')).toHaveCount(1);
    await expect(page.locator('.hf-note-toc')).toHaveCount(0);

    // rendered markdown: headings, a table and a fenced code block from the note's own body
    const preview = page.locator('.hf-nv-preview');
    await expect(preview).toBeVisible();
    await expect(preview.locator('.hf-md h3').first()).toContainText('The seven calls');
    await expect(preview.locator('.hf-md table')).toBeVisible();
    await expect(preview.locator('.hf-md pre code')).not.toHaveCount(0);
    // frontmatter is metadata, not prose: never rendered, and the title is not repeated as a
    // second <h1> under the page heading
    await expect(preview).not.toContainText('title:');
    await expect(preview.locator('h1')).toHaveCount(0);
    // source view starts hidden
    await expect(page.locator('.hf-nv-source')).toBeHidden();

    // toggle to source: highlighted markdown with a line gutter, preview hidden
    await page.locator('.hf-nv-lbl--source').click();
    await expect(page.locator('.hf-nv-source')).toBeVisible();
    await expect(page.locator('.hf-nv-preview')).toBeHidden();
    await expect(page.locator('.hf-nv-source .hf-code .line').first()).toBeVisible();
    // the frontmatter is part of the source, and must NOT be part of the preview
    await expect(page.locator('.hf-nv-source')).toContainText('title:');

    // and back
    await page.locator('.hf-nv-lbl--preview').click();
    await expect(page.locator('.hf-nv-preview')).toBeVisible();

    // raw + copy-path affordances mirror the repo blob viewer
    await expect(page.locator('.hf-blob-actions a', { hasText: 'Raw' }).first())
      .toHaveAttribute('href', '/notes/git-plumbing-cheatsheet/raw/git-plumbing-cheatsheet.md');
    await expect(page.locator('button[data-copy]').first()).toBeVisible();

    await expectBasicA11y(page);
  });

  test('non-markdown note shows highlighted source with a line gutter', async ({ page }) => {
    await page.goto('/notes/check-determinism/');
    await expect(page.locator('h1')).toContainText('Check determinism');

    // no markdown → no preview/source toggle at all
    await expect(page.locator('.hf-mdview-seg')).toHaveCount(0);

    const code = page.locator('.hf-note-file .hf-code');
    await expect(code).toBeVisible();
    await expect(code.locator('.shiki')).toHaveCount(1);
    expect(await code.locator('.line').count()).toBeGreaterThan(10);
    // The gutter is CSS counters on `.line`, and every line is anchorable — with an id
    // namespaced by the note file's section anchor, so a multi-file note cannot emit the same
    // `L1` on three cards (see highlight.ts `idPrefix`).
    await expect(code.locator('[id$="-L1"]')).toHaveCount(1);
    await expect(page.locator('.hf-blob-meta .hf-lang-badge')).toHaveText('PowerShell');

    await expectBasicA11y(page);
  });

  test('multi-file note shows every file with a working anchor list', async ({ page }) => {
    await page.goto('/notes/deploying-a-frozen-forge/');
    await expect(page.locator('h1')).toContainText('Deploying a frozen forge');

    const sections = page.locator('.hf-note-file');
    await expect(sections).toHaveCount(4);
    await expect(page.locator('.hf-note-file-name')).toContainText([
      'deploy.sh',
      'index.md',
      'pages.yml',
      'serve.py',
    ]);

    // table of contents: one entry per file, each pointing at that file's section id
    const toc = page.locator('.hf-note-toc a');
    await expect(toc).toHaveCount(4);
    for (let i = 0; i < 4; i++) {
      const href = (await toc.nth(i).getAttribute('href'))!;
      expect(href.startsWith('#')).toBe(true);
      await expect(page.locator(`.hf-note-file${href}`)).toHaveCount(1);
    }

    // following one actually moves the viewport to that section
    const target = (await toc.nth(3).getAttribute('href'))!;
    await toc.nth(3).click();
    expect(page.url().endsWith(target)).toBe(true);
    await expect(page.locator(target)).toBeInViewport();

    // four distinct languages, and only the markdown file gets a toggle
    await expect(page.locator('.hf-mdview-seg')).toHaveCount(1);

    await expectBasicA11y(page);
  });

  test('folder note with no markdown falls back to the humanised folder name', async ({ page }) => {
    await page.goto('/notes/static-host-configs/');
    await expect(page.locator('h1')).toContainText('Static host configs');
    await expect(page.locator('.hf-note-file')).toHaveCount(3);
    await expect(page.locator('.hf-note-meta')).toContainText('3 files');
    await expect(page.locator('.hf-mdview-seg')).toHaveCount(0);
  });
});

test.describe('raw note files', () => {
  test('serve the exact bytes with a content type from the extension map', async ({ request }) => {
    const res = await request.get('/notes/static-host-configs/raw/vercel.json');
    expect(res.status()).toBe(200);
    expect(res.headers()['content-type']).toContain('application/json');
    const onDisk = fs.readFileSync(path.join(NOTES_DIR, 'static-host-configs', 'vercel.json'));
    expect(Buffer.compare(Buffer.from(await res.body()), onDisk)).toBe(0);
  });

  test('every raw link a note page prints actually resolves', async ({ page, request }) => {
    // Content-independent guard on the URL encoding: a note file name is authored by hand, so
    // the href and the file the build emitted have to agree byte for byte after decoding.
    for (const { slug } of EXPECTED) {
      await page.goto(`/notes/${slug}/`);
      const hrefs = await page
        .locator('a[href^="/notes/"][href*="/raw/"]')
        .evaluateAll((els) => [...new Set(els.map((e) => e.getAttribute('href') ?? ''))]);
      expect(hrefs.length).toBeGreaterThan(0);
      for (const href of hrefs) {
        expect(href).not.toMatch(/[ "'<>]/); // an unencoded href is not a valid URL to begin with
        expect((await request.get(href)).status()).toBe(200);
      }
    }
  });

  test('a single-file note serves its one file as text', async ({ request }) => {
    const res = await request.get('/notes/check-determinism/raw/check-determinism.ps1');
    expect(res.status()).toBe(200);
    expect(res.headers()['content-type']).toContain('text/plain');
    const onDisk = fs.readFileSync(path.join(NOTES_DIR, 'check-determinism.ps1'));
    expect(Buffer.compare(Buffer.from(await res.body()), onDisk)).toBe(0);
  });
});

test.describe('notes without JavaScript', () => {
  test.use({ javaScriptEnabled: false });

  test('index and the preview/source toggle still work', async ({ page }) => {
    await page.goto('/notes/');
    await expect(page.locator('.hf-note-card')).toHaveCount(EXPECTED.length);

    await page.goto('/notes/git-plumbing-cheatsheet/');
    await expect(page.locator('.hf-nv-preview')).toBeVisible();
    await page.locator('.hf-nv-lbl--source').click();
    await expect(page.locator('.hf-nv-source')).toBeVisible();
    await expect(page.locator('.hf-nv-preview')).toBeHidden();
  });
});
