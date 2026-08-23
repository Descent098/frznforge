# Design options (archived)

Phase 0 design explorations, kept for posterity. **Hearth** was chosen (2026-08-23) and
lives on as the real site design; the other three are frozen here as-is.

Each folder holds the files exactly as they were under `src/` when the decision was made:

| Folder   | Direction                                                                 |
|----------|---------------------------------------------------------------------------|
| `ember/` | GitHub-density dark, GitLab-style sidebar, heat gradient everywhere       |
| `frost/` | Light-first, Bitbucket icon rail, ice base with ember rationed to "recent"|
| `anvil/` | Editorial / terminal: monospace chrome, hard rules, heat is the system    |
| `hearth/`| Warm showcase dashboard: hero band, roomy cards, docked floating sidebar — **chosen** (snapshot at decision time, after the stats/README restructure and palette switch) |
| `hub/`   | The `/designs/` index page that linked them                               |

To resurrect one for viewing: copy `<name>/index.astro` + `repo.astro` to
`src/pages/designs/<name>/`, the layout to `src/layouts/designs/`, and the CSS to
`src/styles/designs/`, then fix the relative `import` paths in the layout. All content is
static mock data; nothing is wired to the ingest artifact.

Notes on the Hearth decision:
- Frost's colours were liked too, so Hearth gained a build-time palette switch
  (`frznforge.config.ts` → `theme.palette: 'hearth' | 'frost'`). Frost palette = cool
  neutrals + ice as the action colour (`--hf-accent`).
