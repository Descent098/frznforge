/**
 * Remote sources: mirror cache mechanics + the metadata precedence chain.
 *
 * No test here touches the network. The "remote" is always a local fixture repo cloned by
 * path (`git clone --mirror <dir>` works exactly like an https clone), and every provider
 * call goes through a stub importer.
 */
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import { cachePathFor, hostSlug, resolveConfig, type ResolvedConfig } from '../../src/lib/config/index';
import type { FrznforgeConfigInput, GiteaSourceConfig } from '../../src/lib/config/schema';
import { RepoMetaInput, SCHEMA_VERSION, type Release, type Warning } from '../../src/lib/data/schema';
import type { ImportedRepoMeta, Importer } from '../../src/lib/importers/index';
import { ImporterError } from '../../src/lib/importers/index';
import { ingest, serializeForgeData, writeArtifact } from '../../src/lib/ingest/index';
import { ensureMirror, prepareRemote, type GitRunner, type RemoteRepoInput } from '../../src/lib/ingest/remote';
import { scanRepo } from '../../src/lib/ingest/scan';
import { FixtureRepo, at } from './helpers/fixture-repo';

const SCAN_OPTS = { maxBlobBytes: 512 * 1024, maxCommits: null, tagTrees: 5, archives: false };

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

function gitIn(dir: string, ...args: string[]): string {
  return execFileSync('git', ['-C', dir, ...args], { encoding: 'utf8' }).trim();
}

/** A gitea source pointing at nothing reachable — every test injects the real clone URL. */
function giteaSource(overrides: Partial<GiteaSourceConfig> = {}): GiteaSourceConfig {
  return {
    type: 'gitea',
    host: 'https://gitea.example.com',
    owner: 'acme',
    repo: 'widget',
    ...overrides,
  } as GiteaSourceConfig;
}

function makeConfig(
  root: string,
  cacheDir: string,
  repos: FrznforgeConfigInput['repos'],
  fetchMode: 'auto' | 'never' | 'always' = 'auto',
): ResolvedConfig {
  return resolveConfig(
    {
      owner: { name: 'Test Owner', handle: 'test' },
      repos,
      ingest: { outDir: path.join(root, 'data'), cacheDir, fetch: fetchMode },
    },
    root,
  );
}

/** The resolved (absPath-carrying) remote source at `index`, narrowed for prepareRemote. */
function remoteAt(cfg: ResolvedConfig, index = 0): RemoteRepoInput {
  const src = cfg.repos[index]!;
  if (src.type === 'local') throw new Error('expected a remote source');
  return src;
}

function providerMeta(over: Partial<ImportedRepoMeta> = {}): ImportedRepoMeta {
  return {
    name: null,
    description: 'from the provider',
    homepage: 'https://provider.example/home',
    topics: ['provider-topic'],
    license: 'Apache-2.0',
    defaultBranch: 'main',
    webUrl: 'https://gitea.example.com/acme/widget',
    cloneUrl: 'https://gitea.example.com/acme/widget.git',
    issuesUrl: 'https://gitea.example.com/acme/widget/issues',
    template: false,
    archived: false,
    ...over,
  };
}

function release(tag: string): Release {
  return {
    tag,
    name: `Release ${tag}`,
    body: 'notes',
    url: `https://gitea.example.com/acme/widget/releases/tag/${tag}`,
    prerelease: false,
    publishedAt: at(0),
    author: 'acme',
    assets: [],
  };
}

interface StubOptions {
  meta?: ImportedRepoMeta | (() => never);
  releases?: Release[] | (() => never);
}

function stubImporter(opts: StubOptions = {}): { importer: Importer; calls: string[] } {
  const calls: string[] = [];
  const importer: Importer = {
    provider: 'gitea',
    async fetchMeta() {
      calls.push('meta');
      const meta = opts.meta;
      return typeof meta === 'function' ? meta() : (meta ?? providerMeta());
    },
    async fetchReleases() {
      calls.push('releases');
      const releases = opts.releases;
      return { releases: typeof releases === 'function' ? releases() : (releases ?? []), truncated: false };
    },
  };
  return { importer, calls };
}

/** ensureMirror bound to a local origin, so nothing resolves a hostname. */
function localMirror(origin: string): typeof ensureMirror {
  return (source, cachePath, opts) => ensureMirror(source, cachePath, { ...opts, cloneUrl: origin });
}

function codes(warnings: Warning[]): string[] {
  return warnings.map((w) => w.code);
}

describe('cachePathFor', () => {
  /** The readable prefix of a mirror path, with the identity digest stripped off. */
  const withoutDigest = (p: string | null): string | null =>
    p === null ? null : p.replace(/-[0-9a-f]{8}\.git$/, '.git');

  it('lays mirrors out per provider/host/owner', () => {
    const cache = path.join('/cache');
    expect(cachePathFor(cache, { type: 'local', path: './x' })).toBeNull();
    expect(
      withoutDigest(cachePathFor(cache, { type: 'github', host: 'https://api.github.com', owner: 'a', repo: 'b' })),
    ).toBe(path.join(cache, 'github', 'api.github.com', 'a', 'b.git'));
    expect(withoutDigest(cachePathFor(cache, { type: 'gitlab', host: 'https://gitlab.com', project: 'g/s/p' }))).toBe(
      path.join(cache, 'gitlab', 'gitlab.com', 'g', 's', 'p.git'),
    );
    expect(withoutDigest(cachePathFor(cache, giteaSource({ host: 'https://gitea.example.com:3000/git' })))).toBe(
      path.join(cache, 'gitea', 'gitea.example.com-3000-git', 'acme', 'widget.git'),
    );
    expect(hostSlug('https://Codeberg.org/')).toBe('codeberg.org');
  });

  it('never maps two different repos onto one mirror', () => {
    const cache = path.join('/cache');
    const gh = (owner: string, repo: string) => cachePathFor(cache, { type: 'github', host: 'https://api.github.com', owner, repo });
    // Every character outside [a-z0-9._-] is dropped by the sanitiser, so these two names
    // used to collapse to the same directory and the second repo was published showing the
    // first one's tree, commits and README.
    expect(gh('acme', '文档')).not.toBe(gh('acme', 'ドキュメント'));
    // Case folding is the other way two repos used to collide.
    expect(gh('acme', 'Widget')).not.toBe(gh('acme', 'widget'));
    // …and the same name on two hosts, or under two providers, must stay apart.
    expect(cachePathFor(cache, { type: 'gitea', host: 'https://a.example', owner: 'o', repo: 'r' })).not.toBe(
      cachePathFor(cache, { type: 'gitea', host: 'https://b.example', owner: 'o', repo: 'r' }),
    );
    // Identical sources still agree, so a rebuild reuses its mirror instead of re-cloning.
    expect(gh('acme', 'widget')).toBe(gh('acme', 'widget'));
  });

  it('escapes basenames Windows refuses to create', () => {
    const cache = path.join('/cache');
    for (const repo of ['con', 'aux', 'nul', 'prn', 'com1']) {
      const p = cachePathFor(cache, { type: 'github', host: 'https://api.github.com', owner: 'acme', repo })!;
      expect(path.basename(p).split('.')[0]).not.toBe(repo);
    }
    // A reserved *directory* segment is escaped too.
    const owned = cachePathFor(cache, { type: 'github', host: 'https://api.github.com', owner: 'con', repo: 'r' })!;
    expect(owned.split(path.sep)).toContain('_con');
  });
});

describe('ensureMirror', () => {
  it('clones on the first run and fetches on the second', async () => {
    const origin = newFixture('widget');
    origin.writeAndCommit({ 'a.txt': 'one\n' }, 'first', { date: at(0) });
    const first = origin.head();
    const cacheDir = tempDir('cache');
    const mirrorPath = path.join(cacheDir, 'gitea', 'gitea.example.com', 'acme', 'widget.git');

    const cloned = await ensureMirror(giteaSource(), mirrorPath, { fetch: 'auto', cloneUrl: origin.dir });
    expect(cloned.error).toBeUndefined();
    expect(cloned.action).toBe('cloned');
    expect(gitIn(mirrorPath, 'rev-parse', '--is-bare-repository')).toBe('true');
    expect(gitIn(mirrorPath, 'rev-parse', 'main')).toBe(first);

    origin.writeAndCommit({ 'a.txt': 'two\n' }, 'second', { date: at(60) });
    const second = origin.head();

    const fetched = await ensureMirror(giteaSource(), mirrorPath, { fetch: 'auto', cloneUrl: origin.dir });
    expect(fetched.action).toBe('fetched');
    expect(fetched.error).toBeUndefined();
    expect(gitIn(mirrorPath, 'rev-parse', 'main')).toBe(second);
  });

  it("fetch: 'never' serves the stale cache without running git", async () => {
    const origin = newFixture('widget');
    origin.writeAndCommit({ 'a.txt': 'one\n' }, 'first', { date: at(0) });
    const first = origin.head();
    const cacheDir = tempDir('cache');
    const mirrorPath = path.join(cacheDir, 'widget.git');
    await ensureMirror(giteaSource(), mirrorPath, { fetch: 'auto', cloneUrl: origin.dir });

    origin.writeAndCommit({ 'a.txt': 'two\n' }, 'second', { date: at(60) });
    expect(origin.head()).not.toBe(first);

    const refuse: GitRunner = async (args) => {
      throw new Error(`git must not run offline: ${args.join(' ')}`);
    };
    const res = await ensureMirror(giteaSource(), mirrorPath, {
      fetch: 'never',
      cloneUrl: origin.dir,
      run: refuse,
    });
    expect(res.action).toBe('cached');
    expect(res.error).toBeUndefined();
    // The mirror is deliberately out of date: the new origin commit was never fetched.
    expect(gitIn(mirrorPath, 'rev-parse', 'main')).toBe(first);
  });

  it("fetch: 'never' with no cache reports 'missing'", async () => {
    const cacheDir = tempDir('cache');
    const mirrorPath = path.join(cacheDir, 'nothing.git');
    const res = await ensureMirror(giteaSource(), mirrorPath, {
      fetch: 'never',
      cloneUrl: 'https://gitea.example.com/acme/widget.git',
    });
    expect(res.action).toBe('missing');
    expect(res.error?.message).toContain("ingest.fetch is 'never'");
    expect(fs.existsSync(mirrorPath)).toBe(false);
  });

  it('falls back to the cache when the origin is unreachable', async () => {
    const origin = newFixture('widget');
    origin.writeAndCommit({ 'a.txt': 'one\n' }, 'first', { date: at(0) });
    const first = origin.head();
    const cacheDir = tempDir('cache');
    const mirrorPath = path.join(cacheDir, 'widget.git');
    await ensureMirror(giteaSource(), mirrorPath, { fetch: 'auto', cloneUrl: origin.dir });

    const gone = path.join(path.dirname(origin.dir), 'moved-away');
    fs.renameSync(origin.dir, gone);
    cleanups.push(() => fs.rmSync(gone, { recursive: true, force: true, maxRetries: 5 }));

    const res = await ensureMirror(giteaSource(), mirrorPath, { fetch: 'auto', cloneUrl: origin.dir });
    expect(res.action).toBe('cached');
    expect(res.error).toBeDefined();
    expect(gitIn(mirrorPath, 'rev-parse', 'main')).toBe(first);
  });

  it('reports missing (and leaves nothing behind) when the first clone fails', async () => {
    const cacheDir = tempDir('cache');
    const mirrorPath = path.join(cacheDir, 'gone.git');
    const res = await ensureMirror(giteaSource(), mirrorPath, {
      fetch: 'auto',
      cloneUrl: path.join(cacheDir, 'no-such-origin'),
    });
    expect(res.action).toBe('missing');
    expect(res.error).toBeDefined();
    expect(fs.existsSync(mirrorPath)).toBe(false);
  });

  it('sends the token out of band and never persists or exposes it', async () => {
    const origin = newFixture('widget');
    origin.writeAndCommit({ 'a.txt': 'one\n' }, 'first', { date: at(0) });
    const cacheDir = tempDir('cache');
    const mirrorPath = path.join(cacheDir, 'widget.git');
    const token = 'super-secret-token-value';
    const basic = Buffer.from(`x-access-token:${token}`, 'utf8').toString('base64');

    // Record argv + env, then really run it so the assertions below read a real mirror config.
    const seen: Array<{ args: string[]; env: NodeJS.ProcessEnv }> = [];
    const spy: GitRunner = async (args, { cwd, env }) => {
      seen.push({ args, env: env ?? {} });
      try {
        const stdout = execFileSync('git', args, {
          cwd,
          env: { ...process.env, ...env },
          encoding: 'utf8',
          stdio: ['ignore', 'pipe', 'pipe'],
        });
        return { code: 0, signal: null, stdout, stderr: '' };
      } catch (e) {
        return { code: 1, signal: null, stdout: '', stderr: (e as Error).message };
      }
    };

    const res = await ensureMirror(giteaSource(), mirrorPath, {
      fetch: 'auto',
      cloneUrl: origin.dir,
      token,
      run: spy,
    });
    expect(res.action).toBe('cloned');

    const { args, env } = seen[0]!;
    // The credential travels in the child's environment. On argv it would be readable by any
    // other process on the machine (/proc/<pid>/cmdline, Win32_Process) for the whole clone.
    expect(args.join(' ')).not.toContain(token);
    expect(args.join(' ')).not.toContain(basic);
    expect(args.some((a) => a.includes('extraheader'))).toBe(false);
    expect(args.join(' ')).not.toContain(`${token}@`);

    expect(env.GIT_CONFIG_COUNT).toBe('2');
    expect(env.GIT_CONFIG_KEY_0).toBe('credential.helper');
    expect(env.GIT_CONFIG_VALUE_0).toBe('');
    expect(env.GIT_CONFIG_KEY_1).toBe('http.extraheader');
    expect(env.GIT_CONFIG_VALUE_1).toBe(`Authorization: Basic ${basic}`);

    // …and nothing lands on disk either.
    const persisted = fs.readFileSync(path.join(mirrorPath, 'config'), 'utf8');
    expect(persisted).not.toContain(token);
    expect(persisted).not.toContain('extraheader');
    expect(persisted).not.toContain(basic);
  });

  it('still disables credential helpers when there is no token', async () => {
    const origin = newFixture('widget');
    origin.writeAndCommit({ 'a.txt': 'one\n' }, 'first', { date: at(0) });
    const mirrorPath = path.join(tempDir('cache'), 'widget.git');
    let env: NodeJS.ProcessEnv = {};
    const spy: GitRunner = async (_args, opts) => {
      env = opts.env ?? {};
      return { code: 0, signal: null, stdout: '', stderr: '' };
    };
    await ensureMirror(giteaSource(), mirrorPath, { fetch: 'auto', cloneUrl: origin.dir, run: spy });
    expect(env).toMatchObject({ GIT_CONFIG_COUNT: '1', GIT_CONFIG_KEY_0: 'credential.helper', GIT_CONFIG_VALUE_0: '' });
    expect(env.GIT_CONFIG_KEY_1).toBeUndefined();
  });

  it('turns a failed spawn into a warning instead of an uncaught exception', async () => {
    const mirrorPath = path.join(tempDir('cache'), 'widget.git');
    const exploding: GitRunner = async () => {
      throw new Error('read ENOTCONN');
    };
    // A spawn that dies (an over-long destination path on Windows is the real-world case)
    // must settle as an error result — ensureMirror never throws for an environment problem.
    const res = await ensureMirror(giteaSource(), mirrorPath, {
      fetch: 'auto',
      cloneUrl: 'https://gitea.example.com/acme/widget.git',
      run: exploding,
    });
    expect(res.action).toBe('missing');
    expect(res.error?.message).toContain('ENOTCONN');
  });
});

describe('prepareRemote', () => {
  it('layers config overrides > .frznforge.json > provider metadata', async () => {
    const origin = newFixture('widget');
    origin.writeAndCommit(
      {
        'README.md': '# widget\n',
        '.frznforge.json': JSON.stringify({ description: 'from the repo file', tags: ['file-topic'] }),
      },
      'first',
      { date: at(0) },
    );
    const root = tempDir('root');
    const cacheDir = tempDir('cache');
    const source = giteaSource({ overrides: { name: 'Widget (config)' } });
    const cfg = makeConfig(root, cacheDir, [source]);
    const { importer, calls } = stubImporter();

    const prepared = await prepareRemote(remoteAt(cfg), cfg, {
      createImporter: () => importer,
      ensureMirror: localMirror(origin.dir),
    });
    expect(prepared.ready).toBe(true);
    expect(prepared.action).toBe('cloned');
    expect(prepared.warnings).toEqual([]);
    expect(calls).toEqual(['meta', 'releases']);

    const scanned = await scanRepo(prepared.scanSource, SCAN_OPTS);
    if ('skipped' in scanned) throw new Error('repo was skipped');
    const { repo } = scanned;

    expect(repo.name).toBe('Widget (config)'); // config wins
    expect(repo.description).toBe('from the repo file'); // .frznforge.json beats the provider
    expect(repo.tags).toEqual(['file-topic']); // ditto
    expect(repo.links.homepage).toBe('https://provider.example/home'); // provider fills the gap
    expect(repo.links.upstream).toBe('https://gitea.example.com/acme/widget');
    expect(repo.license).toEqual({ spdx: 'Apache-2.0', file: null, source: 'config' });
    expect(repo.source).toEqual({
      type: 'gitea',
      host: 'https://gitea.example.com',
      owner: 'acme',
      repo: 'widget',
      webUrl: 'https://gitea.example.com/acme/widget',
      cloneUrl: 'https://gitea.example.com/acme/widget.git',
    });
    expect(repo.slug).toBe('widget');
  });

  it('imports provider releases, and honours a repo asking for tag mode', async () => {
    const origin = newFixture('widget');
    origin.writeAndCommit({ 'a.txt': 'one\n' }, 'first', { date: at(0) });
    const root = tempDir('root');
    const source = giteaSource();
    const cfg = makeConfig(root, tempDir('cache'), [source]);
    const { importer } = stubImporter({ releases: [release('v1.0.0')] });

    const prepared = await prepareRemote(remoteAt(cfg), cfg, {
      createImporter: () => importer,
      ensureMirror: localMirror(origin.dir),
    });
    const scanned = await scanRepo(prepared.scanSource, SCAN_OPTS);
    if ('skipped' in scanned) throw new Error('repo was skipped');
    expect(scanned.repo.releaseMode).toBe('provider');
    expect(scanned.repo.releases.map((r) => r.tag)).toEqual(['v1.0.0']);

    // The repo's own file overrides the source default; imported releases are then dropped.
    const tagMode = newFixture('widget');
    tagMode.writeAndCommit({ '.frznforge.json': JSON.stringify({ releaseMode: 'tags' }) }, 'first', { date: at(0) });
    const cfg2 = makeConfig(root, tempDir('cache'), [source]);
    const prepared2 = await prepareRemote(remoteAt(cfg2), cfg2, {
      createImporter: () => stubImporter({ releases: [release('v1.0.0')] }).importer,
      ensureMirror: localMirror(tagMode.dir),
    });
    const scanned2 = await scanRepo(prepared2.scanSource, SCAN_OPTS);
    if ('skipped' in scanned2) throw new Error('repo was skipped');
    expect(scanned2.repo.releaseMode).toBe('tags');
    expect(scanned2.repo.releases).toEqual([]);
  });

  it("fetch: 'never' serves the cached provider data instead of blanking the repo", async () => {
    const origin = newFixture('widget');
    origin.writeAndCommit({ 'a.txt': 'one\n' }, 'first', { date: at(0) });
    const root = tempDir('root');
    const cacheDir = tempDir('cache');
    const source = giteaSource();

    // Seed a cache with a normal run first.
    const warm = makeConfig(root, cacheDir, [source]);
    const online = await prepareRemote(remoteAt(warm), warm, {
      createImporter: () => stubImporter({ releases: [release('v1.0.0')] }).importer,
      ensureMirror: localMirror(origin.dir),
    });

    const cfg = makeConfig(root, cacheDir, [source], 'never');
    const { importer, calls } = stubImporter();
    const prepared = await prepareRemote(remoteAt(cfg), cfg, {
      createImporter: () => importer,
      ensureMirror: localMirror(origin.dir),
    });
    expect(calls).toEqual([]);
    expect(prepared.ready).toBe(true);
    expect(prepared.action).toBe('cached');
    expect(codes(prepared.warnings)).toEqual(['remote-cache-stale']);
    expect(prepared.warnings[0]!.message).toContain('came from the cache');
    // The offline build must publish exactly what the online one did: dropping the
    // description, topics, links and releases here silently rewrites the repo's pages.
    expect(prepared.providerMeta).toEqual(online.providerMeta);
    expect(prepared.releases.map((r) => r.tag)).toEqual(['v1.0.0']);
    expect(prepared.scanSource.source).toMatchObject({ type: 'gitea', webUrl: 'https://gitea.example.com/acme/widget' });
  });

  it('falls back to the cached provider data when the API call fails', async () => {
    const origin = newFixture('widget');
    origin.writeAndCommit({ 'a.txt': 'one\n' }, 'first', { date: at(0) });
    const root = tempDir('root');
    const cacheDir = tempDir('cache');
    const source = giteaSource();

    const warm = makeConfig(root, cacheDir, [source]);
    const online = await prepareRemote(remoteAt(warm), warm, {
      createImporter: () => stubImporter({ releases: [release('v1.0.0')] }).importer,
      ensureMirror: localMirror(origin.dir),
    });

    const boom = () => {
      throw new ImporterError('network', 'GET https://gitea.example.com/api/v1/repos/acme/widget failed');
    };
    const cfg = makeConfig(root, cacheDir, [source]);
    const prepared = await prepareRemote(remoteAt(cfg), cfg, {
      createImporter: () => stubImporter({ meta: boom, releases: boom }).importer,
      ensureMirror: localMirror(origin.dir),
    });
    expect(codes(prepared.warnings)).toEqual([
      'remote-fetch-failed',
      'remote-fetch-failed',
      'remote-cache-stale',
    ]);
    expect(prepared.warnings[2]!.message).toContain('cached provider metadata and releases were used');
    expect(prepared.providerMeta).toEqual(online.providerMeta);
    expect(prepared.releases.map((r) => r.tag)).toEqual(['v1.0.0']);
  });

  it('says so plainly when there is nothing cached to fall back to', async () => {
    const origin = newFixture('widget');
    origin.writeAndCommit({ 'a.txt': 'one\n' }, 'first', { date: at(0) });
    const root = tempDir('root');
    const cacheDir = tempDir('cache');
    const source = giteaSource();

    // Warm only the mirror, never the provider cache.
    const warm = makeConfig(root, cacheDir, [source], 'never');
    await prepareRemote(remoteAt(warm), warm, { ensureMirror: localMirror(origin.dir) });
    const seeded = makeConfig(root, cacheDir, [source]);
    await prepareRemote(remoteAt(seeded), seeded, {
      createImporter: () => null,
      ensureMirror: localMirror(origin.dir),
    });

    const cfg = makeConfig(root, cacheDir, [source], 'never');
    const prepared = await prepareRemote(remoteAt(cfg), cfg, {
      createImporter: () => stubImporter().importer,
      ensureMirror: localMirror(origin.dir),
    });
    expect(codes(prepared.warnings)).toEqual(['remote-cache-stale']);
    expect(prepared.warnings[0]!.message).toContain('nothing is cached');
    expect(prepared.providerMeta).toBeNull();
  });

  it('warns when the provider had more releases than one build will page through', async () => {
    const origin = newFixture('widget');
    origin.writeAndCommit({ 'a.txt': 'one\n' }, 'first', { date: at(0) });
    const root = tempDir('root');
    const cfg = makeConfig(root, tempDir('cache'), [giteaSource()]);
    const importer: Importer = {
      provider: 'gitea',
      fetchMeta: async () => providerMeta(),
      fetchReleases: async () => ({ releases: [release('v1.0.0')], truncated: true }),
    };
    const prepared = await prepareRemote(remoteAt(cfg), cfg, {
      createImporter: () => importer,
      ensureMirror: localMirror(origin.dir),
    });
    expect(codes(prepared.warnings)).toEqual(['remote-fetch-failed']);
    expect(prepared.warnings[0]!.message).toContain('more releases than one build will page through');
  });

  it('names the repo and slug from the config, not from the cache directory', async () => {
    const origin = newFixture('widget');
    origin.writeAndCommit({ 'a.txt': 'one\n' }, 'first', { date: at(0) });
    const root = tempDir('root');
    const source = giteaSource({ owner: 'Acme', repo: 'MyProject' });
    const cfg = makeConfig(root, tempDir('cache'), [source]);

    // No provider metadata at all: the fallback must still be the configured name, not the
    // lower-cased, hash-suffixed mirror directory it happens to live in.
    const prepared = await prepareRemote(remoteAt(cfg), cfg, {
      createImporter: () => null,
      ensureMirror: localMirror(origin.dir),
    });
    expect(prepared.scanSource.slug).toBe('myproject');
    expect(prepared.scanSource.defaultName).toBe('MyProject');
    const scanned = await scanRepo(prepared.scanSource, SCAN_OPTS);
    if ('skipped' in scanned) throw new Error('repo was skipped');
    expect(scanned.repo.name).toBe('MyProject');
    expect(scanned.repo.slug).toBe('myproject');

    // …and the provider's own spelling wins over the config when the API answers.
    const withApi = await prepareRemote(remoteAt(cfg), cfg, {
      createImporter: () => stubImporter({ meta: providerMeta({ name: 'MyProject!' }) }).importer,
      ensureMirror: localMirror(origin.dir),
    });
    const rescanned = await scanRepo(withApi.scanSource, SCAN_OPTS);
    if ('skipped' in rescanned) throw new Error('repo was skipped');
    expect(rescanned.repo.name).toBe('MyProject!');
  });

  it('drops a provider description the artifact schema would reject, and keeps building', async () => {
    const origin = newFixture('widget');
    origin.writeAndCommit({ 'a.txt': 'one\n' }, 'first', { date: at(0) });
    const root = tempDir('root');
    const cfg = makeConfig(root, tempDir('cache'), [giteaSource()]);
    // 350 astral characters: 350 code points but 700 UTF-16 units, which is what
    // `z.string().max(300)` counts. Truncating by code points left 595 units and blew up
    // writeArtifact with a ZodError, killing the whole build over one remote description.
    const emoji = '\u{1F600}'.repeat(350);
    const prepared = await prepareRemote(remoteAt(cfg), cfg, {
      createImporter: () => stubImporter({ meta: providerMeta({ description: emoji }) }).importer,
      ensureMirror: localMirror(origin.dir),
    });
    expect(codes(prepared.warnings)).toEqual(['description-truncated']);
    expect(prepared.providerMeta!.description!.length).toBeLessThanOrEqual(300);
    expect(RepoMetaInput.safeParse(prepared.providerMeta).success).toBe(true);

    const scanned = await scanRepo(prepared.scanSource, SCAN_OPTS);
    if ('skipped' in scanned) throw new Error('repo was skipped');
    // The end-to-end guarantee: this repo can be written to disk.
    await expect(
      writeArtifact(
        { schemaVersion: SCHEMA_VERSION, repos: [scanned.repo], notes: [], organizations: [], warnings: [] },
        new Map(),
        new Map(),
        tempDir('out'),
      ),
    ).resolves.toBeUndefined();
  });

  it("reports 'missing' when there is no cache and no network", async () => {
    const root = tempDir('root');
    const cfg = makeConfig(root, tempDir('cache'), [giteaSource()], 'never');
    const prepared = await prepareRemote(remoteAt(cfg), cfg, {
      createImporter: () => stubImporter().importer,
    });
    expect(prepared.ready).toBe(false);
    expect(prepared.action).toBe('missing');
    expect(codes(prepared.warnings)).toEqual(['remote-cache-stale', 'remote-fetch-failed']);
  });

  it('maps importer failures onto the remote-* warning codes', async () => {
    const origin = newFixture('widget');
    origin.writeAndCommit({ 'a.txt': 'one\n' }, 'first', { date: at(0) });
    const root = tempDir('root');
    const source = giteaSource();

    const run = async (kind: 'auth' | 'rate-limit' | 'network', env: Record<string, string | undefined>) => {
      const cfg = makeConfig(root, tempDir('cache'), [source]);
      const boom = () => {
        throw new ImporterError(kind, `GET https://gitea.example.com/api/v1/repos/acme/widget failed`);
      };
      return prepareRemote(remoteAt(cfg), cfg, {
        env,
        createImporter: () => stubImporter({ meta: boom, releases: boom }).importer,
        ensureMirror: localMirror(origin.dir),
      });
    };

    const anon = await run('auth', {});
    expect(codes(anon.warnings)).toEqual(['remote-auth-missing', 'remote-auth-missing']);
    expect(anon.warnings[0]!.message).toContain('FRZNFORGE_GITEA_TOKEN or GITEA_TOKEN');
    // A failed metadata call still leaves a scannable mirror.
    expect(anon.ready).toBe(true);

    const withToken = await run('auth', { GITEA_TOKEN: 'tok' });
    expect(codes(withToken.warnings)).toEqual(['remote-fetch-failed', 'remote-fetch-failed']);

    const limited = await run('rate-limit', {});
    expect(codes(limited.warnings)).toEqual(['remote-rate-limited', 'remote-rate-limited']);

    const offline = await run('network', { GITEA_TOKEN: 'tok' });
    expect(codes(offline.warnings)).toEqual(['remote-fetch-failed', 'remote-fetch-failed']);
  });

  it('never leaks the token into a warning message', async () => {
    const origin = newFixture('widget');
    origin.writeAndCommit({ 'a.txt': 'one\n' }, 'first', { date: at(0) });
    const root = tempDir('root');
    const cfg = makeConfig(root, tempDir('cache'), [giteaSource()]);
    const token = 'tok-abcdef123456';
    const boom = () => {
      throw new ImporterError('network', `https://oauth2:${token}@gitea.example.com refused the connection`);
    };
    const prepared = await prepareRemote(remoteAt(cfg), cfg, {
      env: { GITEA_TOKEN: token },
      createImporter: () => stubImporter({ meta: boom, releases: boom }).importer,
      ensureMirror: localMirror(origin.dir),
    });
    for (const w of prepared.warnings) {
      expect(w.message).not.toContain(token);
      expect(w.message).toContain('***');
    }
  });

  it('warns and keeps the cached mirror when the refresh fails', async () => {
    const origin = newFixture('widget');
    origin.writeAndCommit({ 'a.txt': 'one\n' }, 'first', { date: at(0) });
    const root = tempDir('root');
    const cacheDir = tempDir('cache');
    const source = giteaSource();

    const warm = makeConfig(root, cacheDir, [source]);
    await prepareRemote(remoteAt(warm), warm, {
      createImporter: () => stubImporter().importer,
      ensureMirror: localMirror(origin.dir),
    });

    const gone = path.join(path.dirname(origin.dir), 'moved-away');
    fs.renameSync(origin.dir, gone);
    cleanups.push(() => fs.rmSync(gone, { recursive: true, force: true, maxRetries: 5 }));

    const cfg = makeConfig(root, cacheDir, [source]);
    const prepared = await prepareRemote(remoteAt(cfg), cfg, {
      createImporter: () => stubImporter().importer,
      ensureMirror: localMirror(origin.dir),
    });
    expect(prepared.ready).toBe(true);
    expect(prepared.action).toBe('cached');
    expect(codes(prepared.warnings)).toEqual(['remote-fetch-failed', 'remote-cache-stale']);
  });
});

describe('ingest with remote sources', () => {
  it('attributes remote warnings to the slug the repo actually gets', async () => {
    const origin = newFixture('widget');
    origin.writeAndCommit({ 'a.txt': 'one\n' }, 'first', { date: at(0) });
    const root = tempDir('root');
    const source = giteaSource({ owner: 'Acme', repo: 'MyProject' });
    const cfg = makeConfig(root, tempDir('cache'), [source]);
    const boom = () => {
      throw new ImporterError('network', 'GET https://gitea.example.com/api/v1/repos/Acme/MyProject failed');
    };

    const { data } = await ingest(
      cfg,
      {},
      {
        remote: {
          env: {},
          createImporter: () => stubImporter({ meta: boom, releases: boom }).importer,
          ensureMirror: localMirror(origin.dir),
        },
      },
    );

    const [repo] = data.repos;
    expect(repo!.slug).toBe('myproject');
    // Every warning must name a repo that exists — the pre-scan slug used to come from the
    // mirror directory, which carries the cache digest and matches no repo at all.
    const slugs = new Set(data.repos.map((r) => r.slug));
    for (const w of data.warnings) {
      if (w.repo !== null) expect(slugs).toContain(w.repo);
    }
    for (const w of repo!.warnings) expect(w.repo).toBe('myproject');
  });

  it('produces the same artifact twice for the same repo at the same commits', async () => {
    const origin = newFixture('widget');
    origin.writeAndCommit({ 'a.txt': 'one\n' }, 'first', { date: at(0) });
    const root = tempDir('root');
    const cacheDir = tempDir('cache');
    const cfg = makeConfig(root, cacheDir, [giteaSource()]);
    const deps = () => ({
      env: {},
      createImporter: () => stubImporter({ releases: [release('v1.0.0')] }).importer,
      ensureMirror: localMirror(origin.dir),
    });

    const first = await ingest(cfg, {}, { remote: deps() });
    const second = await ingest(cfg, {}, { remote: deps() });
    expect(serializeForgeData(second.data)).toBe(serializeForgeData(first.data));
    // …and again offline, off the provider cache the first two runs wrote.
    const offline = makeConfig(root, cacheDir, [giteaSource()], 'never');
    const third = await ingest(offline, {}, { remote: deps() });
    expect(third.data.repos[0]!.releases).toEqual(first.data.repos[0]!.releases);
    expect(third.data.repos[0]!.description).toBe(first.data.repos[0]!.description);
  });
});
