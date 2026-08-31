# 0.2.0 (unreleased)

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

## Other

* **New logo.** Replaced the old logo with a lightweight vector anvil design and reduced favicon size substantially.

* **Build performance.** Pages now build concurrently in pairs, improving self-build time by about 8%. Other performance optimizations were measured and rejected where they provided insufficient benefit.

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
