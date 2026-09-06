/**
 * Repo listing: filter / sort / search / paginate.
 *
 * The pure query logic lives in `web/js/listing.js` — the browser runs the same bytes the
 * build does, so a filter can never mean two different things on the two sides. This module
 * re-exports it and adds `summarize()`, which takes a full `Repo` and so is build-only.
 */
import type { Repo } from './data/schema';
import type { RepoSummary } from '../../web/js/listing.js';

export * from '../../web/js/listing.js';

/** Project a Repo down to what cards and filtering need (no commits/tree/files). */
export function summarize(repo: Repo): RepoSummary {
  return {
    slug: repo.slug,
    name: repo.name,
    description: repo.description,
    tags: repo.tags,
    template: repo.template,
    empty: repo.empty,
    languages: repo.languages.map((l) => ({ name: l.name, percent: l.percent, color: l.color })),
    commitCount: repo.commitCount,
    createdAt: repo.createdAt,
    updatedAt: repo.updatedAt,
  };
}
