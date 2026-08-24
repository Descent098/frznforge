/**
 * Build-time syntax highlighting with Shiki (bundled with Astro). Dual light/dark themes
 * driven by the site's `[data-theme]` attribute via shiki's CSS-variable output.
 */
import { createHighlighter, bundledLanguages, type Highlighter } from 'shiki';

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
    themes: ['github-light', 'github-dark'],
    langs: [],
  }));
}

const loaded = new Set<string>();

/**
 * Highlight source to HTML: `<pre class="shiki ..."><code><span class="line" id="Ln">…`
 * Both themes are emitted (CSS variables); `.hf-code` CSS picks per theme. Every line gets
 * an id `L<n>` so `#L12` anchors work with pure CSS `:target`.
 */
export async function highlightToHtml(code: string, language: string | null, path?: string): Promise<string> {
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
  return hl.codeToHtml(source, {
    lang: loaded.has(lang) ? lang : 'text',
    themes: { light: 'github-light', dark: 'github-dark' },
    defaultColor: false,
    transformers: [
      {
        line(node, line) {
          node.properties['id'] = `L${line}`;
        },
      },
    ],
  });
}

/** Count lines the way editors do (trailing newline doesn't add a line). */
export function countLines(code: string): number {
  if (code.length === 0) return 0;
  const n = code.split('\n').length;
  return code.endsWith('\n') ? n - 1 : n;
}
