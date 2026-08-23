## Development

When starting the dev server, use background mode:

```
astro dev --background
```

Manage the background server with `astro dev stop`, `astro dev status`, and `astro dev logs`.

## Documentation

Full documentation: https://docs.astro.build

Consult these guides before working on related tasks:

- [Adding pages, dynamic routes, or middleware](https://docs.astro.build/en/guides/routing/)
- [Working with Astro components](https://docs.astro.build/en/basics/astro-components/)
- [Using React, Vue, Svelte, or other framework components](https://docs.astro.build/en/guides/framework-components/)
- [Adding or managing content](https://docs.astro.build/en/guides/content-collections/)
- [Adding styles or using Tailwind](https://docs.astro.build/en/guides/styling/)
- [Supporting multiple languages](https://docs.astro.build/en/guides/internationalization/)

## Project layout & commands

- `frznforge.config.ts` — site config (owner, repos to ingest, palette). `content/profile.md` — profile page.
- `npm run ingest` → `data/forge.json` + `data/blobs/` (gitignored). `npm run build` = ingest + `astro build`.
- `src/lib/data/schema.ts` is the ingest ↔ site contract (zod). Bump `SCHEMA_VERSION` + `docs/dev/data-model.md` + snapshots on any change.
- `src/lib/ingest/*` reads git via the CLI only (never the working tree). `src/lib/{site,format,listing,routes,markdown}.ts` are site helpers; `format.ts` and `listing.ts` must stay browser-safe (used by Svelte islands).
- Styles: plain CSS only, `hf-` prefix, tokens at the top of `src/styles/global.css` (site-wide) + `src/styles/repo.css` (repo sub-pages). No Tailwind/Sass.

## Testing

- `npm test` — vitest unit tests (`tests/unit`), fixture git repos in temp dirs.
- `npm run test:e2e` — Playwright; builds the site from fixture repos into `tests/.tmp/e2e` first.
- `npm run check` — `astro check`. Keep all three green before committing.
- Plans & phase checklist: `docs/dev/plans/plan-phases.md`.

