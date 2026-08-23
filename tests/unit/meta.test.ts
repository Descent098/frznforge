import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import { mergeMeta, readRepoMetaFile, slugFor, slugify, truncateDescription } from '../../src/lib/ingest/meta';
import { scanRepo } from '../../src/lib/ingest/scan';
import { FixtureRepo } from './helpers/fixture-repo';

const opts = { maxBlobBytes: 1024 * 1024, maxCommits: null };

describe('meta', () => {
  let repo: FixtureRepo;

  beforeAll(() => {
    repo = FixtureRepo.create('my_Repo Name');
    repo.writeAndCommit(
      {
        'README.md': '# hi\n',
        '.frznforge.json': JSON.stringify({
          name: 'Nice Name',
          description: 'From the repo file',
          tags: ['b', 'a'],
          links: { upstream: 'https://example.com/up', homepage: 'https://example.com' },
          template: true,
          license: 'MIT',
        }),
      },
      'init',
    );
  });
  afterAll(() => repo.cleanup());

  it('slugifies directory names', () => {
    expect(slugify('my_Repo Name')).toBe('my-repo-name');
    expect(slugify('  --Foo.Bar--  ')).toBe('foo-bar');
    expect(slugify('!!!')).toBe('repo');
    expect(slugFor('/x/y/Some.Thing')).toBe('some-thing');
    expect(slugFor('/x/y/Some.Thing', 'custom')).toBe('custom');
    expect(slugFor('/x/y/proj.git')).toBe('proj');
    expect(() => slugFor('/x', 'Bad Slug')).toThrow();
  });

  it('reads + validates the in-repo file from the committed tree', async () => {
    const r = await readRepoMetaFile(repo.dir, 'main');
    expect(r.warnings).toEqual([]);
    expect(r.meta).toEqual({
      name: 'Nice Name',
      description: 'From the repo file',
      tags: ['b', 'a'],
      links: { upstream: 'https://example.com/up', homepage: 'https://example.com' },
      template: true,
      license: 'MIT',
    });
  });

  it('applies precedence overrides > file > defaults', () => {
    const merged = mergeMeta(
      { name: 'dirname' },
      { name: 'File', description: 'file desc', tags: ['x'], links: { issues: 'https://a/i', homepage: 'https://a' } },
      { description: 'override desc', template: true },
    );
    expect(merged).toEqual({
      name: 'File',
      description: 'override desc',
      links: { homepage: 'https://a', issues: 'https://a/i' },
      tags: ['x'],
      template: true,
      license: null,
      releaseMode: 'tags',
    });
    expect(Object.keys(merged.links)).toEqual(['homepage', 'issues']);
    expect(mergeMeta({ name: 'dirname' }, null, undefined)).toEqual({
      name: 'dirname',
      description: null,
      links: {},
      tags: [],
      template: false,
      license: null,
      releaseMode: 'tags',
    });
  });

  it('throws on an over-long override description (config error)', () => {
    expect(() => mergeMeta({ name: 'x' }, null, { description: 'a'.repeat(301) })).toThrow(/300/);
  });

  it('flows through scanRepo with overrides winning', async () => {
    const res = await scanRepo({ absPath: repo.dir, overrides: { description: 'Site override', tags: ['z'] } }, opts);
    if ('skipped' in res) throw new Error('skipped');
    expect(res.repo.slug).toBe('my-repo-name');
    expect(res.repo.name).toBe('Nice Name');
    expect(res.repo.description).toBe('Site override');
    expect(res.repo.tags).toEqual(['z']);
    expect(res.repo.template).toBe(true);
    expect(res.repo.license).toEqual({ spdx: 'MIT', file: null, source: 'config' });
    expect(res.repo.links).toEqual({ homepage: 'https://example.com', upstream: 'https://example.com/up' });
  });

  it('warns and ignores invalid JSON', async () => {
    const r = FixtureRepo.create('badjson');
    try {
      r.writeAndCommit({ '.frznforge.json': '{ not json' }, 'init');
      const res = await readRepoMetaFile(r.dir, 'main');
      expect(res.meta).toBeNull();
      expect(res.warnings.map((w) => w.code)).toEqual(['repo-meta-invalid']);
      const scanned = await scanRepo({ absPath: r.dir }, opts);
      if ('skipped' in scanned) throw new Error('skipped');
      expect(scanned.repo.name).toBe('badjson');
      expect(scanned.repo.warnings.map((w) => w.code)).toEqual(['repo-meta-invalid']);
    } finally {
      r.cleanup();
    }
  });

  it('warns and ignores schema violations', async () => {
    const r = FixtureRepo.create('badschema');
    try {
      r.writeAndCommit({ '.frznforge.json': JSON.stringify({ links: { homepage: 'not a url' }, tags: 'nope' }) }, 'init');
      const res = await readRepoMetaFile(r.dir, 'main');
      expect(res.meta).toBeNull();
      expect(res.warnings[0]!.code).toBe('repo-meta-invalid');
    } finally {
      r.cleanup();
    }
  });

  it('truncates long descriptions from the repo file with a warning', async () => {
    const r = FixtureRepo.create('longdesc');
    try {
      const long = 'd'.repeat(350);
      r.writeAndCommit({ '.frznforge.json': JSON.stringify({ description: long }) }, 'init');
      const res = await readRepoMetaFile(r.dir, 'main');
      expect(res.warnings.map((w) => w.code)).toEqual(['description-truncated']);
      expect(res.meta!.description).toBe('d'.repeat(297) + '…');
      expect(Array.from(res.meta!.description!)).toHaveLength(298);
      expect(truncateDescription('short')).toBe('short');
    } finally {
      r.cleanup();
    }
  });

  it('ignores an uncommitted .frznforge.json on disk', async () => {
    const r = FixtureRepo.create('ondisk');
    try {
      r.writeAndCommit({ 'a.txt': 'a\n' }, 'init');
      r.write({ '.frznforge.json': JSON.stringify({ name: 'Should not be read' }) });
      const res = await readRepoMetaFile(r.dir, 'main');
      expect(res).toEqual({ meta: null, warnings: [] });
    } finally {
      r.cleanup();
    }
  });
});
