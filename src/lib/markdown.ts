/**
 * Render markdown strings (READMEs, release notes) to HTML at build time.
 *
 * Two trust levels, because since schema v3 not all of this content is the site owner's:
 *
 *  - **trusted** (default) — the owner's own writing: `content/profile.md`, and repos
 *    configured as `type: 'local'`. Rendered with raw HTML passed through, same as before.
 *  - **untrusted** — anything that came off a forge: an imported repo's README and its
 *    provider release notes. `docs/user/importing.md` explicitly invites importing repos you
 *    do not control, so anyone who can push a README or publish a release there would
 *    otherwise get arbitrary `<script>` onto this site's origin (the output is emitted with
 *    `set:html`). For that content raw HTML blocks and inline tags are dropped, and link and
 *    image URLs are restricted to http/https/mailto and relative targets.
 *
 * Dropping raw HTML rather than allow-listing it is deliberate: a partial HTML sanitiser is
 * a liability, and the alternative here would mean shipping one with no dependencies.
 *
 * Relative links/images are left as-is for now; Phase 3 rewrites them to file routes.
 */
import { Marked, type Tokens } from 'marked';

const OPTIONS = { gfm: true, breaks: false } as const;

/**
 * Every heading in rendered markdown drops one level: `#` becomes `<h2>`, `##` becomes
 * `<h3>`, and `######` stays `<h6>` because there is nowhere lower to go.
 *
 * Rendered markdown never owns a page here. A README is inside a card whose own head is an
 * `<h2>README</h2>`, on a page whose `<h1>` is the repo (`RepoHeader.astro`); a note's body
 * sits under the note title and the file name; release notes sit under the release. Letting
 * user markdown emit an `<h1>` gave those pages two `<h1>`s and — when a README was a lone
 * title with no `##` in it, which is the most common README there is — a document that
 * stepped `h1 → h3` straight into the About sidebar and failed `heading-order`.
 *
 * The visual result is unchanged: `.hf-md`'s heading sizes are shifted by the same one level
 * in `styles/global.css`, so a `#` still renders as a title. The same demotion is applied to
 * the profile and organization markdown, which goes through Astro's own pipeline — see the
 * rehype plugin in `astro.config.mjs`. Change one and you must change the other.
 */
function demoteHeading(token: Tokens.Heading): false {
  token.depth = Math.min(6, token.depth + 1);
  return false;
}

/** URL schemes a link or image may use; everything else becomes an inert `#`. */
const SAFE_SCHEMES = new Set(['http', 'https', 'mailto']);

/**
 * Decode the entity escapes a payload uses to hide a scheme (`&#106;avascript:`). Only used
 * to *inspect* a URL — never to build the rendered output.
 */
function decodeEntities(value: string): string {
  return value.replace(/&(#x[0-9a-f]+|#\d+|[a-z]+);?/gi, (whole, body: string) => {
    if (body.startsWith('#')) {
      const code = body[1]?.toLowerCase() === 'x' ? Number.parseInt(body.slice(2), 16) : Number.parseInt(body.slice(1), 10);
      return Number.isInteger(code) && code >= 0 && code <= 0x10ffff ? String.fromCodePoint(code) : whole;
    }
    const named: Record<string, string> = { amp: '&', colon: ':', tab: '\t', newline: '\n', lt: '<', gt: '>' };
    return named[body.toLowerCase()] ?? whole;
  });
}

/**
 * `href` unchanged when it is safe to navigate to, `#` when it is not.
 *
 * A URL with no scheme (relative, or a `#fragment`) is fine; a URL with one must name a
 * scheme on the allow-list, which rules out `javascript:`, `data:` and `vbscript:`.
 */
export function safeUrl(href: string): string {
  // Whitespace and control characters go first: `java&#9;script:` still navigates.
  const probe = decodeEntities(href).replace(/[\s\u0000-\u001f\u007f]+/g, '').toLowerCase();
  const scheme = /^([a-z][a-z0-9+.-]*):/.exec(probe);
  if (!scheme) return href;
  return SAFE_SCHEMES.has(scheme[1]!) ? href : '#';
}

/** Minimal escaping for text placed inside the mermaid container's `<code>`. */
function escapeHtml(text: string): string {
  return text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

/**
 * Per-parse switch for the mermaid fence override below. A module-level flag is safe here
 * because `renderMarkdown` parses synchronously — nothing can interleave — and it beats
 * building two more Marked instances for one boolean.
 */
let mermaidEnabled = true;

/**
 * ```` ```mermaid ```` fences (0.2.0): emit the source in an `hf-mermaid` container that
 * the client-side renderer (`MermaidRenderer.astro`) turns into an SVG. The `<pre><code>`
 * IS the no-JS fallback — a reader without JavaScript gets the diagram source as an honest
 * code block, and an invalid diagram stays one.
 *
 * Applies in BOTH trust modes, by the owner's decision: importing a repo is choosing to
 * publish its content, so its diagrams render like everything else — the owner is the one
 * who decides what to import (the same stance the big forges take, rendering mermaid in
 * every README). This does not weaken the untrusted engine's own guarantees — raw HTML is
 * still dropped and URLs still filtered; the fence text is escaped here at build time, and
 * at view time mermaid runs with `securityLevel: 'strict'`, its own sanitiser, in the
 * visitor's browser. `markdown.mermaid: false` turns the whole thing off.
 */
function mermaidFence(token: Tokens.Code): string | false {
  if (!mermaidEnabled) return false;
  const lang = (token.lang ?? '').trim().toLowerCase().split(/\s+/)[0];
  if (lang !== 'mermaid') return false;
  return `<pre class="hf-mermaid" tabindex="0"><code class="language-mermaid">${escapeHtml(token.text)}</code></pre>\n`;
}

const trusted = new Marked(OPTIONS).use({ renderer: { heading: demoteHeading, code: mermaidFence } });

/**
 * Renderer overrides for content frznforge did not author. Returning `false` from an override
 * tells marked to fall through to its own implementation, so link/image only rewrite the URL
 * and keep every other rendering detail (escaping of titles and alt text included).
 */
const untrusted = new Marked(OPTIONS).use({
  renderer: {
    /** Raw HTML — block (`<script>…`) and inline (`<img onerror=…>`) both land here. */
    html(): string {
      return '';
    },
    heading: demoteHeading,
    code: mermaidFence,
    link(token: Tokens.Link): false {
      token.href = safeUrl(token.href);
      return false;
    },
    image(token: Tokens.Image): false {
      token.href = safeUrl(token.href);
      return false;
    },
  },
});

export interface RenderOptions {
  /**
   * False for content imported from a forge. Defaults to true (the owner's own content), so
   * an existing call site keeps its old behaviour.
   */
  trusted?: boolean;
  /**
   * Render ```mermaid fences as diagram containers (`markdown.mermaid` config, 0.2.0).
   * Defaults to true and applies in both trust modes — see `mermaidFence` for why.
   */
  mermaid?: boolean;
}

/**
 * Make a rendered `<pre>` reachable from the keyboard.
 *
 * `.hf-md pre` is `overflow-x: auto`, so a long line makes it a scrollable region — and a
 * scrollable region with no focusable content and no tabindex cannot be scrolled at all
 * without a mouse in Firefox or Safari (Chromium ships focusable scrollers; the other two do
 * not). Shiki-highlighted blocks already carry `tabindex="0"` from `highlight.ts`; marked's
 * do not, which left every fenced block in a README, a note preview and a release body
 * stranded (axe `scrollable-region-focusable`, WCAG 2.1.1).
 *
 * A string pass rather than a renderer override: marked emits code blocks as a bare `<pre>`
 * with no attributes, so this touches exactly those and never a `<pre>` that already carries
 * one (which, in trusted raw HTML, the author is free to set themselves).
 */
export function focusableCodeBlocks(html: string): string {
  return html.replace(/<pre>/g, '<pre tabindex="0">');
}

export function renderMarkdown(src: string, options: RenderOptions = {}): string {
  const engine = options.trusted === false ? untrusted : trusted;
  mermaidEnabled = options.mermaid !== false;
  try {
    return focusableCodeBlocks(engine.parse(src, { async: false }) as string);
  } finally {
    mermaidEnabled = true;
  }
}

/**
 * Whether rendered HTML holds at least one mermaid container — the signal a page uses to
 * include `MermaidRenderer.astro` (and with it the lazily-loaded mermaid bundle) at all.
 * Escaped fences in code blocks cannot match: their quotes are `&quot;`-encoded.
 */
export function containsMermaid(html: string): boolean {
  return html.includes('class="hf-mermaid"');
}

/**
 * Whether a repo's markdown is the site owner's own. Only `type: 'local'` is — every other
 * source is a repo pulled off someone else's forge.
 */
export function isTrustedSource(source: { type: string }): boolean {
  return source.type === 'local';
}

/** Heuristic: treat .md/.markdown/.mdown and extensionless README as markdown. */
export function isMarkdownPath(path: string): boolean {
  return /(\.(md|markdown|mdown|mkd)$)|(^|\/)readme$/i.test(path);
}
