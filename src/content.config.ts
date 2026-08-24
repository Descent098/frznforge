/**
 * Content collections.
 *
 * `profile` holds the single owner profile markdown file whose path comes from
 * frznforge.config.ts (owner.profile). Frontmatter = links + pinned repos; body = rendered on
 * the profile page.
 *
 * `orgs` (schema v4) is the same mechanism, one file per organization:
 * `<content.orgs>/<org-slug>.md`. The collection is the *optional* half of an organization —
 * membership and name come from config, so an org with no markdown file still gets a page.
 *
 * Both loaders take a `file://` base built with `pathToFileURL`: the glob loader wants a URL,
 * and on Windows a bare `C:\…` path is not one.
 */
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { defineCollection } from 'astro:content';
import { glob } from 'astro/loaders';
import { z } from 'astro/zod';
import userConfig from '../frznforge.config';
import { resolveConfig } from './lib/config/index';

const cfg = resolveConfig(userConfig);
const profileDir = path.dirname(cfg.profilePath);
const profileFile = path.basename(cfg.profilePath);

export const ProfileFrontmatter = z.object({
  /** Short tagline under the name. */
  bio: z.string().max(300).optional(),
  location: z.string().optional(),
  workplace: z.string().optional(),
  school: z.string().optional(),
  email: z.email().optional(),
  /** Personal sites, shown as pills (first one is highlighted). */
  sites: z.array(z.url()).default([]),
  linkedin: z.url().optional(),
  /** Other forges where the owner has an account. */
  forges: z
    .object({
      github: z.url().optional(),
      gitlab: z.url().optional(),
      codeberg: z.url().optional(),
      forgejo: z.url().optional(),
      gitea: z.url().optional(),
    })
    .prefault({}),
  /** Repo slugs to pin on the profile (max 10, in order). */
  pinned: z.array(z.string()).max(10).default([]),
  /** Author emails counted as "you" in the contribution graph. Empty = count all commits. */
  identities: z.array(z.email()).default([]),
});
export type ProfileFrontmatter = z.infer<typeof ProfileFrontmatter>;

const profile = defineCollection({
  loader: glob({ pattern: profileFile, base: pathToFileURL(profileDir + path.sep) }),
  schema: ProfileFrontmatter,
});

/**
 * Frontmatter of `<content.orgs>/<org-slug>.md`. The entry `id` is the filename without the
 * extension, which is the organization slug — that is how a page finds its markdown.
 *
 * Deliberately smaller than `ProfileFrontmatter`: an org has no bio/location/school, and its
 * name and membership come from `frznforge.config.ts`, not from here.
 */
export const OrgFrontmatter = z.object({
  /** Overrides the `description` set in `organizations[]` in the config. */
  description: z.string().max(300).optional(),
  /** Org sites, shown as pills (first one is highlighted), same as the profile page. */
  sites: z.array(z.url()).default([]),
  /** Free-form label → URL pairs (chat, docs, forge account, …). */
  links: z.record(z.string(), z.url()).optional(),
  /** Repo slugs to pin on the org overview (max 10, in order). Must be members of the org. */
  pinned: z.array(z.string()).max(10).default([]),
});
export type OrgFrontmatter = z.infer<typeof OrgFrontmatter>;

const orgs = defineCollection({
  loader: glob({ pattern: '*.md', base: pathToFileURL(cfg.orgsDir + path.sep) }),
  schema: OrgFrontmatter,
});

export const collections = { profile, orgs };
