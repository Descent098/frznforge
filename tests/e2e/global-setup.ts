/**
 * Playwright global setup: build the site from a deterministic FIXTURE artifact, using the
 * frznforge binary and nothing else.
 *
 *  0. compile cmd/frznforge → tests/.tmp/frznforge
 *  1. create the fixture git repos under tests/.tmp/e2e (local ones in repos/, the stand-in
 *     "provider" ones in origins/)
 *  2. assemble a fixture SITE ROOT at tests/.tmp/e2e/site — its own frznforge.config.jsonc plus
 *     copies of public/ and web/, because a build reads those from its root
 *  3. seed the ingest cache for the two provider repos (a mirror clone plus the provider
 *     response cache beside it) — see "offline provider repos" below
 *  4. `frznforge ingest --backfill-metadata` → tests/.tmp/e2e/data
 *  5. `frznforge build --no-ingest` twice → dist/, and dist-base/ under FRZNFORGE_BASE=/mysite
 *
 *  6. start `frznforge dev` over each of them, and return the teardown that stops both
 *
 * Step 6 lives here rather than in playwright.config.ts's `webServer` because Playwright starts
 * `webServer` BEFORE `globalSetup` — so a server named there is pointed at a directory steps
 * 1-5 have not created yet, and a clean checkout dies before this file runs at all. It only
 * looked like it worked while tests/.tmp survived from an earlier run.
 *
 * Nothing here imports from src/: 0.4.0 deleted the TypeScript engine, and this file was the
 * last thing holding a reference to it.
 *
 * Nothing here touches the network either — that is the whole subject of the next comment.
 */
import { execFileSync, spawn, type ChildProcess } from 'node:child_process';
import { createHash } from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';

const ROOT = path.resolve(import.meta.dirname, '..', '..');
const TMP = path.join(ROOT, 'tests', '.tmp', 'e2e');
const REPOS = path.join(TMP, 'repos');
/** Git repos that stand in for the provider's git server (never scanned directly). */
const ORIGINS = path.join(TMP, 'origins');
const CACHE = path.join(TMP, 'cache');
const DATA = path.join(TMP, 'data');
const DIST = path.join(TMP, 'dist');
/** Second build of the SAME artifact under `site.base: '/mysite'` (0.2.0 base-path e2e). */
const DIST_BASE = path.join(TMP, 'dist-base');
/**
 * The project root the binary is pointed at: its own config, its own copies of public/ and web/.
 * `frznforge.config.jsonc` is the only filename config.Load looks for and there is no env
 * override for it, so a fixture config means a fixture root.
 */
const SITE = path.join(TMP, 'site');
/**
 * Built once per run, and deliberately OUTSIDE tests/.tmp/e2e: step 1 wipes that directory, and
 * on Windows a running executable cannot be deleted — a dev server from a previous run that had
 * not finished dying would otherwise make the wipe fail rather than the server.
 */
const BIN = path.join(ROOT, 'tests', '.tmp', `frznforge${process.platform === 'win32' ? '.exe' : ''}`);

/**
 * Token variables stripped from every child. The fixture must never authenticate as the
 * developer: if a seeding mistake did send a request to a real provider (see the gate at the
 * bottom), it should fail as an anonymous stranger rather than spend their rate limit.
 */
const TOKEN_VARS = ['GITHUB', 'GITLAB', 'GITEA', 'FORGEJO'].flatMap((p) => [`FRZNFORGE_${p}_TOKEN`, `${p}_TOKEN`]);

const gitEnv = (date: string) => ({
  ...process.env,
  GIT_AUTHOR_NAME: 'Fixture Author',
  GIT_AUTHOR_EMAIL: 'fixture@example.com',
  GIT_COMMITTER_NAME: 'Fixture Author',
  GIT_COMMITTER_EMAIL: 'fixture@example.com',
  GIT_AUTHOR_DATE: date,
  GIT_COMMITTER_DATE: date,
  GIT_CONFIG_GLOBAL: path.join(TMP, 'gitconfig-empty'),
  GIT_CONFIG_NOSYSTEM: '1',
});

function git(cwd: string, args: string[], date = '2024-01-01T00:00:00Z') {
  return execFileSync('git', args, { cwd, env: gitEnv(date), stdio: 'pipe' }).toString();
}

function commitAll(cwd: string, message: string, date: string) {
  git(cwd, ['add', '-A'], date);
  git(cwd, ['commit', '-q', '-m', message], date);
}

function makeRepoIn(base: string, name: string, init: (dir: string) => void) {
  const dir = path.join(base, name);
  fs.mkdirSync(dir, { recursive: true });
  git(dir, ['init', '-q', '-b', 'main']);
  init(dir);
  return dir;
}

function makeRepo(name: string, init: (dir: string) => void) {
  return makeRepoIn(REPOS, name, init);
}

/* ---- running the binary --------------------------------------------------- */

/**
 * Run `frznforge` from the repository root.
 *
 * cwd is ROOT and never SITE: on Windows a process's working directory is locked against
 * deletion, and the next run's wipe removes SITE. `--root` is what points the binary at the
 * fixture — the same split tests/e2e/wizard.spec.ts uses for the same reason.
 */
function frznforge(args: string[], extraEnv: Record<string, string> = {}): string {
  const env: NodeJS.ProcessEnv = {
    ...process.env,
    // The fixture config already says both of these. They are passed anyway so that a child
    // which somehow lost --root still writes into tests/.tmp/e2e instead of the developer's
    // real data/ and .frznforge-cache/ — a hermeticity failure that leaves no trace in the
    // suite's own output, only in their working tree.
    FRZNFORGE_OUT_DIR: DATA,
    FRZNFORGE_CACHE_DIR: CACHE,
  };
  // A developer with FRZNFORGE_BASE exported would otherwise get it applied to BOTH builds.
  delete env.FRZNFORGE_BASE;
  for (const name of TOKEN_VARS) delete env[name];

  try {
    return execFileSync(BIN, args, { cwd: ROOT, stdio: 'pipe', env: { ...env, ...extraEnv } }).toString();
  } catch (err) {
    const e = err as { stdout?: Buffer; stderr?: Buffer; message?: string };
    throw new Error(
      `frznforge ${args.join(' ')} failed\n${e.stdout?.toString() ?? ''}${e.stderr?.toString() ?? ''}${e.message ?? ''}`,
    );
  }
}

/* ---- offline provider repos -----------------------------------------------
 * charlie and delta are ingested through the REAL remote code path — `PrepareRemote` → the
 * provider response cache → the bare mirror in the ingest cache → `ScanRepo` on that mirror —
 * with NO seam of any kind opened in the binary. The TypeScript harness swapped two function
 * arguments (`createImporter` and `ensureMirror`) to stay offline; a compiled binary has no such
 * handle, and adding a `FRZNFORGE_*_FIXTURE` env var would have made the ingest's only
 * test-shaped branch the one thing standing between the suite and the network.
 *
 * Instead the setup pre-seeds exactly what a successful previous run would have left on disk —
 * a mirror clone, and the `<mirror>.meta.json` provider response cache beside it — and runs
 * `frznforge ingest --backfill-metadata`. Backfill means "only fetch repos that have no cached
 * metadata, and never touch git": internal/ingest/remote.go returns from the replay branch
 * before the importer is constructed and before EnsureMirror is called, so this run makes zero
 * HTTP requests and zero network git invocations through unmodified production code. It also
 * emits NO warning, which is what keeps the artifact byte-identical to the one the TypeScript
 * harness produced — `ingest.fetch: "never"` would have been offline too, but at the price of
 * two `remote-cache-stale` warnings that render into every page's footer and would make the
 * fixture's permanent baseline "a degraded offline build".
 *
 * Two things follow from leaning on that branch, and both are defended rather than assumed:
 *   • Backfill's offline-ness is incidental to its documented purpose (API quota), so
 *     TestBackfillReplayTouchesNothing in internal/ingest pins it in the language that owns it.
 *     If that branch changes shape, a Go test goes red before a spec does.
 *   • A mis-seeded cache degrades to the LIVE network silently: `backfillSatisfied` is false,
 *     the importer is built and api.github.com is called for real, and the only symptom is a
 *     metadata-less charlie. Hence assertFixtureArtifact() at the end of this file.
 *
 * The one thing this stops exercising is EnsureMirror's clone-from-a-URL. That is covered in Go
 * against a local origin — internal/ingest/remote_test.go, TestEnsureMirrorClonesThenFetches and
 * its neighbours — not dropped.
 * ------------------------------------------------------------------------ */

/** One configured remote source, in the shape internal/config reads it. */
interface RemoteSource {
  type: string;
  host: string;
  owner: string;
  repo: string;
}

/**
 * The mirror directory a remote source resolves to, re-spelled from `config.MirrorDirName`
 * (internal/config/config.go): a readable `<type>-<host minus scheme>-<owner>-<repo>`,
 * lower-cased with everything outside [a-z0-9._-] mapped to '-' and capped at 48 characters,
 * then a dash and the first eight hex digits of the sha256 of the exact identity.
 *
 * The digest is not decoration. The readable half is lossy, so two different sources can
 * sanitise to one name and then share a mirror — each published with the other's git content.
 * The Go port dropped the digest for a while and this file was written against that version,
 * which is why the shape is spelled out here rather than merely referenced.
 *
 * A two-place invariant, which the project's house rules dislike — but the alternative is
 * parsing JSONC in Node to recover the sources, and this cache layout is user-documented
 * anyway (docs/user/importing.md). The gate at the bottom is what makes the duplication safe:
 * get this name wrong and no cache is found, the replay branch does not fire, and the
 * artifact fails the post-condition rather than quietly reaching for the network.
 */
function mirrorDirName(source: RemoteSource): string {
  const safe = (v: string) => v.toLowerCase().replace(/[^a-z0-9._-]/g, '-');
  const host = safe(source.host.replace(/^https?:\/\//, ''));
  let readable = `${source.type}-${host}-${safe(source.owner)}-${safe(source.repo)}`;
  if (readable.length > 48) readable = readable.slice(0, 48);
  // NUL-separated so ("a", "b-c") and ("a-b", "c") hash differently, exactly as the Go side does.
  const identity = [source.type, source.host, source.owner, source.repo].join('\0');
  const digest = createHash('sha256').update(identity).digest('hex').slice(0, 8);
  return `${readable}-${digest}`;
}

/** The provider response cache file, from `ProviderCachePathFor` (internal/ingest/remote.go). */
function providerCachePath(mirrorPath: string): string {
  return `${mirrorPath.replace(/\.git$/i, '')}.meta.json`;
}

/** Provider metadata, matching `ImportedRepoMeta`'s JSON tags key for key. */
interface RepoMeta {
  name: string;
  description: string | null;
  homepage: string | null;
  topics: string[];
  license: string | null;
  defaultBranch: string;
  webUrl: string;
  cloneUrl: string;
  issuesUrl: string | null;
  template: boolean;
  archived: boolean;
}

/** A release, matching `model.Release`. `assets` must be `[]` and never null, or it is dropped. */
interface Release {
  tag: string;
  name: string;
  body: string;
  url: string;
  prerelease: boolean;
  /** `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$` — anything else is silently discarded on read. */
  publishedAt: string;
  author: string;
  assets: Array<{ name: string; url: string; size: number; contentType: string }>;
}

/**
 * Write one remote repo's cache: a bare mirror cloned from its local origin, and the provider
 * answers beside it. `version: 1` is `providerCacheVersion`; any other value reads as no cache.
 */
function seedRemote(source: RemoteSource, origin: string, meta: RepoMeta, releases: Release[]): void {
  const mirrors = path.join(CACHE, 'mirrors');
  fs.mkdirSync(mirrors, { recursive: true });
  const mirrorPath = path.join(mirrors, mirrorDirName(source));
  // Byte for byte what ensureMirrorLocked runs. Forward slashes: git accepts them on Windows and
  // they keep the arg quoting simple.
  git(mirrors, ['clone', '--mirror', '--quiet', '--', origin.replace(/\\/g, '/'), mirrorPath]);
  fs.writeFileSync(providerCachePath(mirrorPath), `${JSON.stringify({ version: 1, meta, releases }, null, 2)}\n`);
}


/* ---- the two fixture servers ---------------------------------------------- */

const PORT = 4399;
/** The base-path build (0.2.0): the same fixture artifact served under /mysite. */
const BASE_PORT = 4398;

/**
 * Start `frznforge dev` over one built directory.
 *
 * This is the same server a person gets from `frznforge dev` — internal/serve is the single
 * implementation that replaced both scripts/dev.ts and the 54-line tests/e2e/serve.ts. A dev
 * server that is not the server the specs assert against can be wrong exactly where nobody
 * looks, which is how the two once disagreed about whether a `.ps1` file was text or a download.
 *
 * `--dir` says "serve these files", which also suppresses the missing-artifact preflight: there
 * is no project artifact behind a fixture directory to have an opinion about. `--base` is passed
 * on BOTH servers, empty on the first: an absent flag means "inherit site.base from the config
 * --root finds", and only an explicit `--base=` says "serve at the root". `--root` points at the
 * fixture site so no server reads the developer's own config for anything.
 *
 * Spawned directly rather than through a shell: the argument vector goes to the binary as-is,
 * so a path containing a space needs no quoting and cmd.exe never sees it.
 */
function startServer(dir: string, port: number, base: string): ChildProcess {
  const args = ['dev', `--dir=${dir}`, `--root=${SITE}`, `--port=${port}`, `--base=${base}`, '--quiet'];
  const child = spawn(BIN, args, { stdio: ['ignore', 'pipe', 'pipe'] });
  child.stderr?.on('data', (b: Buffer) => process.stderr.write(`[dev :${port}] ${b}`));
  child.on('exit', (code) => {
    if (code !== 0 && code !== null) process.stderr.write(`[dev :${port}] exited ${code}\n`);
  });
  return child;
}

/** Poll until the server answers, so no spec races the listener. */
async function waitForPort(port: number, base: string, child: ChildProcess): Promise<void> {
  const url = `http://localhost:${port}${base}/`;
  const deadline = Date.now() + 30_000;
  for (;;) {
    if (child.exitCode !== null) throw new Error(`frznforge dev on :${port} exited ${child.exitCode} before answering`);
    try {
      const res = await fetch(url);
      if (res.ok) return;
    } catch {
      // not listening yet
    }
    if (Date.now() > deadline) throw new Error(`frznforge dev did not answer ${url} within 30s`);
    await new Promise((r) => setTimeout(r, 100));
  }
}

export default async function globalSetup() {
  fs.rmSync(TMP, { recursive: true, force: true });
  fs.mkdirSync(TMP, { recursive: true });
  fs.writeFileSync(path.join(TMP, 'gitconfig-empty'), '');

  // Built rather than `go run`: `go run` recompiles on every invocation (five of them here plus
  // two dev servers), and it leaves a parent process between Playwright's teardown and the
  // server holding the port. Same call, same reasoning, as tests/e2e/wizard.spec.ts.
  fs.mkdirSync(path.dirname(BIN), { recursive: true });
  execFileSync('go', ['build', '-o', BIN, './cmd/frznforge'], { cwd: ROOT, stdio: 'pipe' });

  // alpha — normal repo with README, .frznforge.json, tags, two languages, recent-ish date
  const alpha = makeRepo('alpha', (d) => {
    // Two mermaid fences (0.2.0): local repos are trusted, so these render as SVGs — two on
    // one page so the duplicate-id a11y probe and per-container id seeding are exercised.
    fs.writeFileSync(
      path.join(d, 'README.md'),
      '# Alpha\n\nA **fixture** repo for e2e tests.\n\n- bullet one\n- bullet two\n\n' +
        '```mermaid\ngraph TD;\n  A[Ingest]-->B[Artifact];\n  B-->C[Site];\n```\n\n' +
        'And a second diagram:\n\n```mermaid\nsequenceDiagram;\n  Reader->>Site: GET /repos/;\n  Site-->>Reader: static HTML;\n```\n',
    );
    fs.writeFileSync(path.join(d, '.frznforge.json'), JSON.stringify({ description: 'Alpha fixture: a static site generator.', tags: ['ssg', 'astro'], links: { homepage: 'https://example.com/alpha', upstream: 'https://github.com/example/alpha' } }, null, 2));
    fs.writeFileSync(path.join(d, 'LICENSE'), 'MIT License\n\nCopyright (c) 2024 Fixture\n\nPermission is hereby granted, free of charge, to any person obtaining a copy...');
    fs.mkdirSync(path.join(d, 'src'));
    fs.mkdirSync(path.join(d, 'src', 'lib'));
    fs.writeFileSync(path.join(d, 'src', 'lib', 'index.ts'), 'export {};\n');
    fs.writeFileSync(path.join(d, 'src', 'index.ts'), 'export const answer: number = 42;\n'.repeat(20));
    fs.writeFileSync(path.join(d, 'src', 'style.css'), 'body { margin: 0; }\n'.repeat(5));
    fs.mkdirSync(path.join(d, 'docs'));
    fs.writeFileSync(path.join(d, 'docs', 'guide.md'), '# Guide\n\nSome **bold** fixture text.\n\n- step one\n- step two\n');
    fs.mkdirSync(path.join(d, 'assets'));
    // a few PNG header bytes (incl. NULs) so ingest classifies it as a binary image
    fs.writeFileSync(path.join(d, 'assets', 'dot.png'), Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01]));
    // URL-hostile committed names, in this same commit so the commit-day count is unchanged.
    // A space must survive percent-encoding (it used to emit an invalid href); '#' and '%'
    // cannot be served statically at all, so they must be listed-but-unlinked rather than
    // aborting the build. See internal/routes `IsRawServable`.
    fs.writeFileSync(path.join(d, 'docs', 'read me.md'), '# Read me\n\nA name with a space.\n');
    fs.writeFileSync(path.join(d, 'docs', '50% off.txt'), 'percent in the name\n');
    fs.writeFileSync(path.join(d, 'docs', 'c#-tips.md'), '# C# tips\n\nHash in the name.\n');
    commitAll(d, 'initial commit', '2024-01-01T00:00:00Z');
    // feature branch with an extra file (branched before the last main commit)
    git(d, ['checkout', '-q', '-b', 'feature/extra'], '2024-01-15T00:00:00Z');
    fs.writeFileSync(path.join(d, 'src', 'extra.ts'), 'export const extra = true;\n');
    commitAll(d, 'add extra feature file', '2024-01-15T00:00:00Z');
    // one RECENT commit (feature branch only) so relative-to-today features
    // (contribution graph, heat colours) have something inside their window
    const recent = new Date(Date.now() - 2 * 86_400_000).toISOString().replace(/\.\d{3}Z$/, 'Z');
    fs.writeFileSync(path.join(d, 'src', 'extra.ts'), 'export const extra = true;\nexport const more = 1;\n');
    git(d, ['add', '-A'], recent);
    // authored by the owner identity from content/profile.md so the contribution graph has data
    git(d, ['commit', '-q', '-m', 'tweak the extra feature', '--author=Kieran Wood <kieran@canadiancoding.ca>'], recent);
    git(d, ['checkout', '-q', 'main'], '2024-01-15T00:00:00Z');
    // 'bump the answer' stays the LAST commit on main (existing assertions rely on it)
    fs.writeFileSync(path.join(d, 'src', 'index.ts'), 'export const answer: number = 43;\n'.repeat(20));
    commitAll(d, 'bump the answer', '2024-02-01T00:00:00Z');
    git(d, ['tag', '-a', 'v1.0.0', '-m', 'First release'], '2024-02-01T00:00:00Z');
    git(d, ['tag', '-a', '--cleanup=verbatim', 'v1.1.0', '-m', 'Second release\n\n## Highlights\n\n- adds a *guide*\n- new `extra` module\n'], '2024-03-01T00:00:00Z');
    git(d, ['tag', 'light'], '2024-03-02T00:00:00Z');
    // gh-pages: a tiny BUILT site, served at /alpha-site/ by the hosting config (0.2.0, schema
    // v7). Its own links are relative on purpose — hosted content is user content, and the
    // base-path build's leak scan must not trip over it.
    git(d, ['checkout', '-q', '-b', 'gh-pages'], '2024-03-05T00:00:00Z');
    git(d, ['rm', '-r', '-q', 'src', 'docs', 'assets', 'README.md', '.frznforge.json', 'LICENSE'], '2024-03-05T00:00:00Z');
    fs.writeFileSync(path.join(d, 'index.html'), '<!doctype html><meta charset="utf-8"><title>alpha site</title><link rel="stylesheet" href="style.css"><h1>built by alpha</h1><script src="app.js"></script>');
    fs.writeFileSync(path.join(d, 'style.css'), 'h1 { color: rebeccapurple; }\n');
    fs.writeFileSync(path.join(d, 'app.js'), 'document.title += " ✓";\n');
    commitAll(d, 'publish the site', '2024-03-05T00:00:00Z');
    git(d, ['checkout', '-q', 'main'], '2024-03-05T00:00:00Z');
  });

  // bravo — template repo, Go, and the only fixture with a LONG history.
  //
  // Every other fixture repo spans one or two months, which is not enough to exercise the
  // Phase 7 insights page: no checkpoint thinning, no x-label thinning, no quiet month, one
  // contributor. So bravo grows a commit a month for over a year. The shape is deliberate:
  //   • 2023-06 → 2024-08, with 2023-11 and 2024-04 SKIPPED, so the zero-filled quiet months
  //     the series emits are actually rendered;
  //   • a second author in some months, so `contributors` is not a flat line of 1;
  //   • the file grows monotonically, so code size has a visible slope;
  //   • Go only, and no tags — the language facet, the `cli` tag and the empty-releases state
  //     that other specs assert on bravo all stay exactly as they were.
  // `scaffold` remains the FIRST commit at its original date; nothing asserts bravo's HEAD.
  makeRepo('bravo', (d) => {
    fs.writeFileSync(path.join(d, 'README.md'), '# Bravo template\n\nClone me.\n');
    fs.writeFileSync(path.join(d, '.frznforge.json'), JSON.stringify({ description: 'Bravo fixture: a Go CLI template.', tags: ['cli', 'go'], template: true }, null, 2));
    fs.writeFileSync(path.join(d, 'main.go'), 'package main\n\nfunc main() {}\n'.repeat(10));
    commitAll(d, 'scaffold', '2023-06-01T00:00:00Z');

    // [year, month, commits that month]; the gaps at 2023-11 and 2024-04 are the point.
    const months: Array<[number, number, number]> = [
      [2023, 7, 1], [2023, 8, 3], [2023, 9, 2], [2023, 10, 4],
      [2024, 1, 2], [2024, 2, 5], [2024, 3, 1],
      [2024, 5, 3], [2024, 6, 2], [2024, 7, 4], [2024, 8, 1],
    ];
    let n = 0;
    for (const [year, month, count] of months) {
      for (let k = 0; k < count; k++) {
        n++;
        const date = `${year}-${String(month).padStart(2, '0')}-${String(2 + k * 4).padStart(2, '0')}T09:0${k}:00Z`;
        fs.appendFileSync(path.join(d, 'main.go'), `\nfunc step${n}() int { return ${n} }\n`);
        git(d, ['add', '-A'], date);
        // a co-maintainer in the busier months, so the contributor series varies
        const author = k === 1 ? ['--author=Bo Maintainer <bo@example.com>'] : [];
        git(d, ['commit', '-q', '-m', `step ${n}`, ...author], date);
      }
    }
  });

  // empty — no commits at all
  makeRepo('empty', () => {});

  // charlie — a GitHub-hosted repo whose releases come from the provider API
  const charlieOrigin = makeRepoIn(ORIGINS, 'charlie', (d) => {
    // An imported README is written by whoever can push to the upstream repo, so it gets
    // the same payload block as the release body below: the repo overview, tree pages and
    // the blob markdown preview all render it UNTRUSTED — mermaid.spec.ts asserts the raw
    // HTML stays inert while the mermaid fence renders (importing = choosing to publish).
    fs.writeFileSync(
      path.join(d, 'README.md'),
      '# Charlie\n\nMirrored from a provider.\n\n' +
        '<script>window.__PWNED = 11</script>\n\n' +
        '<img src=x onerror="window.__PWNED = 12">\n\n' +
        '```mermaid\ngraph TD;\n  A-->B;\n```\n',
    );
    fs.mkdirSync(path.join(d, 'src'));
    fs.writeFileSync(path.join(d, 'src', 'main.ts'), 'export const charlie = true;\n'.repeat(12));
    commitAll(d, 'import charlie', '2024-03-10T00:00:00Z');
    git(d, ['tag', '-a', 'v2.1.0', '-m', 'Sunrise'], '2024-04-01T12:00:00Z');
    fs.writeFileSync(path.join(d, 'src', 'main.ts'), 'export const charlie = true;\nexport const rc = 1;\n');
    commitAll(d, 'prepare the release candidate', '2024-05-01T12:00:00Z');
    git(d, ['tag', '-a', 'v2.2.0-rc.1', '-m', 'Release candidate'], '2024-05-01T12:00:00Z');
  });
  seedRemote(
    // Must match the `github` entry in fixture.config.jsonc, including the defaulted host:
    // internal/config applies `https://api.github.com` when none is given, and that string is
    // half of the mirror directory name.
    { type: 'github', host: 'https://api.github.com', owner: 'fixture', repo: 'charlie' },
    charlieOrigin,
    {
      name: 'charlie',
      description: 'Charlie fixture: metadata imported from a provider API.',
      homepage: 'https://example.com/charlie',
      topics: ['imported', 'provider'],
      license: 'Apache-2.0',
      defaultBranch: 'main',
      webUrl: 'https://github.com/fixture/charlie',
      cloneUrl: 'https://github.com/fixture/charlie.git',
      issuesUrl: 'https://github.com/fixture/charlie/issues',
      template: false,
      archived: false,
    },
    [
      {
        tag: 'v2.1.0',
        // a title distinct from the tag: exercises the name + tag-chip branch
        name: 'Sunrise',
        body: "## What's new\n\n- imported over the provider API\n- ships two uploaded assets\n\nSee `docs/` for the details.\n",
        url: 'https://github.com/fixture/charlie/releases/tag/v2.1.0',
        prerelease: false,
        publishedAt: '2024-04-01T12:00:00Z',
        author: 'Fixture Releaser',
        assets: [
          {
            name: 'charlie-2.1.0-linux-x64.tar.gz',
            url: 'https://github.com/fixture/charlie/releases/download/v2.1.0/charlie-2.1.0-linux-x64.tar.gz',
            size: 1_048_576,
            contentType: 'application/gzip',
          },
          {
            name: 'charlie-2.1.0.sha256',
            url: 'https://github.com/fixture/charlie/releases/download/v2.1.0/charlie-2.1.0.sha256',
            size: 96,
            contentType: 'text/plain',
          },
        ],
      },
      {
        tag: 'v2.2.0-rc.1',
        name: 'v2.2.0-rc.1',
        // Imported release notes are written by whoever can publish on the forge, so this
        // body carries the payload an XSS regression would put on the generated page.
        body:
          'Release candidate. **Not** for production use.\n\n' +
          '<script>window.__PWNED = 1</script>\n\n' +
          '<img src=x onerror="window.__PWNED = 2">\n\n' +
          '[boom](javascript:window.__PWNED=3)\n\n' +
          // A mermaid fence in IMPORTED content renders like any other (0.2.0, owner's
          // decision — importing a repo is choosing to publish it); the raw-HTML payloads
          // above must stay inert regardless.
          '```mermaid\ngraph TD;\n  A-->B;\n```\n',
        url: 'https://github.com/fixture/charlie/releases/tag/v2.2.0-rc.1',
        prerelease: true,
        publishedAt: '2024-05-01T12:00:00Z',
        author: 'Fixture Releaser',
        assets: [],
      },
    ],
  );

  // delta — a provider repo that has published nothing yet (and has no annotated tags, so
  // `resolveReleases` cannot fall back to git): the provider-flavoured empty state
  const deltaOrigin = makeRepoIn(ORIGINS, 'delta', (d) => {
    fs.writeFileSync(path.join(d, 'README.md'), '# Delta\n\nNothing released yet.\n');
    fs.writeFileSync(path.join(d, 'app.ts'), 'export const delta = 0;\n'.repeat(8));
    commitAll(d, 'first push', '2024-03-20T00:00:00Z');
  });
  seedRemote(
    { type: 'gitea', host: 'https://gitea.example.com', owner: 'fixture', repo: 'delta' },
    deltaOrigin,
    {
      name: 'delta',
      description: 'Delta fixture: a Gitea repo with no releases.',
      homepage: null,
      topics: ['imported'],
      license: null,
      defaultBranch: 'main',
      webUrl: 'https://gitea.example.com/fixture/delta',
      cloneUrl: 'https://gitea.example.com/fixture/delta.git',
      issuesUrl: null,
      template: false,
      archived: false,
    },
    // Empty on purpose, and the cache must still SAY so: an absent releases list would leave
    // the replay branch with nothing to hand back and releases.spec.ts's "No releases published
    // on Gitea" empty state would be testing a fetch failure instead.
    [],
  );

  // uncommitted noise in alpha: must NOT show up anywhere. The scanners read git, never the
  // working tree, and this is the end-to-end proof of it.
  fs.writeFileSync(path.join(alpha, 'UNTRACKED-SECRET.txt'), 'should never be published');
  fs.writeFileSync(path.join(alpha, 'README.md'), '# MODIFIED BUT NOT COMMITTED\n');

  writeSiteRoot();

  // The ingest. --backfill-metadata is what reaches the offline replay branch; see the long
  // comment above for why that flag and not `ingest.fetch: "never"`. Its console summary reads
  // oddly on purpose — "0 filled, 0 still missing, 2 already had metadata (no network)" is a
  // correct description of a run that was never meant to fill anything.
  frznforge(['ingest', '--backfill-metadata', `--root=${SITE}`]);
  assertFixtureArtifact();

  // --no-ingest is load-bearing: `frznforge build` scans before it renders, so without the flag
  // this would re-ingest over the artifact the line above just produced. (Not hypothetical — it
  // happened during 0.4.0, against the developer's real config, and every spec stayed green
  // while asserting on the wrong corpus.) It also cannot be combined with --backfill-metadata,
  // which is why the ingest and the builds are separate commands.
  frznforge(['build', '--no-ingest', `--root=${SITE}`, `--out=${DIST}`]);

  // The same artifact again, deployed under a sub-path: FRZNFORGE_BASE flows through
  // config.Resolve → site.base → every URL the renderer emits. `base-path.spec.ts` drives this
  // dist (served with the matching prefix on port 4398) and asserts no root-absolute URL leaked.
  frznforge(['build', '--no-ingest', `--root=${SITE}`, `--out=${DIST_BASE}`], { FRZNFORGE_BASE: '/mysite' });

  // Only now, with both directories on disk, do the servers exist. Returning the teardown is
  // what makes globalSetup responsible for them: Playwright awaits it after the last test, so
  // neither process outlives the run even when the run fails.
  const root = startServer(DIST, PORT, '');
  const base = startServer(DIST_BASE, BASE_PORT, '/mysite');
  try {
    await Promise.all([waitForPort(PORT, '', root), waitForPort(BASE_PORT, '/mysite', base)]);
  } catch (err) {
    root.kill();
    base.kill();
    throw err;
  }
  return () => {
    root.kill();
    base.kill();
  };
}

/**
 * Assemble the fixture project root: the config, and the two asset trees a build copies verbatim.
 *
 * public/ and web/ are COPIED, not linked. `copyAssets` walks <root>/web with filepath.WalkDir,
 * which lstats its own root — a Windows junction or a symlink is yielded as a non-directory
 * entry with no descent, and the build then tries to read a directory as a file. web/ is ~3.8 MB
 * (mostly web/vendor/mermaid); one copy per suite run is cheaper than the class of bug that
 * "dist is a verbatim copy of on-disk bytes" exists to prevent.
 *
 * content/ is NOT copied: owner.profile, content.orgs and notes.dir in the fixture config are
 * absolute paths into the real repository, because the checked-in profile and
 * content/orgs/canadian-coding.md are what several specs assert against.
 */
function writeSiteRoot(): void {
  fs.mkdirSync(SITE, { recursive: true });
  const template = fs.readFileSync(path.join(ROOT, 'tests', 'e2e', 'fixture.config.jsonc'), 'utf8');
  // Forward slashes throughout: they are absolute on Windows as far as filepath.IsAbs is
  // concerned, and they need no JSON escaping, so the template stays readable.
  const slash = (p: string) => p.replace(/\\/g, '/');
  const config = template
    .replaceAll('__ROOT__', slash(ROOT))
    .replaceAll('__REPOS__', slash(REPOS))
    .replaceAll('__TMP__', slash(TMP));
  fs.writeFileSync(path.join(SITE, 'frznforge.config.jsonc'), config);

  fs.cpSync(path.join(ROOT, 'public'), path.join(SITE, 'public'), { recursive: true });
  fs.cpSync(path.join(ROOT, 'web'), path.join(SITE, 'web'), { recursive: true });
}

/**
 * Fail loudly if the fixture artifact is not the one the specs were written against.
 *
 * The seeded caches are the suite's only tie to the offline replay path, and a broken seed does
 * not fail the ingest — it falls through to the live provider API, comes back empty, and the
 * first symptom is a selector error in a spec three files away. Everything asserted here is a
 * property the replay produced and a live-but-offline fetch could not.
 */
function assertFixtureArtifact(): void {
  interface Artifact {
    warnings: Array<{ code: string; repo: string | null; message: string }>;
    repos: Array<{
      slug: string;
      license: { spdx: string | null } | null;
      releases: unknown[];
      source: { type: string } | null;
    }>;
  }
  const file = path.join(DATA, 'forge.json');
  const data = JSON.parse(fs.readFileSync(file, 'utf8')) as Artifact;
  const bad: string[] = [];

  // Any remote-* warning means the network was consulted, or the cache was not: either way the
  // artifact is a degraded one, and it would render into every page's footer tooltip.
  for (const w of data.warnings) {
    if (w.code.startsWith('remote-')) bad.push(`unexpected ${w.code} (${w.repo ?? 'site'}): ${w.message}`);
  }

  const repo = (slug: string) => data.repos.find((r) => r.slug === slug);
  const charlie = repo('charlie');
  if (!charlie) bad.push('charlie is missing from the artifact');
  else {
    // Both come from the seeded provider response and from nowhere else: the mirror has no
    // LICENSE file to sniff, and no annotated-tag fallback runs in "provider" release mode.
    if (charlie.license?.spdx !== 'Apache-2.0') bad.push(`charlie.license.spdx = ${JSON.stringify(charlie.license)}`);
    if (charlie.releases.length !== 2) bad.push(`charlie has ${charlie.releases.length} releases, want 2`);
  }
  const delta = repo('delta');
  if (!delta) bad.push('delta is missing from the artifact');
  else {
    if (delta.source?.type !== 'gitea') bad.push(`delta.source.type = ${JSON.stringify(delta.source)}`);
    // Empty, but present and imported — the provider-flavoured empty state, not a fetch failure.
    if (delta.releases.length !== 0) bad.push(`delta has ${delta.releases.length} releases, want 0`);
  }

  if (bad.length > 0) {
    throw new Error(
      `the fixture artifact is not what the specs assert against (${file}):\n  - ${bad.join('\n  - ')}\n\n` +
        'The provider caches under tests/.tmp/e2e/cache/mirrors are most likely mis-seeded — check\n' +
        'mirrorDirName() in this file against config.MirrorDirName in internal/config/config.go.',
    );
  }
}
