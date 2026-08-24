import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import { resolveConfig, type ResolvedConfig } from '../../src/lib/config/index';
import { loadForgeData, readBlob, resetForgeDataCache } from '../../src/lib/data/load';
import { parseForgeData, type ForgeData } from '../../src/lib/data/schema';
import { ingest, serializeForgeData, writeArtifact } from '../../src/lib/ingest/index';
import { FixtureRepo, at } from './helpers/fixture-repo';

/** Replace temp paths so the snapshot is machine-independent. */
function redact(data: ForgeData, roots: string[]): ForgeData {
  const sorted = [...roots].sort((a, b) => b.length - a.length);
  return JSON.parse(
    JSON.stringify(data, (key, value) => {
      if (key === 'path' && typeof value === 'string' && path.isAbsolute(value)) return '<redacted>';
      if (typeof value === 'string') {
        let v = value;
        for (const r of sorted) v = v.split(r).join('<tmp>');
        v = v.replace(/<tmp>\\/g, '<tmp>/');
        return v;
      }
      return value;
    }),
  );
}

describe('ingest', () => {
  let alpha: FixtureRepo;
  let beta: FixtureRepo;
  let outDir: string;
  let config: ResolvedConfig;

  beforeAll(() => {
    alpha = FixtureRepo.create('alpha');
    alpha.writeAndCommit(
      {
        'README.md': '# Alpha\n\nHello.\n',
        LICENSE: 'MIT License\n\nPermission is hereby granted, free of charge, to any person obtaining a copy\n',
        'src/index.ts': 'export const alpha = true;\n',
        'src/util/math.ts': 'export const add = (a: number, b: number) => a + b;\n',
        'assets/pixel.bin': Buffer.from([0, 1, 2, 3]),
        '.frznforge.json': JSON.stringify({ description: 'Alpha repo', tags: ['demo'], links: { homepage: 'https://alpha.example' } }),
      },
      'initial commit',
      { date: at(0) },
    );
    alpha.writeAndCommit({ 'src/index.ts': 'export const alpha = false;\n' }, 'flip alpha\n\nLonger explanation.\n', {
      date: at(60),
      author: { name: 'Other Dev', email: 'other@example.com' },
    });
    alpha.tag('v0.1.0', { annotated: true, message: 'First release', date: at(90) });
    alpha.branch('dev');
    alpha.checkout('dev');
    alpha.writeAndCommit({ 'src/dev.ts': 'export {};\n' }, 'dev work', { date: at(120) });
    alpha.checkout('main');

    beta = FixtureRepo.create('beta');
    beta.writeAndCommit({ 'main.py': 'print("beta")\n', 'README': 'beta readme\n' }, 'beta init', { date: at(0) });
    beta.tag('light');

    outDir = fs.mkdtempSync(path.join(os.tmpdir(), 'frznforge-out-'));
    config = resolveConfig(
      {
        owner: { name: 'Tester', handle: 'tester' },
        repos: [
          { type: 'local', path: beta.dir },
          { type: 'local', path: alpha.dir, overrides: { tags: ['demo', 'override'] } },
          { type: 'local', path: path.join(os.tmpdir(), 'definitely-not-a-repo-frznforge') },
          { type: 'local', path: alpha.dir, slug: 'beta' },
        ],
        ingest: { outDir, maxBlobBytes: 1024 * 1024, maxCommits: null, concurrency: 2 },
      },
      os.tmpdir(),
    );
  });
  afterAll(() => {
    alpha.cleanup();
    beta.cleanup();
    fs.rmSync(outDir, { recursive: true, force: true, maxRetries: 5 });
  });

  it('scans repos concurrently, sorts by slug, reports missing repos and slug collisions', async () => {
    const started: string[] = [];
    const done: string[] = [];
    const { data, blobs } = await ingest(config, {
      onRepoStart: (s) => started.push(s),
      onRepoDone: (r) => done.push(r.slug),
    });
    parseForgeData(data);
    expect(data.repos.map((r) => r.slug)).toEqual(['alpha', 'beta', 'beta-2']);
    expect(started.sort()).toEqual(['alpha', 'beta', 'beta', 'definitely-not-a-repo-frznforge']);
    expect(done.sort()).toEqual(['alpha', 'beta', 'beta']);

    const codes = data.warnings.map((w) => [w.code, w.repo]);
    expect(codes).toContainEqual(['repo-not-found', null]);
    expect(codes).toContainEqual(['slug-collision', 'beta-2']);

    const a = data.repos[0]!;
    expect(a.description).toBe('Alpha repo');
    expect(a.tags).toEqual(['demo', 'override']);
    expect(a.links).toEqual({ homepage: 'https://alpha.example' });
    expect(a.license).toEqual({ spdx: 'MIT', file: 'LICENSE', source: 'file' });
    expect(a.branches.map((b) => b.name)).toEqual(['dev', 'main']);
    expect(a.commitCount).toBe(3);
    expect(a.gitTags[0]).toMatchObject({ name: 'v0.1.0', annotated: true, message: 'First release' });
    expect(a.contributors.map((c) => c.name)).toEqual(['Test User', 'Other Dev']);
    expect(a.files['assets/pixel.bin']!.binary).toBe(true);
    expect(a.files['assets/pixel.bin']!.stored).toBe(true); // schema v2: small binaries are stored too
    expect(Object.keys(a.files).includes('src/dev.ts')).toBe(false); // dev branch file not in main tree
    for (const f of Object.values(a.files)) if (f.stored) expect(blobs.has(f.sha)).toBe(true);
    expect(blobs.get(a.files['assets/pixel.bin']!.sha)).toEqual(Buffer.from([0, 1, 2, 3]));

    const b2 = data.repos[2]!;
    expect(b2.slug).toBe('beta-2');
    expect(b2.name).toBe('alpha');
    expect(b2.warnings.every((w) => w.repo === 'beta-2')).toBe(true);
    // renamed slug also moves its archives
    expect(b2.archives.every((x) => x.file.startsWith('archives/beta-2/'))).toBe(true);
  });

  it('produces refTrees for non-default branches and archives for default branch + tags', async () => {
    const { data, blobs, archives } = await ingest(config);
    const a = data.repos[0]!;
    expect(Object.keys(a.refTrees).sort()).toEqual(['dev', 'v0.1.0']);
    expect(a.refTrees['main']).toBeUndefined(); // default branch stays in tree/files
    const dev = a.refTrees['dev']!;
    expect(dev.kind).toBe('branch');
    expect(dev.files['src/dev.ts']).toBeDefined();
    expect(blobs.has(dev.files['src/dev.ts']!.sha)).toBe(true);
    expect(a.refTrees['v0.1.0']!.kind).toBe('tag');

    expect(a.archives.map((x) => [x.ref, x.kind, x.file])).toEqual([
      ['main', 'branch', 'archives/alpha/main.zip'],
      ['v0.1.0', 'tag', 'archives/alpha/v0.1.0.zip'],
    ]);
    for (const x of a.archives) {
      expect(x.bytes).toBeGreaterThan(22); // more than an empty zip
      const buf = archives.get(x.file)!;
      expect(buf).toBeDefined();
      expect(buf.length).toBe(x.bytes);
    }
  });

  it('is deterministic across runs', async () => {
    const one = await ingest(config);
    const two = await ingest(config);
    expect(serializeForgeData(one.data)).toBe(serializeForgeData(two.data));
    expect([...one.blobs.keys()].sort()).toEqual([...two.blobs.keys()].sort());
    expect([...one.archives.keys()]).toEqual([...two.archives.keys()]);
    for (const [file, buf] of one.archives) expect(two.archives.get(file)).toEqual(buf);
  });

  it('matches the artifact snapshot (paths redacted)', async () => {
    const { data } = await ingest(config);
    expect(redact(data, [os.tmpdir(), path.dirname(alpha.dir), path.dirname(beta.dir)])).toMatchSnapshot();
  });

  it('writes forge.json + blobs + archives and removes stale files', async () => {
    const { data, blobs, archives } = await ingest(config);
    const blobDir = path.join(outDir, 'blobs');
    fs.mkdirSync(blobDir, { recursive: true });
    const stale = path.join(blobDir, 'f'.repeat(40));
    fs.writeFileSync(stale, 'stale');
    // pre-existing blob with wrong size gets rewritten
    const [someSha, someBuf] = [...blobs.entries()][0]!;
    fs.writeFileSync(path.join(blobDir, someSha), Buffer.concat([someBuf, Buffer.from('junk')]));
    // bogus file inside archives/ must be swept away
    const archiveDir = path.join(outDir, 'archives');
    fs.mkdirSync(path.join(archiveDir, 'alpha'), { recursive: true });
    const staleZip = path.join(archiveDir, 'alpha', 'bogus.zip');
    fs.writeFileSync(staleZip, 'not a zip');

    await writeArtifact(data, blobs, archives, outDir);

    const json = fs.readFileSync(path.join(outDir, 'forge.json'), 'utf8');
    expect(json).toBe(serializeForgeData(data));
    expect(json.endsWith('}\n')).toBe(true);
    expect(fs.existsSync(stale)).toBe(false);
    expect(fs.readdirSync(blobDir).sort()).toEqual([...blobs.keys()].sort());
    expect(fs.readFileSync(path.join(blobDir, someSha))).toEqual(someBuf);

    expect(fs.existsSync(staleZip)).toBe(false);
    for (const [file, buf] of archives) {
      expect(fs.readFileSync(path.join(outDir, file))).toEqual(buf);
    }

    resetForgeDataCache();
    const loaded = loadForgeData(outDir);
    expect(loaded.repos.map((r) => r.slug)).toEqual(['alpha', 'beta', 'beta-2']);
    const readmeSha = loaded.repos[0]!.files['README.md']!.sha;
    expect(readBlob(outDir, readmeSha)).toBe('# Alpha\n\nHello.\n');

    // a second write with a subset of blobs/archives deletes the rest
    const subset = new Map([[someSha, someBuf]]);
    await writeArtifact(data, subset, new Map(), outDir);
    expect(fs.readdirSync(blobDir)).toEqual([someSha]);
    expect(fs.readdirSync(archiveDir)).toEqual([]);
  });

  it('refuses to write an invalid artifact', async () => {
    const bad = { schemaVersion: 1, repos: [{ slug: 'Bad Slug' }], warnings: [] } as unknown as ForgeData;
    await expect(writeArtifact(bad, new Map(), new Map(), outDir)).rejects.toThrow();
  });

  it('handles a config with no repos', async () => {
    const empty = resolveConfig({ owner: { name: 'T', handle: 't' }, ingest: { outDir } }, os.tmpdir());
    const { data, blobs, archives } = await ingest(empty);
    expect(data).toEqual({ schemaVersion: 4, repos: [], notes: [], organizations: [], warnings: [] });
    expect(blobs.size).toBe(0);
    expect(archives.size).toBe(0);
  });
});
