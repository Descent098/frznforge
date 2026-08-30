/**
 * Hosted static sites (schema v7): config validation (structural mistakes fail the parse),
 * branch resolution (explicit, and the gh-pages → main → master fallback), the
 * branchTrees-cap exemption, the hosted file-size cap, dangling-reference warnings, and
 * the artifact ↔ route sync in both directions.
 */
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import { resolveConfig, type ResolvedConfig } from '../../src/lib/config/index';
import { FrznforgeConfigSchema } from '../../src/lib/config/schema';
import type { FrznforgeConfigInput } from '../../src/lib/config/schema';
import { ingest, resolveHostedBranch, serializeForgeData, writeArtifact } from '../../src/lib/ingest/index';
import { readBlobBuffer } from '../../src/lib/data/load';
import { allRoutes, hostedFiles, hostedRoutes } from '../../src/lib/routes';
import { FixtureRepo, at } from './helpers/fixture-repo';

const OWNER = { owner: { name: 'Test Owner', handle: 'test' } };

const cleanups: Array<() => void> = [];
afterEach(() => {
  while (cleanups.length) cleanups.pop()!();
});

function tempDir(prefix: string): string {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), `frznforge-${prefix}-`));
  cleanups.push(() => fs.rmSync(dir, { recursive: true, force: true, maxRetries: 5 }));
  return dir;
}

function newFixture(name: string): FixtureRepo {
  const repo = FixtureRepo.create(name);
  cleanups.push(() => repo.cleanup());
  return repo;
}

function makeConfig(root: string, input: Partial<FrznforgeConfigInput>): ResolvedConfig {
  return resolveConfig(
    {
      ...OWNER,
      ingest: { outDir: path.join(root, 'data'), cacheDir: path.join(root, 'cache') },
      ...input,
    },
    root,
  );
}

describe('hosting config validation', () => {
  it('hard-errors on reserved slugs and duplicates; a mistyped repo stays a warning', () => {
    for (const reserved of ['repos', 'notes', 'orgs', '_astro', 'search-index.json']) {
      expect(
        () => FrznforgeConfigSchema.parse({ ...OWNER, hosting: { sites: [{ repo: 'x', slug: reserved }] } }),
        reserved,
      ).toThrow(/reserved/);
    }
    expect(() =>
      FrznforgeConfigSchema.parse({
        ...OWNER,
        hosting: { sites: [{ repo: 'a', slug: 'mysite' }, { repo: 'b', slug: 'mysite' }] },
      }),
    ).toThrow(/twice/);
    // slug defaults to the repo value, so the duplicate check sees defaults too
    expect(() =>
      FrznforgeConfigSchema.parse({ ...OWNER, hosting: { sites: [{ repo: 'a' }, { repo: 'a' }] } }),
    ).toThrow(/twice/);
    // an un-sluggable repo name needs an explicit slug
    expect(() =>
      FrznforgeConfigSchema.parse({ ...OWNER, hosting: { sites: [{ repo: 'My Repo' }] } }),
    ).toThrow(/path segment/);
    // a plain typo in `repo` parses fine — it becomes an ingest warning, not a parse error
    const ok = FrznforgeConfigSchema.parse({ ...OWNER, hosting: { sites: [{ repo: 'no-such-repo' }] } });
    expect(ok.hosting.sites).toHaveLength(1);
    expect(ok.hosting.maxFileBytes).toBe(20 * 1024 * 1024);
  });
});

describe('resolveHostedBranch', () => {
  it('prefers the configured branch, else gh-pages → main → master, else null', () => {
    expect(resolveHostedBranch(['main', 'gh-pages'], 'site')).toBeNull();
    expect(resolveHostedBranch(['main', 'site'], 'site')).toBe('site');
    expect(resolveHostedBranch(['master', 'main', 'gh-pages'])).toBe('gh-pages');
    expect(resolveHostedBranch(['master', 'main'])).toBe('main');
    expect(resolveHostedBranch(['master', 'dev'])).toBe('master');
    expect(resolveHostedBranch(['dev'])).toBeNull();
  });
});

describe('hosting end to end (ingest → artifact → routes)', () => {
  /** A repo with a dormant gh-pages branch holding a tiny built site (incl. one big file). */
  function siteFixture(): FixtureRepo {
    const r = newFixture('hostee');
    r.writeAndCommit({ 'README.md': '# Hostee\n', 'src/app.ts': 'export {};\n' }, 'code', { date: at(600) });
    r.checkout('gh-pages', true);
    r.git('rm', '-r', '-q', '--cached', '.');
    fs.rmSync(path.join(r.dir, 'src'), { recursive: true, force: true });
    fs.rmSync(path.join(r.dir, 'README.md'), { force: true });
    r.writeAndCommit(
      {
        'index.html': '<!doctype html><title>hostee</title><link rel="stylesheet" href="style.css">built site',
        'style.css': 'body{margin:0}\n',
        'bundle.js': 'x'.repeat(2048), // over the 1 KiB maxBlobBytes below, under maxFileBytes
      },
      'publish site',
      { date: at(0) }, // OLD head date — the most-recently-updated branch cap would drop it
    );
    r.checkout('main');
    return r;
  }

  it('serves the resolved branch: cap-exempt tree, big files stored, routes in both directions', async () => {
    const root = tempDir('hosting');
    const r = siteFixture();
    // ten fresher branches so the default branchTrees window (here: 1) cannot include gh-pages
    r.checkout('busy', true);
    r.writeAndCommit({ 'busy.txt': 'b\n' }, 'busy work', { date: at(1200) });
    r.checkout('main');

    const cfg = makeConfig(root, {
      repos: [{ type: 'local', path: r.dir }],
      ingest: {
        outDir: path.join(root, 'data'),
        cacheDir: path.join(root, 'cache'),
        maxBlobBytes: 1024,
        branchTrees: 1, // 'busy' wins the recency window; gh-pages must be forced anyway
      },
      hosting: { sites: [{ repo: 'hostee', slug: 'mysite' }] },
    });
    const run = await ingest(cfg);
    await writeArtifact(run.data, run.blobs, run.archives, cfg.outDir);

    // resolution recorded in the artifact
    expect(run.data.hosting).toEqual([{ slug: 'mysite', repo: 'hostee', ref: 'gh-pages' }]);
    // forced tree despite the cap; the big bundle is stored under the hosted cap
    const repo = run.data.repos[0]!;
    expect(repo.refTrees['gh-pages']).toBeTruthy();
    expect(repo.refTrees['busy']).toBeTruthy();
    expect(repo.refTrees['gh-pages']!.files['bundle.js']!.stored).toBe(true);
    // ...while the normal blob cap still applies elsewhere (nothing else is over 1 KiB —
    // assert via the file route side instead: every hosted file resolves to stored bytes)
    const files = hostedFiles(run.data);
    expect(files.map((f) => f.url).sort()).toEqual([
      '/mysite/bundle.js',
      '/mysite/index.html',
      '/mysite/style.css',
    ]);
    for (const f of files) expect(readBlobBuffer(cfg.outDir, f.sha)).toBeTruthy();
    // both directions: every hosted route is in allRoutes, and nothing extra appears
    const all = new Set(allRoutes(run.data));
    for (const url of hostedRoutes(run.data)) expect(all.has(url)).toBe(true);
    expect(hostedRoutes(run.data)).toHaveLength(3);
    // determinism: a second run emits identical bytes
    const again = await ingest(cfg);
    expect(serializeForgeData(again.data)).toBe(serializeForgeData(run.data));
  });

  it('warns and drops entries for unknown repos and missing branches', async () => {
    const root = tempDir('hosting-warn');
    const r = newFixture('plain');
    r.writeAndCommit({ 'a.txt': 'a\n' }, 'only', { date: at(0) });
    r.git('branch', '-m', 'trunk'); // no gh-pages/main/master at all

    const cfg = makeConfig(root, {
      repos: [{ type: 'local', path: r.dir }],
      hosting: {
        sites: [
          { repo: 'nope', slug: 'ghost' },
          { repo: 'plain', slug: 'auto-fails' },
          { repo: 'plain', slug: 'explicit-fails', branch: 'gh-pages' },
        ],
      },
    });
    const run = await ingest(cfg);
    expect(run.data.hosting).toEqual([]);
    const codes = run.data.warnings.map((w) => w.code);
    expect(codes).toContain('hosting-unknown-repo');
    expect(codes.filter((c) => c === 'hosting-branch-missing')).toHaveLength(2);
    // repo-scoped warnings sit on the repo too
    expect(run.data.repos[0]!.warnings.filter((w) => w.code === 'hosting-branch-missing')).toHaveLength(2);
  });

  it('hard-errors when a hosted slug collides with a public/ file', async () => {
    const root = tempDir('hosting-public');
    fs.mkdirSync(path.join(root, 'public'));
    fs.writeFileSync(path.join(root, 'public', 'mysite'), 'x');
    const r = newFixture('collides');
    r.writeAndCommit({ 'index.html': 'hi' }, 'site', { date: at(0) });
    const cfg = makeConfig(root, {
      repos: [{ type: 'local', path: r.dir }],
      hosting: { sites: [{ repo: 'collides', slug: 'mysite', branch: 'main' }] },
    });
    await expect(ingest(cfg)).rejects.toThrow(/public\/mysite/);
  });

  it("hosting the default branch raises that branch's stored-file cap", async () => {
    const root = tempDir('hosting-default');
    const r = newFixture('rooted');
    r.writeAndCommit(
      { 'index.html': '<!doctype html>root site', 'big.bin': 'y'.repeat(4096) },
      'site at root',
      { date: at(0) },
    );
    const cfg = makeConfig(root, {
      repos: [{ type: 'local', path: r.dir }],
      ingest: { outDir: path.join(root, 'data'), cacheDir: path.join(root, 'cache'), maxBlobBytes: 1024 },
      hosting: { sites: [{ repo: 'rooted', branch: 'main' }] },
    });
    const run = await ingest(cfg);
    expect(run.data.hosting).toEqual([{ slug: 'rooted', repo: 'rooted', ref: 'main' }]);
    expect(run.data.repos[0]!.files['big.bin']!.stored).toBe(true); // over maxBlobBytes, under the hosted cap
    expect(hostedRoutes(run.data)).toContain('/rooted/big.bin');
  });
});
