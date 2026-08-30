/**
 * `site.base` (0.2.0) at the unit level: config normalisation, the FRZNFORGE_BASE env
 * override, and every URL builder honouring a mocked base through `setSiteBase` — the real
 * value is Vite-inlined at build time and cannot vary inside one process, which is exactly
 * why the seam exists. The end-to-end half (a real sub-path build with a leak scan) lives
 * in tests/e2e/base-path.spec.ts.
 */
import { afterEach, describe, expect, it } from 'vitest';
import { setSiteBase, siteBase, withBase } from '../../src/lib/base';
import { FrznforgeConfigSchema, normalizeBase, resolveConfig } from '../../src/lib/config/index';
import { emptyForgeData } from '../../src/lib/data/schema';
import {
  allRoutes,
  archiveUrl,
  blobUrl,
  commitUrl,
  notesIndexUrl,
  noteRawUrl,
  orgReposUrl,
  rawUrl,
  repoUrl,
} from '../../src/lib/routes';
import { buildSearchIndex } from '../../src/lib/search';

const OWNER = { owner: { name: 'Test Owner', handle: 'test' } };

afterEach(() => setSiteBase(null));

describe('site.base config', () => {
  it('normalises every spelling to a leading slash and no trailing slash', () => {
    expect(normalizeBase('mysite')).toBe('/mysite');
    expect(normalizeBase('/mysite')).toBe('/mysite');
    expect(normalizeBase('/mysite/')).toBe('/mysite');
    expect(normalizeBase('/a/b/')).toBe('/a/b');
    expect(normalizeBase('')).toBeUndefined();
    expect(normalizeBase('/')).toBeUndefined();
    expect(FrznforgeConfigSchema.parse({ ...OWNER, site: { base: 'mysite/' } }).site.base).toBe('/mysite');
    expect(FrznforgeConfigSchema.parse({ ...OWNER, site: { base: '/' } }).site.base).toBeUndefined();
    expect(FrznforgeConfigSchema.parse(OWNER).site.base).toBeUndefined();
  });

  it('rejects bases no URL prefix can carry', () => {
    for (const bad of ['/my site', '/my#site', '/my%site', '/my?site']) {
      expect(() => FrznforgeConfigSchema.parse({ ...OWNER, site: { base: bad } })).toThrow();
    }
  });

  it('FRZNFORGE_BASE overrides site.base, normalised the same way', () => {
    const prev = process.env.FRZNFORGE_BASE;
    try {
      process.env.FRZNFORGE_BASE = 'mysite/';
      expect(resolveConfig(OWNER).site.base).toBe('/mysite');
      process.env.FRZNFORGE_BASE = '/';
      expect(resolveConfig({ ...OWNER, site: { base: '/from-config' } }).site.base).toBeUndefined();
      delete process.env.FRZNFORGE_BASE;
      expect(resolveConfig({ ...OWNER, site: { base: '/from-config' } }).site.base).toBe('/from-config');
    } finally {
      if (prev === undefined) delete process.env.FRZNFORGE_BASE;
      else process.env.FRZNFORGE_BASE = prev;
    }
  });
});

describe('URL builders under a base', () => {
  it('defaults to no prefix outside a based build', () => {
    expect(siteBase()).toBe('');
    expect(repoUrl('x')).toBe('/repos/x/');
  });

  it('prefixes every builder', () => {
    setSiteBase('/mysite');
    expect(withBase('/')).toBe('/mysite/');
    expect(repoUrl('x')).toBe('/mysite/repos/x/');
    expect(blobUrl('x', 'main', 'a b.md')).toBe('/mysite/repos/x/blob/main/a%20b.md/');
    expect(rawUrl('x', 'feat/zip', 'a.txt')).toBe('/mysite/repos/x/raw/feat~zip/a.txt');
    expect(commitUrl('x', 'abc')).toBe('/mysite/repos/x/commit/abc/');
    expect(archiveUrl('x', 'main')).toBe('/mysite/repos/x/archive/main.zip');
    expect(noteRawUrl('n', 'dir/f.txt')).toBe('/mysite/notes/n/raw/dir/f.txt');
    expect(orgReposUrl('o')).toBe('/mysite/orgs/o/repos/');
    expect(notesIndexUrl()).toBe('/mysite/notes/');
  });

  it('allRoutes and the search index carry the prefix everywhere', () => {
    setSiteBase('/mysite');
    const data = emptyForgeData();
    for (const url of allRoutes(data)) expect(url.startsWith('/mysite/')).toBe(true);
    for (const doc of buildSearchIndex(data).docs) expect(doc.url.startsWith('/mysite/')).toBe(true);
  });
});
