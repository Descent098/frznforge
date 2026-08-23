/**
 * Gitea importer (REST v1) — also the Forgejo implementation: Forgejo is an API-compatible
 * fork, so only the `provider` label (and therefore the token env vars, cache directory and
 * `RepoSource.type`) differ. Talks to `<host>/api/v1/repos/<owner>/<repo>` and its
 * `/releases`.
 *
 * Compatible does not mean identical, so every field below is feature-detected rather than
 * assumed: Gitea reports `licenses: ["MIT"]` while Forgejo has no license field at all (the
 * scanner sniffs the cloned LICENSE instead), Forgejo timestamps carry a `+02:00`-style offset,
 * and neither provider reports a content type for release assets.
 */
import type { ForgejoSourceConfig, GiteaSourceConfig } from '../config/schema';
import type { Release, ReleaseAsset } from '../data/schema';
import {
  JsonClient,
  absoluteUrl,
  byteSize,
  defaultUserAgent,
  nullIfEmpty,
  sortAssets,
  sortReleases,
  stringArray,
  toIsoDate,
  toSpdx,
} from './http';
import type { ImportedReleases, Importer, ImportedRepoMeta, ImporterContext } from './types';

/** Releases per request. Gitea/Forgejo cap `limit` at their instance page size (50 by default). */
const PAGE_LIMIT = 50;

/** The subset of `GET /repos/{owner}/{repo}` this importer reads. */
interface GiteaRepo {
  name?: string | null;
  description?: string | null;
  website?: string | null;
  html_url?: string | null;
  clone_url?: string | null;
  default_branch?: string | null;
  topics?: unknown;
  /** Gitea only: SPDX-ish strings. Absent on Forgejo. */
  licenses?: unknown;
  has_issues?: boolean;
  external_tracker?: { external_tracker_url?: string | null } | null;
  template?: boolean;
  archived?: boolean;
}

/** The subset of `GET /repos/{owner}/{repo}/releases` this importer reads. */
interface GiteaRelease {
  tag_name?: string | null;
  name?: string | null;
  body?: string | null;
  html_url?: string | null;
  draft?: boolean;
  prerelease?: boolean;
  published_at?: string | null;
  created_at?: string | null;
  author?: { login?: string | null; username?: string | null } | null;
  assets?: GiteaAsset[] | null;
}

interface GiteaAsset {
  name?: string | null;
  browser_download_url?: string | null;
  size?: number | null;
}

export class GiteaImporter implements Importer {
  /** Derived from the source, so the same class serves both `gitea` and `forgejo`. */
  readonly provider: 'gitea' | 'forgejo';

  protected readonly source: GiteaSourceConfig | ForgejoSourceConfig;
  protected readonly fetchImpl: typeof fetch;
  protected readonly token: string | null;
  protected readonly userAgent: string;
  private readonly client: JsonClient;

  constructor(source: GiteaSourceConfig | ForgejoSourceConfig, ctx: ImporterContext = {}) {
    this.provider = source.type;
    this.source = source;
    this.fetchImpl = ctx.fetchImpl ?? globalThis.fetch;
    this.token = ctx.token ?? null;
    this.userAgent = ctx.userAgent ?? defaultUserAgent();
    this.client = new JsonClient({
      auth: 'token',
      fetchImpl: this.fetchImpl,
      token: this.token,
      userAgent: this.userAgent,
    });
  }

  async fetchMeta(): Promise<ImportedRepoMeta> {
    const repo = await this.client.get<GiteaRepo>(this.repoUrl());
    const webUrl = nullIfEmpty(repo.html_url) ?? this.fallbackWebUrl();
    return {
      name: nullIfEmpty(repo.name) ?? this.source.repo,
      description: nullIfEmpty(repo.description),
      homepage: nullIfEmpty(repo.website),
      topics: stringArray(repo.topics),
      // Forgejo reports no license; the scanner's LICENSE detection fills the gap.
      license: toSpdx(stringArray(repo.licenses)[0]),
      defaultBranch: nullIfEmpty(repo.default_branch),
      webUrl,
      cloneUrl: nullIfEmpty(repo.clone_url) ?? `${webUrl}.git`,
      issuesUrl:
        nullIfEmpty(repo.external_tracker?.external_tracker_url) ??
        (repo.has_issues === true ? `${webUrl}/issues` : null),
      template: repo.template === true,
      archived: repo.archived === true,
    };
  }

  async fetchReleases(): Promise<ImportedReleases> {
    const page = await this.client.getAll<GiteaRelease>(`${this.repoUrl()}/releases?limit=${PAGE_LIMIT}`);
    const web = this.fallbackWebUrl();
    const releases: Release[] = [];
    for (const entry of page.items) {
      // Drafts depend on the token used to build; excluding them keeps the artifact stable.
      if (entry.draft === true) continue;
      const tag = nullIfEmpty(entry.tag_name);
      const publishedAt = toIsoDate(entry.published_at) ?? toIsoDate(entry.created_at);
      if (tag === null || publishedAt === null) continue;
      const author = entry.author;
      releases.push({
        tag,
        name: nullIfEmpty(entry.name) ?? tag,
        body: entry.body ?? '',
        url: mapUrl(nullIfEmpty(entry.html_url), web),
        prerelease: entry.prerelease === true,
        publishedAt,
        author: nullIfEmpty(author?.login) ?? nullIfEmpty(author?.username),
        assets: sortAssets(
          (entry.assets ?? []).map((a) => toAsset(a, web)).filter((a): a is ReleaseAsset => a !== null),
        ),
      });
    }
    return { releases: sortReleases(releases), truncated: page.truncated };
  }

  /** `<host>/api/v1/repos/<owner>/<repo>`. */
  private repoUrl(): string {
    const base = this.source.host.replace(/\/+$/, '');
    return `${base}/api/v1/repos/${encodeURIComponent(this.source.owner)}/${encodeURIComponent(this.source.repo)}`;
  }

  private fallbackWebUrl(): string {
    return `${this.source.host.replace(/\/+$/, '')}/${this.source.owner}/${this.source.repo}`;
  }
}

/**
 * Gitea/Forgejo asset → schema asset. `browser_download_url` is the only URL these APIs give,
 * and neither reports a MIME type; `download_count` is deliberately ignored (volatile).
 */
function toAsset(asset: GiteaAsset, base: string): ReleaseAsset | null {
  const name = nullIfEmpty(asset.name);
  const url = nullIfEmpty(asset.browser_download_url);
  if (name === null || url === null) return null;
  // Absolutised so a relative URL cannot render as a same-origin link on the generated site.
  return { name, url: absoluteUrl(url, base), size: byteSize(asset.size), contentType: null };
}

/** A provider URL made absolute against the repo page, or null when there was none. */
function mapUrl(value: string | null, base: string): string | null {
  return value === null ? null : absoluteUrl(value, base);
}
