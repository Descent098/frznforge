/**
 * GitHub importer (REST v3). Talks to `<host>/repos/<owner>/<repo>` and
 * `<host>/repos/<owner>/<repo>/releases`; `host` defaults to https://api.github.com and is
 * the `/api/v3` base on GitHub Enterprise.
 */
import type { GithubSourceConfig } from '../config/schema';
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

/** Releases per request; GitHub's maximum, so most repos need a single round trip. */
const PER_PAGE = 100;

/** The subset of `GET /repos/{owner}/{repo}` this importer reads. */
interface GithubRepo {
  name?: string | null;
  description?: string | null;
  homepage?: string | null;
  html_url?: string | null;
  clone_url?: string | null;
  default_branch?: string | null;
  topics?: unknown;
  license?: { spdx_id?: string | null } | null;
  has_issues?: boolean;
  is_template?: boolean;
  archived?: boolean;
}

/** The subset of `GET /repos/{owner}/{repo}/releases` this importer reads. */
interface GithubRelease {
  tag_name?: string | null;
  name?: string | null;
  body?: string | null;
  html_url?: string | null;
  draft?: boolean;
  prerelease?: boolean;
  published_at?: string | null;
  created_at?: string | null;
  author?: { login?: string | null } | null;
  assets?: GithubAsset[] | null;
}

interface GithubAsset {
  name?: string | null;
  browser_download_url?: string | null;
  url?: string | null;
  size?: number | null;
  content_type?: string | null;
}

export class GithubImporter implements Importer {
  readonly provider = 'github' as const;

  protected readonly source: GithubSourceConfig;
  protected readonly fetchImpl: typeof fetch;
  protected readonly token: string | null;
  protected readonly userAgent: string;
  private readonly client: JsonClient;

  constructor(source: GithubSourceConfig, ctx: ImporterContext = {}) {
    this.source = source;
    this.fetchImpl = ctx.fetchImpl ?? globalThis.fetch;
    this.token = ctx.token ?? null;
    this.userAgent = ctx.userAgent ?? defaultUserAgent();
    this.client = new JsonClient({
      auth: 'bearer',
      fetchImpl: this.fetchImpl,
      token: this.token,
      userAgent: this.userAgent,
      accept: 'application/vnd.github+json',
    });
  }

  async fetchMeta(): Promise<ImportedRepoMeta> {
    const repo = await this.client.get<GithubRepo>(this.repoUrl());
    const webUrl = nullIfEmpty(repo.html_url) ?? this.fallbackWebUrl();
    return {
      name: nullIfEmpty(repo.name) ?? this.source.repo,
      description: nullIfEmpty(repo.description),
      homepage: nullIfEmpty(repo.homepage),
      topics: stringArray(repo.topics),
      license: toSpdx(repo.license?.spdx_id),
      defaultBranch: nullIfEmpty(repo.default_branch),
      webUrl,
      cloneUrl: nullIfEmpty(repo.clone_url) ?? `${webUrl}.git`,
      issuesUrl: repo.has_issues === true ? `${webUrl}/issues` : null,
      template: repo.is_template === true,
      archived: repo.archived === true,
    };
  }

  async fetchReleases(): Promise<ImportedReleases> {
    const page = await this.client.getAll<GithubRelease>(`${this.repoUrl()}/releases?per_page=${PER_PAGE}`);
    const web = this.fallbackWebUrl();
    const releases: Release[] = [];
    for (const entry of page.items) {
      // Drafts are invisible to anonymous visitors and would appear and disappear with the
      // token used to build, which is exactly the kind of non-determinism we avoid.
      if (entry.draft === true) continue;
      const tag = nullIfEmpty(entry.tag_name);
      const publishedAt = toIsoDate(entry.published_at) ?? toIsoDate(entry.created_at);
      // Without a tag or a usable date there is nothing the site could route to or sort by.
      if (tag === null || publishedAt === null) continue;
      releases.push({
        tag,
        name: nullIfEmpty(entry.name) ?? tag,
        body: entry.body ?? '',
        url: mapUrl(nullIfEmpty(entry.html_url), web),
        prerelease: entry.prerelease === true,
        publishedAt,
        author: nullIfEmpty(entry.author?.login),
        assets: sortAssets(
          (entry.assets ?? []).map((a) => toAsset(a, web)).filter((a): a is ReleaseAsset => a !== null),
        ),
      });
    }
    return { releases: sortReleases(releases), truncated: page.truncated };
  }

  /** `<host>/repos/<owner>/<repo>`, host normalised so a trailing slash cannot double up. */
  private repoUrl(): string {
    const base = this.source.host.replace(/\/+$/, '');
    return `${base}/repos/${encodeURIComponent(this.source.owner)}/${encodeURIComponent(this.source.repo)}`;
  }

  /** Used only when the API omits `html_url`; api.github.com maps to github.com. */
  private fallbackWebUrl(): string {
    const base = this.source.host.replace(/\/+$/, '').replace(/^https:\/\/api\.github\.com$/, 'https://github.com');
    return `${base.replace(/\/api\/v3$/, '')}/${this.source.owner}/${this.source.repo}`;
  }
}

/** A provider URL made absolute against the repo page, or null when there was none. */
function mapUrl(value: string | null, base: string): string | null {
  return value === null ? null : absoluteUrl(value, base);
}

/** GitHub asset → schema asset. `browser_download_url` is the one a visitor can actually use. */
function toAsset(asset: GithubAsset, base: string): ReleaseAsset | null {
  const name = nullIfEmpty(asset.name);
  const url = nullIfEmpty(asset.browser_download_url) ?? nullIfEmpty(asset.url);
  if (name === null || url === null) return null;
  // Absolutised so a relative URL cannot render as a same-origin link on the generated site.
  return { name, url: absoluteUrl(url, base), size: byteSize(asset.size), contentType: nullIfEmpty(asset.content_type) };
}
