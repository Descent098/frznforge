import { describe, expect, it } from 'vitest';
import { SCHEMA_VERSION, parseForgeData, type Repo } from '../../src/lib/data/schema';
import { scanRepo } from '../../src/lib/ingest/scan';
import { FixtureRepo, at } from './helpers/fixture-repo';

const opts = { maxBlobBytes: 1024 * 1024, maxCommits: null };

async function scan(r: FixtureRepo): Promise<Repo> {
  const res = await scanRepo({ absPath: r.dir }, opts);
  if ('skipped' in res) throw new Error(`unexpected skip: ${res.warning.message}`);
  parseForgeData({
    schemaVersion: SCHEMA_VERSION,
    repos: [res.repo],
    notes: [],
    organizations: [],
    warnings: res.repo.warnings,
  });
  return res.repo;
}

describe('empty states', () => {
  it('a. empty repo (no commits) is emitted as empty with repo-empty', async () => {
    const r = FixtureRepo.create('empty');
    try {
      const repo = await scan(r);
      expect(repo.empty).toBe(true);
      expect(repo.defaultBranch).toBeNull();
      expect(repo.branches).toEqual([]);
      expect(repo.gitTags).toEqual([]);
      expect(repo.commits).toEqual({});
      expect(repo.commitCount).toBe(0);
      expect(repo.tree).toEqual([]);
      expect(repo.files).toEqual({});
      expect(repo.languages).toEqual([]);
      expect(repo.contributors).toEqual([]);
      expect(repo.readme).toBeNull();
      expect(repo.license).toBeNull();
      expect(repo.createdAt).toBeNull();
      expect(repo.updatedAt).toBeNull();
      expect(repo.name).toBe('empty');
      expect(repo.warnings.map((w) => w.code)).toEqual(['repo-empty']);
      expect(repo.warnings[0]!.repo).toBe('empty');
    } finally {
      r.cleanup();
    }
  });

  it('a2. empty repo still honours config license override', async () => {
    const r = FixtureRepo.create('empty2');
    try {
      const res = await scanRepo({ absPath: r.dir, overrides: { license: 'MIT', description: 'd' } }, opts);
      if ('skipped' in res) throw new Error('skipped');
      expect(res.repo.license).toEqual({ spdx: 'MIT', file: null, source: 'config' });
      expect(res.repo.description).toBe('d');
    } finally {
      r.cleanup();
    }
  });

  it('b. latest commit deletes everything ⇒ empty tree but not an empty repo', async () => {
    const r = FixtureRepo.create('deleted');
    try {
      r.writeAndCommit({ 'README.md': '# gone\n', 'src/a.ts': 'x\n' }, 'add', { date: at(0) });
      r.rm('.');
      r.commit('delete everything', { date: at(60) });
      const repo = await scan(r);
      expect(repo.empty).toBe(false);
      expect(repo.defaultBranch).toBe('main');
      expect(repo.commitCount).toBe(2);
      expect(repo.branches[0]!.commits).toHaveLength(2);
      expect(repo.tree).toEqual([]);
      expect(repo.files).toEqual({});
      expect(repo.readme).toBeNull();
      expect(repo.license).toBeNull();
      expect(repo.languages).toEqual([]);
      expect(repo.contributors).toHaveLength(1);
      expect(repo.createdAt).toBe(at(0));
      expect(repo.updatedAt).toBe(at(60));
      expect(repo.warnings.map((w) => w.code)).toEqual(['default-branch-empty-tree']);
    } finally {
      r.cleanup();
    }
  });

  it('c. main unborn, work on another branch ⇒ fallback with full content', async () => {
    const r = FixtureRepo.create('unborn-main');
    try {
      r.checkout('feature', true);
      const sha = r.writeAndCommit({ 'README.md': '# feature\n', 'src/x.ts': 'export {};\n' }, 'work', { date: at(0) });
      r.setHead('main');
      const repo = await scan(r);
      expect(repo.empty).toBe(false);
      expect(repo.defaultBranch).toBe('feature');
      expect(repo.branches.map((b) => b.name)).toEqual(['feature']);
      expect(repo.commitCount).toBe(1);
      expect(repo.tree.map((e) => e.path)).toEqual(['README.md', 'src', 'src/x.ts']);
      expect(repo.readme).toMatchObject({ path: 'README.md', content: '# feature\n' });
      expect(repo.languages[0]!.name).toBe('TypeScript');
      expect(repo.tree[0]!.lastCommit).toBe(sha);
      expect(repo.warnings.map((w) => w.code)).toEqual(['default-branch-fallback']);
    } finally {
      r.cleanup();
    }
  });

  it('works on a bare repository', async () => {
    const src = FixtureRepo.create('src');
    let bare: FixtureRepo | null = null;
    try {
      src.writeAndCommit({ 'README.md': '# bare\n' }, 'init', { date: at(0) });
      src.tag('v1', { annotated: true, message: 'one' });
      bare = FixtureRepo.createBare('bare.git');
      src.git('push', '-q', '--mirror', bare.dir);
      const res = await scanRepo({ absPath: bare.dir }, opts);
      if ('skipped' in res) throw new Error(res.warning.message);
      expect(res.repo.slug).toBe('bare');
      expect(res.repo.defaultBranch).toBe('main');
      expect(res.repo.readme!.content).toBe('# bare\n');
      expect(res.repo.gitTags.map((t) => t.name)).toEqual(['v1']);
      expect(res.repo.warnings).toEqual([]);
    } finally {
      src.cleanup();
      bare?.cleanup();
    }
  });
});
