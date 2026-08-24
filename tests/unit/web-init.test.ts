/**
 * `npm run frznforge -- init --web` — the local wizard server.
 *
 * The real `node:http` server is started on port 0 and driven over real HTTP, because the whole
 * point of this module is the request handling: the session key, the Host/Origin pinning, and
 * the fact that a provider token can never come back out. A stubbed `fetchImpl` stands in for
 * the provider, so nothing here touches a socket it did not open itself.
 */
import fs from 'node:fs/promises';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { insertRepos, type Io } from '../../scripts/cli';
import { hostAllowed, hostTrustedForToken, originAllowed, runWebInit, type WebInitOptions } from '../../scripts/lib/web-init';

/* ------------------------------------------------------------------ fixtures */

/** A value that must never appear in anything the server sends to the browser. */
const SENTINEL_TOKEN = 'ghp_SENTINEL_never_leaves_the_process_0123456789';

const FIXTURE_CONFIG = `// frznforge site configuration.
import { defineConfig } from './src/lib/config/schema';

export default defineConfig({
  owner: { name: 'Kieran Wood', handle: 'kieran' },

  /**
   * Repositories to ingest.
   *   { type: 'local', path: '../useful' },
   */
  repos: [
    // Self-host demo: the frznforge repo itself.
    { type: 'local', path: '.', slug: 'frznforge' },
  ],

  ingest: {
    outDir: './data', // keep this comment
  },
});
`;

const GITHUB_PAGE = [
  { name: 'ezcv', full_name: 'me/ezcv', owner: { login: 'me' }, description: 'Static CV generator' },
  { name: 'sdu', full_name: 'me/sdu', owner: { login: 'me' }, description: 'Disk usage', fork: true },
  { name: 'attic', full_name: 'me/attic', owner: { login: 'me' }, description: null, archived: true, private: true },
];

/** A `fetch` that answers the GitHub listing endpoint from memory and 404s everything else. */
function stubFetch(page: unknown[] = GITHUB_PAGE): typeof fetch {
  return (async (input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    if (url.includes('/users/me/repos')) {
      return new Response(JSON.stringify(page), { status: 200, headers: { 'content-type': 'application/json' } });
    }
    return new Response(JSON.stringify({ message: 'Not Found' }), { status: 404 });
  }) as typeof fetch;
}

let tmp: string;

beforeEach(async () => {
  tmp = await fs.mkdtemp(path.join(os.tmpdir(), 'frznforge-web-'));
});

afterEach(async () => {
  await fs.rm(tmp, { recursive: true, force: true });
});

/* ------------------------------------------------------------------ harness */

interface Running {
  /** `http://127.0.0.1:<port>` */
  origin: string;
  port: number;
  token: string;
  out: string[];
  err: string[];
  /** Resolves to the exit code once the server has stopped. */
  exit: Promise<number>;
}

function testIo(overrides: Partial<Io> = {}): Io & { out: string[]; err: string[] } {
  const out: string[] = [];
  const err: string[] = [];
  return {
    out,
    err,
    log: (line) => out.push(line),
    error: (line) => err.push(line),
    isTty: false,
    env: { FRZNFORGE_GITHUB_TOKEN: SENTINEL_TOKEN },
    cwd: tmp,
    ...overrides,
  };
}

/** Start the real server and wait for it to print its URL. Never opens a browser. */
async function start(options: Partial<Omit<WebInitOptions, 'io'>> & { io?: Partial<Io> } = {}): Promise<Running> {
  const io = testIo(options.io);
  const exit = runWebInit({ noOpen: true, port: 0, fetchImpl: stubFetch(), ...options, io });
  // Swallow late rejections; every test asserts on the code through `exit`.
  exit.catch(() => undefined);

  for (let waited = 0; waited < 4000; waited += 10) {
    const line = io.out.find((l) => l.includes('http://127.0.0.1:'));
    if (line) {
      const url = new URL(line.trim());
      return { origin: url.origin, port: Number(url.port), token: url.searchParams.get('s')!, out: io.out, err: io.err, exit };
    }
    await new Promise((r) => setTimeout(r, 10));
  }
  throw new Error(`the server never printed a URL:\n${io.out.join('\n')}`);
}

function get(run: Running, pathname: string, token = run.token): Promise<Response> {
  return fetch(`${run.origin}${pathname}?s=${encodeURIComponent(token)}`);
}

function post(run: Running, pathname: string, body: unknown, token = run.token): Promise<Response> {
  return fetch(`${run.origin}${pathname}?s=${encodeURIComponent(token)}`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(body),
  });
}

/**
 * A raw request, so headers browsers refuse to let scripts set (`Host`, `Origin`) can be
 * spoofed the way an attacker would.
 */
function raw(
  run: Running,
  options: { path: string; method?: string; headers?: Record<string, string>; body?: string },
): Promise<{ status: number; body: string; headers: http.IncomingHttpHeaders }> {
  return new Promise((resolve, reject) => {
    const req = http.request(
      { host: '127.0.0.1', port: run.port, path: options.path, method: options.method ?? 'GET', headers: options.headers },
      (res) => {
        let body = '';
        res.setEncoding('utf8');
        res.on('data', (chunk) => (body += chunk));
        res.on('end', () => resolve({ status: res.statusCode ?? 0, body, headers: res.headers }));
      },
    );
    req.on('error', reject);
    if (options.body) req.write(options.body);
    req.end();
  });
}

/** Everything a response could hide a secret in: every header, plus the body. */
async function fullText(res: Response): Promise<string> {
  const headers = [...res.headers.entries()].map(([k, v]) => `${k}: ${v}`).join('\n');
  return `${headers}\n\n${await res.clone().text()}`;
}

/** The body as JSON, having first asserted that nothing in the whole response leaks the token. */
async function cleanJson<T>(res: Response): Promise<T> {
  expect(await fullText(res)).not.toContain(SENTINEL_TOKEN);
  return (await res.json()) as T;
}

async function writeConfig(): Promise<string> {
  const file = path.join(tmp, 'frznforge.config.ts');
  await fs.writeFile(file, FIXTURE_CONFIG, 'utf8');
  return file;
}

const SELECT_ALL = { provider: 'github', host: 'https://api.github.com', account: 'me', releases: 'provider', select: ['me/ezcv', 'me/sdu', 'me/attic'] };

/* ------------------------------------------------------------------ guards */

describe('request guards (pure)', () => {
  it('accepts only the loopback names of the port it is listening on', () => {
    expect(hostAllowed('127.0.0.1:4321', 4321)).toBe(true);
    expect(hostAllowed('localhost:4321', 4321)).toBe(true);
    expect(hostAllowed('127.0.0.1:9999', 4321)).toBe(false);
    expect(hostAllowed('evil.example', 4321)).toBe(false);
    expect(hostAllowed(undefined, 4321)).toBe(false);
  });

  it('allows a missing Origin but never a foreign one', () => {
    expect(originAllowed(undefined, 4321)).toBe(true);
    expect(originAllowed('http://127.0.0.1:4321', 4321)).toBe(true);
    expect(originAllowed('https://evil.example', 4321)).toBe(false);
    expect(originAllowed('http://127.0.0.1:4322', 4321)).toBe(false);
  });
});

/* ------------------------------------------------------------------ the session key */

describe('the session key', () => {
  it('serves the page to the URL it printed', async () => {
    const run = await start();
    const res = await get(run, '/');
    expect(res.status).toBe(200);
    expect(res.headers.get('content-type')).toContain('text/html');
    const html = await res.text();
    expect(html).toContain('frznforge init');
    expect(html).toContain('Load repositories');
    // A page that cannot reach anything but its own server.
    expect(res.headers.get('content-security-policy')).toContain("default-src 'none'");
    expect(res.headers.get('content-security-policy')).toContain("connect-src 'self'");
    await post(run, '/api/cancel', {});
    expect(await run.exit).toBe(0);
  });

  it('refuses the page and the API without a key, and with the wrong one', async () => {
    const run = await start();
    for (const target of ['/', '/api/context']) {
      const none = await fetch(`${run.origin}${target}`);
      expect(none.status).toBe(403);
      expect(none.headers.get('content-type')).toContain('text/plain');
      expect(await none.text()).toContain('session key');

      const wrong = await get(run, target, 'not-the-key');
      expect(wrong.status).toBe(403);
    }
    // A wrong key of exactly the right length must fail too (the compare is constant-time).
    const sameLength = await get(run, '/api/context', 'x'.repeat(run.token.length));
    expect(sameLength.status).toBe(403);

    await post(run, '/api/cancel', {});
    expect(await run.exit).toBe(0);
  });

  it('refuses a foreign Host header even with a valid key', async () => {
    const run = await start();
    const res = await raw(run, { path: `/?s=${run.token}`, headers: { host: 'evil.example' } });
    expect(res.status).toBe(403);
    expect(res.body).toContain('127.0.0.1');

    const ok = await raw(run, { path: `/?s=${run.token}`, headers: { host: `localhost:${run.port}` } });
    expect(ok.status).toBe(200);

    await post(run, '/api/cancel', {});
    expect(await run.exit).toBe(0);
  });

  it('refuses a foreign Origin on the API even with a valid key', async () => {
    const run = await start();
    const res = await raw(run, {
      path: `/api/repos?s=${run.token}`,
      method: 'POST',
      headers: { host: `127.0.0.1:${run.port}`, origin: 'https://evil.example', 'content-type': 'application/json' },
      body: JSON.stringify({ provider: 'github', account: 'me' }),
    });
    expect(res.status).toBe(403);
    expect(res.body).toContain('cross-origin');

    await post(run, '/api/cancel', {});
    expect(await run.exit).toBe(0);
  });
});

/* ------------------------------------------------------------------ endpoints */

describe('/api/context', () => {
  it('names the token variable but never carries the token', async () => {
    const run = await start();
    const res = await get(run, '/api/context');
    expect(res.status).toBe(200);
    const body = await cleanJson<{
      order: string[];
      providers: Record<string, { token: { names: string[]; from: string | null; found: boolean }; hostRequired: boolean }>;
      configPath: string | null;
    }>(res);
    expect(body.order).toEqual(['github', 'gitlab', 'gitea', 'forgejo']);
    expect(body.providers.github!.token).toEqual({ names: ['FRZNFORGE_GITHUB_TOKEN', 'GITHUB_TOKEN'], from: 'FRZNFORGE_GITHUB_TOKEN', found: true });
    expect(body.providers.gitlab!.token.found).toBe(false);
    expect(body.providers.gitea!.hostRequired).toBe(true);
    expect(body.configPath).toBe(null); // nothing in the temp cwd yet

    await post(run, '/api/cancel', {});
    expect(await run.exit).toBe(0);
  });

  it('reports the resolved config path when there is one', async () => {
    const file = await writeConfig();
    const run = await start();
    const body = (await (await get(run, '/api/context')).json()) as { configPath: string; configName: string };
    expect(body.configPath).toBe(file);
    expect(body.configName).toBe('frznforge.config.ts');
    await post(run, '/api/cancel', {});
    expect(await run.exit).toBe(0);
  });
});

describe('/api/repos', () => {
  it('returns the provider listing, sorted, with the flags the table needs', async () => {
    const run = await start();
    const res = await post(run, '/api/repos', { provider: 'github', host: 'https://api.github.com', account: 'me' });
    expect(res.status).toBe(200);
    const body = await cleanJson<{ repos: Array<Record<string, unknown>>; hostLabel: string }>(res);
    expect(body.hostLabel).toBe('api.github.com');
    expect(body.repos.map((r) => r.fullName)).toEqual(['me/attic', 'me/ezcv', 'me/sdu']);
    expect(body.repos[0]).toMatchObject({ name: 'attic', archived: true, private: true, fork: false });
    expect(body.repos[2]).toMatchObject({ name: 'sdu', fork: true, description: 'Disk usage' });

    await post(run, '/api/cancel', {});
    expect(await run.exit).toBe(0);
  });

  it('turns a provider failure into a 502 with no token in it', async () => {
    const run = await start();
    const res = await post(run, '/api/repos', { provider: 'github', host: 'https://api.github.com', account: 'nobody' });
    expect(res.status).toBe(502);
    const text = await fullText(res);
    expect(text).toContain('404');
    expect(text).not.toContain(SENTINEL_TOKEN);
    await post(run, '/api/cancel', {});
    expect(await run.exit).toBe(0);
  });

  it('rejects an account that could not be a path segment', async () => {
    const run = await start();
    const res = await post(run, '/api/repos', { provider: 'github', account: '../../etc/passwd\n' });
    expect(res.status).toBe(400);
    await post(run, '/api/cancel', {});
    expect(await run.exit).toBe(0);
  });
});

describe('/api/preview', () => {
  it('renders exactly the snippet the write will splice in', async () => {
    await writeConfig();
    const run = await start();
    await post(run, '/api/repos', { provider: 'github', host: 'https://api.github.com', account: 'me' });
    const body = (await (await post(run, '/api/preview', { ...SELECT_ALL, select: ['me/ezcv'] })).json()) as {
      snippet: string;
      added: number;
      changed: boolean;
    };
    expect(body.snippet).toBe("  repos: [\n    { type: 'github', owner: 'me', repo: 'ezcv', releases: 'provider' },\n  ],");
    expect(body).toMatchObject({ added: 1, changed: true });

    await post(run, '/api/cancel', {});
    expect(await run.exit).toBe(0);
  });

  it('refuses a selection whose listing was never loaded', async () => {
    const run = await start();
    const res = await post(run, '/api/preview', SELECT_ALL);
    expect(res.status).toBe(400);
    expect(await res.text()).toContain('load it again');
    await post(run, '/api/cancel', {});
    expect(await run.exit).toBe(0);
  });
});

/* ------------------------------------------------------------------ writing */

describe('/api/write', () => {
  it('splices the entries in, keeps a .bak, and leaves the file parseable and idempotent', async () => {
    const file = await writeConfig();
    const run = await start();
    await post(run, '/api/repos', { provider: 'github', host: 'https://api.github.com', account: 'me' });
    const res = await post(run, '/api/write', { ...SELECT_ALL, select: ['me/ezcv', 'me/sdu'] });
    expect(res.status).toBe(200);
    const body = await cleanJson<{ backup: string; added: number; configPath: string }>(res);
    expect(body).toMatchObject({ added: 2, configPath: file });

    const written = await fs.readFile(file, 'utf8');
    expect(written).toContain("{ type: 'github', owner: 'me', repo: 'ezcv', releases: 'provider' },");
    expect(written).toContain("{ type: 'github', owner: 'me', repo: 'sdu', releases: 'provider' },");
    // Everything outside the array survived.
    expect(written).toContain('// Self-host demo: the frznforge repo itself.');
    expect(written).toContain("outDir: './data', // keep this comment");
    expect(written).toContain("{ type: 'local', path: '.', slug: 'frznforge' },");

    // The backup is the file exactly as it was.
    const backup = await fs.readFile(body.backup, 'utf8');
    expect(backup).toBe(FIXTURE_CONFIG);
    expect(path.dirname(body.backup)).toBe(tmp);
    expect(body.backup.endsWith('.bak')).toBe(true);

    // Re-splicing the same entries is a no-op: the write is idempotent.
    const again = insertRepos(written, [
      { type: 'github', owner: 'me', repo: 'ezcv', releases: 'provider' },
      { type: 'github', owner: 'me', repo: 'sdu', releases: 'provider' },
    ]);
    expect(again).not.toBeNull();
    expect(again!.changed).toBe(false);
    expect(again!.text).toBe(written);

    expect(await run.exit).toBe(0);
  });

  it('accepts pre-built entries as well as a selection', async () => {
    const file = await writeConfig();
    const run = await start();
    const res = await post(run, '/api/write', {
      entries: [{ type: 'forgejo', host: 'https://codeberg.org', owner: 'me', repo: 'tool', releases: 'tags' }],
    });
    expect(res.status).toBe(200);
    expect(await fs.readFile(file, 'utf8')).toContain(
      "{ type: 'forgejo', host: 'https://codeberg.org', owner: 'me', repo: 'tool', releases: 'tags' },",
    );
    expect(await run.exit).toBe(0);
  });

  it('never writes a path the browser chose', async () => {
    const file = await writeConfig();
    const decoy = path.join(tmp, 'decoy.ts');
    await fs.writeFile(decoy, FIXTURE_CONFIG, 'utf8');
    const run = await start();
    const res = await post(run, '/api/write', {
      configPath: decoy,
      file: decoy,
      entries: [{ type: 'github', owner: 'me', repo: 'ezcv' }],
    });
    expect(res.status).toBe(200);
    expect((await res.json() as { configPath: string }).configPath).toBe(file);
    expect(await fs.readFile(decoy, 'utf8')).toBe(FIXTURE_CONFIG);
    expect(await fs.readFile(file, 'utf8')).toContain("repo: 'ezcv'");
    expect(await run.exit).toBe(0);
  });

  it('rejects an entry with characters that have no business in a config', async () => {
    await writeConfig();
    const run = await start();
    const res = await post(run, '/api/write', { entries: [{ type: 'github', owner: "me', evil: '", repo: 'x' }] });
    expect(res.status).toBe(400);
    expect(await res.text()).toContain('invalid owner');
    await post(run, '/api/cancel', {});
    expect(await run.exit).toBe(0);
  });

  it('adds nothing on a second run over the same config', async () => {
    const file = await writeConfig();
    const entries = [{ type: 'github', owner: 'me', repo: 'ezcv', releases: 'provider' }];

    const first = await start();
    expect((await post(first, '/api/write', { entries })).status).toBe(200);
    expect(await first.exit).toBe(0);
    const afterFirst = await fs.readFile(file, 'utf8');

    const second = await start();
    const res = await post(second, '/api/write', { entries });
    expect(res.status).toBe(200);
    expect(await res.json()).toMatchObject({ added: 0, skipped: 1, changed: false, backup: null });
    expect(await second.exit).toBe(0);

    const afterSecond = await fs.readFile(file, 'utf8');
    expect(afterSecond).toBe(afterFirst);
    expect(afterSecond.split("repo: 'ezcv'").length - 1).toBe(1);
  });

  it('stops the server once the write is answered', async () => {
    await writeConfig();
    const run = await start();
    expect((await post(run, '/api/write', { entries: [{ type: 'github', owner: 'me', repo: 'ezcv' }] })).status).toBe(200);
    expect(await run.exit).toBe(0);
    await expect(get(run, '/api/context')).rejects.toThrow();
  });
});

/* ------------------------------------------------------------------ cancel */

describe('cancel', () => {
  it('exits 0 and touches nothing', async () => {
    const file = await writeConfig();
    const run = await start();
    await post(run, '/api/repos', { provider: 'github', host: 'https://api.github.com', account: 'me' });
    const res = await post(run, '/api/cancel', {});
    expect(res.status).toBe(200);
    expect(await run.exit).toBe(0);
    expect(await fs.readFile(file, 'utf8')).toBe(FIXTURE_CONFIG);
    expect(await fs.readdir(tmp)).toEqual(['frznforge.config.ts']); // no .bak
    expect(run.out.join('\n')).toContain('nothing was written');
    await expect(get(run, '/api/context')).rejects.toThrow();
  });
});

/* ------------------------------------------------------------------ secrets */

describe('the provider token', () => {
  it('appears in no response body or header from any endpoint', async () => {
    await writeConfig();
    const run = await start();
    const seen: string[] = [];
    seen.push(await fullText(await get(run, '/')));
    seen.push(await fullText(await get(run, '/api/context')));
    seen.push(await fullText(await post(run, '/api/repos', { provider: 'github', account: 'me' })));
    seen.push(await fullText(await post(run, '/api/repos', { provider: 'github', account: 'nobody' })));
    seen.push(await fullText(await post(run, '/api/preview', { ...SELECT_ALL, select: ['me/ezcv'] })));
    seen.push(await fullText(await post(run, '/api/nope', {})));
    seen.push(await fullText(await get(run, '/api/context', 'wrong')));
    seen.push(await fullText(await post(run, '/api/write', { entries: [{ type: 'github', owner: 'me', repo: 'ezcv' }] })));
    expect(await run.exit).toBe(0);

    for (const text of seen) expect(text).not.toContain(SENTINEL_TOKEN);
    // The URL the wizard printed carries a session key, not the provider token.
    expect(run.out.join('\n')).not.toContain(SENTINEL_TOKEN);
  });

  /**
   * The exfiltration path the response-body assertions above cannot see: the token going
   * *out*, to a host the browser chose. `/api/repos` takes a host from the page, so a URL
   * pasted into "Custom API host" would otherwise receive `Authorization: Bearer <PAT>`.
   */
  describe('is only ever sent to a host the terminal authorised', () => {
    /** A `fetch` that records every request and answers each listing with one repo. */
    function recordingFetch(seen: Array<{ url: string; auth: string | null; privateToken: string | null }>): typeof fetch {
      return (async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
        const headers = new Headers(init?.headers);
        seen.push({ url, auth: headers.get('authorization'), privateToken: headers.get('private-token') });
        const body = url.includes('/users/me/') ? [{ name: 'ezcv', full_name: 'me/ezcv', owner: { login: 'me' } }] : { message: 'Not Found' };
        return new Response(JSON.stringify(body), { status: Array.isArray(body) ? 200 : 404 });
      }) as typeof fetch;
    }

    it('sends nothing to a host the browser invented, and says so', async () => {
      const seen: Array<{ url: string; auth: string | null; privateToken: string | null }> = [];
      const run = await start({ fetchImpl: recordingFetch(seen) });
      const res = await post(run, '/api/repos', { provider: 'github', host: 'http://127.0.0.1:8799', account: 'me' });
      const body = await cleanJson<{ tokenSent: boolean; warnings: string[] }>(res);

      expect(seen.length).toBeGreaterThan(0);
      for (const call of seen) {
        expect(call.url).toContain('127.0.0.1:8799');
        expect(call.auth).toBe(null);
        expect(call.privateToken).toBe(null);
      }
      expect(body.tokenSent).toBe(false);
      expect(body.warnings.join('\n')).toContain('was not sent to it');
      expect(body.warnings.join('\n')).not.toContain(SENTINEL_TOKEN);

      await post(run, '/api/cancel', {});
      expect(await run.exit).toBe(0);
    });

    it('sends it to the provider’s own API host', async () => {
      const seen: Array<{ url: string; auth: string | null; privateToken: string | null }> = [];
      const run = await start({ fetchImpl: recordingFetch(seen) });
      const body = await cleanJson<{ tokenSent: boolean }>(
        await post(run, '/api/repos', { provider: 'github', host: 'https://api.github.com', account: 'me' }),
      );
      expect(body.tokenSent).toBe(true);
      expect(seen[0]!.auth).toBe(`Bearer ${SENTINEL_TOKEN}`);
      await post(run, '/api/cancel', {});
      expect(await run.exit).toBe(0);
    });

    it('sends it to the --host the user named, but only for that provider', async () => {
      const seen: Array<{ url: string; auth: string | null; privateToken: string | null }> = [];
      const run = await start({
        provider: 'github',
        host: 'https://ghe.corp/api/v3',
        fetchImpl: recordingFetch(seen),
        io: { env: { FRZNFORGE_GITHUB_TOKEN: SENTINEL_TOKEN, FRZNFORGE_GITEA_TOKEN: SENTINEL_TOKEN } },
      });
      await cleanJson(await post(run, '/api/repos', { provider: 'github', host: 'https://ghe.corp/api/v3', account: 'me' }));
      expect(seen.at(-1)!.auth).toBe(`Bearer ${SENTINEL_TOKEN}`);

      // Same host, different provider: the page must not be able to redirect another token there.
      await cleanJson(await post(run, '/api/repos', { provider: 'gitea', host: 'https://ghe.corp/api/v3', account: 'me' }));
      expect(seen.at(-1)!.auth).toBe(null);

      await post(run, '/api/cancel', {});
      expect(await run.exit).toBe(0);
    });

    it('pins the decision to the host, not to a string that merely parses as a URL', () => {
      expect(hostTrustedForToken('github', 'https://api.github.com/', {})).toBe(true);
      expect(hostTrustedForToken('github', 'https://API.GitHub.com', {})).toBe(true);
      expect(hostTrustedForToken('github', 'https://api.github.com.evil.example', {})).toBe(false);
      expect(hostTrustedForToken('gitea', 'https://git.example', {})).toBe(false);
      expect(hostTrustedForToken('gitea', 'https://git.example', { provider: 'gitea', host: 'https://git.example' })).toBe(true);
      expect(hostTrustedForToken('github', 'https://git.example', { provider: 'gitea', host: 'https://git.example' })).toBe(false);
    });
  });
});

/* ------------------------------------------------------------------ hostile payloads */

describe('payloads that could corrupt the config', () => {
  it('refuses a host carrying a line terminator, which new URL() silently swallows', async () => {
    // `new URL('https://a.com/x\ny')` parses: the WHATWG parser strips the newline before
    // parsing but the original text keeps it, and it would land raw in a string literal.
    const file = await writeConfig();
    const run = await start();
    const res = await post(run, '/api/write', {
      entries: [{ type: 'github', host: 'https://a.com/x\ny', owner: 'o1', repo: 'r1' }],
    });
    expect(res.status).toBe(400);
    expect((await res.json() as { error: string }).error).toMatch(/not a usable host/);
    expect(await fs.readFile(file, 'utf8')).toBe(FIXTURE_CONFIG);

    await post(run, '/api/cancel', {});
    expect(await run.exit).toBe(0);
  });

  it('refuses a provider-supplied repo name, which the select path never validated', async () => {
    const file = await writeConfig();
    const hostile = [{ name: "evil\n    path: '/etc/passwd', slug: 'pwned", full_name: 'me/evil', owner: { login: 'me' } }];
    const run = await start({ fetchImpl: stubFetch(hostile) });
    await post(run, '/api/repos', { provider: 'github', host: 'https://api.github.com', account: 'me' });
    const res = await post(run, '/api/write', {
      provider: 'github',
      host: 'https://api.github.com',
      account: 'me',
      releases: 'provider',
      select: ['me/evil'],
    });
    expect(res.status).toBeGreaterThanOrEqual(400);
    expect(await fs.readFile(file, 'utf8')).toBe(FIXTURE_CONFIG);

    await post(run, '/api/cancel', {});
    expect(await run.exit).toBe(0);
  });
});

/* ------------------------------------------------------------------ concurrency */

describe('/api/write under two tabs', () => {
  it('answers one write and refuses the rest instead of losing them silently', async () => {
    // Nothing binds the session to one tab: the URL is printed, and `openBrowser` may already
    // have opened one. Six unserialized read-modify-writes all reported "added 1" while only
    // the last entry survived.
    const file = await writeConfig();
    const run = await start();
    const numbers = [1, 2, 3, 4, 5, 6];
    const results = await Promise.all(
      numbers.map(async (n) => {
        const res = await post(run, '/api/write', { entries: [{ type: 'github', owner: 'me', repo: `repo${n}` }] });
        return { status: res.status, body: (await res.json()) as { added?: number; error?: string } };
      }),
    );
    expect(results.filter((r) => r.status === 200)).toHaveLength(1);
    for (const rejected of results.filter((r) => r.status !== 200)) {
      expect(rejected.status).toBe(409);
      expect(rejected.body.error).toMatch(/already (in progress|been written)/);
    }

    const written = await fs.readFile(file, 'utf8');
    const added = numbers.filter((n) => written.includes(`repo${n}`));
    expect(added).toHaveLength(1);
    // Exactly one backup, and it is the pre-write file rather than another writer's output.
    const backups = (await fs.readdir(tmp)).filter((f) => f.endsWith('.bak'));
    expect(backups).toHaveLength(1);
    expect(await fs.readFile(path.join(tmp, backups[0]!), 'utf8')).toBe(FIXTURE_CONFIG);

    expect(await run.exit).toBe(0);
  });
});
