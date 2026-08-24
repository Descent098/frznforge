# Release checklist

Things that cannot be verified from inside this repository, and so have to be done by hand
before a version is announced. Everything else is covered by `npm test`, `npm run test:e2e`
and `npx astro check`.

## Blocking

- [ ] **Publish the repository at the URL the docs tell people to clone.**
      `docs/user/quick-start.md` §1 and `docs/user/starting-a-site.md` both open with

      ```
      git clone https://github.com/Descent098/frznforge my-forge
      ```

      and that URL currently resolves to `remote: Repository not found.` — verified with
      `git ls-remote`, from a machine whose access to github.com is otherwise fine. A stranger
      following either guide fails on its first command, and every step after it is
      unreachable. The same URL is `links.homepage` / `links.issues` / `links.upstream` in this
      repo's own `.frznforge.json`, so the demo site's Code panel advertises a clone URL that
      404s.

      Either publish it there, or change all five places to the real public URL. Then verify
      the way a reader would:

      ```
      docker run --rm -it node:24 bash -c 'git clone <the documented url> my-forge && cd my-forge && npm install && npm run build'
      ```

- [ ] **`package.json` version still matches `VERSION`.** `VERSION` is documented as the
      single source of truth, but `package.json` is what every `npm run …` banners at the
      reader (`> frznforge@0.1.0 ingest`), and the two had drifted (`0.0.1` against `0.1.0`)
      right up to release. Bump `package.json`, `VERSION` and the `CHANGELOG.md` heading in the
      same commit, every time.

## Worth doing

- [ ] Run `npm run smoke:remote` once against the live providers (it is the only thing that
      exercises real GitHub / GitLab / Gitea / Forgejo APIs; CI uses recorded fixtures).
- [ ] Build this project's own site (`npm run build`) and click through
      `/`, a repo overview, a file, `/insights/`, `/notes/`, `/orgs/` in both themes.
- [ ] Re-read `docs/user/quick-start.md` against a real run. The transcripts in it are
      literal, so a change to the ingest summary line, the sidebar or the repo page makes them
      wrong silently.
