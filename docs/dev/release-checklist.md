# Release checklist

Things that cannot be verified from inside this repository, and so have to be done by hand
before a version is announced. Everything else is covered by `npm test`, `npm run test:e2e`
and `npx astro check`.

## Blocking

- [x] **Publish the repository at the URL the docs tell people to clone.**
      `docs/user/quick-start.md` §1 and `docs/user/starting-a-site.md` both open with

      ```
      git clone https://github.com/Descent098/frznforge my-forge
      ```

      For 0.1.0 that URL resolved to `remote: Repository not found.`, which broke both guides
      on their first command. **Resolved for 0.2.0** — `git ls-remote` against it now answers
      with real refs, so the documented clone works and the `links.*` URLs in this repo's own
      `.frznforge.json` point somewhere real. Re-check it every release, the way a reader
      would:

      ```
      docker run --rm -it node:24 bash -c 'git clone <the documented url> my-forge && cd my-forge && npm install && npm run build'
      ```

      (Still worth confirming by hand that the *release commit itself* is pushed: `ls-remote`
      proves the repository exists, not that it carries the version you are announcing.)

      **0.3.0: re-checked, and this is exactly the case the parenthesis warns about.**
      `git ls-remote` answers, so the documented clone works — but the remote's `master` is
      at `fb7235b`, the **0.2.0** commit. None of the 0.3.0 work is pushed. The 0.3.0 work
      is committed locally and must be pushed before the release is announced, or a reader
      following `quick-start.md` gets 0.2.0 while the docs describe 0.3.0.

- [x] **`package.json` version still matches `VERSION`.** `VERSION` is documented as the
      single source of truth, but `package.json` is what every `npm run …` banners at the
      reader (`> frznforge@0.1.0 ingest`), and the two had drifted (`0.0.1` against `0.1.0`)
      right up to release. Bump `package.json`, `VERSION` and the `CHANGELOG.md` heading in the
      same commit, every time. **Four** places, not three — `package-lock.json` carries a
      root `version` too, and it drifted to `0.2.0` while the other three said `0.3.0`.
      *0.2.0: all three read `0.2.0`. 0.3.0: `VERSION`, `package.json`, `package-lock.json`
      and the `CHANGELOG.md` heading all read `0.3.0` — verified.*

## 0.2.0 additions

Each of these ships behaviour that only a real browser against a real deploy can confirm; the
suites cover them against fixtures, which is not the same thing.

- [ ] **A hosted static site, in a real browser.** Configure `hosting.sites` for a repo with a
      `gh-pages` branch, `npm run build`, serve `dist/`, and open `/<slug>/`: the site's own
      relative links, CSS and JS must work, and `/repos/<slug>/` must still show the normal
      forge view with `gh-pages` browsable. The e2e suite asserts content types and one script
      running; it cannot tell you the site *looks* right.
- [ ] **A sub-path deploy, in a real browser.** Build with `site.base: '/mysite'`, serve it
      under that prefix, and click the sidebar, a deep blob link, an archive download and the
      command palette. `tests/e2e/base-path.spec.ts` scans built HTML for root-absolute leaks,
      but only a real deploy proves the *host* serves the prefix the way the recipe in
      `docs/user/deploying.md` assumes.
- [ ] **Mermaid diagrams in both themes.** Open a page with a diagram, toggle light/dark, and
      confirm the diagram re-renders legibly (not just that an `<svg>` exists) — and that a
      page with no diagram still loads no mermaid chunk (devtools Network).
- [ ] **The `--web` wizard end to end.** `npm run frznforge -- init --web`, then: edit a
      setting, add and remove an organization, edit the profile body, press **Done**. Confirm
      the config diff touches only the fields you changed, the `.bak` holds the pre-wizard file,
      and `npm run build` still succeeds on the result.
- [ ] **The rebuild claim.** `npm run build` twice and confirm the second is materially faster
      (0.2.0 measured 23.1 s → 7.9 s on the self-build — the numbers in performance.md). If it
      is not, the highlight memo is
      not being hit — check `<ingest.cacheDir>/highlight/` exists and `ingest.reuse.enabled` is
      true.

## 0.3.0 additions

- [x] **The clone popup, at desktop and phone widths.** Verified by hand in a real browser at
      1200px and 375px: the panel opens at its full 400px (a plain GitHub clone URL fits with
      no ellipsis), nothing overflows it, and at phone width it anchors to the toolbar and
      stays inside both edges. The Playwright pair asserts the geometry; the eyeball confirmed
      it *looks* right. Still worth a glance on a real phone rather than an emulated viewport.
- [x] **Avatars render where they should.** Verified by hand: an `owner.avatar` shows in the
      profile hero (128px) and the sidebar tile (34px) with the right radius and crop, and
      unset avatars still draw the initials block. Note `owner.avatar` has no e2e assertion by
      design — the e2e site build reads the checked-in `frznforge.config.ts`, so a fixture
      cannot inject it (the same limitation `theme.heat` has).
- [ ] **The wizard's 0.3.0 surface, by hand.** `npm run frznforge -- init --web`, then: upload
      a picture for the owner and for an organization, edit an organization *in place*, add a
      contributor, set a cooldown — then make one edit you deliberately DO NOT save and press
      **Done**; it must save it rather than discard it. Confirm the config diff touches only
      the fields you changed and that `npm run build` succeeds on the result. Covered by unit
      and Playwright tests, but this is the page you will actually live in.
- [ ] **A rate-limited build, for real.** The 429 backoff, misses-first ordering and the
      cooldown are covered only against fixtures and an injected clock. Run a real
      `npm run build` over a large corpus (the `frznforge - Copy (3)` config, 72 GitHub repos)
      and watch: repos with no cached metadata should go first, a 429 should back off per host
      rather than per repo, and a second run inside `ingest.reuse.cooldownSeconds` should print
      `⚠️ <repo>: this repo is on cooldown` and finish quickly. This is the scenario the whole
      feature exists for and it has never been exercised end to end.
- [ ] **`ingest.reuse.skipUnchanged` against a live remote.** Set it, build twice, and confirm
      the second run reports `current` for untouched repos. Then push a commit to one of them
      and confirm that repo alone re-fetches. (Note it saves *git* traffic, not API traffic —
      the metadata call happens before the mirror update.)

## Worth doing

- [ ] Run `npm run smoke:remote` once against the live providers (it is the only thing that
      exercises real GitHub / GitLab / Gitea / Forgejo APIs; CI uses recorded fixtures).
- [ ] Build this project's own site (`npm run build`) and click through
      `/`, a repo overview, a file, `/repos/<slug>/insights/`, `/notes/`, `/orgs/` in both
      themes.
- [ ] Re-read `docs/user/quick-start.md` against a real run. The transcripts in it are
      literal, so a change to the ingest summary line, the sidebar or the repo page makes them
      wrong silently.
