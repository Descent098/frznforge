/**
 * Cross-run reuse (`ingest.reuse`, 0.2.0): the scan cache, the remote freshness window,
 * the run log and the `--no-cache` flag.
 *
 * The scan cache is proven black-box, without a git seam: a cache HIT is demonstrated by
 * tampering with the cached entry and seeing the tampered value in the artifact (only a
 * replay can produce it); invalidation is demonstrated by the tampered value vanishing
 * after the input it keys on changes. That is strictly stronger than counting git calls.
 * Nothing here touches the network; clocks are injected.
 */
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import { resolveConfig, type ResolvedConfig } from '../../src/lib/config/index';
import type { FrznforgeConfigInput } from '../../src/lib/config/schema';
import type { Release } from '../../src/lib/data/schema';
import type { ImportedRepoMeta, Importer } from '../../src/lib/importers/index';
import { ImporterError } from '../../src/lib/importers/index';
import {
  ensureMirror,
  ingest,
  parseIngestArgs,
  readRunLog,
  scanCachePathFor,
  serializeForgeData,
  withinFreshWindow,
  writeArtifact,
  type PrepareRemoteDeps,
} from '../../src/lib/ingest/index';
import { FixtureRepo, at } from './helpers/fixture-repo';

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

function makeConfig(
  root: string,
  repos: FrznforgeConfigInput['repos'],
  ingestOver: Partial<NonNullable<FrznforgeConfigInput['ingest']>> = {},
): ResolvedConfig {
  return resolveConfig(
    {
      owner: { name: 'Test Owner', handle: 'test' },
      repos,
      ingest: {
        outDir: path.join(root, 'data'),
        cacheDir: path.join(root, 'cache'),
        ...ingestOver,
      },
    },
    root,
  );
}

describe('parseIngestArgs', () => {
  it('parses --no-cache and rejects anything else', () => {
    expect(parseIngestArgs([])).toEqual({ noCache: false });
    expect(parseIngestArgs(['--no-cache'])).toEqual({ noCache: true });
    expect(() => parseIngestArgs(['--nope'])).toThrow(/unknown flag: --nope/);
    expect(() => parseIngestArgs(['--no-cache', 'extra'])).toThrow(/unknown flag: extra/);
  });
});

describe('withinFreshWindow', () => {
  const now = new Date('2026-01-01T00:10:00Z');
  it('accepts only a fresh entry inside the window', () => {
    expect(withinFreshWindow({ fetchedAt: '2026-01-01T00:09:00Z', fresh: true }, now, 2)).toBe(true);
    expect(withinFreshWindow({ fetchedAt: '2026-01-01T00:05:00Z', fresh: true }, now, 2)).toBe(false); // too old
    expect(withinFreshWindow({ fetchedAt: '2026-01-01T00:09:00Z', fresh: false }, now, 2)).toBe(false); // degraded
    expect(withinFreshWindow({ fetchedAt: '2026-01-01T00:11:00Z', fresh: true }, now, 2)).toBe(false); // future
    expect(withinFreshWindow({ fetchedAt: 'not a date', fresh: true }, now, 2)).toBe(false);
    expect(withinFreshWindow(undefined, now, 2)).toBe(false);
  });
});

describe('scan cache (local repos)', () => {
  function localSetup() {
    const root = tempDir('reuse-root');
    const repo = newFixture('scanned');
    repo.writeAndCommit({ 'a.txt': 'one\n', 'README.md': '# Scanned\n' }, 'first', { date: at(0) });
    repo.tag('v1', { annotated: true, message: 'release one', date: at(60) });
    const cfg = makeConfig(root, [{ type: 'local', path: repo.dir }]);
    return { root, repo, cfg };
  }

  /** The cached entry for `repo`, parsed; the file must exist. */
  function readEntry(cfg: ResolvedConfig, repoDir: string): { file: string; entry: any } {
    const file = scanCachePathFor(cfg.cacheDir, cfg.repos[0]!.absPath);
    expect(cfg.repos[0]!.absPath).toBe(path.resolve(repoDir));
    const entry = JSON.parse(fs.readFileSync(file, 'utf8'));
    return { file, entry };
  }

  it('replays an unchanged repo from the cache and emits identical bytes', async () => {
    const { repo, cfg } = localSetup();
    const run1 = await ingest(cfg);
    await writeArtifact(run1.data, run1.blobs, run1.archives, cfg.outDir);

    // Prove the second run is a replay: tamper with the cached description. Only the cache
    // can produce this value — the repo on disk never had it.
    const { file, entry } = readEntry(cfg, repo.dir);
    entry.repo.description = 'FROM-CACHE';
    fs.writeFileSync(file, JSON.stringify(entry));

    const run2 = await ingest(cfg);
    expect(run2.data.repos[0]!.description).toBe('FROM-CACHE');
    expect(run2.blobs.size).toBe(run1.blobs.size);
    expect(run2.archives.size).toBe(run1.archives.size);

    // Untampered, the replay is byte-identical to the fresh scan.
    fs.rmSync(file);
    const run3 = await ingest(cfg);
    expect(serializeForgeData(run3.data)).toBe(serializeForgeData(run1.data));
    const run4 = await ingest(cfg);
    expect(serializeForgeData(run4.data)).toBe(serializeForgeData(run3.data));
  });

  it('invalidates on a new commit (head sha change)', async () => {
    const { repo, cfg } = localSetup();
    const run1 = await ingest(cfg);
    await writeArtifact(run1.data, run1.blobs, run1.archives, cfg.outDir);
    const { file, entry } = readEntry(cfg, repo.dir);
    entry.repo.description = 'FROM-CACHE';
    fs.writeFileSync(file, JSON.stringify(entry));

    repo.writeAndCommit({ 'b.txt': 'two\n' }, 'second', { date: at(120) });
    const run2 = await ingest(cfg);
    expect(run2.data.repos[0]!.description).not.toBe('FROM-CACHE');
    expect(run2.data.repos[0]!.commitCount).toBe(2);
  });

  it('invalidates on changed scan options', async () => {
    const { repo, cfg } = localSetup();
    const run1 = await ingest(cfg);
    await writeArtifact(run1.data, run1.blobs, run1.archives, cfg.outDir);
    const { file, entry } = readEntry(cfg, repo.dir);
    entry.repo.description = 'FROM-CACHE';
    fs.writeFileSync(file, JSON.stringify(entry));

    const cfg2 = makeConfig(cfg.root, [{ type: 'local', path: repo.dir }], { tagTrees: 3 });
    const run2 = await ingest(cfg2);
    expect(run2.data.repos[0]!.description).not.toBe('FROM-CACHE');
  });

  it('falls back to a real scan when a referenced blob is missing, and never lets writeArtifact prune a replayed repo', async () => {
    const { cfg } = localSetup();
    const run1 = await ingest(cfg);
    await writeArtifact(run1.data, run1.blobs, run1.archives, cfg.outDir);
    const blobDir = path.join(cfg.outDir, 'blobs');
    const blobsBefore = fs.readdirSync(blobDir).sort();
    expect(blobsBefore.length).toBeGreaterThan(0);
    const archiveZip = path.join(cfg.outDir, run1.data.repos[0]!.archives[0]!.file);
    expect(fs.existsSync(archiveZip)).toBe(true);

    // Warm run (replay) followed by writeArtifact: the replay carries its full buffer maps,
    // so the mirror-and-prune pass must keep every blob and archive.
    const run2 = await ingest(cfg);
    await writeArtifact(run2.data, run2.blobs, run2.archives, cfg.outDir);
    expect(fs.readdirSync(blobDir).sort()).toEqual(blobsBefore);
    expect(fs.existsSync(archiveZip)).toBe(true);

    // A deleted blob makes the replay impossible; the run quietly re-scans and heals it.
    fs.rmSync(path.join(blobDir, blobsBefore[0]!));
    const run3 = await ingest(cfg);
    await writeArtifact(run3.data, run3.blobs, run3.archives, cfg.outDir);
    expect(serializeForgeData(run3.data)).toBe(serializeForgeData(run1.data));
    expect(fs.readdirSync(blobDir).sort()).toEqual(blobsBefore);
  });

  it("a slug collision never lets a replay publish the other repo's archive bytes", async () => {
    // Two repos whose directories share a basename → both scan to slug 'twin'; assembly
    // renames the later one to 'twin-2' and rewrites its archive paths AFTER the scan
    // cache was written, so the loser's cached paths point at the winner's zips. The
    // content-hash check must refuse that replay — a reproduced 0.2.0-review bug had the
    // warm run silently overwrite archives/twin-2/*.zip with twin's bytes.
    const root = tempDir('collide');
    fs.writeFileSync(path.join(root, 'gitconfig-empty'), '');
    const makeNamedRepo = (parent: string, content: string): string => {
      const dir = path.join(root, parent, 'twin');
      fs.mkdirSync(dir, { recursive: true });
      const env = {
        ...process.env,
        GIT_AUTHOR_NAME: 'T', GIT_AUTHOR_EMAIL: 't@example.com',
        GIT_COMMITTER_NAME: 'T', GIT_COMMITTER_EMAIL: 't@example.com',
        GIT_AUTHOR_DATE: at(0), GIT_COMMITTER_DATE: at(0),
        GIT_CONFIG_GLOBAL: path.join(root, 'gitconfig-empty'), GIT_CONFIG_NOSYSTEM: '1',
      };
      execFileSync('git', ['init', '-q', '-b', 'main', dir], { env, stdio: 'pipe' });
      fs.writeFileSync(path.join(dir, 'file.txt'), content);
      execFileSync('git', ['-C', dir, 'add', '-A'], { env, stdio: 'pipe' });
      execFileSync('git', ['-C', dir, 'commit', '-q', '-m', 'init'], { env, stdio: 'pipe' });
      return dir;
    };
    const dirA = makeNamedRepo('one', 'AAAA\n');
    const dirB = makeNamedRepo('two', 'BBBB — different content and length\n');
    const cfg = makeConfig(root, [
      { type: 'local', path: dirA },
      { type: 'local', path: dirB },
    ]);

    const run1 = await ingest(cfg);
    await writeArtifact(run1.data, run1.blobs, run1.archives, cfg.outDir);
    const zipA = path.join(cfg.outDir, 'archives', 'twin', 'main.zip');
    const zipB = path.join(cfg.outDir, 'archives', 'twin-2', 'main.zip');
    const bytesA = fs.readFileSync(zipA);
    const bytesB = fs.readFileSync(zipB);
    expect(bytesA.equals(bytesB)).toBe(false);

    const run2 = await ingest(cfg);
    await writeArtifact(run2.data, run2.blobs, run2.archives, cfg.outDir);
    expect(serializeForgeData(run2.data)).toBe(serializeForgeData(run1.data));
    expect(fs.readFileSync(zipA).equals(bytesA)).toBe(true);
    expect(fs.readFileSync(zipB).equals(bytesB)).toBe(true);
  });

  it('reads nothing when reuse is disabled or --no-cache is passed', async () => {
    const { repo, cfg } = localSetup();
    const run1 = await ingest(cfg);
    await writeArtifact(run1.data, run1.blobs, run1.archives, cfg.outDir);
    const { file, entry } = readEntry(cfg, repo.dir);
    entry.repo.description = 'FROM-CACHE';
    fs.writeFileSync(file, JSON.stringify(entry));

    const noCacheRun = await ingest(cfg, {}, { noCache: true });
    expect(noCacheRun.data.repos[0]!.description).not.toBe('FROM-CACHE');

    const disabled = makeConfig(cfg.root, [{ type: 'local', path: repo.dir }], { reuse: { enabled: false } });
    // ⚠ different config → different scan digest? No: reuse config is not a scan input, but
    // assert through the disabled path anyway — the tampered entry must not be read.
    fs.writeFileSync(file, JSON.stringify(entry));
    const run2 = await ingest(disabled);
    expect(run2.data.repos[0]!.description).not.toBe('FROM-CACHE');
  });
});

describe('freshness window (remote sources)', () => {
  function remoteSetup(fetchMode: 'auto' | 'never' | 'always' = 'auto') {
    const root = tempDir('window-root');
    const origin = newFixture('origin');
    origin.writeAndCommit({ 'README.md': '# Widget\n' }, 'first', { date: at(0) });
    const cfg = makeConfig(
      root,
      [{ type: 'gitea', host: 'https://gitea.example.com', owner: 'acme', repo: 'widget' }],
      { fetch: fetchMode },
    );

    let clock = new Date('2026-01-01T00:00:00Z');
    const calls: string[] = [];
    let meta: ImportedRepoMeta = {
      name: null,
      description: 'from the provider',
      homepage: null,
      topics: [],
      license: null,
      defaultBranch: 'main',
      webUrl: 'https://gitea.example.com/acme/widget',
      cloneUrl: 'https://gitea.example.com/acme/widget.git',
      issuesUrl: null,
      template: false,
      archived: false,
    };
    let releases: Release[] = [];
    let failWith: Error | null = null;
    const importer: Importer = {
      provider: 'gitea',
      async fetchMeta() {
        calls.push('meta');
        if (failWith) throw failWith;
        return meta;
      },
      async fetchReleases() {
        calls.push('releases');
        if (failWith) throw failWith;
        return { releases, truncated: false };
      },
    };
    const deps: PrepareRemoteDeps = {
      env: {},
      createImporter: () => importer,
      ensureMirror: (source, cachePath, opts) =>
        ensureMirror(source, cachePath, { ...opts, cloneUrl: origin.dir.replace(/\\/g, '/') }),
      now: () => clock,
    };
    return {
      cfg,
      origin,
      calls,
      deps,
      setClock: (iso: string) => (clock = new Date(iso)),
      setMeta: (m: Partial<ImportedRepoMeta>) => (meta = { ...meta, ...m }),
      setFail: (e: Error | null) => (failWith = e),
    };
  }

  it('skips the fetch inside the window with identical bytes, then refreshes after it', async () => {
    const s = remoteSetup();
    const run1 = await ingest(s.cfg, {}, { remote: s.deps });
    await writeArtifact(run1.data, run1.blobs, run1.archives, s.cfg.outDir);
    expect(s.calls).toEqual(['meta', 'releases']);
    expect(run1.remotes[0]!.action).toBe('cloned');

    const log = await readRunLog(s.cfg.cacheDir);
    expect(log?.remotes[s.cfg.repos[0]!.absPath]).toEqual({ fetchedAt: '2026-01-01T00:00:00.000Z', fresh: true });

    s.setClock('2026-01-01T00:01:00Z'); // inside the 2-minute default window
    const run2 = await ingest(s.cfg, {}, { remote: s.deps });
    expect(s.calls).toEqual(['meta', 'releases']); // untouched
    expect(run2.remotes[0]!.action).toBe('reused');
    expect(serializeForgeData(run2.data)).toBe(serializeForgeData(run1.data));
    // a window-skip must not extend the window
    const log2 = await readRunLog(s.cfg.cacheDir);
    expect(log2?.remotes[s.cfg.repos[0]!.absPath]!.fetchedAt).toBe('2026-01-01T00:00:00.000Z');

    s.setClock('2026-01-01T00:10:00Z'); // outside the window
    const run3 = await ingest(s.cfg, {}, { remote: s.deps });
    expect(s.calls).toEqual(['meta', 'releases', 'meta', 'releases']);
    expect(run3.remotes[0]!.action).toBe('fetched');
    expect(serializeForgeData(run3.data)).toBe(serializeForgeData(run1.data));
  });

  it('always re-attempts a degraded repo, even inside the window', async () => {
    // Three runs, so the dangerous state actually exists: after run 2 the provider cache
    // AND the mirror are both on disk (a buggy window-skip of the degraded entry would
    // succeed silently, serving stale data with zero importer calls) — run 3 must fetch.
    const s = remoteSetup();
    await ingest(s.cfg, {}, { remote: s.deps }); // fully fresh: cache + mirror + stamp
    expect(s.calls.length).toBe(2);

    s.setFail(new ImporterError('rate-limit', 'slow down', { status: 429 }));
    s.setClock('2026-01-01T00:10:00Z'); // outside run 1's window: real, failing fetch
    const run2 = await ingest(s.cfg, {}, { remote: s.deps });
    expect(run2.data.warnings.map((w) => w.code)).toContain('remote-rate-limited');
    expect((await readRunLog(s.cfg.cacheDir))?.remotes[s.cfg.repos[0]!.absPath]!.fresh).toBe(false);

    s.setFail(null);
    s.setClock('2026-01-01T00:11:00Z'); // INSIDE run 2's window — degraded, so no skip
    const run3 = await ingest(s.cfg, {}, { remote: s.deps });
    expect(run3.remotes[0]!.action).toBe('fetched');
    expect(s.calls.length).toBe(6); // re-attempted, and it healed
    expect(run3.data.warnings.map((w) => w.code)).not.toContain('remote-rate-limited');
  });

  it('falls back to a real fetch when the mirror vanished inside the window', async () => {
    // last-run.json and the sibling .meta.json both survive a mirror wipe; a regressed
    // window guard would return 'reused' pointing at a nonexistent path and the repo
    // would silently vanish from the artifact for up to maxAgeMinutes.
    const s = remoteSetup();
    const run1 = await ingest(s.cfg, {}, { remote: s.deps });
    await writeArtifact(run1.data, run1.blobs, run1.archives, s.cfg.outDir);
    fs.rmSync(s.cfg.repos[0]!.absPath, { recursive: true, force: true, maxRetries: 5 });

    s.setClock('2026-01-01T00:01:00Z'); // inside the window
    const run2 = await ingest(s.cfg, {}, { remote: s.deps });
    expect(run2.remotes[0]!.action).toBe('cloned');
    expect(s.calls.length).toBe(4);
    expect(serializeForgeData(run2.data)).toBe(serializeForgeData(run1.data));
  });

  it('ignores the window when the config changed', async () => {
    const s = remoteSetup();
    await ingest(s.cfg, {}, { remote: s.deps });
    expect(s.calls.length).toBe(2);
    s.setClock('2026-01-01T00:01:00Z');
    const changed = makeConfig(
      s.cfg.root,
      [{ type: 'gitea', host: 'https://gitea.example.com', owner: 'acme', repo: 'widget' }],
      { fetch: 'auto', tagTrees: 3 },
    );
    await ingest(changed, {}, { remote: s.deps });
    expect(s.calls.length).toBe(4);
  });

  it("never window-skips under fetch 'always' or with --no-cache", async () => {
    const s = remoteSetup('always');
    await ingest(s.cfg, {}, { remote: s.deps });
    s.setClock('2026-01-01T00:01:00Z');
    await ingest(s.cfg, {}, { remote: s.deps });
    expect(s.calls.length).toBe(4);

    const auto = remoteSetup();
    await ingest(auto.cfg, {}, { remote: auto.deps });
    auto.setClock('2026-01-01T00:01:00Z');
    await ingest(auto.cfg, {}, { remote: auto.deps, noCache: true });
    expect(auto.calls.length).toBe(4);
  });

  it('a --no-cache run records a window the next ordinary run can use', async () => {
    // The run log must be hashed against the CALLER's config, not a mutated fetch:'always'
    // one — otherwise the next ordinary run's config-hash gate rejects the log and the
    // documented "fresh results are still recorded" promise is a dead letter.
    const s = remoteSetup();
    await ingest(s.cfg, {}, { remote: s.deps, noCache: true });
    expect(s.calls.length).toBe(2);
    s.setClock('2026-01-01T00:01:00Z');
    const run2 = await ingest(s.cfg, {}, { remote: s.deps });
    expect(run2.remotes[0]!.action).toBe('reused');
    expect(s.calls.length).toBe(2); // no importer call — the --no-cache run's freshness carried over
  });

  it('refreshed provider metadata with unchanged heads invalidates the scan cache', async () => {
    const s = remoteSetup();
    const run1 = await ingest(s.cfg, {}, { remote: s.deps });
    await writeArtifact(run1.data, run1.blobs, run1.archives, s.cfg.outDir);
    expect(run1.data.repos[0]!.description).toBe('from the provider');

    // New description on the provider, no new commit anywhere. Outside the window so the
    // fetch actually happens; the scan cache must NOT replay the old description.
    s.setMeta({ description: 'rewritten on the forge' });
    s.setClock('2026-01-01T00:10:00Z');
    const run2 = await ingest(s.cfg, {}, { remote: s.deps });
    expect(run2.data.repos[0]!.description).toBe('rewritten on the forge');
  });
});
