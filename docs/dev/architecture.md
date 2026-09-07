# Architecture

> Written for 0.4.0, the version that replaced the Astro/TypeScript build with a single Go
> binary. Three things are in here: **the pipeline**, **the invariants it rests on**, and **the
> test coverage audit** that proves the vitest suite was ported rather than dropped.

## The pipeline

```mermaid
flowchart LR
  subgraph ingest["frznforge ingest — reads git and the network"]
    cfg[["frznforge.config.jsonc"]] --> scan
    scan["ScanRepo\ninternal/ingest/scan.go:123"] --> asm
    notes[["notes/*.md"]] --> asm
    orgs[["content/orgs/*.md"]] --> asm
    asm["Assemble\ninternal/ingest/assemble.go:69"] --> art
    art[("data/forge.json\nschema v8 + blobs/ + archives/")]
  end

  subgraph build["frznforge build — reads neither git nor the network"]
    art --> load["model.Parse\ninternal/model/serialize.go"]
    load --> router["routes.Router.AllRoutes\ninternal/routes/routes.go"]
    router --> emit["page emitters\ninternal/build/pages_*.go"]
    emit --> dist[("dist/")]
    dist -.optional.-> post["postprocess hook\ninternal/build/postprocess.go"]
  end

  subgraph view["the browser — served verbatim, no build step"]
    dist --> js["web/js/*.js\nweb components + listing + palette"]
  end
```

The artifact is the seam. Everything left of it reads the world; everything right of it is a pure
function of the file. That is what makes the build reproducible, and it is why the tests below
split so cleanly into "does ingest read git correctly" and "does the build render the artifact
correctly".

One thing the diagram does *not* show, because it never got built: the fetch/render overlap the
plan called "the streaming pipeline". It was sized and dropped — on the four-repo benchmark
corpus ingest is 7.1 s against a 204 s render, so overlapping the two can save seconds, while
spreading the render across cores saves minutes (`internal/build/parallel.go:19-33`). What
shipped instead is parallel rendering at two levels. The artifact is therefore still written
once and read once (`internal/build/build.go:166`), which is why every page can render
`len .Data.Warnings` in its footer (`internal/render/templates/shell.gohtml:54`) — the count is
known before the first page is written, and the plan's "move the warning count to the client"
decision never had to be paid for.

---

## The three invariants

Everything else in this document is a consequence of these. They are stated in the order a
violation is expensive: break the first and two machines emit different sites from the same input,
break the second and a page starts depending on something outside the artifact, break the third
and the site publishes code nobody committed.

### 1. Determinism — the same artifact produces the same bytes

Two machines, two days, two worker counts: identical input must emit an identical `dist/`. Go
makes this harder than TypeScript did, in two specific ways, and each has a rule:

| Hazard | Rule | Where |
|---|---|---|
| Map iteration is randomised | every ordered output sorts explicitly, with a plain `<` on the string | `internal/ingest/git.go:8-12` |
| A locale-aware compare depends on the build machine's ICU data | code-point order everywhere, on both sides of the artifact | `internal/ingest/git.go:10-12` (ingest), `internal/build/pages_repo_refs.go:398-400` (the file table, where the `localeCompare` bug the port fixed was) |

Concurrency is the third hazard and it is handled structurally rather than by rule: every unit
writes a path derived from the artifact and no two units write the same path, so parallelism
changes the *order* of writes and nothing about their content or their names
(`internal/build/parallel.go:61-70`). `TestSerialAndParallelAgree`
(`internal/build/parallel_test.go:22`) and `TestBuildIsDeterministic`
(`internal/build/determinism_test.go:32`) are the gates, and both run over the fixture always and
over the developer's own corpus when it is there (`buildRoots`, `internal/build/sync_test.go:394`).

The clock is the fourth, and it is the one input a reproducible *site* is allowed to vary on:
"3 days ago" legitimately changes with real time. So it is a parameter rather than a call.
`Options.Now` flows into `render.Site.Now` and every relative date and heat bucket is computed
from it (`internal/build/build.go:66-68`, `internal/render/format.go:30`). Same artifact plus
same clock means same bytes, which is exactly what `TestBuildIsDeterministic` pins.

Wall-clock values are *written down* in only two places, and neither reaches the artifact or
`dist/`: the run log under `<cacheDir>` (`internal/ingest/reuse.go:117-119`) and the timings file,
which measures elapsed time for a living and says so (`internal/timings/timings.go:51-56`).
Nothing clock-derived may enter `forge.json` at all — every date there comes from git.

### 2. The artifact is the seam

`data/forge.json` is a hard boundary, not a convention. Left of it, code reads git, the network
and the filesystem. Right of it, code reads the artifact and the blob store and **nothing else**
— an emitter may not open a file except through `Builder.Blob`, may not read the clock, and may
not sort with a locale-aware comparison (`internal/build/build.go:33-36`, restated for authors in
`internal/build/CONTRACT.md`).

This is what makes the rewrite checkable at all. Because the schema did not move, "the Go ingest
is correct" is a byte-identity statement against the TypeScript ingest rather than a judgement
call, and `frznforge verify` (`cmd/frznforge/main.go:459`) is that check as one command: parse,
re-serialize, compare byte for byte, and point at the first differing byte when it does not
match.

It also draws the line for the two halves' failure modes. Ingest never fails a build over a
repository's state — an empty repo, an unreachable forge, a missing path are warnings
(`cmd/frznforge/ingest.go:13-17`). The renderer never *starts* one without an artifact:
`--no-ingest` refuses rather than publishing an empty site over a good one
(`cmd/frznforge/main.go:349`).

### 3. Git is read through the CLI, never the working tree

Every git call in `internal/ingest` is plumbing over the object database — `for-each-ref`,
`rev-list`, `log`, `ls-tree`, `cat-file`, `rev-parse`. The index and the checkout are never
consulted, so a dirty tree and a clean one produce the same artifact and a bare mirror works
exactly like a checkout (`internal/ingest/git.go:16-18`).

The guard is `TestUncommittedChangesNeverLeak` (`internal/ingest/uncommitted_test.go:26`), which
dirties a fixture every way a tree can be dirtied and asserts the artifact comes out
byte-identical. It is worth knowing that this test did not exist until the Phase 9 audit went
looking for it: the invariant every other guarantee rests on is also the one most easily lost by
accident, because the natural way to read a file is to open it.

Notes are the one deliberate exception and they do not weaken the rule — `notes.dir` is a plain
folder, not a repository, so there is no committed tree to prefer. See
[data-model.md](./data-model.md#guarantees).

---

## The Go packages

Two binaries over one set of internal packages. The arrows are real imports
(`go list -deps ./cmd/...`), and the shape is the point: `internal/model` is at the bottom and
depends on nothing, `internal/ingest` and `internal/build` never import each other, and nothing
imports upward.

```mermaid
flowchart TD
  subgraph cmds["cmd/"]
    ff["frznforge<br/>cmd/frznforge/main.go:44"]
    fd["frzndebugger<br/>cmd/frzndebugger/main.go:1"]
  end

  subgraph left["reads the world"]
    ing["ingest<br/>git CLI + forge APIs"]
    cfg["config<br/>JSONC + defaults + paths"]
    wiz["wizard<br/>init --web"]
    scaf["scaffold<br/>new"]
  end

  subgraph right["pure function of the artifact"]
    bld["build<br/>page families + the pool"]
    rnd["render<br/>html/template"]
    hl["highlight<br/>chroma"]
    md["markdown<br/>goldmark"]
    rt["routes"]
    fm["frontmatter"]
  end

  subgraph base["the contract, and the evidence"]
    mdl["model<br/>schema v8"]
    log["logging<br/>frznforge.log"]
    tim["timings<br/>*-timings.jsonl"]
    srv["serve<br/>frznforge dev"]
  end

  ff --> bld & ing & cfg & wiz & scaf & srv & mdl & log & tim
  fd --> cfg & log & tim
  bld --> rnd & hl & md & rt & fm & cfg & mdl & tim
  ing --> cfg & mdl & rt & md & fm & log & tim
  wiz --> cfg & ing & md
  rnd --> cfg & mdl & rt
  md --> mdl
  rt --> mdl
  tim --> log
```

| Package | What it owns | Start reading at |
|---|---|---|
| `internal/model` | the schema v8 structs, `Parse`, `Serialize`, `Validate` | `internal/model/model.go:44` |
| `internal/config` | the JSONC dialect, defaults, absolute paths, `config migrate` | `internal/config/config.go:328`, `:702` |
| `internal/ingest` | git, the forge importers, the four skips, assembly, `WriteArtifact` | `internal/ingest/ingest.go:136` |
| `internal/routes` | every URL the site emits, and `AllRoutes` — the list the output is checked against | `internal/routes/routes.go:581` |
| `internal/render` | the template set, the shell, and the server half of the browser pair | `internal/render/render.go:1` |
| `internal/build` | the page families, the worker pool, the postprocess hook | `internal/build/build.go:144` |
| `internal/markdown` | goldmark plus the two trust levels | `internal/markdown/markdown.go:1` |
| `internal/highlight` | chroma, the language map, the per-lexer lock | `internal/highlight/highlight.go:1` |
| `internal/frontmatter` | the YAML subset `profile.md`, org pages and notes use | `internal/frontmatter/frontmatter.go` |
| `internal/serve` | `frznforge dev`, which is also the e2e suite's server | `internal/serve/serve.go:1` |
| `internal/logging`, `internal/timings` | the two diagnostic files every run leaves behind | `internal/logging/file.go:13`, `internal/timings/timings.go:1` |
| `internal/scaffold`, `internal/wizard` | `frznforge new`, and `init --web` | `internal/scaffold/scaffold.go`, `internal/wizard/wizard.go` |
| `internal/theme` | the WCAG maths over the palette tokens — **build-time code with no build-time caller**: nothing imports it but its own tests, on purpose | `internal/theme/theme.go:1` |

Two structural rules hold this shape in place:

- **One file per page family.** Each `internal/build/pages_*.go` exports one emit function that
  `build.go` calls (`internal/build/build.go:257` for the site-wide set, `:321` for the per-repo
  set). Nothing else in the package is shared, which is what let the families be written
  independently in Phase 5 — and what stops two of them quietly disagreeing about the shell.
- **`internal/routes` is the only source of URLs.** An emitter never hand-builds an `href`,
  because the router is also what the sync tests read; a URL builder only the renderer used
  could disagree with the test meant to police it (`internal/routes/routes.go:4-7`).

### The no-build asset rule

`public/` and `web/` are copied into the output **verbatim** — no transform, no rename, no
content hash (`internal/build/copyAssets`, `internal/build/build.go:448`). This is the rule a
later convenience quietly breaks, so it is worth stating what depends on it: `web/js/*.js` are
loaded by the browser as written, with relative imports carrying their `.js` extensions, and the
cross-language goldens in `tests/fixtures/` are generated from those exact files. A build step
between disk and browser would make the golden a check on something the browser never runs.

`web/` is copied from the **project**, not embedded in the binary. A site directory that does not
carry it builds pages with no stylesheet and no command palette — the build succeeds and the site
is dead.

The single sanctioned seam is the postprocess hook: one user-supplied command over the finished
directory, after a build that succeeded, with nothing running by default
(`internal/build/postprocess.go:3`).

---

## Test coverage audit

0.3.0 had **42 vitest files** covering the TypeScript. 0.4.0 deletes the TypeScript, so every one
of them had to go somewhere. This table is the accounting, and it is deliberately unflattering:
the plan named this as "the single most likely place for this version to lose something", so a
row that says *partial* or *gap* is more useful here than a row that says *done*.

Status key — **✅ ported**, **◐ partial** (named below), **⊘ obsolete** (the thing it tested no
longer exists). Every row is now settled; nothing is outstanding.

### Ingest — reads git through the CLI, never the working tree

| vitest file | Go replacement | |
|---|---|---|
| `uncommitted.test.ts` | `internal/ingest/uncommitted_test.go` | ✅ |
| `ingest.test.ts` | `internal/ingest/scan_test.go`, `assemble_test.go` | ✅ |
| `commits.test.ts` | `internal/ingest/commits_test.go` | ✅ |
| `contributors.test.ts` | `internal/ingest/contributors_test.go` | ✅ |
| `refs.test.ts` | `internal/ingest/refs_test.go` | ✅ |
| `scan-refs.test.ts` | `internal/ingest/refs_test.go`, `scan_test.go` | ✅ |
| `tree.test.ts` | `internal/ingest/tree_test.go` | ✅ |
| `languages.test.ts` | `internal/ingest/languages_test.go` | ✅ |
| `license.test.ts` | `internal/ingest/license_test.go` | ✅ |
| `meta.test.ts` | `internal/ingest/meta_test.go` | ✅ |
| `insights.test.ts` | `internal/ingest/insights_test.go` | ✅ |
| `notes.test.ts` | `internal/ingest/notes_test.go` | ✅ |
| `orgs.test.ts` | `internal/ingest/orgs_test.go` | ✅ |
| `hosting.test.ts` | `internal/ingest/hosting_test.go` | ✅ |
| `importers.test.ts` | `internal/ingest/importers_test.go`, `importers_parity_test.go` | ✅ |
| `remote.test.ts` | `internal/ingest/remote_test.go` | ✅ |
| `backoff.test.ts` | `internal/ingest/backoff_test.go` | ✅ |
| `reuse.test.ts` | `internal/ingest/reuse_test.go` | ✅ |
| `branch-cap.test.ts` | `internal/ingest/scan_test.go` (`TestScanRepoCapWarnings`, `TestScanRepoHostedBranchIsExemptFromTheCap`), `identity_test.go` | ◐ |
| `empty-states.test.ts` | `internal/ingest/scan_test.go` (`TestScanRepoEmpty`), `assemble_test.go` (`TestAssembleEmptyInputIsAValidArtifact`), `internal/build/sync_test.go` | ✅ |

### The artifact and the routes

| vitest file | Go replacement | |
|---|---|---|
| `schema.test.ts` | `internal/model/model_test.go` | ✅ |
| `repo-path-encoding.test.ts` | `internal/routes/routes_test.go` | ✅ |
| `site-sync.test.ts` | `internal/build/sync_test.go` | ✅ |
| `phase3-sync.test.ts` | `internal/build/sync_test.go`, `internal/build/routes_emitted_test.go` | ✅ |
| `phase6-sync.test.ts` | `internal/build/sync_test.go` | ✅ |
| `base-path.test.ts` | `internal/config/config_test.go`, `internal/routes/routes_test.go`, `tests/e2e/base-path.spec.ts` | ◐ |
| `config-knobs.test.ts` | `internal/config/config_test.go` (`TestMatchesTypeScriptDefaults`) | ✅ |

### Rendering

| vitest file | Go replacement | |
|---|---|---|
| `markdown.test.ts` | `internal/markdown/markdown_test.go`, `adhoc_xss_test.go` | ✅ |
| `phase34-libs.test.ts` | `internal/routes/routes_test.go` (slugs, routes), `internal/highlight/highlight_test.go` (language mapping), `internal/build/profile_test.go` (contributions, activity), `internal/build/search_index_test.go` (index) | ✅ |
| `search.test.ts` | `internal/build/search_index_test.go` — **index only** | ◐ |
| `format.test.ts` | `internal/render/format_test.go` + `golden_test.go` | ◐ |
| `listing.test.ts` | `internal/render/listing_test.go` + `golden_test.go` | ◐ |
| `contrast.test.ts` | `internal/theme/contrast_test.go` | ✅ |
| `assets.test.ts` | `internal/theme/assets_test.go` | ✅ |
| `heat-sync.test.ts` | — | ⊘ |
| `highlight-cache.test.ts` | — | ⊘ |

### The CLI surface

| vitest file | Go replacement | |
|---|---|---|
| `scaffold.test.ts` | `internal/scaffold/{scaffold,build,command}_test.go` — 37 | ✅ |
| `cli.test.ts` | `cmd/frznforge/{cli,dispatch,entries,flags,init,listing,select}_test.go` — 129 | ✅ |
| `build-script.test.ts` | `cmd/frznforge/build_test.go` | ✅ |
| `dev-script.test.ts` | `internal/serve/{serve,mime,notice}_test.go` — 34 | ✅ |
| `config-edit.test.ts` | `internal/wizard/edit_test.go` | ✅ |
| `web-init.test.ts` | `internal/wizard/{server,settings,scaffolded}_test.go` — 97 with the above | ✅ |

---

## The gaps, named

### 1. Browser search ranking had no coverage that survives Node — closed

`web/js/search.js` holds `scoreDoc` and `search` — what is matchable and what outranks what.
Nothing in Go ports them, because nothing server-side ranks: `internal/build/search_index_test.go`
covers the **index** the browser downloads, not the **query** it runs against it.

`tests/unit/search.test.ts` has 18 tests on that ranking. The Playwright suite exercises the
palette's behaviour (`tests/e2e/phase4.spec.ts:33`) — it opens, it finds a repo, the keyboard
works — but it asserts nothing about ordering, so a ranking regression would ship.

**Closed in Phase 9** by `tests/e2e/browser-js.spec.ts` — 21 tests, run in a real browser against
the exact files the server serves. That is better coverage than the unit runner gave them, and it
costs no dependency the version was not already keeping.

It also ended up stronger than the vitest tests it replaces. Under vitest, `buildSearchIndex` and
`scoreDoc` were two functions in one language and one test called both, so the producer and the
consumer were coupled for free. In 0.4.0 the producer is Go and the consumer is JavaScript, and
nothing was standing between them. The spec's last three tests fetch the real emitted
`/search-index.json` and assert what must hold for any index: every document is rankable, every
repository is first for its own name, and every result points at a page that exists.

Two defects surfaced immediately:

- **`web/js/search.js` dropped documents of an unknown kind.** `score += KIND_BONUS[doc.kind]` is
  `undefined` for a kind not in the table, so the score became `NaN` — and `search` keeps a
  document only when `score > 0`, which is false for `NaN`. The document did not rank last, it
  vanished, with nothing logged. Harmless while one file built the index and another read it;
  a live risk now that a kind can be added on the Go side alone. Fixed, and pinned by a test.
- **The dead-URL test is red against the Astro engine**, on exactly `docs/c#-tips.md` and
  `docs/50% off.txt`. That is the palette-404 bug already fixed in the Go build and recorded in
  the changelog, rediscovered from the browser's side. Phase 9 repointed the harness at the Go binary and deleted the Astro branch, so it is green: there is only one engine now.

Two smaller things were seen while reading `web/js/` and deliberately **not** changed, because
neither is a defect a visitor would notice and both alter ranking or URL output that other tests
are generated from:

- `search.js` gives an exact repository-name match a bonus large enough that nothing can displace
  it, but compares `title === query.trim().toLowerCase()` while the terms themselves are split on
  `/\s+/`. A repository named `my repo`, typed with two spaces, still matches — it just quietly
  loses the bonus the comment above it calls "wins outright".
- `listing.js`'s `toSearchParams` serialises a whitespace-only `q`, producing `?q=+++` for a query
  `matchesQuery` treats as no query at all. It round-trips correctly, so it is a URL-cleanliness
  wart rather than a bug. The whitespace behaviour it exposes *is* now pinned on both sides
  (`blankQueries` in the listing golden).

### 2. The cross-language goldens are snapshots

`web/js/format.js` and `web/js/listing.js` are pinned to their Go twins through
`tests/fixtures/{format,listing}-cases.json`, generated **from** the JavaScript. A snapshot is
only as current as the last person who regenerated it — change the JavaScript, skip the
regeneration, and Go keeps agreeing with the old behaviour while the browser ships the new.

Closed in Phase 9: each fixture now records the sha256 of the file it was generated from, and
`internal/render/golden_test.go` re-hashes and fails with the regeneration command. That catches
*staleness*. It does not replace the JavaScript's own tests — see gap 1.

### 3. Build tests that skipped without a local artifact — closed

`internal/build/{determinism,parallel,routes_emitted}_test.go` read the developer's
`data/forge.json` and called `t.Skip` when there was not one. On the clean clone Checkpoint D
requires, and on any CI runner, they printed `ok` while asserting nothing — which is
indistinguishable from passing in every summary anyone reads.

`internal/build/sync_test.go` now owns its inputs end to end: fixture git repositories, a config,
the real Go ingest, the real build. `buildRoots` in that file is what the determinism and
serial-vs-parallel gates run over, so both execute against the fixture **always** and against the
developer's own 2,107-file corpus **as well** when it happens to be there. The big corpus is far
better evidence; it just cannot be relied on to exist.

`routes_emitted_test.go` still skips, and is now redundant rather than load-bearing: `sync_test.go`
makes the same both-directions claim without skipping. It is kept because it makes it over the
real corpus.

### 4. Two tests are obsolete rather than ported

- **`heat-sync.test.ts`** read the *source* to check that every `heatFor(` and `<RepoCard` call
  site threaded `theme.heat` through, because every link in that chain had a silent default. In
  Go there is no silent default to fall through to: `render.HeatFor` takes
  `config.HeatConfig` as a required parameter (`internal/render/format.go:30`), as does
  `buildContribGraph` (`internal/build/pages_profile.go`). Forgetting to thread it is a compile
  error. The type system replaced the test.
- **`highlight-cache.test.ts`** covered `src/lib/highlight-cache.ts`, the cross-run memo that
  existed because Shiki was 84% of the render. chroma is a different order of tool and 0.4.0
  ships no memo at all (`internal/highlight/highlight.go:15` says so and why). The feature is
  gone, so its tests go with it — see `docs/dev/performance.md` for the numbers behind the call.

### 5. `branch-cap` and `base-path` are covered in pieces

Neither has a single Go file that owns it. `branchTrees` is exercised by `scan_test.go`'s cap
warnings and by `identity_test.go`, but the TypeScript test's sharpest assertion — that
`repoRoutes()` actually *shrinks* — has no direct Go equivalent; it is implied by
`sync_test.go` rather than asserted. `site.base` is covered at the config layer, at the router
layer (`internal/routes/routes_test.go:38` runs `AllRoutes` under both `""` and `"/mysite"`), and end to end by
`tests/e2e/base-path.spec.ts`, but no Go test builds a whole site under a base and scans the
output for leaked absolute URLs the way the e2e spec does.

Both are judged acceptable: the assertions exist, they are just distributed. Recorded here so
that is a decision rather than an oversight.
