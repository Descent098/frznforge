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
    { type: 'local', path: '.', slug: 'frznforge', org: 'canadian-coding' },
  ],

  /**
   * Organizations repos can be grouped under. Membership is the union of this `repos` list and
   * any repo source's own `org` field, so either direction alone is enough — frznforge is
   * named here *and* declares `org` above, on purpose, to exercise the de-duplication.
   *
   * Prose, links and pinned repos come from `./content/orgs/<slug>.md` (see `content.orgs`);
   * an org with no such file still gets a page, built from this block plus its repos.
   */
  organizations: [
    {
      slug: 'canadian-coding',
      name: 'Canadian Coding',
      description: 'Small, sturdy, source-available tools that keep working when the server doesn\'t.',
      repos: ['frznforge'],
    },
  ],

  /**
   * Gist-style notes. The defaults are what this site uses, so this block only documents them:
   * notes are read from `./content/notes` — a file directly inside it is a single-file note,
   * a sub-folder is a multi-file note. Dates come from YAML frontmatter only; `useMtime: true`
   * would fall back to filesystem mtimes and give up byte-identical rebuilds.
   */
  notes: {
    dir: './content/notes',
  },

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
