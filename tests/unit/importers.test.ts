/**
 * Provider importer tests. Every request is served from the recorded fixtures in
 * `tests/fixtures/http` through an injected `fetchImpl` — nothing here touches the network.
 */
import { describe, expect, it } from 'vitest';
import { fixtureFetch, loadFixture, type FixtureRoute } from '../fixtures/http/index';
import { Release } from '../../src/lib/data/schema';
import {
  ForgejoImporter,
  GiteaImporter,
  GithubImporter,
  GitlabImporter,
  ImporterError,
  createImporter,
} from '../../src/lib/importers/index';
import { JsonClient, absoluteUrl, scrubIps, toIsoDate } from '../../src/lib/importers/http';
import type {
  ForgejoSourceConfig,
  GiteaSourceConfig,
  GithubSourceConfig,
  GitlabSourceConfig,
} from '../../src/lib/config/schema';

/* ---- helpers -------------------------------------------------------------- */

const ISO = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/;

const githubSource: GithubSourceConfig = {
  type: 'github',
  owner: 'Descent098',
  repo: 'ezcv',
  host: 'https://api.github.com',
};
const gitlabSource: GitlabSourceConfig = {
  type: 'gitlab',
  project: 'gitlab-org/gitlab-runner',
  host: 'https://gitlab.com',
};
const giteaSource: GiteaSourceConfig = {
  type: 'gitea',
  host: 'https://gitea.com',
  owner: 'gitea',
  repo: 'tea',
};
const forgejoSource: ForgejoSourceConfig = {
  type: 'forgejo',
  host: 'https://codeberg.org',
  owner: 'forgejo',
  repo: 'forgejo',
};

interface RecordedCall {
  url: string;
  headers: Record<string, string>;
}

/** Wrap a fetch stand-in so a test can assert what was actually sent (headers, URLs). */
function recording(inner: typeof fetch): { fetchImpl: typeof fetch; calls: RecordedCall[] } {
  const calls: RecordedCall[] = [];
  const impl = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url;
    const headers: Record<string, string> = {};
    for (const [key, value] of Object.entries((init?.headers ?? {}) as Record<string, string>)) {
      headers[key.toLowerCase()] = value;
    }
    calls.push({ url, headers });
    return await inner(input, init);
  };
  return { fetchImpl: impl as unknown as typeof fetch, calls };
}

/** Await a call that must fail and hand back the {@link ImporterError} it threw. */
async function rejection(promise: Promise<unknown>): Promise<ImporterError> {
  try {
    await promise;
  } catch (err) {
    return err as ImporterError;
  }
  throw new Error('expected the call to reject');
}

/** A fetch that always answers with the same canned response, whatever the URL. */
function alwaysRespond(body: string, init: ResponseInit = {}): typeof fetch {
  const impl = async (): Promise<Response> => new Response(body, init);
  return impl as unknown as typeof fetch;
}

/** A fetch that walks the given responses in order, one per call. */
function sequence(steps: Array<() => Promise<Response>>): { fetchImpl: typeof fetch; count: () => number } {
  let index = 0;
  const impl = async (): Promise<Response> => {
    const step = steps[Math.min(index, steps.length - 1)]!;
    index += 1;
    return await step();
  };
  return { fetchImpl: impl as unknown as typeof fetch, count: () => index };
}

const routes = (map: Record<string, FixtureRoute>): typeof fetch => fixtureFetch(map);

/* ---- GitHub --------------------------------------------------------------- */

describe('GithubImporter', () => {
  const meta = (extra: Record<string, FixtureRoute> = {}) =>
    routes({ ...extra, '/repos/Descent098/ezcv': { json: loadFixture('github/repo.json') } });

  it('maps repo metadata', async () => {
    const importer = new GithubImporter(githubSource, { fetchImpl: meta(), token: null });
    expect(await importer.fetchMeta()).toEqual({
      name: 'ezcv',
      description: 'A python-based static site generator for setting up a CV/Resume site',
      homepage: 'https://ezcv.readthedocs.io/en/latest/',
      topics: expect.arrayContaining(['cli', 'python', 'static-site-generator']),
      license: 'MIT',
      defaultBranch: 'master',
      webUrl: 'https://github.com/Descent098/ezcv',
      cloneUrl: 'https://github.com/Descent098/ezcv.git',
      issuesUrl: 'https://github.com/Descent098/ezcv/issues',
      template: false,
      archived: false,
    });
    expect(importer.provider).toBe('github');
  });

  it('maps releases newest-first with normalised dates', async () => {
    const importer = new GithubImporter(githubSource, {
      fetchImpl: routes({ '/releases': { json: loadFixture('github/releases.json') } }),
      token: null,
    });
    const { releases } = await importer.fetchReleases();

    expect(releases.map((r) => r.tag)).toEqual(['v0.3.5', 'v0.3.3', 'v0.2.0', 'V0.1.1']);
    for (const release of releases) {
      expect(release.publishedAt).toMatch(ISO);
      expect(() => Release.parse(release)).not.toThrow();
    }
    expect(releases[0]).toMatchObject({
      tag: 'v0.3.5',
      name: 'V0.3.5; November 17th 2023',
      url: 'https://github.com/Descent098/ezcv/releases/tag/v0.3.5',
      prerelease: false,
      publishedAt: '2023-11-17T21:01:40Z',
      author: 'Descent098',
      assets: [],
    });
  });

  it('maps release assets and sorts them by name', async () => {
    const importer = new GithubImporter(githubSource, {
      fetchImpl: routes({ '/releases': { json: loadFixture('github/releases-with-assets.json') } }),
      token: null,
    });
    const { releases: [latest] } = await importer.fetchReleases();

    expect(latest!.assets.map((a) => a.name)).toEqual([
      'gh_2.98.0_checksums.txt',
      'gh_2.98.0_linux_386.deb',
      'gh_2.98.0_linux_386.rpm',
    ]);
    expect(latest!.assets[0]).toEqual({
      name: 'gh_2.98.0_checksums.txt',
      url: 'https://github.com/cli/cli/releases/download/v2.98.0/gh_2.98.0_checksums.txt',
      size: 1950,
      contentType: 'text/plain; charset=utf-8',
    });
    // Volatile counters must never reach the artifact.
    expect(JSON.stringify(latest!.assets)).not.toContain('download_count');
  });

  it('skips draft releases', async () => {
    const recorded = loadFixture('github/releases.json') as unknown[];
    const withDraft = [
      {
        tag_name: 'v9.9.9-draft',
        name: 'unpublished',
        draft: true,
        prerelease: false,
        created_at: '2026-01-01T00:00:00Z',
        published_at: null,
        assets: [],
      },
      ...recorded,
    ];
    const importer = new GithubImporter(githubSource, {
      fetchImpl: routes({ '/releases': { json: withDraft } }),
      token: null,
    });

    const tags = (await importer.fetchReleases()).releases.map((r) => r.tag);
    expect(tags).not.toContain('v9.9.9-draft');
    expect(tags).toHaveLength(4);
  });

  it('follows Link-header pagination', async () => {
    const page1 = (loadFixture('github/releases.json') as unknown[]).slice(0, 2);
    const page2 = (loadFixture('github/releases.json') as unknown[]).slice(2);
    const { fetchImpl, calls } = recording(
      routes({
        'page=2': { json: page2 },
        '/releases': {
          json: page1,
          headers: {
            link: '<https://api.github.com/repos/Descent098/ezcv/releases?per_page=100&page=2>; rel="next"',
          },
        },
      }),
    );

    const { releases } = await new GithubImporter(githubSource, { fetchImpl, token: null }).fetchReleases();
    expect(releases).toHaveLength(4);
    expect(calls).toHaveLength(2);
    expect(calls[1]!.url).toContain('page=2');
  });

  it('sends a Bearer token, and no Authorization header when anonymous', async () => {
    const authed = recording(meta());
    await new GithubImporter(githubSource, { fetchImpl: authed.fetchImpl, token: 'gh-secret' }).fetchMeta();
    expect(authed.calls[0]!.headers['authorization']).toBe('Bearer gh-secret');
    expect(authed.calls[0]!.headers['user-agent']).toMatch(/^frznforge(\/|$)/);
    expect(authed.calls[0]!.headers['accept']).toBe('application/vnd.github+json');

    const anon = recording(meta());
    await new GithubImporter(githubSource, { fetchImpl: anon.fetchImpl, token: null }).fetchMeta();
    expect(anon.calls[0]!.headers['authorization']).toBeUndefined();
  });
});

/* ---- GitLab --------------------------------------------------------------- */

describe('GitlabImporter', () => {
  const projectRoutes = routes({ '/projects/': { json: loadFixture('gitlab/project.json') } });

  it('maps project metadata, including the license fetched with ?license=true', async () => {
    const { fetchImpl, calls } = recording(projectRoutes);
    const meta = await new GitlabImporter(gitlabSource, { fetchImpl, token: null }).fetchMeta();

    expect(calls[0]!.url).toContain('/api/v4/projects/gitlab-org%2Fgitlab-runner?license=true');
    expect(meta).toEqual({
      name: 'gitlab-runner',
      description:
        'GitLab Runner is the open source project that is used to run your CI/CD jobs and send the results back to GitLab',
      homepage: null,
      topics: ['golang', 'hacktoberfest'],
      license: 'MIT',
      defaultBranch: 'main',
      webUrl: 'https://gitlab.com/gitlab-org/gitlab-runner',
      cloneUrl: 'https://gitlab.com/gitlab-org/gitlab-runner.git',
      issuesUrl: 'https://gitlab.com/gitlab-org/gitlab-runner/-/issues',
      template: false,
      archived: false,
    });
  });

  it('maps releases, links + sources assets, and millisecond timestamps', async () => {
    const importer = new GitlabImporter(gitlabSource, {
      fetchImpl: routes({ '/releases': { json: loadFixture('gitlab/releases.json') } }),
      token: null,
    });
    const { releases } = await importer.fetchReleases();

    // Date order, not tag order: v19.0.3 was released a day after v19.2.1.
    expect(releases.map((r) => r.tag)).toEqual(['v19.3.0', 'v19.2.2', 'v19.0.3', 'v19.2.1']);
    const latest = releases[0]!;
    expect(latest.publishedAt).toBe('2026-08-19T21:10:48Z');
    expect(latest.publishedAt).toMatch(ISO);
    expect(latest.url).toBe('https://gitlab.com/gitlab-org/gitlab-runner/-/releases/v19.3.0');
    expect(latest.author).toBe('ashvins');
    expect(latest.body).toContain('v19.3.0');
    // 3 uploaded links + the 4 archives GitLab generates, sorted by name, sizes unknown.
    expect(latest.assets).toHaveLength(7);
    expect(latest.assets.map((a) => a.name)).toEqual([...latest.assets.map((a) => a.name)].sort());
    expect(latest.assets.filter((a) => a.name.startsWith('Source code'))).toHaveLength(4);
    expect(latest.assets.every((a) => a.size === 0 && a.contentType === null)).toBe(true);
    for (const release of releases) expect(() => Release.parse(release)).not.toThrow();
  });

  it('absolutises host-relative asset and release URLs', async () => {
    // `direct_asset_url` is routinely host-relative. Left as-is it renders on the generated
    // site as a same-origin link (badged "external link") that 404s.
    const importer = new GitlabImporter(gitlabSource, {
      fetchImpl: routes({
        '/releases': {
          json: [
            {
              tag_name: 'v1.0.0',
              released_at: '2026-01-01T00:00:00Z',
              _links: { self: '/gitlab-org/gitlab-runner/-/releases/v1.0.0' },
              assets: {
                links: [{ name: 'installer.exe', direct_asset_url: '/gitlab-org/gitlab-runner/-/releases/v1.0.0/downloads/installer.exe' }],
                sources: [{ format: 'zip', url: '/gitlab-org/gitlab-runner/-/archive/v1.0.0/x.zip' }],
              },
            },
          ],
        },
      }),
      token: null,
    });
    const { releases } = await importer.fetchReleases();
    expect(releases[0]!.url).toBe('https://gitlab.com/gitlab-org/gitlab-runner/-/releases/v1.0.0');
    for (const asset of releases[0]!.assets) {
      expect(asset.url.startsWith('https://gitlab.com/')).toBe(true);
    }
  });

  it('never imports the wall-clock-derived upcoming_release as prerelease', async () => {
    // GitLab computes `upcoming_release` from `released_at > now`, so it flips on its own and
    // would change forge.json (and the Latest/Pre-release badges) with no change to the repo.
    const importer = new GitlabImporter(gitlabSource, {
      fetchImpl: routes({
        '/releases': {
          json: [
            { tag_name: 'v2.0.0', released_at: '2099-01-01T00:00:00Z', upcoming_release: true },
            { tag_name: 'v1.0.0', released_at: '2026-01-01T00:00:00Z', upcoming_release: false },
          ],
        },
      }),
      token: null,
    });
    const { releases } = await importer.fetchReleases();
    expect(releases.map((r) => r.prerelease)).toEqual([false, false]);
  });

  it('follows x-next-page pagination', async () => {
    const all = loadFixture('gitlab/releases.json') as unknown[];
    const { fetchImpl, calls } = recording(
      routes({
        'page=2': { json: all.slice(2) },
        '/releases': { json: all.slice(0, 2), headers: { 'x-next-page': '2' } },
      }),
    );

    const { releases } = await new GitlabImporter(gitlabSource, { fetchImpl, token: null }).fetchReleases();
    expect(releases).toHaveLength(4);
    expect(calls[1]!.url).toContain('page=2');
  });

  it('sends the token as PRIVATE-TOKEN', async () => {
    const { fetchImpl, calls } = recording(projectRoutes);
    await new GitlabImporter(gitlabSource, { fetchImpl, token: 'gl-secret' }).fetchMeta();
    expect(calls[0]!.headers['private-token']).toBe('gl-secret');
    expect(calls[0]!.headers['authorization']).toBeUndefined();
  });
});

/* ---- Gitea / Forgejo ------------------------------------------------------ */

describe('GiteaImporter', () => {
  it('maps repo metadata including the licenses array', async () => {
    const importer = new GiteaImporter(giteaSource, {
      fetchImpl: routes({ '/repos/gitea/tea': { json: loadFixture('gitea/repo.json') } }),
      token: null,
    });

    expect(await importer.fetchMeta()).toEqual({
      name: 'tea',
      description: 'A command line tool to interact with Gitea servers',
      homepage: null,
      topics: ['gitea', 'cli'],
      license: 'MIT',
      defaultBranch: 'main',
      webUrl: 'https://gitea.com/gitea/tea',
      cloneUrl: 'https://gitea.com/gitea/tea.git',
      issuesUrl: 'https://gitea.com/gitea/tea/issues',
      template: false,
      archived: false,
    });
  });

  it('maps releases and assets (no content type available)', async () => {
    const importer = new GiteaImporter(giteaSource, {
      fetchImpl: routes({ '/releases': { json: loadFixture('gitea/releases.json') } }),
      token: null,
    });
    const { releases } = await importer.fetchReleases();

    expect(releases.map((r) => r.tag)).toEqual(['v0.15.1', 'v0.15.0', 'v0.14.2', 'v0.14.1']);
    const latest = releases[0]!;
    expect(latest.publishedAt).toBe('2026-08-02T14:40:05Z');
    expect(latest.author).toBe('giteabot');
    expect(latest.assets[0]).toEqual({
      name: 'checksums.txt',
      url: 'https://gitea.com/gitea/tea/releases/download/v0.15.1/checksums.txt',
      size: 1842,
      contentType: null,
    });
    for (const release of releases) expect(() => Release.parse(release)).not.toThrow();
  });

  it('sends the token as "Authorization: token"', async () => {
    const { fetchImpl, calls } = recording(
      routes({ '/repos/gitea/tea': { json: loadFixture('gitea/repo.json') } }),
    );
    await new GiteaImporter(giteaSource, { fetchImpl, token: 'gt-secret' }).fetchMeta();
    expect(calls[0]!.headers['authorization']).toBe('token gt-secret');
    expect(calls[0]!.url).toBe('https://gitea.com/api/v1/repos/gitea/tea');
  });
});

describe('ForgejoImporter', () => {
  it('reports provider "forgejo" and tolerates the missing license field', async () => {
    const importer = new ForgejoImporter(forgejoSource, {
      fetchImpl: routes({ '/repos/forgejo/forgejo': { json: loadFixture('forgejo/repo.json') } }),
      token: null,
    });

    expect(importer.provider).toBe('forgejo');
    expect(await importer.fetchMeta()).toEqual({
      name: 'forgejo',
      description: 'Beyond coding. We forge.',
      homepage: 'https://forgejo.org',
      topics: ['forge', 'forgejo', 'git', 'self-hosted'],
      license: null,
      defaultBranch: 'forgejo',
      webUrl: 'https://codeberg.org/forgejo/forgejo',
      cloneUrl: 'https://codeberg.org/forgejo/forgejo.git',
      issuesUrl: 'https://codeberg.org/forgejo/forgejo/issues',
      template: false,
      archived: false,
    });
  });

  it('normalises offset timestamps to UTC and sorts by date, not by tag', async () => {
    const importer = new ForgejoImporter(forgejoSource, {
      fetchImpl: routes({ '/releases': { json: loadFixture('forgejo/releases.json') } }),
      token: null,
    });
    const { releases } = await importer.fetchReleases();

    // v15.0.7 was published minutes after v16.0.2 but before v16.0.3: date order, not tag order.
    expect(releases.map((r) => r.tag)).toEqual(['v16.0.3', 'v15.0.7', 'v16.0.2']);
    expect(releases[0]!.publishedAt).toBe('2026-08-20T08:49:50Z');
    for (const release of releases) {
      expect(release.publishedAt).toMatch(ISO);
      expect(() => Release.parse(release)).not.toThrow();
    }
  });
});

/* ---- registry ------------------------------------------------------------- */

describe('createImporter', () => {
  it('returns null for local sources and the right provider otherwise', () => {
    expect(createImporter({ type: 'local', path: './repo' })).toBeNull();
    expect(createImporter(githubSource, { token: null })?.provider).toBe('github');
    expect(createImporter(gitlabSource, { token: null })?.provider).toBe('gitlab');
    expect(createImporter(giteaSource, { token: null })?.provider).toBe('gitea');
    expect(createImporter(forgejoSource, { token: null })?.provider).toBe('forgejo');
  });
});

/* ---- failures ------------------------------------------------------------- */

describe('importer failures', () => {
  const failing = async (route: FixtureRoute): Promise<ImporterError> => {
    const importer = new GithubImporter(githubSource, {
      fetchImpl: routes({ '/repos/': route }),
      token: null,
    });
    try {
      await importer.fetchMeta();
    } catch (err) {
      return err as ImporterError;
    }
    throw new Error('expected the importer to throw');
  };

  it('maps 404 to not-found', async () => {
    const err = await failing({ status: 404, json: loadFixture('github/repo-404.json') });
    expect(err).toBeInstanceOf(ImporterError);
    expect(err.kind).toBe('not-found');
    expect(err.status).toBe(404);
    expect(err.message).toContain('Not Found');
  });

  it('maps 401 to auth and mentions the missing token', async () => {
    const err = await failing({ status: 401, json: { message: 'Bad credentials' } });
    expect(err.kind).toBe('auth');
    expect(err.message).toContain('no token configured');
  });

  it('maps 403 with rate-limit headers to rate-limit with retryAfter', async () => {
    const reset = Math.floor(Date.now() / 1000) + 120;
    const err = await failing({
      status: 403,
      json: loadFixture('github/rate-limited.json'),
      headers: { 'x-ratelimit-remaining': '0', 'x-ratelimit-reset': String(reset) },
    });
    expect(err.kind).toBe('rate-limit');
    expect(err.retryAfter).toBeGreaterThan(100);
    expect(err.retryAfter).toBeLessThanOrEqual(120);
  });

  it('keeps the clock and the caller’s IP out of the message that becomes a warning', async () => {
    // The message ships verbatim in forge.json. A countdown ("retry after 3599s") makes two
    // builds of the same repos differ; GitHub's anonymous body quotes the build host's IP.
    const reset = Math.floor(Date.now() / 1000) + 3599;
    const err = await failing({
      status: 403,
      json: { message: 'API rate limit exceeded for 203.0.113.1. (But here is the good news: …)' },
      headers: { 'x-ratelimit-remaining': '0', 'x-ratelimit-reset': String(reset) },
    });
    expect(err.kind).toBe('rate-limit');
    expect(err.message).not.toMatch(/retry after/i);
    expect(err.message).not.toMatch(/\b\d{1,3}(\.\d{1,3}){3}\b/);
    expect(err.message).toContain('[ip]');
    // Still available to the caller for console output, just not in the artifact text.
    expect(err.retryAfter).toBeGreaterThan(0);
  });

  it('scrubs IPv6 literals but leaves clock times alone', () => {
    expect(scrubIps('blocked 2001:db8:85a3:0:0:8a2e:370:7334 here')).toBe('blocked [ip] here');
    expect(scrubIps('resets at 10:30:45')).toBe('resets at 10:30:45');
  });

  it('maps 403 without rate-limit headers to auth', async () => {
    const err = await failing({ status: 403, json: { message: 'Forbidden' } });
    expect(err.kind).toBe('auth');
  });

  it('maps a non-JSON body to bad-response', async () => {
    const importer = new GithubImporter(githubSource, {
      fetchImpl: alwaysRespond('<html>upstream proxy error</html>', {
        headers: { 'content-type': 'text/html' },
      }),
      token: null,
    });
    await expect(importer.fetchMeta()).rejects.toMatchObject({ kind: 'bad-response' });
  });

  it('maps a fetch throw to network', async () => {
    const impl = (async () => {
      throw new Error('getaddrinfo ENOTFOUND api.github.com');
    }) as unknown as typeof fetch;
    const importer = new GithubImporter(githubSource, { fetchImpl: impl, token: null });
    await expect(importer.fetchMeta()).rejects.toMatchObject({ kind: 'network' });
  });

  it('never leaks the token into an error message', async () => {
    const token = 'ghp_SUPER_SECRET_VALUE_0123456789';
    const echo = new GithubImporter(githubSource, {
      token,
      // A provider that echoes the credential back in its error body is the worst case.
      fetchImpl: alwaysRespond(JSON.stringify({ message: `token ${token} is not valid` }), { status: 401 }),
    });
    const authError = await rejection(echo.fetchMeta());
    expect(authError.message).not.toContain(token);
    expect(authError.message).toContain('***');

    const thrower = (async () => {
      throw new Error(`connect failed for Bearer ${token}`);
    }) as unknown as typeof fetch;
    const networkError = await rejection(
      new GithubImporter(githubSource, { token, fetchImpl: thrower }).fetchMeta(),
    );
    expect(networkError.message).not.toContain(token);
  });
});

/* ---- JsonClient ----------------------------------------------------------- */

describe('JsonClient', () => {
  const client = (fetchImpl: typeof fetch, maxPages?: number): JsonClient =>
    new JsonClient({ auth: 'bearer', fetchImpl, retryDelayMs: 0, ...(maxPages ? { maxPages } : {}) });

  it('retries once after a 5xx and succeeds', async () => {
    const { fetchImpl, count } = sequence([
      async () => new Response('{}', { status: 502 }),
      async () => new Response(JSON.stringify({ ok: true }), { status: 200 }),
    ]);
    await expect(client(fetchImpl).get('https://example.test/thing')).resolves.toEqual({ ok: true });
    expect(count()).toBe(2);
  });

  it('retries once after a network throw and gives up as "network"', async () => {
    let calls = 0;
    const impl = (async () => {
      calls += 1;
      throw new Error('socket hang up');
    }) as unknown as typeof fetch;
    await expect(client(impl).get('https://example.test/thing')).rejects.toMatchObject({ kind: 'network' });
    expect(calls).toBe(2);
  });

  it('classifies a 5xx that survives the retry as network', async () => {
    const impl = alwaysRespond(JSON.stringify({ message: 'boom' }), { status: 503 });
    await expect(client(impl).get('https://example.test/thing')).rejects.toMatchObject({
      kind: 'network',
      status: 503,
    });
  });

  it('caps pagination and reports that the walk was cut short', async () => {
    let calls = 0;
    const impl = (async () => {
      calls += 1;
      return new Response(JSON.stringify([calls]), {
        status: 200,
        headers: { link: '<https://example.test/items?page=99>; rel="next"' },
      });
    }) as unknown as typeof fetch;
    const page = await client(impl, 5).getAll<number>('https://example.test/items');
    expect(calls).toBe(5);
    expect(page.items).toHaveLength(5);
    // A truncated walk must be distinguishable from a complete one, or the caller silently
    // publishes a repo missing its older releases.
    expect(page).toMatchObject({ truncated: true, pages: 5, maxPages: 5 });
  });

  it('reports a complete page walk as not truncated', async () => {
    const impl = (async () => new Response(JSON.stringify([1, 2]), { status: 200 })) as unknown as typeof fetch;
    const page = await client(impl, 5).getAll<number>('https://example.test/items');
    expect(page).toEqual({ items: [1, 2], truncated: false, pages: 1, maxPages: 5 });
  });

  it('rejects a paginated endpoint that answers with an object', async () => {
    const impl = alwaysRespond(JSON.stringify({ items: [] }), { status: 200 });
    await expect(client(impl).getAll('https://example.test/items')).rejects.toMatchObject({
      kind: 'bad-response',
    });
  });

  it('keeps query strings out of error messages', async () => {
    const impl = alwaysRespond(JSON.stringify({ message: 'nope' }), { status: 404 });
    const err = await rejection(client(impl).get('https://example.test/items?private_token=leaky'));
    expect(err.message).not.toContain('private_token');
  });
});

/* ---- normalisation helpers ------------------------------------------------ */

describe('toIsoDate', () => {
  it('honours an explicit zone', () => {
    expect(toIsoDate('2026-08-20T10:49:50+02:00')).toBe('2026-08-20T08:49:50Z');
    expect(toIsoDate('2026-08-19T21:10:48.199Z')).toBe('2026-08-19T21:10:48Z');
    expect(toIsoDate('2026-08-20T10:49:50-0600')).toBe('2026-08-20T16:49:50Z');
  });

  it('reads a zoneless timestamp as UTC, not as the build machine’s local time', () => {
    // Date.parse() treats these as local time, so the same self-hosted Gitea/Forgejo payload
    // would land in the artifact shifted by whatever offset the build machine happens to use.
    expect(toIsoDate('2026-08-20T10:49:50')).toBe('2026-08-20T10:49:50Z');
    expect(toIsoDate('2026-08-20 10:49:50')).toBe('2026-08-20T10:49:50Z');
    expect(toIsoDate('2026-08-20')).toBe('2026-08-20T00:00:00Z');
  });

  it('returns null for anything unusable', () => {
    for (const v of [null, undefined, 42, '', '   ', 'not a date']) expect(toIsoDate(v)).toBeNull();
  });
});

describe('absoluteUrl', () => {
  it('resolves relative provider URLs against the repo page and leaves absolute ones alone', () => {
    const base = 'https://gitlab.test/group/proj';
    expect(absoluteUrl('/group/proj/-/releases/v1/downloads/x.bin', base)).toBe(
      'https://gitlab.test/group/proj/-/releases/v1/downloads/x.bin',
    );
    expect(absoluteUrl('uploads/x.bin', base)).toBe('https://gitlab.test/group/proj/uploads/x.bin');
    expect(absoluteUrl('https://cdn.test/x.bin', base)).toBe('https://cdn.test/x.bin');
    // An unusable base must not lose the value.
    expect(absoluteUrl('/x', 'not a url')).toBe('/x');
  });
});
