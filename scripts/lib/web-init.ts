/**
 * `npm run frznforge -- init --web` — the same wizard as the terminal flow, in a browser tab.
 *
 * Picking twelve repositories out of an account of ninety is miserable as a numbered list, and
 * pleasant as a filterable table with checkboxes. So this module stands up a tiny local HTTP
 * server, opens a page against it, and reuses `scripts/cli.ts` for every decision that matters:
 * the listing (`listRepos`), the entry shapes (`entriesFor`), the textual splice
 * (`insertRepos`/`updateConfigFile`). The browser is a *view*; nothing new is decided in it.
 *
 * The security model, because a page on localhost is still a page:
 *
 *  - **Bound to 127.0.0.1 only.** Never `0.0.0.0`: the wizard can write a file, so it must not
 *    be reachable from the network the machine happens to be on.
 *  - **Per-run session token.** Minted with `node:crypto`, printed in the URL, and required on
 *    every request — page and API. Without it another local process, or any web page the user
 *    happens to have open, could drive the wizard: `127.0.0.1` is same-origin-ish to nobody,
 *    but it *is* reachable by a `fetch()` from any site in the browser. The token is the thing
 *    that makes that fail.
 *  - **Host and Origin pinning.** A `Host` header that is not `127.0.0.1:<port>` /
 *    `localhost:<port>` is refused, which is what stops DNS rebinding (an attacker's name
 *    resolving to 127.0.0.1 arrives with *their* host header). API requests carrying a foreign
 *    `Origin` are refused too.
 *  - **The provider token never reaches the browser, and only ever goes to a host the terminal
 *    authorised.** The page asks the server for a listing; the server calls the provider.
 *    `/api/context` reports the *name* of the environment variable a token came from and
 *    nothing else, and every error is scrubbed before it is sent. The host is the subtle part:
 *    the page has a free-text "Custom API host" field, so a host typed (or posted) there would
 *    otherwise receive an `Authorization: Bearer <PAT>` for a URL the user never vetted. A
 *    listing therefore only carries the token when its host is the provider's own default, or
 *    the exact `--host` the user put on their own command line for that same provider
 *    (`hostTrustedForToken`). Any other host is listed **unauthenticated**, and the page is
 *    told so.
 *  - **The browser never names the file.** Writes always go to the path resolved here, from
 *    `--config` or `findConfigFile()`.
 */
import { spawn } from 'node:child_process';
import { randomBytes, timingSafeEqual } from 'node:crypto';
import fs from 'node:fs/promises';
import http from 'node:http';
import type { AddressInfo } from 'node:net';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { redactToken } from '../../src/lib/importers/types';
import {
  EXCLUDE_FILTERS,
  PROVIDERS,
  PROVIDER_NAMES,
  SAFE_FIELD,
  assertSafeHost,
  entriesFor,
  findConfigFile,
  insertRepos,
  listRepos,
  renderSnippet,
  tokenStatus,
  updateConfigFile,
  type Io,
  type ProviderName,
  type RemoteRepo,
  type RepoEntry,
} from '../cli';

/* ------------------------------------------------------------------ options */

export interface WebInitOptions {
  provider?: ProviderName;
  host?: string;
  account?: string;
  releases?: 'provider' | 'tags';
  /** Config file to write; when omitted the server resolves it with findConfigFile(). */
  configPath?: string;
  /** 0 or undefined = pick a free ephemeral port. */
  port?: number;
  /** Skip launching a browser (tests, headless boxes). */
  noOpen?: boolean;
  io: Io;
  fetchImpl?: typeof fetch;
}

/** A forgotten tab must not leave a writable server running forever. */
export const INACTIVITY_MS = 15 * 60 * 1000;

/** Bodies bigger than this are refused outright — the real ones are a few kilobytes. */
const MAX_BODY_BYTES = 1024 * 1024;

/** Exit codes `runWebInit` can resolve to. 130 is the conventional SIGINT code. */
const EXIT = { ok: 0, failed: 1, interrupted: 130 } as const;

/* ------------------------------------------------------------------ request guards */

/**
 * `Host` must name the loopback interface we are actually listening on.
 *
 * This is the DNS-rebinding guard: `evil.example` re-resolved to 127.0.0.1 still sends
 * `Host: evil.example`, so pinning the header keeps the wizard reachable only through a URL
 * the user could have typed themselves.
 */
export function hostAllowed(header: string | undefined, port: number): boolean {
  if (!header) return false;
  return header === `127.0.0.1:${port}` || header === `localhost:${port}` || header === `[::1]:${port}`;
}

/** An absent `Origin` is fine (a typed-in URL has none); a foreign one never is. */
export function originAllowed(header: string | undefined, port: number): boolean {
  if (header === undefined || header === '' || header === 'null') return true;
  return header === `http://127.0.0.1:${port}` || header === `http://localhost:${port}` || header === `http://[::1]:${port}`;
}

/** Compare two API bases the way `entryKey` does: no trailing slash, case-insensitive. */
function sameHost(a: string | null | undefined, b: string | null | undefined): boolean {
  if (!a || !b) return false;
  return a.replace(/\/+$/, '').toLowerCase() === b.replace(/\/+$/, '').toLowerCase();
}

/**
 * May the environment's provider token be sent to this host?
 *
 * Only two hosts qualify: the provider's own default API base, and the `--host` the user typed
 * on their own command line *for that same provider*. Everything else — including anything the
 * browser types into "Custom API host" — is listed without credentials. Binding the CLI host to
 * the CLI provider matters: without it, `--provider=gitea --host=https://intranet` would let
 * the page switch to GitHub and post the GitHub PAT to that same intranet box.
 */
export function hostTrustedForToken(
  provider: ProviderName,
  host: string,
  cli: { provider?: ProviderName; host?: string } = {},
): boolean {
  if (sameHost(host, PROVIDERS[provider].defaultHost)) return true;
  return cli.provider === provider && sameHost(host, cli.host);
}

/** Constant-time token comparison, so a wrong guess leaks nothing through timing. */
function tokenMatches(presented: string | null | undefined, expected: string): boolean {
  if (typeof presented !== 'string') return false;
  const a = Buffer.from(presented, 'utf8');
  const b = Buffer.from(expected, 'utf8');
  if (a.length !== b.length) return false;
  return timingSafeEqual(a, b);
}

/* ------------------------------------------------------------------ payload validation */

/**
 * `SAFE_FIELD` (shared with `entryFor`, which applies it to listing-derived values) is what
 * stops a typo or a hostile API response from putting nonsense in a config file the user will
 * read for years. `renderEntry` escaping quotes, backslashes and line terminators is the other
 * half; neither is trusted to be the only one.
 */
const MAX_ENTRIES = 500;

function badRequest(message: string): Error {
  const error = new Error(message);
  (error as { status?: number }).status = 400;
  return error;
}

function asRecord(value: unknown): Record<string, unknown> {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) throw badRequest('expected a JSON object');
  return value as Record<string, unknown>;
}

function asProvider(value: unknown): ProviderName {
  if (typeof value === 'string' && (PROVIDER_NAMES as string[]).includes(value)) return value as ProviderName;
  throw badRequest(`unknown provider: ${JSON.stringify(value)}`);
}

function asReleases(value: unknown): 'provider' | 'tags' {
  if (value === undefined || value === 'provider') return 'provider';
  if (value === 'tags') return 'tags';
  throw badRequest("releases must be 'provider' or 'tags'");
}

/** Host as the browser typed it: http(s) only, no credentials, no query. */
function asHost(value: unknown, required: boolean): string {
  if (typeof value !== 'string' || value.trim() === '') {
    if (required) throw badRequest('a host is required for this provider');
    return '';
  }
  const text = value.trim().replace(/\/+$/, '');
  // `assertSafeHost` validates the *text*, not just what `new URL` makes of it: the WHATWG
  // parser strips tabs and newlines before parsing, so `https://a.com/x\ny` parses fine and the
  // newline would survive into the config file's string literal and break it.
  try {
    assertSafeHost(text);
  } catch (error) {
    throw badRequest(error instanceof Error ? error.message : String(error));
  }
  return text;
}

/**
 * An account is a user, an org, or a GitLab group path. `listRepos` URL-encodes it, so a `..`
 * cannot escape anything — but a config full of `../../etc` is nobody's intent, so say no here
 * rather than send it to a provider and report its 404.
 */
function asAccount(value: unknown): string {
  if (typeof value !== 'string' || value.trim() === '') throw badRequest('an account is required');
  const text = value.trim();
  if (!SAFE_FIELD.test(text)) throw badRequest(`account contains characters that cannot be in a path: ${text}`);
  const segments = text.split('/');
  if (segments.some((s) => s === '' || s === '.' || s === '..')) throw badRequest(`not an account name: ${text}`);
  return text;
}

function asField(value: unknown, key: string): string | undefined {
  if (value === undefined || value === null) return undefined;
  if (typeof value !== 'string' || !SAFE_FIELD.test(value)) throw badRequest(`invalid ${key}: ${JSON.stringify(value)}`);
  return value;
}

/** Validate one browser-supplied entry before it can reach the config file. */
function asEntry(value: unknown): RepoEntry {
  const raw = asRecord(value);
  const entry: RepoEntry = { type: asProvider(raw.type) };
  const host = raw.host === undefined || raw.host === null ? '' : asHost(raw.host, false);
  if (host) entry.host = host;
  entry.owner = asField(raw.owner, 'owner');
  entry.repo = asField(raw.repo, 'repo');
  entry.project = asField(raw.project, 'project');
  entry.slug = asField(raw.slug, 'slug');
  if (raw.releases !== undefined) entry.releases = asReleases(raw.releases);
  if (!entry.project && !(entry.owner && entry.repo)) throw badRequest('an entry needs owner+repo, or project');
  return entry;
}

/* ------------------------------------------------------------------ the page */

const PAGE_FILE = fileURLToPath(new URL('./web-init-page.html', import.meta.url));
let pageCache: string | null = null;

/** The wizard's HTML. Static — nothing is interpolated into it, so it cannot carry an injection. */
export async function renderPage(): Promise<string> {
  if (pageCache === null) pageCache = await fs.readFile(PAGE_FILE, 'utf8');
  return pageCache;
}

/* ------------------------------------------------------------------ server */

interface Listing {
  key: string;
  provider: ProviderName;
  host: string;
  account: string;
  repos: RemoteRepo[];
}

function listingKey(provider: string, host: string, account: string): string {
  return `${provider}|${host.toLowerCase()}|${account.toLowerCase()}`;
}

/** `https://api.github.com` → `api.github.com`, for a label the page can show. */
function hostLabel(host: string): string {
  try {
    return new URL(host).host;
  } catch {
    return host;
  }
}

export async function runWebInit(opts: WebInitOptions): Promise<number> {
  const io = opts.io;
  const configPath = opts.configPath ? path.resolve(io.cwd, opts.configPath) : await findConfigFile(io.cwd);
  const sessionToken = randomBytes(24).toString('base64url');

  let listing: Listing | null = null;
  /**
   * One write per run, and never two at once.
   *
   * `/api/write` is read-modify-write on a file, and nothing binds the session to a single tab:
   * the URL is printed in the terminal and `openBrowser` may already have opened one, so two
   * tabs can both offer a working **Write to config** button. Without this latch each request
   * read the original file, spliced its own entry, and wrote — every one answering 200
   * "added 1" while only the last one's entry survived. The flag is set synchronously before
   * the first `await`, which on a single-threaded event loop is all the mutual exclusion a
   * read-modify-write needs.
   */
  let writeState: 'idle' | 'running' | 'done' = 'idle';
  let settled = false;
  let resolveDone!: (code: number) => void;
  const done = new Promise<number>((resolve) => {
    resolveDone = resolve;
  });

  /* ---------------------------------------------------------------- helpers bound to the run */

  /** Never let a provider token reach the browser, even through an error message. */
  const scrub = (value: unknown, token?: string | null): string => {
    const text = value instanceof Error ? value.message : String(value);
    return redactToken(text, token ?? null);
  };

  const send = (res: http.ServerResponse, status: number, type: string, body: string, last = false): void => {
    const buffer = Buffer.from(body, 'utf8');
    res.writeHead(status, {
      'content-type': type,
      'content-length': String(buffer.byteLength),
      'cache-control': 'no-store',
      'x-content-type-options': 'nosniff',
      'referrer-policy': 'no-referrer',
      ...(last ? { connection: 'close' } : {}),
    });
    res.end(buffer);
  };

  const sendJson = (res: http.ServerResponse, status: number, body: unknown, last = false): void =>
    send(res, status, 'application/json; charset=utf-8', JSON.stringify(body), last);

  const server = http.createServer((req, res) => {
    handle(req, res).catch((error) => {
      const status = typeof (error as { status?: number }).status === 'number' ? (error as { status: number }).status : 500;
      if (!res.headersSent) sendJson(res, status, { error: scrub(error) });
      else res.end();
    });
  });

  const port = (): number => (server.address() as AddressInfo | null)?.port ?? 0;

  /* ---------------------------------------------------------------- lifecycle */

  let idleTimer: NodeJS.Timeout | null = null;
  const bump = (): void => {
    if (idleTimer) clearTimeout(idleTimer);
    idleTimer = setTimeout(() => {
      finish(EXIT.failed, `No activity for ${Math.round(INACTIVITY_MS / 60000)} minutes — the init server has stopped. Nothing was written.`);
    }, INACTIVITY_MS);
    idleTimer.unref?.();
  };

  const onSigint = (): void => finish(EXIT.interrupted, '\nInterrupted — nothing was written.');

  /**
   * Stop listening, then hang up. `close()` alone waits on idle keep-alive sockets the page
   * left behind, so idle ones are dropped immediately and the rest get a short grace period.
   */
  const shutdown = (): Promise<void> =>
    new Promise((resolve) => {
      let once = false;
      const finished = (): void => {
        if (once) return;
        once = true;
        resolve();
      };
      server.close(finished);
      server.closeIdleConnections?.();
      const grace = setTimeout(() => {
        server.closeAllConnections?.();
        finished();
      }, 400);
      grace.unref?.();
    });

  function finish(code: number, message?: string): void {
    if (settled) return;
    settled = true;
    if (idleTimer) clearTimeout(idleTimer);
    process.off('SIGINT', onSigint);
    if (message) io.log(message);
    void shutdown().then(() => resolveDone(code));
  }

  /** Close only once the response has actually left the socket. */
  const finishAfter = (res: http.ServerResponse, code: number, message?: string): void => {
    res.on('close', () => finish(code, message));
    if (res.writableEnded) finish(code, message);
  };

  /* ---------------------------------------------------------------- request handling */

  async function readBody(req: http.IncomingMessage): Promise<unknown> {
    const chunks: Buffer[] = [];
    let size = 0;
    for await (const chunk of req) {
      size += (chunk as Buffer).byteLength;
      if (size > MAX_BODY_BYTES) throw badRequest('request body is too large');
      chunks.push(chunk as Buffer);
    }
    const text = Buffer.concat(chunks).toString('utf8');
    if (text.trim() === '') return {};
    try {
      return JSON.parse(text);
    } catch {
      throw badRequest('body is not JSON');
    }
  }

  /** Entries either come pre-built (`entries`) or are named against the cached listing. */
  function entriesFrom(body: Record<string, unknown>): RepoEntry[] {
    if (Array.isArray(body.entries)) {
      if (body.entries.length > MAX_ENTRIES) throw badRequest('too many entries');
      return body.entries.map(asEntry);
    }
    const provider = asProvider(body.provider);
    const host = asHost(body.host, PROVIDERS[provider].hostRequired) || PROVIDERS[provider].defaultHost || '';
    const account = asAccount(body.account);
    const releases = asReleases(body.releases);
    const select = Array.isArray(body.select) ? body.select : null;
    if (!select) throw badRequest('expected "entries" or "select"');
    if (select.length > MAX_ENTRIES) throw badRequest('too many entries');

    const key = listingKey(provider, host, account);
    if (!listing || listing.key !== key) throw badRequest('that repository list is no longer loaded — load it again');
    const wanted = new Set(select.filter((s): s is string => typeof s === 'string'));
    const picked = listing.repos.filter((r) => wanted.has(r.fullName));
    if (picked.length === 0) throw badRequest('nothing selected');
    return entriesFor(provider, host, picked, releases);
  }

  async function previewFor(entries: RepoEntry[]): Promise<{ snippet: string; changed: boolean; added: number; skipped: number }> {
    const snippet = renderSnippet(entries);
    if (!configPath) return { snippet, changed: false, added: entries.length, skipped: 0 };
    const source = await fs.readFile(configPath, 'utf8').catch(() => null);
    const result = source === null ? null : insertRepos(source, entries);
    if (!result) return { snippet, changed: false, added: entries.length, skipped: 0 };
    return { snippet, changed: result.changed, added: result.added.length, skipped: result.skipped.length };
  }

  async function handle(req: http.IncomingMessage, res: http.ServerResponse): Promise<void> {
    const bound = port();
    if (!hostAllowed(req.headers.host, bound)) {
      send(res, 403, 'text/plain; charset=utf-8', 'forbidden: this server only answers on 127.0.0.1\n');
      return;
    }
    const url = new URL(req.url ?? '/', `http://127.0.0.1:${bound}`);
    const presented = url.searchParams.get('s') ?? (req.headers['x-frznforge-session'] as string | undefined) ?? null;
    if (!tokenMatches(presented, sessionToken)) {
      send(res, 403, 'text/plain; charset=utf-8', 'forbidden: missing or wrong session key — open the URL printed in the terminal\n');
      return;
    }
    if (url.pathname.startsWith('/api/') && !originAllowed(req.headers.origin, bound)) {
      send(res, 403, 'text/plain; charset=utf-8', 'forbidden: cross-origin request\n');
      return;
    }
    bump();

    if (url.pathname === '/' || url.pathname === '/index.html') {
      if (req.method !== 'GET' && req.method !== 'HEAD') {
        sendJson(res, 405, { error: 'method not allowed' });
        return;
      }
      const html = await renderPage();
      const buffer = Buffer.from(html, 'utf8');
      res.writeHead(200, {
        'content-type': 'text/html; charset=utf-8',
        'content-length': String(buffer.byteLength),
        'cache-control': 'no-store',
        'x-content-type-options': 'nosniff',
        'referrer-policy': 'no-referrer',
        // The page is self-contained; this makes "no external requests" a rule the browser enforces.
        'content-security-policy':
          "default-src 'none'; connect-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; img-src data:; form-action 'none'; base-uri 'none'; frame-ancestors 'none'",
      });
      res.end(req.method === 'HEAD' ? undefined : buffer);
      return;
    }

    if (url.pathname === '/api/context') {
      const providers: Record<string, unknown> = {};
      for (const name of PROVIDER_NAMES) {
        const info = PROVIDERS[name];
        const status = tokenStatus(name, io.env);
        providers[name] = {
          label: info.label,
          defaultHost: info.defaultHost,
          hostRequired: info.hostRequired,
          hostSuggestion: info.hostSuggestion,
          accountLabel: info.accountLabel,
          tokenScopes: info.tokenScopes,
          // Names only. `status.token` is deliberately not forwarded.
          token: { names: status.names, from: status.from, found: status.token !== null },
        };
      }
      sendJson(res, 200, {
        order: PROVIDER_NAMES,
        providers,
        defaults: {
          provider: opts.provider ?? 'github',
          host: opts.host ?? null,
          account: opts.account ?? null,
          releases: opts.releases ?? 'provider',
        },
        configPath,
        configName: configPath ? path.basename(configPath) : null,
      });
      return;
    }

    if (req.method !== 'POST') {
      sendJson(res, 405, { error: 'method not allowed' });
      return;
    }
    const body = asRecord(await readBody(req));

    if (url.pathname === '/api/repos') {
      const provider = asProvider(body.provider);
      const info = PROVIDERS[provider];
      const host = asHost(body.host, info.hostRequired) || info.defaultHost || '';
      const account = asAccount(body.account);
      const status = tokenStatus(provider, io.env);
      // The one rule that keeps a PAT out of a stranger's logs: a host the terminal did not
      // authorise gets an anonymous request, never an Authorization header.
      const trusted = hostTrustedForToken(provider, host, { provider: opts.provider, host: opts.host });
      const token = trusted ? status.token : null;
      const warnings: string[] = [];
      if (!trusted && status.token !== null) {
        warnings.push(
          `${hostLabel(host)} is not ${PROVIDERS[provider].label}'s own API host, so $${status.from} was not sent to it — ` +
            `only public repositories are listed. Restart with --host=${host} if that host really is your instance.`,
        );
      }
      let repos: RemoteRepo[];
      try {
        repos = await listRepos(provider, {
          host,
          account,
          token,
          fetchImpl: opts.fetchImpl ?? io.fetchImpl,
          warn: (line) => warnings.push(line),
        });
      } catch (error) {
        sendJson(res, 502, { error: scrub(error, status.token) });
        return;
      }
      listing = { key: listingKey(provider, host, account), provider, host, account, repos };
      sendJson(res, 200, {
        provider,
        host,
        hostLabel: hostLabel(host),
        account,
        warnings,
        tokenSent: trusted && status.token !== null,
        // Which quick filters this listing can actually answer for. GitLab's project listings
        // report neither forks nor (for users) archived state, and a toggle that silently does
        // nothing is worse than one that is visibly unavailable.
        flags: Object.fromEntries(EXCLUDE_FILTERS.map((f) => [f.one, repos.every((r) => f.known(r))])),
        repos,
      });
      return;
    }

    if (url.pathname === '/api/preview') {
      sendJson(res, 200, { ...(await previewFor(entriesFrom(body))), configPath });
      return;
    }

    if (url.pathname === '/api/write') {
      if (!configPath) {
        sendJson(res, 409, { error: 'no frznforge.config.ts was found — copy the snippet into your config by hand' });
        return;
      }
      // Validated before the latch, so a malformed request cannot wedge the endpoint.
      const entries = entriesFrom(body);
      if (writeState !== 'idle') {
        sendJson(res, 409, {
          error:
            writeState === 'running'
              ? 'a write is already in progress — wait for it to finish'
              : `${configPath} has already been written by this session; re-run init to add more`,
        });
        return;
      }
      writeState = 'running';
      let result: Awaited<ReturnType<typeof updateOrExplain>>;
      try {
        result = await updateOrExplain(configPath, entries);
      } catch (error) {
        writeState = 'idle';
        sendJson(res, 409, { error: `could not write ${configPath}: ${scrub(error)}` });
        return;
      }
      if ('error' in result) {
        writeState = 'idle';
        sendJson(res, 409, result);
        return;
      }
      writeState = 'done';
      sendJson(
        res,
        200,
        { configPath, backup: result.backup, added: result.added.length, skipped: result.skipped.length, changed: result.changed },
        true,
      );
      if (result.changed) {
        io.log(`Wrote ${result.added.length} entr${result.added.length === 1 ? 'y' : 'ies'} to ${configPath}.`);
        if (result.backup) io.log(`Backup: ${result.backup}`);
        io.log('Next: npm run build   (remote repos are mirror-cloned into .frznforge-cache/ on the first run)');
      } else {
        io.log(`Everything selected was already in ${configPath} — nothing written.`);
      }
      finishAfter(res, EXIT.ok);
      return;
    }

    if (url.pathname === '/api/cancel') {
      sendJson(res, 200, { cancelled: true }, true);
      finishAfter(res, EXIT.ok, 'Cancelled — nothing was written.');
      return;
    }

    sendJson(res, 404, { error: `no such endpoint: ${url.pathname}` });
  }

  /* ---------------------------------------------------------------- go */

  await new Promise<void>((resolve, reject) => {
    server.once('error', reject);
    server.listen(opts.port && opts.port > 0 ? opts.port : 0, '127.0.0.1', () => {
      server.off('error', reject);
      resolve();
    });
  });

  const url = `http://127.0.0.1:${port()}/?s=${sessionToken}`;
  io.log('frznforge init — the wizard is open in your browser.');
  io.log(`  ${url}`);
  io.log('  Only this machine can reach it, and only with the key in that URL. Ctrl-C to stop.');
  if (!configPath) io.log('  No frznforge.config.ts found — the wizard will show a snippet to paste instead.');
  bump();
  process.once('SIGINT', onSigint);
  server.on('error', (error) => finish(EXIT.failed, `error: ${scrub(error)}`));
  if (!opts.noOpen) openBrowser(url, io);

  return done;
}

/**
 * `updateConfigFile`, with the one unrecoverable case turned into something sayable.
 *
 * "Nothing changed" is *not* an error: re-running the wizard over repos that are already in the
 * config is the idempotent path the terminal flow also treats as a success.
 */
async function updateOrExplain(
  file: string,
  entries: RepoEntry[],
): Promise<{ backup: string | null; added: RepoEntry[]; skipped: RepoEntry[]; changed: boolean } | { error: string }> {
  const result = await updateConfigFile(file, entries);
  if (!result) return { error: `could not find a repos: [ … ] array in ${file} — paste the snippet in by hand` };
  return result;
}

/**
 * Best-effort browser launch. A failure is not an error: the URL is on stdout either way, which
 * is also the only thing that works over SSH.
 */
function openBrowser(url: string, io: Io): void {
  const [command, args] =
    process.platform === 'win32'
      ? [process.env.ComSpec ?? 'cmd.exe', ['/d', '/s', '/c', 'start', '', url]]
      : process.platform === 'darwin'
        ? ['open', [url]]
        : ['xdg-open', [url]];
  try {
    const child = spawn(command, args, { detached: true, stdio: 'ignore', windowsHide: true });
    child.on('error', () => io.log('  (could not open a browser automatically — open the URL above)'));
    child.unref();
  } catch {
    io.log('  (could not open a browser automatically — open the URL above)');
  }
}
