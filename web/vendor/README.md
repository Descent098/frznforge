# Vendored browser assets

Third-party code that ships to the browser **as published**, copied here rather than pulled
through a bundler. The 0.4.0 build has no bundler — everything under `web/` is served byte for
byte — and the published pages call no third-party host, so a CDN is not an option either.

Nothing in this folder is edited. To refresh one, bump the package in `package.json`,
`npm install`, re-run the copy below, and note the new version here.

## mermaid 11.17.2

Client-side renderer for ` ```mermaid ` fences. Loaded lazily by `web/js/mermaid.js` — only
once a diagram nears the viewport, and only on pages whose markdown actually contains one
(`containsMermaid()` in `src/lib/markdown.ts` decides).

Source: `node_modules/mermaid/dist`, the **`esm.min`** variant — the one mermaid publishes for
browsers, with its dependencies (cytoscape, katex, d3, …) already inside it and every import
relative, so a browser can load it directly. `mermaid.core.mjs` is the *bundler* variant and
leaves bare specifiers behind; it does not work here.

```sh
cp node_modules/mermaid/dist/mermaid.esm.min.mjs web/vendor/mermaid/
cp node_modules/mermaid/dist/chunks/mermaid.esm.min/*.mjs web/vendor/mermaid/chunks/mermaid.esm.min/
```

104 files, ~3.5 MB on disk. That sounds like a lot next to the rest of the site, and it is the
same payload the Vite build produced before 0.4.0 (`dist/_astro/` was 3,495,584 bytes, of which
3.42 MB was mermaid split across ~97 chunks). It is also *lazy*: the entry pulls only the
chunks the diagrams on the page actually need, so a reader who never opens a page with a
diagram downloads none of it.
