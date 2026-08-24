/**
 * Markdown rendering, and in particular the trust boundary added in Phase 5.
 *
 * A repo imported from a forge is not the site owner's content: its README and its provider
 * release notes are written by whoever can push to it, and the site emits the rendered HTML
 * with `set:html`. Everything below is a regression guard on "that content cannot execute".
 */
import { describe, expect, it } from 'vitest';
import { isMarkdownPath, isTrustedSource, renderMarkdown, safeUrl } from '../../src/lib/markdown';

describe('renderMarkdown (trusted)', () => {
  it('passes the owner’s own HTML through, as before', () => {
    const html = renderMarkdown('<div class="note">hi</div>\n\nplain **text**');
    expect(html).toContain('<div class="note">hi</div>');
    expect(html).toContain('<strong>text</strong>');
  });

  it('defaults to trusted so existing call sites are unchanged', () => {
    expect(renderMarkdown('<b>x</b>')).toBe(renderMarkdown('<b>x</b>', { trusted: true }));
  });
});

/**
 * Headings are demoted one level in BOTH pipelines.
 *
 * Rendered markdown never owns a page: a README sits in a card whose head is already an
 * `<h2>`, on a page whose `<h1>` is the repo. Letting user markdown emit `<h1>` gave those
 * pages two `<h1>`s, and a README that is a lone title with no `##` in it — the most common
 * README shape there is — stepped `h1 → h3` straight into the About sidebar and failed
 * `heading-order`. See `.hf-md`'s shifted heading sizes in global.css: the markup moved, the
 * rendered page did not.
 */
describe('renderMarkdown (heading levels)', () => {
  it('demotes every heading one level', () => {
    const html = renderMarkdown(['# One', '', '## Two', '', '### Three', '', '#### Four', '', '##### Five', ''].join('\n'));
    expect(html).toContain('<h2>One</h2>');
    expect(html).toContain('<h3>Two</h3>');
    expect(html).toContain('<h4>Three</h4>');
    expect(html).toContain('<h5>Four</h5>');
    expect(html).toContain('<h6>Five</h6>');
    expect(html).not.toContain('<h1');
  });

  it('clamps at h6 rather than emitting an h7', () => {
    const html = renderMarkdown('###### Six');
    expect(html).toContain('<h6>Six</h6>');
    expect(html).not.toContain('<h7');
  });

  it('demotes setext headings too', () => {
    const html = renderMarkdown(['Title', '=====', '', 'Sub', '---', ''].join('\n'));
    expect(html).toContain('<h2>Title</h2>');
    expect(html).toContain('<h3>Sub</h3>');
  });

  it('applies to untrusted content as well', () => {
    expect(renderMarkdown('# Imported', { trusted: false })).toContain('<h2>Imported</h2>');
  });
});

/**
 * Fenced code blocks scroll horizontally (`.hf-md pre { overflow-x: auto }`) and contain
 * nothing focusable, so without a tabindex they cannot be scrolled without a mouse in Firefox
 * or Safari (axe `scrollable-region-focusable`, WCAG 2.1.1). Shiki-highlighted blocks already
 * carry one; marked's did not.
 */
describe('renderMarkdown (code blocks)', () => {
  it('makes every code block keyboard-focusable', () => {
    const html = renderMarkdown(['```bash', 'npm test', '```', ''].join('\n'));
    expect(html).toContain('<pre tabindex="0">');
    expect(html).not.toMatch(/<pre>/);
  });

  it('does the same for untrusted content', () => {
    expect(renderMarkdown('    indented code\n', { trusted: false })).toContain('<pre tabindex="0">');
  });
});

describe('renderMarkdown (untrusted)', () => {
  const render = (src: string) => renderMarkdown(src, { trusted: false });

  it('drops raw HTML blocks and inline tags', () => {
    const html = render(
      [
        '# Release 1.0',
        '',
        '<script>window.__PWNED = 1</script>',
        '',
        'inline <img src=x onerror="window.__PWNED2 = 1"> tag',
        '',
        '<iframe src="https://evil.test"></iframe>',
      ].join('\n'),
    );
    expect(html).not.toContain('<script');
    expect(html).not.toContain('onerror');
    expect(html).not.toContain('<iframe');
    expect(html).not.toContain('<img');
    // The surrounding markdown still renders (demoted a level — see the heading tests below).
    expect(html).toContain('<h2>Release 1.0</h2>');
    expect(html).toContain('inline');
  });

  it('neutralises script-bearing link and image URLs', () => {
    const html = render(
      [
        '[click](javascript:alert(1))',
        '',
        '[click](JaVaScRiPt&#58;alert(1))',
        '',
        '![x](data:text/html;base64,PHNjcmlwdD4=)',
        '',
        '[ok](https://example.test/page) and [rel](./docs/a.md) and [frag](#top)',
      ].join('\n'),
    );
    expect(html).not.toMatch(/href="\s*javascript/i);
    expect(html).not.toMatch(/src="\s*data:/i);
    // Legitimate targets survive untouched.
    expect(html).toContain('href="https://example.test/page"');
    expect(html).toContain('href="./docs/a.md"');
    expect(html).toContain('href="#top"');
  });

  it('escapes markdown-level HTML metacharacters in text', () => {
    expect(render('a < b & c > d')).not.toContain('a < b');
    expect(render('`<script>`')).toContain('&lt;script&gt;');
  });
});

describe('safeUrl', () => {
  it('allows http, https, mailto and every relative form', () => {
    for (const url of ['https://a.test/x', 'http://a.test', 'mailto:a@b.test', '/abs', './rel', '#frag', 'a/b.png']) {
      expect(safeUrl(url)).toBe(url);
    }
  });

  it('rejects executable schemes however they are spelled', () => {
    for (const url of [
      'javascript:alert(1)',
      'JAVASCRIPT:alert(1)',
      'java\tscript:alert(1)',
      ' javascript:alert(1)',
      '&#106;avascript:alert(1)',
      'vbscript:msgbox(1)',
      'data:text/html,<script>x</script>',
    ]) {
      expect(safeUrl(url)).toBe('#');
    }
  });
});

describe('isTrustedSource', () => {
  it('trusts only local repos', () => {
    expect(isTrustedSource({ type: 'local' })).toBe(true);
    for (const type of ['github', 'gitlab', 'gitea', 'forgejo']) {
      expect(isTrustedSource({ type })).toBe(false);
    }
  });
});

describe('isMarkdownPath', () => {
  it('matches the markdown extensions and a bare README', () => {
    expect(isMarkdownPath('docs/a.md')).toBe(true);
    expect(isMarkdownPath('README')).toBe(true);
    expect(isMarkdownPath('src/a.ts')).toBe(false);
  });
});
