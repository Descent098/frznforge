# Release checklist

What has to be true before a version is announced. Down to the end of **Diagnostics** these are
commands to run; below that are claims no suite can make, because they need a real browser, a real
forge, or a reader who has never seen this repository.

Every command in this file was run against the tree it documents, and the transcripts are real.
That is not politeness. The 0.3.0 release shipped a guide whose first step named a config file
that had not existed for a version, and a reader who followed it built a site with zero pages — a
command nobody re-ran is exactly the failure this file exists to catch.

Boxes are reset each release. The italic notes under an item are history: what an earlier release
found when it did the check, kept because the finding is the reason the item is here.

---

## Blocking

- [ ] **The release commit is PUSHED, not merely committed.** `docs/user/quick-start.md` §1 and
      `docs/user/starting-a-site.md` both open with

      ```
      git clone https://github.com/Descent098/frznforge my-site
      ```

      so whatever that URL serves *is* the release, whatever the local tree says.

      ```sh
      git ls-remote --heads https://github.com/Descent098/frznforge
      git rev-parse HEAD
      git rev-list --count <the remote master sha>..HEAD    # must be 0
      ```

      `ls-remote` proves the repository **exists**. It does not prove it carries the version you
      are announcing, and that distinction is the whole item — an answering remote is what made
      this look fine in 0.2.0 and 0.3.0 while it was not. The count is the check; a clone of the
      URL is the confirmation.

      *0.1.0: the URL answered `remote: Repository not found.`, which broke both guides on their
      first command. Fixed for 0.2.0.*

      *0.3.0: `ls-remote` answered, so the documented clone worked — but the remote's `master` was
      at `fb7235b`, the **0.2.0** commit. None of the 0.3.0 work was pushed. This is exactly the
      case the paragraph above warns about, and it is the second release in a row it caught.*

      *0.4.0, run on the release candidate: **still not pushed, and now the damage is larger.**
      `git ls-remote --heads` answers*

      ```
      0f9931552197452a4321622349e2032a24ac3162	refs/heads/master
      23ab206b344a98a65f9cdaf5583da18716fc8897	refs/heads/v0.4.0
      ```

      *against a local `HEAD` of `9ef09f4`, four commits ahead. A shallow clone of that URL gets
      the 0.3.0 tree — `astro.config.ts`, `src/`, `scripts/`, `frznforge.config.ts`, `VERSION`
      reading `0.3.0`, and no `cmd/` directory at all:*

      ```
      $ git clone --depth 1 https://github.com/Descent098/frznforge published
      $ cat published/VERSION
      0.3.0
      $ ls published/cmd
      ls: cannot access 'published/cmd': No such file or directory
      ```

      *A reader following the 0.4.0 docs against that clone cannot run `go build ./cmd/frznforge`,
      because there is nothing to build. Push before announcing.*

- [ ] **`VERSION`, `package.json` and the `CHANGELOG.md` heading agree.** `VERSION` is the single
      source of truth, but `package.json` is what `npm run test:e2e` banners at anyone running the
      suite, and the two have drifted before (`0.0.1` against `0.1.0`, right up to release).

      ```sh
      cat VERSION
      grep -n '"version"' package.json package-lock.json | head -4
      head -1 CHANGELOG.md
      ```

      ```
      0.4.0
      package.json:5:  "version": "0.4.0",
      package-lock.json:3:  "version": "0.4.0",
      package-lock.json:9:      "version": "0.4.0",
      package-lock.json:19:      "version": "1.63.0",
      # 0.4.0 (unreleased)
      ```

      **Four places to move, not three.** `package-lock.json` carries the version twice — once at
      the root and once in the `""` entry for the project itself — and in 0.3.0 it drifted to
      `0.2.0` while the other three said `0.3.0`. Line 19 and below are dependency versions and
      are not yours to edit.

      Do not hand-edit the lockfile at all. It now describes exactly two dev dependencies
      (`@playwright/test`, `@types/node`), so after bumping `package.json`:

      ```sh
      npm install --package-lock-only
      ```

      rewrites lines 3 and 9 and touches nothing else — checked by bumping `0.4.0` → `0.4.1` in a
      copy: both lines moved, `1.63.0` and `26.4.1` stayed, and the file was still 81 lines.

      Also drop `(unreleased)` from the `CHANGELOG.md` heading.

- [ ] **No reader-facing guide still names the Node build.** This is the 0.3.0 failure with a
      command attached. `migrating.md` and `performance.md` quote 0.3.0 on purpose, so exclude
      them and read whatever is left:

      ```sh
      git grep -nE 'npm (run|test|install)|astro |vitest|scripts/|src/lib' -- \
        README.md docs/user docs/dev \
        ':!docs/dev/plans' ':!docs/user/migrating.md' ':!docs/dev/performance.md' \
        ':!docs/dev/release-checklist.md'
      ```

      Three files are excluded because they quote 0.3.0 on purpose: `migrating.md` is the upgrade
      table, `performance.md` holds the TypeScript baseline the Go numbers are measured against,
      and this file names the dead commands it replaced. Everything the grep still returns should
      be `npm run test:e2e` (which is real) or a sentence explaining that something is gone.
      Anything else is a guide that will not run.

      *0.4.0: this caught one. `docs/user/quick-start.md` told the reader to open
      `frznforge.config.jsonc` and then showed the contents as a TypeScript module —
      `import { defineConfig } from './src/lib/config/schema';` — which is the 0.3.0 failure
      repeating in the same guide. Fixed during the 0.4.0 documentation pass, by which point the
      grep had already earned its place here.*

---

## The gates

Five commands. All five green, on the release commit, before anything else in this file matters.

```sh
gofmt -l cmd internal                  # silence is the pass
go build ./internal/... ./cmd/...
go vet   ./internal/... ./cmd/...
go test  ./internal/... ./cmd/...
npm ci && npm run test:e2e
```

The Go suite takes about a minute and a half and prints one line per package:

```
ok  	frznforge/internal/build	29.341s
ok  	frznforge/internal/config	0.256s
ok  	frznforge/internal/highlight	1.874s
ok  	frznforge/internal/ingest	62.829s
ok  	frznforge/internal/model	0.691s
ok  	frznforge/internal/render	0.432s
…
ok  	frznforge/cmd/frzndebugger	0.754s
ok  	frznforge/cmd/frznforge	0.555s

real	1m30.226s
```

**Why the Go commands are scoped and not `./...`.** A built site under `dist/` contains the raw
source files of every repository it publishes. This project's own corpus publishes Go repositories,
so `dist/` holds hundreds of stray `.go` files inside this module, and `./...` walks into them:

```
$ go build ./...
malformed import path "frznforge/dist/repos/cpsc-utilities/raw/master/tower of hanoi": invalid char ' '
malformed import path "frznforge/dist/repos/projects-experiments/raw/master/Languages/Go/The tour": invalid char ' '
dist\repos\…\cmd\api\main.go:14:2: no required module provides package github.com/joho/godotenv/autoload
dist\repos\…\internal\server\routes.go:4:2: no required module provides package github.com/a-h/templ
```

Those are other people's programs, and none of them is going to build here. `./internal/... ./cmd/...`
is the module. If you want `./...` back for a session, build the site somewhere the Go tool skips —
`frznforge build --out=_dist` — because directories starting with `_` are ignored.

**There is no `npm run check`.** 0.4.0 removed TypeScript rather than upgrading it, so there is
nothing to type-check; `tsconfig.json` exists for editors and Playwright transpiles its own specs.
Do not go looking for the missing third command.

**`npm ci` before the e2e run.** `node_modules/` is not checked in, and the install is small
enough that there is no reason to skip it:

```
$ npm ci
added 5 packages, and audited 6 packages in 945ms
```

Playwright also needs its browsers on the machine. `npx playwright install --dry-run` prints the
builds it wants and the directory it looks for them in; `npx playwright install` is what puts them
there.

*0.4.0, on the release candidate: **`npm run test:e2e` is red, and it is the harness, not a spec.**
`config.MirrorDirName` (`internal/config/config.go:785`) now appends an 8-hex digest of the
source's identity to the mirror directory name; `mirrorDirName()` in `tests/e2e/global-setup.ts:178`
still builds the old digest-free name. The two seeded remote fixtures are therefore seeded under
names ingest does not look for, `charlie` and `delta` fall out of the artifact, and global setup
fails its own preflight before a single spec runs. The preflight names the fix in its error
message. Note the same digest is missing from the cache-layout tables in
`docs/dev/build-steps.md` and `docs/user/migrating.md`.*

---

## The deep run

Opt-in, and worth naming because nothing else in the suite does what it does:

```sh
FRZNFORGE_FULL_CORPUS=1 go test ./internal/build/ -v -timeout 90m
```

(`AGENTS.md`, `docs/dev/README.md` and the comment in `internal/build/sync_test.go:410` all still
say `-timeout 40m`, without `-v`. See below — that number is now too small.)

Without that variable, `internal/build`'s determinism and concurrency gates run against a fixture
they build themselves — which is what keeps them honest on a clean clone, and is why they are in
the ordinary gate at all. With it, they additionally render **this machine's real corpus twice**:
once to prove two runs of the same artifact emit identical bytes, once to prove the parallel build
and `--serial` agree. `TestBuildEmitsEveryRoute` also joins in, checking every route the router
predicts against every file the build actually wrote, in both directions.

Two builds of 73 repositories per test is why it carries an explicit timeout and why it is not in
the default gate: it once pushed `go test` past the ten-minute clock and failed on time rather
than on truth. Run it before a release, not on every commit. It skips silently on a machine with
no `data/forge.json`, so run `frznforge ingest` first or you will get a green result that checked
the fixture and nothing else.

**The documented `-timeout 40m` no longer fits the corpus. Use 90m, and pass `-v`.** Run as
written it panics on the clock, inside the last test:

```
$ FRZNFORGE_FULL_CORPUS=1 go test ./internal/build/ -timeout 40m
panic: test timed out after 40m0s
	running tests:
		TestBuildEmitsEveryRoute (4m58s)
…
FAIL	frznforge/internal/build	2400.102s
```

That is the clock, not a determinism failure — and without `-v` there is nothing in the output to
say so, which makes a 40-minute run worth nothing. The same suite with room to finish takes
**43 minutes** on this machine and passes:

```
$ FRZNFORGE_FULL_CORPUS=1 go test ./internal/build/ -v -timeout 90m
    determinism_test.go:79: 47183 files, byte-identical across two runs
--- PASS: TestBuildIsDeterministic (1022.69s)
    --- PASS: TestBuildIsDeterministic/fixture (1.20s)
    --- PASS: TestBuildIsDeterministic/this_repository (1019.67s)
    parallel_test.go:79: 47183 files identical between serial and 32 workers
--- PASS: TestSerialAndParallelAgree (1055.01s)
--- PASS: TestBuildEmitsEveryRoute (487.69s)
ok  	frznforge/internal/build	2579.310s
```

Two tests at ~17 minutes each is where it goes, and both spend it the same way: they render 73
repositories to 47,183 files, twice, and hash every one. That is the claim — 47,183 files
byte-identical across two runs, and 47,183 files identical between `--serial` and 32 workers — and
it is not a claim a fixture of 67 files can make. The 40m in `internal/build/sync_test.go:410` was
written when the corpus was smaller; 43 against 40 is a coin flip, so give it 90 and stop thinking
about it.

---

## The reader path

This replaces the `docker run node:24` container the 0.2.0 and 0.3.0 checklists used, which
installed Node and ran a build script that no longer exists.

The 0.4.0 claim is **Go ≥ 1.24 and git, and nothing else** — no Node, no `npm install`, no
lockfile, no bundler. It is a claim about a machine, so check it on one: a clean clone, a Go
toolchain, git, and nothing installed to help.

```sh
git clone https://github.com/Descent098/frznforge my-site   # or the release commit, if it is not pushed yet
cd my-site
go build ./cmd/frznforge

rm -rf content frznforge.config.jsonc README.md
./frznforge new . --force
# edit frznforge.config.jsonc: uncomment the "local" example and point it at a repository
./frznforge build
./frznforge dev
```

Run it in a temp directory, not beside your own site — `new` writes into the directory it is
given, and `build` deletes and rewrites `dist/`.

A real run of exactly that, with the clone itself as the one repository:

```
$ ./frznforge new . --force
Scaffolded a frznforge site in …\my-site.

  + frznforge.config.jsonc                 site config — start here
  + content/profile.md                     your profile page
  + content/notes/welcome.md               an example note (safe to delete)
  + content/orgs/example-org.md.example    an example org page (inert until renamed)
  + README.md                              your site’s README, not frznforge’s
  = .gitignore                             already existed — left untouched

$ ./frznforge build
frznforge build: scanning first — pass --no-ingest to render the artifact on disk instead.
frznforge ingest → …\my-site\data
  ▸ frznforge
    ✓ frznforge: 34 commits, 1 branches, 0 tags, 437 files
done: 1 repo(s), 1 note(s), 429 blob(s), 1 archive(s), 1 warning(s) in 3323ms

built 1100 files (84.3 MB) in 7.983s
```

Three things to actually look at, because each of them fails quietly rather than loudly:

- **`web/` came with the clone.** It is copied out of the project directory into `dist/`, not
  embedded in the binary, so a site that does not carry it builds without complaint and serves
  pages with no stylesheet and no command palette. Open the served page; do not just read the
  file count.
- **What `frznforge new` printed as step 1.** In a clone `web/` is already there, so the steps
  start at "edit the config". Run `new .` in an empty directory instead and step 1 must be the
  engine, which is the only warning a site assembled by copying will ever get:

  ```
    1. Put the frznforge engine where this site can reach it:
       - the `frznforge` binary on your PATH (go build -o frznforge ./cmd/frznforge in a
         frznforge checkout)
       - that checkout's web/ copied into . — it is served verbatim, and without it
         the site builds fine and then has no styles and a dead listing
  ```
- **The second build is much faster, and no `highlight/` directory appears.** Running the same
  build again on the same clone: ingest `3323ms → 145ms` (the scan cache replayed), render
  `7.983s → 5.204s`. The cache holds `last-run.json` and `scan/` and nothing else. 0.4.0 ships
  **no highlight memo**; `<ingest.cacheDir>/highlight/` is dead weight left by 0.3.0 and every
  other doc now tells the reader to delete it (`internal/highlight/highlight.go:15`). If a second
  build is not faster, look at `ingest.reuse.enabled` and the scan cache — not at a directory that
  no longer exists.

---

## The wizard

- [ ] **`frznforge init --web` round-trips the config with its comments intact.** The config is
      roughly 60% comments and those comments are its documentation, so a wizard that reformats
      the file destroys the thing the file is for. The splice editors are covered by unit tests
      and by `tests/e2e/wizard.spec.ts`, but this is the page you will actually live in.

      A headless round-trip you can run in a scratch site, which checks the invariant without a
      browser:

      ```sh
      grep -c '//\|/\*' frznforge.config.jsonc      # comment lines, before
      sha256sum frznforge.config.jsonc

      frznforge init --web --no-open --port=4400    # prints http://127.0.0.1:4400/?s=<key>
      curl -s -X POST "http://127.0.0.1:4400/api/config/write?s=<key>" \
        -H 'Content-Type: application/json' \
        -d '{"operations":[{"op":"set","path":"site.title","value":"Release smoke test"}]}'
      curl -s -X POST "http://127.0.0.1:4400/api/done?s=<key>"

      grep -c '//\|/\*' frznforge.config.jsonc      # comment lines, after — must be the same
      diff frznforge.config.jsonc.*.bak frznforge.config.jsonc
      ```

      ```
      52
      {"backup":"…\\frznforge.config.jsonc.20260907T103002Z.bak","changed":true,…}
      {"done":true,"writes":1}
      52
      14c14
      <     "title": "My forge",
      ---
      >     "title": "Release smoke test",
      ```

      One line changed, 52 comment lines before and after, and the `.bak` holds the pre-wizard
      file. Then `frznforge build --no-ingest` on the result, which is the check that the loader
      still accepts what the wizard wrote.

      Then do it in the browser, because the API is not the surface anyone uses: edit a setting,
      add and remove an organization, edit an organization **in place**, upload a picture for the
      owner and for an organization, add a contributor, set a cooldown, edit the profile body —
      then make one edit you deliberately **do not** save and press **Done**. It must save that
      edit rather than discard it. Confirm the config diff touches only the fields you changed.

---

## Diagnostics

- [ ] **A build leaves its evidence, and `frzndebugger` opens it.** Every run of `build`, `ingest`
      and `dev` writes two files into `<ingest.outDir>` whether or not anybody asked, because the
      run that goes wrong is the one nobody was watching. After a build:

      ```sh
      ls data/frznforge.log data/frznforge-timings.jsonl
      frzndebugger --plain
      ```

      ```
      frzndebugger  …\my-site\data
        log:     …\data\frznforge.log (1327 records)
        timings: …\data\frznforge-timings.jsonl (24 steps)

      == no unfinished steps: every step that started also finished

      == timings
        run: 20260907T102845Z-8568 (1 of 1)  5.27s wall, 24 steps
        step                                             n     totalv       mean       best      worst  fail
        - run build                                      1      5.27s      5.27s      5.27s      5.27s     .
          - build.site site                              1      5.27s      5.27s      5.27s      5.27s     .
            - build.repos site                           1      4.68s      4.68s      4.68s      4.68s     .
      …
      ```

      Both binaries have to be built for this: `go build ./cmd/frznforge && go build ./cmd/frzndebugger`.

      Then check the two files have opposite lifetimes, which is the design and the thing that
      quietly breaks. Build three times and run `frzndebugger --plain --run=all`: the timings line
      reads `run: all (3 in file)` while the log still holds only the last run. The log is
      truncated per run because the question is "what did the run that just failed do"; the
      timings file is appended because comparing runs is the only way to answer "what was slow".

      Neither file may ever appear in `dist/`. Check that too; it is one `ls`.

- [ ] **`frznforge dev` writes `frznforge-dev.log` and leaves `frznforge.log` alone.** Its own
      check, because it is the one that fails quietly: the loop is build-then-preview, the log is
      truncated per run, so one shared name means the command you run to *look* at the site erases
      the record of the command that built it. Build, note the log, then preview:

      ```sh
      frznforge build
      md5sum data/frznforge.log
      frznforge dev --port=4455        # in another shell; Ctrl-C when done
      ls data/*.log                    # frznforge.log AND frznforge-dev.log
      md5sum data/frznforge.log        # unchanged
      ```

      *0.4.0, on the release candidate: **this fails.** No `frznforge-dev.log` is created, and
      `data/frznforge.log` comes back with four records whose first line reads `command=dev` — the
      build's log, gone. `startDiagnostics` does pick the right name
      (`cmd/frznforge/diagnostics.go:60`), but `logging.SetupFileNamed`
      (`internal/logging/file.go:119`) ignores the `name` argument it is handed and opens
      `LogPath(outDir)` at line 127, which is always `frznforge.log`. `LogPathNamed` exists three
      lines above it and is unused. Every doc that describes the split — `AGENTS.md`,
      `docs/dev/README.md`, `docs/dev/build-steps.md` §4 — describes intended behaviour the binary
      does not have.*

---

## By hand, in a real browser

Each of these ships behaviour that only a real browser against a real deploy can confirm. The
suites cover them against fixtures, which is not the same thing. They entered in the version
named; they have to be re-checked when the renderer changes, and in 0.4.0 the renderer changed
completely.

### Entered in 0.2.0

- [ ] **A hosted static site.** Configure `hosting.sites` for a repo with a `gh-pages` branch,
      `frznforge build`, `frznforge dev`, and open `/<slug>/`: the site's own relative links, CSS
      and JS must work, and `/repos/<slug>/` must still show the normal forge view with `gh-pages`
      browsable. The e2e suite asserts content types and one script running; it cannot tell you
      the site *looks* right.
- [ ] **A sub-path deploy.** Build with `"base": "/mysite"` under `site`, then `frznforge dev` —
      it reads `site.base` from the config and serves there, no flag needed:

      ```
      serving …\my-site\dist on http://localhost:4456/mysite/
      ```

      `/mysite/` answers 200 and `/` answers 404, which is the right pair. Then click the sidebar,
      a deep blob link, an archive download and the command palette.
      `tests/e2e/base-path.spec.ts` scans built HTML for root-absolute leaks, but only a real
      deploy proves the *host* serves the prefix the way the recipe in `docs/user/deploying.md`
      assumes.
- [ ] **Mermaid diagrams in both themes.** Open a page with a diagram, toggle light/dark, and
      confirm the diagram re-renders legibly — not just that an `<svg>` exists — and that a page
      with no diagram still loads no mermaid chunk (devtools Network).

### Entered in 0.3.0

- [ ] **The clone popup, at desktop and phone widths.**

      *0.3.0, verified by hand at 1200px and 375px: the panel opens at its full 400px (a plain
      GitHub clone URL fits with no ellipsis), nothing overflows it, and at phone width it anchors
      to the toolbar and stays inside both edges. The Playwright pair asserts the geometry; the
      eyeball confirmed it looks right. Still worth a glance on a real phone rather than an
      emulated viewport.*

- [ ] **Avatars render where they should.** An `owner.avatar` in the profile hero (128px) and the
      sidebar tile (34px), right radius and crop, and unset avatars still drawing the initials
      block.

      *The 0.3.0 reason for this being an eyeball item has expired: the e2e site no longer builds
      from the checked-in project config but from `tests/e2e/fixture.config.jsonc`, which does set
      `owner.avatar`. What has not changed is that no spec asserts the profile hero image
      (`tests/e2e/orgs.spec.ts:44`), and that a passing `<img>` assertion would not tell you the
      crop is right. `theme.heat` is still absent from the fixture config entirely.*

- [ ] **A rate-limited build, for real.** The 429 backoff, misses-first ordering and the cooldown
      are covered only against fixtures and an injected clock. Run a real `frznforge build` over a
      large corpus — **this repository's own config is that corpus: 72 GitHub sources and one
      local** — and watch: repos with no cached metadata go first, a 429 backs off per *host*
      rather than per repo, and a second run inside `ingest.reuse.cooldownSeconds` prints
      `⚠️ <repo>: this repo is on cooldown` and finishes quickly.

      `frznforge build --log=debug 2> build.log` is how to see it, since every HTTP request is
      logged before it starts as well as after it finishes. This is the scenario the whole feature
      exists for and it has never been exercised end to end.

- [ ] **`ingest.reuse.skipUnchanged` against a live remote.** Set it, build twice, and confirm the
      second run reports `current` for untouched repos. Then push a commit to one of them and
      confirm that repo alone re-fetches. It saves *git* traffic, not API traffic — the metadata
      call happens before the mirror update.

### Entered in 0.4.0

- [ ] **Code colours, in both themes.** Highlighting moved from Shiki's inline custom properties
      to chroma's token classes, coloured once in `web/css/repo.css` under `[data-theme]`, so the
      light/dark switch on code is now the same mechanism as the rest of the page. Open a file,
      toggle the theme, and read it. Then open a `.cfg` or `.conf` file and confirm it has colour
      at all — that pair rendered plain under 0.3.0, and the fix is a language-name map that a
      test now checks in both directions.
- [ ] **File table ordering.** The old renderer sorted with `localeCompare`, which depends on the
      build machine's ICU data; the Go renderer orders by code point. `config.go` must now sort
      **before** `config_test.go`. One glance at any repo's file table settles it.
- [ ] **The command palette offers no file that 404s.** The 0.3.0 search index listed every blob
      path, including ones holding `#` or `%`, which get no page because no static URL can
      round-trip them. Ctrl-K, type a fragment of such a path, and confirm nothing unclickable
      comes back.

---

## Worth doing

- [ ] **Build this project's own site and click through it.** `frznforge build`, `frznforge dev`,
      then `/`, a repo overview, a file, `/repos/<slug>/insights/`, `/notes/`, `/orgs/` in both
      themes. With 72 GitHub sources this is also the only thing left that exercises the live
      provider APIs — `npm run smoke:remote` is gone, and every Go test uses fixtures and never
      touches the network, so a provider that changed a field shape shows up here or nowhere.
- [ ] **`frznforge verify` on the artifact that build produced.** It re-serializes and compares
      byte for byte, which is the cheapest possible check that the schema and the writer still
      agree:

      ```
      $ frznforge verify
      ok data\forge.json — 25005457 bytes, schema v8, 73 repos, 5 notes, 1 orgs, re-serialized byte for byte
      ```

      The counts are the other half of the check: 73 repos is the whole corpus, and a number that
      dropped is a source that failed to ingest and warned instead of erroring.

- [ ] **Re-read `docs/user/quick-start.md` against a real run.** The transcripts in it are
      literal, so a change to the ingest summary line, the sidebar or the repo page makes them
      wrong silently. This is the guide that broke in 0.3.0.
- [ ] **Delete `<ingest.cacheDir>/highlight/` if an upgraded site still has one.** Nothing writes
      it, nothing reads it, nothing prunes it, and on a 73-repository corpus it is megabytes of
      gzipped HTML fragments. `docs/user/migrating.md` §4 tells readers the same thing.
