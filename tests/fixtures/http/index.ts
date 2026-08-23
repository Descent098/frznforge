import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const FIXTURE_DIR = path.dirname(fileURLToPath(import.meta.url));

/** One canned HTTP response, keyed in {@link fixtureFetch} by a URL substring. */
export interface FixtureRoute {
  /** HTTP status to return. Defaults to 200. */
  status?: number;
  /** Body, serialised as JSON. Omit for an empty body (e.g. a bare 304). */
  json?: unknown;
  /** Extra response headers, merged over `content-type: application/json`. */
  headers?: Record<string, string>;
}

/**
 * Build a `fetch` stand-in that serves recorded fixtures instead of hitting the network.
 *
 * Routes are matched by *substring* of the request URL, in declaration order, so the most
 * specific pattern must come first (`/releases` before `/repos/owner/name`, for example).
 * An unmatched URL throws rather than falling through to a default, so a test that forgets
 * a route fails loudly and names the URL it was missing.
 *
 * @param routes URL substring → canned response.
 * @returns A function assignable to `globalThis.fetch`.
 *
 * @example
 * const fetchImpl = fixtureFetch({
 *   '/releases': { json: loadFixture('github/releases.json') },
 *   '/repos/': { json: loadFixture('github/repo.json') },
 * });
 */
export function fixtureFetch(routes: Record<string, FixtureRoute>): typeof fetch {
  const entries = Object.entries(routes);
  const impl = async (
    input: RequestInfo | URL,
    _init?: RequestInit,
  ): Promise<Response> => {
    const url = requestUrl(input);
    for (const [pattern, route] of entries) {
      if (!url.includes(pattern)) continue;
      const status = route.status ?? 200;
      const body = route.json === undefined ? null : JSON.stringify(route.json);
      return new Response(body, {
        status,
        // Response rejects a statusText with non-token characters, so leave it to the default.
        headers: {
          'content-type': 'application/json; charset=utf-8',
          ...(route.headers ?? {}),
        },
      });
    }
    throw new Error(
      `fixtureFetch: no route matches ${url}\n  known patterns: ${
        entries.length ? entries.map(([p]) => p).join(', ') : '(none)'
      }`,
    );
  };
  return impl as unknown as typeof fetch;
}

/**
 * Read and parse a JSON fixture stored in this directory.
 *
 * @param relPath Path relative to `tests/fixtures/http`, e.g. `"github/repo.json"`.
 *   Forward slashes are fine on Windows — the path is re-joined per platform.
 */
export function loadFixture(relPath: string): unknown {
  const full = path.join(FIXTURE_DIR, ...relPath.split(/[\\/]/));
  return JSON.parse(readFileSync(full, 'utf8'));
}

/** Normalise the many things `fetch` accepts into a plain URL string. */
function requestUrl(input: RequestInfo | URL): string {
  if (typeof input === 'string') return input;
  if (input instanceof URL) return input.toString();
  return input.url;
}
