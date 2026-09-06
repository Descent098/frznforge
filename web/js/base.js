/**
 * The deploy base path (`site.base`), for code running in the browser.
 *
 * The server half (`src/lib/base.ts`) reads `import.meta.env.BASE_URL`, which Vite inlines at
 * build time. Nothing inlines anything here — these bytes are served to the browser exactly as
 * written — so the base has to arrive through the DOM instead: the site shell stamps it on
 * `<html data-base="/mysite">` and this module reads it back.
 *
 * `normalize` is imported by the server half, so the two agree on what a base *is* by
 * construction rather than by two copies of a regex.
 *
 * See `web/js/format.js` for the rules this folder follows.
 */

/**
 * Normalise any spelling to '' (root) or '/prefix' with no trailing slash.
 * @param {string} value
 * @returns {string}
 */
export function normalize(value) {
  const trimmed = value.trim().replace(/\/+$/, '');
  if (trimmed === '' || trimmed === '/') return '';
  return trimmed.startsWith('/') ? trimmed : `/${trimmed}`;
}

/**
 * `''` for a root deploy, `'/mysite'` (leading slash, no trailing slash) otherwise.
 *
 * Read fresh each call rather than cached at module load: the element scripts are modules and
 * may evaluate before or after the attribute is meaningful, and the read is a property lookup.
 *
 * @returns {string}
 */
export function siteBase() {
  if (typeof document === 'undefined') return '';
  return normalize(document.documentElement.dataset.base ?? '');
}

/**
 * Prefix a root-relative path (`'/repos/'`) with the deploy base.
 * @param {string} path
 * @returns {string}
 */
export function withBase(path) {
  return `${siteBase()}${path}`;
}
