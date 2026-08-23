/**
 * Route derivation from the artifact. Pages call these from getStaticPaths; the sync tests
 * call the same functions to assert "everything in the artifact has a page" without a build.
 *
 * Ref names appear in URLs as a "ref slug": '/' → '~' (git refnames can never contain '~'),
 * so `feat/zip` browses at /repos/<slug>/tree/feat~zip/.
 */
import type { FileInfo, ForgeData, ReleaseAsset, Repo, Tag, TreeEntry } from './data/schema';

/**
 * Code-point string order, matching what the importers use when they sort the artifact
 * (`compareStrings` in src/lib/importers/http.ts). `localeCompare` must not be used for
 * anything that decides rendered order: it depends on the build machine's ICU data, so two
 * machines would emit different HTML from the same artifact.
 */
function cmp(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0;
}

export interface RepoRoute { slug: string; url: string; repo: Repo }

/** One route per repo (including empty repos — they get an explanatory page). */
export function getRepoRoutes(data: ForgeData): RepoRoute[] {
  return data.repos.map((repo) => ({ slug: repo.slug, url: repoUrl(repo.slug), repo }));
}

export function repoUrl(slug: string): string {
  return `/repos/${slug}/`;
}

/* ---- refs ---------------------------------------------------------------- */

export function refSlug(refName: string): string {
  return refName.replace(/\//g, '~');
}
export function refFromSlug(slug: string): string {
  return slug.replace(/~/g, '/');
}

export interface BrowsableRef {
  name: string;
  slugged: string;
  kind: 'branch' | 'tag';
  isDefault: boolean;
  commit: string;
  tree: TreeEntry[];
  files: Record<string, FileInfo>;
}

/** Every ref with a browsable tree: default branch first, then refTrees (branches, then tags). */
export function browsableRefs(repo: Repo): BrowsableRef[] {
  const out: BrowsableRef[] = [];
  if (repo.defaultBranch) {
    const head = repo.branches.find((b) => b.name === repo.defaultBranch);
    out.push({
      name: repo.defaultBranch,
      slugged: refSlug(repo.defaultBranch),
      kind: 'branch',
      isDefault: true,
      commit: head?.head ?? '',
      tree: repo.tree,
      files: repo.files,
    });
  }
  const rest = Object.values(repo.refTrees).sort(
    (a, b) => Number(a.kind === 'tag') - Number(b.kind === 'tag') || cmp(a.name, b.name),
  );
  for (const r of rest) {
    out.push({ name: r.name, slugged: refSlug(r.name), kind: r.kind, isDefault: false, commit: r.commit, tree: r.tree, files: r.files });
  }
  return out;
}

export function findRef(repo: Repo, sluggedRef: string): BrowsableRef | undefined {
  return browsableRefs(repo).find((r) => r.slugged === sluggedRef || r.name === refFromSlug(sluggedRef));
}

/* ---- repo sub-page URLs --------------------------------------------------- */

export const treeUrl = (slug: string, ref: string, path = '') =>
  `/repos/${slug}/tree/${refSlug(ref)}/${path ? path + '/' : ''}`;
export const blobUrl = (slug: string, ref: string, path: string) => `/repos/${slug}/blob/${refSlug(ref)}/${path}/`;
export const rawUrl = (slug: string, ref: string, path: string) => `/repos/${slug}/raw/${refSlug(ref)}/${path}`;
export const commitsUrl = (slug: string, ref: string, page = 1) =>
  `/repos/${slug}/commits/${refSlug(ref)}/${page > 1 ? `page/${page}/` : ''}`;
export const commitUrl = (slug: string, sha: string) => `/repos/${slug}/commit/${sha}/`;
export const branchesUrl = (slug: string) => `/repos/${slug}/branches/`;
export const tagsUrl = (slug: string) => `/repos/${slug}/tags/`;
export const releasesUrl = (slug: string) => `/repos/${slug}/releases/`;
export const releaseUrl = (slug: string, tag: string) => `/repos/${slug}/releases/${refSlug(tag)}/`;
export const archiveUrl = (slug: string, ref: string) => `/repos/${slug}/archive/${refSlug(ref)}.zip`;

/* ---- releases ------------------------------------------------------------- */

/**
 * Annotated tags, newest first — the tag-mode release list.
 *
 * Prefer `resolveReleases()`: this only sees git tags and so misses provider-imported
 * releases. Kept because the tags page and the tag-mode pages read it directly.
 */
export function releasesOf(repo: Repo): Tag[] {
  return repo.gitTags.filter((t) => t.annotated).sort((a, b) => cmp(b.date, a.date) || cmp(a.name, b.name));
}

/**
 * One release as the site renders it, from either origin.
 *
 * `source` says where it came from: `'provider'` releases carry a body written on the forge
 * plus downloadable `assets`; `'tag'` releases are annotated git tags, whose message is the
 * body and which never have assets. `commit` is the commit the tag points at, or `null`
 * when a provider release names a tag this mirror does not have.
 */
export interface SiteRelease {
  /** Tag name — also the URL segment (`releaseUrl(slug, tag)`). */
  tag: string;
  name: string;
  /** Markdown: the provider's release notes, or the annotated tag message. */
  body: string;
  /** Release page on the provider; always null for tag-derived releases. */
  url: string | null;
  prerelease: boolean;
  /** Publish date (provider) or tag date, ISO UTC. */
  date: string;
  assets: ReleaseAsset[];
  source: 'provider' | 'tag';
  commit: string | null;
}

/**
 * The release list for a repo, newest first (date desc, tag asc as tiebreak).
 *
 * Provider-imported releases win when there are any; otherwise this falls back to the
 * annotated-tag derivation, so a repo that switches `releaseMode` — or a remote whose
 * releases could not be fetched this build — still renders.
 */
export function resolveReleases(repo: Repo): SiteRelease[] {
  const out: SiteRelease[] =
    repo.releases.length > 0
      ? repo.releases.map((r) => ({
          tag: r.tag,
          name: r.name,
          body: r.body,
          url: r.url,
          prerelease: r.prerelease,
          date: r.publishedAt,
          assets: r.assets,
          source: 'provider' as const,
          commit: repo.gitTags.find((t) => t.name === r.tag)?.target ?? null,
        }))
      : releasesOf(repo).map((t) => ({
          tag: t.name,
          name: t.name,
          body: t.message ?? '',
          url: null,
          prerelease: false,
          date: t.date,
          assets: [],
          source: 'tag' as const,
          commit: t.target,
        }));
  return out.sort((a, b) => cmp(b.date, a.date) || cmp(a.tag, b.tag));
}

/* ---- exhaustive route listing (pages + sync tests) ------------------------- */

export const COMMITS_PER_PAGE = 50;

export interface FileRoute { repo: Repo; ref: BrowsableRef; entry: TreeEntry }

/** Every directory (tree) route for a repo: one per ref root + one per tree entry. */
export function treeRoutes(repo: Repo): Array<{ repo: Repo; ref: BrowsableRef; path: string; url: string }> {
  const out: Array<{ repo: Repo; ref: BrowsableRef; path: string; url: string }> = [];
  for (const ref of browsableRefs(repo)) {
    out.push({ repo, ref, path: '', url: treeUrl(repo.slug, ref.name) });
    for (const e of ref.tree) {
      if (e.type === 'tree') out.push({ repo, ref, path: e.path, url: treeUrl(repo.slug, ref.name, e.path) });
    }
  }
  return out;
}

/** Every file (blob) route. Submodules are excluded; symlinks get a page. */
export function blobRoutes(repo: Repo): Array<FileRoute & { url: string }> {
  const out: Array<FileRoute & { url: string }> = [];
  for (const ref of browsableRefs(repo)) {
    for (const e of ref.tree) {
      if (e.type === 'blob' || e.type === 'symlink') out.push({ repo, ref, entry: e, url: blobUrl(repo.slug, ref.name, e.path) });
    }
  }
  return out;
}

/** Raw routes exist only for stored blobs (content available in the blob store). */
export function rawRoutes(repo: Repo): Array<FileRoute & { url: string }> {
  return blobRoutes(repo)
    .filter(({ ref, entry }) => ref.files[entry.path]?.stored)
    .map(({ repo: r, ref, entry }) => ({ repo: r, ref, entry, url: rawUrl(r.slug, ref.name, entry.path) }));
}

export function commitsPageCount(repo: Repo, refName: string): number {
  const branch = repo.branches.find((b) => b.name === refName);
  const count = branch ? branch.commits.length : 0;
  return Math.max(1, Math.ceil(count / COMMITS_PER_PAGE));
}

/** Every static URL the site emits for one repo. */
export function repoRoutes(repo: Repo): string[] {
  const urls = [repoUrl(repo.slug)];
  if (repo.empty) return urls;
  urls.push(branchesUrl(repo.slug), tagsUrl(repo.slug), releasesUrl(repo.slug));
  for (const t of treeRoutes(repo)) urls.push(t.url);
  for (const b of blobRoutes(repo)) urls.push(b.url);
  for (const r of rawRoutes(repo)) urls.push(r.url);
  for (const b of repo.branches) {
    for (let p = 1; p <= commitsPageCount(repo, b.name); p++) urls.push(commitsUrl(repo.slug, b.name, p));
  }
  for (const sha of Object.keys(repo.commits)) urls.push(commitUrl(repo.slug, sha));
  // one page per release, from provider data when there is any (dedupe: a provider may
  // report two releases against the same tag)
  const releaseUrls = new Set(resolveReleases(repo).map((r) => releaseUrl(repo.slug, r.tag)));
  for (const url of releaseUrls) urls.push(url);
  for (const a of repo.archives) urls.push(archiveUrl(repo.slug, a.ref));
  return urls;
}

/** Every static URL the site emits. */
export function allRoutes(data: ForgeData): string[] {
  return ['/', '/repos/', '/404', ...data.repos.flatMap((r) => repoRoutes(r))];
}
