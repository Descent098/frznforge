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

/**
 * True when `desc` is longer than the artifact allows.
 *
 * Counted in UTF-16 code units, because that is what `z.string().max(300)` counts. Measuring
 * code points here instead would let a description of 300 emoji (600 units) sail past this
 * check and then fail `parseForgeData`, taking the whole build down over remote data.
 */
export function isDescriptionTooLong(desc: string): boolean {
  return desc.length > MAX_DESCRIPTION;
}

/** Truncate to MAX_DESCRIPTION code units (299 + "…"). Returns the input when short enough. */
export function truncateDescription(desc: string): string {
  if (!isDescriptionTooLong(desc)) return desc;
  let end = MAX_DESCRIPTION - 1;
  // Never cut between the two halves of a surrogate pair — that produces a lone surrogate,
  // which is a valid JS string but mojibake everywhere it is rendered.
  const lead = desc.charCodeAt(end - 1);
  if (lead >= 0xd800 && lead <= 0xdbff) end -= 1;
  return desc.slice(0, end) + '…';
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
    if (typeof obj.description === 'string' && isDescriptionTooLong(obj.description)) {
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
  releaseMode: 'tags' | 'provider';
}

/** Site-config overrides > in-repo file > derived defaults. */
export function mergeMeta(
  defaults: { name: string },
  fileMeta: RepoMetaInput | null,
  overrides: RepoMetaInput | undefined,
): MergedMeta {
  if (overrides?.description !== undefined && isDescriptionTooLong(overrides.description)) {
    throw new Error(
      `config overrides.description for '${defaults.name}' is longer than ${MAX_DESCRIPTION} characters`,
    );
  }
  const pick = <K extends keyof RepoMetaInput>(k: K): RepoMetaInput[K] | undefined =>
    overrides?.[k] !== undefined ? overrides[k] : fileMeta?.[k];
  // `links` merges per key rather than being picked wholesale (which is what every other
  // field does), matching layerMeta in scan.ts: adding a `donations` link in the config must
  // not delete the homepage/issues/upstream links the lower layers derived.
  const links: RepoLinks = { ...fileMeta?.links, ...overrides?.links };
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
