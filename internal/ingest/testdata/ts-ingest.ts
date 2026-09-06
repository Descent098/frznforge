/**
 * Reference implementation for the Go port's end-to-end identity test: runs the WHOLE
 * TypeScript ingest pipeline over a corpus the Go side also ingests, and writes the artifact
 * plus its blob and archive stores to disk so the two can be compared byte for byte.
 *
 * Invoked as `npx tsx internal/ingest/testdata/ts-ingest.ts <request.json>` from the project
 * root, the same way `ts-scan.ts` and `ts-assemble.ts` are.
 *
 * The config is read from a file the Go side reads too — strict JSON, which is also valid
 * JSONC — so the two implementations cannot be compared against two different configurations.
 * Only the seams that would otherwise touch the network are replaced, exactly as
 * tests/e2e/global-setup.ts replaces them: `createImporter` hands back canned provider answers
 * and `ensureMirror` is the production function pointed at a local origin directory. Everything
 * else — cache layout, mirror scan, metadata precedence, releaseMode, warnings — is production
 * code, so the artifact this produces is shaped exactly like a real import's.
 *
 * This exists only for tests. Nothing in the shipped site imports it.
 */
import fs from 'node:fs';
import { resolveConfig, type RepoSourceConfig } from '../../../src/lib/config/index';
import type { FrznforgeConfigInput } from '../../../src/lib/config/schema';
import { ensureMirror, ingest, writeArtifact, type PrepareRemoteDeps } from '../../../src/lib/ingest';
import type { ImportedRepoMeta, Importer } from '../../../src/lib/importers/index';
import type { Release } from '../../../src/lib/data/schema';

const requestPath = process.argv[2];
if (!requestPath) {
  console.error('usage: tsx ts-ingest.ts <request.json>');
  process.exit(2);
}

interface RemoteFixture {
  /** Local git repo the mirror is cloned from, in place of the provider's git server. */
  origin: string;
  meta: ImportedRepoMeta;
  releases: Release[];
}

const request = JSON.parse(fs.readFileSync(requestPath, 'utf8')) as {
  /** Project root the config's relative paths resolve against. */
  root: string;
  /** The shared config file — strict JSON, read by the Go side as JSONC. */
  configPath: string;
  outDir: string;
  cacheDir: string;
  /** Keyed by `owner/repo` (or the GitLab project path), as `remoteKey` below spells it. */
  remotes?: Record<string, RemoteFixture>;
  noCache?: boolean;
  backfillMetadata?: boolean;
};

/** Stable key for a configured remote source; matches the Go test's key function. */
function remoteKey(source: RepoSourceConfig): string {
  if (source.type === 'local') return '';
  return source.type === 'gitlab' ? source.project : `${source.owner}/${source.repo}`;
}

const remotes = new Map<string, RemoteFixture>(Object.entries(request.remotes ?? {}));

const remoteDeps: PrepareRemoteDeps = {
  // No token is ever resolved: the env the token comes from is empty.
  env: {},
  createImporter: (source) => {
    const fixture = remotes.get(remoteKey(source));
    if (!fixture) return null;
    const importer: Importer = {
      provider: source.type as Importer['provider'],
      fetchMeta: async () => fixture.meta,
      fetchReleases: async () => ({ releases: fixture.releases, truncated: false }),
    };
    return importer;
  },
  ensureMirror: (source, cachePath, opts) => {
    const fixture = remotes.get(remoteKey(source));
    if (!fixture) throw new Error(`no remote fixture registered for ${remoteKey(source)}`);
    // forward slashes: git accepts them on Windows and they keep the arg quoting simple
    return ensureMirror(source, cachePath, { ...opts, cloneUrl: fixture.origin.replace(/\\/g, '/') });
  },
};

// The env overrides would silently redirect the output away from the directory under
// comparison, so the request's paths are the only ones that count.
delete process.env.FRZNFORGE_OUT_DIR;
delete process.env.FRZNFORGE_CACHE_DIR;
delete process.env.FRZNFORGE_BASE;

// outDir and cacheDir are overridden on the INPUT, before resolution rather than after: a
// remote source's mirror path is derived from the resolved cacheDir, so setting it afterwards
// would leave every mirror pointing into the shared config's cache directory. The Go test
// mutates the parsed config in exactly the same place, which is what lets the two engines read
// one config file and still write to two directories.
const input = JSON.parse(fs.readFileSync(request.configPath, 'utf8')) as FrznforgeConfigInput;
input.ingest = { ...(input.ingest ?? {}), outDir: request.outDir, cacheDir: request.cacheDir };
const config = resolveConfig(input, request.root);

const { data, blobs, archives } = await ingest(
  config,
  {},
  {
    remote: remoteDeps,
    ...(request.noCache ? { noCache: true } : {}),
    ...(request.backfillMetadata ? { backfillMetadata: true } : {}),
  },
);
await writeArtifact(data, blobs, archives, request.outDir);
