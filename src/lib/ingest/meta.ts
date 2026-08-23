/**
 * Repo metadata: `.frznforge.json` from the default branch tree (never the working copy),
 * merged with site-config overrides and derived defaults.
 */
import path from 'node:path';
import { RepoMetaInput, Slug, type RepoLinks, type Warning } from '../data/schema';
import { gitMaybe, readBlob } from './git';

export const META_FILENAME = '.frznforge.json';
export const MAX_DESCRIPTION = 300;

export interface RepoMetaFileResult {
  meta: RepoMetaInput | null;
  warnings: Warning[];
}

/** Truncate to MAX_DESCRIPTION chars (297 + "…"). Returns the input when short enough. */
export function truncateDescription(desc: string): string {
  const chars = Array.from(desc);
  if (chars.length <= MAX_DESCRIPTION) return desc;
  return chars.slice(0, MAX_DESCRIPTION - 3).join('') + '…';
}

/**
 * Read + validate `.frznforge.json` from `<treeish>:.frznforge.json`. Missing ⇒ meta null,
 * no warnings. Invalid JSON / schema ⇒ `repo-meta-invalid`, meta null. Over-long
 * description ⇒ truncated + `description-truncated`.
 */
export async function readRepoMetaFile(repo: string, treeish: string): Promise<RepoMetaFileResult> {
  const warnings: Warning[] = [];
  const sha = await gitMaybe(repo, ['rev-parse', '--verify', '--quiet', `${treeish}:${META_FILENAME}`]);
  if (sha === null || !sha.trim()) return { meta: null, warnings };
  const content = await readBlob(repo, sha.trim());
  let parsed: unknown;
  try {
    parsed = JSON.parse(content.toString('utf8'));
  } catch (e) {
    warnings.push({
      code: 'repo-meta-invalid',
      repo: null,
      message: `${META_FILENAME} is not valid JSON (${(e as Error).message}); ignored`,
    });
    return { meta: null, warnings };
  }
  if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
    const obj = parsed as Record<string, unknown>;
    if (typeof obj.description === 'string' && Array.from(obj.description).length > MAX_DESCRIPTION) {
      obj.description = truncateDescription(obj.description);
      warnings.push({
        code: 'description-truncated',
        repo: null,
        message: `${META_FILENAME} description exceeded ${MAX_DESCRIPTION} characters and was truncated`,
      });
    }
  }
  const result = RepoMetaInput.safeParse(parsed);
  if (!result.success) {
    const issues = result.error.issues.map((i) => `${i.path.join('.') || '(root)'}: ${i.message}`).join('; ');
    return {
      meta: null,
      warnings: [
        { code: 'repo-meta-invalid', repo: null, message: `${META_FILENAME} failed validation (${issues}); ignored` },
      ],
    };
  }
  return { meta: result.data, warnings };
}

export interface MergedMeta {
  name: string;
  description: string | null;
  links: RepoLinks;
  tags: string[];
  template: boolean;
  /** SPDX override, if any. */
  license: string | null;
  releaseMode: 'tags';
}

/** Site-config overrides > in-repo file > derived defaults. */
export function mergeMeta(
  defaults: { name: string },
  fileMeta: RepoMetaInput | null,
  overrides: RepoMetaInput | undefined,
): MergedMeta {
  if (overrides?.description !== undefined && Array.from(overrides.description).length > MAX_DESCRIPTION) {
    throw new Error(
      `config overrides.description for '${defaults.name}' is longer than ${MAX_DESCRIPTION} characters`,
    );
  }
  const pick = <K extends keyof RepoMetaInput>(k: K): RepoMetaInput[K] | undefined =>
    overrides?.[k] !== undefined ? overrides[k] : fileMeta?.[k];
  const links = pick('links') ?? {};
  return {
    name: pick('name') ?? defaults.name,
    description: pick('description') ?? null,
    links: sortedLinks(links),
    tags: [...(pick('tags') ?? [])],
    template: pick('template') ?? false,
    license: pick('license') ?? null,
    releaseMode: pick('releaseMode') ?? 'tags',
  };
}

/** Rebuild links in schema key order (determinism). */
function sortedLinks(links: RepoLinks): RepoLinks {
  const out: RepoLinks = {};
  if (links.homepage !== undefined) out.homepage = links.homepage;
  if (links.issues !== undefined) out.issues = links.issues;
  if (links.donations !== undefined) out.donations = links.donations;
  if (links.upstream !== undefined) out.upstream = links.upstream;
  return out;
}

/** Directory basename → slug: lowercase, non-[a-z0-9] runs → '-', trimmed. */
export function slugify(name: string): string {
  const s = name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '');
  return s.length ? s : 'repo';
}

/** Slug for a repo source: explicit slug, else slugified directory basename. */
export function slugFor(absPath: string, explicit?: string): string {
  const slug = explicit ?? slugify(repoBasename(absPath));
  const ok = Slug.safeParse(slug);
  if (!ok.success) throw new Error(`invalid slug ${JSON.stringify(slug)} for ${absPath}`);
  return slug;
}

/** Directory basename; for bare repos named `foo.git` the `.git` suffix is dropped. */
export function repoBasename(absPath: string): string {
  const base = path.basename(path.resolve(absPath));
  return base.endsWith('.git') && base.length > 4 ? base.slice(0, -4) : base;
}
