/**
 * The one place the deploy base path (`site.base`, 0.2.0) is read. Everything that emits a
 * site-internal URL goes through `withBase()` — the `src/lib/routes.ts` builders, the
 * chrome components' handful of literal hrefs, and the islands' fallbacks — so a site
 * deployed under `/mysite` prefixes every link, and a root deploy emits exactly the URLs
 * it always has.
 *
 * Browser-safe by construction: the value comes from `import.meta.env.BASE_URL`, which
 * Astro derives from `site.base` (via astro.config.ts) and Vite inlines statically into
 * server AND island bundles — no config import, no node built-ins, so Svelte islands can
 * use it. The guarded read matters because this module is also loaded entirely outside
 * Vite (`npm run measure` under tsx, the Playwright suite), where `import.meta.env` does
 * not exist; there the base is '' unless a test sets one.
 */

let override: string | null = null;

/** Normalise any spelling to '' (root) or '/prefix' with no trailing slash. */
function normalize(value: string): string {
  const trimmed = value.trim().replace(/\/+$/, '');
  if (trimmed === '' || trimmed === '/') return '';
  return trimmed.startsWith('/') ? trimmed : `/${trimmed}`;
}

/** `''` for a root deploy, `'/mysite'` (leading slash, no trailing slash) otherwise. */
export function siteBase(): string {
  if (override !== null) return override;
  const env = (import.meta as { env?: { BASE_URL?: string } }).env;
  return normalize(env?.BASE_URL ?? '/');
}

/** Prefix a root-relative path (`'/repos/'`) with the deploy base. */
export function withBase(path: string): string {
  return `${siteBase()}${path}`;
}

/**
 * Test seam: force a base (or `null` to return to the environment's). Unit tests use it to
 * pin the builders under a non-default base, since the real value is inlined at build time
 * and cannot vary inside one process.
 */
export function setSiteBase(base: string | null): void {
  override = base === null ? null : normalize(base);
}
