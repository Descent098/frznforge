/**
 * Sync tests: ingest artifact ↔ site. Every repo in the artifact must get a page and appear
 * in the listing; the site loader must round-trip what ingest wrote; empty/odd repos must
 * still produce valid, summarisable data. Uses the same route/listing functions the pages use.
 */
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import { resolveConfig, type ResolvedConfig } from '../../src/lib/config/index';
import { loadForgeData, readBlob, resetForgeDataCache } from '../../src/lib/data/load';
import type { ForgeData } from '../../src/lib/data/schema';
import { ingest, writeArtifact } from '../../src/lib/ingest';
import { applyListing, defaultQuery, summarize } from '../../src/lib/listing';
import { allRoutes, getRepoRoutes, repoUrl } from '../../src/lib/routes';
import { aggregateLanguages, heatFor, relativeTime } from '../../src/lib/format';
import { FixtureRepo } from './helpers/fixture-repo';

describe('artifact ↔ site sync', () => {
  let alpha: FixtureRepo;
  let wiped: FixtureRepo;
  let empty: FixtureRepo;
  let outDir: string;
  let config: ResolvedConfig;
  let data: ForgeData;

  beforeAll(async () => {
    alpha = FixtureRepo.create('alpha');
    alpha.writeAndCommit({ 'README.md': '# Alpha\n', 'src/a.ts': 'export const a = 1;\n', '.frznforge.json': '{"description":"Alpha","tags":["x"]}' }, 'init');
    alpha.tag('v1', { annotated: true, message: 'one' });

    wiped = FixtureRepo.create('wiped');
    wiped.writeAndCommit({ 'a.txt': 'a\n' }, 'add');
    wiped.rm('a.txt');
    wiped.commit('remove everything');

    empty = FixtureRepo.create('empty');

    outDir = fs.mkdtempSync(path.join(os.tmpdir(), 'frznforge-sync-'));
    config = resolveConfig(
      {
        owner: { name: 'Tester', handle: 'tester' },
        repos: [
          { type: 'local', path: alpha.dir },
          { type: 'local', path: wiped.dir },
          { type: 'local', path: empty.dir },
        ],
        ingest: { outDir, maxBlobBytes: 1024 * 1024, maxCommits: null, concurrency: 3 },
      },
      os.tmpdir(),
    );
    const result = await ingest(config);
    await writeArtifact(result.data, result.blobs, outDir);
    resetForgeDataCache();
    data = loadForgeData(outDir);
  });

  afterAll(() => {
    alpha.cleanup();
    wiped.cleanup();
    empty.cleanup();
    fs.rmSync(outDir, { recursive: true, force: true, maxRetries: 5 });
  });

  it('loader round-trips what ingest wrote (incl. blobs)', () => {
    expect(data.repos.map((r) => r.slug)).toEqual(['alpha', 'empty', 'wiped']);
    const a = data.repos[0]!;
    const file = a.files['src/a.ts']!;
    expect(file.stored).toBe(true);
    expect(readBlob(outDir, file.sha)).toBe('export const a = 1;\n');
    expect(readBlob(outDir, '0'.repeat(40))).toBeNull();
  });

  it('every repo in the artifact has exactly one route and appears in the listing', () => {
    const routes = getRepoRoutes(data);
    expect(routes.map((r) => r.slug).sort()).toEqual(data.repos.map((r) => r.slug).sort());
    expect(new Set(routes.map((r) => r.url)).size).toBe(routes.length);
    for (const r of routes) expect(r.url).toBe(repoUrl(r.slug));
    const all = allRoutes(data);
    expect(all).toContain('/');
    expect(all).toContain('/repos/');
    for (const r of routes) expect(all).toContain(r.url);

    const listing = applyListing(data.repos.map(summarize), defaultQuery(50));
    expect(listing.total).toBe(data.repos.length);
    expect(listing.items.map((r) => r.slug).sort()).toEqual(data.repos.map((r) => r.slug).sort());
  });

  it('empty and wiped repos still summarise and render-safe', () => {
    const e = data.repos.find((r) => r.slug === 'empty')!;
    const w = data.repos.find((r) => r.slug === 'wiped')!;
    expect(e.empty).toBe(true);
    expect(w.empty).toBe(false);
    expect(w.tree).toEqual([]);
    expect(data.warnings.map((x) => x.code)).toEqual(expect.arrayContaining(['repo-empty', 'default-branch-empty-tree']));
    for (const r of [e, w]) {
      const s = summarize(r);
      expect(s.slug).toBe(r.slug);
      expect(() => heatFor(s.updatedAt)).not.toThrow();
      expect(() => relativeTime(s.updatedAt)).not.toThrow();
    }
    expect(summarize(e).updatedAt).toBeNull();
    expect(aggregateLanguages(data.repos).map((l) => l.name)).toEqual(['TypeScript']);
  });

  it('uncommitted work never reaches the artifact', async () => {
    alpha.write({ 'SECRET.txt': 'nope', 'README.md': '# modified, not committed\n' });
    const again = await ingest(config);
    const a = again.data.repos.find((r) => r.slug === 'alpha')!;
    expect(a.files['SECRET.txt']).toBeUndefined();
    expect(a.readme?.content).toBe('# Alpha\n');
  });
});
