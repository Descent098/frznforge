# 0.4.0 (unreleased)

## Features

* **Every command is the binary now, including the browser wizard.** `frznforge build | ingest | dev | init | new | verify | config migrate` replace the `npm run` scripts, with the flag behaviour `scripts/build.ts` established: `--no-ingest` renders the artifact already on disk and refuses when there is none, and the ingest flags pass through. `init --web` is the same 1,791-line wizard page as before — it is already plain HTML and vanilla JavaScript, so it crossed over as an embedded asset rather than being rewritten — served by Go handlers over JSONC splice editors that keep every byte outside an edited field identical. `frznforge dev` replaces both `astro preview` and the e2e suite's own server, so the command you run and the command the tests run are finally the same code.

  *Migration:* `npm run <x>` becomes `frznforge <x>`. Node is no longer needed to build a site — Go and git are.

* **Every run writes down what it did, and `frzndebugger` reads it back.** `<ingest.outDir>/frznforge.log` records the last run — every subprocess and every fetch logged *before* it starts as well as after it finishes, every lock and semaphore boundary, flushed per record so a killed process keeps its tail. `<ingest.outDir>/frznforge-timings.jsonl` records what each step cost, appended across runs so today can be compared with yesterday, as JSON Lines so an interrupted file still parses; steps nest, so one repository's fetch and render sit under that repository and that repository sits under the run, and the package computes count/total/mean/best/worst so nothing downstream can disagree about what "worst" means. Secrets are removed at the sink rather than at the call sites, so no future log line can leak by forgetting.

  `frzndebugger` is a second binary — the generator has no business carrying a debugger — and opens a terminal explorer over both files, `--web` for a browser and `--plain` for a script. It leads with **steps that started and never finished**, because that is the answer when a run stops, and it says how long they had been running by reading the log rather than the last step that managed to complete. Neither binary gained a dependency: the project still has two, and the TUI is escape codes and the web view is one embedded page.

* **`--log` says what a run is doing.** Every command takes `--log=<error|warn|info|debug>` (or a bare `--log` for debug, or `FRZNFORGE_LOG` to avoid retyping), writing structured records to stderr while progress stays on stdout — so `frznforge build --log=debug 2> build.log` is a complete answer to "what happened". Every git call and every HTTP request is recorded *before* it starts as well as after it finishes, which is the part that matters: a command that never returns leaves a start record with no matching finish, naming the exact invocation that stalled. It is off by default and costs one comparison per call site when off. This exists because a build hung for an hour and said nothing at all; the bug above was found with it in about a minute.

* **A post-processing hook, for the tooling frznforge refuses to be.** A `postprocess` block in the config (or `--postprocess <cmd>`, or `FRZNFORGE_POSTPROCESS`) runs one command over the finished output with the directory in `$FRZNFORGE_DIST_DIR`. Nothing runs by default, and frznforge itself still never minifies, bundles or hashes: the UI is no-build from end to end, and this is the single seam where your own tooling gets to touch the result. It fires only after a build that succeeded — handing a half-written `dist/` to a minifier publishes a site that is neither the old one nor the new one — and a command that exits non-zero fails the build with its output shown.

## Bug Fixes

* **File tables were ordered by the build machine's locale data.** `FileTable.astro` sorted names with `localeCompare(name, 'en', { sensitivity: 'base' })` — the exact thing `src/lib/routes.ts` warns against in writing, because ICU data differs between machines and two builds of the same artifact could then emit different HTML. It also read oddly: `config_test.go` sorted before `config.go`. The Go renderer orders by code point everywhere, which is stable, matches git's own tree order, and puts `config.go` first.

* **Two remote repositories could share one mirror, and each be published with the other's git content.** The cache directory name is derived from the source's identity, sanitised so it is a legal filename — case folded, everything outside `[a-z0-9._-]` mapped to `-`. That is lossy, so the name alone cannot identify a source: a GitLab subgroup `group/sub/proj` and a project called `group-sub/proj` produce the same string, as do two Gitea repositories differing only in case. The 0.3.0 engine appended a digest of the exact identity for precisely this reason; the Go port kept the sanitising, dropped the digest, and replaced it with a comment asserting that collisions could not happen. The digest is back.

  *Migration:* mirror directories are renamed by this, so the first ingest after upgrading re-clones each remote repository once. Nothing else changes, and the old directories are safe to delete.

* **A build of more repositories than you have cores deadlocked and never returned.** Rendering shares one worker semaphore across two nesting levels — repositories on the outside, each repository's blob and raw pages inside — and every unit took a slot and then waited for its children to take slots of their own. Once the outer level alone could fill the semaphore that is a deadlock: on a 32-core machine a 73-repository site parked 31 repositories in `wg.Wait()` and thousands of page goroutines in `chan send`, with no error and no output. Whoever calls into the pool now runs units itself rather than only waiting, and helpers never block acquiring a slot, so progress no longer depends on the semaphore at any nesting depth — and goroutines follow the worker count instead of the page count, which the old shape had at 14,000 before a single page rendered.

* **`frznforge dev` erased the log of the build you were about to debug.** The run log is truncated per run and the preview server wrote the same file, so the documented build-then-preview loop destroyed its own evidence. `dev` writes `frznforge-dev.log` now: a server that lives for hours and a build that ended are different things and deserve different files.

* **Redaction rewrote file paths.** Deciding "mask this attribute" and "remove this value from every record" used one list of words, so any environment variable whose name matched — `CLAUDE_CODE_SESSION_ID` was the one that bit — had its value stripped out of unrelated text. Every path in the log came out naming a directory that does not exist. The two decisions are now separate lists, the second strictly narrower, and a value shaped like a path or carrying whitespace is never substituted whatever its variable is called.

* **`frznforge build` hung forever on any repository containing a file larger than `ingest.maxBlobBytes`.** It printed the repository's name and then stopped, with no error and no output, on Windows only — and only when run from cmd.exe or PowerShell rather than Git Bash. Reading a blob stops at the size limit and used to kill git to end it, but the `git` on the PATH those shells give you is a 46 KB wrapper in `Git\cmd` that re-execs the real 4 MB binary in `Git\mingw64\bin`: killing the wrapper leaves the real git running with the stderr pipe still open, so `cmd.Wait()` waited for an EOF that could never arrive. Stopping the read now closes the pipe instead, which makes git exit by itself whichever binary is really running, and `WaitDelay` bounds the wait so no inherited handle can ever wedge a run again.

* **A search result of an unrecognised kind vanished from the command palette.** `scoreDoc` added `KIND_BONUS[doc.kind]` without a fallback, so a kind missing from that table made the whole score `NaN` — and since `search` keeps a document only when `score > 0`, which is false for `NaN`, the document was dropped rather than ranked last, with nothing logged anywhere. It was latent while one TypeScript file built the index and another read it; it is a live risk now that the index is built in Go (`internal/build/search_index.go`) and ranked in the browser (`web/js/search.js`), where a kind can be added on one side alone. Found by running the ranking against the real emitted index for the first time.

* **The command palette offered file results that 404.** The search index listed every blob in a repo's default-branch tree, including paths holding `#` or `%` — which get no blob page, because no static URL can round-trip them. Ingest already said so, raising `repo-path-unservable` with the words "listed in the file table but has no page", and the index listed them anyway. It now applies the same exclusion the route builder does, which is the only way the two can agree.

* **`.cfg` and `.conf` files rendered with no syntax colouring.** Ingest labels them `INI` (`src/lib/ingest/languages.ts:182`) but the highlighter's language map was keyed `'Ini'` (`src/lib/highlight.ts:64`), so the lookup missed and the file fell through to the extension fallback — which rescues `.ini`, because Shiki has a language of that name, and cannot rescue `.cfg` or `.conf`, because it does not. Found while porting the map to Go, where it is now checked in both directions: a name ingest can emit that the map does not cover is a test failure, not an uncoloured file.

## Other

* **The Node build path is gone.** `src/`, `scripts/`, `tests/unit/`, the parity harness, `astro.config.ts`, `svelte.config.js` and `vitest.config.ts` are deleted, and `package.json` is down to `@playwright/test` — `node_modules` went from a full Astro toolchain to 5 packages and 22 MB. Building a site now needs **Go and git and nothing else**; Node is required only to run the browser suite. The Playwright specs are unchanged and still pass 213/213, now against a fixture the Go binary ingests and builds and two `frznforge dev` servers.

* **The TypeScript 7 deferral is closed by removing TypeScript, not upgrading it.** 0.3.0 pinned the compiler at 6.0.3 because `@astrojs/check` and `@astrojs/svelte` both declared `typescript: ^5 || ^6`; both are gone, and with them the only thing that wanted a compiler. Playwright transpiles its own specs, so the suite runs with none installed and `npm run check` no longer exists. `tsconfig.json` stays for editors, and the harness is no longer type-checked in CI — thirteen spec files that fail loudly when wrong are a cheaper guard than a compiler nothing else needs.

* **A whole child process disappeared with the config format.** `scripts/lib/config-load.ts` existed only because tsx caches modules by path, so the wizard could not re-read the config it had just written without spawning a fresh process and waiting up to 20 seconds for it. Go re-reads a file. The generation counter, the in-flight memo and the spawn timeout are deleted rather than ported, and each handler simply reads the file again — which is the clearest single illustration of what moving the config from TypeScript to JSONC actually bought.

* **Documentation rewritten against the code.** Every dev and user doc was checked claim by claim against what the engine actually does rather than against its previous version — with mermaid diagrams and `file:line` references in the dev docs, and every command in the user docs executed before it was written down. The migration guide, the release checklist and the quick start were the ones that had rotted furthest. The sweep found more in the code than in the prose: a mirror-cache collision, four separate tests that had quietly stopped asserting anything after Phase 9 deleted what they read, and a fix from earlier in this version that had been reported as working while ignoring its own argument.

* **The test suite was audited file by file, and the audit is written down.** All 42 vitest files are mapped to their Go replacements in `docs/dev/architecture.md`, with five gaps named rather than quietly dropped. Three were closed on the spot: the working-tree rule — the invariant that ingest reads git through the CLI and never the checkout — had no Go coverage at all; the contribution graph and activity feed were covered only by the Astro parity harness, which is deleted this version and which compares two implementations that could be wrong the same way; and the artifact-to-routes sync tests skipped entirely on a machine with no `data/forge.json`, so on a clean clone the build's strongest structural test printed `ok` while asserting nothing. Two gaps remain open and are documented with the reasoning: browser search ranking, and the cross-language goldens.

* **A changed browser helper can no longer outrun its golden.** `tests/fixtures/{format,listing}-cases.json` are generated *from* `web/js/{format,listing}.js`, which makes the JavaScript the reference — but a snapshot is only as current as the last person who regenerated it, so editing the JavaScript and forgetting the regeneration left Go agreeing with the old behaviour while the browser shipped the new. Each fixture now records the sha256 of the file it came from, and the Go test re-hashes it and fails with the exact command to run.

* **The Go engine's foundation is in.** `frznforge verify` reads and re-emits the artifact byte for byte; the config moved to JSONC with `frznforge config migrate` carrying every comment across; routes, display helpers, frontmatter, markdown (goldmark) and highlighting (chroma) are ported. Each is pinned to the implementation it replaces by a golden generated *from* that implementation — 256 routes at two deploy bases, 145 formatting cases against the browser's own copy, 35 frontmatter cases, 85 language names. Two dependencies, both pure Go.

* **The site no longer ships a framework.** The three Svelte islands are now plain web components — `<hf-repo-listing>` and `<hf-command-palette>` — served from a new `web/` folder with no build step of any kind: no transpile, no bundler, no hashing. The listing is *progressive enhancement* rather than hydration, so the server renders the complete first page and the element adopts that DOM instead of re-rendering it. `dist/_astro/` went from **102 files / 3,495,584 bytes to 2 files / 64,281 bytes** — stylesheets only, zero JavaScript — and with Astro no longer inlining its island runtime into every page, `dist/` fell from 65.6 MB to 62.0 MB across the same 633 pages. All 187 existing Playwright tests pass unedited.

* **Mermaid is vendored instead of bundled.** It was the only dependency that genuinely needed a bundler: Vite split it into ~97 chunks totalling 3.42 MB, which was 98% of everything in `dist/_astro/`. `web/vendor/mermaid/` now holds the ESM build mermaid publishes for browsers, loaded lazily by relative import, so a diagram-free page still fetches none of it and the published pages still call no third-party host.

* **Shared browser/build logic has one implementation.** `web/js/{format,listing,search,base}.js` are plain JavaScript with JSDoc types, imported by the browser directly and re-exported by `src/lib/{format,listing,search,base}.ts`, which add only the helpers that take artifact types. The old rule — "`format.ts` and `listing.ts` must stay browser-safe" — is replaced by there being nothing to keep in sync.

  *Migration:* `web/` is part of the engine. If you assembled your site by copying the engine into it, copy `web/` too, and drop `svelte.config.js` — `docs/user/starting-a-site.md` has the updated list. Without it the site builds, then serves a dead listing and no command palette.

* **Dependency updates, scoped to what survives the rewrite.** Updated `@playwright/test` (1.62.1 → 1.63.0) and `@types/node` (26.4.0 → 26.4.1). Astro (7.2.9 → 7.3.1) and vitest (4 → 5) were deliberately *not* updated: 0.4.0 replaces Astro with a Go build and ports the vitest suite to Go, and until then Astro's output is the reference the new renderer is compared against — moving it mid-migration would move the reference. TypeScript 7 stays deferred here for the same reason it was deferred in 0.3.0, and the deferral is re-opened once `@astrojs/check` and `@astrojs/svelte`, which pin `typescript: ^5 || ^6`, leave the repository.

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

* **`npm run build` takes flags.** It is a script rather than a chained `ingest && astro build`, so the two halves can be steered: `--no-ingest` renders the artifact already on disk (for iterating on templates and styles, or after a `--backfill-metadata` run), and ingest flags like `--backfill-metadata` are forwarded to the ingest step. Anything else is passed through to `astro build`. `--no-ingest` refuses to run when there is no artifact yet, rather than quietly building an empty site over a good one.

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
