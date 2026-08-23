// frznforge site configuration.
// Read at build time (astro build / astro dev / npm run ingest) — changing it requires a rebuild.
// Reference: docs/user/configuration.md

import { defineConfig } from './src/lib/config/schema';

export default defineConfig({
  site: {
    title: 'frznforge',
    // url: 'https://forge.example.com', // used for absolute links / feeds later
  },

  owner: {
    name: 'Kieran Wood',
    handle: 'kieran',
    /** Markdown file rendered on the profile page; frontmatter carries links + pinned repos. */
    profile: './content/profile.md',
  },

  theme: {
    /**
     * Colour palette. Layout and components are identical; only colours change.
     *  - 'hearth' — warm: off-white / ember-tinted charcoal canvas, ember as the action colour.
     *  - 'frost'  — cool: slate / blue-tinted navy canvas, ice as the action colour.
     */
    palette: 'hearth',
  },

  /**
   * Repositories to ingest. Phase 1: local paths only (absolute, or relative to this file).
   * Each entry may override metadata that would otherwise come from the repo's own
   * `.frznforge.json` (read from the committed tree, never the working copy).
   *
   * Example:
   *   { type: 'local', path: '../useful', slug: 'useful',
   *     overrides: { description: 'Offline-first toolbox', tags: ['pwa'] } },
   */
  repos: [
    // Until you add repos here, the site builds with an empty listing.
    // Self-host demo: the frznforge repo itself.
    { type: 'local', path: '.', slug: 'frznforge' },
  ],

  ingest: {
    /** Where forge.json + blobs/ are written. Relative to this file. */
    outDir: './data',
    /** Text files larger than this are listed but their content is not stored. */
    maxBlobBytes: 512 * 1024,
    /** Cap on commits stored per repo (null = all). Keeps huge histories bounded. */
    maxCommits: null,
    /** How many repos to scan concurrently. */
    concurrency: 4,
  },

  listing: {
    pageSize: 50,
  },
});
