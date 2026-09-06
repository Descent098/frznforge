# Writing a page family

How to add one of the site's page families to the Go renderer. Read this before writing an
emitter; it is the contract the driver and the other families rely on.

## The shape

Each family owns **one** file in `internal/build/` (`pages_*.go`) exporting one function that
`build.go` already calls. Nothing else in this package is shared, which is what lets families be
written independently — and what stops two of them quietly disagreeing about the shell.

```go
func emitProfile(b *Builder) error                      // site-wide families
func emitRepoOverview(b *Builder, repo *model.Repo) err // per-repo families
```

An emitter's job is: derive its payload from the artifact, hand it to the renderer, write.

**It must not**: read the filesystem (except `b.Blob`), consult the clock (`b.Site.Now` is the
build clock), or sort with a locale-aware comparison. All three are how a "static site" stops
being reproducible — two machines would emit different HTML from the same artifact.

## The Builder

```go
b.Cfg       *config.Resolved   // site config, defaults applied, paths absolute
b.Data      *model.ForgeData   // the artifact
b.Site      *render.Site       // config + artifact + router + loaded markdown content
b.Router    routes.Router      // every URL comes from here — never hand-build an href
b.Blob(sha) ([]byte, error)    // a stored blob; missing is an error, not an empty file
b.Markdown(src, trusted) string// rendered markdown at the right trust level
b.WritePage(route, tpl, page) error  // render a body template inside the shell, write it
b.WriteFile(route, content) error    // write bytes at a route (raw files, JSON, zips)
```

`routes` is the only source of URLs. `b.Router.BlobURL(...)`, `b.Router.CommitURL(...)` and so
on already carry the deploy base, and `tests/e2e/base-path.spec.ts` fails the build if a
root-absolute href escapes.

## Trust

`b.Markdown(src, trusted)` — pass `trusted` only for content the site owner wrote:
`content/profile.md`, `content/orgs/*.md`, and a **local** repo's own README. A repo imported
from a provider, and any provider-supplied release note, is **untrusted**: raw HTML is stripped
and URLs are sanitised. `markdown.IsTrustedSource(repo.Source)` decides it for repo content —
use that rather than re-deriving `type == "local"`.

Getting this backwards publishes somebody else's `<script>` on your site.

## Templates

`internal/render/templates/*.gohtml`, embedded at build time, one `{{define "name"}}` per file.
Page bodies are named `page-<family>`; partials get a plain name.

```
{{define "page-notes"}} … {{end}}          → b.WritePage(url, "page-notes", page)
{{define "note-file-view"}} … {{end}}      → {{template "note-file-view" (dict "File" .)}}
```

Inside a template, `.` is a `render.Page`:

- `.Data` is the **artifact** (`*model.ForgeData`) — `Page` embeds `*Site`.
- `.Payload` is your page's own data. They are named apart on purpose: an embedded field and an
  outer field of the same name resolve silently to the outer one.
- `.Title`, `.Description`, `.Active` (`profile`/`repos`/`notes`/`orgs`), `.ExtraStyles`,
  `.ExtraScripts`.

Available functions are in `render.funcs()` — URL builders, `relativeTime`, `heat`, `formatInt`,
`formatBytes`, `initials`, `licenseURL`, `deref`, `plural`, `dict`, `add`/`sub`. Add one there
only if it is genuinely presentational; anything with logic belongs in Go where it can be tested.

**Escaping.** `html/template` escapes everything by default, which is what makes rendering
untrusted repo content safe. The only values that may be `template.HTML` are the outputs of
`internal/markdown` and `internal/highlight`. `htmlOf` in `helpers.go` is how you mark them, and
it must not be used for anything else.

## Shared partials

Already written; use them, do not redefine them:

- `shell` — the page shell. `b.WritePage` wraps your body in it.
- `sidebar`, `icon-sprite`, `avatar` — chrome.
- `repo-card` — one repo card. `{{template "repo-card" (dict "Repo" $summary "TagHref" $base)}}`
  where `$summary` is a `render.RepoSummary` (`render.Summarize(repo)`) and `$base` is the
  listing path tag chips should point at, ending in `/`.

## Extra stylesheets

The per-section stylesheets are already in `web/css/` and are copied verbatim. A page that needs
one adds it to `.ExtraStyles`:

```go
page.ExtraStyles = []string{b.Router.WithBase("/css/notes.css")}
```

Same for `.ExtraScripts` (`/js/mermaid.js` on a page whose markdown holds a diagram —
`markdown.ContainsMermaid(html)` decides; `/js/copy.js` on a page with a `button[data-copy]`).

## What "done" means

1. `gofmt -w`, `go build ./...`, `go vet ./...`, `go test ./...` clean.
2. `go run ./cmd/frznforge build --out=/tmp/site` gets past your family.
3. The markup matches what the Astro component emitted — same classes, same ids, same
   attributes, same nesting. The e2e suite asserts on those selectors and **may not be edited**.
   `dist/` holds Astro's current output: read the real HTML for the page you are porting rather
   than inferring it from the `.astro` source.
