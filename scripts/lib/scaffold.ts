/**
 * `npm run frznforge -- new <dir>` — write a fresh site's hand-authored files into a directory.
 *
 * What this is *not*: a published `npm create frznforge` package. Nothing here is on a registry,
 * so there is no `npx create-frznforge` to run and no way for a scaffolded directory to pull the
 * generator in as a dependency. What this command does instead is the honest half of the same
 * job — it writes every file a site owner actually authors (config, profile, notes, orgs,
 * .gitignore, their own README) into a directory, correct on the first build, with one commented
 * example of every source type. The engine (`src/`, `astro.config.mjs`, `package.json`, …) comes
 * from a frznforge checkout; `docs/user/starting-a-site.md` spells out both halves and what the
 * npm route would need.
 *
 * Three rules the whole module is built around:
 *
 *  1. **Never clobber.** An existing file is reported and left exactly as it was, `--force` or
 *     not. `--force` only relaxes the "the directory must be empty" precondition.
 *  2. **Say everything out loud.** Every path that was written, every path that was kept, and
 *     the next steps naming the files to edit. A scaffold the user has to go spelunking through
 *     is not a scaffold.
 *  3. **Everything written must be valid.** The generated `frznforge.config.ts` parses under the
 *     real `FrznforgeConfigSchema`, and the generated `content/profile.md` frontmatter parses
 *     under the real `ProfileFrontmatter`. `tests/unit/scaffold.test.ts` asserts both against the
 *     genuine schemas rather than copies of them.
 *
 * The file list is a pure function (`scaffoldFiles`) so the tests, the dry run and the writer all
 * agree by construction, and so `--dry-run` cannot drift from what a real run would produce.
 */
import fs from 'node:fs/promises';
import path from 'node:path';

/* ------------------------------------------------------------------ file set */

/** One file the scaffold writes. `path` is relative to the target directory, POSIX-separated. */
export interface ScaffoldFile {
  path: string;
  /** Full text, LF line endings, always ending in a newline. */
  contents: string;
  /** One line for the printed tree, explaining what the file is for. */
  purpose: string;
}

const CONFIG_TS = `// frznforge site configuration.
// Read at build time (npm run ingest / astro dev / astro build) — change it, then rebuild.
//
// Full reference: docs/user/configuration.md
// Getting started: docs/user/starting-a-site.md
// Importing from a forge: docs/user/importing.md

import { defineConfig } from './src/lib/config/schema';

export default defineConfig({
  site: {
    title: 'My forge',
    // url: 'https://forge.example.com',  // shown as the host label in the sidebar; reserved for absolute links/feeds later
    // description: 'Everything I have shipped, in one place.',
  },

  owner: {
    /** Shown as the site owner's display name. */
    name: 'Your Name',
    /** Lowercase slug: letters, digits and dashes. Rendered as @handle. */
    handle: 'you',
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
   * The repositories this site is built from. Empty is valid — the site builds with an empty
   * listing — so add them one at a time and rebuild as you go.
   *
   * Every entry accepts \`slug\` (the URL segment, defaults to the repo name), \`org\` (a slug
   * from \`organizations\` below), \`releases\` ('provider' | 'tags') and \`overrides\`
   * (description, tags, links, license, template — these beat the repo's own .frznforge.json).
   *
   * Remote sources are mirror-cloned into \`ingest.cacheDir\` on the first build. API tokens are
   * read from the environment only (see docs/user/importing.md); never write one in this file.
   *
   * Uncomment one and make it yours:
   */
  repos: [
    // A git repository on this machine. Absolute, or relative to this config file. Bare repos
    // work too. This is the only source type that needs no network at all.
    // { type: 'local', path: '../my-project', slug: 'my-project' },

    // GitHub. \`host\` only for GitHub Enterprise: 'https://git.example.com/api/v3'.
    // { type: 'github', owner: 'you', repo: 'my-project' },

    // GitLab. \`project\` is the full namespaced path, subgroups included.
    // { type: 'gitlab', project: 'you/my-project' },

    // Gitea. \`host\` is the instance root; the importer appends /api/v1.
    // { type: 'gitea', host: 'https://gitea.example.com', owner: 'you', repo: 'my-project' },

    // Forgejo. Same API as Gitea; named separately so the UI can credit it correctly.
    // { type: 'forgejo', host: 'https://codeberg.org', owner: 'you', repo: 'my-project' },
  ],

  /**
   * Optional groupings with their own overview page at /orgs/<slug>/. A repo joins either by
   * being listed here or by setting \`org: '<slug>'\` on its own source entry — membership is the
   * union, so either direction alone is enough. Prose and links come from
   * \`content/orgs/<slug>.md\`; an org with no such file still gets a page.
   */
  organizations: [
    // { slug: 'my-org', name: 'My Org', description: 'What ties these together.',
    //   repos: ['my-project'] },
  ],

  /**
   * Gist-style notes read from a plain folder. A file directly inside it is a single-file note;
   * a sub-folder is a multi-file note. Dates come from YAML frontmatter.
   */
  notes: {
    dir: './content/notes',
    /**
     * Fall back to file modification times for notes with no frontmatter \`date\`. Off because
     * mtimes are not reproducible — a fresh clone stamps every file with the clone time, so two
     * builds of the same content would stop being byte-identical.
     */
    useMtime: false,
  },

  content: {
    /** One <org-slug>.md per organization. The folder may be absent. */
    orgs: './content/orgs',
  },

  ingest: {
    /** Where forge.json + blobs/ + archives/ are written. Git-ignored; regenerated every build. */
    outDir: './data',
    /** Text files larger than this are listed but their content is not stored. */
    maxBlobBytes: 512 * 1024,
    /** Cap on commits stored per repo (null = all). */
    maxCommits: null,
    /** How many repos to scan concurrently. */
    concurrency: 4,
    /** Newest N tags get browsable trees + source archives. 0 disables tag trees. */
    tagTrees: 25,
    /**
     * How many NON-default branches get a browsable file tree ('all' for every branch).
     *
     * The single biggest lever on build size: tree/blob/raw pages are emitted per browsable ref,
     * so a repo with 27 branches costs 27x its file count in pages. The default branch always
     * has a tree and never counts against this. Skipped branches still appear on the branches
     * page — they just have no file browser.
     */
    branchTrees: 10,
    /** Zip source archives (git archive) for the default branch + the tags above. */
    archives: true,
    /** Mirror clones + cached provider responses for remote sources. Git-ignored. */
    cacheDir: './.frznforge-cache',
    /** 'auto' (fetch, fall back to cache) | 'never' (offline) | 'always'. */
    fetch: 'auto',
    /** Per-repo insights page: monthly commits/contributors + a sampled code-size series. */
    insights: {
      enabled: true,
      /** Maximum monthly code-size checkpoints per repo (first and last are always included). */
      samples: 24,
      /** Byte budget for line counting at one checkpoint; past it the point reports bytes only. */
      maxBytesPerSample: 20 * 1024 * 1024,
    },
  },

  listing: {
    /** Repos per page on /repos/. */
    pageSize: 50,
  },
});
`;

const PROFILE_MD = `---
# Your profile page. Everything above the second '---' is metadata frznforge renders as
# links, pills and pinned cards; everything below it is markdown, rendered as your README.
#
# Delete what you do not want — every key here is optional. URLs must be real URLs and
# 'email' must be a real address, or the build will tell you which one is not.

bio: One line about what you build.
location: Somewhere, Earth
# workplace: Your Company
# school: Your University
# email: you@example.com
sites:
  - https://example.com
# linkedin: https://www.linkedin.com/in/you
# forges:
#   github: https://github.com/you
#   gitlab: https://gitlab.com/you
#   codeberg: https://codeberg.org/you

# Repo slugs to feature at the top of the profile, in order (max 10). These are the slugs
# from frznforge.config.ts, not the display names.
pinned: []

# Author emails that count as "you" in the contribution graph. Leave empty to count every
# commit in every ingested repo; list your addresses to count only yours.
identities: []
---

# Hi, I'm Your Name

Two or three sentences about what you make and why anyone should care. This is the first
thing a visitor reads, so it is worth more than the rest of this file put together.

## What I'm working on

- **A project** — one line on what it does.
- **Another project** — one line on what it does.

## Elsewhere

Links live in the frontmatter above, not down here — they render as pills next to your
name so they stay visible while someone scrolls.
`;

const NOTE_MD = `---
title: Welcome to your notes
description: What this folder is for, and how a file in it becomes a page.
date: 2026-01-01
tags:
  - frznforge
---

# Welcome to your notes

This folder is the gist-shaped corner of your site. Drop a file in it and it becomes a page
at \`/notes/<name>/\`; make a **folder** instead and every file inside it becomes one
multi-file note, with \`index.md\` (or the first markdown file) as the body.

Notes are not a git repository. They are read straight from disk, which is why you can edit
one and rebuild without committing anything.

## Frontmatter

| Key | What it does |
| --- | --- |
| \`title\` | Page title. Falls back to the first \`# heading\`, then to the filename. |
| \`description\` | One-line summary in the notes listing. |
| \`date\` | Sort order. \`YYYY-MM-DD\`. Without it a note has no date — see \`notes.useMtime\`. |
| \`tags\` | Filter chips on the notes listing. |

## Delete this file

It exists so the folder is not empty and so you have something to copy. Nothing depends on it.
`;

// The frontmatter has to be the very first thing in the file, so the "how to use this"
// block lives in the body as an HTML comment (markdown renders it to nothing). That way
// renaming the file is genuinely all it takes — no un-commenting, no reordering.
const ORG_EXAMPLE = `---
description: What ties these repositories together. Overrides the description in the config.
sites:
  - https://example.com
links:
  GitHub: https://github.com/my-org
  Email: mailto:hello@example.com
pinned: []
---

<!--
  An example organization page. It is inert as it stands: the orgs loader only reads *.md,
  and this file is named .md.example, so a fresh site does not ship with a stray org.

  To use it:
    1. Add the org to frznforge.config.ts:
         organizations: [
           { slug: 'my-org', name: 'My Org', repos: ['my-project'] },
         ]
    2. Rename this file to my-org.md — the filename without .md IS the slug, and that is how
       the page finds this text.
    3. Put your repo slugs in \`pinned\` above, and rebuild.

  An org with no markdown file still gets a page, built from the config entry and its repos.
  This file only adds prose, links and pinned repos on top.
-->

My Org is the name I publish under. A paragraph here about what the umbrella means and what
someone should expect to find under it.

## What ties these together

- **One principle.** A sentence.
- **Another principle.** A sentence.
`;

const GITIGNORE = `# frznforge ingest artifact — forge.json + blobs/ + archives/ (regenerated by \`npm run ingest\`)
/data/

# build output
/dist/

# remote mirror cache (ingest.cacheDir; safe to delete, re-fetched on the next build)
/.frznforge-cache/

# dependencies
node_modules/

# generated Astro types
.astro/

# environment — API tokens for remote sources live here, never in frznforge.config.ts
.env
.env.production

# OS noise
.DS_Store
`;

const README_MD = `# My forge

A [frznforge](https://github.com/Descent098/frznforge) site: a static, read-only forge built
from a pile of git repositories. No accounts, no issues, no stars — just the code, the
history and the prose, rebuilt from scratch on every deploy.

## The three commands

\`\`\`sh
npm install     # once
npm run dev     # local preview on http://localhost:4321 (uses the last ingest)
npm run build   # ingest the repos, then build the static site into dist/
\`\`\`

\`npm run build\` is \`npm run ingest\` followed by \`astro build\`. Run \`npm run ingest\` on its
own after changing which repositories you list, then \`npm run dev\` to look at the result.
Everything in \`dist/\` is plain files — upload them anywhere.

## What to edit

| File | What it controls |
| --- | --- |
| \`frznforge.config.ts\` | Site title, your name and handle, palette, **which repositories to ingest**, organizations, ingest limits. |
| \`content/profile.md\` | Your profile page: links and pinned repos in the frontmatter, your README below it. |
| \`content/notes/\` | Notes. One file = one note; one folder = one multi-file note. |
| \`content/orgs/\` | One \`<org-slug>.md\` per organization listed in the config. |

Start with the \`repos: [ … ]\` array in \`frznforge.config.ts\` — until something is in it, the
site builds correctly but lists nothing.

## Where the engine lives

This directory holds the files **you** author. The generator itself — \`src/\`,
\`astro.config.mjs\`, \`package.json\`, \`scripts/\` — comes from a frznforge checkout and belongs
in this same directory. See \`docs/user/starting-a-site.md\` in that checkout if these files
are not here yet.
`;

/**
 * Every file a fresh site starts with, in the order they are written and printed.
 *
 * Pure: no filesystem, no clock, no environment. The dry run prints exactly this list and a
 * real run writes exactly this list, so the two cannot disagree.
 */
export function scaffoldFiles(): ScaffoldFile[] {
  return [
    { path: 'frznforge.config.ts', contents: CONFIG_TS, purpose: 'site config — start here' },
    { path: 'content/profile.md', contents: PROFILE_MD, purpose: 'your profile page' },
    { path: 'content/notes/welcome.md', contents: NOTE_MD, purpose: 'an example note (safe to delete)' },
    { path: 'content/orgs/example-org.md.example', contents: ORG_EXAMPLE, purpose: 'an example org page (inert until renamed)' },
    { path: '.gitignore', contents: GITIGNORE, purpose: 'ignores data/, dist/, the cache, node_modules' },
    { path: 'README.md', contents: README_MD, purpose: 'your site’s README, not frznforge’s' },
  ];
}

/* ------------------------------------------------------------------ writing */

export interface ScaffoldOptions {
  /** Target directory. Resolved by the caller against its own cwd. */
  dir: string;
  /** Allow a directory that already has files in it. Never allows overwriting one. */
  force?: boolean;
  /** Print what would happen; touch nothing. */
  dryRun?: boolean;
}

export interface ScaffoldResult {
  /** Absolute target directory. */
  dir: string;
  /** Relative paths written (empty on a dry run). */
  written: string[];
  /** Relative paths that already existed and were left alone. */
  kept: string[];
  /** Relative paths a real run would write — the same on a dry run and a real one. */
  planned: string[];
  dryRun: boolean;
  /** True when the target directory did not exist and was (or would be) created. */
  createdDir: boolean;
  /**
   * True when the engine (`src/lib/config/schema.ts`) is already sitting in the target, i.e.
   * the user scaffolded into a frznforge checkout. "Copy the engine in" is then not a next
   * step, and printing it anyway is how a good instruction list teaches people to skim.
   */
  engineReady: boolean;
}

/** A refusal the CLI turns into `error: …` and exit 1, with no stack trace. */
export class ScaffoldError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'ScaffoldError';
  }
}

/**
 * Is this directory empty enough to scaffold into?
 *
 * A lone `.git` does not count: `git init my-site && cd my-site` before scaffolding is a normal
 * way to start, and refusing it would send people to `--force` for no reason. Anything else —
 * including a stray dotfile — makes the directory non-empty, because the whole point of the
 * check is "I am about to write into a directory you may care about".
 */
async function isEmptyEnough(dir: string): Promise<boolean> {
  const entries = await fs.readdir(dir);
  return entries.every((name) => name === '.git');
}

/**
 * Write the scaffold into `options.dir`.
 *
 * Refuses (throws `ScaffoldError`) when the target exists, is non-empty and `--force` was not
 * given, or when it exists and is not a directory. Existing files are *never* overwritten, with
 * or without `--force`: they come back in `kept` so the caller can print them.
 */
export async function scaffold(options: ScaffoldOptions): Promise<ScaffoldResult> {
  const dir = path.resolve(options.dir);
  const dryRun = options.dryRun === true;

  let createdDir = false;
  const stat = await fs.stat(dir).catch(() => null);
  if (stat === null) {
    createdDir = true;
  } else if (!stat.isDirectory()) {
    throw new ScaffoldError(`${dir} exists and is not a directory.`);
  } else if (!(await isEmptyEnough(dir)) && options.force !== true) {
    throw new ScaffoldError(
      `${dir} is not empty. Re-run with --force to add the missing files to it ` +
        '(existing files are never overwritten), or pick an empty directory.',
    );
  }

  const files = scaffoldFiles();
  const written: string[] = [];
  const kept: string[] = [];
  const planned: string[] = [];

  for (const file of files) {
    const target = path.join(dir, ...file.path.split('/'));
    // stat rather than a plain write with 'wx': the caller has to be able to *say* which files
    // it left alone, and on a dry run there is no write to learn it from.
    const exists = createdDir ? false : await fs.stat(target).then(() => true, () => false);
    if (exists) {
      kept.push(file.path);
      continue;
    }
    planned.push(file.path);
    if (dryRun) continue;
    await fs.mkdir(path.dirname(target), { recursive: true });
    // 'wx' closes the gap between the stat above and the write: if something else created the
    // file in between, we keep our promise never to clobber rather than winning the race.
    try {
      await fs.writeFile(target, file.contents, { encoding: 'utf8', flag: 'wx' });
      written.push(file.path);
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== 'EEXIST') throw error;
      kept.push(file.path);
      planned.pop();
    }
  }

  const engineReady = createdDir
    ? false
    : await fs.stat(path.join(dir, 'src', 'lib', 'config', 'schema.ts')).then(() => true, () => false);

  return { dir, written, kept, planned, dryRun, createdDir, engineReady };
}

/* ------------------------------------------------------------------ reporting */

/**
 * The block printed after a successful run: the exact files to edit, in the order they matter,
 * and the commands that turn them into a site. Named files only — "configure your site" is not
 * a next step, "open frznforge.config.ts and fill in repos: [ … ]" is.
 */
export function nextSteps(result: ScaffoldResult, cwd: string = process.cwd()): string[] {
  const rel = path.relative(cwd, result.dir) || '.';
  const where = rel.startsWith('..') ? result.dir : rel;
  const steps: string[][] = [];
  if (!result.engineReady) {
    steps.push([
      `Put the frznforge engine in ${where}: copy src/, scripts/, public/, astro.config.mjs,`,
      'svelte.config.js, tsconfig.json and package.json from your frznforge checkout, then run',
      'npm install there. (docs/user/starting-a-site.md — there is no npm package to install yet.)',
    ]);
  }
  steps.push([
    `Edit ${path.join(where, 'frznforge.config.ts')}`,
    '- site.title, owner.name, owner.handle',
    '- repos: [ … ]  — uncomment one of the five examples and point it at a repository',
  ]);
  steps.push([`Edit ${path.join(where, 'content', 'profile.md')} — bio, sites, pinned, and the body under the frontmatter`]);
  steps.push([`Replace ${path.join(where, 'content', 'notes', 'welcome.md')} with a note of your own, or delete it`]);
  steps.push(['npm run build   (then serve dist/ anywhere)']);

  const out = ['Next steps'];
  steps.forEach(([head, ...rest], i) => {
    out.push(`  ${i + 1}. ${head}`);
    for (const line of rest) out.push(`     ${line}`);
  });
  return out;
}

/**
 * Run the whole command and print it. Returns a process exit code; every refusal is a
 * `ScaffoldError` the caller reports without a stack trace.
 */
export async function runScaffold(
  options: ScaffoldOptions & { cwd?: string },
  log: (line: string) => void,
): Promise<ScaffoldResult> {
  const result = await scaffold(options);

  if (result.dryRun) {
    log(`Dry run — nothing was written to ${result.dir}${result.createdDir ? ' (would be created)' : ''}.`);
    log('');
    log('Would create:');
    const byPath = new Map(scaffoldFiles().map((f) => [f.path, f]));
    for (const p of result.planned) log(`  + ${p.padEnd(38)} ${byPath.get(p)!.purpose}`);
    for (const p of result.kept) log(`  = ${p.padEnd(38)} already there, would be left alone`);
    log('');
    log('Re-run without --dry-run to write these files.');
    return result;
  }

  log(`Scaffolded a frznforge site in ${result.dir}${result.createdDir ? ' (created)' : ''}.`);
  log('');
  const byPath = new Map(scaffoldFiles().map((f) => [f.path, f]));
  for (const p of result.written) log(`  + ${p.padEnd(38)} ${byPath.get(p)!.purpose}`);
  for (const p of result.kept) log(`  = ${p.padEnd(38)} already existed — left untouched`);
  if (result.written.length === 0) log('  (nothing to do — every file was already there)');
  log('');
  for (const line of nextSteps(result, options.cwd)) log(line);
  return result;
}
