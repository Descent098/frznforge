# frznforge user documentation

Six guides, in reading order.

1. **[Quick start](./quick-start.md)** — clone, point it at a repository, ingest, run it.
   Ten minutes to a site on `localhost`, including a profile page and a first note.

2. **[Starting a site](./starting-a-site.md)** — `frznforge -- new`, what it scaffolds, and how
   to keep your content in its own repository rather than inside the engine's clone.

3. **[Configuration](./configuration.md)** — every key in `frznforge.config.ts`, the
   `.frznforge.json` you commit inside a repository, profile and organization frontmatter,
   the notes folder, and what each warning code means.

4. **[Importing from a forge](./importing.md)** — publishing repositories hosted on GitHub,
   GitLab, Gitea or Forgejo: the `init` command (terminal and `--web`), API tokens, the mirror
   cache, offline builds, and how untrusted markdown is handled.

5. **[Deploying](./deploying.md)** — what `npm run build` produces, how big it gets and how to
   make it smaller, a working GitHub Actions workflow, and the exact settings for Cloudflare
   Pages, Netlify, nginx, Apache, Caddy and S3.

6. **[Migrating from a forge](./migrating.md)** — what carries over and what does not (no
   issues, no pull requests, no stars, no CI), how releases map, what happens to private
   repositories, URL equivalents, and how to keep the site fresh.

Reading in a hurry? Start with the quick start, then jump to deploying.

Developer documentation — the ingest ↔ site data contract and the phase plan — lives in
[`../dev/`](../dev/).
