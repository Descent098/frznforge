/**
 * Per-origin rate-limit backoff, shared by every {@link JsonClient} in the process.
 *
 * Why per *origin* and not per client: ingest runs `ingest.concurrency` repos at once, and
 * on a corpus like "72 GitHub repos" all of them talk to `api.github.com`. A retry policy
 * held per client would have each of those repos discover the same 429 independently and
 * hammer the window in parallel. Keyed on the origin, the first repo to be limited makes
 * every other request to that host wait behind the same timer, while a request to a
 * different forge (codeberg, a self-hosted Gitea) is completely unaffected.
 *
 * Two kinds of limit, deliberately handled differently:
 *
 *  - **Short** (retry-after within {@link BackoffOptions.maxDelayMs}, or no hint at all):
 *    wait, then retry, with exponential growth per consecutive failure on that origin.
 *  - **Long** (the provider says "come back in 40 minutes" — GitHub's hourly quota):
 *    do NOT sleep through it and do NOT keep retrying. The origin is marked blocked until
 *    that instant and every later request to it fails immediately with a `rate-limit`
 *    error, which is what `prepareRemote` already turns into a `remote-rate-limited`
 *    warning plus the cached-metadata fallback. One repo pays for the discovery; the rest
 *    of the build degrades quickly and finishes instead of making N × attempts doomed
 *    calls.
 *
 * The clock and the sleep are injectable so tests never actually wait.
 */
import { ImporterError } from './types';

export interface BackoffOptions {
  /** First delay; doubles per consecutive rate-limited response on the same origin. */
  baseDelayMs?: number;
  /** Ceiling for a delay we are willing to sleep through. Longer waits block instead. */
  maxDelayMs?: number;
  /** Total attempts per request, including the first. */
  maxAttempts?: number;
  /** Injected for tests. */
  sleepImpl?: (ms: number) => Promise<void>;
  now?: () => number;
  /** Jitter factor in [0, 1); defaults to `Math.random`. Deterministic in tests. */
  jitter?: () => number;
}

interface OriginState {
  /** Consecutive rate-limited responses; drives the exponential growth. */
  failures: number;
  /** Epoch ms before which no request to this origin may be sent. */
  waitUntil: number;
  /** Epoch ms before which requests fail immediately instead of waiting. */
  blockedUntil: number;
}

const DEFAULT_BASE_DELAY_MS = 1_000;
const DEFAULT_MAX_DELAY_MS = 60_000;
const DEFAULT_MAX_ATTEMPTS = 4;

/** The origin part of a URL, or the raw string when it will not parse. */
export function originOf(url: string): string {
  try {
    return new URL(url).origin;
  } catch {
    return url;
  }
}

export class OriginBackoff {
  private readonly states = new Map<string, OriginState>();
  private readonly baseDelayMs: number;
  private readonly maxDelayMs: number;
  readonly maxAttempts: number;
  private readonly sleepImpl: (ms: number) => Promise<void>;
  private readonly now: () => number;
  private readonly jitter: () => number;

  constructor(options: BackoffOptions = {}) {
    this.baseDelayMs = options.baseDelayMs ?? DEFAULT_BASE_DELAY_MS;
    this.maxDelayMs = options.maxDelayMs ?? DEFAULT_MAX_DELAY_MS;
    this.maxAttempts = options.maxAttempts ?? DEFAULT_MAX_ATTEMPTS;
    this.sleepImpl = options.sleepImpl ?? ((ms) => new Promise((r) => setTimeout(r, ms)));
    this.now = options.now ?? Date.now;
    this.jitter = options.jitter ?? Math.random;
  }

  private state(origin: string): OriginState {
    let s = this.states.get(origin);
    if (!s) {
      s = { failures: 0, waitUntil: 0, blockedUntil: 0 };
      this.states.set(origin, s);
    }
    return s;
  }

  /**
   * Gate a request. Throws when the origin is hard-blocked (so the caller degrades to its
   * cache immediately), otherwise sleeps out any pending short backoff.
   *
   * The wait is re-checked in a loop: another in-flight request may extend the window while
   * this one is sleeping, and it must not slip through early.
   */
  async beforeRequest(origin: string, describeUrl: string): Promise<void> {
    const s = this.state(origin);
    if (s.blockedUntil > this.now()) {
      const seconds = Math.ceil((s.blockedUntil - this.now()) / 1000);
      throw new ImporterError(
        'rate-limit',
        `${describeUrl}: ${origin} is rate-limited for another ${seconds}s; not retrying`,
        { retryAfter: seconds },
      );
    }
    for (;;) {
      const remaining = s.waitUntil - this.now();
      if (remaining <= 0) return;
      await this.sleepImpl(remaining);
    }
  }

  /** A request to this origin came back healthy: forget the consecutive-failure count. */
  noteSuccess(origin: string): void {
    const s = this.state(origin);
    s.failures = 0;
    s.waitUntil = 0;
    s.blockedUntil = 0;
  }

  /**
   * Record a rate-limited response and decide what happens next.
   *
   * Returns true when the caller should retry (a short wait was scheduled — this call
   * sleeps it out), false when it should give up and let the cache take over.
   */
  async noteRateLimit(
    origin: string,
    retryAfterSeconds: number | undefined,
    attempt: number,
  ): Promise<boolean> {
    const s = this.state(origin);
    s.failures += 1;

    // The provider's own number wins when it gave one; otherwise grow exponentially from
    // the consecutive-failure count, with jitter so parallel repos do not resynchronise.
    const advised = retryAfterSeconds !== undefined ? retryAfterSeconds * 1000 : null;
    const exponential = this.baseDelayMs * 2 ** Math.min(s.failures - 1, 10);
    const jittered = exponential * (1 + this.jitter());
    const delay = advised ?? jittered;

    if (delay > this.maxDelayMs) {
      // Too long to sit through. Block the origin for the whole stated window so every
      // other repo on this host fails fast into its cache instead of queueing behind it.
      s.blockedUntil = this.now() + delay;
      s.waitUntil = 0;
      return false;
    }

    const until = this.now() + delay;
    if (until > s.waitUntil) s.waitUntil = until;
    if (attempt >= this.maxAttempts) return false;
    await this.beforeRequest(origin, origin);
    return true;
  }

  /** Test/reporting view of an origin's current state. */
  peek(origin: string): { failures: number; waitingMs: number; blockedMs: number } {
    const s = this.state(origin);
    const now = this.now();
    return {
      failures: s.failures,
      waitingMs: Math.max(0, s.waitUntil - now),
      blockedMs: Math.max(0, s.blockedUntil - now),
    };
  }

  /**
   * Forget every origin's state.
   *
   * An ingest process runs once, so nothing in a build needs this — it exists for long-lived
   * processes and for tests, where one case's rate-limited fixture would otherwise block
   * that host for every case that follows it in the same process.
   */
  reset(): void {
    this.states.clear();
  }

  /** True while the origin is refusing requests outright (used for build reporting). */
  isBlocked(origin: string): boolean {
    return this.state(origin).blockedUntil > this.now();
  }
}

/**
 * The process-wide gate. Every client shares it by default, which is the whole point:
 * concurrency is per repo, but a provider's rate limit is per host.
 */
export const sharedBackoff = new OriginBackoff();
