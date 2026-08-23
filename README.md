# frznforge

A **read-only, static forge** for one person. Point it at your git repositories, run a
build, and host the output anywhere static files go. No server, no accounts, no issues,
no pull requests, no stars — just your projects, browsable and cloneable.

It is for **source-available** projects, and for people who self-host a private forge
(Forgejo, Gitea, GitLab…) but want a public, safe, always-up showcase of their work.

- **Ingest** scans local git repositories (concurrently) into a JSON artifact — committed
  content only, never your working tree.
- **Build** turns that artifact into a fully static Astro site: profile page, repository
  listing with search/filter/sort, and a repository overview per repo.
- **Deploy** the `dist/` folder to GitHub Pages, Cloudflare Pages, Netlify, S3, a NAS…

> Status: early. See [docs/dev/plans/plan-phases.md](docs/dev/plans/plan-phases.md) for
> what exists and what's next.

## Quick start

```bash
npm install
# edit frznforge.config.ts: add your repos + owner details
# edit content/profile.md: bio, links, pinned repos
npm run ingest     # git → data/forge.json (+ blobs)
npm run dev        # or: astro dev --background
npm run build      # = ingest + astro build → dist/
```

## Configuration

Everything lives in [`frznforge.config.ts`](frznforge.config.ts) (site title, owner, palette,
repos, ingest limits) and [`content/profile.md`](content/profile.md) (profile frontmatter +
body). Per-repo metadata (description, links, tags, template flag, license) comes from a
`.frznforge.json` committed inside each repo, overridable from the site config. See
[docs/user/configuration.md](docs/user/configuration.md).

## Development

```bash
npm test            # vitest unit tests (ingest extractors, listing logic, schema)
npm run test:e2e    # playwright, builds the site from a fixture artifact
npm run check       # astro check
```

Data model: [docs/dev/data-model.md](docs/dev/data-model.md). Design explorations that led
to the current look: [docs/dev/plans/design-options](docs/dev/plans/design-options).

Versioning: `VERSION` is the single source of truth; every change is logged in
[CHANGELOG.md](CHANGELOG.md).

## License

MIT — see [LICENSE](LICENSE).
