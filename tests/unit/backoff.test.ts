/**
 * Per-origin rate-limit backoff (0.3.0). The motivating case is a corpus of ~72 GitHub
 * repos ingested `concurrency` at a time: every one of them talks to `api.github.com`, so
 * the retry policy has to be a property of the HOST, not of one client or one repo.
 *
 * Nothing here sleeps: the clock and the sleep are injected, and the assertions are about
 * what was scheduled and how many requests actually went out.
 */
import { describe, expect, it } from 'vitest';
import { OriginBackoff, originOf, type BackoffOptions } from '../../src/lib/importers/backoff';
import { JsonClient } from '../../src/lib/importers/http';
import { ImporterError } from '../../src/lib/importers/index';

/** A backoff with a fake clock: sleeping advances time instead of waiting. */
function fakeBackoff(overrides: BackoffOptions = {}) {
  let now = 1_000_000;
  const slept: number[] = [];
  const backoff = new OriginBackoff({
    baseDelayMs: 1_000,
    maxDelayMs: 60_000,
    maxAttempts: 4,
    jitter: () => 0, // deterministic: assert the exact exponential ladder
    now: () => now,
    sleepImpl: async (ms) => {
      slept.push(ms);
      now += ms;
    },
    ...overrides,
  });
  return { backoff, slept, advance: (ms: number) => (now += ms), at: () => now };
}

/** A fetchImpl that replays a scripted list of responses and counts the calls. */
function scriptedFetch(script: Array<{ status: number; headers?: Record<string, string>; body?: string }>) {
  const urls: string[] = [];
  const fetchImpl = (async (url: string) => {
    urls.push(String(url));
    const step = script[Math.min(urls.length - 1, script.length - 1)]!;
    return new Response(step.body ?? '{"ok":true}', {
      status: step.status,
      headers: step.headers ?? {},
    });
  }) as unknown as typeof fetch;
  return { fetchImpl, urls };
}

const RATE_LIMIT_HEADERS = { 'x-ratelimit-remaining': '0' };

describe('OriginBackoff', () => {
  it('grows exponentially per consecutive rate limit on the same origin', async () => {
    const { backoff, slept } = fakeBackoff();
    // No Retry-After: the delay comes from the consecutive-failure count. jitter() is 0, so
    // the ladder is exactly base, base*2, base*4.
    expect(await backoff.noteRateLimit('https://api.github.com', undefined, 1)).toBe(true);
    expect(await backoff.noteRateLimit('https://api.github.com', undefined, 2)).toBe(true);
    expect(await backoff.noteRateLimit('https://api.github.com', undefined, 3)).toBe(true);
    expect(slept).toEqual([1_000, 2_000, 4_000]);
  });

  it('honours the provider’s Retry-After instead of guessing', async () => {
    const { backoff, slept } = fakeBackoff();
    expect(await backoff.noteRateLimit('https://api.github.com', 7, 1)).toBe(true);
    expect(slept).toEqual([7_000]);
  });

  it('stops retrying once the attempt budget is spent', async () => {
    const { backoff } = fakeBackoff({ maxAttempts: 2 });
    expect(await backoff.noteRateLimit('https://api.github.com', 1, 1)).toBe(true);
    expect(await backoff.noteRateLimit('https://api.github.com', 1, 2)).toBe(false);
  });

  it('blocks (rather than sleeps through) a limit longer than the ceiling', async () => {
    // GitHub's hourly quota says "come back in 40 minutes". Sleeping that out would hang the
    // build; retrying would waste every repo's attempts. The origin is marked blocked.
    const { backoff, slept } = fakeBackoff();
    expect(await backoff.noteRateLimit('https://api.github.com', 2400, 1)).toBe(false);
    expect(slept).toEqual([]); // nothing was waited on
    expect(backoff.isBlocked('https://api.github.com')).toBe(true);

    await expect(backoff.beforeRequest('https://api.github.com', 'GET /repos/x')).rejects.toThrow(
      /rate-limited for another/,
    );
    // ...and it is the kind that maps onto `remote-rate-limited` + the cached fallback
    await backoff
      .beforeRequest('https://api.github.com', 'GET /repos/x')
      .catch((e: unknown) => expect((e as ImporterError).kind).toBe('rate-limit'));
  });

  it('isolates origins: one forge backing off never delays another', async () => {
    const { backoff, slept } = fakeBackoff();
    await backoff.noteRateLimit('https://api.github.com', 30, 1);
    expect(slept).toEqual([30_000]);

    // codeberg has its own (clean) state — no wait at all
    await backoff.beforeRequest('https://codeberg.org', 'GET /x');
    expect(slept).toEqual([30_000]);
    expect(backoff.peek('https://codeberg.org').failures).toBe(0);
  });

  it('a success clears the consecutive-failure count', async () => {
    const { backoff, slept } = fakeBackoff();
    await backoff.noteRateLimit('https://api.github.com', undefined, 1);
    await backoff.noteRateLimit('https://api.github.com', undefined, 2);
    backoff.noteSuccess('https://api.github.com');
    slept.length = 0;
    await backoff.noteRateLimit('https://api.github.com', undefined, 1);
    expect(slept).toEqual([1_000]); // back to the first rung, not the third
  });

  it('holds one shared window per origin, so every caller waits behind the same timer', async () => {
    // Modelling real concurrency: caller A hits the limit and is still sleeping (its sleep
    // never resolves here) while caller B arrives. B must see A's window, not a fresh one.
    let now = 1_000_000;
    const backoff = new OriginBackoff({
      baseDelayMs: 1_000,
      maxDelayMs: 60_000,
      jitter: () => 0,
      now: () => now,
      sleepImpl: () => new Promise<void>(() => {}), // A stays parked in its backoff
    });

    void backoff.noteRateLimit('https://api.github.com', 30, 1);
    await Promise.resolve();

    // The window is open and is a property of the ORIGIN — B reads the same remaining time.
    expect(backoff.peek('https://api.github.com').waitingMs).toBe(30_000);
    now += 10_000;
    expect(backoff.peek('https://api.github.com').waitingMs).toBe(20_000);
    // ...and a different origin is untouched by it
    expect(backoff.peek('https://codeberg.org').waitingMs).toBe(0);

    now += 20_000;
    expect(backoff.peek('https://api.github.com').waitingMs).toBe(0);
  });

  it('originOf keys on scheme+host+port, and survives a non-URL', () => {
    expect(originOf('https://api.github.com/repos/a/b')).toBe('https://api.github.com');
    expect(originOf('https://git.example.com:3000/api/v1/x')).toBe('https://git.example.com:3000');
    expect(originOf('https://gitlab.com/api/v4/y')).toBe('https://gitlab.com');
    // different hosts must not collide
    expect(originOf('https://codeberg.org/api/v1/x')).not.toBe(originOf('https://api.github.com/x'));
    expect(originOf('not a url')).toBe('not a url');
  });
});

describe('JsonClient rate-limit handling', () => {
  const client = (fetchImpl: typeof fetch, backoff: OriginBackoff) =>
    new JsonClient({ auth: 'bearer', fetchImpl, retryDelayMs: 0, backoff });

  it('retries a 429 and succeeds, without spending the 5xx retry budget', async () => {
    const { backoff } = fakeBackoff();
    const { fetchImpl, urls } = scriptedFetch([
      { status: 429, headers: { 'retry-after': '2' } },
      { status: 429, headers: { 'retry-after': '2' } },
      { status: 200, body: '{"name":"ok"}' },
    ]);
    const result = await client(fetchImpl, backoff).get<{ name: string }>('https://api.github.com/repos/a/b');
    expect(result.name).toBe('ok');
    expect(urls.length).toBe(3); // two rate-limited attempts then the good one
  });

  it('treats GitHub’s 403-with-no-quota-left as a rate limit, not an auth failure', async () => {
    const { backoff } = fakeBackoff();
    const { fetchImpl, urls } = scriptedFetch([
      { status: 403, headers: RATE_LIMIT_HEADERS },
      { status: 200, body: '{"name":"ok"}' },
    ]);
    const result = await client(fetchImpl, backoff).get<{ name: string }>('https://api.github.com/repos/a/b');
    expect(result.name).toBe('ok');
    expect(urls.length).toBe(2);
  });

  it('gives up after the attempt budget and reports a rate limit', async () => {
    const { backoff } = fakeBackoff({ maxAttempts: 2 });
    const { fetchImpl, urls } = scriptedFetch([{ status: 429, headers: { 'retry-after': '1' } }]);
    await expect(
      client(fetchImpl, backoff).get('https://api.github.com/repos/a/b'),
    ).rejects.toMatchObject({ kind: 'rate-limit' });
    expect(urls.length).toBe(2);
  });

  it('once an origin is hard-blocked, later calls cost no requests at all', async () => {
    // The 72-repo case: repo #1 discovers the hourly limit, repos #2..72 must not each make
    // four more doomed calls — they fail fast and fall back to their cached metadata.
    const { backoff } = fakeBackoff();
    const { fetchImpl, urls } = scriptedFetch([{ status: 429, headers: { 'retry-after': '3600' } }]);
    const c = client(fetchImpl, backoff);

    await expect(c.get('https://api.github.com/repos/a/b')).rejects.toMatchObject({ kind: 'rate-limit' });
    const afterFirst = urls.length;

    await expect(c.get('https://api.github.com/repos/c/d')).rejects.toMatchObject({ kind: 'rate-limit' });
    expect(urls.length).toBe(afterFirst); // no new request went out

    // a different forge still works
    const { fetchImpl: otherFetch, urls: otherUrls } = scriptedFetch([{ status: 200, body: '{"name":"ok"}' }]);
    await client(otherFetch, backoff).get('https://codeberg.org/api/v1/repos/a/b');
    expect(otherUrls.length).toBe(1);
  });

  it('does not treat a plain 401 as a rate limit', async () => {
    const { backoff } = fakeBackoff();
    const { fetchImpl, urls } = scriptedFetch([{ status: 401, body: '{"message":"Bad credentials"}' }]);
    await expect(client(fetchImpl, backoff).get('https://api.github.com/repos/a/b')).rejects.toMatchObject({
      kind: 'auth',
    });
    expect(urls.length).toBe(1); // no retry ladder for a bad token
  });
});
