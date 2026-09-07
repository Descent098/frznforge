package scaffold

// The six files a fresh site starts with, as literals.
//
// They are constants rather than templates because every one of them is meant to be read and
// edited by a person on day one: a template with holes punched in it drifts from the real config
// the moment a key is added, and the tests below assert these bytes against the REAL loader and
// the REAL frontmatter parser, which only works if the bytes are fixed.

// configJSONC is the generated frznforge.config.jsonc.
//
// 0.4.0's config is JSONC, not TypeScript: the engine is a Go binary and can no longer execute
// its own configuration. What survives the change is the thing that mattered — the comments.
// They are most of this file, and they are the only documentation a site owner has open while
// they edit it.
const configJSONC = `// frznforge site configuration.
// Read when the site is built (frznforge ingest / frznforge build) — change it, then rebuild.
//
// The format is JSON with comments: ` + "`//`" + ` and ` + "`/* … */`" + ` are fine, and so is a trailing
// comma before a ` + "`}`" + ` or a ` + "`]`" + `. Nothing else JSON refuses is allowed — no unquoted keys,
// no single quotes.
//
// Full reference:    docs/user/configuration.md
// Getting started:   docs/user/starting-a-site.md
// Importing a forge: docs/user/importing.md

{
  "site": {
    "title": "My forge",
    // "url": "https://forge.example.com",   // the host label in the sidebar; absolute links later
    // "description": "Everything I have shipped, in one place.",
  },

  "owner": {
    /** Shown as the site owner's display name. */
    "name": "Your Name",
    /** Lowercase slug: letters, digits and dashes. Rendered as @handle. */
    "handle": "you",
    /** Markdown file rendered on the profile page; frontmatter carries links + pinned repos. */
    "profile": "./content/profile.md",
  },

  "theme": {
    /**
     * Colour palette. Layout and components are identical; only colours change.
     *  - "hearth" — warm: off-white / ember-tinted charcoal canvas, ember as the action colour.
     *  - "frost"  — cool: slate / blue-tinted navy canvas, ice as the action colour.
     */
    "palette": "hearth",
  },

  /**
   * The repositories this site is built from. Empty is valid — the site builds with an empty
   * listing — so add them one at a time and rebuild as you go.
   *
   * Every entry accepts "slug" (the URL segment, defaults to the repo name), "org" (a slug from
   * "organizations" below), "releases" ("provider" or "tags") and "overrides" (description,
   * tags, links, license, template — these beat the repo's own .frznforge.json).
   *
   * Remote sources are mirror-cloned into "ingest.cacheDir" on the first build. API tokens are
   * read from the environment only (see docs/user/importing.md); never write one in this file.
   *
   * ` + "`frznforge init`" + ` fills this array in for you from a provider account.
   *
   * Uncomment one and make it yours:
   */
  "repos": [
    // A git repository on this machine. Absolute, or relative to this config file. Bare repos
    // work too. This is the only source type that needs no network at all.
    // { "type": "local", "path": "../my-project", "slug": "my-project" },

    // GitHub. "host" only for GitHub Enterprise: "https://git.example.com/api/v3".
    // { "type": "github", "owner": "you", "repo": "my-project" },

    // GitLab. "project" is the full namespaced path, subgroups included.
    // { "type": "gitlab", "project": "you/my-project" },

    // Gitea. "host" is the instance root; the importer appends /api/v1.
    // { "type": "gitea", "host": "https://gitea.example.com", "owner": "you", "repo": "my-project" },

    // Forgejo. Same API as Gitea; named separately so the UI can credit it correctly.
    // { "type": "forgejo", "host": "https://codeberg.org", "owner": "you", "repo": "my-project" },
  ],

  /**
   * Optional groupings with their own overview page at /orgs/<slug>/. A repo joins either by
   * being listed here or by setting "org": "<slug>" on its own source entry — membership is the
   * union, so either direction alone is enough. Prose and links come from
   * "content/orgs/<slug>.md"; an org with no such file still gets a page.
   */
  "organizations": [
    // {
    //   "slug": "my-org",
    //   "name": "My Org",
    //   "description": "What ties these together.",
    //   "repos": ["my-project"],
    // },
  ],

  /**
   * Gist-style notes read from a plain folder. A file directly inside it is a single-file note;
   * a sub-folder is a multi-file note. Dates come from YAML frontmatter.
   */
  "notes": {
    "dir": "./content/notes",
    /**
     * Fall back to file modification times for notes with no frontmatter "date". Off because
     * mtimes are not reproducible — a fresh clone stamps every file with the clone time, so two
     * builds of the same content would stop being byte-identical.
     */
    "useMtime": false,
  },

  "content": {
    /** One <org-slug>.md per organization. The folder may be absent. */
    "orgs": "./content/orgs",
  },

  "ingest": {
    /** Where forge.json + blobs/ + archives/ are written. Git-ignored; regenerated every build. */
    "outDir": "./data",
    /** Text files larger than this are listed but their content is not stored. */
    "maxBlobBytes": 524288, // 512 * 1024
    /** Cap on commits stored per repo (null = all). */
    "maxCommits": null,
    /** How many repos to scan concurrently. */
    "concurrency": 4,
    /** Newest N tags get browsable trees + source archives. 0 disables tag trees. */
    "tagTrees": 25,
    /**
     * How many NON-default branches get a browsable file tree ("all" for every branch).
     *
     * The single biggest lever on build size: tree/blob/raw pages are emitted per browsable ref,
     * so a repo with 27 branches costs 27x its file count in pages. The default branch always
     * has a tree and never counts against this. Skipped branches still appear on the branches
     * page — they just have no file browser.
     */
    "branchTrees": 10,
    /** Zip source archives (git archive) for the default branch + the tags above. */
    "archives": true,
    /** Mirror clones + cached provider responses for remote sources. Git-ignored. */
    "cacheDir": "./.frznforge-cache",
    /** "auto" (fetch, fall back to cache) | "never" (offline) | "always". */
    "fetch": "auto",
    /** Per-repo insights page: monthly commits/contributors + a sampled code-size series. */
    "insights": {
      "enabled": true,
      /** Maximum monthly code-size checkpoints per repo (first and last are always included). */
      "samples": 24,
      /** Byte budget for line counting at one checkpoint; past it the point reports bytes only. */
      "maxBytesPerSample": 20971520, // 20 * 1024 * 1024
    },
  },

  "listing": {
    /** Repos per page on /repos/. */
    "pageSize": 50,
  },
}
`

// profileMarkdown is content/profile.md: the frontmatter the profile page reads for its links
// and pinned cards, and a body the owner is meant to rewrite entirely.
const profileMarkdown = `---
# Your profile page. Everything above the second '---' is metadata frznforge renders as
# links, pills and pinned cards; everything below it is markdown, rendered as your README.
#
# Delete what you do not want — every key here is optional.

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
# from frznforge.config.jsonc, not the display names.
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
`

// noteMarkdown is content/notes/welcome.md: the example note, which doubles as the
// documentation for the notes folder.
const noteMarkdown = "---\n" +
	"title: Welcome to your notes\n" +
	"description: What this folder is for, and how a file in it becomes a page.\n" +
	"date: 2026-01-01\n" +
	"tags:\n" +
	"  - frznforge\n" +
	"---\n" +
	"\n" +
	"# Welcome to your notes\n" +
	"\n" +
	"This folder is the gist-shaped corner of your site. Drop a file in it and it becomes a page\n" +
	"at `/notes/<name>/`; make a **folder** instead and every file inside it becomes one\n" +
	"multi-file note, with `index.md` (or the first markdown file) as the body.\n" +
	"\n" +
	"Notes are not a git repository. They are read straight from disk, which is why you can edit\n" +
	"one and rebuild without committing anything.\n" +
	"\n" +
	"## Frontmatter\n" +
	"\n" +
	"| Key | What it does |\n" +
	"| --- | --- |\n" +
	"| `title` | Page title. Falls back to the first `# heading`, then to the filename. |\n" +
	"| `description` | One-line summary in the notes listing. |\n" +
	"| `date` | Sort order. `YYYY-MM-DD`. Without it a note has no date — see `notes.useMtime`. |\n" +
	"| `tags` | Filter chips on the notes listing. |\n" +
	"\n" +
	"## Delete this file\n" +
	"\n" +
	"It exists so the folder is not empty and so you have something to copy. Nothing depends on it.\n"

// orgExample is content/orgs/example-org.md.example.
//
// The frontmatter has to be the very first thing in the file, so the "how to use this" block
// lives in the body as an HTML comment (markdown renders it to nothing). That way renaming the
// file is genuinely all it takes — no un-commenting, no reordering.
const orgExample = "---\n" +
	"description: What ties these repositories together. Overrides the description in the config.\n" +
	"sites:\n" +
	"  - https://example.com\n" +
	"links:\n" +
	"  GitHub: https://github.com/my-org\n" +
	"  Email: mailto:hello@example.com\n" +
	"pinned: []\n" +
	"---\n" +
	"\n" +
	"<!--\n" +
	"  An example organization page. It is inert as it stands: the orgs loader only reads *.md,\n" +
	"  and this file is named .md.example, so a fresh site does not ship with a stray org.\n" +
	"\n" +
	"  To use it:\n" +
	"    1. Add the org to frznforge.config.jsonc:\n" +
	"         \"organizations\": [\n" +
	"           { \"slug\": \"my-org\", \"name\": \"My Org\", \"repos\": [\"my-project\"] },\n" +
	"         ]\n" +
	"    2. Rename this file to my-org.md — the filename without .md IS the slug, and that is how\n" +
	"       the page finds this text.\n" +
	"    3. Put your repo slugs in `pinned` above, and rebuild.\n" +
	"\n" +
	"  An org with no markdown file still gets a page, built from the config entry and its repos.\n" +
	"  This file only adds prose, links and pinned repos on top.\n" +
	"-->\n" +
	"\n" +
	"My Org is the name I publish under. A paragraph here about what the umbrella means and what\n" +
	"someone should expect to find under it.\n" +
	"\n" +
	"## What ties these together\n" +
	"\n" +
	"- **One principle.** A sentence.\n" +
	"- **Another principle.** A sentence.\n"

// gitignoreFile ignores everything the build regenerates.
//
// It names no package manager: 0.4.0's engine is one binary, so a site directory holds content,
// web/ and nothing that needs installing.
const gitignore = "# frznforge ingest artifact — forge.json + blobs/ + archives/ (`frznforge ingest` rewrites it)\n" +
	"/data/\n" +
	"\n" +
	"# build output\n" +
	"/dist/\n" +
	"\n" +
	"# remote mirror cache (ingest.cacheDir; safe to delete, re-fetched on the next build)\n" +
	"/.frznforge-cache/\n" +
	"\n" +
	"# the engine binary, if you build it in here rather than putting it on your PATH\n" +
	"/frznforge\n" +
	"/frznforge.exe\n" +
	"\n" +
	"# environment — API tokens for remote sources live here, never in frznforge.config.jsonc\n" +
	".env\n" +
	".env.production\n" +
	"\n" +
	"# OS noise\n" +
	".DS_Store\n"

// readmeMarkdown is the site owner's README — theirs, not frznforge's. It carries the three
// commands and the table of files they edit, because that is what someone returning to this
// directory in six months needs.
const readmeMarkdown = "# My forge\n" +
	"\n" +
	"A [frznforge](https://github.com/Descent098/frznforge) site: a static, read-only forge built\n" +
	"from a pile of git repositories. No accounts, no issues, no stars — just the code, the\n" +
	"history and the prose, rebuilt from scratch on every deploy.\n" +
	"\n" +
	"## The three commands\n" +
	"\n" +
	"```sh\n" +
	"frznforge ingest   # read the repositories → data/forge.json + data/blobs/ + data/archives/\n" +
	"frznforge build    # render that artifact into dist/\n" +
	"frznforge dev      # serve dist/ on http://localhost:4321 — the last build, nothing rebuilt\n" +
	"```\n" +
	"\n" +
	"`build` renders the artifact `ingest` wrote: it reads no git and no network, so after adding\n" +
	"a repository run `ingest` again first. `dev` rebuilds nothing either — it serves what `build`\n" +
	"produced. Everything in `dist/` is plain files; upload the folder anywhere.\n" +
	"\n" +
	"## What to edit\n" +
	"\n" +
	"| File | What it controls |\n" +
	"| --- | --- |\n" +
	"| `frznforge.config.jsonc` | Site title, your name and handle, palette, **which repositories to ingest**, organizations, ingest limits. |\n" +
	"| `content/profile.md` | Your profile page: links and pinned repos in the frontmatter, your README below it. |\n" +
	"| `content/notes/` | Notes. One file = one note; one folder = one multi-file note. |\n" +
	"| `content/orgs/` | One `<org-slug>.md` per organization. |\n" +
	"\n" +
	"Start with the `\"repos\": [ … ]` array in `frznforge.config.jsonc` — until something is in it,\n" +
	"the site builds correctly but lists nothing. `frznforge init` can fill it in for you from a\n" +
	"GitHub, GitLab, Gitea or Forgejo account.\n" +
	"\n" +
	"## Where the engine lives\n" +
	"\n" +
	"This directory holds the files **you** author. The engine is the `frznforge` binary — put it\n" +
	"on your PATH — plus one directory that lives here beside your content: `web/`, the\n" +
	"stylesheets and browser scripts, copied into `dist/` verbatim. Both come from a frznforge\n" +
	"checkout; see `docs/user/starting-a-site.md` there if `web/` is not here yet.\n"
