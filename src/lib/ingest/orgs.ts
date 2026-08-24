/**
 * Organization resolution (schema v4): turn `config.organizations` plus the `org` field on each
 * repo source into `Organization[]`.
 *
 * Membership is the *union* of the two directions, so a repo can be claimed by the org
 * (`organizations[].repos`) or claim the org itself (`repos[].org`), and doing both is a no-op
 * rather than a duplicate. Dangling references on either side are warnings, never failures:
 * a config edit that renames a repo must not take the site down.
 */
import type { ResolvedConfig } from '../config/index';
import type { Organization, Warning } from '../data/schema';

/**
 * One scanned repo as organization resolution sees it.
 *
 * Deliberately not `Repo`: the artifact's `Repo` carries no `org` (it is a site-config concern,
 * not a property of the repository) and the slug here must be the *final* one, after ingest's
 * collision renaming. `ingest()` builds these pairs from the scanned repos and their sources.
 */
export interface OrgRepoInput {
  /** Final artifact slug — what lands in `Organization.repos` and in URLs. */
  slug: string;
  /** `org` from this repo's source config, or null when it declared none. */
  org: string | null;
}

/** What `resolveOrganizations` hands back to `ingest()`. */
export interface ResolveOrganizationsResult {
  /**
   * Every configured organization, sorted by slug, each with its member repo slugs sorted and
   * de-duplicated. An org with no members is still emitted — it has a page either way.
   */
  organizations: Organization[];
  /**
   * `org-unknown-repo` (an `organizations[].repos` entry matches no ingested repo; `repo: null`)
   * and `repo-unknown-org` (a source's `org` matches no configured organization; `repo` = that
   * repo's slug). Emit orgs in config order then repos in slug order so the list is stable.
   */
  warnings: Warning[];
}

/**
 * Code-point string order. Never `localeCompare`: it depends on the build machine's ICU data,
 * and this ordering ends up in the artifact, which must be byte-identical across machines.
 */
function cmp(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0;
}

/** Mutable accumulator for one organization while both membership directions are collected. */
interface Draft {
  slug: string;
  name: string;
  description: string | null;
  /** Member repo slugs; a Set so the two directions cannot produce a duplicate. */
  members: Set<string>;
  /** Entries already reported as `org-unknown-repo`, so a slug listed twice warns once. */
  reported: Set<string>;
}

/**
 * Resolve organization membership. Pure and synchronous — it only reads config and slugs.
 *
 * Two shapes the config schema allows but does not describe:
 *  - **The same org slug twice in `organizations`.** The definitions are merged: the first
 *    one's `name`/`description` win and the `repos` lists are unioned. Dropping the later one
 *    would silently lose members, and failing would be a build error over a duplicated key.
 *  - **One repo listed by several orgs.** It joins all of them. A repo source's own `org` field
 *    can only name one org, but `organizations[].repos` is a claim, not an exclusive one.
 *
 * @param config Resolved site config; supplies `organizations`.
 * @param repos The scanned repos, already in final-slug order.
 * @returns Organizations sorted by slug with sorted member lists, plus dangling-reference warnings.
 */
export function resolveOrganizations(
  config: ResolvedConfig,
  repos: OrgRepoInput[],
): ResolveOrganizationsResult {
  const warnings: Warning[] = [];
  const known = new Set(repos.map((r) => r.slug));

  // Config order, so `org-unknown-repo` warnings come out in the order the user wrote them.
  const drafts = new Map<string, Draft>();
  for (const org of config.organizations) {
    const existing = drafts.get(org.slug);
    const draft: Draft = existing ?? {
      slug: org.slug,
      name: org.name,
      description: org.description ?? null,
      members: new Set<string>(),
      reported: new Set<string>(),
    };
    if (!existing) drafts.set(org.slug, draft);
    for (const slug of org.repos ?? []) {
      if (known.has(slug)) {
        draft.members.add(slug);
      } else if (!draft.reported.has(slug)) {
        draft.reported.add(slug);
        warnings.push({
          code: 'org-unknown-repo',
          repo: null,
          message: `organization '${org.slug}' lists repo '${slug}', which was not ingested; ignored`,
        });
      }
    }
  }

  // Then the repo → org direction, in slug order regardless of how `repos` was handed over,
  // so the warning list does not depend on config or scan-completion order.
  for (const repo of [...repos].sort((a, b) => cmp(a.slug, b.slug))) {
    if (!repo.org) continue;
    const draft = drafts.get(repo.org);
    if (draft) {
      draft.members.add(repo.slug);
    } else {
      warnings.push({
        code: 'repo-unknown-org',
        repo: repo.slug,
        message: `repo '${repo.slug}' declares org '${repo.org}', which is not configured; ignored`,
      });
    }
  }

  const organizations: Organization[] = [...drafts.values()]
    .sort((a, b) => cmp(a.slug, b.slug))
    .map((d) => ({
      slug: d.slug,
      name: d.name,
      description: d.description,
      repos: [...d.members].sort(cmp),
    }));

  return { organizations, warnings };
}
