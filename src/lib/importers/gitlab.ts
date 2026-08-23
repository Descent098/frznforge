/**
 * GitLab importer (REST v4). Talks to `<host>/api/v4/projects/<url-encoded project path>`
 * and its `/releases`; `host` defaults to https://gitlab.com.
 *
 * GitLab is the odd one out and needs the most translation:
 *  - the project payload names things differently (`http_url_to_repo`, `web_url`, `tag_list`),
 *    and an unauthenticated response simply omits fields like `archived` — missing means
 *    *unknown*, never `false`;
 *  - `license` is returned only with `?license=true`;
 *  - releases have no `id`, no `draft` and no `prerelease`. GitLab's `upcoming_release` is
 *    *not* a substitute: the API computes it from `released_at > now`, so it flips on its own
 *    once a scheduled release date passes and would change `forge.json` with no change to the
 *    repo. Imported GitLab releases therefore always report `prerelease: false`.
 *  - the notes live in `description`, and assets split into `assets.links[]` and the four
 *    auto-generated `assets.sources[]` — **neither carries a size or a content type**, so
 *    imported GitLab assets report `size: 0`. Asset links may also be host-relative, so every
 *    URL is resolved against the project's web URL before it reaches the artifact.
 */
import type { GitlabSourceConfig } from '../config/schema';
import type { Release, ReleaseAsset } from '../data/schema';
import {
  JsonClient,
  absoluteUrl,
  defaultUserAgent,
  nullIfEmpty,
  sortAssets,
  sortReleases,
  stringArray,
  toIsoDate,
  toSpdx,
} from './http';
import type { ImportedReleases, Importer, ImportedRepoMeta, ImporterContext } from './types';

/** GitLab's maximum page size. */
const PER_PAGE = 100;

/** The subset of `GET /projects/:id?license=true` this importer reads. */
interface GitlabProject {
  name?: string | null;
  path?: string | null;
  description?: string | null;
  web_url?: string | null;
  http_url_to_repo?: string | null;
  default_branch?: string | null;
  topics?: unknown;
  tag_list?: unknown;
  license?: { key?: string | null; nickname?: string | null } | null;
  /** Not part of the documented project payload everywhere, but honoured when present. */
  homepage?: string | null;
  issues_enabled?: boolean;
  archived?: boolean;
}

/** The subset of `GET /projects/:id/releases` this importer reads. */
interface GitlabRelease {
  tag_name?: string | null;
  name?: string | null;
  description?: string | null;
  created_at?: string | null;
  released_at?: string | null;
  /** Read only to document that it exists — deliberately not mapped; see the module docstring. */
  upcoming_release?: boolean;
  author?: { username?: string | null; name?: string | null } | null;
  assets?: {
    links?: Array<{ name?: string | null; url?: string | null; direct_asset_url?: string | null }> | null;
    sources?: Array<{ format?: string | null; url?: string | null }> | null;
  } | null;
  _links?: { self?: string | null } | null;
}

export class GitlabImporter implements Importer {
  readonly provider = 'gitlab' as const;

  protected readonly source: GitlabSourceConfig;
  protected readonly fetchImpl: typeof fetch;
  protected readonly token: string | null;
  protected readonly userAgent: string;
  private readonly client: JsonClient;

  constructor(source: GitlabSourceConfig, ctx: ImporterContext = {}) {
    this.source = source;
    this.fetchImpl = ctx.fetchImpl ?? globalThis.fetch;
    this.token = ctx.token ?? null;
    this.userAgent = ctx.userAgent ?? defaultUserAgent();
    this.client = new JsonClient({
      auth: 'private-token',
      fetchImpl: this.fetchImpl,
      token: this.token,
      userAgent: this.userAgent,
    });
  }

  async fetchMeta(): Promise<ImportedRepoMeta> {
    const project = await this.client.get<GitlabProject>(`${this.projectUrl()}?license=true`);
    const webUrl = nullIfEmpty(project.web_url) ?? this.fallbackWebUrl();
    const topics = stringArray(project.topics);
    return {
      name: nullIfEmpty(project.path) ?? nullIfEmpty(project.name) ?? this.projectName(),
      description: nullIfEmpty(project.description),
      homepage: nullIfEmpty(project.homepage),
      topics: topics.length > 0 ? topics : stringArray(project.tag_list),
      license: toSpdx(project.license?.key) ?? nullIfEmpty(project.license?.nickname),
      defaultBranch: nullIfEmpty(project.default_branch),
      webUrl,
      cloneUrl: nullIfEmpty(project.http_url_to_repo) ?? `${webUrl}.git`,
      // An unauthenticated payload omits `issues_enabled`; only an explicit false hides the link.
      issuesUrl: project.issues_enabled === false ? null : `${webUrl}/-/issues`,
      // GitLab projects have no "template" flag in this payload.
      template: false,
      archived: project.archived === true,
    };
  }

  async fetchReleases(): Promise<ImportedReleases> {
    const page = await this.client.getAll<GitlabRelease>(`${this.projectUrl()}/releases?per_page=${PER_PAGE}`);
    const web = this.fallbackWebUrl();
    const releases: Release[] = [];
    for (const entry of page.items) {
      const tag = nullIfEmpty(entry.tag_name);
      // `released_at` is the display date and can differ from `created_at`.
      const publishedAt = toIsoDate(entry.released_at) ?? toIsoDate(entry.created_at);
      if (tag === null || publishedAt === null) continue;
      const author = entry.author;
      releases.push({
        tag,
        name: nullIfEmpty(entry.name) ?? tag,
        body: entry.description ?? '',
        url: absoluteUrl(nullIfEmpty(entry._links?.self) ?? this.releaseUrl(tag), web),
        // Deliberately not `upcoming_release`: GitLab derives that from the wall clock
        // (`released_at > now`), so importing it would make the artifact change by itself.
        prerelease: false,
        publishedAt,
        author: nullIfEmpty(author?.username) ?? nullIfEmpty(author?.name),
        assets: sortAssets(this.assetsOf(entry, web)),
      });
    }
    return { releases: sortReleases(releases), truncated: page.truncated };
  }

  /** Uploaded links first-class, plus the four archives GitLab generates for every tag. */
  private assetsOf(entry: GitlabRelease, web: string): ReleaseAsset[] {
    const assets: ReleaseAsset[] = [];
    for (const link of entry.assets?.links ?? []) {
      const name = nullIfEmpty(link.name);
      const url = nullIfEmpty(link.direct_asset_url) ?? nullIfEmpty(link.url);
      if (name === null || url === null) continue;
      // `direct_asset_url` is routinely host-relative; left as-is it would render as a link
      // into the generated site. Size and content type are simply not available here.
      assets.push({ name, url: absoluteUrl(url, web), size: 0, contentType: null });
    }
    for (const source of entry.assets?.sources ?? []) {
      const format = nullIfEmpty(source.format);
      const url = nullIfEmpty(source.url);
      if (format === null || url === null) continue;
      assets.push({ name: `Source code (${format})`, url: absoluteUrl(url, web), size: 0, contentType: null });
    }
    return assets;
  }

  /** Last segment of the configured namespaced path — the project's own name. */
  private projectName(): string {
    const segments = this.source.project.split('/').filter(Boolean);
    return segments[segments.length - 1] ?? this.source.project;
  }

  /** `<host>/api/v4/projects/<url-encoded path with namespace>`. */
  private projectUrl(): string {
    const base = this.source.host.replace(/\/+$/, '');
    return `${base}/api/v4/projects/${encodeURIComponent(this.source.project)}`;
  }

  private releaseUrl(tag: string): string {
    return `${this.fallbackWebUrl()}/-/releases/${encodeURIComponent(tag)}`;
  }

  /** Web URL derived from the config, for the rare payload that omits `web_url`. */
  private fallbackWebUrl(): string {
    return `${this.source.host.replace(/\/+$/, '')}/${this.source.project.replace(/^\/+/, '')}`;
  }
}
