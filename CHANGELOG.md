# 0.3.0 (2026-08-31)

## Features

* **Licenses link to their canonical page.** A recognised SPDX id on the repo header badge and in the About panel now links to choosealicense.com, or to creativecommons.org for Creative Commons licenses. Unrecognised ids (and the `Custom` placeholder) stay plain text rather than guessing a URL.
* **Hosted sites are linked from the repo they come from.** A repo published through `hosting.sites` now shows a "Hosted site" row in its About panel linking to the served site. Previously the hosting binding existed in the artifact but nothing in the UI pointed at it.

* **Opt-in refetch controls.** `ingest.reuse.skipUnchanged` runs one `git ls-remote` per remote repo and skips the fetch when the mirror already holds every ref the remote does; any difference, or any failure of the probe, falls through to a normal fetch. `ingest.reuse.cooldownSeconds` skips a repo whose last *fully successful* fetch (both the git and metadata halves) was within the cooldown, reporting it during the build. Both are off by default, and both are byte-neutral: a skipped run emits the identical artifact.

* **Rate limits back off per origin.** A 429 — or GitHub's 403-with-no-quota-left — is now retried with exponential backoff keyed to the host, so every repo being ingested in parallel from one forge waits behind a single timer while other forges are unaffected. The provider's `Retry-After` is honoured; a limit longer than a minute blocks that host for the stated period so the remaining repos fall back to cached metadata immediately instead of each burning a retry ladder. Repos with no cached metadata are fetched first, so a limited run spends its budget where there is nothing to fall back on. `ingest.failOnDegraded` (default off) makes such a run exit non-zero instead of quietly publishing stale metadata.

* **Pictures for the owner, organizations and contributors.** `owner.avatar`, `organizations[].avatar` and a new top-level `contributors[]` block let you attach a real name, picture, blurb and link to the people and groups on the site; anywhere without one keeps the initials block. Images are paths inside `public/` rather than URLs, so the published pages still load nothing from a third party. A `contributors[]` entry listing several `emails` merges them into one person, since one contributor committing from two machines is one contributor. Artifact schema v8 — additive, so the only migration is re-running the build (`npm run build`), and an artifact left over from an older version now says exactly that instead of failing with a raw schema error.

* **The init wizard edits people, pictures and the 0.3.0 settings.** `frznforge init --web` gains a contributors list, an Edit control on every list row (organizations, contributors, hosted sites and sources can now be corrected in place instead of removed and re-added), a file picker for every avatar field, and the new ingest settings. Uploads write into `public/images/` under a name the *server* chooses from the image's own bytes — the browser never names a file on disk.

* **`npm run dev` serves the last build instead of pretending to be live.** It now explains that nothing is rebuilt, checks that a build exists (and says what to run when it doesn't), then hands off to `astro preview`. The raw Astro dev server is still available as `npm run astro dev`.

* **`--backfill-metadata` fills in repos the rate limit skipped.** `npm run ingest -- --backfill-metadata` asks the provider only about repos that have no cached metadata, and touches git for nothing. On a large account the commits always arrive (cloning is unmetered) while metadata is metered, so an ordinary run spends its budget re-requesting answers it already has and leaves the same tail of repos blank every time. Measured on a 72-repo account: 13 blank repos filled in 30s using 13 requests, with the other 59 replayed from cache and no git traffic at all. The artifact is the same one a full run would write — this is a cheaper route to it, not a partial one.

## Bug Fixes

* **The wizard's Done button discarded unsaved edits.** Pressing Done ended the session without saving a settings field or profile body that had been edited but not explicitly saved. It now saves them first, and a value the config schema rejects keeps the wizard open with the error rather than exiting having thrown the edit away.

* **The clone popup was squeezed to the width of its button.** Its `max-width: 100%` resolved against the shrink-wrapped `<details>` that contains it, clamping the panel to the "Clone" button and pushing its contents outside. The panel now sizes against the viewport, and is 400px so a typical GitHub clone URL fits without truncation. Below 900px — where the toolbar drops its `margin-left: auto` and the button is no longer at the right edge — the panel anchors to the toolbar instead, so it can no longer hang off the side of a phone screen.

## Other

* **Per-half fetch status is recorded.** The ingest run log (`<cacheDir>/last-run.json`) is now version 2: beside the existing timestamp and freshness flag, each remote source records whether the *git mirror* fetch and the *provider metadata* fetch each succeeded, plus the mirror's refs at the end of the run. The halves are reported by the fetch code rather than inferred from warning codes, because `remote-cache-stale` is raised for both a stale mirror and stale metadata. A version 1 log on disk is discarded and rebuilt, which costs one un-skipped fetch cycle.

* **Insights lead with lines of code.** The code-size tile now shows the line count as the headline number and the approximate byte size beneath it, with the label following suit. A checkpoint that went over the ingest read budget cannot count lines, so it keeps bytes as the headline and says why.

* **Documentation sweep.** Audited every doc against the code for the release: the copy-the-engine recipe in `starting-a-site.md` (and the scaffolded site's own README) named `astro.config.mjs`, which has been `astro.config.ts` since 0.2.0 — following it produced a site that built zero pages. Also corrected two contradictory rebuild figures, added the missing v8 entry to the data-model version history, and documented the run log's v2 fields and the rate-limit behaviour where a rate-limited reader actually lands.

* **Dependency updates.** Updated Astro (7.2.4 → 7.2.9), Svelte (5.56.10 → 5.57.0), marked (18.0.10 → 18.0.11), tsx (4.23.12 → 4.23.13), and `@types/node` (26.2.0 → 26.4.0). TypeScript 7.0.2 is available but deliberately deferred: `@astrojs/check` and `@astrojs/svelte` both declare a `typescript` peer range of `^5.0.0 || ^6.0.0`, so the project stays pinned on 6.0.3 until the Astro toolchain supports 7.

# 0.2.0 (2026-08-30)

## Features

* **Configurable recency accent.** Added `theme.heat` to control the fire→ice recency thresholds while keeping the palette colours fixed and WCAG AA compliant.
* **Recent-history ingest limits.** Added `ingest.maxCommitAgeDays` to limit imported history by age while preserving branch heads and deterministic builds.
* **Ingest caching and reuse.** Added `ingest.reuse` to skip unnecessary fetches and repo scans without changing artifact output. Includes `--no-cache` for forced fresh ingest.
* **Mermaid diagrams.** Markdown `mermaid` fences now render as client-side diagrams using a bundled, sanitized Mermaid runtime.
* **Sub-path deployments.** Added `site.base` support for deploying the site under paths such as `/mysite`.
* **Static repo hosting.** Added `hosting.sites` for serving a repository's static site at `/<slug>/`, with artifact schema v7.
* **Expanded init wizard.** `frznforge init --web` can now edit the full configuration, including settings, organizations, hosted sites, ingest options, and owner profile.

## Bug Fixes

* **Preserved file history with ingest limits.** File tables now retain last-commit information even when commit history is capped or age-limited, using artifact schema v6.

* **Wizard could not save the page size.** The init wizard rendered a "Repos per page" field that the server's allow-list rejected, failing the whole settings save with a 400. `listing.pageSize` is now editable, and a test cross-checks every field the page offers against the allow-list.

## Other

* **New logo.** Replaced the old logo with a lightweight vector anvil design and reduced favicon size substantially.

* **Build performance.** Pages now build concurrently in pairs, improving self-build time by about 8%. Other performance optimizations were measured and rejected where they provided insufficient benefit.

* **Much faster rebuilds.** Syntax highlighting measured as 84% of the render, so its output is now memoized across builds in `<ingest.cacheDir>/highlight/`. A no-change `npm run build` drops from 23.1s to 7.9s (−66%). Highlighting is a pure function, so a cached result is byte-identical to a fresh one — no page is skipped, and the key covers Shiki's version and themes. Verified by building the site with and without the cache and hashing all 1,153 files: none of the 423 highlighted pages differed. Governed by `ingest.reuse.enabled`; `FRZNFORGE_NO_HL_CACHE=1` bypasses it.

# 0.1.0 (2026-08-24)

First release. frznforge turns git repositories into a **static, read-only forge site** with repository browsing, history, branches, tags, releases, insights, notes, organizations, and profiles.

No server, database, accounts, issues, pull requests, or stars. Artifact schema v5.

## Features

* **Static forge generation.** `npm run ingest` creates a deterministic JSON artifact and content-addressed blob store; Astro turns it into static HTML.
* **Configuration.** `frznforge.config.ts` defines site, owner, theme, repository sources, and ingest settings, with per-repo `.frznforge.json` metadata support.
* **Repository ingest.** Imports repository metadata, branches, tags, commits, file trees, contributors, languages, READMEs, licenses, source archives, and browsable file content.
* **Hearth-designed site.** Includes profiles, repository listings, search/filtering, themes, repository overviews, rendered READMEs, and responsive static pages.
* **Repository browsing.** Browse files, branches, tags, history, commits, releases, raw files, downloads, and source archives with syntax highlighting.
* **Profile and command palette.** Includes contribution activity, heat maps, recent activity, fuzzy search, and keyboard-accessible navigation.
* **Forge importers.** Supports GitHub, GitLab, Gitea, and Forgejo with cached mirrors, releases, offline builds, and safe rendering of imported Markdown.
* **Interactive init wizard.** `frznforge init` helps select repositories and providers, with CLI and local web modes.
* **Site scaffolding.** `frznforge new <dir>` creates a complete starter site without overwriting existing files.
* **Notes.** Added gist-style Markdown and source notes with search, raw URLs, highlighting, and palette integration.
* **Organizations.** Group repositories into organizations with profiles, KPIs, pinned repositories, and listings.
* **Insights.** Added per-repository monthly charts for commits, contributors, and code size.
* **Build-size controls.** `ingest.branchTrees` limits non-default branch trees to control page count and build size.
* **Accessibility and responsive design.** Added comprehensive WCAG testing, keyboard navigation, responsive layouts, accessible headings, focus management, and theme support.
* **User documentation.** Added quick-start, setup, configuration, importing, deployment, and migration guides.

## Bug Fixes

* **Encoded repository paths.** File routes now safely handle URL-special characters and flag paths that cannot be represented by static hosts.
* **Improved colour contrast.** Light and dark themes now meet WCAG AA requirements across surfaces and syntax highlighting.
* **Accessible control labels.** Fixed accessible names for sidebar controls and the commit copy button.
* **Fixed heading structure.** Corrected duplicate and skipped headings across repository pages and rendered Markdown.
* **Fixed line counting.** Code views now correctly handle files with and without trailing newlines.
* **Fixed multi-file note anchors.** Prevented duplicate line IDs across files.
* **Restored command-palette focus.** Focus now returns to the element active before the palette opened.
* **Keyboard-scrollable regions.** Code blocks and contribution graphs can now receive keyboard focus and scrolling.
* **Responsive commit bar.** Fixed horizontal overflow on narrow screens.
* **Visible focus rings.** Added focus styling to `summary` elements.

## Other

* **Tests.** Added extensive unit, end-to-end, accessibility, sync, and remote-provider test coverage without network access in the main suite.
* **Developer docs.** Added documentation for the data model, performance, release process, and development plans.
* **Project metadata.** Added MIT licensing and repository metadata.
* **Design history.** Documented the four early visual explorations and the selection of Hearth as the final design.
