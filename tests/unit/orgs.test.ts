/**
 * Organization membership resolution (schema v4).
 *
 * Pure input → output: no git, no filesystem, no clock. Everything here is about the union of
 * the two membership directions, the two dangling-reference warnings, and the ordering that
 * makes forge.json byte-identical between runs and between machines.
 */
import os from 'node:os';
import { describe, expect, it } from 'vitest';
import { resolveConfig, type FrznforgeConfigInput } from '../../src/lib/config/index';
import { Organization, emptyForgeData } from '../../src/lib/data/schema';
import { resolveOrganizations, type OrgRepoInput } from '../../src/lib/ingest/orgs';
import { unmatchedOrgContent } from '../../src/lib/routes';

/** A resolved config with just the parts organization resolution reads. */
function configWith(organizations: FrznforgeConfigInput['organizations']) {
  return resolveConfig({ owner: { name: 'Tester', handle: 'tester' }, organizations }, os.tmpdir());
}

/** Shorthand for the scanned-repo pairs `ingest()` hands to `resolveOrganizations`. */
function repo(slug: string, org: string | null = null): OrgRepoInput {
  return { slug, org };
}

describe('resolveOrganizations', () => {
  it('returns nothing when no organizations are configured, whatever the repos say', () => {
    const { organizations, warnings } = resolveOrganizations(configWith([]), [repo('alpha'), repo('beta')]);
    expect(organizations).toEqual([]);
    expect(warnings).toEqual([]);
  });

  it('takes membership from the organization\'s own repos list', () => {
    const cfg = configWith([{ slug: 'acme', name: 'Acme', description: 'Makers of things', repos: ['alpha', 'beta'] }]);
    const { organizations, warnings } = resolveOrganizations(cfg, [repo('alpha'), repo('beta'), repo('gamma')]);
    expect(warnings).toEqual([]);
    expect(organizations).toEqual([
      { slug: 'acme', name: 'Acme', description: 'Makers of things', repos: ['alpha', 'beta'] },
    ]);
  });

  it('takes membership from a repo source declaring `org`', () => {
    const cfg = configWith([{ slug: 'acme', name: 'Acme' }]);
    const { organizations, warnings } = resolveOrganizations(cfg, [repo('alpha', 'acme'), repo('beta')]);
    expect(warnings).toEqual([]);
    expect(organizations).toEqual([{ slug: 'acme', name: 'Acme', description: null, repos: ['alpha'] }]);
  });

  it('unions both directions and never lists a repo twice', () => {
    // `alpha` is claimed by the org *and* claims the org — the shipped frznforge.config.ts
    // does exactly this, so the de-duplication path is exercised by the real site too.
    const cfg = configWith([{ slug: 'acme', name: 'Acme', repos: ['alpha', 'beta'] }]);
    const { organizations, warnings } = resolveOrganizations(cfg, [repo('alpha', 'acme'), repo('beta'), repo('gamma', 'acme')]);
    expect(warnings).toEqual([]);
    expect(organizations[0]!.repos).toEqual(['alpha', 'beta', 'gamma']);
  });

  it('emits an organization with no members rather than dropping it', () => {
    const cfg = configWith([{ slug: 'empty-org', name: 'Empty Org' }]);
    const { organizations, warnings } = resolveOrganizations(cfg, [repo('alpha')]);
    expect(organizations).toEqual([{ slug: 'empty-org', name: 'Empty Org', description: null, repos: [] }]);
    expect(warnings).toEqual([]);
  });

  it('sorts organizations by slug and members by code point, independent of input order', () => {
    const cfg = configWith([
      { slug: 'zulu', name: 'Zulu', repos: ['ab', 'a-b', 'a2'] },
      { slug: 'alpha-co', name: 'Alpha Co', repos: ['a2'] },
    ]);
    const repos = [repo('a2'), repo('ab'), repo('a-b'), repo('m1', 'alpha-co')];
    const { organizations } = resolveOrganizations(cfg, repos);
    expect(organizations.map((o) => o.slug)).toEqual(['alpha-co', 'zulu']);
    // '-' (0x2D) sorts before digits and letters; `localeCompare` would put 'a-b' last.
    expect(organizations[1]!.repos).toEqual(['a-b', 'a2', 'ab']);
    expect(organizations[0]!.repos).toEqual(['a2', 'm1']);

    // Same inputs in a different order → identical output (determinism).
    const shuffled = resolveOrganizations(cfg, [...repos].reverse());
    expect(shuffled.organizations).toEqual(organizations);
  });

  it('warns `org-unknown-repo` once per dangling entry, in config order, with repo: null', () => {
    const cfg = configWith([
      { slug: 'zulu', name: 'Zulu', repos: ['ghost'] },
      { slug: 'acme', name: 'Acme', repos: ['alpha', 'nope', 'nope'] },
    ]);
    const { organizations, warnings } = resolveOrganizations(cfg, [repo('alpha')]);
    expect(warnings.map((w) => ({ code: w.code, repo: w.repo }))).toEqual([
      { code: 'org-unknown-repo', repo: null },
      { code: 'org-unknown-repo', repo: null },
    ]);
    expect(warnings[0]!.message).toContain("organization 'zulu' lists repo 'ghost'");
    expect(warnings[1]!.message).toContain("organization 'acme' lists repo 'nope'");
    // the dangling names never reach the artifact
    expect(organizations.map((o) => o.repos)).toEqual([['alpha'], []]);
  });

  it('warns `repo-unknown-org` in slug order, stamped with the repo slug', () => {
    const cfg = configWith([{ slug: 'acme', name: 'Acme' }]);
    const { organizations, warnings } = resolveOrganizations(cfg, [
      repo('zeta', 'typo-org'),
      repo('alpha', 'other-typo'),
      repo('beta', 'acme'),
    ]);
    expect(warnings.map((w) => ({ code: w.code, repo: w.repo }))).toEqual([
      { code: 'repo-unknown-org', repo: 'alpha' },
      { code: 'repo-unknown-org', repo: 'zeta' },
    ]);
    expect(warnings[0]!.message).toContain("repo 'alpha' declares org 'other-typo'");
    expect(organizations[0]!.repos).toEqual(['beta']);
  });

  it('orders warnings organizations-first, then repos', () => {
    const cfg = configWith([{ slug: 'acme', name: 'Acme', repos: ['ghost'] }]);
    const { warnings } = resolveOrganizations(cfg, [repo('alpha', 'nowhere')]);
    expect(warnings.map((w) => w.code)).toEqual(['org-unknown-repo', 'repo-unknown-org']);
  });

  it('lets several organizations claim the same repo', () => {
    // A repo source's `org` names exactly one org, but `organizations[].repos` is a claim and
    // not an exclusive one — so a shared library can legitimately appear under two groups.
    const cfg = configWith([
      { slug: 'acme', name: 'Acme', repos: ['shared'] },
      { slug: 'beta-co', name: 'Beta Co', repos: ['shared'] },
    ]);
    const { organizations, warnings } = resolveOrganizations(cfg, [repo('shared', 'acme')]);
    expect(warnings).toEqual([]);
    expect(organizations.map((o) => o.repos)).toEqual([['shared'], ['shared']]);
  });

  it('merges a duplicated organization slug instead of failing or losing members', () => {
    const cfg = configWith([
      { slug: 'acme', name: 'Acme', description: 'first', repos: ['alpha'] },
      { slug: 'acme', name: 'Acme Again', description: 'second', repos: ['beta'] },
    ]);
    const { organizations } = resolveOrganizations(cfg, [repo('alpha'), repo('beta')]);
    expect(organizations).toEqual([
      { slug: 'acme', name: 'Acme', description: 'first', repos: ['alpha', 'beta'] },
    ]);
  });

  it('produces organizations that validate against the artifact schema', () => {
    const cfg = configWith([{ slug: 'acme', name: 'Acme', repos: ['alpha', 'ghost'] }]);
    const { organizations } = resolveOrganizations(cfg, [repo('alpha'), repo('beta', 'acme')]);
    for (const org of organizations) expect(() => Organization.parse(org)).not.toThrow();
  });
});

describe('unmatchedOrgContent', () => {
  /** An artifact holding just the organizations named. */
  const withOrgs = (...slugs: string[]) => ({
    ...emptyForgeData(),
    organizations: slugs.map((slug) => ({ slug, name: slug, description: null, repos: [] })),
  });

  it('names every orgs markdown file no configured organization claims', () => {
    // One typo in the filename otherwise discards the file's prose, sites, links and pinned
    // repos with no page and no other symptom.
    const data = withOrgs('canadian-coding', 'lonely');
    expect(unmatchedOrgContent(data, ['canadian-codng', 'canadian-coding', 'ghost'])).toEqual([
      'canadian-codng',
      'ghost',
    ]);
  });

  it('is quiet when every file matches, and when there are no files at all', () => {
    const data = withOrgs('canadian-coding', 'lonely');
    expect(unmatchedOrgContent(data, ['lonely', 'canadian-coding'])).toEqual([]);
    expect(unmatchedOrgContent(data, [])).toEqual([]);
    // no organizations configured: every file is unmatched, and still sorted
    expect(unmatchedOrgContent(emptyForgeData(), ['b', 'a'])).toEqual(['a', 'b']);
  });
});
