/**
 * Route derivation from the artifact. Pages call these from getStaticPaths; the sync tests
 * call the same functions to assert "everything in the artifact has a page" without a build.
 *
 * Ref names appear in URLs as a "ref slug": '/' → '~' (git refnames can never contain '~'),
 * so `feat/zip` browses at /repos/<slug>/tree/feat~zip/.
 *
 * Every URL a builder returns is prefixed with the deploy base (`site.base`, 0.2.0) via
 * `withBase()` — pages, the search index and the sync tests all consume the same builders,
 * so a sub-path deploy prefixes everything or nothing. getStaticPaths params never come
 * from these URLs (routes carry `ref`/`path`/`slug` fields for that), which is what lets
 * the URLs carry the base while the emitted file paths do not.
 */
import { withBase } from './base';
import type {
  FileInfo,
  ForgeData,
  HostedSite,
  Note,
  NoteFile,
  Organization,
  ReleaseAsset,
  Repo,
  Tag,
  TreeEntry,
} from './data/schema';

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
  return withBase(`/repos/${slug}/`);
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

/**
 * Percent-encode each segment of a path that goes into a URL, keeping `/` literal so the
 * directory structure survives.
 *
 * Repo paths and ref names are whatever was committed, not slugs: spaces, `&`, `+`, `;`,
 * `#` and non-ASCII are all legal in a git path, and a raw interpolation produces either an
 * invalid href (`href="/repos/x/blob/main/read me.md/"`) or a build abort. See
 * `isRawServable` for the two characters encoding cannot rescue.
 */
export const encodePathSegments = (path: string) => path.split('/').map(encodeURIComponent).join('/');

export const treeUrl = (slug: string, ref: string, path = '') =>
  withBase(`/repos/${slug}/tree/${encodePathSegments(refSlug(ref))}/${path ? encodePathSegments(path) + '/' : ''}`);
export const blobUrl = (slug: string, ref: string, path: string) =>
  withBase(`/repos/${slug}/blob/${encodePathSegments(refSlug(ref))}/${encodePathSegments(path)}/`);
export const rawUrl = (slug: string, ref: string, path: string) =>
  withBase(`/repos/${slug}/raw/${encodePathSegments(refSlug(ref))}/${encodePathSegments(path)}`);
export const commitsUrl = (slug: string, ref: string, page = 1) =>
  withBase(`/repos/${slug}/commits/${refSlug(ref)}/${page > 1 ? `page/${page}/` : ''}`);
export const commitUrl = (slug: string, sha: string) => withBase(`/repos/${slug}/commit/${sha}/`);
export const branchesUrl = (slug: string) => withBase(`/repos/${slug}/branches/`);
export const tagsUrl = (slug: string) => withBase(`/repos/${slug}/tags/`);
export const releasesUrl = (slug: string) => withBase(`/repos/${slug}/releases/`);
/** The insights page (schema v5). Only built when `hasInsights(repo)` — see there. */
export const insightsUrl = (slug: string) => withBase(`/repos/${slug}/insights/`);
export const releaseUrl = (slug: string, tag: string) => withBase(`/repos/${slug}/releases/${refSlug(tag)}/`);
export const archiveUrl = (slug: string, ref: string) => withBase(`/repos/${slug}/archive/${refSlug(ref)}.zip`);

/* ---- insights (schema v5) --------------------------------------------------- */

/**
 * Whether a repo has an insights page — and therefore an Insights tab.
 *
 * `Repo.insights` is `null` for an empty repo and whenever `ingest.insights.enabled` is off,
 * and a repo whose history produced no monthly buckets has nothing to plot. One helper so the
 * page's `getStaticPaths`, `repoRoutes()`, the sync test and `RepoHeader.astro` cannot drift
 * apart: a tab without a page is a dead link, a page without a tab is unreachable.
 */
export function hasInsights(repo: Repo): boolean {
  return !repo.empty && repo.insights !== null && repo.insights.commits.length > 0;
}

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

/* ---- notes & organizations (schema v4) ------------------------------------ */

/**
 * Index of every note. Always exists, even with no notes (like `/repos/`). A function
 * rather than a constant so a base mocked mid-process (`setSiteBase` in tests) applies —
 * a module-load-time constant would capture whichever base was set first.
 */
export const notesIndexUrl = () => withBase('/notes/');
/** Index of every organization. Always exists, even with no organizations. */
export const orgsIndexUrl = () => withBase('/orgs/');

/** One note's page: all of its files. */
export const noteUrl = (slug: string) => withBase(`/notes/${slug}/`);

/**
 * Characters in a file path that no static URL can round-trip.
 *
 * This governs BOTH note raw routes and repo `tree`/`blob`/`raw` routes: a note file name is
 * whatever the author typed on disk and a repo path is whatever was committed, so neither is
 * a slug and both can hold anything the filesystem (or git) allows. Percent-encoding handles
 * almost all of it — spaces, `&`, `+`, `;`, non-ASCII all survive, because the build writes
 * the file under its literal name and the browser's decoded request matches it. Two
 * characters do not:
 *
 *  - `#` — the build escapes it in the OUTPUT FILENAME (`c%23-tips.md` lands on disk with a
 *    literal `%23`), so the encoded URL decodes to a name that is not there;
 *  - `%` — the generated path is re-encoded and then decoded, and `50%%20off.txt` is not
 *    valid percent-encoding, which aborts the whole build.
 *
 * Rather than emit a dead link (or fail), such a file is published without a raw route: it
 * still renders inline on the note page, and ingest raises `note-file-unservable`.
 */
const UNSERVABLE_CHARS = /[#%]/;

/**
 * Whether a path can appear in a static URL at all. See `UNSERVABLE_CHARS`.
 *
 * Used for note file paths (raw routes) and for repo paths and ref names (tree, blob and
 * raw routes). A path this rejects gets no route: ingest warns (`note-file-unservable`,
 * `repo-path-unservable`) and the UI renders the name without a link, which beats emitting
 * a dead one or aborting the build.
 *
 * @param filePath A `NoteFile.path`, a repo `TreeEntry.path`, or a slugged ref name —
 *   forward slashes, relative to its root.
 */
export function isRawServable(filePath: string): boolean {
  return !UNSERVABLE_CHARS.test(filePath);
}

/**
 * Raw bytes of one file in a note. `filePath` is `NoteFile.path` (relative to the note root,
 * forward slashes); each segment is percent-encoded, because a note file name is authored by
 * hand and may hold spaces, `&`, `+` or non-ASCII. Directory separators stay literal so the
 * URL keeps the note's folder structure. No trailing slash, matching `rawUrl` for repo files.
 *
 * Only call this for paths `isRawServable` accepts — the two characters it rejects have no
 * encoding that survives a static build (see `UNSERVABLE_CHARS`).
 */
export const noteRawUrl = (slug: string, filePath: string) =>
  withBase(`/notes/${slug}/raw/${filePath.split('/').map(encodeURIComponent).join('/')}`);
/** An organization's overview page (profile markdown + pinned repos + members). */
export const orgUrl = (slug: string) => withBase(`/orgs/${slug}/`);
/** The full repo listing scoped to one organization. */
export const orgReposUrl = (slug: string) => withBase(`/orgs/${slug}/repos/`);

export interface NoteRoute { slug: string; url: string; note: Note }

/** One route per note, in artifact order. */
export function getNoteRoutes(data: ForgeData): NoteRoute[] {
  return data.notes.map((note) => ({ slug: note.slug, url: noteUrl(note.slug), note }));
}

export interface NoteFileRoute { note: Note; file: NoteFile; url: string }

/**
 * Raw routes exist only for stored note files whose path a static URL can round-trip — the
 * rest either have no bytes to serve (`stored: false`) or no URL that would reach them
 * (`isRawServable`, which ingest has already warned about).
 */
export function noteRawRoutes(data: ForgeData): NoteFileRoute[] {
  const out: NoteFileRoute[] = [];
  for (const note of data.notes) {
    for (const file of note.files) {
      if (file.stored && isRawServable(file.path)) out.push({ note, file, url: noteRawUrl(note.slug, file.path) });
    }
  }
  return out;
}

export interface OrgRoute { slug: string; url: string; org: Organization; repos: Repo[] }

/** One route per organization, in artifact order (slug asc), with its member repos resolved. */
export function getOrgRoutes(data: ForgeData): OrgRoute[] {
  return data.organizations.map((org) => ({
    slug: org.slug,
    url: orgUrl(org.slug),
    org,
    repos: reposInOrg(data, org),
  }));
}

/**
 * Member repos of an organization, in `Organization.repos` order (slug asc). Slugs with no
 * matching repo are dropped — ingest already warned (`org-unknown-repo`), and a page must not
 * blow up over a stale config entry.
 */
export function reposInOrg(data: ForgeData, org: Organization): Repo[] {
  const bySlug = new Map(data.repos.map((r) => [r.slug, r]));
  return org.repos.map((slug) => bySlug.get(slug)).filter((r): r is Repo => r !== undefined);
}

/** Every static URL the site emits for notes: the index, each note, each stored file's raw URL. */
export function notesRoutes(data: ForgeData): string[] {
  const urls = [notesIndexUrl()];
  for (const { url } of getNoteRoutes(data)) urls.push(url);
  for (const { url } of noteRawRoutes(data)) urls.push(url);
  return urls;
}

/**
 * Ids in the `orgs` content collection that no configured organization claims, sorted.
 *
 * An orgs markdown file is only ever reached through an organization's slug, so one typo in
 * the filename silently discards the whole file — prose, sites, links, pinned repos — with no
 * page and no other symptom. Ingest cannot catch it (the collection is a site-build concern,
 * not an artifact one), so `/orgs/` reports it at build time instead.
 *
 * @param data The artifact, for the organizations that exist.
 * @param contentIds Collection entry ids — for `content/orgs/<slug>.md`, the `<slug>`.
 */
export function unmatchedOrgContent(data: ForgeData, contentIds: readonly string[]): string[] {
  const configured = new Set(data.organizations.map((o) => o.slug));
  return contentIds.filter((id) => !configured.has(id)).sort(cmp);
}

/** Every static URL the site emits for organizations: the index, each org, each org's listing. */
export function orgRoutes(data: ForgeData): string[] {
  const urls = [orgsIndexUrl()];
  for (const org of data.organizations) urls.push(orgUrl(org.slug), orgReposUrl(org.slug));
  return urls;
}

/* ---- hosted static sites (schema v7) -------------------------------------- */

export interface HostedFileRoute { site: HostedSite; path: string; sha: string; url: string }

/**
 * Every servable file of every hosted site, in artifact order (sites by slug, files by
 * path). Each file is emitted at its LITERAL path under `/<slug>/` — `index.html` lands at
 * `<slug>/index.html`, so `/<slug>/` works through the same directory-index resolution
 * every static host (and the site's own preview server) already does. Unstored files and
 * `#`/`%` paths have nothing to serve (ingest warned `hosting-file-unservable`).
 */
export function hostedFiles(data: ForgeData): HostedFileRoute[] {
  const out: HostedFileRoute[] = [];
  const bySlug = new Map(data.repos.map((r) => [r.slug, r]));
  for (const site of data.hosting) {
    const repo = bySlug.get(site.repo);
    if (!repo) continue; // the resolver only records existing repos; never crash over a hand-edited artifact
    const files = site.ref === repo.defaultBranch ? repo.files : (repo.refTrees[site.ref]?.files ?? {});
    for (const [p, info] of Object.entries(files)) {
      if (!info.stored || !isRawServable(p)) continue;
      out.push({ site, path: p, sha: info.sha, url: withBase(`/${site.slug}/${encodePathSegments(p)}`) });
    }
  }
  return out;
}

/** Every static URL the hosted sites emit (one per served file). */
export function hostedRoutes(data: ForgeData): string[] {
  return hostedFiles(data).map((f) => f.url);
}

/* ---- exhaustive route listing (pages + sync tests) ------------------------- */

export const COMMITS_PER_PAGE = 50;

export interface FileRoute { repo: Repo; ref: BrowsableRef; entry: TreeEntry }

/**
 * Every directory (tree) route for a repo: one per ref root + one per tree entry.
 *
 * Paths and refs that no static URL can round-trip are skipped (`isRawServable`); ingest has
 * already raised `repo-path-unservable` for them.
 */
export function treeRoutes(repo: Repo): Array<{ repo: Repo; ref: BrowsableRef; path: string; url: string }> {
  const out: Array<{ repo: Repo; ref: BrowsableRef; path: string; url: string }> = [];
  for (const ref of browsableRefs(repo)) {
    if (!isRawServable(ref.slugged)) continue;
    out.push({ repo, ref, path: '', url: treeUrl(repo.slug, ref.name) });
    for (const e of ref.tree) {
      if (e.type === 'tree' && isRawServable(e.path)) out.push({ repo, ref, path: e.path, url: treeUrl(repo.slug, ref.name, e.path) });
    }
  }
  return out;
}

/**
 * Every file (blob) route. Submodules are excluded; symlinks get a page. Unservable paths
 * and refs are skipped for the same reason as `treeRoutes`.
 */
export function blobRoutes(repo: Repo): Array<FileRoute & { url: string }> {
  const out: Array<FileRoute & { url: string }> = [];
  for (const ref of browsableRefs(repo)) {
    if (!isRawServable(ref.slugged)) continue;
    for (const e of ref.tree) {
      if ((e.type === 'blob' || e.type === 'symlink') && isRawServable(e.path)) {
        out.push({ repo, ref, entry: e, url: blobUrl(repo.slug, ref.name, e.path) });
      }
    }
  }
  return out;
}

/**
 * Raw routes exist only for stored blobs (content available in the blob store). Derived from
 * `blobRoutes`, so it inherits the unservable-path exclusion.
 */
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
  if (hasInsights(repo)) urls.push(insightsUrl(repo.slug));
  for (const t of treeRoutes(repo)) urls.push(t.url);
  for (const b of blobRoutes(repo)) urls.push(b.url);
  for (const r of rawRoutes(repo)) urls.push(r.url);
  for (const b of repo.branches) {
    for (let p = 1; p <= commitsPageCount(repo, b.name); p++) urls.push(commitsUrl(repo.slug, b.name, p));
  }
  for (const sha of Object.keys(repo.commits)) urls.push(commitUrl(repo.slug, sha));
  // Display-support commits (schema v6) get pages too — file tables and tag rows link to
  // them. Disjoint from `commits` by construction, so no dedupe is needed.
  for (const sha of Object.keys(repo.extraCommits)) urls.push(commitUrl(repo.slug, sha));
  // one page per release, from provider data when there is any (dedupe: a provider may
  // report two releases against the same tag)
  const releaseUrls = new Set(resolveReleases(repo).map((r) => releaseUrl(repo.slug, r.tag)));
  for (const url of releaseUrls) urls.push(url);
  for (const a of repo.archives) urls.push(archiveUrl(repo.slug, a.ref));
  return urls;
}

/**
 * Every static URL the site emits.
 *
 * The Phase 6 sync test asserts the build against exactly this list, so the notes and orgs
 * index pages are included unconditionally — like `/repos/`, they exist (and say "nothing
 * here yet") even when the artifact has none. Only the sidebar hides them at count 0.
 */
export function allRoutes(data: ForgeData): string[] {
  return [
    withBase('/'),
    withBase('/repos/'),
    withBase('/404'),
    ...data.repos.flatMap((r) => repoRoutes(r)),
    ...notesRoutes(data),
    ...orgRoutes(data),
    ...hostedRoutes(data),
  ];
}
