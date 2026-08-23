/**
 * Language detection by extension / filename, and per-repo language statistics.
 * The map is intentionally small (no linguist); colours follow GitHub's linguist palette.
 */
import type { FileInfo, LanguageStat } from '../data/schema';

export interface LanguageDef {
  name: string;
  color: string | null;
  /** Documentation / data / config languages are listed per file but excluded from stats. */
  prose?: boolean;
}

const L = (name: string, color: string | null, prose = false): LanguageDef => ({ name, color, prose });

const LANGS = {
  TypeScript: L('TypeScript', '#3178c6'),
  JavaScript: L('JavaScript', '#f1e05a'),
  Python: L('Python', '#3572a5'),
  Go: L('Go', '#00add8'),
  Rust: L('Rust', '#dea584'),
  Svelte: L('Svelte', '#ff3e00'),
  Astro: L('Astro', '#ff5a03'),
  Vue: L('Vue', '#41b883'),
  HTML: L('HTML', '#e34c26'),
  CSS: L('CSS', '#663399'),
  SCSS: L('SCSS', '#c6538c'),
  Sass: L('Sass', '#a53b70'),
  Less: L('Less', '#1d365d'),
  Shell: L('Shell', '#89e051'),
  PowerShell: L('PowerShell', '#012456'),
  Batchfile: L('Batchfile', '#c1f12e'),
  Dockerfile: L('Dockerfile', '#384d54'),
  Makefile: L('Makefile', '#427819'),
  CMake: L('CMake', '#da3434'),
  C: L('C', '#555555'),
  'C++': L('C++', '#f34b7d'),
  'C#': L('C#', '#178600'),
  'Objective-C': L('Objective-C', '#438eff'),
  Java: L('Java', '#b07219'),
  Kotlin: L('Kotlin', '#a97bff'),
  Scala: L('Scala', '#c22d40'),
  Groovy: L('Groovy', '#4298b8'),
  Swift: L('Swift', '#f05138'),
  Ruby: L('Ruby', '#701516'),
  PHP: L('PHP', '#4f5d95'),
  Perl: L('Perl', '#0298c3'),
  Lua: L('Lua', '#000080'),
  R: L('R', '#198ce7'),
  Julia: L('Julia', '#a270ba'),
  Dart: L('Dart', '#00b4ab'),
  Elixir: L('Elixir', '#6e4a7e'),
  Erlang: L('Erlang', '#b83998'),
  Haskell: L('Haskell', '#5e5086'),
  OCaml: L('OCaml', '#ef7a08'),
  'F#': L('F#', '#b845fc'),
  Clojure: L('Clojure', '#db5855'),
  Elm: L('Elm', '#60b5cc'),
  Zig: L('Zig', '#ec915c'),
  Nim: L('Nim', '#ffc200'),
  Crystal: L('Crystal', '#000100'),
  SQL: L('SQL', '#e38c00'),
  Nix: L('Nix', '#7e7eff'),
  Terraform: L('HCL', '#844fba'),
  Protobuf: L('Protocol Buffer', null),
  GraphQL: L('GraphQL', '#e10098'),
  'Jupyter Notebook': L('Jupyter Notebook', '#da5b0b'),
  TeX: L('TeX', '#3d6117'),
  Assembly: L('Assembly', '#6e4c13'),
  WebAssembly: L('WebAssembly', '#04133b'),
  Solidity: L('Solidity', '#aa6746'),
  Vim: L('Vim Script', '#199f4b'),
  'Emacs Lisp': L('Emacs Lisp', '#c065db'),
  'Common Lisp': L('Common Lisp', '#3fb68b'),
  Scheme: L('Scheme', '#1e4aec'),
  Fortran: L('Fortran', '#4d41b1'),
  Pascal: L('Pascal', '#e3f171'),
  Ada: L('Ada', '#02f88c'),
  D: L('D', '#ba595e'),
  V: L('V', '#4f87c4'),
  Gleam: L('Gleam', '#ffaff3'),
  CoffeeScript: L('CoffeeScript', '#244776'),
  Handlebars: L('Handlebars', '#f7931e'),
  Pug: L('Pug', '#a86454'),
  MDX: L('MDX', '#fcb32c'),
  // prose / data / config — listed per file, excluded from stats
  Markdown: L('Markdown', '#083fa1', true),
  JSON: L('JSON', '#292929', true),
  YAML: L('YAML', '#cb171e', true),
  TOML: L('TOML', '#9c4221', true),
  XML: L('XML', '#0060ac', true),
  INI: L('INI', '#d1dbe0', true),
  CSV: L('CSV', null, true),
  Text: L('Text', null, true),
  reStructuredText: L('reStructuredText', '#141414', true),
  AsciiDoc: L('AsciiDoc', '#73a0c5', true),
  SVG: L('SVG', '#ff9900', true),
  Ignore: L('Ignore List', '#000000', true),
  Lockfile: L('Lockfile', null, true),
  EditorConfig: L('EditorConfig', '#fff1f2', true),
  'Git Attributes': L('Git Attributes', '#f44d27', true),
  'Git Config': L('Git Config', '#f44d27', true),
} satisfies Record<string, LanguageDef>;

type LangKey = keyof typeof LANGS;

const BY_EXT: Record<string, LangKey> = {
  ts: 'TypeScript', tsx: 'TypeScript', mts: 'TypeScript', cts: 'TypeScript',
  js: 'JavaScript', jsx: 'JavaScript', mjs: 'JavaScript', cjs: 'JavaScript',
  py: 'Python', pyi: 'Python', pyw: 'Python',
  go: 'Go',
  rs: 'Rust',
  svelte: 'Svelte',
  astro: 'Astro',
  vue: 'Vue',
  html: 'HTML', htm: 'HTML', xhtml: 'HTML',
  css: 'CSS',
  scss: 'SCSS',
  sass: 'Sass',
  less: 'Less',
  sh: 'Shell', bash: 'Shell', zsh: 'Shell', fish: 'Shell', ksh: 'Shell',
  ps1: 'PowerShell', psm1: 'PowerShell', psd1: 'PowerShell',
  bat: 'Batchfile', cmd: 'Batchfile',
  dockerfile: 'Dockerfile',
  mk: 'Makefile', mak: 'Makefile',
  cmake: 'CMake',
  c: 'C', h: 'C',
  cpp: 'C++', cc: 'C++', cxx: 'C++', 'c++': 'C++', hpp: 'C++', hh: 'C++', hxx: 'C++', inl: 'C++',
  cs: 'C#', csx: 'C#',
  m: 'Objective-C', mm: 'Objective-C',
  java: 'Java',
  kt: 'Kotlin', kts: 'Kotlin',
  scala: 'Scala', sc: 'Scala',
  groovy: 'Groovy', gradle: 'Groovy',
  swift: 'Swift',
  rb: 'Ruby', rake: 'Ruby', gemspec: 'Ruby',
  php: 'PHP', phtml: 'PHP',
  pl: 'Perl', pm: 'Perl',
  lua: 'Lua',
  r: 'R', rmd: 'R',
  jl: 'Julia',
  dart: 'Dart',
  ex: 'Elixir', exs: 'Elixir',
  erl: 'Erlang', hrl: 'Erlang',
  hs: 'Haskell', lhs: 'Haskell',
  ml: 'OCaml', mli: 'OCaml',
  fs: 'F#', fsi: 'F#', fsx: 'F#',
  clj: 'Clojure', cljs: 'Clojure', cljc: 'Clojure', edn: 'Clojure',
  elm: 'Elm',
  zig: 'Zig',
  nim: 'Nim',
  cr: 'Crystal',
  sql: 'SQL',
  nix: 'Nix',
  tf: 'Terraform', tfvars: 'Terraform', hcl: 'Terraform',
  proto: 'Protobuf',
  graphql: 'GraphQL', gql: 'GraphQL',
  ipynb: 'Jupyter Notebook',
  tex: 'TeX', sty: 'TeX', cls: 'TeX', bib: 'TeX',
  asm: 'Assembly', s: 'Assembly',
  wat: 'WebAssembly', wast: 'WebAssembly',
  sol: 'Solidity',
  vim: 'Vim',
  el: 'Emacs Lisp',
  lisp: 'Common Lisp', lsp: 'Common Lisp',
  scm: 'Scheme', ss: 'Scheme', rkt: 'Scheme',
  f: 'Fortran', f90: 'Fortran', f95: 'Fortran', f03: 'Fortran', for: 'Fortran',
  pas: 'Pascal', pp: 'Pascal',
  adb: 'Ada', ads: 'Ada',
  d: 'D',
  v: 'V',
  gleam: 'Gleam',
  coffee: 'CoffeeScript',
  hbs: 'Handlebars', handlebars: 'Handlebars',
  pug: 'Pug', jade: 'Pug',
  mdx: 'MDX',
  md: 'Markdown', markdown: 'Markdown', mdown: 'Markdown', mkd: 'Markdown',
  json: 'JSON', jsonc: 'JSON', json5: 'JSON', webmanifest: 'JSON', geojson: 'JSON',
  yml: 'YAML', yaml: 'YAML',
  toml: 'TOML',
  xml: 'XML', xsl: 'XML', xsd: 'XML', plist: 'XML', csproj: 'XML', props: 'XML', targets: 'XML',
  ini: 'INI', cfg: 'INI', conf: 'INI',
  csv: 'CSV', tsv: 'CSV',
  txt: 'Text', text: 'Text',
  rst: 'reStructuredText',
  adoc: 'AsciiDoc', asciidoc: 'AsciiDoc',
  svg: 'SVG',
  lock: 'Lockfile',
};

const BY_FILENAME: Record<string, LangKey> = {
  dockerfile: 'Dockerfile',
  containerfile: 'Dockerfile',
  makefile: 'Makefile',
  gnumakefile: 'Makefile',
  'cmakelists.txt': 'CMake',
  rakefile: 'Ruby',
  gemfile: 'Ruby',
  podfile: 'Ruby',
  brewfile: 'Ruby',
  vagrantfile: 'Ruby',
  justfile: 'Makefile',
  'package-lock.json': 'Lockfile',
  'yarn.lock': 'Lockfile',
  'pnpm-lock.yaml': 'Lockfile',
  'bun.lockb': 'Lockfile',
  'cargo.lock': 'Lockfile',
  'go.sum': 'Lockfile',
  'poetry.lock': 'Lockfile',
  'composer.lock': 'Lockfile',
  'gemfile.lock': 'Lockfile',
  'flake.lock': 'Lockfile',
  '.gitignore': 'Ignore',
  '.dockerignore': 'Ignore',
  '.npmignore': 'Ignore',
  '.prettierignore': 'Ignore',
  '.eslintignore': 'Ignore',
  '.gitattributes': 'Git Attributes',
  '.gitmodules': 'Git Config',
  '.gitconfig': 'Git Config',
  '.editorconfig': 'EditorConfig',
  '.bashrc': 'Shell',
  '.bash_profile': 'Shell',
  '.zshrc': 'Shell',
  '.profile': 'Shell',
  'go.mod': 'Text',
  license: 'Text',
  licence: 'Text',
  copying: 'Text',
  readme: 'Text',
  authors: 'Text',
  changelog: 'Text',
  contributors: 'Text',
  version: 'Text',
};

/** Path segments that mark vendored / generated content (excluded from stats). */
const VENDORED_DIRS = new Set(['node_modules', 'vendor', 'dist', 'build', '.git', 'bower_components', 'third_party', '__pycache__', '.venv', 'venv']);

function basename(p: string): string {
  const i = p.lastIndexOf('/');
  return i === -1 ? p : p.slice(i + 1);
}

/** Language definition for a path, or null when unknown. */
export function detectLanguageDef(path: string): LanguageDef | null {
  const base = basename(path);
  const lower = base.toLowerCase();
  const byName = BY_FILENAME[lower];
  if (byName) return LANGS[byName];
  // "Dockerfile.dev", "Makefile.am"
  if (lower.startsWith('dockerfile.')) return LANGS.Dockerfile;
  if (lower.startsWith('makefile.')) return LANGS.Makefile;
  const dot = lower.lastIndexOf('.');
  if (dot <= 0) return null;
  const ext = lower.slice(dot + 1);
  const byExt = BY_EXT[ext];
  return byExt ? LANGS[byExt] : null;
}

/** Language name for a path (null when unknown). */
export function detectLanguage(path: string): string | null {
  return detectLanguageDef(path)?.name ?? null;
}

/** Colour for a language name (null when unknown). */
export function languageColor(name: string): string | null {
  for (const def of Object.values(LANGS)) if (def.name === name) return def.color;
  return null;
}

/** Whether a path is in a vendored/generated location or is a minified asset. */
export function isVendoredPath(path: string): boolean {
  const segs = path.split('/');
  const name = segs[segs.length - 1] ?? '';
  if (segs.slice(0, -1).some((s) => VENDORED_DIRS.has(s.toLowerCase()))) return true;
  if (/\.min\.[a-z0-9]+$/i.test(name)) return true;
  return false;
}

/** Whether a file counts toward language stats. */
export function countsTowardStats(file: Pick<FileInfo, 'path' | 'binary' | 'language'>): boolean {
  if (file.binary || !file.language) return false;
  if (isVendoredPath(file.path)) return false;
  const def = detectLanguageDef(file.path);
  if (!def || def.prose) return false;
  return true;
}

/**
 * Bytes per language over the given files (docs/config/vendored/binary excluded), sorted
 * by bytes desc then name, with percents summing to 100 (drift folded into the largest).
 */
export function languageStats(files: Iterable<FileInfo>): LanguageStat[] {
  const bytes = new Map<string, number>();
  for (const f of files) {
    if (!countsTowardStats(f)) continue;
    bytes.set(f.language!, (bytes.get(f.language!) ?? 0) + f.size);
  }
  const total = Array.from(bytes.values()).reduce((a, b) => a + b, 0);
  if (total === 0) return [];
  const stats: LanguageStat[] = Array.from(bytes.entries())
    .map(([name, b]) => ({ name, bytes: b, percent: Math.round((b / total) * 1000) / 10, color: languageColor(name) }))
    .sort((a, b) => b.bytes - a.bytes || (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
  const sum = stats.reduce((a, s) => a + s.percent, 0);
  const drift = Math.round((100 - sum) * 10) / 10;
  if (drift !== 0 && stats[0]) stats[0].percent = Math.round((stats[0].percent + drift) * 10) / 10;
  return stats;
}
