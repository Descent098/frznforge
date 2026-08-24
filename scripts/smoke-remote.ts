#!/usr/bin/env tsx
/**
 * `npm run smoke:remote` — the live end-to-end check for Phase 5 importers.
 *
 * The unit tests drive every importer against recorded JSON fixtures with no network, which
 * proves the parsing but not the *plumbing*: real TLS, real pagination, real redirects, real
 * `git clone --mirror` over https, real rate limits. This script is the other half. It builds
 * a throwaway artifact from one small **public** repo on each of the four supported
 * providers, using the production ingest pipeline with nothing stubbed.
 *
 * What it deliberately does NOT do:
 *  - touch `frznforge.config.ts`, `data/` or `.frznforge-cache/` — it builds its own
 *    `ResolvedConfig` in memory and writes under `tests/.tmp/smoke/`;
 *  - require a token. The repos are public, so an anonymous run exercises the same code path
 *    minus the `Authorization` header. A token in the environment is picked up as usual
 *    (`GITHUB_TOKEN`, `GITLAB_TOKEN`, …) and mainly buys a higher rate limit;
 *  - ingest whole histories. `maxCommits`/`tagTrees` are capped (see `INGEST_LIMITS`) so a
 *    run stays under a minute even on a cold cache.
 *
 * Unlike `npm run ingest`, this script **exits non-zero** when a provider fails. Ingest is
 * required to survive an unreachable forge with a warning; a smoke test is required to
 * notice. Warnings are still printed in full either way.
 *
 * Usage:
 *   npm run smoke:remote                 # all four providers
 *   npm run smoke:remote -- github gitea # only the named ones
 *   npm run smoke:remote -- --keep-cache # reuse the mirrors from the last run (fast re-run)
 *
 * Then, to check the generated pages:
 *   npx astro build --outDir tests/.tmp/smoke/dist     (with FRZNFORGE_OUT_DIR=tests/.tmp/smoke/data)
 *   node tests/e2e/serve.mjs tests/.tmp/smoke/dist 4400
 *
 * The whole directory is disposable: `rm -rf tests/.tmp/smoke` when you are done.
 */
import fs from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { resolveConfig, type RepoSourceConfig, type ResolvedConfig } from '../src/lib/config/index';
import { ingest, writeArtifact } from '../src/lib/ingest';
import type { ForgeData, Repo } from '../src/lib/data/schema';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const SMOKE = path.join(ROOT, 'tests', '.tmp', 'smoke');
const DATA = path.join(SMOKE, 'data');
const CACHE = path.join(SMOKE, 'cache');
const DIST = path.join(SMOKE, 'dist');

/**
 * Keep the run short and the artifact small. A smoke test proves the wiring, not the
 * scanner's throughput — that is what the unit tests and the self-build cover.
 */
const INGEST_LIMITS = {
  /** Newest N commits per repo. `tea` alone has thousands. */
  maxCommits: 200,
  /** Browsable trees + zip archives for the newest N tags (plus the default branch). */
  tagTrees: 3,
} as const;

/**
 * One small public repo per provider. Sizes are the bare mirror as measured on 2026-08-23;
 * if one of these moves or grows, swap it for another small public repo rather than dropping
 * the provider — every provider must stay covered.
 */
interface SmokeTarget {
  /** Provider key, also what a command-line filter matches. */
  provider: 'github' | 'gitlab' | 'gitea' | 'forgejo';
  source: RepoSourceConfig;
  /** Slug the repo is expected to land on, for the report. */
  slug: string;
  /** Roughly how big the mirror clone is, so a surprise is obvious in the output. */
  note: string;
}

const TARGETS: SmokeTarget[] = [
  {
    provider: 'github',
    slug: 'sdu',
    note: '~1 MB mirror, 2 provider releases (no binary assets)',
    source: { type: 'github', owner: 'Descent098', repo: 'sdu', host: 'https://api.github.com' },
  },
  {
    provider: 'gitlab',
    slug: 'release-cli',
    note: '~3 MB mirror, releases carry asset *links* and generated source archives',
    source: { type: 'gitlab', project: 'gitlab-org/release-cli', host: 'https://gitlab.com' },
  },
  {
    provider: 'gitea',
    slug: 'tea',
    note: '~21 MB mirror, releases carry many binary assets',
    source: { type: 'gitea', host: 'https://gitea.com', owner: 'gitea', repo: 'tea' },
  },
  {
    provider: 'forgejo',
    slug: 'fuzzel',
    note: '~8 MB mirror on Codeberg, releases carry signed tarball assets',
    source: { type: 'forgejo', host: 'https://codeberg.org', owner: 'dnkl', repo: 'fuzzel' },
  },
];

/* ---- reporting ----------------------------------------------------------- */

interface Row {
  provider: string;
  slug: string;
  ok: boolean;
  commits: number;
  branches: number;
  tags: number;
  releases: number;
  releaseMode: string;
  assets: number;
  files: number;
  archives: number;
  warnings: number;
  detail: string;
}

function pad(value: string, width: number): string {
  return value.length >= width ? value : value + ' '.repeat(width - value.length);
}

function table(rows: Row[]): string {
  const head = ['provider', 'slug', 'commits', 'branches', 'tags', 'releases', 'mode', 'assets', 'files', 'zips', 'warn'];
  const body = rows.map((r) => [
    r.provider,
    r.slug,
    String(r.commits),
    String(r.branches),
    String(r.tags),
    String(r.releases),
    r.releaseMode,
    String(r.assets),
    String(r.files),
    String(r.archives),
    String(r.warnings),
  ]);
  const widths = head.map((h, i) => Math.max(h.length, ...body.map((b) => b[i]!.length)));
  const line = (cells: string[]) => '  ' + cells.map((c, i) => pad(c, widths[i]!)).join('  ');
  return [line(head), line(widths.map((w) => '-'.repeat(w))), ...body.map(line)].join('\n');
}

/** Rate-limit headers, if the provider sends any. Free to ask GitHub: /rate_limit is exempt. */
async function githubRateLimit(): Promise<string | null> {
  try {
    const res = await fetch('https://api.github.com/rate_limit', {
      headers: { 'user-agent': 'frznforge-smoke', accept: 'application/vnd.github+json' },
    });
    const remaining = res.headers.get('x-ratelimit-remaining');
    const limit = res.headers.get('x-ratelimit-limit');
    const reset = res.headers.get('x-ratelimit-reset');
    if (!remaining || !limit) return null;
    const resetAt = reset ? new Date(Number(reset) * 1000).toISOString() : 'unknown';
    return `${remaining}/${limit} core requests left, resets at ${resetAt}`;
  } catch {
    return null; // the smoke run's own result is the thing that matters
  }
}

/* ---- main ---------------------------------------------------------------- */

const args = process.argv.slice(2);
const keepCache = args.includes('--keep-cache');
const wanted = args.filter((a) => !a.startsWith('--'));
const targets = wanted.length > 0 ? TARGETS.filter((t) => wanted.includes(t.provider)) : TARGETS;

if (targets.length === 0) {
  console.error(`no provider matched ${wanted.join(', ')}; known: ${TARGETS.map((t) => t.provider).join(', ')}`);
  process.exit(2);
}

console.log('frznforge remote smoke test — LIVE NETWORK, public repos, no stubs');
console.log(`  out   ${DATA}`);
console.log(`  cache ${CACHE}${keepCache ? ' (reused)' : ' (fresh)'}`);
console.log(`  caps  maxCommits=${INGEST_LIMITS.maxCommits}, tagTrees=${INGEST_LIMITS.tagTrees} — histories are`);
console.log('        deliberately truncated so a run stays quick; this is a wiring check, not a full ingest.');
for (const t of targets) console.log(`  ▸ ${pad(t.provider, 8)} ${t.note}`);
console.log('');

if (!keepCache) await fs.rm(CACHE, { recursive: true, force: true });
await fs.rm(DATA, { recursive: true, force: true });
await fs.mkdir(DATA, { recursive: true });

// `resolveConfig` prefers these over whatever the config says, and a leftover value from an
// e2e run would silently redirect the smoke build into the wrong directory.
delete process.env.FRZNFORGE_OUT_DIR;
delete process.env.FRZNFORGE_CACHE_DIR;

// Built here rather than loaded from frznforge.config.ts: the smoke run must not depend on,
// or disturb, the user's real configuration. `owner`/`site` are only needed to satisfy the
// schema — ingest reads neither.
const config: ResolvedConfig = resolveConfig(
  {
    site: { title: 'frznforge smoke' },
    owner: { name: 'frznforge smoke', handle: 'smoke', profile: './content/profile.md' },
    repos: targets.map((t) => t.source),
    ingest: {
      outDir: DATA,
      cacheDir: CACHE,
      maxCommits: INGEST_LIMITS.maxCommits,
      tagTrees: INGEST_LIMITS.tagTrees,
      archives: true,
      concurrency: 4,
    },
  },
  ROOT,
);

const started = performance.now();
const { data, blobs, archives, remotes } = await ingest(config, {
  onRepoStart: (slug) => console.log(`  ▸ ${slug}`),
  onRemote: ({ slug, provider, action }) => console.log(`    ⇄ ${slug} (${provider}: ${action})`),
  onRepoDone: (repo) =>
    console.log(
      `    ✓ ${repo.slug}: ${repo.commitCount} commits, ${repo.branches.length} branches, ` +
        `${repo.gitTags.length} tags, ${repo.releases.length} releases, ${Object.keys(repo.files).length} files`,
    ),
});
const ingestMs = Math.round(performance.now() - started);

await writeArtifact(data, blobs, archives, DATA);

console.log('');
for (const w of data.warnings) {
  console.warn(`  ⚠ [${w.code}]${w.repo ? ` ${w.repo}:` : ''} ${w.message}`);
}
if (data.warnings.length === 0) console.log('  (no warnings)');
console.log('');

/* ---- verdict ------------------------------------------------------------- */

const bySlug = new Map<string, Repo>(data.repos.map((r) => [r.slug, r]));
const rows: Row[] = [];

for (const target of targets) {
  const status = remotes.find((r) => r.provider === target.provider);
  const repo = data.repos.find((r) => r.source.type === target.provider);
  if (!repo) {
    rows.push({
      provider: target.provider,
      slug: status?.slug ?? target.slug,
      ok: false,
      commits: 0, branches: 0, tags: 0, releases: 0, releaseMode: '-', assets: 0, files: 0, archives: 0,
      warnings: data.warnings.filter((w) => w.repo === (status?.slug ?? target.slug)).length,
      detail: 'no repo in the artifact — the import or the mirror clone failed (see warnings above)',
    });
    continue;
  }
  const assets = repo.releases.reduce((n, r) => n + r.assets.length, 0);
  const problems: string[] = [];
  if (repo.empty || repo.commitCount === 0) problems.push('no commits');
  if (Object.keys(repo.files).length === 0) problems.push('no files');
  if (repo.defaultBranch === null) problems.push('no default branch');
  if (repo.releaseMode !== 'provider') problems.push(`releaseMode is '${repo.releaseMode}'`);
  if (repo.releases.length === 0) problems.push('no provider releases');
  if (repo.source.type === 'local') problems.push('source recorded as local');
  rows.push({
    provider: target.provider,
    slug: repo.slug,
    ok: problems.length === 0,
    commits: repo.commitCount,
    branches: repo.branches.length,
    tags: repo.gitTags.length,
    releases: repo.releases.length,
    releaseMode: repo.releaseMode,
    assets,
    files: Object.keys(repo.files).length,
    archives: repo.archives.length,
    warnings: repo.warnings.length,
    detail: problems.join('; ') || 'ok',
  });
}

console.log(table(rows));
console.log('');

for (const row of rows) {
  const repo = bySlug.get(row.slug);
  if (!repo || repo.releases.length === 0) continue;
  const newest = repo.releases[0]!;
  const asset = repo.releases.flatMap((r) => r.assets)[0];
  console.log(
    `  ${pad(row.provider, 8)} newest release: ${newest.tag} "${newest.name}" ` +
      `(${newest.publishedAt}${newest.prerelease ? ', prerelease' : ''}, ${newest.assets.length} asset(s))`,
  );
  if (newest.url) console.log(`  ${' '.repeat(8)} release page: ${newest.url}`);
  if (asset) console.log(`  ${' '.repeat(8)} first asset:  ${asset.name} (${asset.size} bytes) ${asset.url}`);
}

const rate = await githubRateLimit();
if (rate) console.log(`\n  github rate limit: ${rate}`);

const failed = rows.filter((r) => !r.ok);
console.log(
  `\ndone: ${data.repos.length} repo(s), ${blobs.size} blob(s), ${archives.size} archive(s), ` +
    `${data.warnings.length} warning(s) in ${ingestMs}ms`,
);
console.log(`artifact: ${path.join(DATA, 'forge.json')} (schemaVersion ${(data as ForgeData).schemaVersion})`);
console.log('next:');
console.log(`  FRZNFORGE_OUT_DIR=${DATA} npx astro build --outDir ${DIST}`);
console.log(`  npx tsx tests/e2e/serve.ts ${DIST} 4400`);

if (failed.length > 0) {
  console.error('\nFAILED providers:');
  for (const f of failed) console.error(`  ✗ ${f.provider} (${f.slug}): ${f.detail}`);
  console.error('\nA real build would have continued with a warning; a smoke test must not.');
  process.exit(1);
}
console.log('\nall providers OK');
