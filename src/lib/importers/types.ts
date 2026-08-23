/**
 * The contract every provider importer implements.
 *
 * An importer is the *metadata* half of a remote source: it answers two questions over the
 * provider's REST API — "what does this repo say about itself" and "what releases does it
 * have". The git half is a plain `git clone --mirror` into the ingest cache, scanned by the
 * existing local scanner, so nothing below ever touches git.
 *
 * Rules that apply to every implementation:
 *  - **Tokens come from the environment only.** Never log one, never put one in an error
 *    message, a warning or the artifact. Run anything you interpolate through
 *    `redactToken()` first.
 *  - **Never hard-fail the build.** Throw `ImporterError` and let the caller decide between
 *    "use the cache" and "skip the repo with a warning".
 *  - **Deterministic output.** No wall-clock values, no download counters, no star counts.
 *    Timestamps are normalised to `IsoDate` (UTC, seconds precision).
 *  - **Injectable fetch.** Take `ImporterContext.fetchImpl` so unit tests can run against
 *    recorded JSON fixtures with no network.
 */
import type { RemoteRepoSourceConfig, RepoSourceConfig } from '../config/schema';
import type { Release } from '../data/schema';

export type { RemoteRepoSourceConfig, RepoSourceConfig };

/** The four supported hosting providers. */
export type ProviderName = 'github' | 'gitlab' | 'gitea' | 'forgejo';

/** Sent as `User-Agent` when a context does not override it (GitHub rejects requests without one). */
export const DEFAULT_USER_AGENT = 'frznforge';

/**
 * Repo metadata as the provider reports it. Fed into the scanner as low-precedence
 * defaults: config `overrides` > the repo's committed `.frznforge.json` > this >
 * values derived from the repository itself.
 */
export interface ImportedRepoMeta {
  /**
   * Repo name exactly as the provider spells it (`MyProject`, `文档`). Without this the
   * display name would fall back to the mirror's directory basename, which is lower-cased and
   * ASCII-only because it is a filesystem key, not user-facing text.
   */
  name: string | null;
  description: string | null;
  homepage: string | null;
  topics: string[];
  /** SPDX id when the provider reports one, else null (the scanner still sniffs LICENSE). */
  license: string | null;
  defaultBranch: string | null;
  /** Human-facing repo page. */
  webUrl: string;
  /** URL a visitor can `git clone` (also what the mirror is cloned from). */
  cloneUrl: string;
  issuesUrl: string | null;
  template: boolean;
  archived: boolean;
}

/** A repo's release list as imported, plus whether the pagination cap cut it short. */
export interface ImportedReleases {
  /** Newest first: `publishedAt` desc, `tag` asc as tiebreak. `[]` when the repo has none. */
  releases: Release[];
  /**
   * True when the provider still had pages left when the client's page cap ran out, so this
   * list is missing the oldest releases. The caller warns; it must not look like a short list.
   */
  truncated: boolean;
}

/** What a provider module exposes. One instance per configured remote source. */
export interface Importer {
  readonly provider: ProviderName;
  fetchMeta(): Promise<ImportedRepoMeta>;
  fetchReleases(): Promise<ImportedReleases>;
}

/** Why an importer call failed. Maps 1:1 onto the `remote-*` warning codes. */
export type ImporterErrorKind = 'auth' | 'not-found' | 'rate-limit' | 'network' | 'bad-response';

/**
 * A failed provider call. Always catchable by the caller and always turned into a warning —
 * an unreachable, unauthenticated or rate-limited remote must never fail a build.
 *
 * The message MUST NOT contain a token: build it from the URL and status only, and pass
 * anything user-supplied through `redactToken()`.
 */
export class ImporterError extends Error {
  readonly kind: ImporterErrorKind;
  /** HTTP status, when the failure came from a response. */
  readonly status?: number;
  /** Seconds to wait, parsed from `Retry-After` / rate-limit reset headers. */
  readonly retryAfter?: number;

  constructor(
    kind: ImporterErrorKind,
    message: string,
    options: { status?: number; retryAfter?: number; cause?: unknown } = {},
  ) {
    super(message, options.cause !== undefined ? { cause: options.cause } : undefined);
    this.name = 'ImporterError';
    this.kind = kind;
    if (options.status !== undefined) this.status = options.status;
    if (options.retryAfter !== undefined) this.retryAfter = options.retryAfter;
  }
}

/** Everything an importer needs beyond its source config. */
export interface ImporterContext {
  /** Injected for tests; defaults to the global `fetch`. */
  fetchImpl?: typeof fetch;
  /** API token, already resolved from the environment. `null`/absent = anonymous. */
  token?: string | null;
  /** `User-Agent` header; defaults to `DEFAULT_USER_AGENT`. */
  userAgent?: string;
}

/** Default token env var per provider, used when the source sets no `tokenEnv`. */
const PROVIDER_TOKEN_ENV: Record<ProviderName, string> = {
  github: 'GITHUB_TOKEN',
  gitlab: 'GITLAB_TOKEN',
  gitea: 'GITEA_TOKEN',
  forgejo: 'FORGEJO_TOKEN',
};

/**
 * Environment variables to consult for a source's token, in order:
 *  1. `source.tokenEnv`, when set — an explicit choice replaces the defaults entirely;
 *  2. otherwise `FRZNFORGE_<PROVIDER>_TOKEN`, then the provider's conventional variable
 *     (`GITHUB_TOKEN`, `GITLAB_TOKEN`, `GITEA_TOKEN`, `FORGEJO_TOKEN`).
 *
 * Local sources have no token and get an empty list.
 */
export function tokenEnvFor(source: RepoSourceConfig): string[] {
  if (source.type === 'local') return [];
  if (source.tokenEnv) return [source.tokenEnv];
  return [`FRZNFORGE_${source.type.toUpperCase()}_TOKEN`, PROVIDER_TOKEN_ENV[source.type]];
}

/**
 * First non-empty value among `tokenEnvFor(source)`, trimmed; `null` when none is set.
 * `env` is injectable so tests never depend on the ambient environment.
 */
export function resolveToken(
  source: RepoSourceConfig,
  env: Record<string, string | undefined> = process.env,
): string | null {
  for (const name of tokenEnvFor(source)) {
    const value = env[name];
    if (typeof value === 'string' && value.trim() !== '') return value.trim();
  }
  return null;
}

/**
 * Replace every occurrence of `token` with `***`. Call this on anything that might have
 * been built from a URL or header carrying credentials before it reaches a message, a
 * warning or a log line.
 */
export function redactToken(text: string, token?: string | null): string {
  if (!token) return text;
  return text.split(token).join('***');
}
