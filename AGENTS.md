## Development

`npm run dev` (`scripts/dev.ts`) prints a notice and runs `astro preview` over `dist/`: it serves the **most recent `npm run build`** and rebuilds nothing. It exits 1 with instructions if `dist/` or `data/forge.json` is missing. To see a code or content change, run `npm run build`.

The raw Astro dev server (HMR, but `loadForgeData` memoises `data/forge.json` for the process lifetime, so a re-ingest 404s until restart) is `npm run astro dev`. When starting it, use background mode:

```
astro dev --background
```

Manage the background server with `astro dev stop`, `astro dev status`, and `astro dev logs`.

## Documentation

Full documentation: https://docs.astro.build

Consult these guides before working on related tasks:

- [Adding pages, dynamic routes, or middleware](https://docs.astro.build/en/guides/routing/)
- [Working with Astro components](https://docs.astro.build/en/basics/astro-components/)
- Browser code: `web/` (no framework, no build step — see the house rule below)
- [Adding or managing content](https://docs.astro.build/en/guides/content-collections/)
- [Adding styles or using Tailwind](https://docs.astro.build/en/guides/styling/)
- [Supporting multiple languages](https://docs.astro.build/en/guides/internationalization/)

## Project layout & commands

- `frznforge.config.ts` — site config (owner, repos to ingest, palette). `content/profile.md` — profile page.
- `npm run ingest` → `data/forge.json` + `data/blobs/` (gitignored). `npm run build` = ingest + `astro build`.
- `src/lib/data/schema.ts` is the ingest ↔ site contract (zod). Bump `SCHEMA_VERSION` + `docs/dev/data-model.md` + snapshots on any change.
- `src/lib/ingest/*` reads git via the CLI only (never the working tree). `src/lib/{site,routes,markdown}.ts` are site helpers.
- `web/` is the browser half and is served **verbatim** — plain ES modules, no transpile, no bundler, relative imports with `.js` extensions. `web/js/{format,listing,search,base}.js` are THE implementations; `src/lib/{format,listing,search,base}.ts` re-export them and add the parts that take artifact types. Never fork one into a second copy. `web/vendor/` is pre-built third-party code (mermaid); it is excluded from `tsconfig.json` because `allowJs` would otherwise parse 3.5 MB of minified JS on every `astro check`.
- Styles: plain CSS only, `hf-` prefix, tokens at the top of `web/css/global.css` (site-wide) + `web/css/repo.css` (repo sub-pages). No Tailwind/Sass.

## Testing

- `npm test` — vitest unit tests (`tests/unit`), fixture git repos in temp dirs.
- `npm run test:e2e` — Playwright; builds the site from fixture repos into `tests/.tmp/e2e` first.
- `npm run check` — `astro check`. Keep all three green before committing.
- Plans & phase checklist: `docs/dev/plans/plan-phases.md`.

