/**
 * Build-time syntax highlighting with Shiki (bundled with Astro). Dual light/dark themes
 * driven by the site's `[data-theme]` attribute via shiki's CSS-variable output.
 */
import { createHighlighter, bundledLanguages, type Highlighter } from 'shiki';
import { highlightFingerprint, highlightKey, memoizeHighlight } from './highlight-cache';

/** Artifact language name (from ingest's language map) → shiki language id. */
const LANGUAGE_TO_SHIKI: Record<string, string> = {
  'TypeScript': 'typescript',
  'JavaScript': 'javascript',
  'TSX': 'tsx',
  'JSX': 'jsx',
  'Python': 'python',
  'Go': 'go',
  'Rust': 'rust',
  'Svelte': 'svelte',
  'Astro': 'astro',
  'HTML': 'html',
  'CSS': 'css',
  'SCSS': 'scss',
  'Sass': 'sass',
  'Less': 'less',
  'Shell': 'shellscript',
  'PowerShell': 'powershell',
  'Batchfile': 'bat',
  'Dockerfile': 'docker',
  'Markdown': 'markdown',
  'MDX': 'mdx',
  'JSON': 'json',
  'JSON with Comments': 'jsonc',
  'YAML': 'yaml',
  'TOML': 'toml',
  'XML': 'xml',
  'SVG': 'xml',
  'C': 'c',
  'C++': 'cpp',
  'C#': 'csharp',
  'Java': 'java',
  'Kotlin': 'kotlin',
  'Swift': 'swift',
  'Ruby': 'ruby',
  'PHP': 'php',
  'Lua': 'lua',
  'R': 'r',
  'Dart': 'dart',
  'Elixir': 'elixir',
  'Erlang': 'erlang',
  'Haskell': 'haskell',
  'Zig': 'zig',
  'SQL': 'sql',
  'Vue': 'vue',
  'Makefile': 'make',
  'CMake': 'cmake',
  'Nix': 'nix',
  'HCL': 'hcl',
  'Protocol Buffer': 'proto',
  'GraphQL': 'graphql',
  'TeX': 'latex',
  'Objective-C': 'objective-c',
  'Scala': 'scala',
  'Perl': 'perl',
  'Groovy': 'groovy',
  'Ini': 'ini',
  'Diff': 'diff',
};

/** Shiki lang id for an artifact language name (or by file extension fallback); 'text' when unknown. */
export function shikiLang(language: string | null, path?: string): string {
  if (language && LANGUAGE_TO_SHIKI[language] && LANGUAGE_TO_SHIKI[language] in bundledLanguages) {
    return LANGUAGE_TO_SHIKI[language]!;
  }
  if (path) {
    const ext = path.replace(/^.*\./, '').toLowerCase();
    if (ext && ext !== path && ext in bundledLanguages) return ext;
  }
  return 'text';
}

let highlighterPromise: Promise<Highlighter> | null = null;

function getHighlighter(): Promise<Highlighter> {
  return (highlighterPromise ??= createHighlighter({
    // The HIGH-CONTRAST github themes, not the plain ones.
    //
    // `github-light` ships four token colours that miss WCAG AA even on its own #ffffff —
    // keywords #D73A49 at 4.6:1 there and 4.5:1 on this site's warmer card, variables #E36209
    // at 3.5:1, strings #22863A, comments #6A737D — and `github-dark`'s comment grey lands at
    // 3.2:1 on Hearth's dark surface. That is the single most-read text on the site failing
    // the floor, and patching individual literals out of a theme (which this file used to do
    // for the dark comment grey) is a game of whack-a-mole against a bundled JSON file.
    // The high-contrast variants are github's own answer to exactly this and clear AA on every
    // surface this site paints code on. `tests/e2e/a11y.spec.ts` measures it.
    themes: ['github-light-high-contrast', 'github-dark-high-contrast'],
    langs: [],
  }));
}

const loaded = new Set<string>();

/** The two themes every render emits, in one place so the fingerprint and the render agree. */
const THEMES = { light: 'github-light-high-contrast', dark: 'github-dark-high-contrast' } as const;

/**
 * One render of the real code path, hashed into the cache fingerprint (see highlight-cache.ts).
 *
 * Rendered through a **real grammar**, not `text`. A `text` render emits a single untokenised
 * span carrying only each theme's foreground and background, so a theme edit that changed a
 * keyword, string or comment colour — exactly the edit this file's own history records, and
 * exactly what a reader would expect the canary to catch — would leave the fingerprint
 * unmoved and every cached entry serving the old colours. A snippet with a keyword, a string
 * and a comment exposes those token colours to the hash. Falls back to `text` only if the
 * grammar will not load, which merely weakens the canary; it never makes it wrong.
 */
function canaryHtml(hl: Highlighter): string {
  const lang = loaded.has(CANARY_LANG) ? CANARY_LANG : 'text';
  return codeToHtml(hl, "const canary = 'x'; // 1", lang, 'canary-');
}

/** Grammar the canary renders through; loaded once, before the first fingerprint. */
const CANARY_LANG = 'typescript';

let canaryReady: Promise<void> | null = null;

/** Load the canary's grammar so the fingerprint sees token colours, not just fg/bg. */
function prepareCanary(hl: Highlighter): Promise<void> {
  return (canaryReady ??= (async () => {
    if (loaded.has(CANARY_LANG)) return;
    try {
      await hl.loadLanguage(CANARY_LANG as Parameters<Highlighter['loadLanguage']>[0]);
      loaded.add(CANARY_LANG);
    } catch {
      /* the canary degrades to `text`; it is weaker, never wrong */
    }
  })());
}

/** The single place `codeToHtml` is called, so a cached result and a fresh one cannot diverge. */
function codeToHtml(hl: Highlighter, source: string, lang: string, idPrefix: string): string {
  return hl.codeToHtml(source, {
    lang,
    themes: THEMES,
    defaultColor: false,
    transformers: [
      {
        line(node, line) {
          node.properties['id'] = `${idPrefix}L${line}`;
        },
      },
    ],
  });
}

/**
 * Highlight source to HTML: `<pre class="shiki ..."><code><span class="line" id="Ln">…`
 * Both themes are emitted (CSS variables); `.hf-code` CSS picks per theme. Every line gets
 * an id `<idPrefix>L<n>` so `#L12` anchors work with pure CSS `:target`.
 *
 * @param idPrefix Namespace for the per-line ids. A page that highlights ONE file can leave
 *   it empty and get the documented `L12`. A page that highlights several — a multi-file note
 *   renders one card per file — **must** pass a per-file prefix: without it every card emitted
 *   `id="L1"…"L30"` again, so `#L5` resolved to the first file only and the lines of every
 *   later file were permanently unlinkable. `NoteFileView` passes its section anchor, giving
 *   `f-netlify-toml-L5`.
 */
export async function highlightToHtml(
  code: string,
  language: string | null,
  path?: string,
  idPrefix = '',
): Promise<string> {
  const hl = await getHighlighter();
  // Shiki emits one `.line` per newline-separated segment, so a file that ends with a
  // trailing newline (almost all of them) gets an extra empty line and a phantom gutter
  // number that disagrees with the "N lines" label countLines() produces. Drop that one
  // newline so the gutter and the label always agree.
  const source = code.endsWith('\n') ? code.slice(0, -1) : code;
  let lang = shikiLang(language, path);
  if (lang !== 'text' && !loaded.has(lang)) {
    try {
      await hl.loadLanguage(lang as Parameters<Highlighter['loadLanguage']>[0]);
      loaded.add(lang);
    } catch {
      lang = 'text';
    }
  }
  /*
   * Key on the language actually handed to Shiki, never on the one asked for.
   *
   * Loading a grammar can fail for reasons that have nothing to do with the grammar existing
   * (`shikiLang` already guarantees that): a dynamic import hitting EMFILE while Astro renders
   * pages concurrently, a half-written chunk in node_modules. The render then falls back to
   * plain `text` — and if the key still said `typescript`, that unhighlighted output would be
   * written to disk under the TypeScript key and served by every later build, turning a
   * transient hiccup into permanently uncoloured code. Keyed on the effective language, the
   * fallback is stored as what it is — a `text` render under the `text` key — and the next
   * build, whose grammar loads, misses and highlights properly. The one cost is that a grammar
   * loads even when every file of that language will hit; that is a few ms per language per
   * build, and it buys back the load-bearing guarantee.
   */
  const effective = loaded.has(lang) ? lang : 'text';
  await prepareCanary(hl);
  const key = highlightKey(await highlightFingerprint(() => canaryHtml(hl)), effective, idPrefix, source);
  return memoizeHighlight(key, () => codeToHtml(hl, source, effective, idPrefix));
}

/** Count lines the way editors do (trailing newline doesn't add a line). */
export function countLines(code: string): number {
  if (code.length === 0) return 0;
  const n = code.split('\n').length;
  return code.endsWith('\n') ? n - 1 : n;
}
