/**
 * Route derivation from the artifact. Pages call these from getStaticPaths; the sync tests
 * call the same functions to assert "every repo in the artifact has a page" without a build.
 */
import type { ForgeData, Repo } from './data/schema';

export interface RepoRoute { slug: string; url: string; repo: Repo }

/** One route per repo (including empty repos — they get an explanatory page). */
export function getRepoRoutes(data: ForgeData): RepoRoute[] {
  return data.repos.map((repo) => ({ slug: repo.slug, url: repoUrl(repo.slug), repo }));
}

export function repoUrl(slug: string): string {
  return `/repos/${slug}/`;
}

/** Every static URL the Phase 2 site emits. */
export function allRoutes(data: ForgeData): string[] {
  return ['/', '/repos/', '/404', ...getRepoRoutes(data).map((r) => r.url)];
}
