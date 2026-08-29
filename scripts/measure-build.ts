#!/usr/bin/env tsx
/**
 * `npx tsx scripts/measure-build.ts` — where the pages come from, and what they weigh.
 *
 * frznforge is a static site, so build time is page count and page count is arithmetic on the
 * artifact: tree, blob and raw routes are emitted **per browsable ref**, which is why four
 * remote repos with 27+22+7+2 branches once produced 15,988 pages (Phase 7 capped that with
 * `ingest.branchTrees`; the numbers live in `docs/dev/performance.md`).
 *
 * This script is the instrument behind those numbers. It reads an artifact — any artifact,
 * `--data=<dir>` — and reports, per repo, how many browsable refs it has and how each route
 * family multiplies out, so a user can predict their build before running it. Optionally it
 * builds the site and weighs the result.
 *
 * It never writes into the artifact, never touches `frznforge.config.ts`, and (without
 * `--build`) never runs anything: analysis alone is safe to run against `./data` mid-session.
 *
 * Usage:
 *   npx tsx scripts/measure-build.ts                            # analyse ./data
 *   npx tsx scripts/measure-build.ts --data=tests/.tmp/smoke/data
 *   npx tsx scripts/measure-build.ts --data=<dir> --build       # + astro build into a temp dist
 *   npx tsx scripts/measure-build.ts --data=<dir> --build --out=tests/.tmp/perf/dist-a
 *   npx tsx scripts/measure-build.ts --data=<dir> --ingest-ms=41234   # fold in a measured ingest
 *   npx tsx scripts/measure-build.ts --data=<dir> --json        # machine-readable
 *
 * `--ingest-ms` exists because an artifact does not record how long it took to produce (it
 * must not: wall-clock values in the artifact would break determinism). `npm run smoke:remote`
 * and `npm run ingest` both print their own duration — pass it in to get one complete table.
 */
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { gzipSync } from 'node:zlib';
import { fileURLToPath } from 'node:url';
import type { ForgeData, Repo } from '../src/lib/data/schema';
import {
  allRoutes,
  blobRoutes,
  browsableRefs,
  commitsPageCount,
  notesRoutes,
  orgRoutes,
  rawRoutes,
  repoRoutes,
  resolveReleases,
  treeRoutes,
} from '../src/lib/routes';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

/* ---- arguments ------------------------------------------------------------ */

const argv = process.argv.slice(2);
const flag = (name: string): string | null => {
  const hit = argv.find((a) => a === `--${name}` || a.startsWith(`--${name}=`));
  if (hit === undefined) return null;
  const eq = hit.indexOf('=');
  return eq === -1 ? '' : hit.slice(eq + 1);
};

if (flag('help') !== null) {
  console.log(fs.readFileSync(fileURLToPath(import.meta.url), 'utf8').split('*/')[0]!.replace(/^#!.*\n/, ''));
  process.exit(0);
}

const dataDir = path.resolve(ROOT, flag('data') || 'data');
const wantBuild = flag('build') !== null;
const outDir = path.resolve(ROOT, flag('out') || path.join('tests', '.tmp', 'perf', `dist-${path.basename(dataDir)}`));
const ingestMs = flag('ingest-ms') ? Number(flag('ingest-ms')) : null;
const asJson = flag('json') !== null;

/* ---- artifact ------------------------------------------------------------- */

/**
 * Read the artifact **without** validating it against the current schema.
 *
 * `parseForgeData` would reject anything but today's `SCHEMA_VERSION`, and the whole point of
 * this script is comparing artifacts — including one produced by an older ingest, or by a
 * branch that has not been merged yet. Route derivation only needs the fields the helpers in
 * `src/lib/routes.ts` read, so missing newer keys are filled with their empty value instead.
 */
function readArtifact(dir: string): ForgeData {
  const file = path.join(dir, 'forge.json');
  if (!fs.existsSync(file)) {
    console.error(`no artifact at ${file} — run \`npm run ingest\` (or point --data at one)`);
    process.exit(2);
  }
  const raw = JSON.parse(fs.readFileSync(file, 'utf8')) as ForgeData;
  for (const repo of raw.repos) {
    // schema v5 additions, absent in older artifacts; `hasInsights` dereferences this one.
    repo.insights ??= null;
  }
  raw.notes ??= [];
  raw.organizations ??= [];
  return raw;
}

const data = readArtifact(dataDir);

/* ---- per-repo route arithmetic -------------------------------------------- */

interface RepoRow {
  slug: string;
  branches: number;
  /** Branches + tags that have a browsable tree — the multiplier on tree/blob/raw. */
  refs: number;
  /** Mean tree entries per browsable ref, i.e. what each extra ref costs. */
  entriesPerRef: number;
  tree: number;
  blob: number;
  raw: number;
  commitPages: number;
  commits: number;
  releases: number;
  archives: number;
  total: number;
  /** Pages that would disappear if only the default branch were browsable. */
  perRefPages: number;
}

function measureRepo(repo: Repo): RepoRow {
  const refs = browsableRefs(repo);
  const tree = treeRoutes(repo).length;
  const blob = blobRoutes(repo).length;
  const raw = rawRoutes(repo).length;
  const commitPages = repo.branches.reduce((n, b) => n + commitsPageCount(repo, b.name), 0);
  const entries = refs.reduce((n, r) => n + r.tree.length, 0);
  return {
    slug: repo.slug,
    branches: repo.branches.length,
    refs: refs.length,
    entriesPerRef: refs.length ? entries / refs.length : 0,
    tree,
    blob,
    raw,
    commitPages,
    // extraCommits (schema v6) get /commit/ pages too — without them this column would no
    // longer sum to `total` on artifacts built with maxCommits / maxCommitAgeDays.
    commits: Object.keys(repo.commits).length + Object.keys(repo.extraCommits).length,
    releases: new Set(resolveReleases(repo).map((r) => r.tag)).size,
    archives: repo.archives.length,
    total: repoRoutes(repo).length,
    perRefPages: tree + blob + raw,
  };
}

const rows = data.repos.map(measureRepo);
const siteRoutes = allRoutes(data);
const sum = (pick: (r: RepoRow) => number) => rows.reduce((n, r) => n + pick(r), 0);

/* ---- optional build ------------------------------------------------------- */

interface DistReport {
  buildMs: number;
  files: number;
  html: number;
  bytes: number;
  /** Heaviest HTML pages, raw and gzipped — the asset budget. */
  heaviest: Array<{ rel: string; bytes: number; gzip: number }>;
  /** Named page types with a representative page each. */
  samples: Array<{ kind: string; rel: string; bytes: number; gzip: number }>;
}

function walk(dir: string): string[] {
  const out: string[] = [];
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const abs = path.join(dir, e.name);
    if (e.isDirectory()) out.push(...walk(abs));
    else if (e.isFile()) out.push(abs);
  }
  return out;
}

/** Raw and gzipped size of one file — gzip stands in for what a static host actually ships. */
function weigh(abs: string): { bytes: number; gzip: number } {
  const buf = fs.readFileSync(abs);
  return { bytes: buf.length, gzip: gzipSync(buf, { level: 9 }).length };
}

function build(): DistReport {
  fs.rmSync(outDir, { recursive: true, force: true });
  fs.mkdirSync(outDir, { recursive: true });
  // `node node_modules/astro/bin/astro.mjs` rather than `npx astro`: no shell quoting to get
  // wrong on Windows, and no npx resolution step inside the timed section.
  const astroBin = path.join(ROOT, 'node_modules', 'astro', 'bin', 'astro.mjs');
  const started = performance.now();
  execFileSync(process.execPath, [astroBin, 'build', '--outDir', outDir], {
    cwd: ROOT,
    env: { ...process.env, FRZNFORGE_OUT_DIR: dataDir },
    stdio: asJson ? 'ignore' : 'inherit',
  });
  const buildMs = Math.round(performance.now() - started);

  const files = walk(outDir);
  const html = files.filter((f) => f.endsWith('.html'));
  const bytes = files.reduce((n, f) => n + fs.statSync(f).size, 0);
  const rel = (abs: string) => path.relative(outDir, abs).split(path.sep).join('/');

  const weighed = html
    .map((abs) => ({ abs, size: fs.statSync(abs).size }))
    .sort((a, b) => b.size - a.size)
    .slice(0, 8)
    .map(({ abs }) => ({ rel: rel(abs), ...weigh(abs) }));

  // One representative page per heavy type. The blob sample is the largest blob page, which is
  // the worst case for Shiki output; the rest are whichever page of that kind sorts first.
  const pick = (kind: string, match: (r: string) => boolean, largest = false) => {
    const hits = html.filter((a) => match(rel(a)));
    if (hits.length === 0) return null;
    const abs = largest ? hits.sort((a, b) => fs.statSync(b).size - fs.statSync(a).size)[0]! : hits.sort()[0]!;
    return { kind, rel: rel(abs), ...weigh(abs) };
  };
  const samples = [
    pick('home', (r) => r === 'index.html'),
    pick('repo listing', (r) => r === 'repos/index.html'),
    pick('repo overview', (r) => /^repos\/[^/]+\/index\.html$/.test(r)),
    pick('file browser (tree)', (r) => /^repos\/[^/]+\/tree\//.test(r), true),
    pick('blob (largest)', (r) => /^repos\/[^/]+\/blob\//.test(r), true),
    pick('commits page', (r) => /^repos\/[^/]+\/commits\//.test(r), true),
    pick('single commit', (r) => /^repos\/[^/]+\/commit\//.test(r), true),
    pick('insights', (r) => /^repos\/[^/]+\/insights\/index\.html$/.test(r)),
  ].filter((s): s is NonNullable<typeof s> => s !== null);

  return { buildMs, files: files.length, html: html.length, bytes, heaviest: weighed, samples };
}

const dist = wantBuild ? build() : null;

/* ---- report --------------------------------------------------------------- */

const pad = (v: string, w: number) => (v.length >= w ? v : ' '.repeat(w - v.length) + v);
const padR = (v: string, w: number) => (v.length >= w ? v : v + ' '.repeat(w - v.length));
const kb = (n: number) => `${(n / 1024).toFixed(1)} kB`;
const secs = (ms: number) => `${(ms / 1000).toFixed(1)}s (${Math.floor(ms / 60000)}m${String(Math.round((ms % 60000) / 1000)).padStart(2, '0')}s)`;

function table(head: string[], body: string[][]): string {
  const widths = head.map((h, i) => Math.max(h.length, ...body.map((b) => b[i]!.length)));
  const line = (cells: string[]) => '  ' + cells.map((c, i) => (i === 0 ? padR(c, widths[i]!) : pad(c, widths[i]!))).join('  ');
  return [line(head), line(widths.map((w) => '-'.repeat(w))), ...body.map(line)].join('\n');
}

if (asJson) {
  console.log(
    JSON.stringify(
      {
        dataDir,
        schemaVersion: data.schemaVersion,
        repos: rows,
        totals: {
          pages: siteRoutes.length,
          repoPages: sum((r) => r.total),
          perRefPages: sum((r) => r.perRefPages),
          notesPages: notesRoutes(data).length,
          orgPages: orgRoutes(data).length,
        },
        ingestMs,
        dist,
      },
      null,
      2,
    ),
  );
} else {
  console.log(`frznforge build measurement`);
  console.log(`  artifact      ${path.join(dataDir, 'forge.json')} (schemaVersion ${data.schemaVersion})`);
  console.log(`  repos         ${data.repos.length}, notes ${data.notes.length}, orgs ${data.organizations.length}`);
  console.log('');
  console.log(
    table(
      ['repo', 'branches', 'refs', 'entries/ref', 'tree', 'blob', 'raw', 'commitpg', 'commits', 'rel', 'zips', 'pages'],
      rows.map((r) => [
        r.slug,
        String(r.branches),
        String(r.refs),
        r.entriesPerRef.toFixed(0),
        String(r.tree),
        String(r.blob),
        String(r.raw),
        String(r.commitPages),
        String(r.commits),
        String(r.releases),
        String(r.archives),
        String(r.total),
      ]),
    ),
  );
  console.log('');
  console.log(`  refs        = browsable refs (default branch + refTrees). The tree/blob/raw multiplier.`);
  console.log(`  tree+blob+raw = ${sum((r) => r.perRefPages)} of ${siteRoutes.length} pages (${((sum((r) => r.perRefPages) / Math.max(1, siteRoutes.length)) * 100).toFixed(1)}%) — the part 'ingest.branchTrees' controls.`);
  console.log('');
  console.log(`  total pages          ${siteRoutes.length}`);
  console.log(`    repo pages         ${sum((r) => r.total)}`);
  console.log(`    notes pages        ${notesRoutes(data).length}`);
  console.log(`    org pages          ${orgRoutes(data).length}`);
  console.log(`    site pages         3  (/, /repos/, /404)`);
  if (ingestMs !== null) console.log(`  ingest wall clock    ${secs(ingestMs)}   (--ingest-ms)`);
  if (dist) {
    console.log(`  astro build          ${secs(dist.buildMs)}`);
    console.log(`  dist                 ${dist.files} files, ${dist.html} html, ${(dist.bytes / 1024 / 1024).toFixed(1)} MB on disk`);
    console.log(`  ms per page          ${(dist.buildMs / Math.max(1, dist.html)).toFixed(1)}`);
    console.log('');
    console.log('  page weight by type (raw / gzip):');
    console.log(
      table(
        ['type', 'raw', 'gzip', 'path'],
        dist.samples.map((s) => [s.kind, kb(s.bytes), kb(s.gzip), s.rel]),
      ),
    );
    console.log('');
    console.log('  heaviest pages:');
    console.log(
      table(
        ['#', 'raw', 'gzip', 'path'],
        dist.heaviest.map((h, i) => [String(i + 1), kb(h.bytes), kb(h.gzip), h.rel]),
      ),
    );
    console.log('');
    console.log(`  dist: ${outDir}`);
  } else {
    console.log('');
    console.log('  (pass --build to also run `astro build` into a temp dist and weigh the output)');
  }
}
