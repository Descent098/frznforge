# frznforge user documentation

frznforge turns a pile of git repositories into a static, read-only forge site: browsable
history, file trees, releases, notes and a profile page, rendered once at build time and served
as plain files. No server, no database, no accounts.

Building one needs **Go ≥ 1.24 and git**, and nothing else. The engine is a single binary —
`go build ./cmd/frznforge` — plus the `web/` directory it copies into the output verbatim.

Six guides, in reading order.

1. **[Quick start](./quick-start.md)** — clone, point it at a repository, ingest, run it.
   Ten minutes to a site on `localhost`, including a profile page and a first note.

2. **[Starting a site](./starting-a-site.md)** — `frznforge new`, the six files it scaffolds,
   where the engine has to live, and how to keep your content in its own repository rather than
   inside the engine's clone.

3. **[Configuration](./configuration.md)** — every key in `frznforge.config.jsonc` with its
   default, the `.frznforge.json` you commit inside a repository, profile and organization
   frontmatter, the notes folder, the `postprocess` hook, the diagnostics every run leaves
   behind, and what each of the 26 warning codes means.

4. **[Importing from a forge](./importing.md)** — publishing repositories hosted on GitHub,
   GitLab, Gitea or Forgejo: the `init` command (terminal and `--web`), API tokens, the mirror
   cache, rate limits, offline builds, and how untrusted markdown is handled.

5. **[Deploying](./deploying.md)** — what `frznforge build` produces, how big it gets and how to
   make it smaller, a working GitHub Actions workflow, and the exact settings for Cloudflare
   Pages, Netlify, nginx, Apache, Caddy and S3.

6. **[Migrating](./migrating.md)** — two migrations. Part A upgrades an existing site from
   frznforge 0.3.0 to 0.4.0, where the engine became a Go binary and the config became JSONC.
   Part B is coming *off* a hosted forge: what carries over and what does not (no issues, no
   pull requests, no stars, no CI), how releases map, what happens to private repositories, and
   URL equivalents.

## The commands, in one place

```
frznforge build [--no-ingest] [--no-cache] [--backfill-metadata] [--postprocess=<cmd>]
frznforge ingest [--no-cache] [--backfill-metadata]
frznforge dev [--port=<n>] [--dir=<path>]
frznforge init [--web] [--provider=…] [--account=…] [--select=…]
frznforge new <dir> [--force] [--dry-run]
frznforge verify [<forge.json>]
frznforge config migrate [--force]
```

`build` is ingest plus render and is the whole build; `dev` serves the last one and rebuilds
nothing. Every command takes `--log=<error|warn|info|debug>`, which writes to stderr while
progress stays on stdout. A second binary, `frzndebugger` (`--web`, `--plain`), reads the run
log and timings file that `build`, `ingest` and `dev` leave in `data/`.

Upgrading from 0.3.0? `npm run <x>` became `frznforge <x>` — the table is in
[migrating.md §3](./migrating.md#3-npm-run-x-becomes-frznforge-x).

## Developer documentation

The Go package layout, the ingest ↔ site data contract, the build steps, performance
measurements and the phase plans live in [`../dev/`](../dev/).
