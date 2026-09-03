# Delivery phases — 0.3.0

Companion to [TODO](TODO), which is the *what* for this version (there is no separate
`version-3.md`); this file is the *in what order*. Same shape as
[version-2-phased.md](version-2-phased.md): each phase ends in something runnable and
demoable on its own; prerequisites land before the phases that consume them (fetch-status
recording before cooldowns; avatars before the wizard that uploads them); each feature
lands both of its sides — data and UI — inside one phase; and the wizard phase comes last
before release because it must expose every setting the earlier phases add.

**Checkpoints** (the TODO's ask): the full suite — `npm test`, `npm run test:e2e`,
`npm run check`, plus a clean `npm run build` — runs only at the checkpoints marked below,
and must be green before the next group starts. Between checkpoints, each phase runs the
*targeted* suites it touches (named per phase). Within a checkpoint group, phases whose
file footprints are disjoint (noted per group) can be implemented by parallel subagents;
phases with a shared file are sequenced or given an explicit merge point.

Conventions used below:

- **Ships** – the user-visible or developer-visible result when the phase is done.
- **Done when** – the acceptance bar. Nothing moves to the next phase until these hold.
- **Tests** – what must exist per the `TODO` rules: unit + UI coverage for anything
  crossing the ingest ↔ site boundary or the server ↔ browser boundary (both sides in the
  same change), and sync/regression tests whenever the artifact changes.
- **Versioning** – the first change of 0.3.0 — Phase 1 — bumps `VERSION` and
  `package.json` to `0.3.0` and opens a `# 0.3.0 (unreleased)` heading in `CHANGELOG.md`
  (0.2.0 is dated 2026-08-30, so a new heading opens). Every change in every phase then
  adds its entries under **Features** / **Bug Fixes** / **Other**; the release date is
  stamped only in the final phase. The `TODO` checkboxes are ticked as each item lands —
  never batched at the end — so a usage-limit interruption leaves an honest trail.
- **Data model** – any change to `src/lib/data/schema.ts` that alters emitted JSON follows
  [data-model.md § Bumping SCHEMA_VERSION](../data-model.md)'s four steps in the same
  change (bump `SCHEMA_VERSION`; update the doc; regenerate the ingest snapshot and
  extractor tests; changelog entry) plus the sync tests — and, new for 0.3.0 per the
  updated `TODO`: if a bump has any migration consequence beyond "re-run ingest", the
  migration guide goes in the changelog entry itself. Exactly one bump is planned
  (Phase 7, v7 → v8); no other phase may open one while it is in flight.

**Owner decisions (2026-08-31)** — asked before Phase 1, binding on the phases named:

- *Phase 1, TypeScript 7*: try it; if `@astrojs/check`/astro aren't ready, **pin at 6.x**
  and record the deferral (changelog **Other** + the TODO's **For human**). Don't fight it.
- *Phase 5, same-hash skip*: **all-refs-match skips the repo's fetch** — one `git ls-remote`
  probe, any changed ref means a normal full fetch. Confirmed as the intended semantic
  given that mirrors have no per-branch fetch.
- *Phase 7, avatars*: **`public/images/` with a public-relative path in config** — not the
  ingest blob store.
- *Phase 4/5, persistent 429s*: **make it configurable** (deviation from the plan's
  original "always continue"). Add `ingest.failOnDegraded: boolean`, default `false` =
  today's behaviour (warn, use cached metadata, finish the build); `true` exits non-zero
  when any repo ends the run degraded, so a rate-limited build can be made to fail loudly
  in CI rather than publish stale metadata. The knob lands in Phase 4 beside the backoff,
  is documented in `configuration.md`, and is exposed by the Phase 8 wizard like every
  other 0.3.0 setting.

Standing constraints every phase inherits: the build stays fully static; `src/lib/format.ts`
and `src/lib/listing.ts` stay browser-safe; ingest stays deterministic (no wall-clock
values in `forge.json`, ever — timestamps live in `ingest.cacheDir` sidecars only;
time-dependent logic takes injected clocks); plain CSS with `hf-` prefixes; and the
TODO's stance that **usability beats accessibility when they collide** — this is a
personal site; keep things usable, don't spend functionality or performance on a11y.

---

## Phase 1 — Maintenance: dependency updates, open 0.3.0 ✅ *(done 2026-08-31)*

Goal: the TODO's maintenance item, done first so every later phase builds and tests on
the updated toolchain, and so the version bookkeeping opens.

Current picture (`npm outdated`, 2026-08-31): five in-range bumps — astro 7.2.4 → 7.2.9,
svelte 5.56.10 → 5.57.0, marked 18.0.10 → 18.0.11, tsx 4.23.12 → 4.23.13,
@types/node 26.2.0 → 26.4.0 — and one **major**: typescript 6.0.3 → 7.0.2. Everything
else (mermaid, @astrojs/svelte, @astrojs/check, vitest, @playwright/test) is current.

Ships
- [x] `npm update` for the five in-range packages; lockfile committed.
- [x] TypeScript 7 evaluated *separately, second*: bump, run `npm run check` + `npm test`
  + a build. Gate on `@astrojs/check`/astro declaring TS 7 support (check their peer
  ranges and release notes before bumping). If anything breaks or the ecosystem hasn't
  caught up, stay on 6.x and record the deferral in the changelog under **Other** — a
  pinned known-good major beats a fought-for broken one.
  *As built: **deferred, without installing it** — the planned gate answered the question
  on its own. Both `@astrojs/check` and `@astrojs/svelte` declare a `typescript` peer
  range of `^5.0.0 || ^6.0.0`, so 7.x is out of range for the toolchain that has to
  consume it; installing it to watch `astro check` fail would have proven nothing the
  peer ranges did not already say. Pinned at 6.0.3, recorded in the changelog and in the
  TODO's **For human**.*
- [x] `VERSION` + `package.json` → `0.3.0`; `CHANGELOG.md` gains `# 0.3.0 (unreleased)`
  with the dependency work under **Other**.

Done when
- [x] Full gates green on the updated deps (this phase's verification *is* Checkpoint 1's
  first half — see below).

Tests
- [x] None new — the deliverable is the existing suites passing on new versions. Watch
  specifically: `markdown.test.ts` (marked), `mermaid.spec.ts` (mermaid untouched but
  renders through astro/vite), the wizard e2e (svelte islands untouched, page is vanilla —
  low risk), and `astro check` (typescript).

---

## Phase 2 — Repo-page UI: clone popup, insights KPI, license links, hosted sites in About ✅ *(done 2026-08-31)*

Goal: the three Design items plus "show hosted site(s) in the about section" — all four
live on the repo overview/insights pages, all four are site-side only, none touch the
artifact.

Ships
- [x] **Clone popup width** (`docs/dev/plans/TODO` "really skinny … tying the width to the
  size of the button"). Root cause, confirmed: the popup `.hf-clone-pop` declares
  `width: 360px; max-width: 100%` (`src/styles/global.css:964-975`), but the absolute
  override (`global.css:1247`) positions it inside `.hf-clone` (`position: relative`,
  `global.css:1243`) — the `<details>` element, which as a flex item of
  `.hf-toolbar-right` shrink-wraps to the summary button, so `max-width: 100%` clamps
  360px down to the button width. Fix in CSS only: on the popup-mode rule, let the popup
  size itself independently of its containing block — e.g.
  `max-width: min(360px, calc(100vw - 32px))` (or an equivalent that keeps it on-screen
  at small viewports; it stays right-anchored via the existing `right: 0`). Check the
  `::before` arrow offset (`global.css:976-983`) still points at the button, and check
  both palettes and mobile widths.
  *As built, with one thing the plan got wrong: the desktop half is exactly as designed
  (`max-width: min(400px, calc(100vw - 32px))` on the popup-mode rule) — but **400px, not
  360px**, because at 360 a plain `https://github.com/<owner>/<repo>.git` still lost its
  last 16px to the ellipsis, and reading that URL is the panel's whole purpose. The plan's
  assumption that `right: 0` keeps it on-screen at small viewports is **false**: below
  900px `.hf-toolbar-right` drops its `margin-left: auto` (`global.css:1133`), so the
  button sits mid-row and a right-anchored panel hangs ~117px off the left of a 375px
  screen. Mobile therefore anchors to `.hf-toolbar` instead (already `position: relative`)
  via `left: 0; right: 0` with `.hf-clone` made `position: static`, and the arrow — which
  points at a button that has moved — is hidden. That override block **must sit after**
  `.hf-clone { position: relative }` in source order: a media query adds no specificity,
  and the first attempt silently lost to the later rule. It also passed the first
  containment-only mobile test while rendering 105px wide, so that test now asserts the
  panel is genuinely wider than the button as well.*
- [x] **Insights code-size KPI swap** (TODO: "make the lines of code the larger text, and
  the approximate size the smaller text"). In the fourth KPI tile,
  `src/pages/repos/[slug]/insights/index.astro:159-169`: today `hf-kpi-value` holds
  `formatBytes(latestSize.bytes)` and `hf-kpi-sub` holds the line count. Swap them —
  lines (via the existing `only(latestSize.lines, 'line')` formatting) into
  `hf-kpi-value`, bytes into the sub-line. Keep the fallbacks honest: when `hasLines` is
  false (the over-budget / binaries case, locals at lines 77-100), bytes stays the big
  number and the existing caveat text stays the sub — don't render a dash as the hero
  number when a real value exists one line down.
- [x] **License badge links** (TODO: link common licenses to choosealicense.com /
  creativecommons.org). New pure helper `licenseUrl(spdx: string): string | null` in
  `src/lib/format.ts` (browser-safe, no config): a small explicit map — SPDX ids that
  choosealicense.com hosts (MIT, Apache-2.0, GPL-2.0/3.0, LGPL, AGPL-3.0, MPL-2.0,
  BSD-2/3-Clause, Unlicense, BSL-1.0, EPL-2.0, 0BSD …) →
  *As built: one detail the plan's "lowercased-id" shorthand would have got wrong — our own
  detector emits the GNU disambiguation suffix (`GPL-3.0-only`, `AGPL-3.0-only`,
  `LGPL-2.1-only`; `src/lib/ingest/license.ts:49-53`), while choosealicense serves
  `/licenses/gpl-3.0/`. `licenseUrl` strips a trailing `-only` / `-or-later` before
  matching, and a test walks every id `detectSpdx` can return to keep the map honest as it
  grows.*
  `https://choosealicense.com/licenses/<lowercased-id>/`; `CC-BY*`/`CC0` family →
  the matching `https://creativecommons.org/licenses/...` (CC0 →
  `/publicdomain/zero/1.0/`). Unknown / null / `Custom` → `null`, render unchanged.
  Apply at both render sites: the header badge (`src/components/RepoHeader.astro:73` —
  the `<span class="hf-badge">` becomes an `<a class="hf-badge">` when a URL exists;
  match existing badge hover/focus styling) and the About sidebar meta row
  (`src/pages/repos/[slug]/index.astro:152`). External link semantics
  (`rel="noopener"`, same treatment other external links get).
- [x] **Hosted site(s) in About** (TODO: "If a repo has site(s) associated with it that
  are being hosted, show them in the about section"). The binding already lives in the
  artifact: `ForgeData.hosting: HostedSite[]` (`{ slug, repo, ref }`,
  `src/lib/data/schema.ts:667-686`) — but nothing in the UI reads it today. Add a
  `hostedUrl(slug)` helper beside `hostedFiles`/`hostedRoutes` in `src/lib/routes.ts`
  (base-aware via `withBase`, like every other builder), look up
  `data.hosting.filter(h => h.repo === repo.slug)` in the repo overview frontmatter
  (`getStaticPaths` already calls `getData()` at `index.astro:14` — pass the matches
  through props), and render them in the About links list (the `links` array assembled
  at `index.astro:41-45`) as "Hosted site" rows — plural-safe, one row per site, using
  the existing icon + `.hf-lbl` + link pattern. Note these are *internal* links (no
  `prettyUrl` host-stripping weirdness — show the site slug or path).

Done when
- [x] The clone popup opens at its full intended width from a narrow button, nothing
  pokes out, on desktop and mobile widths in both palettes.
- [x] The code-size tile leads with lines; a fixture repo over the read budget still
  shows bytes big with the caveat.
- [x] An MIT repo's badge links to choosealicense; a repo with no SPDX renders exactly
  today's markup.
- [x] The hosting fixture repo's overview shows a working link to its hosted site
  (including under the base-path build); a non-hosted repo's About is unchanged.

Tests
- [x] Unit: `format.test.ts` — `licenseUrl` map hits, CC family, null/unknown/Custom.
  `site-sync.test.ts`/route suites — `hostedUrl` under default and mocked base.
- [x] UI (e2e): `site.spec.ts` — license badge is a link with the right href on the
  licensed fixture, plain span otherwise; `insights.spec.ts` — the KPI value/sub swap;
  `hosting.spec.ts` — the About row links to the hosted site and it loads;
  `base-path.spec.ts` inherits the leak scan (the new internal link must be
  base-prefixed). Clone popup: a viewport-width assertion in `site.spec.ts` (popup
  bounding box ≥ its intended width or viewport-clamped, not button-clamped).
- [x] Data model: none — all four are render-side.

---

### ✅ CHECKPOINT 1 — after Phases 1–2 — *PASSED 2026-08-31*

*Result: `npm test` 598 passed / 1 skipped (39 files, up from 592 — 6 new unit tests),
`npm run test:e2e` 181 passed / 1 skipped (up from 175 — 6 new specs), `npm run check`
0 errors 0 warnings 4 hints (the 4 pre-date this version), `npm run build` clean at 616
pages. Also verified by hand in a real browser at 1200px and 375px, both the desktop and
the mobile clone-popup paths, plus the license links and the insights tile — which is how
the mobile source-order bug above was caught after the automated check had gone green.
Run sequentially rather than as parallel subagents: Phase 1 finished in three commands,
so the coordination would have cost more than it saved.*

Full suite (`npm test`, `npm run test:e2e`, `npm run check`, `npm run build`) green.
Subagent note: Phase 1 (package.json / lockfile / possibly tsconfig) and Phase 2
(styles + pages + format.ts/routes.ts) have disjoint footprints and can run as parallel
subagents; land Phase 1's lockfile first if run sequentially so Phase 2's tests execute
on the new versions. The checkpoint validates the union either way.

---

## Phase 3 — Fetch-status recording (run-log v2) ✅ *(done 2026-08-31)*

Goal: the TODO's "indicate somewhere stored if when building, fetching the data and
metadata was successful (if you don't already), this will be relevant in a later ask" —
the later asks being Phase 5's cooldown ("since last **successful** fetch") and same-hash
skip. Today's record is close but too coarse: `last-run.json`
(`src/lib/ingest/reuse.ts:80-124`) stores one `{ fetchedAt, fresh }` per source, where
`fresh` conflates the git mirror and the provider-metadata halves (the `DEGRADED` set at
`src/lib/ingest/index.ts:326-340` collapses both into one boolean). No per-branch head is
recorded anywhere outside the artifact.

Ships
- [x] **Run-log entry v2.** `RUN_LOG_VERSION` → 2; `RunLogEntry` grows to
  `{ fetchedAt, fresh, git: { ok: boolean }, meta: { ok: boolean }, heads?: Record<ref, sha> }`
  (exact field names at implementer's discretion; the contract is: git-fetch success,
  metadata-fetch success, and the mirror's ref heads as of the last *successful* git
  fetch, recorded from the same `git for-each-ref` read `scanInputDigest` already does
  at `reuse.ts:159-183`). `fresh` keeps its exact current meaning (both halves healthy)
  so the reuse window's semantics don't move. Success/failure is derived where it's
  already known: the warning stamping in `index.ts:325-340` splits the `DEGRADED` set by
  which half each code indicts (`remote-fetch-failed` → git, `remote-rate-limited` /
  `remote-auth-missing` → meta, `remote-cache-stale` → whichever half read stale). A v1
  log on disk is discarded wholesale (the existing `configHash`-mismatch path — cheap,
  correct, and the sidecar is rebuildable by definition).
- [x] The `'reused'`-action rule survives: a window-skip or replay keeps the previous
  stamp verbatim (the "window cannot extend itself" invariant from 0.2.0, and now also
  "a skip cannot launder a failed half into a successful one").
- [x] Timestamps stay sidecar-only; the clock stays the injected `PrepareRemoteDeps.now`.
  Nothing in this phase touches `forge.json` — byte-identity across runs is unchanged.

Done when
- [x] After a run with one rate-limited repo, `last-run.json` shows that repo with
  `git.ok: true, meta.ok: false` and everything healthy elsewhere; after a clean run,
  every entry carries the mirror's actual heads.
  *As built, one deliberate departure from the plan: the halves are **reported by
  `prepareRemote`** (a new `fetchStatus` on its result) rather than derived from the
  warning codes as the plan sketched. Writing it the planned way exposed why it cannot
  work — `remote-cache-stale` is raised BOTH for a mirror that could not be refreshed and
  for provider metadata served from cache, so the code alone cannot say which half failed.
  Field names are flat (`gitOk`, `metaOk`, `heads`) rather than nested. `withinFreshWindow`
  now takes `Pick<RunLogEntry, 'fetchedAt' | 'fresh'>`: it reads only those two, and
  narrowing it kept every existing caller and test untouched by the v2 widening.*

Tests
- [x] Unit: `reuse.test.ts` — v2 shape round-trip, v1-on-disk discarded, per-half
  success derivation from each warning code, heads recorded and *kept* through a
  window-skip, byte-identity of the artifact across a record/skip pair. The existing
  window tests re-run untouched (semantics of `fresh` unchanged).
- [x] Data model: none (sidecar only).

---

## Phase 4 — Rate-limit resilience: misses-first ordering, per-origin backoff ✅ *(done 2026-08-31)*

Goal: the TODO's 429 cluster, sized for the real corpus (the `Copy (3)` example config:
72 GitHub sources — at `ingest.concurrency: 4` with zero retry, a rate-limited run today
burns its budget on repos that already have cached metadata and then fails the rest).

Ships
- [x] **Misses first** (TODO: "If there are repo's that have no metadata, run updates on
  them first, since they're most likely to be misses from a previous build"). In
  `ingest()` (`src/lib/ingest/index.ts:101`), before the `pool(...)` at line 141: a cheap
  pre-pass stats each remote source's provider cache file (`providerCachePathFor`,
  `src/lib/ingest/remote.ts:534-537`) and *processing* order becomes: remotes with no
  cached `.meta.json` first, then the rest, each group keeping config order (stable
  sort); local sources are unaffected. **Determinism guard:** processing order is not
  artifact order — assembly must (and today does) produce output ordered independently
  of completion order; add an explicit test that a reordered run is byte-identical, so
  this stays true.
- [x] **Per-origin exponential backoff** (TODO: "keyed to the origin (e.g. github.com
  should have 1 timeout, codeberg.com a different one)"). In
  `src/lib/importers/http.ts`: today `classifyStatus` (247-259) tags a 429 and
  `parseRetryAfter` (273-291) even computes the server's requested delay — **and nothing
  reads it** (`ImporterError.retryAfter` has zero consumers). Add a module-level
  per-origin gate shared by every `JsonClient` (keyed on `new URL(host).origin`; every
  provider config carries `host` — `src/lib/config/schema.ts:57,67,75,86`): on 429,
  wait `retryAfter` when the server said so, else exponential (base ~1s, ×2, jittered,
  capped ~60s, bounded total attempts ~4), and while an origin is backing off *every*
  request to that origin queues behind the same timer — four in-flight github repos must
  not each independently hammer the window, while a codeberg fetch proceeds untouched.
  On final failure the existing `remote-rate-limited` → cached-metadata degradation path
  is unchanged. The delay/clock is injectable (the `sleepImpl`/`now` pattern the module's
  retry already uses for its 500-retry) so tests never sleep. Console-side: the ingest
  reporter (`scripts/ingest.ts:70-78`) notes when an origin is in backoff, so a long
  pause is explained, not silent.
- [x] **`ingest.failOnDegraded: boolean`** (default `false`), per the owner decision above.
  `false` = today's behaviour (warn, fall back to cached metadata, finish). `true` = after
  assembly, if any repo carries a `DEGRADED` warning code (the set at
  `src/lib/ingest/index.ts:326`), `scripts/ingest.ts` exits non-zero with a summary of
  which repos and why — after writing nothing, or after writing and saying so plainly;
  pick one and document it (recommendation: still write the artifact, then fail — a
  partial artifact plus a red build is more debuggable than neither). Not a
  determinism concern: exit code only, artifact bytes unchanged.
- [x] Interplay note, documented in code: the backoff gates the *provider API* half only;
  git mirror traffic (`ensureMirrorLocked`, `remote.ts:313-338`) is not API-rate-limited
  and does not queue behind it.

Done when
- [x] *As built, one addition the plan did not anticipate, and it is the part that most
  helps the 72-repo case: a limit LONGER than the 60s ceiling does not sleep and does not
  retry — it marks the origin blocked for the stated window, so repos 2..72 on that host
  fail fast into their cached metadata instead of each burning a full retry ladder. A
  short limit still waits and retries as planned. Rate-limit retries also carry their own
  attempt budget rather than consuming the existing 5xx/network one. `OriginBackoff.reset()`
  exists because the gate is process-wide by design: correct for a build, but in a test
  process one fixture's 429 would otherwise block that host for every later case (it did —
  five importer tests caught it).*
- [x] Against the http fixtures, a 429-then-success sequence succeeds on retry with the
  fixture's `retry-after` honored; two clients on one origin serialize their backoff
  while a second origin proceeds; a cold-cache repo demonstrably fetches before a
  warm-cache one; a full ingest of a mixed config is byte-identical to the same config
  ingested in plain order.

Tests
- [x] Unit: `importers.test.ts` / a new `backoff.test.ts` — retry-after honored,
  exponential progression with fake clock, cap, attempt bound, per-origin isolation,
  queue-behind-timer behavior, final-failure error shape unchanged.
  `ingest.test.ts`/`reuse.test.ts` — misses-first ordering (spy on fetch order),
  reorder byte-identity.
- [x] Data model: none. The `remote-rate-limited` warning code and its meaning are
  unchanged — retries just make it rarer.

---

## Phase 5 — Opt-in refetch controls: same-hash skip, cooldown ✅ *(done 2026-08-31)*

Goal: the TODO's opt-in build settings. Both consume Phase 3's run-log v2. Both are
**opt-in, default off** — the TODO is explicit — and both must be byte-neutral: a skipped
fetch produces the identical artifact the unskipped run would have produced from the same
cache (the Phase-2-of-0.2.0 reuse precedent).

Ships
- [x] **Config.** New keys under `ingest.reuse` (they are refetch-avoidance knobs, same
  family as the freshness window): `reuse.skipUnchanged: boolean` (default `false`) and
  `reuse.cooldownSeconds: number | null` (default `null`). Zod: non-negative integer for
  the cooldown. Documented in `configuration.md` with the tradeoff each one buys.
- [x] **Same-hash skip** (TODO: "If the commit hash is the same as it was last time for
  that branch, don't fetch that branch"). Design realities, stated up front: mirror
  updates are per-*repo* (`git remote update --prune`, `remote.ts:314`), not per-branch,
  and no pre-fetch SHA source exists today (grep confirms zero `ls-remote` anywhere). So
  the semantic delivered is: before updating an existing mirror, run one
  `git ls-remote --heads --tags <cloneUrl>` (a single cheap git-protocol round-trip, not
  provider-API traffic, so it doesn't touch Phase 4's budget) and compare against the
  run-log's recorded heads; **if every ref matches, skip `remote update` entirely** —
  which is exactly "don't fetch that branch" for every branch at once, the only
  granularity a mirror offers. Any mismatch, missing heads record, or `ls-remote`
  failure → fetch as today (fail open). `fetch: 'always'`/`'never'` are unaffected
  (`'always'` is an explicit ask; `'never'` never fetches anyway). The skip records
  action `'fetched'`-equivalent freshness only if it can prove heads matched — reuse
  semantics: skip work, never change bytes.
- [x] **Cooldown** (TODO: "If the cooldown period has not elapsed since last
  **successful** fetch, skip the repo … with a message 'this repo is on cooldown' with
  the usual warning emoji"). In the pre-`prepareRemote` window logic
  (`index.ts:190-193`, beside the existing `skipFetch`): if `cooldownSeconds` is set and
  `now - entry.fetchedAt < cooldown` **and both halves of the last fetch succeeded**
  (`git.ok && meta.ok` — the TODO's bolded *successful*), skip the fetch (both halves)
  and build from cache. A previously degraded repo is *never* cooldown-skipped — same
  principle as the 0.2.0 freshness window, and it composes with Phase 4: the repos most
  likely to be in trouble go first and are always retried. Console: the per-repo
  reporter line (`scripts/ingest.ts:70-78`) prints `⚠️ <slug>: this repo is on cooldown`
  (reported via `RemoteStatus`/`onRemote` — add a `cooldown: boolean` or a new
  `MirrorAction`-adjacent field to the reporting-only struct at `index.ts:50-57`; it
  never enters the artifact).
- [x] Precedence, documented and tested: `fetch` mode > freshness window (an in-window
  repo never reaches the cooldown check — it's already skipped) > cooldown >
  same-hash skip (cheapest last: the probe only runs when nothing else already decided
  to skip).

Done when
- [x] *As built: the skip returns a new `MirrorAction` — `'current'` — rather than reusing
  `'cached'`. That mattered: `'cached'` means "the update FAILED and the old mirror was
  used" and raises `remote-cache-stale`, which would have made every successful skip look
  like a degradation, poisoned `gitOk`, and then blocked the cooldown. `'current'` counts
  as a healthy git half. The tests assert on that action rather than spying on git argv —
  only the ls-remote-matched path can produce it, so it is the stronger signal.*
- [x] With `skipUnchanged: true`, a second ingest against unchanged remotes performs zero
  `remote update` calls and emits a byte-identical artifact; pushing a commit to the
  fixture remote un-skips exactly that repo.
- [x] With a 60s cooldown and an injected clock, a re-run inside the window prints the
  ⚠️ cooldown line and skips; a repo whose last run was rate-limited is fetched anyway;
  advancing the clock past the window fetches everything.

Tests
- [x] Unit: `reuse.test.ts`/`remote.test.ts` — heads-match skip, single-ref mismatch
  fetches, `ls-remote` failure fails open, cooldown honored/expired/degraded-bypass,
  precedence order, defaults-off means zero behavior change, byte-identity for every
  skip path, config validation. Fixture remotes are local bare repos (the existing
  fixture-repo helper), so `ls-remote` runs against real git without network.
- [x] UI: none (build-time only). The e2e build runs with both knobs off — explicitly
  assert the defaults in `config-knobs.test.ts` so the suite proves the off state.
- [x] Data model: none. Sidecar and console only.

---

### ✅ CHECKPOINT 2 — after Phases 3–5 — *PASSED 2026-08-31*

*Result: `npm test` 634 passed / 1 skipped (40 files, up from 598 — a new `backoff.test.ts`
plus 21 more in `reuse.test.ts`/`config-knobs.test.ts`), `npm run test:e2e` 181 passed /
1 skipped, `npm run check` 0 errors, `npm run build` clean with 0 warnings, and
`forge.json` byte-identical across a warm run AND across a `--no-cache` run in between.*

*Run sequentially, not as parallel subagents: Phase 3 and Phase 4 both edit the same
regions of `src/lib/ingest/index.ts` (the run-log stamp, the pool call, the RemoteStatus
shape) and Phase 5 consumes Phase 3's output, so the stated merge point would have cost
more coordination than the parallelism was worth at this size.*

*The plan's manual item — a real `npm run build` against the 72-remote `Copy (3)` corpus —
is **carried over, not done**: it clones 72 mirrors over the live network against the
owner's own GitHub quota, which is not something to trigger unattended. It is listed in the
TODO's **For human** section. Everything it would exercise is covered at unit level with an
injected clock and a real local `ls-remote`.*

Full suite green, plus one manual: a real `npm run build` against a config with several
live GitHub repos (the `Copy (3)` config is the reference corpus) completes without 429
failures, and a second build inside a set cooldown prints the ⚠️ lines and finishes
substantially faster. Subagent note: Phase 3 (reuse.ts + the run-log stamping block of
ingest/index.ts) and Phase 4 (http.ts + the pool-ordering block of ingest/index.ts) touch
different regions of `src/lib/ingest/index.ts` — parallel subagents are fine but their
index.ts edits need a stated merge point (Phase 3 owns lines ~325-340, Phase 4 owns
~141 and the reporter); Phase 5 depends on Phase 3 and follows it.

---

## Phase 6 — `npm run dev` repurpose + build-pipeline documentation ✅ *(done 2026-08-31)*

Goal: two small, independent deliverables that both want the pipeline settled: the dev
script's honest replacement, and `docs/dev/build-steps.md` — written *after* Phases 3–5
so the diagrams document the pipeline 0.3.0 actually ships.

Ships
- [x] **`npm run dev` → notice + preview** (TODO: "show a message that the information
  displayed is taken from the most recent build, and that it doesn't build itself, then
  run `astro preview`"). New tiny `scripts/dev.ts`; `package.json`'s `dev` script points
  at it. It: (1) prints the notice — the site being served reflects the most recent
  `npm run build`; nothing rebuilds on file changes; run `npm run build` to refresh;
  (2) sanity-checks that `dist/` and `data/forge.json` exist, and if not says exactly
  what to run instead of letting `astro preview` fail obscurely; (3) spawns
  `astro preview`, passing through args/port, inheriting stdio, forwarding exit code.
  The raw Astro dev server stays reachable as `npm run astro dev` for anyone who really
  wants HMR against a stale artifact (`loadForgeData` memoises `forge.json` for the
  process lifetime — `src/lib/data/load.ts:16-28` — which is exactly why `dev` was
  "mostly useless"). Collateral updates, same change: `CLAUDE.md`/`AGENTS.md` (the
  `astro dev --background` guidance no longer matches `npm run dev`),
  `.claude/launch.json` (already preview-based — verify), and any docs that say
  `npm run dev` (`quick-start.md`, `starting-a-site.md` — grep).
- [x] **`docs/dev/build-steps.md`** (TODO: mermaid diagrams + code references). The
  document walks `npm run build` end to end, per the TODO's dev-docs rule (diagrams +
  code references throughout):
  1. a top-level flowchart: config resolve → ingest (`scripts/ingest.ts` →
     `ingest()` at `src/lib/ingest/index.ts:101`) → artifact (`forge.json` + blobs +
     archives) → `astro build` → `dist/`;
  2. a per-repo sequence diagram of the remote path: freshness window → cooldown →
     same-hash probe → mirror update (`ensureMirrorLocked`) → provider metadata with
     per-origin backoff (`JsonClient`) → scan or scan-cache replay → assembly — i.e.
     Phases 3–5 drawn as they landed, including where `last-run.json`, the `.meta.json`
     provider cache, the scan cache, and the highlight cache each read/write;
  3. a short "where success/failure is recorded" section (run-log v2 fields, the warning
     codes, what `--no-cache` bypasses);
  4. every node annotated with `file.ts:line`-style references, per the TODO's
     documentation rule.
  Cross-link it from `docs/dev/README.md` and from `performance.md` where the caches
  are discussed.

Done when
- [x] `npm run dev` on a fresh clone (no build yet) prints actionable guidance and exits
  cleanly; after a build it serves the site with the notice shown first.
- [x] `build-steps.md` exists, renders its mermaid on the forge itself (the self-hosted
  repo ingests its own docs — a nice dogfood: the diagrams render via 0.2.0's mermaid
  support), and its code references resolve to real lines.

Tests
- [x] Unit: a small `dev-script` test if the guard logic is extracted as a function
  (missing-dist message, arg passthrough); otherwise the script stays thin enough that
  the e2e-adjacent manual check suffices — don't build a test rig for a 30-line wrapper.
- [x] Docs have no automated gate; the review bar is the Done-when.

---

## Phase 7 — People & avatars: owner image, org images, contributor entries (schema v8) ✅ *(done 2026-08-31)*

Goal: the TODO's two people-clusters — "an image for the main user" and "information
about other contributors and organizations" — landed together because they share one
schema bump, one image-storage convention, and one rendering pattern. Today every avatar
in the site is `initials(...)` text (`src/lib/format.ts:190`; seven render sites), no
config or frontmatter key carries an image, and provider metadata doesn't even read
`avatar_url` (`ImportedRepoMeta`, `src/lib/importers/types.ts:36-56`).

Ships
- [x] **Image storage convention.** Owner/org/contributor images are site content, not
  ingest artifacts: they live in `public/` (Astro serves them verbatim; no pipeline
  work), by convention under `public/images/` (e.g. `public/images/owner.png`,
  `public/images/orgs/<slug>.png`) but any `public/`-relative path is accepted. Config
  stores the public-relative path; rendering wraps it in `withBase`. The hosting
  reserved-path collision check (`resolveHosting`, `src/lib/ingest/hosting.ts:43-104`)
  already hard-errors on `public/` collisions, so a hosted slug can't shadow `images/`
  — verify with a test rather than assuming.
- [x] **Owner avatar.** `avatar: string` (optional) — decide its home deliberately:
  `owner` config block (`frznforge.config.ts`, beside `name`/`handle`) rather than
  profile frontmatter, because the wizard edits config natively and the sidebar needs it
  on every page (frontmatter is profile-page-scoped). Rendered as `<img class="hf-avatar
  hf-avatar--xl">` (object-fit cover, same box the initials div occupies) on the profile
  hero (`src/pages/index.astro:80`) and the sidebar brand area if applicable
  (`src/components/Sidebar.astro:79`); initials remain the no-avatar fallback
  everywhere. Alt text: empty (`alt=""`) — decorative, name is adjacent; the usability
  stance says don't over-engineer.
- [x] **Avatar import from forges** (TODO: "When importing from github or some other
  forge, give the option to import the profile picture as well, and store it"). This is
  an *init-time* fetch, not an ingest concern: a `frznforge init`/wizard action that,
  given the owner's forge handle, downloads the avatar (GitHub:
  `https://github.com/<user>.png` or the users API `avatar_url`; equivalents per
  provider on gitlab/gitea/forgejo) to `public/images/owner.png` and sets
  `owner.avatar`. In the terminal wizard it's a y/n prompt; the web-wizard side lands in
  Phase 8. Repo *ingest* does not fetch avatars — keeping the ingest pipeline free of
  new network calls right after Phase 4/5 tamed it. (If org-avatar import from provider
  orgs proves cheap it may piggyback on the same helper; otherwise org images are
  user-supplied files only. Record whichever way it goes.)
- [x] **Organization images + fields.** `OrganizationConfig`
  (`src/lib/config/schema.ts:151-161`) gains `avatar?: string` (public-relative path).
  Artifact `Organization` (`src/lib/data/schema.ts:642-649`) gains `avatar: string |
  null` — **this is the v8 `SCHEMA_VERSION` bump**, executed as one change with
  data-model.md, the snapshot, extractor tests, sync tests, and the changelog entry (no
  migration steps beyond re-ingest: additive field, old artifacts fail schema-load and
  re-ingest as designed — say exactly that in the changelog per the updated TODO rule).
  Rendered in `OrgHeader.astro:58`, the orgs index card (`src/pages/orgs/index.astro:60`),
  initials fallback preserved.
- [x] **Contributor entries** (TODO: "similar behaviour for contributors, which allow
  the same fields as organizations, including the avatar"). New top-level config
  `contributors: [{ name, emails: string[], avatar?, description?, url? }]` — matched
  against git-derived contributors (`src/lib/ingest/contributors.ts`;
  `Contributor` at `data/schema.ts:167-174`) by email, the same identity mechanism
  `ProfileFrontmatter.identities` already uses. Matched contributors are *enriched* in
  the artifact (fields join `Contributor`, riding the same v8 bump); unmatched config
  entries raise a build warning (the `org-unknown-repo` precedent); unconfigured
  contributors render exactly as today. Rendering: the About-sidebar contributor list
  (`src/pages/repos/[slug]/index.astro:171-180`) shows avatars where present (initials
  otherwise) and links the `url` when given; commit lists stay initials-only for now
  (perf: no per-commit image soup) unless it falls out free.
- [x] Docs: `configuration.md` — `owner.avatar`, org `avatar`, the `contributors` block,
  the `public/images/` convention.

Done when
- [x] A config with an owner avatar, one org image, and one enriched contributor renders
  all three (profile hero, org header + card, About contributors), falls back to
  initials wherever unset, works under the base path, and a stale (v7) artifact
  triggers a clean re-ingest rather than a confusing failure.

Tests
- [x] Unit: `schema.test.ts`/snapshot regen for v8; `orgs.test.ts` — avatar threading +
  unknown-contributor warning; `contributors.test.ts` — email matching, enrichment,
  no-match passthrough; config validation for the new keys; the hosting reserved-path
  interaction probe.
- [x] UI (e2e): `site.spec.ts`/`orgs.spec.ts` — `<img>` present with correct base-aware
  src on fixture pages, initials fallback asserted on an avatar-less fixture;
  `base-path.spec.ts` leak scan covers the new srcs automatically.
- [x] Data model: the v8 bump done as one change (the TODO migration rule applies —
  changelog carries the note).

---

## Phase 8 — Wizard: uploads, edit-in-place, new settings, Done saves everything ✅ *(done 2026-08-31)*

Goal: the wizard catches up with 0.3.0 — last feature phase, per the 0.2.0 precedent,
because it must expose every setting the version added (`reuse.skipUnchanged`,
`reuse.cooldownSeconds`, `owner.avatar`, org `avatar`, `contributors`). Server:
`scripts/lib/web-init.ts`; page: `scripts/lib/web-init-page.html`.

Ships
- [x] **Image upload** (TODO: owner upload + org upload in the web UI). New endpoint
  `/api/upload` inside the existing security envelope (127.0.0.1 bind, session token,
  Host/Origin pinning, serialized writes): accepts a JSON body with base64 image data
  (staying inside the existing `readBody` JSON path — no multipart parser dependency;
  raise `MAX_BODY_BYTES` for this endpoint only, to a couple of MB), validates magic
  bytes (png/jpeg/webp), and writes **only** to server-chosen paths under
  `public/images/` derived from the target (`owner`, `org:<slug>`, `contributor:<n>`) —
  the browser never names a file, the standing rule. Sets the matching config field via
  the existing operations machinery in the same request or a follow-up op. CSP: `img-src
  data:` already allows client-side preview of the picked file; `form-action 'none'`
  stays (uploads go through `fetch`, not form posts). `.bak` semantics don't apply to
  new image files; an overwritten existing image gets the `backupOnce` treatment.
- [x] **Owner avatar import in the web UI**: a "fetch from forge" button beside the
  upload (server does the download using Phase 7's helper — the browser can't, CSP
  `connect-src 'self'`).
- [x] **Edit-in-place** (TODO: "edit the information once entered, not just remove").
  The 0.2.0 scope-down gets paid off: the config-edit engine
  (`scripts/lib/config-edit.ts`) gains an indexed-set operation — `setInArray(source,
  arrayPath, index, field, value)` — with the same textual-edit contract (only the
  edited field's bytes change; expressions and comments elsewhere survive verbatim).
  Server allow-list learns the new op for `organizations[]`, `repos[]` (slug/org/
  releases — the 0.2.0 wish), `hosting.sites[]`, and the new `contributors[]`; the page
  grows edit affordances on the existing cards (prefill → save via ops). Validation
  path is unchanged: apply to a clone, `FrznforgeConfigSchema.parse`, write, re-load in
  a child process, restore on failure.
- [x] **New settings exposed**: the ingest card gains `reuse.skipUnchanged` +
  `reuse.cooldownSeconds`; owner card gains the avatar controls; org card gains image;
  a contributors card (add/edit/remove) joins the family. The 0.2.0 page↔allow-list
  drift guard (`web-init.test.ts`) must be extended first — it's the test that makes
  "expose everything" checkable.
- [x] **Done saves all** (TODO: "Hitting `done` in the web UI should also save/apply all
  current changes"). Today `doneClick()` (`web-init-page.html:1474-1485`) posts
  `/api/done` with **no flush** — unsaved settings edits and profile-body text are
  silently discarded (confirmed: no dirty tracking exists in the page). Fix on both
  sides of the contract: the page tracks dirty state per section (settings ops pending,
  profile textarea vs last-saved body, in-progress org/contributor forms) and
  `doneClick()` runs the pending saves through the existing save paths *sequentially,
  surfacing any validation error and aborting the shutdown on failure* (a refused value
  must not be half-lost at exit — the user fixes it or explicitly cancels), then posts
  `/api/done`. `/api/done`'s server behavior (drain queue, report count) is already
  correct and stays; the finish card reports what was flushed.
- [x] The token-never-leaks sweep extends over `/api/upload` and every new response.

Done when
- [x] From the page: upload an owner image and see it in config + on disk; fetch an
  avatar from GitHub; edit an existing org's name in place (file diff touches only that
  field); set a cooldown; add a contributor; then — with an *unsaved* settings edit and
  *unsaved* profile text pending — hit Done and find every pending change saved, the
  config still parsing, and `npm run build` green on the result.

Tests
- [x] Unit: `config-edit.test.ts` — `setInArray` (comments, expressions, escaped quotes,
  nested arrays, byte-identity outside the field, out-of-range index refused);
  `web-init.test.ts` — upload endpoint (magic-byte refusal, size cap, path pinning,
  traversal attempts, overwrite backup), allow-list additions incl. `__proto__`-family
  probes on the new ops, drift guard over the new fields, done-flush ordering at the
  API level, token sweep.
- [x] UI (e2e): `wizard.spec.ts` — an upload round-trip (tiny fixture png), an in-place
  org edit asserting the on-disk diff, and the Done-with-pending-edits flush asserting
  the file afterwards.
- [x] Data model: none — the wizard edits config/content/images, never the artifact.

---

### ✅ CHECKPOINT 3 — after Phases 6–8 — *PASSED 2026-08-31*

*Result: `npm test` 673 passed / 1 skipped (41 files, up from 634), `npm run test:e2e`
187 passed / 1 skipped (up from 181), `npm run check` 0 errors, `npm run build` clean with
0 warnings, and `forge.json` byte-identical across runs.*

*Phase 6 DID run as a parallel subagent while Phase 7 was implemented here — the footprints
were disjoint (scripts/dev.ts + docs vs. src/lib + components), with an explicit
do-not-touch list for the shared bookkeeping files (CHANGELOG, TODO, this plan). It worked,
with one caveat worth recording: the agent's `file.ts:line` citations in build-steps.md went
stale under my concurrent edits to `src/lib/ingest/index.ts`, and it said so. They were
re-verified at this checkpoint — every reference resolves, and the ones spot-checked
(`ingest()`, `highlightToHtml`'s memo call, `OriginBackoff.beforeRequest`/`noteRateLimit`)
point at the right code.*

*Phase 6 also found a real error in this plan's own text: the remote path fetches provider
metadata BEFORE the mirror (`prepareRemote` needs the API answer for the clone URL), not
after as Phase 4's sequence sketch assumed. The consequence for Phase 5 is worth stating
plainly: **`skipUnchanged` saves git traffic, not API traffic** — the `ls-remote` probe lives
inside `ensureMirror`, which runs after the metadata call. The cooldown is the knob that
skips both halves, and it is the one that helps with 429s.*

Full suite green. Subagent note: Phase 6 (scripts/dev.ts + docs) is disjoint from
Phase 7 (ingest/site/schema) and can run in parallel with it; Phase 8 depends on
Phase 7's config keys and helper and runs after. Phase 6's build-steps.md depends only
on Phases 3–5 (already landed at Checkpoint 2).

---

## Phase 9 — Docs sweep, checklist, cut 0.3.0 ✅ *(done 2026-08-31)*

Goal: everything a stranger needs, then the release.

Ships
- [x] Docs sweep, code-vs-doc audited (the 0.2.0 Phase 9 method — it found 24 mismatches
  last time): `configuration.md` documents every 0.3.0 key (`reuse.skipUnchanged`,
  `reuse.cooldownSeconds`, `owner.avatar`, org `avatar`, `contributors`, license-link
  behavior if it warrants a note); `data-model.md` reflects v8; `build-steps.md`
  cross-checked against the shipped pipeline one final time; `quick-start.md` /
  `starting-a-site.md` reflect the new `npm run dev` behavior and transcripts re-verified;
  README version/schema references current.
- [x] `docs/dev/release-checklist.md` run and extended with the 0.3.0 items needing a
  real browser/deploy: the clone popup at mobile width, an avatar-bearing deploy, the
  wizard upload + Done-flush end to end, a rate-limited-then-recovered build. The five
  carried-over 0.2.0 manual items get resolved or re-carried with reasons (the standing
  precedent: honest annotations beat silent ticks).
- [x] `CHANGELOG.md`'s `0.3.0 (unreleased)` heading gains its release date; `VERSION`,
  `package.json`, and the heading agree; the `TODO`'s 0.3.0 items are all ticked (or
  explicitly moved to a future version with a note in **For human**).

Done when
- [x] All three gates green, `npm run build` clean, ingest byte-identical across runs,
  checklist items ticked or carried with reasons, and the **For human** section of the
  TODO lists anything that genuinely needs the owner's eyes (at minimum: the manual
  browser checks, and the TypeScript 7 decision if it was deferred in Phase 1).

Tests
- [x] None new — the deliverable is the docs, the checklist, and the gates staying green
  over everything 0.3.0 shipped.
  *Deviation, on purpose: two tests WERE added, because the sweep found code worth changing.
  Exercising the v7 → v8 migration by hand showed a stale artifact failing with a raw Zod
  dump ("Invalid input: expected 8") that never said what to do about it; `loadForgeData`
  now names the version it found and tells the reader to re-run the build. One test covers
  that message, a second proves the version check does not swallow genuine corruption of a
  correctly-versioned artifact.*

*Run as a **read-only** audit subagent — findings only, every fix applied here, so no file
had two writers. It reported 10 confirmed contradictions and 6 documentation gaps, plus an
explicit list of what it checked and found correct: all 60+ `file.ts:line` citations in
build-steps.md resolve to the right symbol (the drift I expected from Phase 7's concurrent
edits did not materialise), the warning table matches the `WarningCode` union in both
directions, and configuration.md documents every config key with the right defaults.*

*The worst finding was not a 0.3.0 regression at all: `starting-a-site.md`'s copy-the-engine
command has named `astro.config.mjs` since the 0.2.0 rename, so anyone following it built a
site with zero pages — and the troubleshooting entry for exactly that symptom pointed at the
same nonexistent file. Ten references fixed across the docs, the scaffolder (which shipped it
into every generated README) and two code comments. Also corrected: two contradictory rebuild
figures (63% vs 66%; 16.7→5.0 vs 23.1→7.9), the missing v8 row in the data-model version
history, the run log still documented as v1, and `package-lock.json` left at 0.2.0 while the
other three version files said 0.3.0 — a fourth place the version lives, now named in the
checklist.*

---

## Cross-cutting rules (the `TODO` contract, restated)

- **TODO hygiene**: check items off *as they land*, so a usage-limit interruption leaves
  an accurate picture; add human-review items to **For human** as they arise.
- **Data model changes**: bump `SCHEMA_VERSION`, update `docs/dev/data-model.md`,
  regenerate the snapshot, adjust the sync tests *in the same change*; if there are
  migration consequences, the migration guide goes in the changelog. One bump is planned
  (Phase 7); no second bump may be in flight at the same time.
- **Both sides tested**: ingest ↔ site features (avatars, contributors) and server ↔
  browser features (wizard uploads, Done-flush) land unit *and* UI tests in the same
  change.
- **Changelog discipline**: every change lands its entry under `0.3.0 (unreleased)` in
  the right category in the same commit; 2–4 sentences per change.
- **Determinism**: no wall-clock values in `forge.json`; timestamps live in cacheDir
  sidecars; injected clocks for anything time-dependent (cooldown, backoff); skip paths
  (cooldown, same-hash, misses-first ordering) are byte-neutral by test.
- **Browser safety**: `format.ts` (which gains `licenseUrl`) and `listing.ts` stay free
  of node/config imports.
- **Usability over accessibility** where they collide (TODO rule) — but regressions in
  the existing a11y suite still fail the build; the rule governs new-work tradeoffs, not
  the gates.
- **The gates**: `npm test`, `npm run test:e2e`, `npm run check` green before every
  commit; the *full* suite specifically at the three checkpoints and release.

## Sequencing notes

- Phase 1 first (everything else tests on the updated deps). Phase 2 is independent of
  everything and parallelizable with Phase 1.
- Phase 3 before Phase 5 (run-log v2 is the cooldown's and hash-skip's data source).
  Phase 4 is independent of both but shares `src/lib/ingest/index.ts` with Phase 3 —
  parallel subagents need the stated merge point (Checkpoint 2 note).
- Phase 6's documentation half must follow Phases 3–5 (it documents them); its dev-script
  half could land any time but rides along.
- Phase 7 before Phase 8 (the wizard uploads into Phase 7's convention and exposes its
  keys). Phase 8 is last-before-release for the same reason it was in 0.2.0: "expose
  every setting this version added" only exists once the settings do.
- The 72-remote example config (`frznforge - Copy (3)`) is the reference corpus for
  Phase 4/5 acceptance and the Checkpoint 2 manual check — the features exist because of
  that scale.
