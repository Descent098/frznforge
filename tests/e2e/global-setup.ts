/**
 * Playwright global setup: build the site from a deterministic FIXTURE artifact.
 *  1. create the fixture git repos under tests/.tmp/e2e (local ones in repos/, the stand-in
 *     "provider" ones in origins/)
 *  2. run the real ingest pipeline on them → tests/.tmp/e2e/data
 *  3. `astro build` with FRZNFORGE_OUT_DIR pointing at that data → tests/.tmp/e2e/dist
 * The webServer in playwright.config.ts then serves that dist.
 *
 * Nothing here touches the network — see `remoteDeps` for how the provider repos are faked.
 */
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { resolveConfig, type RepoSourceConfig } from '../../src/lib/config/index';
import userConfig from '../../frznforge.config';
import { ensureMirror, ingest, writeArtifact, type PrepareRemoteDeps } from '../../src/lib/ingest';
import type { ImportedRepoMeta, Importer } from '../../src/lib/importers/index';
import type { Release } from '../../src/lib/data/schema';

const ROOT = path.resolve(import.meta.dirname, '..', '..');
const TMP = path.join(ROOT, 'tests', '.tmp', 'e2e');
const REPOS = path.join(TMP, 'repos');
/** Git repos that stand in for the provider's git server (never scanned directly). */
const ORIGINS = path.join(TMP, 'origins');
const CACHE = path.join(TMP, 'cache');
const DATA = path.join(TMP, 'data');
const DIST = path.join(TMP, 'dist');

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

/* ---- provider stand-ins ---------------------------------------------------
 * charlie and delta are ingested through the REAL remote code path —
 * `prepareRemote` → `git clone --mirror` into the ingest cache → `scanRepo` on the bare
 * mirror — with exactly two seams swapped so the build stays offline:
 *   • `createImporter` returns a stub that hands back canned metadata + releases instead of
 *     calling a REST API;
 *   • `ensureMirror` is the production function, called with the local `origins/<name>`
 *     directory as the clone URL instead of the provider's https URL.
 * Everything downstream (cache layout, mirror scan, metadata precedence, releaseMode,
 * warnings) is production code, so the artifact these produce is shaped exactly like a real
 * provider import.
 * ------------------------------------------------------------------------ */

/** Stable key for a configured remote source (fixture repo names are unique). */
function remoteKey(source: RepoSourceConfig): string {
  if (source.type === 'local') return '';
  return source.type === 'gitlab' ? source.project : `${source.owner}/${source.repo}`;
}

interface RemoteFixture {
  /** Local git repo the mirror is cloned from. */
  origin: string;
  meta: ImportedRepoMeta;
  releases: Release[];
}

const remoteFixtures = new Map<string, RemoteFixture>();

const remoteDeps: PrepareRemoteDeps = {
  // No token is ever resolved: the env the token comes from is empty.
  env: {},
  createImporter: (source) => {
    const fixture = remoteFixtures.get(remoteKey(source));
    if (!fixture) return null;
    const importer: Importer = {
      provider: source.type as Importer['provider'],
      fetchMeta: async () => fixture.meta,
      fetchReleases: async () => ({ releases: fixture.releases, truncated: false }),
    };
    return importer;
  },
  ensureMirror: (source, cachePath, opts) => {
    const fixture = remoteFixtures.get(remoteKey(source));
    if (!fixture) throw new Error(`no remote fixture registered for ${remoteKey(source)}`);
    // forward slashes: git accepts them on Windows and they keep the arg quoting simple
    return ensureMirror(source, cachePath, { ...opts, cloneUrl: fixture.origin.replace(/\\/g, '/') });
  },
};

export default async function globalSetup() {
  fs.rmSync(TMP, { recursive: true, force: true });
  fs.mkdirSync(TMP, { recursive: true });
  fs.writeFileSync(path.join(TMP, 'gitconfig-empty'), '');

  // alpha — normal repo with README, .frznforge.json, tags, two languages, recent-ish date
  const alpha = makeRepo('alpha', (d) => {
    fs.writeFileSync(path.join(d, 'README.md'), '# Alpha\n\nA **fixture** repo for e2e tests.\n\n- bullet one\n- bullet two\n');
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
    // aborting the build. See src/lib/routes.ts `isRawServable`.
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
    fs.writeFileSync(path.join(d, 'README.md'), '# Charlie\n\nMirrored from a provider.\n');
    fs.mkdirSync(path.join(d, 'src'));
    fs.writeFileSync(path.join(d, 'src', 'main.ts'), 'export const charlie = true;\n'.repeat(12));
    commitAll(d, 'import charlie', '2024-03-10T00:00:00Z');
    git(d, ['tag', '-a', 'v2.1.0', '-m', 'Sunrise'], '2024-04-01T12:00:00Z');
    fs.writeFileSync(path.join(d, 'src', 'main.ts'), 'export const charlie = true;\nexport const rc = 1;\n');
    commitAll(d, 'prepare the release candidate', '2024-05-01T12:00:00Z');
    git(d, ['tag', '-a', 'v2.2.0-rc.1', '-m', 'Release candidate'], '2024-05-01T12:00:00Z');
  });
  remoteFixtures.set('fixture/charlie', {
    origin: charlieOrigin,
    meta: {
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
    releases: [
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
          '[boom](javascript:window.__PWNED=3)\n',
        url: 'https://github.com/fixture/charlie/releases/tag/v2.2.0-rc.1',
        prerelease: true,
        publishedAt: '2024-05-01T12:00:00Z',
        author: 'Fixture Releaser',
        assets: [],
      },
    ],
  });

  // delta — a provider repo that has published nothing yet (and has no annotated tags, so
  // `resolveReleases` cannot fall back to git): the provider-flavoured empty state
  const deltaOrigin = makeRepoIn(ORIGINS, 'delta', (d) => {
    fs.writeFileSync(path.join(d, 'README.md'), '# Delta\n\nNothing released yet.\n');
    fs.writeFileSync(path.join(d, 'app.ts'), 'export const delta = 0;\n'.repeat(8));
    commitAll(d, 'first push', '2024-03-20T00:00:00Z');
  });
  remoteFixtures.set('fixture/delta', {
    origin: deltaOrigin,
    meta: {
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
    releases: [],
  });

  // uncommitted noise in alpha: must NOT show up anywhere
  fs.writeFileSync(path.join(alpha, 'UNTRACKED-SECRET.txt'), 'should never be published');
  fs.writeFileSync(path.join(alpha, 'README.md'), '# MODIFIED BUT NOT COMMITTED\n');

  // resolveConfig honours these env vars; the fixture picks its own directories
  delete process.env.FRZNFORGE_OUT_DIR;
  delete process.env.FRZNFORGE_CACHE_DIR;

  const cfg = resolveConfig(
    {
      ...userConfig,
      repos: [
        // `org` here exercises the repo → organization direction of membership; `bravo` is
        // claimed from the other side, by the organization's own `repos` list below.
        { type: 'local', path: path.join(REPOS, 'alpha'), org: 'canadian-coding' },
        { type: 'local', path: path.join(REPOS, 'bravo') },
        { type: 'local', path: path.join(REPOS, 'empty') },
        { type: 'github', owner: 'fixture', repo: 'charlie' },
        { type: 'gitea', host: 'https://gitea.example.com', owner: 'fixture', repo: 'delta' },
      ],
      // The shipped config's organization points at `frznforge`, which this fixture does not
      // build, so its members are re-pointed at fixture repos. The SLUG is kept so the org
      // still picks up the real `content/orgs/canadian-coding.md` (body, sites, links) — the
      // markdown half of an organization is repo content, not fixture data.
      organizations: [
        {
          slug: 'canadian-coding',
          name: 'Canadian Coding',
          description: "Small, sturdy, source-available tools that keep working when the server doesn't.",
          repos: ['bravo'],
        },
      ],
      // `insights.samples` is deliberately below bravo's active-month count so the build
      // exercises checkpoint THINNING (`sampled: true`) rather than measuring every month —
      // the path a real repo with years of history takes. Every other knob stays at its
      // shipped default so the fixture keeps testing what users actually get.
      ingest: {
        ...(userConfig.ingest ?? {}),
        outDir: DATA,
        cacheDir: CACHE,
        insights: { ...(userConfig.ingest?.insights ?? {}), samples: 6 },
      },
    },
    ROOT,
  );
  const { data, blobs, archives } = await ingest({ ...cfg, outDir: DATA }, {}, { remote: remoteDeps });
  await writeArtifact(data, blobs, archives, DATA);

  execFileSync('npx', ['astro', 'build', '--outDir', DIST], {
    cwd: ROOT,
    stdio: 'pipe',
    shell: true,
    env: { ...process.env, FRZNFORGE_OUT_DIR: DATA },
  });
}
