/**
 * Shared plumbing for the provider importers: a tiny JSON-over-HTTP client plus the
 * normalisation helpers every provider needs to turn its payloads into schema shapes.
 *
 * Why it exists: the four providers differ in auth header, pagination header and error body,
 * but everything else — timeouts, one retry, "never leak the token", mapping a failure onto an
 * {@link ImporterError} kind — is identical, and getting that wrong is what breaks builds.
 *
 * Two rules this module enforces for everyone:
 *  - **The token only ever travels in a request header.** It is never put in a URL and never
 *    interpolated into a message; every message is run through {@link redactToken} anyway.
 *  - **Nothing volatile is normalised.** Download counters, star counts and "now" never make
 *    it into a value returned from here (the one exception is `retryAfter`, which is a hint to
 *    the caller and never reaches the artifact).
 */
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import type { Release, ReleaseAsset } from '../data/schema';
import { DEFAULT_USER_AGENT, ImporterError, redactToken } from './types';

/* ---- client ------------------------------------------------------------- */

/**
 * How a provider wants its token presented:
 *  - `bearer`        — `Authorization: Bearer <t>` (GitHub)
 *  - `private-token` — `PRIVATE-TOKEN: <t>` (GitLab)
 *  - `token`         — `Authorization: token <t>` (Gitea, Forgejo)
 */
export type AuthScheme = 'bearer' | 'private-token' | 'token';

/** Requests are abandoned after this long; an unreachable forge must not stall a build. */
export const DEFAULT_TIMEOUT_MS = 20_000;
/**
 * Hard cap on pages followed by {@link JsonClient.getAll}, so a huge repo cannot hang a build.
 * Hitting it is reported ({@link PagedResult.truncated}), never silently swallowed.
 */
export const DEFAULT_MAX_PAGES = 20;
/** Backoff before the single retry of a 5xx/network failure. */
const DEFAULT_RETRY_DELAY_MS = 500;
/** One retry, i.e. two attempts total. */
const ATTEMPTS = 2;

export interface JsonClientOptions {
  /** Token presentation for this provider. */
  auth: AuthScheme;
  /** Injected for tests; defaults to the global `fetch`. */
  fetchImpl?: typeof fetch;
  /** Resolved API token, or null/absent for an anonymous client. */
  token?: string | null;
  userAgent?: string;
  /** `Accept` header; defaults to `application/json`. */
  accept?: string;
  timeoutMs?: number;
  /** Backoff before the retry. Tests pass 0 to keep the suite fast. */
  retryDelayMs?: number;
  maxPages?: number;
}

/** One paginated `getAll` walk: the items, and whether the page cap cut it short. */
export interface PagedResult<T> {
  items: T[];
  /** True when the provider still had a next page when {@link JsonClientOptions.maxPages} ran out. */
  truncated: boolean;
  /** Pages actually fetched. */
  pages: number;
  /** The cap that was in force. */
  maxPages: number;
}

/**
 * A `GET`-only JSON client for one provider API.
 *
 * Every failure surfaces as an {@link ImporterError} with a kind the caller can map onto a
 * `remote-*` warning; nothing here throws a bare `Error`.
 */
export class JsonClient {
  private readonly fetchImpl: typeof fetch;
  private readonly token: string | null;
  private readonly userAgent: string;
  private readonly auth: AuthScheme;
  private readonly accept: string;
  private readonly timeoutMs: number;
  private readonly retryDelayMs: number;
  private readonly maxPages: number;

  constructor(options: JsonClientOptions) {
    this.auth = options.auth;
    this.fetchImpl = options.fetchImpl ?? globalThis.fetch;
    this.token = options.token ?? null;
    this.userAgent = options.userAgent ?? defaultUserAgent();
    this.accept = options.accept ?? 'application/json';
    this.timeoutMs = options.timeoutMs ?? DEFAULT_TIMEOUT_MS;
    this.retryDelayMs = options.retryDelayMs ?? DEFAULT_RETRY_DELAY_MS;
    this.maxPages = options.maxPages ?? DEFAULT_MAX_PAGES;
  }

  /** Fetch one JSON document. */
  async get<T>(url: string): Promise<T> {
    const response = await this.request(url);
    return (await this.readJson(url, response)) as T;
  }

  /**
   * Fetch a JSON array, following pagination until the provider stops offering a next page or
   * {@link JsonClientOptions.maxPages} is reached. Handles both styles in use:
   * `Link: <…>; rel="next"` (GitHub/Gitea/Forgejo) and `x-next-page` (GitLab).
   *
   * `truncated` is true when the page cap stopped the walk while the provider was still
   * offering more. The caller must surface that: a short list and a capped one look identical
   * in the artifact, and silently dropping a repo's older releases is worse than saying so.
   */
  async getAll<T>(url: string): Promise<PagedResult<T>> {
    const items: T[] = [];
    let next: string | null = url;
    let page = 0;
    for (; next !== null && page < this.maxPages; page += 1) {
      const current: string = next;
      const response = await this.request(current);
      const body = await this.readJson(current, response);
      if (!Array.isArray(body)) {
        throw new ImporterError(
          'bad-response',
          `${describe(current, this.token)} did not return a JSON array`,
          { status: response.status },
        );
      }
      items.push(...(body as T[]));
      next = nextPageUrl(response, current);
    }
    return { items, truncated: next !== null, pages: page, maxPages: this.maxPages };
  }

  /** Headers for every request. The token appears here and nowhere else. */
  private headers(): Record<string, string> {
    const headers: Record<string, string> = {
      accept: this.accept,
      'user-agent': this.userAgent,
    };
    if (this.token) {
      if (this.auth === 'private-token') headers['private-token'] = this.token;
      else headers['authorization'] = `${this.auth === 'bearer' ? 'Bearer' : 'token'} ${this.token}`;
    }
    return headers;
  }

  /**
   * One request with a timeout and a single retry on a network error or a 5xx. A 5xx that
   * survives the retry is returned and classified as `network` by {@link this.readJson}: from
   * the build's point of view a broken forge and an unreachable one are the same thing.
   */
  private async request(url: string): Promise<Response> {
    let cause: unknown;
    for (let attempt = 1; attempt <= ATTEMPTS; attempt += 1) {
      try {
        const response = await this.send(url);
        if (response.status < 500 || attempt === ATTEMPTS) return response;
        cause = new Error(`HTTP ${response.status}`);
      } catch (err) {
        cause = err;
      }
      if (attempt < ATTEMPTS && this.retryDelayMs > 0) await sleep(this.retryDelayMs);
    }
    throw new ImporterError(
      'network',
      `${describe(url, this.token)} failed: ${redactToken(errorText(cause), this.token)}`,
      { cause },
    );
  }

  private async send(url: string): Promise<Response> {
    const signal = timeoutSignal(this.timeoutMs);
    const init: RequestInit = { method: 'GET', headers: this.headers(), redirect: 'follow' };
    if (signal) init.signal = signal;
    return await this.fetchImpl(url, init);
  }

  /** Parse a response body, or throw the right {@link ImporterError} for a failed one. */
  private async readJson(url: string, response: Response): Promise<unknown> {
    const text = await bodyText(response);
    if (!response.ok) throw this.toError(url, response, text);
    if (text.trim() === '') {
      throw new ImporterError('bad-response', `${describe(url, this.token)} returned an empty body`, {
        status: response.status,
      });
    }
    try {
      return JSON.parse(text) as unknown;
    } catch (err) {
      throw new ImporterError(
        'bad-response',
        `${describe(url, this.token)} returned a body that is not JSON`,
        { status: response.status, cause: err },
      );
    }
  }

  /**
   * Map a non-2xx response onto an {@link ImporterError}. Never mentions the token.
   *
   * The message ends up verbatim inside a `Warning` in `forge.json`, which constrains it in
   * two ways the raw provider response does not respect:
   *  - **nothing clock-derived.** `retryAfter` is a countdown, so it differs between two
   *    builds of the same repos; it stays on the error object for the console and is kept out
   *    of the message.
   *  - **no host identity.** GitHub's anonymous rate-limit body quotes the caller's public IP
   *    ("rate limit exceeded for 203.0.113.1"), which has no business being published in a
   *    build artifact — {@link scrubIps} takes it out.
   */
  private toError(url: string, response: Response, text: string): ImporterError {
    const detail = scrubIps(redactToken(providerMessage(text), this.token));
    const retryAfter = parseRetryAfter(response.headers);
    const kind = classifyStatus(response.status, response.headers, detail);
    const parts = [`${describe(url, this.token)} → HTTP ${response.status}`];
    if (detail) parts.push(detail);
    if (kind === 'auth' && !this.token) parts.push('no token configured for this source');
    return new ImporterError(kind, parts.join(' — '), {
      status: response.status,
      ...(retryAfter !== undefined ? { retryAfter } : {}),
    });
  }
}

/**
 * Replace IPv4/IPv6 literals with `[ip]`.
 *
 * Provider error bodies quote the requester's own address (GitHub's anonymous rate-limit
 * message is the common one). Those bodies are copied into artifact warnings, so the build
 * host's public IP would otherwise be published with the site.
 */
export function scrubIps(text: string): string {
  return text
    .replace(/\b\d{1,3}(?:\.\d{1,3}){3}\b/g, '[ip]')
    // Four or more colon-separated hextets: enough to exclude a `10:30:45` clock time.
    .replace(/\b(?:[0-9a-f]{1,4}:){3,7}[0-9a-f]{1,4}\b/gi, '[ip]');
}

/* ---- failure classification --------------------------------------------- */

/**
 * HTTP status → {@link ImporterError} kind.
 *
 * 401/403 are ambiguous: GitHub answers a rate limit with 403 and a bad token with 401, so the
 * headers decide. Anything 5xx counts as `network` — after the retry it is indistinguishable
 * from an unreachable host and gets the same "use the cache" treatment.
 */
export function classifyStatus(
  status: number,
  headers: Headers,
  detail = '',
): 'auth' | 'not-found' | 'rate-limit' | 'network' | 'bad-response' {
  if (status === 404 || status === 410) return 'not-found';
  if (status === 429) return 'rate-limit';
  if (status === 401 || status === 403) {
    return isRateLimited(headers, detail) ? 'rate-limit' : 'auth';
  }
  if (status >= 500) return 'network';
  return 'bad-response';
}

/** True when the response carries a rate-limit signal rather than an auth failure. */
function isRateLimited(headers: Headers, detail: string): boolean {
  const remaining = headers.get('x-ratelimit-remaining') ?? headers.get('ratelimit-remaining');
  if (remaining !== null && Number(remaining) === 0) return true;
  if (headers.get('retry-after') !== null) return true;
  return /rate limit|too many requests|throttl/i.test(detail);
}

/**
 * Seconds to wait before retrying, from `Retry-After` (delta seconds or HTTP date) or a
 * rate-limit reset header (epoch seconds), or undefined when the response says nothing.
 */
export function parseRetryAfter(headers: Headers, now: number = Date.now()): number | undefined {
  const retryAfter = headers.get('retry-after');
  if (retryAfter !== null && retryAfter.trim() !== '') {
    const seconds = Number(retryAfter);
    if (Number.isFinite(seconds)) return Math.max(0, Math.round(seconds));
    const at = Date.parse(retryAfter);
    if (!Number.isNaN(at)) return Math.max(0, Math.ceil((at - now) / 1000));
  }
  const reset = headers.get('x-ratelimit-reset') ?? headers.get('ratelimit-reset');
  if (reset !== null && reset.trim() !== '') {
    const value = Number(reset);
    if (Number.isFinite(value)) {
      // Big values are an epoch (GitHub, GitLab); small ones are already a delta.
      const seconds = value > 1e9 ? (value * 1000 - now) / 1000 : value;
      return Math.max(0, Math.ceil(seconds));
    }
  }
  return undefined;
}

/** Best-effort human message out of a provider's error body (all four use `message`/`error`). */
export function providerMessage(text: string): string {
  if (text.trim() === '') return '';
  try {
    const body = JSON.parse(text) as Record<string, unknown>;
    for (const key of ['message', 'error_description', 'error']) {
      const value = body?.[key];
      if (typeof value === 'string' && value.trim() !== '') return value.trim();
    }
  } catch {
    // A non-JSON error body (an HTML proxy page, say) tells us nothing worth quoting.
  }
  return '';
}

/* ---- pagination ---------------------------------------------------------- */

/** Next page URL from the response headers, or null when this was the last page. */
export function nextPageUrl(response: Response, currentUrl: string): string | null {
  const link = response.headers.get('link');
  if (link) {
    const next = parseLinkHeader(link).next;
    if (next) return new URL(next, currentUrl).toString();
  }
  // GitLab: no Link header on every deployment, but always x-next-page (empty on the last page).
  const nextPage = response.headers.get('x-next-page');
  if (nextPage !== null && nextPage.trim() !== '') {
    const url = new URL(currentUrl);
    url.searchParams.set('page', nextPage.trim());
    return url.toString();
  }
  return null;
}

/** `<url>; rel="next", <url>; rel="last"` → `{ next, last }`. */
export function parseLinkHeader(value: string): Record<string, string> {
  const links: Record<string, string> = {};
  for (const part of value.split(',')) {
    const match = /<([^>]+)>\s*;\s*(.+)/.exec(part.trim());
    if (!match) continue;
    const rel = /rel\s*=\s*"?([^";]+)"?/.exec(match[2]!);
    if (rel) links[rel[1]!.trim()] = match[1]!.trim();
  }
  return links;
}

/* ---- normalisation shared by the providers ------------------------------- */

/** A trailing `Z` or `±HH:MM` / `±HHMM` offset — i.e. the timestamp says which zone it is in. */
const EXPLICIT_ZONE = /(?:Z|[+-]\d{2}:?\d{2})$/i;

/**
 * A provider timestamp normalised to the artifact's `IsoDate` (UTC, seconds precision), or
 * null when it is missing/unparseable. Providers disagree wildly — GitLab sends milliseconds,
 * Forgejo sends `+02:00` offsets — and the artifact has to be byte-identical between builds.
 *
 * A date-*time* carrying no zone (`2026-08-20T10:49:50`, or the space-separated form some
 * self-hosted Gitea/Forgejo deployments emit) is read as UTC rather than handed to
 * `Date.parse`, which would interpret it as the build machine's local time and shift the
 * value in the artifact by that machine's offset. A bare date already means UTC to `Date.parse`.
 */
export function toIsoDate(value: unknown): string | null {
  if (typeof value !== 'string') return null;
  const raw = value.trim();
  if (raw === '') return null;
  const zoneless = /\d{2}:\d{2}/.test(raw) && !EXPLICIT_ZONE.test(raw);
  const ms = Date.parse(zoneless ? `${raw.replace(' ', 'T')}Z` : raw);
  if (Number.isNaN(ms)) return null;
  return `${new Date(ms).toISOString().slice(0, 19)}Z`;
}

/**
 * Resolve a provider URL against the repo's web base, so a host-relative one (GitLab hands
 * out `/group/proj/-/releases/…/downloads/x` for release assets) does not end up in the
 * artifact as a link that resolves against the *generated site* and 404s. Returns the input
 * unchanged when it cannot be resolved.
 */
export function absoluteUrl(value: string, base: string): string {
  try {
    return new URL(value, base.endsWith('/') ? base : `${base}/`).toString();
  } catch {
    return value;
  }
}

/** Trimmed string, or null when absent/empty. Providers use `""` and `null` interchangeably. */
export function nullIfEmpty(value: unknown): string | null {
  if (typeof value !== 'string') return null;
  const trimmed = value.trim();
  return trimmed === '' ? null : trimmed;
}

/** Non-empty strings out of a value that should have been a string array. */
export function stringArray(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  return value.filter((v): v is string => typeof v === 'string' && v.trim() !== '').map((v) => v.trim());
}

/** A non-negative integer byte count, or 0 when the provider reports none. */
export function byteSize(value: unknown): number {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 0) return 0;
  return Math.floor(value);
}

/** Locale-independent string order, so the artifact does not depend on the build machine. */
export function compareStrings(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0;
}

/** Assets in a stable order: by name, then URL for ties. */
export function sortAssets(assets: ReleaseAsset[]): ReleaseAsset[] {
  return [...assets].sort((a, b) => compareStrings(a.name, b.name) || compareStrings(a.url, b.url));
}

/** Releases newest first: `publishedAt` desc, `tag` asc as tiebreak. */
export function sortReleases(releases: Release[]): Release[] {
  return [...releases].sort(
    (a, b) => compareStrings(b.publishedAt, a.publishedAt) || compareStrings(a.tag, b.tag),
  );
}

/**
 * Canonical SPDX id for a provider license key.
 *
 * GitHub reports `spdx_id` already canonical, GitLab reports a lowercase key (`"mit"`) and
 * Gitea an SPDX-ish string; this maps the ids frznforge's own detector emits so a repo shows
 * the same license whether it was sniffed locally or imported.
 */
const SPDX_BY_KEY: Record<string, string> = {
  '0bsd': '0BSD',
  'agpl-3.0': 'AGPL-3.0-only',
  'apache-2.0': 'Apache-2.0',
  'artistic-2.0': 'Artistic-2.0',
  'bsd-2-clause': 'BSD-2-Clause',
  'bsd-3-clause': 'BSD-3-Clause',
  'bsl-1.0': 'BSL-1.0',
  'cc0-1.0': 'CC0-1.0',
  'epl-2.0': 'EPL-2.0',
  'eupl-1.2': 'EUPL-1.2',
  'gpl-2.0': 'GPL-2.0-only',
  'gpl-3.0': 'GPL-3.0-only',
  isc: 'ISC',
  'lgpl-2.1': 'LGPL-2.1-only',
  'lgpl-3.0': 'LGPL-3.0-only',
  mit: 'MIT',
  'mit-0': 'MIT-0',
  'mpl-2.0': 'MPL-2.0',
  'ms-pl': 'MS-PL',
  ncsa: 'NCSA',
  'ofl-1.1': 'OFL-1.1',
  'osl-3.0': 'OSL-3.0',
  postgresql: 'PostgreSQL',
  unlicense: 'Unlicense',
  'upl-1.0': 'UPL-1.0',
  wtfpl: 'WTFPL',
  zlib: 'Zlib',
};

/**
 * Normalise a provider license id. Unknown ids are passed through unchanged rather than
 * guessed at; `NOASSERTION` (GitHub's "there is a LICENSE but we can't tell what it is") and
 * `other` become null so the scanner's own detection wins.
 */
export function toSpdx(value: unknown): string | null {
  const raw = nullIfEmpty(value);
  if (raw === null) return null;
  const key = raw.toLowerCase();
  if (key === 'noassertion' || key === 'other' || key === 'unknown') return null;
  return SPDX_BY_KEY[key] ?? raw;
}

/* ---- misc ---------------------------------------------------------------- */

/** `frznforge/<version>`, or the bare product name when VERSION is unreadable. */
let cachedUserAgent: string | null = null;
export function defaultUserAgent(): string {
  if (cachedUserAgent !== null) return cachedUserAgent;
  cachedUserAgent = DEFAULT_USER_AGENT;
  try {
    const here = path.dirname(fileURLToPath(import.meta.url));
    const version = readFileSync(path.join(here, '..', '..', '..', 'VERSION'), 'utf8').trim();
    if (version !== '') cachedUserAgent = `${DEFAULT_USER_AGENT}/${version}`;
  } catch {
    // No VERSION file (installed as a dependency, say) — the bare name is a valid User-Agent.
  }
  return cachedUserAgent;
}

/** Strip the query string from a URL for messages: it can carry ids we have no need to echo. */
function describe(url: string, token: string | null): string {
  let display = url;
  try {
    const parsed = new URL(url);
    display = `${parsed.origin}${parsed.pathname}`;
  } catch {
    // Not a parseable URL; show it as given (still redacted below).
  }
  return `GET ${redactToken(display, token)}`;
}

/** A response body as text, tolerating a stream that fails mid-read. */
async function bodyText(response: Response): Promise<string> {
  try {
    return await response.text();
  } catch {
    return '';
  }
}

function errorText(cause: unknown): string {
  if (cause instanceof Error) return cause.message;
  return String(cause);
}

/** An abort signal that fires after `ms`, when the runtime offers one. */
function timeoutSignal(ms: number): AbortSignal | undefined {
  const ctor = globalThis.AbortSignal as (typeof AbortSignal & { timeout?: (ms: number) => AbortSignal }) | undefined;
  return typeof ctor?.timeout === 'function' ? ctor.timeout(ms) : undefined;
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
