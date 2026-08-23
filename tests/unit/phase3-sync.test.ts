/**
 * Phase 3 sync tests: the artifact's trees, refs, tags and archives must map 1:1 onto the
 * routes the site emits (the same functions the pages' getStaticPaths use).
 */
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import { resolveConfig } from '../../src/lib/config/index';
import { loadForgeData, readBlob, readBlobBuffer, resetForgeDataCache } from '../../src/lib/data/load';
import type { ForgeData } from '../../src/lib/data/schema';
import { ingest, writeArtifact } from '../../src/lib/ingest';
import {
  COMMITS_PER_PAGE,
  archiveUrl,
  blobRoutes,
  blobUrl,
  browsableRefs,
  commitUrl,
  commitsPageCount,
  commitsUrl,
  findRef,
  rawRoutes,
  releaseUrl,
  releasesOf,
  repoRoutes,
  treeRoutes,
  treeUrl,
} from '../../src/lib/routes';
import { FixtureRepo } from './helpers/fixture-repo';

describe('phase 3: artifact ↔ file/route sync', () => {
  let repo: FixtureRepo;
  let outDir: string;
  let data: ForgeData;

  beforeAll(async () => {
    repo = FixtureRepo.create('depth');
    repo.writeAndCommit(
      {
        'README.md': '# Depth\n',
        'src/a.ts': 'export const a = 1;\n',
        'src/nested/b.ts': 'export const b = 2;\n',
        'bin/blob.bin': Buffer.from([0x00, 0x01, 0x02, 0xff, 0x00]),
      },
      'init',
    );
    repo.checkout('feat/zip', true);
    repo.writeAndCommit({ 'src/extra.ts': 'export const extra = 3;\n' }, 'feature work');
    repo.checkout('main');
    repo.writeAndCommit({ 'src/a.ts': 'export const a = 10;\n' }, 'bump a');
    repo.tag('v1.0.0', { annotated: true, message: 'first\n\nrelease *notes*' });
    repo.tag('lightweight');

    outDir = fs.mkdtempSync(path.join(os.tmpdir(), 'frznforge-phase3-'));
    const config = resolveConfig(
      {
        owner: { name: 'Tester', handle: 'tester' },
        repos: [{ type: 'local', path: repo.dir }],
        ingest: { outDir, maxBlobBytes: 1024 * 1024, maxCommits: null, concurrency: 2 },
      },
      os.tmpdir(),
    );
    const result = await ingest(config);
    await writeArtifact(result.data, result.blobs, result.archives, outDir);
    resetForgeDataCache();
    data = loadForgeData(outDir);
  });

  afterAll(() => {
    repo.cleanup();
    fs.rmSync(outDir, { recursive: true, force: true, maxRetries: 5 });
  });

  it('every tree entry of every browsable ref has exactly its URL in repoRoutes (blob+tree 1:1)', () => {
    for (const r of data.repos) {
      const urls = new Set(repoRoutes(r));
      for (const ref of browsableRefs(r)) {
        // the ref root itself
        expect(urls.has(treeUrl(r.slug, ref.name))).toBe(true);
        for (const e of ref.tree) {
          if (e.type === 'tree') expect(urls.has(treeUrl(r.slug, ref.name, e.path))).toBe(true);
          else if (e.type === 'blob' || e.type === 'symlink') expect(urls.has(blobUrl(r.slug, ref.name, e.path))).toBe(true);
        }
      }
      // and the reverse: tree/blob routes never invent paths that are not in the artifact
      for (const t of treeRoutes(r)) {
        if (t.path !== '') expect(t.ref.tree.some((e) => e.type === 'tree' && e.path === t.path)).toBe(true);
      }
      for (const b of blobRoutes(r)) {
        expect(b.ref.tree.some((e) => e.path === b.entry.path && e.type !== 'tree')).toBe(true);
      }
    }
  });

  it('every stored file has a raw route with readable bytes (binary included)', () => {
    for (const r of data.repos) {
      const raw = rawRoutes(r);
      const rawUrls = new Set(raw.map((x) => x.url));
      for (const ref of browsableRefs(r)) {
        for (const [p, info] of Object.entries(ref.files)) {
          if (info.stored) {
            expect(rawUrls.has(`/repos/${r.slug}/raw/${ref.slugged}/${p}`)).toBe(true);
            expect(readBlobBuffer(outDir, info.sha)).not.toBeNull();
          }
        }
      }
      // binary blob is stored (schema v2) and byte-exact
      const bin = r.files['bin/blob.bin'];
      if (bin) {
        expect(bin.binary).toBe(true);
        expect(bin.stored).toBe(true);
        expect([...readBlobBuffer(outDir, bin.sha)!]).toEqual([0x00, 0x01, 0x02, 0xff, 0x00]);
        expect(readBlob(outDir, bin.sha)).not.toBeNull();
      }
    }
  });

  it('findRef resolves slugged ref names, including "/" → "~"', () => {
    const r = data.repos[0]!;
    expect(findRef(r, 'feat~zip')?.name).toBe('feat/zip');
    expect(findRef(r, 'main')?.isDefault).toBe(true);
    expect(findRef(r, 'does-not-exist')).toBeUndefined();
    // the feature branch tree carries the extra file; main does not
    expect(findRef(r, 'feat~zip')!.tree.some((e) => e.path === 'src/extra.ts')).toBe(true);
    expect(findRef(r, 'main')!.tree.some((e) => e.path === 'src/extra.ts')).toBe(false);
  });

  it('every annotated tag has a release URL; lightweight tags do not', () => {
    for (const r of data.repos) {
      const urls = new Set(repoRoutes(r));
      for (const t of r.gitTags) {
        expect(urls.has(releaseUrl(r.slug, t.name))).toBe(t.annotated);
      }
      expect(releasesOf(r).map((t) => t.name)).toEqual(r.gitTags.filter((t) => t.annotated).map((t) => t.name));
    }
    const r = data.repos[0]!;
    expect(releasesOf(r).map((t) => t.name)).toContain('v1.0.0');
    expect(releasesOf(r).map((t) => t.name)).not.toContain('lightweight');
  });

  it('every archive has an archive URL and its zip exists on disk', () => {
    const r = data.repos[0]!;
    expect(r.archives.length).toBeGreaterThan(0);
    const urls = new Set(repoRoutes(r));
    for (const a of r.archives) {
      expect(urls.has(archiveUrl(r.slug, a.ref))).toBe(true);
      const abs = path.join(outDir, a.file);
      expect(fs.existsSync(abs)).toBe(true);
      expect(fs.statSync(abs).size).toBe(a.bytes);
      expect(a.bytes).toBeGreaterThan(22); // bigger than an empty zip
    }
  });

  it('commit page URLs cover every commit and pagination matches branch commit counts', () => {
    for (const r of data.repos) {
      const urls = new Set(repoRoutes(r));
      for (const sha of Object.keys(r.commits)) expect(urls.has(commitUrl(r.slug, sha))).toBe(true);
      for (const b of r.branches) {
        const pages = commitsPageCount(r, b.name);
        expect(pages).toBe(Math.max(1, Math.ceil(b.commits.length / COMMITS_PER_PAGE)));
        for (let p = 1; p <= pages; p++) expect(urls.has(commitsUrl(r.slug, b.name, p))).toBe(true);
        expect(urls.has(commitsUrl(r.slug, b.name, pages + 1))).toBe(false);
      }
    }
  });
});
