import { describe, expect, it } from 'vitest';
import { SCHEMA_VERSION, emptyForgeData, parseForgeData } from '../../src/lib/data/schema';

describe('schema', () => {
  it('accepts the empty artifact', () => {
    expect(parseForgeData(emptyForgeData())).toEqual({
      schemaVersion: SCHEMA_VERSION,
      repos: [],
      notes: [],
      organizations: [],
      hosting: [],
      warnings: [],
    });
  });

  it('rejects a corrupted artifact', () => {
    const bad = {
      schemaVersion: SCHEMA_VERSION,
      repos: [
        {
          slug: 'x',
          name: 'x',
          description: null,
          source: { type: 'local', path: '/x' },
          links: {},
          tags: [],
          template: false,
          license: null,
          releaseMode: 'tags',
          empty: false,
          defaultBranch: 'main',
          branches: [{ name: 'main', head: 'not-a-sha', commits: [], lastCommitDate: '2024-01-01T00:00:00Z' }],
          gitTags: [],
          commits: {},
          commitCount: 0,
          tree: [],
          files: {},
          languages: [],
          contributors: [],
          readme: null,
          createdAt: null,
          updatedAt: null,
          warnings: [],
        },
      ],
      warnings: [],
    };
    expect(() => parseForgeData(bad)).toThrow();
    expect(() => parseForgeData({ schemaVersion: 999, repos: [], warnings: [] })).toThrow();
    expect(() => parseForgeData({ schemaVersion: SCHEMA_VERSION, repos: [], warnings: [{ code: 'nope', repo: null, message: '' }] })).toThrow();
  });
});
