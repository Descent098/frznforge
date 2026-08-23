# Recorded provider HTTP fixtures

Real, **trimmed** responses from the four forge APIs the Phase 5 importers talk to. Unit
tests must never hit the network; they load these through `loadFixture()` and serve them
with `fixtureFetch()` (both exported from `./index.ts`).

- **Recorded:** 2026-08-23 (`date -u +%Y-%m-%d`)
- **All requests were unauthenticated.** No tokens were sent and none are stored here.
- Every file is pretty-printed with 2-space indent and a trailing newline.

## Files

| File | Endpoint | State |
| --- | --- | --- |
| `github/repo.json` | `GET https://api.github.com/repos/Descent098/ezcv` | trimmed |
| `github/releases.json` | `GET https://api.github.com/repos/Descent098/ezcv/releases?per_page=100` | trimmed |
| `github/releases-with-assets.json` | `GET https://api.github.com/repos/cli/cli/releases?per_page=3` | trimmed (extra, see note) |
| `github/repo-404.json` | `GET https://api.github.com/repos/Descent098/definitely-does-not-exist-frznforge` | verbatim (404 body) |
| `github/rate-limited.json` | — | **synthetic** |
| `gitlab/project.json` | `GET https://gitlab.com/api/v4/projects/gitlab-org%2Fgitlab-runner?license=true` | verbatim |
| `gitlab/releases.json` | `GET https://gitlab.com/api/v4/projects/gitlab-org%2Fgitlab-runner/releases?per_page=20` | trimmed |
| `gitea/repo.json` | `GET https://gitea.com/api/v1/repos/gitea/tea` | trimmed |
| `gitea/releases.json` | `GET https://gitea.com/api/v1/repos/gitea/tea/releases?limit=20` | trimmed |
| `forgejo/repo.json` | `GET https://codeberg.org/api/v1/repos/forgejo/forgejo` | trimmed |
| `forgejo/releases.json` | `GET https://codeberg.org/api/v1/repos/forgejo/forgejo/releases?limit=10` | trimmed |

Total on disk: ~69 KB.

### What "trimmed" means

Fields were only ever **deleted**, never invented or renamed, with the exceptions listed
under "Edits beyond deletion" below. Specifically:

- **Release lists** are cut to the first 3–5 entries and each release to its first 3 assets.
- **GitHub repo**: dropped the ~40 `*_url` hypermedia templates, `temp_clone_token` (it was
  `null`, dropped on principle), and `template_repository`. `owner` keeps its identity and
  URL fields only.
- **GitHub releases**: `author` and `assets[].uploader` reduced — `uploader` removed
  entirely, `author` kept as login/id/node_id/avatar_url/url/html_url/type/site_admin.
- **GitLab releases**: `assets.links` cut to 3 (the live response has 38 per release);
  `assets.sources`, `evidences`, `commit`, and `_links` are intact.
- **Gitea/Forgejo repo**: dropped the merge-policy block (`allow_*`, `default_*_style`,
  `autodetect_manual_merge`, …), `permissions`, `internal_tracker`, `repo_transfer`.
- **Gitea/Forgejo releases**: `author` reduced to identity fields (dropped `last_login`,
  `followers_count`, `following_count`, `starred_repos_count`, `website`, `description`,
  `location`, `pronouns`, `language`, `source_id`, `prohibit_login`).

### Edits beyond deletion

Three, all deliberate and all recorded here so nobody mistakes them for API behaviour:

1. `github/rate-limited.json` is **hand-written**, not recorded — it reproduces GitHub's
   documented 403/429 rate-limit body (`message` + `documentation_url`). The IP in the
   message is the reserved documentation address `203.0.113.1`. Serve it with an explicit
   status and headers, e.g.
   `{ status: 403, headers: { 'x-ratelimit-remaining': '0', 'x-ratelimit-reset': '1774000000' } }`.
2. `gitlab/releases.json` — one release author's `public_email` held a real personal
   address; the field is kept (importers may read it) but blanked to `""`.
3. `github/releases-with-assets.json` — release `body` truncated at 600 characters with a
   `[body truncated for fixture size]` marker. Every other body in every other file is the
   full original text.

### Why the extra `github/releases-with-assets.json`

`Descent098/ezcv` has 11 releases and **zero** release assets, so it cannot exercise the
GitHub asset-mapping path at all. `cli/cli` was recorded as a second GitHub release fixture
purely to cover `assets[]` (including `digest`, `label`, and a `Bot` uploader). Use `ezcv`
for the ordinary path, `cli/cli` for assets.

## Re-recording

Requires network access. Run unauthenticated so nothing personal leaks in:

```sh
curl -sS -H 'Accept: application/json' -H 'User-Agent: frznforge-fixture-recorder' \
  'https://api.github.com/repos/Descent098/ezcv'
curl -sS -H 'Accept: application/json' -H 'User-Agent: frznforge-fixture-recorder' \
  'https://api.github.com/repos/Descent098/ezcv/releases?per_page=100'
curl -sS -H 'Accept: application/json' -H 'User-Agent: frznforge-fixture-recorder' \
  'https://api.github.com/repos/cli/cli/releases?per_page=3'
curl -sS -H 'Accept: application/json' -H 'User-Agent: frznforge-fixture-recorder' \
  'https://api.github.com/repos/Descent098/definitely-does-not-exist-frznforge'
curl -sS 'https://gitlab.com/api/v4/projects/gitlab-org%2Fgitlab-runner?license=true'
curl -sS 'https://gitlab.com/api/v4/projects/gitlab-org%2Fgitlab-runner/releases?per_page=20'
curl -sS 'https://gitea.com/api/v1/repos/gitea/tea'
curl -sS 'https://gitea.com/api/v1/repos/gitea/tea/releases?limit=20'
curl -sS 'https://codeberg.org/api/v1/repos/forgejo/forgejo'
curl -sS 'https://codeberg.org/api/v1/repos/forgejo/forgejo/releases?limit=10'
```

Then re-apply the trimming above, re-scrub, and update the "Recorded" date. Do **not** paste
in a token: these repos are all public and the fixtures must stay reproducible by anyone.

## Per-provider shape gotchas

These are the differences the importers have to absorb.

**GitHub**
- Repo clone URL is `clone_url`; page is `html_url`; slug is `full_name`.
- `license` is an object (`{key,name,spdx_id,url,node_id}`) or `null`.
- `topics` is a plain string array. `archived`, `disabled`, `is_template` are booleans.
- Releases carry both `created_at` (tag date) and `published_at`; a **draft** release has
  `published_at: null`, so publish date must fall back to `created_at`.
- `assets[].browser_download_url` is the user-facing download; `assets[].url` is the API
  handle. `content_type` and `size` are present; `digest` may be `null` on older assets.
- Errors are `{message, documentation_url, status}` — note `status` is a **string** (`"404"`).
- Rate limiting is 403 *or* 429 with the same body shape; the real signal is the
  `x-ratelimit-remaining: 0` header, not the status code.

**GitLab** (most divergent of the four)
- Slug is `path_with_namespace`, clone URL is `http_url_to_repo`, page is `web_url`.
  There is no `full_name`, no `clone_url`, no `html_url`.
- **Unauthenticated project responses are a reduced field set**: `archived`,
  `issues_enabled`, `empty_repo`, `open_issues_count` and friends are simply absent. The
  importer must treat missing as unknown, not as `false`.
- `license` is **only returned when you pass `?license=true`** — that is why the recorded
  URL has the query param. It comes back as `{key,name,nickname,html_url,source_url}` plus a
  sibling top-level `license_url`. Note `key` is lowercase (`"mit"`), unlike GitHub's
  `spdx_id` (`"MIT"`).
- Both `tag_list` and `topics` are returned with identical contents; prefer `topics`.
- Releases have **no `id`** and **no `draft`/`prerelease`** flags. The nearest equivalents
  are `upcoming_release` (boolean) and comparing `released_at` to now. The tag is the key.
- Release body is `description`, not `body`. Dates are `created_at` **and** `released_at`
  (`released_at` is the one to display; they can differ).
- Assets are nested two levels: `assets.sources[]` (auto-generated `{format,url}` archives,
  four of them: zip/tar.gz/tar.bz2/tar) and `assets.links[]` (uploaded/external
  `{id,name,url,direct_asset_url,link_type}`). **There is no `size` and no `content_type`
  on either** — asset size is simply unavailable from GitLab.
- Extra fields with no analogue elsewhere: `evidences[]`, `commit`, `commit_path`,
  `tag_path`, `_links`.
- Project path must be URL-encoded in the path segment (`gitlab-org%2Fgitlab-runner`).

**Gitea**
- Repo shape is close to GitHub's but flatter: `clone_url`, `html_url`, `full_name`,
  `default_branch`, `archived`, `template` (not `is_template`), `website` (not `homepage`),
  `stars_count`/`forks_count` (note the `s`), `empty`, `mirror`.
- License is `licenses`: an **array of SPDX strings** (`["MIT"]`), not an object.
- `topics` is a string array. `has_issues`, `has_releases`, `has_pull_requests` present.
- Releases: `body` (like GitHub), both `created_at` and `published_at`, `draft` and
  `prerelease` booleans, and a numeric `id`.
- `author` is a **full user object**, much fatter than GitHub's, and it includes an `email`
  (a `…@noreply.gitea.com` alias for these fixtures). Prefer `author.login` /
  `author.full_name`; `username` is a duplicate of `login`.
- `assets[]` has `{id,name,size,download_count,created_at,uuid,browser_download_url}` —
  **no `content_type` and no `url`**; `browser_download_url` is the only URL. There is a
  `uuid` GitHub does not have.

**Forgejo** (Codeberg — same API family as Gitea, but not identical)
- Everything above for Gitea applies, with these deltas:
  - Repo has **no `licenses` field at all** — the Gitea license array is Gitea-only, so
    license must come from the cloned tree, not the API.
  - Repo has no `has_code`, no `projects_mode`; it adds `has_wiki_contents`, `wiki_branch`,
    `wiki_ssh_url`, `wiki_clone_url`, `globally_editable_wiki`, `parent`.
  - Releases add `hide_archive_links` and `archive_download_count`.
  - `assets[]` adds a `type` field Gitea omits.
  - `author` adds `pronouns`.
- Treat "Gitea-compatible" as a base and feature-detect per field; do not assume a field
  exists just because the other one has it.

**Common to all four**
- Every provider paginates differently: GitHub `?per_page=` + `page=`, GitLab `?per_page=`
  (+ `X-Total-Pages` header), Gitea/Forgejo `?limit=` + `page=`.
- Timestamps: GitHub/Gitea/Forgejo use `Z`-suffixed UTC; GitLab uses millisecond precision
  (`2026-08-23T19:51:00.239Z`). Parse, do not string-compare.
