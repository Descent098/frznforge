/**
 * Content collections. `profile` holds the single owner profile markdown file whose path
 * comes from frznforge.config.ts (owner.profile). Frontmatter = links + pinned repos;
 * body = rendered on the profile page.
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
});
export type ProfileFrontmatter = z.infer<typeof ProfileFrontmatter>;

const profile = defineCollection({
  loader: glob({ pattern: profileFile, base: pathToFileURL(profileDir + path.sep) }),
  schema: ProfileFrontmatter,
});

export const collections = { profile };
