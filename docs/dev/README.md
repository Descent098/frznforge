# Developer docs

Notes for working *on* frznforge. If you are trying to publish a site with it, start at
[docs/user/](../user/README.md) instead.

| Guide | |
|---|---|
| [Build steps](./build-steps.md) | What `npm run build` does, step by step: ingest, the remote path and its caches, the artifact, `astro build` |
| [Data model](./data-model.md) | `forge.json` — the ingest ↔ site contract, and the rules for changing it |
| [Build performance](./performance.md) | Where the time and the bytes go, measured, plus the knobs and the ideas that were rejected |
| [Release checklist](./release-checklist.md) | What to run and check before cutting a version |
| [Plans](./plans/) | Phase plans and the cross-cutting rules each version is held to |

## House rules

- `src/lib/data/schema.ts` is the boundary between ingest and the site. Any change to it bumps
  `SCHEMA_VERSION` and updates [data-model.md](./data-model.md) and the snapshots, in the same
  change.
- `src/lib/ingest/*` reads git through the CLI only, never the working tree.
- `web/` is served verbatim to the browser: plain ES modules, no transpile, no bundler,
  relative imports carrying their `.js` extension. `web/js/{format,listing,search,base}.js`
  are the single implementations; `src/lib/*.ts` re-export them and add the artifact-typed
  helpers the browser never needs. Anything in `web/js/` must stay free of node and config
  imports — it is loaded straight by a browser.
- Plain CSS only, `hf-` prefix, tokens at the top of `web/css/global.css` and
  `web/css/repo.css`.
- `npm test`, `npm run test:e2e` and `npm run check` all stay green, and no test touches the
  network.

## The three commands

```sh
npm run ingest   # git + forge APIs → data/forge.json, data/blobs/, data/archives/
npm run build    # ingest, then astro build → dist/
npm run dev      # serve the last build (astro preview); rebuilds nothing
```

`npm run dev` deliberately does **not** run Astro's dev server: `loadForgeData` memoises the
artifact for the life of the process, so a dev server keeps serving whatever data existed when
it started. `npm run astro dev` is still there if you want HMR on components and styles and
can live with that. The details are in [build-steps.md](./build-steps.md).
