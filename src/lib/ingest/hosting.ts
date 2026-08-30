/**
 * Hosted static sites (schema v7): resolve the config's `hosting.sites` entries against
 * the scanned repos into the artifact's `ForgeData.hosting` records.
 *
 * Two halves share the branch-resolution rule: `scanRepo` uses `resolveHostedBranch` at
 * scan time to know which branches must get (cap-exempt, big-file-capable) trees, and the
 * assembly uses it again here against the exact same inputs — a branch list is a pure
 * function of the repo, so both resolve identically. Dangling references follow the
 * organizations precedent: warnings, never failures.
 */
import fs from 'node:fs';
import path from 'node:path';
import type { ResolvedConfig } from '../config/index';
import type { HostedSite, Repo, Warning } from '../data/schema';
import { isRawServable } from '../routes';

/** Default branch lookup order when a hosting entry names none (version-2.md's order). */
export const HOSTED_BRANCH_FALLBACKS = ['gh-pages', 'main', 'master'] as const;

/** The branch a hosted entry serves: the configured one, else the first existing fallback. */
export function resolveHostedBranch(branchNames: readonly string[], requested?: string): string | null {
  if (requested !== undefined) return branchNames.includes(requested) ? requested : null;
  for (const candidate of HOSTED_BRANCH_FALLBACKS) {
    if (branchNames.includes(candidate)) return candidate;
  }
  return null;
}

export interface ResolveHostingResult {
  /** Sorted by slug — artifact order. */
  hosting: HostedSite[];
  /** Site-level warnings (`repo: null`). */
  warnings: Warning[];
}

/**
 * Resolve every hosting entry against the FINAL (post-collision-rename) repos, so
 * `hosting.sites[].repo` always means the slug that actually reached the artifact — the
 * same rule organization membership follows. Repo-scoped problems (missing branch,
 * unservable paths) are pushed onto the matched repo's own warning list, so call this
 * BEFORE the assembly mirrors repo warnings into the site-level list.
 */
export function resolveHosting(config: ResolvedConfig, repos: Repo[]): ResolveHostingResult {
  const warnings: Warning[] = [];
  const hosting: HostedSite[] = [];
  const bySlug = new Map(repos.map((r) => [r.slug, r]));

  // The reserved-slug set is static and checked at config parse; the user-owned `public/`
  // directory is not, so it is checked here — a HARD error, like an invalid config: a
  // hosted site silently fighting `public/x` over who owns `/x/…` would be a wrong site
  // that reports success.
  let publicEntries: Set<string>;
  try {
    publicEntries = new Set(fs.readdirSync(path.join(config.root, 'public')));
  } catch {
    publicEntries = new Set(); // no public/ — nothing to collide with
  }

  for (const site of config.hosting.sites) {
    const slug = site.slug ?? site.repo;
    if (publicEntries.has(slug)) {
      throw new Error(
        `hosted site '/${slug}/' collides with public/${slug} — rename one of them ` +
          `(public/ files are copied to the site root verbatim)`,
      );
    }
    const repo = bySlug.get(site.repo);
    if (!repo) {
      warnings.push({
        code: 'hosting-unknown-repo',
        repo: null,
        message: `hosting entry '${slug}' names repo '${site.repo}', which is not in the artifact; the site is not served`,
      });
      continue;
    }
    const ref = resolveHostedBranch(repo.branches.map((b) => b.name), site.branch);
    if (ref === null) {
      repo.warnings.push({
        code: 'hosting-branch-missing',
        repo: repo.slug,
        message:
          site.branch !== undefined
            ? `hosted branch '${site.branch}' does not exist; '/${slug}/' is not served`
            : `no branch to host (none of ${HOSTED_BRANCH_FALLBACKS.join(', ')} exist); '/${slug}/' is not served`,
      });
      continue;
    }
    const files = ref === repo.defaultBranch ? repo.files : (repo.refTrees[ref]?.files ?? {});
    const unservable = Object.keys(files).filter((p) => !isRawServable(p)).length;
    if (unservable > 0) {
      repo.warnings.push({
        code: 'hosting-file-unservable',
        repo: repo.slug,
        message:
          `${unservable} file(s) on hosted branch '${ref}' contain '#' or '%', which no static URL ` +
          `can round-trip; they are missing from '/${slug}/'`,
      });
    }
    hosting.push({ slug, repo: repo.slug, ref });
  }

  hosting.sort((a, b) => (a.slug < b.slug ? -1 : a.slug > b.slug ? 1 : 0));
  return { hosting, warnings };
}
