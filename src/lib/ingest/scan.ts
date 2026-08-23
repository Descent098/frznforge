/**
 * scanRepo — orchestrates the extractors for one local repository and assembles a `Repo`
 * in schema key order. Never reads the working tree.
 */
import type { Readme, Repo, RepoMetaInput, Warning } from '../data/schema';
import { contributorsFromCommits } from './contributors';
import { loadCommits } from './commits';
import { isGitRepo, looksBinary, readBlob } from './git';
import { languageStats } from './languages';
import { detectSpdx, findLicenseEntry, resolveLicense } from './license';
import { mergeMeta, readRepoMetaFile, repoBasename, slugFor } from './meta';
import { findReadmeEntry } from './readme';
import { detectDefaultBranch, listBranchRefs, loadBranches, loadTags } from './refs';
import { listRootTree, scanTree } from './tree';

export interface ScanSource {
  absPath: string;
  slug?: string;
  overrides?: RepoMetaInput;
}

export interface ScanOptions {
  maxBlobBytes: number;
  maxCommits: number | null;
}

export type ScanResult = { repo: Repo; blobs: Map<string, Buffer> } | { skipped: true; warning: Warning };

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

  const branchesRes = await loadBranches(repoPath, branchRefs, opts.maxCommits);
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

  // metadata
  const metaFile = head ? await readRepoMetaFile(repoPath, head) : { meta: null, warnings: [] as Warning[] };
  metaFile.warnings.forEach(warn);
  const meta = mergeMeta({ name: repoBasename(repoPath) }, metaFile.meta, source.overrides);

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

  // dates
  let createdAt: string | null = null;
  let updatedAt: string | null = null;
  for (const c of commitList) {
    if (createdAt === null || c.commitDate < createdAt) createdAt = c.commitDate;
    if (updatedAt === null || c.commitDate > updatedAt) updatedAt = c.commitDate;
  }

  const repo: Repo = {
    slug,
    name: meta.name,
    description: meta.description,
    source: { type: 'local', path: repoPath },
    links: meta.links,
    tags: meta.tags,
    template: meta.template,
    license,
    releaseMode: meta.releaseMode,
    empty,
    defaultBranch: def.name,
    branches: branchesRes.branches,
    gitTags,
    commits,
    commitCount: commitList.length,
    tree: treeRes.tree,
    files: treeRes.files,
    languages: languageStats(Object.values(treeRes.files)),
    contributors: contributorsFromCommits(commitList),
    readme,
    createdAt,
    updatedAt,
    warnings,
  };
  return { repo, blobs: treeRes.blobs };
}
