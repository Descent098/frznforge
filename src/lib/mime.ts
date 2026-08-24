/**
 * Extension → content-type mapping for the static raw endpoints (repo blobs and note files).
 *
 * A static host decides the real `Content-Type` at serve time from the file extension, so this
 * table only governs the response Astro builds — what `astro preview` and the e2e server hand
 * back. It exists as one shared module rather than a copy per endpoint because the two must
 * never disagree about, say, whether `.svg` is served as an image.
 *
 * Pure and dependency-free: no node imports, so it stays usable from anywhere.
 */

/**
 * Lowercased extension (no dot) → content type. Small on purpose: it covers what actually
 * turns up in a source repo or a notes folder, and everything else falls back to plain text.
 */
const MIME: Record<string, string> = {
  png: 'image/png',
  jpg: 'image/jpeg',
  jpeg: 'image/jpeg',
  gif: 'image/gif',
  webp: 'image/webp',
  svg: 'image/svg+xml',
  ico: 'image/x-icon',
  json: 'application/json',
  pdf: 'application/pdf',
  zip: 'application/zip',
  wasm: 'application/wasm',
  html: 'text/html; charset=utf-8',
  htm: 'text/html; charset=utf-8',
  css: 'text/css; charset=utf-8',
  js: 'text/javascript; charset=utf-8',
  mjs: 'text/javascript; charset=utf-8',
  woff: 'font/woff',
  woff2: 'font/woff2',
  ttf: 'font/ttf',
  mp3: 'audio/mpeg',
  mp4: 'video/mp4',
  webm: 'video/webm',
};

/**
 * Content type for a file path.
 *
 * Defaults to UTF-8 plain text: an unknown extension in a source repo is far more likely to be
 * text than a binary the browser should download, and serving it as `application/octet-stream`
 * would turn "view the raw file" into a download prompt.
 *
 * @param path File path or name; only the final extension is inspected.
 * @returns A `Content-Type` header value.
 */
export function mimeFor(path: string): string {
  const dot = path.lastIndexOf('.');
  const ext = dot === -1 ? '' : path.slice(dot + 1).toLowerCase();
  return MIME[ext] ?? 'text/plain; charset=utf-8';
}
