/**
 * scanRepo — orchestrates the extractors for one local repository and assembles a `Repo`
 * in schema key order. Never reads the working tree.
 */
import type {
  Archive,
  Readme,
  RefTree,
  Release,
  Repo,
  RepoLinks,
  RepoMetaInput,
  RepoSource,
  Warning,
} from '../data/schema';
import { contributorsFromCommits } from './contributors';
import { loadCommits } from './commits';
import { gitBuffer, isGitRepo, looksBinary, readBlob } from './git';
import { computeInsights, DEFAULT_INSIGHTS_OPTIONS, type InsightsOptions } from './insights';
import { languageStats } from './languages';
import { detectSpdx, findLicenseEntry, resolveLicense } from './license';
import { mergeMeta, readRepoMetaFile, repoBasename, slugFor } from './meta';
import { findReadmeEntry } from './readme';
import { detectDefaultBranch, listBranchRefs, loadBranches, loadTags } from './refs';
import { listRootTree, scanTree } from './tree';
import { isRawServable } from '../routes';

export interface ScanSource {
  absPath: string;
  slug?: string;
  /**
   * Display name to use when no metadata layer sets one. Defaults to the repo directory
   * basename; remote sources pass the name from the config instead, because their directory
   * is a sanitised cache key rather than the repo's real name.
   */
  defaultName?: string;
  overrides?: RepoMetaInput;
  /**
   * Metadata reported by a hosting provider (schema v3). Lowest of the three metadata
   * layers: config `overrides` > the repo's committed `.frznforge.json` > this.
   */
  providerMeta?: RepoMetaInput | null;
  /** Artifact `source` value; defaults to `{ type: 'local', path: absPath }`. */
  source?: RepoSource;
  /** Release mode when neither `.frznforge.json` nor `overrides` picks one. Default 'tags'. */
  releaseMode?: 'tags' | 'provider';
  /** Provider-imported releases; only kept when the effective mode is 'provider'. */
  releases?: Release[];
}

/** Merge two metadata layers field-by-field, `high` winning wherever it is set. */
function layerMeta(high: RepoMetaInput | null, low: RepoMetaInput | null | undefined): RepoMetaInput | null {
  if (!low) return high;
  if (!high) return low;
  const merged: RepoMetaInput = { ...low };
  for (const [key, value] of Object.entries(high)) {
    if (value !== undefined) (merged as Record<string, unknown>)[key] = value;
  }
  const links: RepoLinks = { ...low.links, ...high.links };
  if (Object.keys(links).length > 0) merged.links = links;
  return merged;
}

export interface ScanOptions {
  maxBlobBytes: number;
  maxCommits: number | null;
  /**
   * Keep only commits from the last N days, anchored to the repo's newest branch-head
   * commit date (deterministic — see `loadBranches`). Default null = no limit.
   */
  maxCommitAgeDays?: number | null;
  /** Newest N tags get browsable trees + archives (0 = none). Default 25. */
  tagTrees?: number;
  /**
   * How many NON-default branches get browsable trees, most recently updated first;
   * `'all'` for every branch, `0` for none. Default 10 (schema v5).
   *
   * The default branch always has a tree (`Repo.tree`/`Repo.files`) and never counts against
   * this cap. Tree/blob/raw pages are emitted per browsable ref, so this is the main control
   * on build size.
   */
  branchTrees?: number | 'all';
  /** Produce zip source archives with `git archive`. Default true. */
  archives?: boolean;
  /** Insights knobs (schema v5). Defaults to `DEFAULT_INSIGHTS_OPTIONS`. */
  insights?: InsightsOptions;
}

export type ScanResult =
  | { repo: Repo; blobs: Map<string, Buffer>; archives: Array<{ file: string; data: Buffer }> }
  | { skipped: true; warning: Warning };

/** Ref name → filesystem-safe slug: '/' → '~' (git refnames can never contain '~'). */
export function refSlug(name: string): string {
  return name.replace(/\//g, '~');
}

export async function scanRepo(source: ScanSource, opts: ScanOptions): Promise<ScanResult> {
  const repoPath = source.absPath;
  const slug = slugFor(repoPath, source.slug);

  if (!(await isGitRepo(repoPath))) {
    return {
      skipped: true,
      warning: {
        code: 'repo-not-found',
        repo: null,
        message: `${repoPath} is not a git repository (configured slug '${slug}'); skipped`,
      },
    };
  }

  const warnings: Warning[] = [];
  const warn = (w: Warning) => warnings.push({ ...w, repo: slug });

  // refs
  const branchRefs = await listBranchRefs(repoPath);
  const def = await detectDefaultBranch(repoPath, branchRefs);
  def.warnings.forEach(warn);
  const empty = def.name === null;
  if (empty) warn({ code: 'repo-empty', repo: null, message: 'repository has no commits on any branch' });

  const branchesRes = await loadBranches(repoPath, branchRefs, opts.maxCommits, opts.maxCommitAgeDays ?? null);
  branchesRes.warnings.forEach(warn);
  const gitTags = empty ? [] : await loadTags(repoPath);
  const commits = await loadCommits(repoPath, branchesRes.shas);
  const commitList = Object.values(commits);

  // default-branch tree
  const defaultRef = def.name ? branchRefs.find((b) => b.name === def.name)! : null;
  const head = defaultRef?.head ?? null;
  const treeRes = head
    ? await scanTree(repoPath, head, { maxBlobBytes: opts.maxBlobBytes })
    : { tree: [], files: {}, blobs: new Map<string, Buffer>(), contents: new Map<string, Buffer>() };
  if (head && treeRes.tree.length === 0) {
    warn({
      code: 'default-branch-empty-tree',
      repo: null,
      message: `default branch '${def.name}' has no files at HEAD`,
    });
  }

  // per-ref trees: the newest `branchTrees` non-default branches + the newest `tagTrees` tags
  const tagTreeCap = opts.tagTrees ?? 25;
  const branchTreeCap = opts.branchTrees ?? 10;
  const blobs = new Map<string, Buffer>(treeRes.blobs);
  const treeCache = new Map<string, { tree: typeof treeRes.tree; files: typeof treeRes.files }>();
  if (head) treeCache.set(head, { tree: treeRes.tree, files: treeRes.files });

  const treeFor = async (commit: string) => {
    const cached = treeCache.get(commit);
    if (cached) return cached;
    const res = await scanTree(repoPath, commit, { maxBlobBytes: opts.maxBlobBytes });
    for (const [sha, buf] of res.blobs) blobs.set(sha, buf);
    const entry = { tree: res.tree, files: res.files };
    treeCache.set(commit, entry);
    return entry;
  };

  // Branch trees are capped exactly like tag trees, and for the same reason: tree/blob/raw
  // pages are generated per browsable ref, so N branches multiply the page count by N. The
  // default branch is always browsable (it is `tree`/`files`, not a refTree) and is never
  // counted against the cap; the rest are kept most-recently-updated first, then emitted in
  // name order so `refTrees` key order stays deterministic.
  const nonDefaultBranches = branchRefs.filter((b) => b.name !== def.name);
  const branchesByDateDesc = [...nonDefaultBranches].sort((a, b) =>
    a.headDate > b.headDate ? -1 : a.headDate < b.headDate ? 1 : a.name < b.name ? -1 : 1,
  );
  const treedBranches = (
    branchTreeCap === 'all' ? branchesByDateDesc : branchesByDateDesc.slice(0, branchTreeCap)
  ).sort((a, b) => (a.name < b.name ? -1 : 1));
  const browsableBranchCount = treedBranches.length + (def.name ? 1 : 0);
  if (browsableBranchCount < branchRefs.length) {
    warn({
      code: 'branch-trees-capped',
      repo: null,
      message:
        `${browsableBranchCount} of ${branchRefs.length} branches have browsable trees ` +
        `(ingest.branchTrees = ${branchTreeCap})`,
    });
  }

  const refTrees: Record<string, RefTree> = {};
  for (const ref of treedBranches) {
    const t = await treeFor(ref.head);
    refTrees[ref.name] = { kind: 'branch', name: ref.name, commit: ref.head, tree: t.tree, files: t.files };
  }

  const tagsByDateDesc = [...gitTags].sort((a, b) =>
    a.date > b.date ? -1 : a.date < b.date ? 1 : a.name < b.name ? -1 : 1,
  );
  const treedTags = tagsByDateDesc.slice(0, tagTreeCap).sort((a, b) => (a.name < b.name ? -1 : 1));
  if (gitTags.length > tagTreeCap) {
    warn({
      code: 'tag-trees-capped',
      repo: null,
      message: `${treedTags.length} of ${gitTags.length} tags have browsable trees (ingest.tagTrees = ${tagTreeCap})`,
    });
  }
  for (const tag of treedTags) {
    if (refTrees[tag.name]) continue; // a branch of the same name already claimed the key
    const t = await treeFor(tag.target);
    refTrees[tag.name] = { kind: 'tag', name: tag.name, commit: tag.target, tree: t.tree, files: t.files };
  }

  // zip source archives: default branch + every treed tag
  const archives: Archive[] = [];
  const archiveData: Array<{ file: string; data: Buffer }> = [];
  if (opts.archives ?? true) {
    const targets: Array<{ ref: string; kind: 'branch' | 'tag'; commit: string }> = [];
    if (head && def.name) targets.push({ ref: def.name, kind: 'branch', commit: head });
    for (const tag of treedTags) targets.push({ ref: tag.name, kind: 'tag', commit: tag.target });
    for (const t of targets) {
      const file = `archives/${slug}/${refSlug(t.ref)}.zip`;
      const data = await gitBuffer(repoPath, [
        'archive',
        '--format=zip',
        `--prefix=${slug}-${refSlug(t.ref)}/`,
        t.commit,
      ]);
      archives.push({ ref: t.ref, kind: t.kind, commit: t.commit, file, bytes: data.length });
      archiveData.push({ file, data });
    }
  }

  // metadata
  const metaFile = head ? await readRepoMetaFile(repoPath, head) : { meta: null, warnings: [] as Warning[] };
  metaFile.warnings.forEach(warn);
  // Precedence: config overrides > .frznforge.json > provider metadata > derived defaults.
  // The two lower layers are pre-merged so mergeMeta's own two-layer pick still applies.
  const meta = mergeMeta(
    { name: source.defaultName ?? repoBasename(repoPath) },
    layerMeta(metaFile.meta, source.providerMeta),
    source.overrides,
  );
  const releaseMode: 'tags' | 'provider' =
    source.overrides?.releaseMode ?? metaFile.meta?.releaseMode ?? source.releaseMode ?? 'tags';
  const releases = releaseMode === 'provider' ? (source.releases ?? []) : [];

  // root-level files: license + readme
  const rootEntries = head ? await listRootTree(repoPath, head) : [];
  const readContent = async (sha: string): Promise<Buffer> => treeRes.contents.get(sha) ?? (await readBlob(repoPath, sha));

  let detectedLicense: { file: string; spdx: string | null } | null = null;
  const licEntry = findLicenseEntry(rootEntries);
  if (licEntry) {
    const buf = await readContent(licEntry.sha);
    detectedLicense = { file: licEntry.path, spdx: looksBinary(buf) ? null : detectSpdx(buf.toString('utf8')) };
  }
  const license = resolveLicense(detectedLicense, meta.license);

  let readme: Readme | null = null;
  const readmeEntry = findReadmeEntry(rootEntries);
  if (readmeEntry && (readmeEntry.size ?? 0) <= opts.maxBlobBytes) {
    const buf = await readContent(readmeEntry.sha);
    if (!looksBinary(buf)) readme = { path: readmeEntry.path, sha: readmeEntry.sha, content: buf.toString('utf8') };
  }

  // A committed path (or ref name) carrying `#` or `%` can be listed but not linked: see
  // `isRawServable`. Warn once per offender so the owner learns why a file has no page,
  // rather than the build aborting on an invalid URL or the site shipping a dead link.
  // Refs are checked on the slugged name, which is what actually goes into the URL.
  for (const [refName, entries] of [
    [def.name, treeRes.tree] as const,
    ...Object.values(refTrees).map((rt) => [rt.name, rt.tree] as const),
  ]) {
    if (refName === null) continue;
    if (!isRawServable(refSlug(refName))) {
      warn({
        code: 'repo-path-unservable',
        repo: null,
        message: `ref '${refName}' contains '#' or '%', which a static URL cannot round-trip; its files are not browsable`,
      });
      continue;
    }
    for (const e of entries) {
      if (e.type !== 'blob' && e.type !== 'symlink' && e.type !== 'tree') continue;
      if (isRawServable(e.path)) continue;
      warn({
        code: 'repo-path-unservable',
        repo: null,
        message:
          `'${e.path}' on ref '${refName}' contains '#' or '%', which a static URL cannot ` +
          `round-trip; it is listed in the file table but has no page`,
      });
    }
  }

  // dates
  let createdAt: string | null = null;
  let updatedAt: string | null = null;
  for (const c of commitList) {
    if (createdAt === null || c.commitDate < createdAt) createdAt = c.commitDate;
    if (updatedAt === null || c.commitDate > updatedAt) updatedAt = c.commitDate;
  }

  // insights (schema v5): monthly commits/contributors + a sampled code-size series over the
  // default branch. Sampling is bounded by `ingest.insights` and derived from the commit list,
  // never from a clock, so the artifact stays deterministic.
  const defaultBranch = def.name ? (branchesRes.branches.find((b) => b.name === def.name) ?? null) : null;
  const insightsRes = await computeInsights(repoPath, {
    commits,
    branchCommits: defaultBranch?.commits ?? null,
    head,
    options: opts.insights ?? DEFAULT_INSIGHTS_OPTIONS,
  });
  insightsRes.warnings.forEach(warn);

  const repo: Repo = {
    slug,
    name: meta.name,
    description: meta.description,
    source: source.source ?? { type: 'local', path: repoPath },
    links: meta.links,
    tags: meta.tags,
    template: meta.template,
    license,
    releaseMode,
    releases,
    empty,
    defaultBranch: def.name,
    branches: branchesRes.branches,
    gitTags,
    commits,
    commitCount: commitList.length,
    tree: treeRes.tree,
    files: treeRes.files,
    refTrees,
    archives,
    languages: languageStats(Object.values(treeRes.files)),
    contributors: contributorsFromCommits(commitList),
    insights: insightsRes.insights,
    readme,
    createdAt,
    updatedAt,
    warnings,
  };
  return { repo, blobs, archives: archiveData };
}
