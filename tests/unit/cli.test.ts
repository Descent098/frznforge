/**
 * `npm run frznforge -- init` — the pure pieces.
 *
 * No network (every provider call goes through an injected `fetchImpl`) and no TTY
 * (`Io.isTty` is false everywhere), so nothing in here can hang waiting for input.
 */
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import {
  NON_TTY_MESSAGE,
  USAGE,
  backupPathFor,
  entriesFor,
  entryKey,
  insertRepos,
  main,
  parseArgs,
  parseSelection,
  renderEntry,
  renderSnippet,
  selectRepos,
  tokenMessage,
  tokenStatus,
  updateConfigFile,
  type Io,
  type RemoteRepo,
} from '../../scripts/cli';

/* ------------------------------------------------------------------ helpers */

let tmp: string;

beforeEach(async () => {
  tmp = await fs.mkdtemp(path.join(os.tmpdir(), 'frznforge-cli-'));
});

afterEach(async () => {
  await fs.rm(tmp, { recursive: true, force: true });
});

const FIXTURE_CONFIG = `// frznforge site configuration.
// Read at build time — changing it requires a rebuild.
import { defineConfig } from './src/lib/config/schema';

export default defineConfig({
  owner: { name: 'Kieran Wood', handle: 'kieran', profile: './content/profile.md' },

  /**
   * Repositories to ingest.
   * Example:
   *   { type: 'local', path: '../useful' },
   */
  repos: [
    // Self-host demo: the frznforge repo itself.
    { type: 'local', path: '.', slug: 'frznforge' },
  ],

  ingest: {
    outDir: './data', // keep this comment
  },
});
`;

function repo(fullName: string, extra: Partial<RemoteRepo> = {}): RemoteRepo {
  const [owner = '', name = ''] = fullName.split('/');
  return { name, fullName, owner, description: null, archived: false, private: false, fork: false, ...extra };
}

/** An `Io` that records everything written instead of printing it. */
function testIo(overrides: Partial<Io> = {}): Io & { out: string[]; err: string[] } {
  const out: string[] = [];
  const err: string[] = [];
  return {
    out,
    err,
    log: (line) => out.push(line),
    error: (line) => err.push(line),
    isTty: false,
    env: {},
    cwd: tmp,
    ...overrides,
  };
}

/** A `fetch` that answers the listing endpoints from in-memory JSON. Never touches a socket. */
function fakeFetch(pages: Record<string, unknown>): typeof fetch {
  return (async (input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    for (const [pattern, body] of Object.entries(pages)) {
      if (url.includes(pattern)) {
        return new Response(JSON.stringify(body), { status: 200, headers: { 'content-type': 'application/json' } });
      }
    }
    return new Response(JSON.stringify({ message: 'Not Found' }), { status: 404 });
  }) as typeof fetch;
}

/* ------------------------------------------------------------------ argument parsing */

describe('parseArgs', () => {
  it('reads a command plus --key=value and --key value flags', () => {
    const { command, flags, errors } = parseArgs([
      'init',
      '--provider=github',
      '--account',
      'Descent098',
      '--select=1,3',
      '--releases',
      'tags',
      '--print',
    ]);
    expect(errors).toEqual([]);
    expect(command).toBe('init');
    expect(flags.provider).toBe('github');
    expect(flags.account).toBe('Descent098');
    expect(flags.select).toBe('1,3');
    expect(flags.releases).toBe('tags');
    expect(flags.print).toBe(true);
    expect(flags.yes).toBe(false);
  });

  it('accepts -h and --help', () => {
    expect(parseArgs(['-h']).flags.help).toBe(true);
    expect(parseArgs(['init', '--help']).flags.help).toBe(true);
  });

  it('strips a trailing slash from --host', () => {
    expect(parseArgs(['init', '--host=https://codeberg.org/']).flags.host).toBe('https://codeberg.org');
  });

  it('collects errors instead of throwing', () => {
    expect(parseArgs(['init', '--nope']).errors).toEqual(['unknown option: --nope']);
    expect(parseArgs(['init', '--provider=bitbucket']).errors[0]).toMatch(/--provider must be one of/);
    expect(parseArgs(['init', '--releases=maybe']).errors[0]).toMatch(/--releases must be/);
    expect(parseArgs(['init', '--account']).errors).toEqual(['--account needs a value']);
    expect(parseArgs(['init', 'extra']).errors).toEqual(['unexpected argument: extra']);
  });
});

/* ------------------------------------------------------------------ selection */

describe('parseSelection', () => {
  it('handles all / none / empty', () => {
    expect(parseSelection('all', 3)).toEqual({ ok: true, value: [0, 1, 2] });
    expect(parseSelection('ALL', 2)).toEqual({ ok: true, value: [0, 1] });
    expect(parseSelection('none', 3)).toEqual({ ok: true, value: [] });
    expect(parseSelection('   ', 3)).toEqual({ ok: true, value: [] });
  });

  it('handles indexes and ranges, sorted and de-duplicated', () => {
    expect(parseSelection('1,3,5-8', 10)).toEqual({ ok: true, value: [0, 2, 4, 5, 6, 7] });
    expect(parseSelection('3, 1 , 3', 4)).toEqual({ ok: true, value: [0, 2] });
    expect(parseSelection('2-2', 4)).toEqual({ ok: true, value: [1] });
  });

  it('rejects nonsense', () => {
    expect(parseSelection('abc', 3)).toEqual({ ok: false, error: 'not a number: abc' });
    expect(parseSelection('0', 3)).toEqual({ ok: false, error: '0 is out of range (1-3)' });
    expect(parseSelection('4', 3)).toEqual({ ok: false, error: '4 is out of range (1-3)' });
    expect(parseSelection('1-9', 3)).toEqual({ ok: false, error: '4 is out of range (1-3)' });
    expect(parseSelection('5-2', 9)).toEqual({ ok: false, error: 'range 5-2 runs backwards' });
    expect(parseSelection('1;2', 3).ok).toBe(false);
  });
});

describe('selectRepos', () => {
  const repos = [repo('me/alpha'), repo('me/beta'), repo('me/gamma')];

  it('selects by index and by name', () => {
    expect(selectRepos(repos, '1,3')).toEqual({ ok: true, value: [repos[0], repos[2]] });
    expect(selectRepos(repos, 'all')).toEqual({ ok: true, value: repos });
    expect(selectRepos(repos, 'gamma,alpha')).toEqual({ ok: true, value: [repos[2], repos[0]] });
    expect(selectRepos(repos, 'me/beta')).toEqual({ ok: true, value: [repos[1]] });
  });

  it('reports unknown names and out-of-range indexes', () => {
    expect(selectRepos(repos, 'delta')).toEqual({ ok: false, error: 'no repository named delta' });
    expect(selectRepos(repos, '9')).toEqual({ ok: false, error: '9 is out of range (1-3)' });
  });
});

/* ------------------------------------------------------------------ entries */

describe('entries', () => {
  it('renders one line per source, omitting the default host', () => {
    expect(renderEntry({ type: 'github', owner: 'a', repo: 'b', releases: 'provider' })).toBe(
      "{ type: 'github', owner: 'a', repo: 'b', releases: 'provider' }",
    );
    expect(renderEntry({ type: 'gitea', host: 'https://git.example.com', owner: 'a', repo: 'b' })).toBe(
      "{ type: 'gitea', host: 'https://git.example.com', owner: 'a', repo: 'b' }",
    );
  });

  it('builds github/gitlab/forgejo entries from a listing', () => {
    expect(entriesFor('github', 'https://api.github.com', [repo('me/alpha')], 'provider')).toEqual([
      { type: 'github', owner: 'me', repo: 'alpha', releases: 'provider' },
    ]);
    expect(
      entriesFor('gitlab', 'https://gitlab.com', [{ ...repo('g/s/proj'), project: 'g/s/proj', name: 'proj' }], 'tags'),
    ).toEqual([{ type: 'gitlab', project: 'g/s/proj', releases: 'tags' }]);
    expect(entriesFor('forgejo', 'https://codeberg.org', [repo('me/alpha')], 'provider')).toEqual([
      { type: 'forgejo', host: 'https://codeberg.org', owner: 'me', repo: 'alpha', releases: 'provider' },
    ]);
  });

  it('disambiguates repos that would share a slug', () => {
    const entries = entriesFor('github', 'https://api.github.com', [repo('one/tool'), repo('two/tool')], 'provider');
    expect(entries.map((e) => e.slug)).toEqual(['one-tool', 'two-tool']);
  });

  it('keys sources by identity, defaulting the host', () => {
    expect(entryKey({ type: 'github', owner: 'A', repo: 'B' })).toBe(
      entryKey({ type: 'github', host: 'https://api.github.com/', owner: 'a', repo: 'b' }),
    );
    expect(entryKey({ type: 'gitea', host: 'https://x.dev', owner: 'a', repo: 'b' })).not.toBe(
      entryKey({ type: 'forgejo', host: 'https://x.dev', owner: 'a', repo: 'b' }),
    );
  });

  it('renders a pasteable snippet', () => {
    expect(renderSnippet([{ type: 'github', owner: 'a', repo: 'b' }])).toBe(
      "  repos: [\n    { type: 'github', owner: 'a', repo: 'b' },\n  ],",
    );
  });
});

/* ------------------------------------------------------------------ config insertion */

describe('insertRepos', () => {
  const entry = { type: 'github', owner: 'Descent098', repo: 'ezcv', releases: 'provider' } as const;

  it('adds entries to the repos array without disturbing anything else', () => {
    const result = insertRepos(FIXTURE_CONFIG, [entry]);
    expect(result).not.toBeNull();
    expect(result!.changed).toBe(true);
    expect(result!.added).toHaveLength(1);
    expect(result!.text).toContain("    { type: 'github', owner: 'Descent098', repo: 'ezcv', releases: 'provider' },");
    // every original comment survives
    expect(result!.text).toContain('// Self-host demo: the frznforge repo itself.');
    expect(result!.text).toContain('* Repositories to ingest.');
    expect(result!.text).toContain("outDir: './data', // keep this comment");
    // the existing local entry is untouched
    expect(result!.text).toContain("{ type: 'local', path: '.', slug: 'frznforge' },");
    // nothing outside the array moved
    expect(result!.text.replace(/^ +\{ type: 'github'.*\n/m, '')).toBe(FIXTURE_CONFIG);
  });

  it('is idempotent: a second run adds nothing', () => {
    const first = insertRepos(FIXTURE_CONFIG, [entry])!;
    const second = insertRepos(first.text, [entry])!;
    expect(second.changed).toBe(false);
    expect(second.added).toEqual([]);
    expect(second.skipped).toEqual([entry]);
    expect(second.text).toBe(first.text);
  });

  it('matches existing entries regardless of quoting, key order and default host', () => {
    const existing = `export default { repos: [\n  { repo: "ezcv", owner: "Descent098", type: "github", host: "https://api.github.com" },\n] };\n`;
    const result = insertRepos(existing, [entry])!;
    expect(result.changed).toBe(false);
    expect(result.skipped).toEqual([entry]);
  });

  it('fills an empty array', () => {
    const result = insertRepos('  repos: [],\n', [entry])!;
    expect(result.text).toBe(
      "  repos: [\n    { type: 'github', owner: 'Descent098', repo: 'ezcv', releases: 'provider' },\n  ],\n",
    );
  });

  it('adds the missing comma after an entry without a trailing one', () => {
    const result = insertRepos("export default { repos: [\n  { type: 'local', path: '.' }\n] };\n", [entry])!;
    expect(result.text).toContain("{ type: 'local', path: '.' },\n");
    expect(result.text).toContain("{ type: 'github', owner: 'Descent098', repo: 'ezcv', releases: 'provider' },");
  });

  it('ignores a repos: [ ... ] that only appears inside a string or comment', () => {
    expect(insertRepos("const s = 'repos: [';\n", [entry])).toBeNull();
    expect(insertRepos('export default {};\n', [entry])).toBeNull();
  });

  it('does not read entry fields out of a comment', () => {
    // A commented-out example above a real entry used to be read *instead of* the real entry,
    // because readField takes the first match in the element's source text.
    const commentedExample = `export default { repos: [
  // { type: 'github', owner: 'Descent098', repo: 'ezcv' },   <- example, not enabled
  { type: 'local', path: '.' },
] };
`;
    const skipped = insertRepos(commentedExample, [entry, { ...entry, repo: 'sdu' }])!;
    // ezcv is only mentioned in a comment, so it must be added, not reported as present.
    expect(skipped.added.map((e) => e.repo)).toEqual(['ezcv', 'sdu']);
    expect(skipped.skipped).toEqual([]);

    const shadowedReal = `export default { repos: [
  // was: { type: 'github', owner: 'Descent098', repo: 'beta' } — renamed
  { type: 'github', owner: 'Descent098', repo: 'ezcv', releases: 'provider' },
] };
`;
    const duplicate = insertRepos(shadowedReal, [entry])!;
    // …and the real entry underneath the comment must still be recognised, not duplicated.
    expect(duplicate.changed).toBe(false);
    expect(duplicate.skipped).toEqual([entry]);
  });

  it('sees through a block comment too', () => {
    const source = `export default { repos: [
  /* { type: 'github', owner: 'Descent098', repo: 'ezcv' } */
  { type: 'local', path: '.' },
] };
`;
    expect(insertRepos(source, [entry])!.added).toEqual([entry]);
  });
});

describe('updateConfigFile', () => {
  it('backs the file up, writes once, and does nothing on a re-run', async () => {
    const file = path.join(tmp, 'frznforge.config.ts');
    await fs.writeFile(file, FIXTURE_CONFIG, 'utf8');
    const entry = { type: 'forgejo', host: 'https://codeberg.org', owner: 'me', repo: 'thing' } as const;

    const first = await updateConfigFile(file, [entry], { now: new Date('2026-08-23T19:51:00Z') });
    expect(first!.changed).toBe(true);
    expect(first!.backup).toBe(`${file}.20260823T195100Z.bak`);
    expect(await fs.readFile(first!.backup!, 'utf8')).toBe(FIXTURE_CONFIG);

    const written = await fs.readFile(file, 'utf8');
    expect(written).toContain("{ type: 'forgejo', host: 'https://codeberg.org', owner: 'me', repo: 'thing' },");
    expect(written).toContain('// Self-host demo: the frznforge repo itself.');

    const second = await updateConfigFile(file, [entry]);
    expect(second!.changed).toBe(false);
    expect(second!.backup).toBeNull();
    expect(await fs.readFile(file, 'utf8')).toBe(written);
  });

  it('names backups with a UTC timestamp', () => {
    expect(backupPathFor('/x/frznforge.config.ts', new Date('2026-01-02T03:04:05.678Z'))).toBe(
      '/x/frznforge.config.ts.20260102T030405Z.bak',
    );
  });
});

/* ------------------------------------------------------------------ tokens */

describe('token reporting', () => {
  const SECRET = 'ghp_supersecretvalue1234567890';

  it('prefers FRZNFORGE_<PROVIDER>_TOKEN, then the conventional variable', () => {
    expect(tokenStatus('github', {}).names).toEqual(['FRZNFORGE_GITHUB_TOKEN', 'GITHUB_TOKEN']);
    expect(tokenStatus('gitlab', {}).names).toEqual(['FRZNFORGE_GITLAB_TOKEN', 'GITLAB_TOKEN']);
    expect(tokenStatus('forgejo', {}).names).toEqual(['FRZNFORGE_FORGEJO_TOKEN', 'FORGEJO_TOKEN']);
    expect(tokenStatus('github', { GITHUB_TOKEN: SECRET }).from).toBe('GITHUB_TOKEN');
    expect(tokenStatus('github', { FRZNFORGE_GITHUB_TOKEN: SECRET, GITHUB_TOKEN: 'other' }).from).toBe(
      'FRZNFORGE_GITHUB_TOKEN',
    );
    expect(tokenStatus('github', {}).from).toBeNull();
  });

  it('names the variable but never the value', () => {
    const found = tokenMessage(tokenStatus('github', { GITHUB_TOKEN: SECRET }));
    expect(found).toContain('$GITHUB_TOKEN');
    expect(found).not.toContain(SECRET);

    const missing = tokenMessage(tokenStatus('gitea', {}));
    expect(missing).toContain('$GITEA_TOKEN');
    expect(missing).toContain('public repositories only');
    expect(missing).not.toContain(SECRET);
  });

  it('keeps the value out of everything init prints', async () => {
    const io = testIo({
      env: { GITHUB_TOKEN: SECRET },
      fetchImpl: fakeFetch({ '/users/me/repos': [{ name: 'alpha', full_name: 'me/alpha', owner: { login: 'me' } }] }),
    });
    const code = await main(['init', '--provider=github', '--account=me', '--select=all', '--print'], io);
    expect(code).toBe(0);
    const everything = [...io.out, ...io.err].join('\n');
    expect(everything).toContain('$GITHUB_TOKEN');
    expect(everything).not.toContain(SECRET);
    expect(everything).toContain("{ type: 'github', owner: 'me', repo: 'alpha', releases: 'provider' },");
  });
});

/* ------------------------------------------------------------------ dispatch */

describe('main', () => {
  it('prints usage for --help and exits 0', async () => {
    const io = testIo();
    expect(await main(['--help'], io)).toBe(0);
    expect(io.out.join('\n')).toBe(USAGE);
  });

  it('rejects an unknown command with usage and exit 1', async () => {
    const io = testIo();
    expect(await main(['frobnicate'], io)).toBe(1);
    expect(io.err.join('\n')).toContain('unknown command "frobnicate"');
    expect(io.err.join('\n')).toContain('Usage');
  });

  it('rejects bad flags with exit 1', async () => {
    const io = testIo();
    expect(await main(['init', '--provider=bitbucket'], io)).toBe(1);
    expect(io.err.join('\n')).toContain('--provider must be one of');
  });

  it('exits 1 with instructions when stdin is not a TTY and answers are missing', async () => {
    const io = testIo();
    expect(await main(['init'], io)).toBe(1);
    expect(io.err.join('\n')).toBe(NON_TTY_MESSAGE);
    expect(io.err.join('\n')).toContain('docs/user/importing.md');
    expect(io.err.join('\n')).toContain('--provider=github');
  });

  it('still bails out when only some flags are given', async () => {
    const io = testIo();
    expect(await main(['init', '--provider=gitea', '--account=me', '--select=all'], io)).toBe(1);
    expect(io.err.join('\n')).toBe(NON_TTY_MESSAGE); // gitea needs --host
  });

  it('runs end to end with flags, no TTY and no network', async () => {
    const file = path.join(tmp, 'frznforge.config.ts');
    await fs.writeFile(file, FIXTURE_CONFIG, 'utf8');
    const io = testIo({
      fetchImpl: fakeFetch({
        '/api/v1/users/me/repos': [
          { name: 'beta', full_name: 'me/beta', owner: { login: 'me' }, description: 'B', archived: true },
          { name: 'alpha', full_name: 'me/alpha', owner: { login: 'me' }, description: 'A' },
        ],
      }),
    });
    const code = await main(
      ['init', '--provider=forgejo', '--host=https://codeberg.org', '--account=me', '--select=alpha', '--releases=tags', '--yes'],
      io,
    );
    expect(code).toBe(0);
    const written = await fs.readFile(file, 'utf8');
    expect(written).toContain(
      "{ type: 'forgejo', host: 'https://codeberg.org', owner: 'me', repo: 'alpha', releases: 'tags' },",
    );
    expect(written).not.toContain("repo: 'beta'");
    expect(written).toContain('// Self-host demo: the frznforge repo itself.');
    expect(io.out.join('\n')).toContain('Backup: ');
  });

  it('never writes with --print', async () => {
    const file = path.join(tmp, 'frznforge.config.ts');
    await fs.writeFile(file, FIXTURE_CONFIG, 'utf8');
    const io = testIo({
      fetchImpl: fakeFetch({ '/users/me/repos': [{ name: 'alpha', full_name: 'me/alpha', owner: { login: 'me' } }] }),
    });
    expect(await main(['init', '--provider=github', '--account=me', '--select=all', '--print', '--yes'], io)).toBe(0);
    expect(await fs.readFile(file, 'utf8')).toBe(FIXTURE_CONFIG);
  });

  it('does not write when confirmation is impossible and --yes is absent', async () => {
    const file = path.join(tmp, 'frznforge.config.ts');
    await fs.writeFile(file, FIXTURE_CONFIG, 'utf8');
    const io = testIo({
      fetchImpl: fakeFetch({ '/users/me/repos': [{ name: 'alpha', full_name: 'me/alpha', owner: { login: 'me' } }] }),
    });
    expect(await main(['init', '--provider=github', '--account=me', '--select=all'], io)).toBe(0);
    expect(await fs.readFile(file, 'utf8')).toBe(FIXTURE_CONFIG);
    expect(io.out.join('\n')).toContain('--yes');
  });

  it('reports a bad --select without touching the config', async () => {
    const io = testIo({
      fetchImpl: fakeFetch({ '/users/me/repos': [{ name: 'alpha', full_name: 'me/alpha', owner: { login: 'me' } }] }),
    });
    expect(await main(['init', '--provider=github', '--account=me', '--select=42'], io)).toBe(1);
    expect(io.err.join('\n')).toContain('--select: 42 is out of range (1-1)');
  });

  it('never prompts on a TTY when the flags already describe the run', async () => {
    // --print is documented as the scriptable form; blocking on "Where should releases come
    // from?" there made a terminal behave differently from CI over the same command line.
    const file = path.join(tmp, 'frznforge.config.ts');
    await fs.writeFile(file, FIXTURE_CONFIG, 'utf8');
    const io = testIo({
      isTty: true,
      fetchImpl: fakeFetch({ '/users/me/repos': [{ name: 'alpha', full_name: 'me/alpha', owner: { login: 'me' } }] }),
    });
    // stdin is empty here: had a prompt been reached this would exit 1 on InputClosedError.
    expect(await main(['init', '--provider=github', '--account=me', '--select=all', '--print'], io)).toBe(0);
    expect(io.out.join('\n')).toContain("{ type: 'github', owner: 'me', repo: 'alpha', releases: 'provider' },");
    expect(await fs.readFile(file, 'utf8')).toBe(FIXTURE_CONFIG);

    const written = testIo({
      isTty: true,
      fetchImpl: fakeFetch({ '/users/me/repos': [{ name: 'alpha', full_name: 'me/alpha', owner: { login: 'me' } }] }),
    });
    expect(await main(['init', '--provider=github', '--account=me', '--select=all', '--yes'], written)).toBe(0);
    expect(await fs.readFile(file, 'utf8')).toContain("repo: 'alpha', releases: 'provider' },");
  });

  it('says so when the account listing was cut short by the page cap', async () => {
    // A full last page means there was more; a caller cannot otherwise tell a complete
    // listing from a truncated one, and --select=1,3,5-8 against it is silently wrong.
    const page = Array.from({ length: 100 }, (_, i) => ({
      name: `r${String(i).padStart(3, '0')}`,
      full_name: `me/r${String(i).padStart(3, '0')}`,
      owner: { login: 'me' },
    }));
    const io = testIo({
      fetchImpl: (async () => new Response(JSON.stringify(page), { status: 200 })) as typeof fetch,
    });
    expect(await main(['init', '--provider=github', '--account=me', '--select=all', '--print'], io)).toBe(0);
    expect(io.err.join('\n')).toContain('listing stopped after 1000 repositories');
    expect(io.err.join('\n')).toContain('--select=name,name');
  });

  it('turns a rate-limited provider response into a readable error', async () => {
    const io = testIo({
      fetchImpl: (async () =>
        new Response(JSON.stringify({ message: 'API rate limit exceeded' }), {
          status: 403,
          headers: { 'x-ratelimit-remaining': '0' },
        })) as typeof fetch,
    });
    expect(await main(['init', '--provider=github', '--account=me', '--select=all'], io)).toBe(1);
    expect(io.err.join('\n')).toContain('rate limited by api.github.com');
    expect(io.err.join('\n')).toContain('$GITHUB_TOKEN');
  });
});
