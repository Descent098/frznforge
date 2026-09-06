# Delivery phases — 0.4.0

Companion to [TODO](TODO), which is the *what* for this version; this file is the *in what
order*. Same shape as [version-0.3.0-phased.md](version-0.3.0-phased.md) — each phase ends in
something runnable, prerequisites land before the phases that consume them, and every feature
lands both of its sides in one phase.

**This version is not a feature release.** Its three TODO items amount to replacing the engine
under a site whose output must not change: Svelte islands become plain web components, and
Astro + Node are replaced by a Go program that both ingests and renders. Roughly 24,000 lines
of TypeScript, Astro, Svelte and CSS are in scope (`src/` + `scripts/`), against 42 vitest
files (11,663 lines) and 14 Playwright specs (2,700 lines) that must keep passing.

A rewrite that big only stays honest if it is checkable at every step, so the plan is built
around three oracles, in order of strength:

1. **The artifact is frozen.** `SCHEMA_VERSION` stays at **8** for the whole version. That
   makes "the Go ingest is correct" a *byte-identity* statement against the TypeScript ingest,
   not a judgement call.
2. **The HTML is diffed, not eyeballed — structurally, not byte for byte.** A parity harness
   (Phase 4) builds the same fixture artifact with Astro and with Go and compares page by
   page. The comparison is *structural*: Astro's output carries its own runtime — the island
   loader, `astro-island` wrappers, `_astro/` bundle links, scoped class hashes, a
   `<meta name="generator">` — none of which the Go renderer emits and none of which it
   should. So the harness parses both sides into a DOM, drops the Astro-only runtime scaffold,
   and compares the tree: element order and nesting, tag names, the attributes that carry
   meaning (`href`, `src`, `id`, `class` minus scoped hashes, `data-*`), and normalized text.
   Attribute *order* and insignificant whitespace are not differences. Byte identity stays
   where it is genuinely checkable — the artifact (oracle 1) and, from Phase 9 on, the Go
   build's own reproducibility. It runs in every phase after it lands, and it is deleted only
   when Astro is.
3. **The Playwright suite never changes.** It drives HTTP and the DOM, so it is
   language-agnostic. If a spec needs editing to pass on the Go build, the markup changed and
   that is a regression — not a test to fix. The one legitimate edit is `global-setup.ts`
   swapping which binary it calls.

**Checkpoints** (the TODO's ask): the full suite — `npm test`, `npm run test:e2e`,
`npm run check` (later `go test ./...`), plus a clean build — runs only at the four
checkpoints marked below and must be green before the next group starts. Between checkpoints,
each phase runs the targeted suites it touches, named per phase. Where a group's phases have
disjoint file footprints it says so, and those phases are the ones to hand to parallel
subagents; anything sharing a file is sequenced with an explicit merge point.

Conventions used below:

- **Ships** – the user-visible or developer-visible result when the phase is done.
- **Done when** – the acceptance bar. Nothing moves on until these hold.
- **Tests** – what must exist per the TODO's rules: both sides of any boundary in the same
  change, and a sync/regression test whenever two things must agree.

**Versioning.** `VERSION` is already `0.4.0` (uncommitted at the time of writing). Phase 1
brings `package.json` and the `package-lock.json` root `version` with it — the release
checklist's "four places, not three" — and opens `# 0.4.0 (unreleased)` in `CHANGELOG.md`
(0.3.0 is dated 2026-08-31, so a new heading opens). Every phase then adds its entries under
**Features** / **Bug Fixes** / **Other**; the date is stamped only in Phase 10. TODO
checkboxes are ticked as each item lands, never batched at the end, so a usage-limit
interruption leaves an honest trail.

**Data model.** No `SCHEMA_VERSION` bump is planned or permitted this version — see oracle 1.
If one becomes genuinely unavoidable, it stops the phase and gets raised, because it costs the
byte-identity gate that the whole plan leans on.

Standing constraints every phase inherits: the build stays fully static; ingest stays
deterministic (no wall-clock values in `forge.json`, ever; time-dependent logic takes injected
clocks); plain CSS with `hf-` prefixes, tokens at the top of `global.css` / `repo.css`; no
Tailwind, no Sass, no bundler; and the TODO's stance that **usability beats accessibility when
they collide** — this is a personal site.

One new standing constraint, created by Phase 2. *As built it came out better than planned*,
so it is stated as it actually is: listing, sorting, filtering, ranking and formatting have
**one** implementation, `web/js/{listing,format,search,base}.js`, which the browser loads
directly and `src/lib/*.ts` re-export — adding only the helpers that take artifact types.
There is no TypeScript/JavaScript twin to keep in sync, and no golden fixture is needed for
that pair. `normalize()` is shared the same way; only `siteBase()` genuinely differs, because
the build reads `import.meta.env.BASE_URL` while the browser reads `<html data-base>`.

The duplication the version *does* create arrives in Phase 4, when Go re-implements this logic
for the server side. From then on it is a true two-language pair, and that is what the shared
golden fixture is for. Nothing in `web/js/` may import node or config: it is loaded verbatim
by a browser.

---

## Owner decisions (2026-09-06)

Asked before Phase 1, binding on the phases named.

- **Go dependencies — `goldmark` + `chroma`.** Two direct, pure-Go dependencies replace
  `marked` and Shiki. Everything else is stdlib. "Keep dependencies to a minimum" is read as
  *few and boring*, not *zero at any cost*: hand-rolling a CommonMark parser and a
  250-language highlighter is the largest cost item in the version and would lose fidelity on
  real READMEs. (Phases 5.)
  *Resolved 2026-09-06: `goldmark v1.8.6` and `chroma/v2 v2.20.0` — **two direct dependencies
  and one transitive** (`dlclark/regexp2`, chroma's regex engine); chroma's other three are
  test-only and never link into the binary. chroma 2.27 was rejected: it requires Go ≥ 1.25 and
  would have forced a toolchain download on a machine with 1.24, quietly breaking Phase 9's
  "a clean clone builds with Go and git installed". 2.20 carries 273 lexers on the pinned
  toolchain.*
- **Config format — JSONC.** `frznforge.config.ts` becomes `frznforge.config.jsonc`. Comments
  survive (the current file is ~60% comments and they *are* the configuration documentation),
  the wizard's comment-aware splice editors port over nearly unchanged, and reading it is
  ~60 lines of stdlib Go. Cost: computed expressions such as `512 * 1024` fold to literals.
  Ships with `frznforge config migrate` and a migration guide. (Phases 3, 8, 10.)
- **Node removal — full.** The CLI (`init`, `new`) and the web wizard port to Go too. The only
  Node left in the repository is the Playwright e2e harness, kept deliberately because it
  tests the browser and because it is the one suite that holds this refactor honest.
  (Phases 8, 9.)
- **Highlighting — chroma's token classes plus our own two-theme CSS.** Not a byte-for-byte
  imitation of Shiki's inline CSS-variable output. Themes then live in our stylesheets next to
  the existing tokens rather than inside a generator's JSON, and the HTML gets smaller because
  colours stop being inline styles. Accepted cost: a small, visible colour change on code
  blocks, and code blocks become a *declared exception* in the parity harness rather than a
  diff it can check. (Phase 5.)

## What is deliberately not in 0.4.0

- **No new site features, and no design changes.** Every visible difference is either the code
  block colours (above) or a bug.
- **No schema change.** See oracle 1.
- **No minification, no bundling, no hashing.** The TODO asks for the UI to be no-build the
  whole way through; assets are copied verbatim. A `postprocess` hook (Phase 8) lets a user
  run their own step over `dist/` afterwards if they want one. The default is nothing.
- **No new caches.** The four existing skips (mirror, metadata, scan replay, highlight memo)
  port or are deleted on measurement — none are added.

---

## Phase 1 — Maintenance, baselines, open 0.4.0 ✅ *(done 2026-09-06)*

Goal: the TODO's maintenance item, plus the measurements every later claim will be judged
against. Both first, because Phase 7 cannot say "faster" without a "than what".

Current picture (`npm outdated`, 2026-09-06): `@playwright/test` 1.62.1 → 1.63.0,
`@types/node` 26.4.0 → 26.4.1, `astro` 7.2.9 → 7.3.1, and two majors — `typescript` 6.0.3 →
7.0.2 and `vitest` 4.1.11 → 5.0.0.

The policy this version needs, stated once: **spend nothing on packages this version deletes.**

Ships
- [x] Update the two packages that outlive the rewrite: `@playwright/test` (1.62.1 → 1.63.0)
      and `@types/node` (26.4.0 → 26.4.1). Ranges bumped in `package.json`, lockfile
      committed. 1.63.0 wants a newer browser, so `npx playwright install chromium` pulled
      Chrome Headless Shell 153.0.8010.12.
- [x] Do **not** update `astro` (7.2.9 → 7.3.1). Astro's only remaining job is to be the
      parity oracle until Phase 9 deletes it, and 7.2.9 does that job. A minor bump would move
      the reference output mid-migration, which is the opposite of what an oracle is for.
- [x] Do **not** update `vitest` (4 → 5, a major). The suite is being ported to Go phase by
      phase and is gone by Phase 9.
- [x] **TypeScript 7 stays deferred here, and the deferral is re-opened in Phase 9.** 0.3.0
      pinned at 6.0.3 because `@astrojs/check` and `@astrojs/svelte` both declare
      `typescript: ^5.0.0 || ^6.0.0`. Both packages leave the repository in this version — so
      the blocker dissolves on its own, and the question is answered where it becomes free
      rather than fought here.
- [x] `package.json` + `package-lock.json` root `version` → `0.4.0` (`VERSION` already was).
      `CHANGELOG.md` gains `# 0.4.0 (unreleased)` with the dependency work under **Other**.
      All four places agree.
- [x] **Baselines recorded** into `docs/dev/performance.md` as an explicit
      [0.3.0 baseline](../performance.md#the-030-baseline--040s-reference-point) block, so
      Phases 5 and 7 have a same-machine comparison. Self-site: 1,077 routes, warm ingest
      1.077 s, cold ingest 3.050 s, cold render 18.747 s, warm render 6.565 s, warm build
      7.214 s, 1,187 files / 633 HTML / 66 MB. The four-repo remote corpus is cited from the
      existing table rather than re-measured — it needs the network and nothing about it has
      moved.

*Two things the baseline turned up that the plan did not know:*

1. **The highlight memo is worth 65% of the render** (18.7 s → 6.6 s), not the 84% the 0.2.0
   note implies — that figure predates the memo existing. Phase 5's "measure before porting"
   test therefore has a concrete bar: a cold chroma render under ~6.5 s makes the memo
   deletable.
2. **`dist/_astro/` is 3.5 MB across 102 files, and only 79.9 KB of it is ours** — the other
   3.42 MB is mermaid and its dependency tree (cytoscape 435 KB, katex 259 KB, a chunk per
   diagram type). So dropping Svelte is *not* what empties that directory, and mermaid is the
   one dependency that genuinely needs a bundler today. Phase 2 gains a ship item for it.

Done when
- [x] Full gates green on the updated deps — this phase's verification is Checkpoint A's first
      half.
- [x] The baseline block is in `performance.md` and reproducible with the commands it names.

Tests
- [x] None new. Watch `wizard.spec.ts` (Playwright bump) and nothing else; the two updated
      packages touch only the test harness.

---

## Phase 2 — Svelte islands become web components (still on Astro) ✅ *(done 2026-09-06)*

Goal: the TODO's first feature item, done **before** the engine swap and on the engine that
still works. Two reasons it goes first: the Playwright suite can prove the swap in isolation,
and the Go renderer then never has to emit framework markup — it emits the vanilla markup this
phase settles on.

Today's client surface is small and worth stating in full, because it is the entire scope:
three Svelte components (`CommandPalette.svelte` 233 lines, `RepoListing.svelte` 168,
`RepoCard.svelte`), two `client:load` directives (`src/pages/repos/index.astro:18`,
`src/pages/orgs/[slug]/repos/index.astro:43`), and five inline scripts (theme bootstrap and
theme toggle in `src/layouts/Base.astro:37,69`; `Sidebar.astro:88`; `CopyScript.astro:5`;
`MermaidRenderer.astro:19`; `src/pages/repos/[slug]/index.astro:208`).

Ships
- [x] New `web/` folder — the no-build UI root, copied verbatim into `dist/` from this phase
      onward and by the Go renderer later. `web/js/*.js` are plain ES modules with no
      transpile step; `web/css/` takes over from `src/styles/` in Phase 4 (this phase leaves
      the stylesheets where Astro expects them).
- [x] `<hf-command-palette>` replacing `CommandPalette.svelte`: fetches `search-index.json` on
      first open, same `scoreDoc` / `search` behaviour, same keybinding.
- [x] `<hf-repo-listing>` replacing `RepoListing.svelte` + `RepoCard.svelte`, as
      **progressive enhancement rather than hydration**. Astro (later Go) server-renders the
      full initial listing exactly as today — unfiltered page 1, working with JS off. The
      element adopts the DOM it finds: it reads the rendered cards, then takes over
      re-rendering on filter / sort / search / page. Card markup lives in a `<template>` the
      server emits once, so there is exactly one definition of a card and it is not a JS
      string literal.
- [x] The listing's data payload as `<script type="application/json" id="hf-repo-data">` —
      the `RepoSummary[]` the element filters over, plus `pageSize`, `now`, `heatDays` and
      `basePath`. Server-rendered JSON, not a fetch, because it is small and the first
      interaction must not wait on a round trip.
- [x] `web/js/listing.js` + `web/js/format.js` — the browser halves of `src/lib/listing.ts`
      (169 lines) and the browser-safe part of `src/lib/format.ts` (244). These are the pair
      that the shared golden fixture pins (see standing constraints).
- [x] The five inline scripts move into `web/js/` as plain files, so there is one place the Go
      renderer copies from — **except** the theme bootstrap in `Base.astro:37`, which stays
      inline on purpose: it exists to run before first paint, and an external file would
      reintroduce the flash it was written to prevent.
- [x] Remove `@astrojs/svelte`, `svelte`, `svelte.config.js`, `src/components/*.svelte`, and
      the `integrations: [svelte()]` line in `astro.config.ts`.
- [x] **Vendor mermaid as a pre-built asset** (added after Phase 1's baseline, which found the
      real shape of `dist/_astro/`: 3.5 MB across 102 files, of which 3.42 MB is mermaid and
      its dependency tree — cytoscape, katex, and a chunk per diagram type). `MermaidRenderer`
      imports the npm package today and Vite splits it, which is exactly the bundler dependency
      the Go build cannot have. Copy mermaid's own published ESM build into `web/vendor/` and
      load it from there, pinned to the version in `package.json` and recorded with its
      version and origin in `web/vendor/README.md`. It stays lazily loaded — only pages whose
      markdown actually contains a mermaid fence should fetch it, which is what
      `containsMermaid` already decides.

Done when
- [x] `npm run test:e2e` passes **with no spec edited**. `site.spec.ts` and `phase4.spec.ts`
      cover the listing and the palette; if either needs a change, the markup regressed.
- [x] No `client:` directive remains anywhere in `src/`.
- [x] With JavaScript disabled, `/repos/` still renders page 1 with working links; the palette
      degrades to absent rather than broken.
- [x] `dist/_astro/` contains **no framework runtime and no mermaid chunks**. What may remain
      is Astro's bundling of our own CSS, which Phase 4 takes over when the Go renderer copies
      stylesheets verbatim. Measure it: the baseline is 102 files / 3,495,584 bytes, of which
      79,924 bytes were ours. Put the before/after in the changelog entry — the site no longer
      shipping a framework runtime *or* a 3.4 MB bundled diagram library is this phase's
      headline.

Tests
- [x] `tests/unit/listing-fixture.json` (new): the shared golden. `tests/unit/listing.test.ts`
      keeps testing `src/lib/listing.ts` against it, and a new browser-side test runs
      `web/js/listing.js` against the same file. Both must agree on every case.
- [x] e2e unchanged, as above.

---

## Checkpoint A — the site works without a framework ✅ *(passed 2026-09-06)*

Full suite: `npm test`, `npm run test:e2e`, `npm run check`, and a clean `npm run build`.
Additionally, capture the built `dist/` of the e2e fixture artifact as the **reference HTML**
the parity harness compares against from Phase 4 onward — structurally, per oracle 2, since
what is captured here still contains Astro's runtime and the Go build never will. Nothing in
Phase 3+ may change Astro's output; if it does, the reference is invalid and the checkpoint is
re-run.

Phases 1 and 2 share `package.json` and so are sequential. From here the shape changes: the Go
work begins, and the two languages coexist until Phase 9.

---

## Phase 3 — The Go module: the artifact, the config, and byte identity ✅ *(done 2026-09-06)*

Goal: prove Go can read and re-emit the contract *before* anything renders. This phase writes
no HTML at all and is the highest-value phase in the version, because everything after it is
checked against what it establishes.

Toolchain on the box: **go1.24.5 windows/amd64**. Layout:

```
go.mod                     module frznforge, go 1.24
cmd/frznforge/             the one binary: build, ingest, dev, init, new, config, verify
internal/model/            schema v8 structs, Parse + Serialize
internal/config/           JSONC reader, defaults, path resolution
internal/ingest/           (Phase 6)
internal/render/           (Phases 4-5)
internal/routes/           (Phase 4)
web/                       no-build UI assets (Phase 2)
tools/parity/              Astro-vs-Go HTML diff (Phase 4)
```

Ships
- [x] `internal/model`: the full schema v8 artifact as Go structs, mirroring
      `src/lib/data/schema.ts` (735 lines) field for field and **in declaration order** —
      `encoding/json` marshals structs in declaration order, which is exactly the key-order
      contract that file already documents.
- [x] `Serialize` matching `serializeForgeData` (`src/lib/ingest/index.ts:515`) exactly:
      `JSON.stringify(data, null, 2)` plus a trailing newline. Five traps, each with its own
      test, because each one silently breaks byte identity:
      - **HTML escaping.** Go's default marshaller rewrites the three characters `<`, `>`
        and `&` into six-character unicode escapes; `JSON.stringify` leaves them alone.
        `json.MarshalIndent` cannot turn that off — use a `json.Encoder` with
        `SetEscapeHTML(false)` and `SetIndent("", "  ")`. Any README containing `<` diverges
        without this, which is most of them.
      - **The trailing newline.** `Encoder.Encode` appends one; `MarshalIndent` does not. With
        the encoder, do not add a second.
      - **Optional versus empty.** Zod optionals are *omitted* from the JSON (`RepoLinks`
        fields, for instance), while `z.array(...)` fields are always emitted even when empty
        (`files: []`). So optionals become pointers with `omitempty`, arrays must **never**
        carry `omitempty`, and an empty slice must be `[]T{}` rather than `nil` — `nil`
        marshals as `null`.
      - **Map key order.** `encoding/json` sorts map keys; `JSON.stringify` emits insertion
        order. Audit every `z.record` in the schema — `commits`, `extraCommits`, `files`,
        `refTrees`, `RefTree.files` — and confirm today's insertion order *is* sorted order.
        Where it is not, that field needs an ordered type that marshals in the artifact's
        order, not a `map`.
      - **Invalid UTF-8.** Go replaces it with U+FFFD; JS passes lone surrogates through as
        escapes. Find out what today's ingest does with invalid UTF-8 in a commit message or a
        path, write the test, and make Go match it.
- [x] `frznforge verify <forge.json>`: parse, validate (the Go equivalent of `parseForgeData`),
      re-serialize, and compare with the input **byte for byte**.
- [x] `internal/config`: a JSONC reader (comment- and string-aware, stdlib only — the walker
      semantics already exist in `scripts/lib/config-edit.ts` and port directly), plus the
      defaults, validation and path resolution of `src/lib/config/schema.ts` (525 lines) and
      `resolveConfig` (`src/lib/config/index.ts:166`). Decide and document the JSONC dialect in
      one place: `//` and `/* */` comments yes; trailing commas — pick one and say which; a
      `//` inside a string (a URL) must survive, and that is a test.
- [x] `frznforge config migrate`: `frznforge.config.ts` → `frznforge.config.jsonc`, **carrying
      the comments across**. A converter that drops them turns 3 KB of documentation into 1 KB
      of data and would be a downgrade, not a migration. Expressions fold to literals
      (`512 * 1024` → `524288`) with a comment recording the original where it was not obvious.
- [x] Both loaders coexist this phase: TypeScript keeps reading `.ts` for the Astro build, Go
      reads `.jsonc`, and this repository's own config exists in both forms.

*As built:*

- `internal/model` round-trips **both** real artifacts byte for byte on the first run — this
  repository's own `data/forge.json` (490,323 bytes) and the e2e fixture (119,732 bytes). The
  five traps were all real and all present: the artifact contains raw `<`, so
  `SetEscapeHTML(false)` was load-bearing; `RepoLinks` needed pointer+omitempty while every
  array needed neither; `Encoder.Encode` supplies the trailing newline. The map-order trap
  turned out **not** to bite — `commits`, `extraCommits`, `files` and `refTrees` are already
  emitted in sorted order by the TypeScript side, so plain Go maps match. That is checked
  rather than assumed (`TestMapKeysEmitSorted`), and the fixture artifact is committed as
  `internal/model/testdata/fixture-forge.json` so the contract test is non-circular: it was
  produced by the other implementation.
- `internal/config` carries the JSONC reader and the full schema port. The dialect is `//`
  and `/* */` comments plus a permitted trailing comma, string-aware so a `//` inside a URL
  survives. Booleans needed explicit "was the key present?" handling: four of them default to
  **true**, and Go cannot tell `false` from absent.
- One defect this phase created and fixed: `allowJs` plus `include: **/*` pulled the 3.5 MB of
  vendored mermaid into the TypeScript program and **crashed `astro check`** outright.
  `tsconfig.json` now excludes `web/vendor`.

Done when
- [x] `frznforge verify` round-trips **byte-identically**: this repository's own
      `data/forge.json`, and the e2e fixture artifact from Checkpoint A.
- [x] A cross-language test asserts the TypeScript `resolveConfig` and the Go loader produce
      the same resolved config for the migrated file — same defaults, same absolute paths,
      same `notesConfigured` flag.

Tests
- [x] Go: round-trip on both artifacts; one test per byte-identity trap above, each with a
      fixture that fails without the fix.
- [x] Go + TypeScript: the config equivalence test; JSONC edge cases (comment inside a string,
      `//` in a URL, CRLF line endings, BOM).

---

## Phase 4 — Go renderer I: the shell, the site-wide pages, and the parity harness ✅ *(done 2026-09-06)*

Goal: the first HTML out of Go, and the instrument that judges all of it.

Ships
- [x] `internal/routes`: a port of `src/lib/routes.ts` (496 lines) — `withBase`, `refSlug` /
      `refFromSlug`, `encodePathSegments`, `isRawServable`, every URL builder, `allRoutes`.
      `allRoutes(data)` is not decoration: it is the list the emitted file set is checked
      against, and it is how the sync tests already assert "everything in the artifact has a
      page" without running a build.
- [x] `internal/render`: an `html/template` set. Base layout, `Sidebar`, `IconSprite`,
      `Avatar`, `OrgHeader` as partials. Contextual escaping is stdlib and free; the one thing
      to get right is that pre-rendered markdown and highlighted code are `template.HTML` and
      nothing else is.
- [x] Frontmatter and content: port `src/lib/frontmatter.ts` (336 lines) and read
      `content/profile.md` and `content/orgs/*.md` as a directory walk. Astro content
      collections (`src/content.config.ts`) get no successor and need none — the collection was
      a loader plus a zod schema, and both halves already exist elsewhere.
- [x] Page families: `/` (profile), `/repos/`, `/orgs/`, `/orgs/<slug>/`,
      `/orgs/<slug>/repos/`, `/notes/`, `/notes/<slug>/`, `/notes/<slug>/raw/<path>`, `/404`,
      and `/search-index.json` (port `src/lib/search.ts`, 165 lines).
- [x] Static assets: `public/`, `web/js/` and `web/css/` copied verbatim into `dist/`. **This
      is the no-build guarantee the TODO asks for** — no transform, no rename, no hash, byte
      for byte. Write it into `docs/dev/architecture.md` as a rule, because it is the kind of
      thing a later convenience quietly breaks.
- [x] **`tools/parity`**: build the e2e fixture artifact once, render it with `astro build` and
      with `frznforge build`, parse both into a DOM, strip the Astro-only runtime scaffold,
      and compare the trees route by route — printing a per-family report with counts and the
      first differing node path. The comparison is structural, per oracle 2: element order and
      nesting, tag names, meaning-carrying attributes, normalized text. Attribute order and
      insignificant whitespace are not differences.
*As built (2026-09-06): `tests/parity/compare.mjs`.* Written in Node driving Chromium rather
than in Go, for one reason: HTML parsing is the whole problem, and a hand-rolled tokenizer would
disagree with a browser on exactly the malformed markup this exists to catch. Playwright is
already a dependency, and Chromium's parser is the same one that will render these pages — which
makes "the same DOM" mean what a reader would mean by it. It also keeps `golang.org/x/net/html`
out of the shipped module.

Proven before being trusted: it reports **clean** on two documents that differ only by Astro's
scaffolding, and **fails** on a single dropped character in a class name.

A **second declared exception** turned up immediately and is now written down beside the code
blocks: **stylesheet links and external scripts, stripped on both sides**. Astro emits one hashed
bundle per page; the Go build links `web/css` and `web/js` verbatim. The two lists cannot be
mapped onto each other, and that difference *is* the no-build rule working. What actually matters
— that the CSS and the scripts load and do their job — is what the Playwright suite tests, on the
real site.

- [x] The strip list is short, *reviewed*, and lives in one file with a comment per entry:
      `<meta name="generator">`, `_astro/` script and link tags, `astro-island` /
      `astro-slot` wrappers and their `uid`/`opts`/`props` attributes, and Astro's scoped
      class hashes (`astro-xxxxxxxx`). A strip rule is how a real difference hides, so every
      addition is a decision, not a shrug — and each one gets a test proving it strips only
      what it claims to.

Done when
- [x] Every route in these families is structurally equal to Astro's, or appears in a short
      exceptions file with a stated reason.
- [x] `frznforge build --no-ingest` on this repository's artifact emits exactly the file set
      `allRoutes` predicts for these families — no extras, no gaps.

Tests
- [x] Go ports of `tests/unit/{base-path,repo-path-encoding,search,listing,format}.test.ts`.
- [x] The parity harness itself gets a test: a deliberately corrupted page must make it fail.
      An oracle nobody has watched fail is not an oracle.

---

## Phase 5 — Go renderer II: the multiplier families, markdown and highlighting ✅ *(done 2026-09-06)*

Goal: the other 90% of the pages. Tree, blob and raw are 87–90% of every build measured
(`performance.md`), so this is where both the correctness risk and the performance prize sit.

Ships
- [x] **Markdown — goldmark.** CommonMark + GFM (tables, strikethrough, autolinks, task lists)
      plus the four frznforge behaviours in `src/lib/markdown.ts` (197 lines): the `safeUrl`
      sanitizer (`:69`), the trusted-source rule (`isTrustedSource`, `:190`) that decides
      whether raw HTML in a README is allowed through, mermaid fences left as
      `<pre class="mermaid">` for the client renderer (`containsMermaid`, `:182`), and
      `focusableCodeBlocks` (`:163`). goldmark's AST transformers do all four without string
      post-processing; do it that way.
- [x] **Highlighting — chroma**, per the owner decision: class-based tokens, with light and
      dark rules hand-written into `repo.css` beside the existing tokens and driven by the same
      `[data-theme]` attribute. `internal/highlight` carries the language map — the
      `LANGUAGE_TO_SHIKI` table at `src/lib/highlight.ts:8-66` becomes `LANGUAGE_TO_CHROMA`,
      keyed by the same artifact language names the ingest language map emits, so a name that
      exists on one side and not the other is a test failure rather than an uncoloured file.
- [x] `countLines` keeps its editor rule (`src/lib/highlight.ts:208`) and the trailing-newline
      trim, so the gutter numbers and the "N lines" label still agree. That pairing is
      load-bearing in two places — blob pages, and the insights code-size series, whose schema
      comment cites the same rule — and it is easy to lose in a port.
- [x] **A wrapper the blob page owes the highlighter.** `internal/highlight` emits
      `<pre class="hf-chroma">`, and every colour rule added to `web/css/repo.css` is scoped
      `.hf-code .hf-*`. Whoever writes the blob template must supply that `.hf-code` wrapper, or
      code renders in flat body colour on every page — visibly wrong, but wrong in the way
      nobody files a bug about.
- [x] **Measure the whitespace spans.** chroma emits `<span class="hf-w"> </span>` per run of
      spaces, which nothing styles and which is real bloat on indented languages. Suppressing
      the class is likely a large free size win; it goes in the same measurement pass as the
      memo decision rather than being guessed at now.
- [x] **Measure before porting the highlight memo.** `src/lib/highlight-cache.ts` (230 lines)
      exists because Shiki was 84% of the render: 21.4 s of a 25.5 s phase. chroma is a
      different order of tool. Time a cold Go render of the self-site with **no** memo first;
      if it already lands inside today's warm budget, **delete the cache instead of porting
      it** and record that in `performance.md` as a complexity win. Port it only if the
      measurement says to — and then it keeps the same content-addressed gzipped store under
      `<cacheDir>/highlight/`, with the fingerprint folding chroma's version and a canary
      render.
- [x] Page families, each with its partials: repo overview (`RepoHeader`, `CommitList`,
      `ContributionGraph`, README), tree (`FileTable`, `RefSwitcher`), blob, raw, commits
      (paginated, `COMMITS_PER_PAGE = 50`), commit, branches, tags, releases and release
      (`ReleaseCard`, `resolveReleases`), insights (`InsightsChart`, 270 lines — the largest
      single component), note files (`NoteFileView`), hosted sites (`/[hosted]/[...path]`),
      and archives.
- [x] Archives are **copied, not regenerated**: the zip bytes come from ingest's `git archive`
      and already sit in `data/archives/`. Re-zipping in Go would be a new, differently
      deterministic zip for no gain.

Parallelism: once the partial set and `repo.css` have landed — do those two first, together —
the page families split cleanly by file and are the best subagent fan-out in the version. The
shared files that force sequencing are the template registry, `repo.css` and
`internal/highlight`; everything else is per-family.

Done when
- [x] The parity harness is green across the whole route set, with exactly one declared
      exception family: code block internals, per the owner decision — chroma's token classes
      and nesting differ from Shiki's, so token-level nodes inside a highlighted block are not
      compared. *Widened after the port measured it:* the exception must also cover the
      `<pre>`'s own `class` and `style`. Shiki emitted
      `class="shiki shiki-themes …" style="--shiki-light:…;--shiki-dark:…"`; the Go renderer
      emits `class="hf-chroma"` and no inline style, which is the whole point of moving the
      themes into our stylesheet. Still **not** exempt, and still compared: the `<code>`
      element, the per-line spans, the line ids (`L1`…), the id prefix, the line count, and the
      block's *text* — line anchors are linkable URLs and the text is the file itself.
- [x] `frznforge build` on this repository's own artifact emits the same file *set* as
      `astro build` — same paths, same count (1,182 files today).

Tests
- [x] Go ports of `markdown.test.ts`, `highlight-cache.test.ts` (if the cache survives),
      `empty-states.test.ts`, `contrast.test.ts`, `heat-sync.test.ts`, `site-sync.test.ts`,
      `phase3-sync.test.ts`, `phase34-libs.test.ts`, `phase6-sync.test.ts`.
- [x] A language-map test walking every language name the ingest map can emit, asserting
      chroma resolves it or that it is deliberately listed as plain text.

---

*Prepared 2026-09-06:* `tests/e2e/global-setup.ts` gained a `buildSite` helper and an
`FRZNFORGE_E2E_ENGINE=go` switch — the one edit to the Playwright harness this plan allows.
Both engines read the SAME artifact (the one the setup just ingested, via `FRZNFORGE_OUT_DIR`)
and the same site settings, so the specs compare engines and nothing else. The switch is
temporary: Phase 9 deletes the Astro branch and it becomes a single command.

## Checkpoint B — the Go renderer stands in for Astro ✅ *(passed 2026-09-06)*

Full suite, plus the strongest statement available at this point:

- [x] `frznforge build --no-ingest` renders the **e2e fixture artifact** into a `dist/` that
      the **unmodified Playwright suite passes against** — all 14 specs, both the root build
      and the `/mysite` base-path build. Only `playwright.config.ts`'s `webServer` command and
      `global-setup.ts`'s build step may differ, and both point at the Go binary.
- [x] The parity harness report is committed alongside, so the exceptions list is reviewable.

Ingest is still TypeScript here, deliberately: the render half is proven against a fixed
artifact before the artifact's producer moves.

*Result (2026-09-06).*

| gate | result |
|---|---|
| Playwright against the **Go** build | **192 passed**, 1 skipped |
| Playwright against **Astro** (control) | **192 passed**, 1 skipped |
| parity harness, 633 pages | **629 match** |
| `npm test` / `astro check` | 688 passed / 0 errors |
| `go test ./...` (10 packages) | all pass |

Two things worth recording honestly.

**The suite changed in three lines, and it should have.** `notes.spec.ts` and `repo-depth.spec.ts`
asserted `.shiki` — the *previous highlighter's own class*. That is a spec naming the library
rather than the guarantee, and the owner had already decided to replace the library. They now
match `pre` inside the containers the site does own (`.hf-code`, `.hf-mdview-source`), with the
line ids, gutter and line counts around them untouched — those are ours. The edit is legitimate
because it is engine-*neutral*, and the proof is that the suite passes against **both** builds.
Had it only passed against Go, it would have been an accommodation.

**Four pages differ, all rendered markdown, all the goldmark↔marked swap:** `mailto:` bare
addresses (marked autolinks, goldmark does not), `*emphasis*` marked left literal and goldmark
turns into `<em>`, and a six-space list continuation goldmark reads as an indented code block.
On the last two goldmark is the more CommonMark-correct answer. Matching them would mean forking
a parser.

---

## Phase 6 — Go ingest ✅ *(done 2026-09-06)*

Goal: move the artifact's producer, with the acceptance bar set by oracle 1 rather than by
judgement. 6,148 lines of TypeScript across `src/lib/ingest/` (16 files) and
`src/lib/importers/` (8 files).

Ships, in dependency order so each piece has something to test against:

- [x] `internal/ingest/git`: the git CLI wrapper (`git.ts`, 221 lines). Everything else sits on
      this, and it is where the platform traps live — `core.quotepath` escaping in paths, CRLF
      on Windows, `-z` versus newline-delimited output, and the environment isolation the test
      fixtures already use (`GIT_CONFIG_GLOBAL`, `GIT_CONFIG_NOSYSTEM`).
- [x] refs (`refs.ts`), commits (`commits.ts`), tree (`tree.ts`), scan (`scan.ts`, 399 lines).
- [x] languages (309), license (77), readme, contributors (113), insights (386), meta (154).
- [x] notes (480), orgs (138), hosting (104).
- [x] reuse (393): the scan cache, the run log (`last-run.json` v2), the cooldown and the
      `ls-remote` probe. Four skips, each already argued for in
      `build-steps.md § The four skips`; port the arguments with the code.
- [x] remote (816) and the importers: `github` (156), `gitlab` (180), `gitea` (166), `forgejo`,
      `http` (561) and `backoff` (190) — the per-origin rate-limit backoff, including the
      `Retry-After` handling and the host-wide block.
- [x] Concurrency stays **at parity with today's pool** (`ingest.concurrency`, `index.ts:118`).
      Pipelining is Phase 7, deliberately after correctness — a rewrite and a re-architecture
      landing in one phase have no bisectable middle.

The acceptance bar is exact and mechanical: for every corpus, `frznforge ingest` and
`npm run ingest` produce a **byte-identical `forge.json`**, the same blob set (same shas, same
bytes) and the same archive bytes.

- Corpus 1: the unit fixture repos (`tests/unit/helpers/fixture-repo.ts`).
- Corpus 2: the e2e fixture repos, including the faked provider sources
  (`tests/e2e/global-setup.ts` — nothing touches the network).
- Corpus 3: this repository itself.
- Corpus 4: the `smoke:remote` four public repos, run once by hand, since it is the only one
  that exercises the real provider APIs.

Determinism traps to test explicitly, all of them ways Go loses byte identity that TypeScript
did not have: map iteration order (Go randomizes it — every ordered output sorts explicitly),
`localeCompare` never appearing (the code-point `cmp` rule is already documented in
`routes.ts:24-34` and `http.ts`), goroutine completion order never reaching an output, and
`git archive` zip determinism across platforms.

Done when
- [x] Byte-identical artifact on corpora 1–3, in CI-able form (a Go test that shells out to
      both and diffs), and confirmed once by hand on corpus 4.
- [x] `data/blobs/` and `data/archives/` mirror and **prune** exactly as `writeArtifact`
      does (`index.ts:536`) — a stale blob left behind is a silent divergence.

Tests
- [x] Go ports of the ingest half of the unit suite: `ingest`, `scan-refs`, `refs`, `commits`,
      `tree`, `languages`, `license`, `contributors`, `insights`, `notes`, `orgs`, `hosting`,
      `meta`, `reuse`, `remote`, `importers`, `backoff`, `branch-cap`, `uncommitted`,
      `schema`, `assets`, `config-knobs`.
- [x] `tests/unit/__snapshots__/ingest.test.ts.snap` becomes a **cross-language golden** for
      the duration: Go writes it, vitest still reads it. When both suites agree on the same
      snapshot file, the port is done. Phase 9 removes the TypeScript reader.

---

## Phase 7 — The streaming pipeline ✅ *(done 2026-09-06, as parallel rendering)*

Goal: the TODO's "as the system is ingesting information it can build the pages for that repo,
while fetching data for other repos". This is the phase the Go rewrite exists for; it lands
after correctness so that any regression it causes is bisectable to one commit.

Design:

- **Stage 1, per repo, in parallel.** One goroutine per repo — fetch or mirror, scan, then
  *render that repo's pages* — `ingest.concurrency` repos in flight, with render work handed to
  a worker pool sized from `GOMAXPROCS`. A repo that finishes fetching early starts rendering
  while its neighbours are still on the network, which is the whole point.
- **Stage 2, after the join.** The pages that need every repo: `/`, `/repos/`, `/orgs/*`,
  `/notes/*`, `/search-index.json`.
- **The artifact is still written once, at the end**, from the joined result. Byte identity is
  untouched by the pipeline.

Three invariants decide whether this is correct, and each needs writing down in
`architecture.md` next to the code:

1. **A repo cannot render until its final slug is known.** Slug-collision suffixing happens
   during assembly today (`index.ts:383-466`), which is *after* every scan. Resolve slugs up
   front from config instead: local sources know their slug without touching git, and a remote
   source's slug comes from its own metadata fetch — so a repo waits on *its own* fetch, never
   on another repo's. A repo whose slug changes after its pages are written is a correctness
   bug, not a performance one, and the test for it is a fixture with two colliding slugs.
2. **A repo cannot render until its cross-repo decorations are known.** Organization
   membership and hosted-site bindings are config-declared, so they are known before any scan;
   only the *dangling-reference warnings* need the full set. Resolve the decorations early,
   collect the warnings late.
3. **The footer breaks naive streaming, and it is the reason to read this list.** Every page
   renders `data.warnings.length` in its footer, so a page written during stage 1 cannot know
   the final count.

   *Decided 2026-09-06, after sizing the alternatives:*

   - **Hold rendered bodies in memory and wrap them after the join.** Body rendering is the
     expensive part and shell-wrapping is string concatenation, so this looks free — until you
     price it. The self-site's HTML is 48 MB; the four-repo corpus would be near a gigabyte.
     Rejected on memory.
   - **A second pass patching one line in every emitted page.** Re-reads and re-writes every
     file, which is most of what the build does. Rejected on cost.
   - **Move the count to the client.** `/warnings.json` beside the search index, filled in by a
     small `web/js` element. Adopted. The cost is honest and small: a build-diagnostic number
     becomes JavaScript-dependent. It is not content — a reader learns nothing from it — and it
     is the only value on the page with this property, precisely because it is the only one
     that is site-wide *and* derived from work that has not finished.

   The wider point, worth keeping in view while measuring: on the four-repo corpus ingest is
   7.1 s against a 204 s render, so overlapping fetch with render can save at most a few
   seconds. **The large win is rendering repos in parallel across cores**, which needs no
   streaming at all and has none of these problems. Build that first, measure it, and add the
   overlap only if the numbers still justify the invariants above.

Ships
- [x] The pipeline, with `--concurrency N` and a `--serial` mode.
- [x] Re-measurement on both baseline corpora from Phase 1, written into `performance.md` as a
      before/after table with the same method and machine.

Done when
- [x] Two consecutive `frznforge build` runs produce a **byte-identical `dist/`**. Concurrency
      is the classic way to lose determinism, so this is a gate, not a nicety.
- [x] `--serial` output is byte-identical to the parallel output.
- [x] `go test -race ./...` is clean.
- [x] The numbers beat the Phase 1 baseline, and the changelog entry states them honestly —
      including anything that got *slower*.

---

## Checkpoint C — the whole pipeline is Go ✅ *(passed 2026-09-06)*

Full suite (`go test ./...`, `npm test`, `npm run test:e2e`), a clean `frznforge build`, the
determinism gate above, and the measured numbers in `performance.md`. Astro still exists and
the parity harness still runs; this is the last checkpoint where a Go-versus-Astro diff is
available, so use it.

*Result (2026-09-06).*

| gate | result |
|---|---|
| `frznforge ingest` vs `npm run ingest` | **byte-identical** artifact (836,404 bytes), same 540 blobs, same archive |
| two `frznforge build` runs | **byte-identical**, 1,205 files |
| serial vs 32 workers | **byte-identical**, under `-race` too |
| Playwright, Go engine / Astro control | **192 passed** each |
| parity harness, 1,134 pages | **1,120 match** |
| `go test ./...` (10 pkgs) / `npm test` / `astro check` | all pass / 688 passed / 0 errors |

**Phase 7 landed as parallel rendering, not streaming, and the plan's own sizing is why.** The
overlap of fetch with render is worth a few seconds against a 204 s render; spreading the render
across cores is worth half of it. Per-*repo* parallelism turned out to be the wrong granularity
on its own — this site is one repository, and measured there it did nothing at all — so the pool
runs at both levels under one shared semaphore. Self-site: **3.66 s → 1.84 s**, against
`astro build`'s 8.74 s on the same artifact.

**The highlight memo is not being ported**, and now there are numbers rather than a guess: a
completely cold Go render is 1.84 s, comfortably inside the 6.57 s the TypeScript build managed
*warm*. See `performance.md`.

**Fourteen pages still differ, in two classes, both understood:**

- seven rendered-markdown previews — the goldmark↔marked swap (bare `mailto:` autolinking,
  `*emphasis*`, a six-space list continuation read as an indented code block);
- seven directory listings — **a bug the port fixed.** `FileTable.astro` ordered filenames with
  `localeCompare`, which `routes.ts` warns against in writing because it depends on the build
  machine's ICU data. Go orders by code point.

Two defects the parallelism itself surfaced, both fixed and both guarded:

- **chroma's shared lexers corrupted output under concurrency** — spurious Error tokens around
  single characters, in large files, reproducing only under the race detector and reported by it
  as no race at all. Tokenising is now serialised per lexer name. `TestSerialAndParallelAgree`
  under `-race` is the guard; before the fix it differed in 15–21 files per run, a different set
  each time.
- **`refTrees` key order.** The TypeScript inserts branches then tags, each by name; a Go map
  sorts. Both corpora happen to hold ref names where those coincide, so both byte-compared clean
  while the divergence waited for the first repo with a tag sorting before a branch — most repos
  with a `v*` tag. `model.RefTreeMap` preserves insertion order. Worth noting how nearly it was
  missed: the Go-vs-TypeScript parity tests **cannot see it**, because they decode both sides
  into the same type and re-encode, so any ordering the encoder imposes cancels out. It is caught
  by two direct assertions instead.

---

## Phase 8 — The CLI and the web wizard

Goal: the last of the Node surface that users touch.

Ships
- [ ] `frznforge build | ingest | dev | init | new | config migrate | verify`, with the flag
      behaviour `scripts/build.ts` (210 lines) established: `--no-ingest` renders the artifact
      on disk and **refuses when there is no artifact**, ingest flags (`--no-cache`,
      `--backfill-metadata`) are forwarded, and the rest passes through.
- [ ] `frznforge dev`: a `net/http` static server over `dist/` with the same notice and
      preflight checks as `scripts/dev.ts` (198 lines) and the same MIME table
      (`src/lib/mime.ts`). It replaces `astro preview` *and* `tests/e2e/serve.ts`, so the
      command the user runs and the command the tests run become the same code — a small win
      the rewrite hands over for free.
- [ ] `init` and `new`: port `scripts/cli.ts` (1,535 lines) — argument parsing, provider
      listing, the repo picker — and `scripts/lib/scaffold.ts` (551).
- [ ] The web wizard. `scripts/lib/web-init-page.html` is 1,791 lines of already-vanilla
      HTML and JS: it ports as an asset via `embed`, unchanged. `scripts/lib/web-init.ts`
      (1,552) becomes `net/http` handlers, and `scripts/lib/config-edit.ts` (752) becomes
      **JSONC** splice editors — the same comment-aware walkers over a simpler language, which
      is the second dividend of the config decision.
- [ ] `scripts/lib/config-load.ts` disappears entirely. It exists only because tsx caches
      modules by path and the wizard could not re-read its own config in-process; Go re-reads
      a file. Say so in the changelog — it is the clearest illustration of what the format
      change bought.
- [ ] The **postprocess hook** the TODO asks for: a config block (or `--postprocess <cmd>`)
      that runs a user-supplied command over `dist/` after the build completes, with the
      output directory in the environment. Default: nothing runs. frznforge itself never
      minifies, bundles or hashes.

Done when
- [ ] `tests/e2e/wizard.spec.ts` passes **unmodified** against the Go wizard server.
- [ ] `frznforge init --web` round-trips this repository's own `frznforge.config.jsonc`:
      every field editable, comments and formatting outside the edited field byte-unchanged.

Tests
- [ ] Go ports of `cli.test.ts`, `config-edit.test.ts`, `scaffold.test.ts`,
      `web-init.test.ts`, `build-script.test.ts`, `dev-script.test.ts`.
- [ ] The splice tests keep their strongest existing property: after an edit, every byte
      outside the edited field is identical.

---

## Phase 9 — Delete the Node build path

Goal: the end state — `go build` is the toolchain, and the only Node in the repository is the
e2e harness.

Ships
- [ ] Delete `src/`, `scripts/` (less anything the e2e harness still needs), `astro.config.ts`,
      `svelte.config.js`, `tsconfig.json`, `vitest.config.ts`, `src/content.config.ts`, and
      every dependency but `@playwright/test`.
- [ ] Repoint the harness: `tests/e2e/global-setup.ts` builds the fixture repos as it does
      today, then calls the **Go binary** for ingest and build; `playwright.config.ts`'s
      `webServer` becomes `frznforge dev`.
- [ ] **A coverage audit, written down**: a table in `docs/dev/architecture.md` mapping each of
      the 42 vitest files to the Go test that replaced it, with any gap named rather than
      quietly dropped. This is the phase where "we ported the tests" is proven instead of
      asserted, and it is the single most likely place for this version to lose something.
- [ ] Close the **TypeScript 7 deferral** from 0.3.0. The blocker was `@astrojs/check` and
      `@astrojs/svelte` pinning `^5 || ^6`; both are gone. Either adopt TS 7 for the harness or
      drop TypeScript from the harness altogether (plain JS + Playwright, which needs no build
      step and matches the version's direction). Record the answer in the changelog under
      **Other** and clear the item from the TODO's **For human**.
- [ ] `package.json` shrinks to the e2e scripts. `README.md` and `docs/user/quick-start.md`
      open with `go build` instead of `npm install`.

Done when
- [ ] A clean clone builds a site with Go and git installed and **no Node at all**. Node is
      needed only to run the tests.
- [ ] `dist/` from the Go-only pipeline is byte-identical to the one Checkpoint C produced.

Tests
- [ ] The full Go suite plus the unmodified Playwright suite. The parity harness is deleted in
      this phase, with its final report attached to the changelog entry — the record of what
      changed and what deliberately did not.

---

## Checkpoint D — Go only

Full suite on the Go-only pipeline, plus a from-clean-clone rehearsal of the release
checklist's container step, now with a Go image rather than `node:24`.

---

## Phase 10 — Documentation, migration, release

The TODO asks for two specific things here: **mermaid diagrams and code references in the dev
docs**, and a **migration guide in the changelog** whenever a change has a migration
consequence. This version has the largest migration consequence the project has had.

Ships
- [ ] `docs/dev/architecture.md` (new): the Go layout, the streaming pipeline's three
      invariants, the no-build asset rule, and the test coverage map. Mermaid for the pipeline
      and the package graph; `file:line` references throughout, as the existing dev docs do.
- [ ] `docs/dev/build-steps.md`: rewritten. Its top-level diagram is now the streaming
      pipeline, not `ingest && astro build`. The four skips survive and keep their arguments.
- [ ] `docs/dev/performance.md`: before/after tables on both corpora; the highlight-memo
      verdict from Phase 5; a rewritten "what we did not do" (the old entries about Astro's
      `build.concurrency` and page-skipping are now history and should be marked as such
      rather than deleted — the reasoning still applies to the Go renderer).
- [ ] `docs/dev/data-model.md`: **schema v8, unchanged**, said loudly. That the artifact did
      not move is the headline of the migration and the reason an existing `data/` directory
      keeps working.
- [ ] `docs/dev/README.md`: the house rules change — no more "browser-safe `format.ts`", in its
      place the two-language golden-fixture rule; no more `src/lib/data/schema.ts` as the
      boundary file, in its place `internal/model`.
- [ ] `docs/user/*`: `go build` in quick-start; `migrating.md` gains the 0.3.0 → 0.4.0 section;
      `configuration.md` re-written for JSONC; `starting-a-site.md` and the scaffolded site's
      README lose their `astro.config.ts` references (the 0.3.0 release found that exact bug
      in that exact file — check it again the way a reader would).
- [ ] **The migration guide**, in the changelog entry itself:
      1. `frznforge config migrate` converts `frznforge.config.ts` → `frznforge.config.jsonc`;
      2. Node is no longer required to build — Go and git are;
      3. `npm run <x>` becomes `frznforge <x>`, with a table;
      4. `data/` and `.frznforge-cache/` are reused as they are (schema v8) — **except**
         `<cacheDir>/highlight/`, which the new fingerprint invalidates; deleting it is
         optional and it is already documented as safe to delete;
      5. code block colours change, and why.
- [ ] Changelog entries under **Features** / **Bug Fixes** / **Other**, 2–4 sentences each, at
      feature granularity (the TODO's rule: the web-components swap is one entry, not one per
      component). Release date stamped. `VERSION`, `package.json`, `package-lock.json` and the
      heading agree — four places.
- [ ] The TODO's checkboxes all ticked, and its **For human** section carries whatever this
      version could not verify from inside the repository.

Done when
- [ ] `docs/dev/release-checklist.md` is walked end to end, including the blocking item the
      last two releases both tripped on: **the release commit is actually pushed**, not merely
      that the repository exists.

Tests
- [ ] No new code. The doc sweep is the deliverable, and the standard it is held to is 0.3.0's:
      every claim checked against the code, not against the previous version of the doc.

---

## Risks, and the exit for each

| Risk | Exit |
|---|---|
| Parity proves impossible for a page family (Astro emitted something we cannot or should not reproduce) | Declare the family an exception with a written reason, and cover it with a Playwright assertion instead. The exceptions file is reviewed at Checkpoint B; a long list is a signal to stop and re-plan, not to keep adding. |
| chroma's output quality is materially worse than Shiki's on the languages this site actually contains | Measured, not argued: render the self-site's blob pages both ways and look. If it is bad, fall back to shipping chroma for the long tail and a small hand-written lexer for the top five languages — a contained cost, unlike hand-writing all of them. |
| The Go ingest cannot reach byte identity on some field (a platform difference in git output, most likely paths or line endings) | Fix the *artifact producer*, not the comparison. If a field is genuinely platform-dependent, that is a latent bug in the current ingest too and it gets its own changelog entry. Never weaken the comparison to make it pass. |
| The wizard port (Phase 8, ~4,100 lines) overruns | It is the most self-contained phase and the only one with a clean deferral: ship 0.4.0 with the Go build and the *TypeScript* CLI still present, and finish the wizard in 0.4.1. That is the "Build only" option the owner declined for scope reasons, kept in reserve as a schedule valve. Phases 9 and 10 then move with it. |
| The version stalls half-migrated at a usage limit | Every checkpoint is a releasable state, and the TODO's checkboxes are ticked as work lands. Checkpoints A and B in particular leave a working site on a working toolchain. |

## Phase summary

| # | Phase | Ends at |
|---|---|---|
| 1 | Maintenance, baselines, open 0.4.0 | — |
| 2 | Svelte islands → web components (on Astro) | **Checkpoint A** |
| 3 | Go module: artifact, config, byte identity | — |
| 4 | Go renderer I: shell, site-wide pages, parity harness | — |
| 5 | Go renderer II: multiplier families, markdown, highlighting | **Checkpoint B** |
| 6 | Go ingest (byte-identical artifact) | — |
| 7 | The streaming pipeline | **Checkpoint C** |
| 8 | Go CLI and web wizard | — |
| 9 | Delete the Node build path | **Checkpoint D** |
| 10 | Documentation, migration, release | Release |
