# Build performance

frznforge emits a fully static site, so "how long does it take" is really "how many pages are
there", and the page count is arithmetic on the artifact. This document gives you that
arithmetic, the numbers measured on a real multi-repo build, the knobs that move them, and the
things we deliberately did not build.

Everything here is reproducible with `scripts/measure-build.ts` — see
[Reproducing the measurement](#reproducing-the-measurement).

## Where the pages come from

Almost every page belongs to one of three families, and one of them is multiplied.

**Per repo, once:** the overview, `/branches/`, `/tags/`, `/releases/`, and (schema v5)
`/insights/` when `hasInsights(repo)`.

**Per repo, per item:** one page per commit in `repo.commits` (plus, schema v6, one per
display-support commit in `repo.extraCommits` — nonzero only when the history-narrowing
knobs are set or a tag points outside branch history), one paginated commit-list page
per 50 commits *per branch* (every branch, capped or not), one page per release, one zip per
archived ref.

**Per repo, per browsable ref × per path** — this is the multiplier:

```
tree pages = Σ over browsable refs ( 1 + directories in that ref )
blob pages = Σ over browsable refs ( files + symlinks in that ref )
raw files  = Σ over browsable refs ( files whose FileInfo.stored is true )
```

A **browsable ref** is a ref that has a tree in the artifact:

```
browsable refs = 1 (the default branch)
               + min(other branches, ingest.branchTrees)     # 'all' ⇒ no limit
               + min(tags,           ingest.tagTrees)        # minus name collisions
```

So the whole repo comes out as:

```
pages(repo) = 5                                   # overview + branches + tags + releases + insights
            + refs × (1 + dirs + files + symlinks + stored files)
            + Σ over branches ⌈commits(branch) / 50⌉
            + |repo.commits| + |repo.extraCommits|
            + distinct release tags
            + archives
```

Worked example — `sdu` from the benchmark below: 2 branches, 2 tags, `tagTrees: 3` ⇒ **4
browsable refs**, each with ~21 tree entries (4 dirs, 17 blobs, 17 of them stored).

```
5 + (18 tree + 68 blob + 68 raw) + 2 commit-list pages + 49 commits + 2 releases + 3 zips = 215
```

`npm run measure` (or `npx tsx scripts/measure-build.ts`) prints exactly this decomposition for
any artifact, so you can predict a build before you run it.

Two footnotes that matter when you compare numbers with the Astro build log:

- **Routes are not all `.html`.** `raw/*` routes write the file's bytes and archives write
  `.zip`s. In the benchmark below, 21,869 routes produced 13,164 HTML pages and 8,715 other
  files. When someone says "16k pages", check which they mean.
- **The multiplier is the whole story.** Tree + blob + raw are 87–90% of every build we have
  measured.

## Measured: `ingest.branchTrees`, before and after

The problem the cap fixes: tree/blob/raw pages are generated per browsable ref, and nothing
bounded how many branches were browsable. Four real public repos with 27 + 22 + 8 + 2 branches
made every one of them browsable.

**Machine.** Windows 11 Pro 26200, AMD Ryzen 9 7945HX (32 threads), 62 GB RAM, Node v24.6.0,
git 2.50.1, Astro 7.2.4. Measured **2026-08-24**, schema v5.

**Corpus.** `npm run smoke:remote` against its four public repos — GitHub `Descent098/sdu`,
GitLab `gitlab-org/release-cli`, Gitea `gitea/tea`, Codeberg `dnkl/fuzzel` — with the smoke
script's own caps (`maxCommits: 200`, `tagTrees: 3`) and a warm mirror cache. Both runs used
the identical working tree; only `ingest.branchTrees` changed.

| | `branchTrees: 'all'` | `branchTrees: 10` (default) | change |
|---|---:|---:|---:|
| browsable refs | 70 | 43 | −39% |
| routes | 27,813 | 21,869 | −21% |
| HTML pages | 16,388 | 13,164 | −20% |
| `astro build` | 301.2 s (5m01) | 204.2 s (3m24) | **−32%** |
| ingest (warm cache) | 9.1 s | 7.1 s | −22% |
| `dist/` on disk | 1,245.5 MB | 854.2 MB | −31% |
| files in `dist/` | 27,823 | 21,879 | −21% |

Per repo:

| repo | branches | refs (all → 10) | pages (all → 10) | change |
|---|---:|---:|---:|---:|
| `fuzzel` | 27 | 30 → 14 | 6,928 → 4,687 | −32% |
| `release-cli` | 22 | 25 → 14 | 6,891 → 3,188 | −54% |
| `tea` | 8 | 11 → 11 | 13,759 → 13,759 | **0%** |
| `sdu` | 2 | 4 → 4 | 215 → 215 | 0% |

### Read this honestly

The cap does what it claims — a third off the build, a third off the output — but it only
helps repos that have more than ten branches. `tea` has eight, so the default never touches
it, and `tea` alone is **63% of the capped build** (13,759 of 21,869 routes). Its cost is not
branch count: it is 647 tree entries per ref (a Go repo that vendors its dependencies) times
11 refs, three of which are tags.

Two consequences:

- For a vendored monorepo, `ingest.tagTrees` and a *low* `branchTrees` are the levers, not the
  default. `tea`'s eleven refs are wildly uneven: its default branch costs 704 pages, but its
  `release/v0.7` branch costs 3,812 and `release/v0.4` 2,185 — old branches that vendored more
  than the current one does. At `branchTrees: 0, tagTrees: 0` the repo would be **~1,240
  pages instead of 13,759**, an order of magnitude, from one line of config.
- The default of 10 is a safety rail against pathological branch counts, not a tuning knob.
  Anyone with a big repo still has to think about it. That is why the cap warns
  (`branch-trees-capped`) instead of silently trimming.

The plan's original measurement (15,988 pages in 5m23s) reproduces: the same corpus with `all`
now yields 16,388 HTML pages in 5m01, the difference being branches and commits added upstream
since, plus the four new insights pages.

## Measured: cross-run ingest reuse (`ingest.reuse`, 0.2.0)

The 0.2.0 plan asked why "the rebuild time with a constructed cache is as slow as a build
without one". The honest answer was that until 0.2.0 the caches saved **network only**: the
mirror saved the clone, `.meta.json` saved the API calls, and every run still re-ran the
full scan — `for-each-ref`, the whole `git log`, `ls-tree` per browsable ref,
`cat-file --batch` of every stored blob, `git archive` per treed ref, the insights
checkpoints. `ingest.reuse` (on by default) closes that: a repo whose refs, HEAD, metadata
inputs and scan options are unchanged replays its recorded scan from
`<cacheDir>/scan/<digest>.json`, and a remote source fetched fully-fresh within the last
`maxAgeMinutes` (default 2) is not re-fetched at all. Reuse never changes artifact bytes —
a hit replays exactly what the fresh scan produced, or quietly falls back to a real scan.

Measured on this repository's own site (1 repo, 18 commits, 219 files, 5 notes,
2026-08-28, Windows 11 / warm mirror):

| run                                   | `npm run ingest` |
| ------------------------------------- | ---------------- |
| cold scan cache (first run)           | ≈ 2.0 s          |
| warm (nothing changed)                | ≈ 0.22–0.24 s    |

−89% on the no-change re-ingest, which is exactly the `npm run build` inner loop while
editing content or styles. Keep the proportions in mind: on the four-repo remote corpus
above, warm-cache ingest was already 7.1 s against a 204 s render — ingest reuse makes the
small half smaller and does nothing for the large half, which remains a page-count problem
(see the cap, and "What we did not do"). `npm run ingest -- --no-cache` bypasses every
cache for one run; `tests/unit/reuse.test.ts` is the correctness half (byte-identity,
tamper-proof hit/invalidation cases, the degraded-repo retry rule, prune safety).

## Measured: astro render concurrency (0.2.0, adopted at 2)

The 0.2.0 plan's "concurrently build island-pages … one thread per repo" has no supported
shape — Astro has no per-repo build unit and no multi-process partial build, and sharding
was rejected above. What Astro does expose is `build.concurrency`: how many pages render at
once inside the one process. Measured on the self-build (schema v6 artifact, ~600 pages,
Windows 11, two runs per value, `Measure-Command { npx astro build }`, 2026-08-28):

| `build.concurrency` | runs           | median  |
| ------------------- | -------------- | ------- |
| 1 (Astro's default) | 19.2 s, 19.6 s | ≈19.4 s |
| **2 (adopted)**     | 18.2 s, 17.6 s | ≈17.9 s |
| 4                   | 21.8 s, 19.0 s | ≈20.4 s |

2 is a small (~8%) but consistent win — both its runs beat every run at 1 and 4 — and 4 is
measurably worse. That shape makes sense: the pages' blob reads are synchronous
(`readFileSync` in frontmatter), so there is little I/O for concurrent renders to overlap
and the render is CPU-bound. Adopted as a literal in `astro.config.mjs`; re-measure by
overriding it there if the page mix ever changes materially.

## Measured, then rejected again: skip-unchanged-pages (0.2.0)

The 0.2.0 wish list re-floated "if the most recent commit hash matches the one on the page,
skip rebuilding it in dist". The standing rejection above holds, and 0.2.0 moved its bar
*further away*: `ingest.reuse` cut the no-change re-ingest from ≈2.0 s to ≈0.22 s on the
self-build, so rendering now dominates a no-change `npm run build` even harder (the revisit
bar — "only with a measured build where ingest, not rendering, dominates" — fails by more
than it did in 0.1.0). Concretely, what a route → input-hash manifest would have to model
before a single page could be skipped safely: `search-index.json` (any repo/note/org
change), every listing page and the sidebar counts (any repo added/removed/renamed), the
footer warning count on every page (any warning anywhere), the profile's contribution
graph/activity feed/KPIs (any commit anywhere), org overview aggregates (any member
change), and `_astro/*` hashed asset names (any CSS/JS change relinks every page). A miss
in any of these is a silently wrong page in a build that reports success. Still no.

## Measured, then rejected: sqlite for blobs + cache (0.2.0)

The 0.2.0 wish list asked whether storing blobs and cache data in sqlite "can help make
things faster instead of storing it all as files and having to eat the cost of
reading+writing them all the time". Measured, that cost is a rounding error. On the
self-build artifact (295 blobs, 2.8 MB), timed through the two functions a backend swap
would replace (`readBlobBuffer` / `writeArtifact`):

| operation                                   | measured |
| ------------------------------------------- | -------- |
| read every blob in the store                | 45 ms    |
| `writeArtifact`, cold (every byte written)  | 188 ms   |
| `writeArtifact`, warm (stat-and-skip pass)  | 25 ms    |

Against a ≈2.3 s cold ingest and a ≈18 s render, a storage backend that cost literally
zero would win a few hundred milliseconds — and on the four-repo remote corpus, warm-cache
ingest (7.1 s) is dominated by git subprocess work, not blob I/O, with the 0.2.0 scan
cache already skipping the repeated reads that motivated the idea. The costs of adopting
it are real and the wins are not: reads must stay synchronous inside Astro frontmatter,
which means `node:sqlite`'s `DatabaseSync` — still printing an `ExperimentalWarning` on
Node 24 and flag-gated at this project's Node floor (`engines: >=22.12.0`) — or
`better-sqlite3`, a native build dependency in a project that keeps its production
dependency set deliberately tiny; sqlite file locking would need its own Windows
verification; and the
content-addressed `blobs/` directory is what makes the scan cache's rehydration and
`writeArtifact`'s prune trivially correct today. Rejected. Revisit only with a measured
corpus where blob-store I/O, not git or rendering, dominates the build.

## The knobs

| knob | default | what it costs you if you raise it | what you lose if you lower it |
|---|---|---|---|
| `ingest.branchTrees` | `10` | tree/blob/raw pages × each extra branch | older branches are listed on `/branches/` but not browsable; no file tree, no per-file pages, no archive |
| `ingest.tagTrees` | `25` | same multiplier, per tag; also one zip archive each | older tags are listed and still have release pages, but you cannot browse the code at that tag |
| `ingest.maxCommits` | `null` (all) | one page per commit, plus one list page per 50 per branch, plus ingest time reading them | history is truncated to the newest N per branch; the contribution graph, contributors and insights all see only that window |
| `ingest.maxBlobBytes` | `512 kB` | blob store size and `dist/` size; a big file's page is also the heaviest HTML you will ship | oversized files are listed with a size but have no content, no highlighting and no raw route |
| `ingest.insights.samples` | `24` | one `ls-tree` (+ bounded `cat-file`) per checkpoint at ingest; no extra pages | a coarser code-size line; the commits/contributors series is exact regardless |
| `ingest.insights.maxBytesPerSample` | `20 MB` | ingest time reading blob content to count lines | checkpoints past the budget report `lines: null`, the series is flagged `approximate`, and that checkpoint's `bytes` loses its binary filter — the blobs past the budget are never read, so they cannot be classified and their size is counted whatever they are. The page and the `insights-approximate` warning both say so |
| `ingest.archives` | `true` | one `git archive` per default branch + treed tag, and those bytes in `dist/` | no download-zip buttons |

Insights are cheap on the page-count side: they add exactly one page per non-empty repo.

## Asset budget

Measured on the `branchTrees: 10` build above. "gzip" is level 9 — a stand-in for what a
static host actually transfers; neither Astro nor frznforge precompresses.

**Shared assets, fetched once and cached for the whole site** (121.3 kB raw / 35.0 kB gzip):

| asset | raw | gzip | on which pages |
|---|---:|---:|---|
| `Base.css` | 58.9 kB | 10.5 kB | every page |
| `client.js` (Svelte runtime) | 40.5 kB | 15.6 kB | every page (pulled in by the palette island) |
| `CommandPalette.js` | 5.8 kB | 2.8 kB | every page |
| `client.svelte.js` | 0.9 kB | 0.5 kB | every page |
| `RepoListing.js` | 10.6 kB | 4.2 kB | `/repos/` and `/orgs/*/repos/` only |
| `notes.css` | 4.5 kB | 1.2 kB | note pages only |
| `search-index.json` | 65.9 kB | 6.1 kB | fetched by the command palette on first open |

So the shared cost of any page after the first is **~29 kB gzip**, and the HTML below is what
each additional navigation transfers.

**Page weight by type** (n = pages of that type in this build):

| page type | n | median | median gzip | p90 | p99 | max |
|---|---:|---:|---:|---:|---:|---:|
| blob (file view) | 8,702 | 37.3 kB | 8.7 kB | 137.3 kB | 707.6 kB | **2,846.5 kB** |
| tree (file browser) | 1,568 | 21.3 kB | 6.5 kB | 27.7 kB | 53.8 kB | 155.7 kB |
| single commit | 2,559 | 18.7 kB | 6.2 kB | 20.1 kB | 29.4 kB | 327.0 kB |
| commit list (50/page) | 213 | 65.1 kB | 11.0 kB | 66.9 kB | 67.9 kB | 68.0 kB |
| repo overview | 4 | 54.0 kB | 13.7 kB | — | — | 68.7 kB |
| insights | 4 | 42.6 kB | 9.3 kB | — | — | 61.8 kB |
| home | 1 | 39.7 kB | 9.0 kB | — | — | — |
| repo listing | 1 | 27.4 kB | 7.1 kB | — | — | — |

Budget, stated as a rule rather than a wish:

- **Typical page: under 15 kB gzip of HTML** plus the ~29 kB of shared assets. Everything but
  blob pages clears this comfortably at the median.
- **Blob pages are the outlier and always will be.** The weight is Shiki's output: one `<span>`
  per token, so a 300 kB machine-generated Go table becomes a 2.8 MB HTML page. It gzips to
  62 kB — a ratio of 46:1, because the markup is enormously repetitive — so the *transfer* is
  fine and the *disk* is not: those pages are why `dist/` is 854 MB. `ingest.maxBlobBytes` is
  the control; the current 512 kB default already excludes anything larger from the store.
- **Insights added no measurable weight**: 9.3 kB gzip median, inline SVG, no client JS, no
  chart library.
- **No page loads a third-party asset**, so there is no budget line for fonts or CDNs.
- **Mermaid (0.2.0) is the one deliberate exception to "no heavy JS", and it is fenced
  off.** Rendering ```` ```mermaid ```` fences client-side puts ~3.3 MB of code-split
  mermaid chunks *on disk* in `_astro/` — but a page loads them only if it actually holds a
  diagram, and only once one nears the viewport (`MermaidRenderer.astro` is included
  per-page on a `containsMermaid()` check, and the import is behind an
  IntersectionObserver). Measured on the e2e fixture site (local server, no compression):
  a diagram-free page still transfers exactly the ~48 kB raw JS it did before, asserted by
  `tests/e2e/mermaid.spec.ts`; the page with a flowchart + a sequence diagram transferred
  ~943 kB raw JS total (mermaid core + the two diagram-type chunks; gzip would cut that
  roughly 3×). The fences themselves cost nothing at build time — the static HTML carries
  only the escaped diagram source, which is also the no-JS fallback.

## Reproducing the measurement

```sh
# 1. build a real multi-repo artifact (live network, four public repos)
npx tsx scripts/smoke-remote.ts --branch-trees=all
npx tsx scripts/measure-build.ts --data=tests/.tmp/smoke/data --build --out=tests/.tmp/perf/dist-all

# 2. the same corpus at the default cap, reusing the mirrors so only the cap differs
npx tsx scripts/smoke-remote.ts --keep-cache --branch-trees=10
npx tsx scripts/measure-build.ts --data=tests/.tmp/smoke/data --build --out=tests/.tmp/perf/dist-10
```

`measure-build.ts` takes `--data=<dir>` (default `./data`), never writes into the artifact, and
without `--build` runs nothing at all — safe against a live `./data` mid-session. `--json`
emits the same report as machine-readable data; `--ingest-ms=<n>` folds an ingest duration into
the table (the artifact cannot record its own build time without breaking determinism, so both
`npm run ingest` and the smoke script print it for you to pass in).

`tests/unit/branch-cap.test.ts` is the correctness half: it drives the real scanner against
fixture repos and asserts that the cap keeps the right refs, warns with the right counts,
tie-breaks deterministically, and actually shrinks `repoRoutes()`.

## What we did not do, and why

The Phase 7 plan floated more than the cap. These were considered and rejected; the reasoning
is here so it does not have to be re-litigated.

**Incremental build caching between runs.** The tempting version — remember which pages were
rendered last time and skip the unchanged ones — needs a dependency graph from artifact fields
to output pages, and it needs to be *right*, because a stale page is a silently wrong site.
Astro has no supported partial-output mode, so we would own both the graph and the
invalidation. Meanwhile the caching that pays for itself already exists: mirror clones are
cached (`ingest.cacheDir`), the blob store is content-addressed so identical content across
refs is stored once, and the artifact itself is the boundary between "read git" and "render
pages". What remains is Astro's per-page render at ~15 ms, which is not a caching problem, it
is a page-count problem — and page count is what the cap addresses. Revisit only with a
measured build where ingest, not rendering, dominates.

**A client-side file viewer instead of static blob pages.** Generating blob pages only for the
default branch and fetching the rest from the existing `raw/` routes would delete most of the
build. It would also mean no-JS users cannot read code, deep links resolve through JavaScript
rather than the filesystem, syntax highlighting moves into the browser (Shiki's grammars are
megabytes), and every accessibility guarantee we hold today would need re-testing against a
dynamic view. A frozen forge whose main feature needs JavaScript is a different product.

**Dropping `raw/` routes for non-default refs.** Halves the file count, breaks "every file you
can see, you can download", and saves little wall clock — raw routes are a file copy, not a
render.

**Precompressing `dist/` (gzip/brotli on disk).** Every target host does this in the CDN layer,
and doubling the file count to pre-bake it would make the build slower, not faster.

**Excluding vendored paths from blob pages.** This is the single biggest lever on the corpus
above — `tea`'s `vendor/` is most of its 647 entries per ref. It is rejected because it changes
what the site *shows*, not what it costs: a mirror that silently omits vendored code is
lying about the repository. Vendored paths are already excluded from language stats and from
insights' code-size series, where they distort a measurement rather than hide a file.

**Sharding or parallelising `astro build`.** Astro renders pages concurrently already, and
splitting one site across processes means merging `dist/` and reconciling the shared asset
hashes. No.
