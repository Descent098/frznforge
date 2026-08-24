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
  EXCLUDE_FILTERS,
  EXCLUDE_HELP,
  NON_TTY_MESSAGE,
  USAGE,
  askForSelection,
  backupPathFor,
  entriesFor,
  listRepos,
  entryKey,
  excludedEverythingMessage,
  insertRepos,
  main,
  parseAllSpec,
  parseArgs,
  parseSelection,
  renderEntry,
  renderSnippet,
  resolveSelection,
  selectRepos,
  selectionSummary,
  tokenMessage,
  tokenStatus,
  updateConfigFile,
  type ExcludeFilter,
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

/** Unwrap a `Result`, failing the test (rather than the type checker) when it is an error. */
function ok<T>(result: { ok: true; value: T } | { ok: false; error: string }): T {
  if (!result.ok) throw new Error(`expected ok, got: ${result.error}`);
  return result.value;
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

  it('reads the browser-UI flags', () => {
    const { flags, errors } = parseArgs(['init', '--web', '--port=4173', '--no-open']);
    expect(errors).toEqual([]);
    expect(flags.web).toBe(true);
    expect(flags.port).toBe(4173);
    expect(flags.noOpen).toBe(true);

    const bare = parseArgs(['init']).flags;
    expect(bare.web).toBe(false);
    expect(bare.noOpen).toBe(false);
    expect(bare.port).toBeUndefined();
    // 0 is meaningful: "pick a free ephemeral port".
    expect(parseArgs(['init', '--web', '--port', '0']).flags.port).toBe(0);
  });

  it('rejects a port that is not a port', () => {
    expect(parseArgs(['init', '--port=x']).errors[0]).toMatch(/--port must be a whole number/);
    expect(parseArgs(['init', '--port=70000']).errors[0]).toMatch(/--port must be a whole number/);
    expect(parseArgs(['init', '--port=80.5']).errors[0]).toMatch(/--port must be a whole number/);
  });

  it('rejects --web together with --print', () => {
    const { errors } = parseArgs(['init', '--web', '--print']);
    expect(errors).toHaveLength(1);
    expect(errors[0]).toContain('--web and --print cannot be combined');
    expect(parseArgs(['init', '--web']).errors).toEqual([]);
    expect(parseArgs(['init', '--print']).errors).toEqual([]);
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

/* ------------------------------------------------------------------ all-<flags> */

describe('parseAllSpec', () => {
  const codes = EXCLUDE_FILTERS.map((f) => f.code);

  it('reads plain all, and nothing else', () => {
    expect(parseAllSpec('all')).toEqual({ ok: true, value: new Set() });
    expect(parseAllSpec('  ALL  ')).toEqual({ ok: true, value: new Set() });
    expect(parseAllSpec('*')).toEqual({ ok: true, value: new Set() });
    // not an `all` form at all → null, so the caller falls back to indexes and names
    expect(parseAllSpec('')).toBeNull();
    expect(parseAllSpec('none')).toBeNull();
    expect(parseAllSpec('1,3,5-8')).toBeNull();
    expect(parseAllSpec('ezcv,sdu')).toBeNull();
    expect(parseAllSpec('allsorts')).toBeNull();
  });

  it('reads every code in the table on its own', () => {
    for (const filter of EXCLUDE_FILTERS) {
      expect(parseAllSpec(`all-${filter.code}`)).toEqual({ ok: true, value: new Set([filter.code]) });
    }
  });

  it('reads codes concatenated, dash separated, repeated and in any case', () => {
    const [first = '', second = ''] = codes;
    const both = new Set([first, second]);
    expect(parseAllSpec(`all-${first}${second}`)).toEqual({ ok: true, value: both });
    expect(parseAllSpec(`all-${first}-${second}`)).toEqual({ ok: true, value: both });
    expect(parseAllSpec(`all-${second}-${first}`)).toEqual({ ok: true, value: both });
    expect(parseAllSpec(`ALL-${first.toUpperCase()}${second}`)).toEqual({ ok: true, value: both });
    expect(parseAllSpec(`all-${first}-${first}`)).toEqual({ ok: true, value: new Set([first]) });
    expect(parseAllSpec(`all-${codes.join('')}`)).toEqual({ ok: true, value: new Set(codes) });
  });

  it('names the offending flag and explains the known ones', () => {
    expect(parseAllSpec('all-nx')).toEqual({
      ok: false,
      error: `unknown filter 'nx' in 'all-nx'; known: ${EXCLUDE_HELP}`,
    });
    // a good flag followed by a bad one points at the bad one, not at the whole spec
    expect(parseAllSpec('all-nfnx')).toEqual({
      ok: false,
      error: `unknown filter 'nx' in 'all-nfnx'; known: ${EXCLUDE_HELP}`,
    });
    expect(parseAllSpec('all-')).toEqual({ ok: false, error: `'all-' names no filter; known: ${EXCLUDE_HELP}` });
  });

  it('spells the known filters out of the table, once', () => {
    expect(EXCLUDE_HELP).toBe('nf = forks, na = archived, np = private');
    expect(EXCLUDE_HELP).toBe(EXCLUDE_FILTERS.map((f) => `${f.code} = ${f.label}`).join(', '));
  });
});

describe('resolveSelection', () => {
  /** The listing flags a filter can key off; used to build a repo that trips exactly one. */
  const FLAG_KEYS = ['fork', 'archived', 'private'] as const;

  /** A repo that matches `filter` and nothing else — derived from the predicate, not restated. */
  function repoMatching(filter: ExcludeFilter): RemoteRepo {
    const key = FLAG_KEYS.find((k) => filter.matches({ [k]: true }));
    expect(key, `no listing flag makes ${filter.code} match`).toBeDefined();
    return repo(`me/${filter.code}`, { [key!]: true });
  }

  const plain = repo('me/plain');
  const forked = repo('me/forked', { fork: true });
  const old = repo('me/old', { archived: true });
  const hidden = repo('me/hidden', { private: true });
  const listing = [forked, hidden, old, plain];

  it('drops exactly what each code names', () => {
    for (const filter of EXCLUDE_FILTERS) {
      const flagged = repoMatching(filter);
      const result = resolveSelection([plain, flagged], `all-${filter.code}`);
      expect(result).toEqual({
        ok: true,
        value: {
          repos: [plain],
          total: 2,
          filtered: true,
          excluded: [{ code: filter.code, label: filter.label, one: filter.one, count: 1 }],
        },
      });
    }
  });

  it('combines codes', () => {
    expect(ok(resolveSelection(listing, 'all-nfna'))).toMatchObject({ repos: [hidden, plain] });
    expect(ok(resolveSelection(listing, 'all-nf-na-np'))).toMatchObject({ repos: [plain] });
    expect(ok(resolveSelection(listing, 'all'))).toMatchObject({ repos: listing, filtered: false, excluded: [] });
  });

  it('is a no-op when nothing matches', () => {
    const clean = [plain, repo('me/other')];
    const result = resolveSelection(clean, 'all-nf');
    expect(ok(result)).toEqual({ repos: clean, total: 2, filtered: true, excluded: [] });
    expect(selectionSummary(ok(result))).toBeNull();
  });

  it('counts a repo once, under the first reason that dropped it', () => {
    const both = repo('me/both', { fork: true, archived: true });
    const result = resolveSelection([plain, both, old], 'all-nfna');
    expect(ok(result).repos).toEqual([plain]);
    expect(ok(result).excluded).toEqual([
      { code: 'nf', label: 'forks', one: 'fork', count: 1 },
      { code: 'na', label: 'archived', one: 'archived', count: 1 },
    ]);
  });

  it('leaves explicit index and name selections alone', () => {
    // `1,3` and `ezcv,sdu` name repositories outright: being a fork or archived is irrelevant.
    const named = [repo('me/ezcv', { fork: true }), plain, repo('me/sdu', { archived: true, private: true })];
    expect(ok(resolveSelection(named, '1,3'))).toMatchObject({ repos: [named[0], named[2]], filtered: false });
    expect(ok(resolveSelection(named, 'ezcv,sdu'))).toMatchObject({ repos: [named[0], named[2]], filtered: false });
    expect(selectRepos(named, 'ezcv,sdu')).toEqual({ ok: true, value: [named[0], named[2]] });
  });

  it('passes the parse error through', () => {
    expect(resolveSelection(listing, 'all-nx').ok).toBe(false);
    expect(selectRepos(listing, 'all-nx')).toEqual({
      ok: false,
      // Both halves: `all-nx` is neither a known filter nor a repository in this listing.
      error: `unknown filter 'nx' in 'all-nx'; known: ${EXCLUDE_HELP}, and no listed repository is named 'all-nx'`,
    });
  });
});

describe('selection reporting', () => {
  const outcome = (repos: number, total: number, excluded: Array<[string, number]>) => ({
    repos: Array.from({ length: repos }, (_, i) => i),
    total,
    filtered: true,
    excluded: excluded.map(([code, count]) => {
      const filter = EXCLUDE_FILTERS.find((f) => f.code === code)!;
      return { code: filter.code, label: filter.label, one: filter.one, count };
    }),
  });

  it('says how many survived and why the rest did not', () => {
    expect(selectionSummary(outcome(12, 20, [['nf', 5], ['na', 3]]))).toBe(
      'selected 12 of 20 (excluded 5 forks, 3 archived)',
    );
  });

  it('uses the singular for a count of one', () => {
    expect(selectionSummary(outcome(2, 3, [['nf', 1]]))).toBe('selected 2 of 3 (excluded 1 fork)');
    expect(selectionSummary(outcome(1, 3, [['nf', 1], ['np', 1]]))).toBe(
      'selected 1 of 3 (excluded 1 fork, 1 private)',
    );
  });

  it('omits reasons that dropped nothing', () => {
    expect(selectionSummary(outcome(4, 5, [['na', 1]]))).toBe('selected 4 of 5 (excluded 1 archived)');
    expect(selectionSummary(outcome(5, 5, []))).toBeNull();
  });

  it('spells out a filter that left nothing', () => {
    expect(excludedEverythingMessage('all-nf', outcome(0, 4, [['nf', 4]]))).toBe(
      'all-nf excluded all 4 repositories (4 forks) — nothing to add.',
    );
    expect(excludedEverythingMessage(' all-np ', outcome(0, 1, [['np', 1]]))).toBe(
      'all-np excluded all 1 repository (1 private) — nothing to add.',
    );
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

  it('adds an account minus its forks and archives, and says what it dropped', async () => {
    const io = testIo({
      fetchImpl: fakeFetch({
        '/users/me/repos': [
          { name: 'alpha', full_name: 'me/alpha', owner: { login: 'me' } },
          { name: 'beta', full_name: 'me/beta', owner: { login: 'me' }, fork: true },
          { name: 'delta', full_name: 'me/delta', owner: { login: 'me' }, archived: true },
          { name: 'gamma', full_name: 'me/gamma', owner: { login: 'me' }, fork: true },
        ],
      }),
    });
    expect(await main(['init', '--provider=github', '--account=me', '--select=all-nfna', '--print'], io)).toBe(0);
    const printed = io.out.join('\n');
    expect(printed).toContain('selected 1 of 4 (excluded 2 forks, 1 archived)');
    expect(printed).toContain("{ type: 'github', owner: 'me', repo: 'alpha', releases: 'provider' },");
    expect(printed).not.toContain("repo: 'beta'");
    expect(printed).not.toContain("repo: 'delta'");
  });

  it('rejects an unknown filter with the list of known ones', async () => {
    const io = testIo({
      fetchImpl: fakeFetch({ '/users/me/repos': [{ name: 'alpha', full_name: 'me/alpha', owner: { login: 'me' } }] }),
    });
    expect(await main(['init', '--provider=github', '--account=me', '--select=all-nx', '--print'], io)).toBe(1);
    expect(io.err.join('\n')).toBe(
      `--select: unknown filter 'nx' in 'all-nx'; known: ${EXCLUDE_HELP}, and no listed repository is named 'all-nx'`,
    );
  });

  it('refuses to write an empty selection when a filter excluded everything', async () => {
    const file = path.join(tmp, 'frznforge.config.ts');
    await fs.writeFile(file, FIXTURE_CONFIG, 'utf8');
    const io = testIo({
      fetchImpl: fakeFetch({
        '/users/me/repos': [
          { name: 'alpha', full_name: 'me/alpha', owner: { login: 'me' }, fork: true },
          { name: 'beta', full_name: 'me/beta', owner: { login: 'me' }, fork: true },
        ],
      }),
    });
    expect(await main(['init', '--provider=github', '--account=me', '--select=all-nf', '--yes'], io)).toBe(1);
    expect(io.err.join('\n')).toContain('all-nf excluded all 2 repositories (2 forks) — nothing to add.');
    expect(await fs.readFile(file, 'utf8')).toBe(FIXTURE_CONFIG);
  });

  it('leaves an unfiltered run silent about exclusions', async () => {
    const io = testIo({
      fetchImpl: fakeFetch({
        '/users/me/repos': [{ name: 'alpha', full_name: 'me/alpha', owner: { login: 'me' }, fork: true }],
      }),
    });
    expect(await main(['init', '--provider=github', '--account=me', '--select=all', '--print'], io)).toBe(0);
    expect(io.out.join('\n')).not.toContain('excluded');
    expect(io.out.join('\n')).toContain("repo: 'alpha'");
  });

  it('rejects --web with --print before opening anything', async () => {
    const io = testIo();
    expect(await main(['init', '--web', '--print'], io)).toBe(1);
    expect(io.err.join('\n')).toContain('--web and --print cannot be combined');
  });

  it('documents the selection syntax in the usage text', () => {
    expect(USAGE).toContain(EXCLUDE_HELP);
    expect(USAGE).toContain('all-nf');
    expect(USAGE).toContain('--web');
    expect(USAGE).toContain('--port=<n>');
    expect(USAGE).toContain('--no-open');
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

/* ------------------------------------------------------------------ config safety */

describe('what can and cannot reach a config file', () => {
  const LS = String.fromCharCode(8232); // U+2028, a line terminator to older JS parsers
  const PS = String.fromCharCode(8233); // U+2029

  it('escapes line terminators, not just quotes', () => {
    // A raw newline inside '…' is a syntax error, so an unescaped one leaves the user's config
    // unparseable — the only copy of the original being the .bak.
    expect(renderEntry({ type: 'github', owner: 'o', repo: 'a\nb' })).toBe(
      "{ type: 'github', owner: 'o', repo: 'a\\nb' }",
    );
    expect(renderEntry({ type: 'github', owner: 'o', repo: 'a\rb' })).toContain('a\\rb');
    expect(renderEntry({ type: 'github', owner: 'o', repo: `a${LS}b` })).toContain('a\\u2028b');
    expect(renderEntry({ type: 'github', owner: 'o', repo: `a${PS}b` })).toContain('a\\u2029b');
    for (const rendered of [
      renderEntry({ type: 'github', owner: 'o', repo: 'a\nb' }),
      renderEntry({ type: 'github', owner: 'o', repo: `a${LS}b` }),
    ]) {
      expect(rendered).not.toMatch(/[\n\r]/);
      expect(rendered.includes(LS) || rendered.includes(PS)).toBe(false);
    }
  });

  it('refuses a listing-supplied name that has no business in a config file', () => {
    // The listing is remote input; `entriesFor` is the one place it becomes config text.
    const hostile: RemoteRepo = {
      name: "evil\n    path: '/etc/passwd', slug: 'pwned",
      fullName: 'me/evil',
      owner: 'me',
      description: null,
      private: false,
    };
    expect(() => entriesFor('github', 'https://api.github.com', [hostile], 'provider')).toThrow(
      /repository name that cannot go in a config file/,
    );
    expect(() =>
      entriesFor('gitlab', 'https://gitlab.com', [{ ...hostile, name: 'p', project: "g/p',\n  evil: '" }], 'provider'),
    ).toThrow(/project path that cannot go in a config file/);
  });

  it('refuses a host that could break out of a string literal', () => {
    const plain = repo('me/alpha');
    expect(() => entriesFor('gitea', "https://x.dev/a\nb", [plain], 'provider')).toThrow(/not a usable host/);
    expect(() => entriesFor('gitea', "https://x.dev/'", [plain], 'provider')).toThrow(/not a usable host/);
    expect(() => entriesFor('gitea', 'ftp://x.dev', [plain], 'provider')).toThrow(/http or https/);
    expect(() => entriesFor('gitea', 'https://user:pw@x.dev', [plain], 'provider')).toThrow(/credentials/);
  });

  it('stops a hostile listing before it touches the file', async () => {
    const file = path.join(tmp, 'frznforge.config.ts');
    await fs.writeFile(file, FIXTURE_CONFIG, 'utf8');
    const io = testIo({
      fetchImpl: fakeFetch({
        '/users/me/repos': [{ name: "ok\n    path: '/etc/passwd", full_name: 'me/ok', owner: { login: 'me' } }],
      }),
    });
    expect(await main(['init', '--provider=github', '--account=me', '--select=all', '--yes', `--config=${file}`], io)).toBe(1);
    expect(io.err.join('\n')).toContain('cannot go in a config file');
    expect(await fs.readFile(file, 'utf8')).toBe(FIXTURE_CONFIG);
  });

  it('never lets two backups in the same second overwrite each other', async () => {
    const file = path.join(tmp, 'frznforge.config.ts');
    await fs.writeFile(file, FIXTURE_CONFIG, 'utf8');
    const now = new Date('2026-08-24T05:03:00.000Z');
    const first = await updateConfigFile(file, [{ type: 'github', owner: 'me', repo: 'one' }], { now });
    const second = await updateConfigFile(file, [{ type: 'github', owner: 'me', repo: 'two' }], { now });
    expect(first!.backup).toBe(backupPathFor(file, now));
    expect(second!.backup).not.toBe(first!.backup);
    // The first backup still holds the original: it was not clobbered by the second write.
    expect(await fs.readFile(first!.backup!, 'utf8')).toBe(FIXTURE_CONFIG);
    expect(await fs.readFile(second!.backup!, 'utf8')).toContain("repo: 'one'");
  });
});

/* ------------------------------------------------------------------ flags a listing cannot answer */

describe('listings that do not report every flag', () => {
  /** A GitLab *user* project listing: the simple representation, verified against gitlab.com. */
  const GITLAB_USER_PAGE = [
    { path_with_namespace: 'me/plain', description: null, visibility: 'public' },
    { path_with_namespace: 'me/mirror', description: 'a fork, but the listing does not say so', visibility: 'public' },
  ];

  it('reports fork and archived as unknown for a GitLab user listing', async () => {
    const repos = await listRepos('gitlab', {
      host: 'https://gitlab.com',
      account: 'me',
      fetchImpl: fakeFetch({ '/api/v4/users/me/projects': GITLAB_USER_PAGE }),
    });
    // `false` would mean "not a fork"; these listings simply never say.
    expect(repos.map((r) => r.fork)).toEqual([undefined, undefined]);
    expect(repos.map((r) => r.archived)).toEqual([undefined, undefined]);
    expect(repos.map((r) => r.private)).toEqual([false, false]);
  });

  it('reads archived from a GitLab group listing, which does carry it', async () => {
    const repos = await listRepos('gitlab', {
      host: 'https://gitlab.com',
      account: 'g',
      fetchImpl: fakeFetch({
        '/api/v4/groups/g/projects': [
          { path_with_namespace: 'g/live', visibility: 'public', archived: false },
          { path_with_namespace: 'g/old', visibility: 'public', archived: true },
        ],
      }),
    });
    expect(repos.map((r) => r.archived)).toEqual([false, true]);
    expect(repos.every((r) => r.fork === undefined)).toBe(true);
  });

  it('refuses a filter the listing cannot answer instead of quietly keeping everything', async () => {
    const io = testIo({ fetchImpl: fakeFetch({ '/api/v4/users/me/projects': GITLAB_USER_PAGE }) });
    expect(await main(['init', '--provider=gitlab', '--account=me', '--select=all-nf', '--print'], io)).toBe(1);
    expect(io.err.join('\n')).toContain("'nf' cannot be applied here");
    expect(io.err.join('\n')).toContain('which repositories are forks');
    expect(io.err.join('\n')).toContain('name,name');
  });

  it('still honours the flags the same listing does report', async () => {
    const io = testIo({ fetchImpl: fakeFetch({ '/api/v4/users/me/projects': GITLAB_USER_PAGE }) });
    expect(await main(['init', '--provider=gitlab', '--account=me', '--select=all-np', '--print'], io)).toBe(0);
    expect(io.out.join('\n')).toContain("project: 'me/plain'");
  });

  it('leaves GitHub alone — its listing answers all three', async () => {
    const repos = await listRepos('github', {
      host: 'https://api.github.com',
      account: 'me',
      fetchImpl: fakeFetch({
        '/users/me/repos': [
          { name: 'a', full_name: 'me/a', owner: { login: 'me' } },
          { name: 'b', full_name: 'me/b', owner: { login: 'me' }, fork: true, archived: true, private: true },
        ],
      }),
    });
    expect(repos.map((r) => [r.fork, r.archived, r.private])).toEqual([
      [false, false, false],
      [true, true, true],
    ]);
    expect(ok(resolveSelection(repos, 'all-nfna')).repos.map((r) => r.name)).toEqual(['a']);
  });
});

/* ------------------------------------------------------------------ names that look like filters */

describe('repositories whose name starts with all-', () => {
  const contributors = repo('me/all-contributors');
  const filterish = repo('me/all-nf');
  const plain = repo('me/plain');
  const forked = repo('me/forked', { fork: true });
  const listing = [contributors, filterish, forked, plain];

  it('selects a real repository by name before reading it as a filter', () => {
    expect(ok(resolveSelection(listing, 'all-contributors')).repos).toEqual([contributors]);
    expect(ok(resolveSelection(listing, 'ALL-Contributors')).repos).toEqual([contributors]);
    expect(ok(resolveSelection(listing, 'me/all-contributors')).repos).toEqual([contributors]);
  });

  it('lets a repository literally named all-nf win over the filter grammar', () => {
    const outcome = ok(resolveSelection(listing, 'all-nf'));
    expect(outcome.repos).toEqual([filterish]);
    expect(outcome.filtered).toBe(false);
  });

  it('still reads all-nf as a filter when no repository claims the name', () => {
    const outcome = ok(resolveSelection([plain, forked], 'all-nf'));
    expect(outcome.repos).toEqual([plain]);
    expect(outcome.filtered).toBe(true);
  });

  it('never lets a repo called plain "all" shadow the all keyword', () => {
    const everything = repo('me/all');
    expect(ok(resolveSelection([everything, plain], 'all')).repos).toEqual([everything, plain]);
  });

  it('points at the filter grammar when all is written with a space', () => {
    const result = resolveSelection([plain, forked], 'all -nf');
    expect(result.ok).toBe(false);
    expect(result.ok ? '' : result.error).toContain('did you mean all-nf');
    expect(result.ok ? '' : result.error).toContain('no spaces');
  });

  it('says what is wrong when a filter is mixed into a list', () => {
    const result = resolveSelection([plain, forked], 'all-nf,plain');
    expect(result.ok).toBe(false);
    expect(result.ok ? '' : result.error).toContain('mixes an all-… filter with a list');
  });
});

/* ------------------------------------------------------------------ the interactive loop */

describe('askForSelection', () => {
  const plain = repo('me/plain');
  const forked = repo('me/forked', { fork: true });

  /** Answers the prompt from a script, recording what was asked and printed. */
  function scripted(answers: string[]): {
    ask: (q: string, fallback: string) => Promise<string>;
    write: (line: string) => void;
    asked: number;
    lines: string[];
  } {
    const state = {
      asked: 0,
      lines: [] as string[],
      ask: async (_q: string, fallback: string) => {
        const next = answers[state.asked];
        state.asked += 1;
        if (next === undefined) throw new Error('the loop asked more times than the script answers');
        return next === '' ? fallback : next;
      },
      write: (line: string) => state.lines.push(line),
    };
    return state;
  }

  it('accepts "none" and stops asking', async () => {
    // The prompt advertises `none` in its own syntax line; re-asking made it unanswerable and
    // the only way out of the wizard was Ctrl-C.
    const s = scripted(['none']);
    expect(await askForSelection([plain, forked], s.ask, s.write)).toEqual([]);
    expect(s.asked).toBe(1);
  });

  it('takes the shown [all] default for an empty answer', async () => {
    const s = scripted(['']);
    expect(await askForSelection([plain, forked], s.ask, s.write)).toEqual([plain, forked]);
    expect(s.asked).toBe(1);
  });

  it('re-asks after a spec that could not be parsed', async () => {
    const s = scripted(['all-nx', 'plain']);
    expect(await askForSelection([plain, forked], s.ask, s.write)).toEqual([plain]);
    expect(s.asked).toBe(2);
    expect(s.lines.join('\n')).toContain("unknown filter 'nx'");
  });

  it('re-asks when a filter excluded every repository', async () => {
    const s = scripted(['all-nf', 'none']);
    expect(await askForSelection([forked], s.ask, s.write)).toEqual([]);
    expect(s.asked).toBe(2);
    expect(s.lines.join('\n')).toContain('nothing to add');
  });
});
