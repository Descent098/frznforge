---
title: Deploying a frozen forge
description: Three ways to put a fully static forge online — rsync to a box, GitHub Pages, or a local preview server that mimics production routing.
date: 2026-07-11
tags:
  - deployment
  - static
  - ci
---

# Deploying a frozen forge

`npm run build` leaves you with a `dist/` directory of plain files. No runtime, no database,
no origin server that can be down at 3am. That makes deployment boring, which is the point —
but there are three details that trip people up every time.

## The three details

1. **Trailing slashes.** Every route ends in `/` and is emitted as `<route>/index.html`.
   A host that does not do directory-index resolution will 404 on `/repos/frznforge/`.
2. **Content addressing.** `blobs/` filenames are sha1s of their contents, so they are
   immutable. Cache them forever; cache the HTML for seconds.
3. **The artifact is not in git.** `data/forge.json` and `data/blobs/` are gitignored, so CI
   has to run `npm run ingest` before `astro build` — and ingest needs the *repos*, which for
   a self-hosted forge means `fetch-depth: 0`, not a shallow clone.

## What's in this note

| File | Use it when |
| --- | --- |
| `deploy.sh` | You own a box and want `rsync` with an atomic symlink swap |
| `pages.yml` | You want GitHub Actions to build and publish to Pages |
| `serve.py` | You want to check the built output locally, with production-ish routing |

The local server is worth the thirty lines: `python -m http.server` resolves directory
indexes, but it does not send the headers a real host sends, and it happily serves paths a
CDN would reject. Catching a trailing-slash bug on your laptop beats catching it in
production. See also the
[Astro deploy guides](https://docs.astro.build/en/guides/deploy/) for host-specific notes.
