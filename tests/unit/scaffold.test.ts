/**
 * `npm run frznforge -- new <dir>` — the scaffold.
 *
 * Two things are worth more than the rest of this file: the generated `frznforge.config.ts` is
 * evaluated and parsed with the **real** `FrznforgeConfigSchema`, and the generated
 * `content/profile.md` frontmatter is parsed with the **real** `ProfileFrontmatter` from
 * `src/content.config.ts`. Copies of those schemas would pass forever while the scaffold rotted.
 *
 * `astro:content` is a virtual module that only exists inside an Astro build, so importing the
 * content config here needs the one-line stub below; everything else in that file (`astro/zod`,
 * `astro/loaders`, the project config) is real.
 *
 * The end-to-end proof — scaffold, drop the engine in beside it, ingest, build — is real but
 * costs a few seconds and a copy of `src/`, so it is opt-in:
 *
 *     FRZNFORGE_SCAFFOLD_BUILD=1 npx vitest run tests/unit/scaffold.test.ts
 *
 * Nothing here touches the network, and every write happens under `os.tmpdir()`.
 */
import { spawnSync } from 'node:child_process';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { FrznforgeConfigSchema } from '../../src/lib/config/schema';
import { parseFrontmatter } from '../../src/lib/frontmatter';
import { main, type Io } from '../../scripts/cli';
import {
  ScaffoldError,
  nextSteps,
  scaffold,
  scaffoldFiles,
  runScaffold,
} from '../../scripts/lib/scaffold';

// `src/content.config.ts` imports `astro:content`, which Astro injects at build time and Vitest
// cannot resolve. Only `defineCollection` is called at module scope, and only to build a value
// this test never looks at.
vi.mock('astro:content', () => ({ defineCollection: (collection: unknown) => collection }));

const REPO_ROOT = path.resolve(__dirname, '..', '..');

/* ------------------------------------------------------------------ helpers */

let tmp: string;

beforeEach(async () => {
  tmp = await fs.mkdtemp(path.join(os.tmpdir(), 'frznforge-scaffold-'));
});

afterEach(async () => {
  await fs.rm(tmp, { recursive: true, force: true });
});

function fakeIo(cwd: string): Io & { out: string[]; err: string[] } {
  const out: string[] = [];
  const err: string[] = [];
  return {
    out,
    err,
    log: (line) => out.push(line),
    error: (line) => err.push(line),
    isTty: false,
    env: {},
    cwd,
  };
}

/** Every file under `dir`, as POSIX-separated paths relative to it, sorted. */
async function listTree(dir: string): Promise<string[]> {
  const found: string[] = [];
  async function walk(current: string, prefix: string): Promise<void> {
    for (const entry of await fs.readdir(current, { withFileTypes: true })) {
      const rel = prefix ? `${prefix}/${entry.name}` : entry.name;
      if (entry.isDirectory()) await walk(path.join(current, entry.name), rel);
      else found.push(rel);
    }
  }
  await walk(dir, '');
  return found.sort();
}

const EXPECTED_TREE = [
  '.gitignore',
  'README.md',
  'content/notes/welcome.md',
  'content/orgs/example-org.md.example',
  'content/profile.md',
  'frznforge.config.ts',
].sort();

/**
 * Evaluate the object literal the generated config passes to `defineConfig`.
 *
 * `defineConfig` is the identity function, so the literal *is* the config — and evaluating it
 * this way proves it is syntactically valid JS as a side effect. Importing the file instead
 * would mean resolving its `./src/lib/config/schema` import from a temp directory, which says
 * nothing extra about the config and a lot about module resolution.
 */
function evaluateConfig(source: string): unknown {
  const open = source.indexOf('defineConfig(');
  const close = source.lastIndexOf(');');
  expect(open, 'generated config calls defineConfig(...)').toBeGreaterThan(-1);
  expect(close).toBeGreaterThan(open);
  const literal = source.slice(open + 'defineConfig('.length, close);
  return new Function(`return (${literal});`)() as unknown;
}

/* ------------------------------------------------------------------ the file set */

describe('scaffoldFiles', () => {
  it('is the tree a new site starts with, and nothing else', () => {
    expect(scaffoldFiles().map((f) => f.path).sort()).toEqual(EXPECTED_TREE);
  });

  it('writes LF text files that end in a newline and have unique paths', () => {
    const files = scaffoldFiles();
    expect(new Set(files.map((f) => f.path)).size).toBe(files.length);
    for (const file of files) {
      expect(file.contents.includes('\r'), `${file.path} has CR characters`).toBe(false);
      expect(file.contents.endsWith('\n'), `${file.path} has no trailing newline`).toBe(true);
      expect(file.purpose.length, `${file.path} has no purpose line`).toBeGreaterThan(0);
    }
  });

  it('gitignores everything the build regenerates', () => {
    const gitignore = scaffoldFiles().find((f) => f.path === '.gitignore')!.contents;
    for (const entry of ['/data/', '/dist/', '/.frznforge-cache/', 'node_modules/']) {
      expect(gitignore).toContain(entry);
    }
  });

  it("gives the user a README of their own with the three commands", () => {
    const readme = scaffoldFiles().find((f) => f.path === 'README.md')!.contents;
    for (const command of ['npm install', 'npm run dev', 'npm run build']) {
      expect(readme).toContain(command);
    }
    // Their site's README, not this project's: it must name the files they edit.
    expect(readme).toContain('frznforge.config.ts');
    expect(readme).toContain('content/profile.md');
  });
});

/* ------------------------------------------------------------------ generated config */

describe('the generated frznforge.config.ts', () => {
  const source = () => scaffoldFiles().find((f) => f.path === 'frznforge.config.ts')!.contents;

  it('parses under the real FrznforgeConfigSchema', () => {
    const parsed = FrznforgeConfigSchema.parse(evaluateConfig(source()));
    expect(parsed.owner.handle).toMatch(/^[a-z0-9][a-z0-9-]*$/);
    expect(parsed.repos).toEqual([]); // every example is commented out
    expect(parsed.theme.palette).toBe('hearth');
    expect(parsed.ingest.outDir).toBe('./data');
    expect(parsed.notes.useMtime).toBe(false); // determinism is the default a new site gets
  });

  it('imports defineConfig from the engine, exactly like the reference config does', () => {
    expect(source()).toContain("import { defineConfig } from './src/lib/config/schema';");
  });

  it('carries one commented example of every source type', () => {
    const text = source();
    for (const type of ['local', 'github', 'gitlab', 'gitea', 'forgejo']) {
      expect(text, `no ${type} example`).toMatch(new RegExp(`^\\s*//.*type: '${type}'`, 'm'));
    }
  });

  it('never writes a token into the config it generates', () => {
    // Tokens are an environment concern; a scaffold that suggested otherwise would teach the
    // wrong habit on day one.
    expect(source()).not.toMatch(/token\s*:/i);
    expect(source()).toContain('read from the environment only');
  });
});

/* ------------------------------------------------------------------ generated markdown */

describe('the generated markdown', () => {
  it('has profile frontmatter that satisfies the real ProfileFrontmatter schema', async () => {
    const { ProfileFrontmatter } = await import('../../src/content.config');
    const md = scaffoldFiles().find((f) => f.path === 'content/profile.md')!.contents;
    const { data, body } = parseFrontmatter(md);

    const parsed = ProfileFrontmatter.parse(data);
    expect(parsed.bio).toBeTruthy();
    expect(parsed.sites.length).toBeGreaterThan(0);
    expect(parsed.pinned).toEqual([]);
    expect(parsed.identities).toEqual([]);
    // The body is the half a new owner is meant to rewrite, so it has to be there.
    expect(body).toContain('# Hi, I');
  });

  it('has an org example that satisfies the real OrgFrontmatter schema', async () => {
    const { OrgFrontmatter } = await import('../../src/content.config');
    const md = scaffoldFiles().find((f) => f.path === 'content/orgs/example-org.md.example')!.contents;
    // Frontmatter first, or Astro would not see it as frontmatter at all once the file is
    // renamed — the "how to use this" block therefore lives in the body.
    expect(md.startsWith('---\n')).toBe(true);
    const parsed = OrgFrontmatter.parse(parseFrontmatter(md).data);
    expect(parsed.description).toBeTruthy();

    // `.md.example`, so the orgs glob (`*.md`) cannot pick it up and a fresh site ships no
    // organization it never asked for.
    expect(path.extname('example-org.md.example')).not.toBe('.md');
  });

  it('has an example note with the frontmatter the notes ingest reads', () => {
    const md = scaffoldFiles().find((f) => f.path === 'content/notes/welcome.md')!.contents;
    const { data } = parseFrontmatter(md);
    expect(data.title).toBeTruthy();
    expect(data.description).toBeTruthy();
    expect(data.date).toMatch(/^\d{4}-\d{2}-\d{2}$/); // fixed, not a clock: rebuilds stay identical
    expect(data.tags).toEqual(['frznforge']);
  });
});

/* ------------------------------------------------------------------ writing */

describe('scaffold', () => {
  it('creates the directory and writes the whole tree', async () => {
    const dir = path.join(tmp, 'fresh');
    const result = await scaffold({ dir });

    expect(result.createdDir).toBe(true);
    expect(result.dryRun).toBe(false);
    expect(result.written.sort()).toEqual(EXPECTED_TREE);
    expect(result.kept).toEqual([]);
    expect(await listTree(dir)).toEqual(EXPECTED_TREE);

    // What is on disk is byte-for-byte what the pure file set said it would be.
    for (const file of scaffoldFiles()) {
      expect(await fs.readFile(path.join(dir, ...file.path.split('/')), 'utf8')).toBe(file.contents);
    }
  });

  it('writes into an existing but empty directory', async () => {
    const dir = path.join(tmp, 'empty');
    await fs.mkdir(dir);
    const result = await scaffold({ dir });
    expect(result.createdDir).toBe(false);
    expect(result.written.sort()).toEqual(EXPECTED_TREE);
  });

  it('treats a directory holding only .git as empty', async () => {
    const dir = path.join(tmp, 'git-init');
    await fs.mkdir(path.join(dir, '.git'), { recursive: true });
    await expect(scaffold({ dir })).resolves.toMatchObject({ kept: [] });
  });

  it('refuses a non-empty directory without --force', async () => {
    const dir = path.join(tmp, 'busy');
    await fs.mkdir(dir);
    await fs.writeFile(path.join(dir, 'notes.txt'), 'mine\n');

    await expect(scaffold({ dir })).rejects.toBeInstanceOf(ScaffoldError);
    await expect(scaffold({ dir })).rejects.toThrow(/not empty.*--force/s);
    // The refusal is total: not one file was written before it gave up.
    expect(await listTree(dir)).toEqual(['notes.txt']);
  });

  it('refuses a target that exists and is not a directory', async () => {
    const file = path.join(tmp, 'a-file');
    await fs.writeFile(file, 'x');
    await expect(scaffold({ dir: file })).rejects.toThrow(/not a directory/);
  });

  it('--force writes into a non-empty directory without touching what is there', async () => {
    const dir = path.join(tmp, 'busy');
    await fs.mkdir(dir);
    await fs.writeFile(path.join(dir, 'notes.txt'), 'mine\n');
    await fs.writeFile(path.join(dir, 'README.md'), '# my own readme\n');

    const result = await scaffold({ dir, force: true });
    expect(result.kept).toEqual(['README.md']);
    expect(result.written.sort()).toEqual(EXPECTED_TREE.filter((p) => p !== 'README.md'));
    expect(await fs.readFile(path.join(dir, 'README.md'), 'utf8')).toBe('# my own readme\n');
    expect(await fs.readFile(path.join(dir, 'notes.txt'), 'utf8')).toBe('mine\n');
  });

  it('--dry-run writes nothing at all, not even the directory', async () => {
    const dir = path.join(tmp, 'never');
    const result = await scaffold({ dir, dryRun: true });

    expect(result.written).toEqual([]);
    expect(result.planned.sort()).toEqual(EXPECTED_TREE);
    expect(result.createdDir).toBe(true); // "would be created"
    await expect(fs.stat(dir)).rejects.toMatchObject({ code: 'ENOENT' });
  });

  it('--dry-run still reports the files it would leave alone', async () => {
    const dir = path.join(tmp, 'partial');
    await fs.mkdir(dir);
    await fs.writeFile(path.join(dir, '.gitignore'), 'dist\n');

    const result = await scaffold({ dir, force: true, dryRun: true });
    expect(result.kept).toEqual(['.gitignore']);
    expect(result.planned).not.toContain('.gitignore');
    expect(await listTree(dir)).toEqual(['.gitignore']);
  });

  it('is safe to run twice: the second run changes nothing', async () => {
    const dir = path.join(tmp, 'twice');
    await scaffold({ dir });
    const before = new Map<string, string>();
    for (const p of await listTree(dir)) before.set(p, await fs.readFile(path.join(dir, p), 'utf8'));
    // Edits survive a re-run — the whole point of never overwriting.
    await fs.writeFile(path.join(dir, 'content', 'profile.md'), '---\nbio: mine\n---\n');

    const second = await scaffold({ dir, force: true });
    expect(second.written).toEqual([]);
    expect(second.kept.sort()).toEqual(EXPECTED_TREE);
    expect(await fs.readFile(path.join(dir, 'content', 'profile.md'), 'utf8')).toBe('---\nbio: mine\n---\n');
    for (const [p, contents] of before) {
      if (p === 'content/profile.md') continue;
      expect(await fs.readFile(path.join(dir, p), 'utf8')).toBe(contents);
    }
  });
});

/* ------------------------------------------------------------------ reporting */

describe('reporting', () => {
  it('names the exact files to edit', async () => {
    const dir = path.join(tmp, 'steps');
    const result = await scaffold({ dir });
    const lines = nextSteps(result, tmp).join('\n');
    expect(lines).toContain(path.join('steps', 'frznforge.config.ts'));
    expect(lines).toContain(path.join('steps', 'content', 'profile.md'));
    expect(lines).toContain('repos: [');
    expect(lines).toContain('npm run build');
  });

  it('drops the "copy the engine in" step when the engine is already there', async () => {
    const dir = path.join(tmp, 'in-a-checkout');
    await fs.mkdir(path.join(dir, 'src', 'lib', 'config'), { recursive: true });
    await fs.writeFile(path.join(dir, 'src', 'lib', 'config', 'schema.ts'), '// pretend engine\n');

    const result = await scaffold({ dir, force: true });
    expect(result.engineReady).toBe(true);
    const lines = nextSteps(result, tmp);
    expect(lines.join('\n')).not.toContain('Put the frznforge engine');
    expect(lines[1]).toMatch(/^ {2}1\. Edit /); // renumbered, not left with a gap
  });

  it('prints every written path, then the next steps', async () => {
    const out: string[] = [];
    await runScaffold({ dir: path.join(tmp, 'printed'), cwd: tmp }, (line) => out.push(line));
    const text = out.join('\n');
    for (const file of scaffoldFiles()) expect(text).toContain(`+ ${file.path}`);
    expect(text).toContain('Next steps');
  });

  it('a dry run says so before it says anything else', async () => {
    const out: string[] = [];
    await runScaffold({ dir: path.join(tmp, 'dry'), dryRun: true, cwd: tmp }, (line) => out.push(line));
    expect(out[0]).toMatch(/^Dry run/);
    expect(out.join('\n')).toContain('Re-run without --dry-run');
  });
});

/* ------------------------------------------------------------------ the CLI */

describe('frznforge new', () => {
  it('scaffolds through main() and reports success', async () => {
    const io = fakeIo(tmp);
    const code = await main(['new', 'my-site'], io);
    expect(code).toBe(0);
    expect(io.err).toEqual([]);
    expect(await listTree(path.join(tmp, 'my-site'))).toEqual(EXPECTED_TREE);
  });

  it('resolves the directory against the caller cwd, not the process cwd', async () => {
    const io = fakeIo(tmp);
    expect(await main(['new', './nested/site'], io)).toBe(0);
    expect(await listTree(path.join(tmp, 'nested', 'site'))).toEqual(EXPECTED_TREE);
  });

  it('accepts --dry-run and --force', async () => {
    const dry = fakeIo(tmp);
    expect(await main(['new', 'peek', '--dry-run'], dry)).toBe(0);
    await expect(fs.stat(path.join(tmp, 'peek'))).rejects.toMatchObject({ code: 'ENOENT' });

    const dir = path.join(tmp, 'occupied');
    await fs.mkdir(dir);
    await fs.writeFile(path.join(dir, 'mine.txt'), 'x');
    const refused = fakeIo(tmp);
    expect(await main(['new', 'occupied'], refused)).toBe(1);
    expect(refused.err.join('\n')).toContain('--force');

    const forced = fakeIo(tmp);
    expect(await main(['new', 'occupied', '--force'], forced)).toBe(0);
    expect(await listTree(dir)).toEqual([...EXPECTED_TREE, 'mine.txt'].sort());
  });

  it('rejects flags that belong to the other command instead of ignoring them', async () => {
    const wrongWay = fakeIo(tmp);
    expect(await main(['init', '--dry-run'], wrongWay)).toBe(1);
    expect(wrongWay.err.join('\n')).toContain('--dry-run is an option of new');

    const otherWay = fakeIo(tmp);
    expect(await main(['new', 'x', '--print'], otherWay)).toBe(1);
    expect(otherWay.err.join('\n')).toContain('--print is an init option');
    await expect(fs.stat(path.join(tmp, 'x'))).rejects.toMatchObject({ code: 'ENOENT' });
  });

  it('asks for a directory when none is given', async () => {
    const io = fakeIo(tmp);
    expect(await main(['new'], io)).toBe(1);
    expect(io.err[0]).toContain('new needs a directory');
  });

  it('still rejects a stray argument after a command that takes none', async () => {
    const io = fakeIo(tmp);
    expect(await main(['init', 'extra'], io)).toBe(1);
    expect(io.err.join('\n')).toContain('unexpected argument: extra');
  });

  it('documents new in --help', async () => {
    const io = fakeIo(tmp);
    expect(await main(['--help'], io)).toBe(0);
    const usage = io.out.join('\n');
    expect(usage).toContain('new <dir>');
    expect(usage).toContain('--dry-run');
    expect(usage).toContain('starting-a-site.md');
  });
});

/* ------------------------------------------------------------------ the real thing */

/**
 * The whole point of the scaffold: a directory it wrote, plus the engine, really builds.
 *
 * Opt-in (`FRZNFORGE_SCAFFOLD_BUILD=1`) because it copies `src/`, links `node_modules` and runs
 * two child processes — a few seconds that the rest of the unit suite should not pay on every
 * run. `node_modules/.astro` is deliberately *not* linked: it is Astro's content-layer cache,
 * and reusing this repo's would let the reference site's content leak into the fresh one and
 * make the build look like it worked when it had not.
 */
const buildProof = process.env.FRZNFORGE_SCAFFOLD_BUILD === '1';

describe.runIf(buildProof)('a scaffolded directory really builds', () => {
  it('ingests and builds a site with no repositories configured', async () => {
    const dir = path.join(tmp, 'real-site');
    await scaffold({ dir });

    for (const name of ['src', 'scripts', 'public']) {
      await fs.cp(path.join(REPO_ROOT, name), path.join(dir, name), { recursive: true });
    }
    for (const name of ['package.json', 'astro.config.mjs', 'svelte.config.js', 'tsconfig.json']) {
      await fs.copyFile(path.join(REPO_ROOT, name), path.join(dir, name));
    }
    const modules = path.join(dir, 'node_modules');
    await fs.mkdir(modules);
    for (const entry of await fs.readdir(path.join(REPO_ROOT, 'node_modules'), { withFileTypes: true })) {
      if (entry.name === '.astro') continue;
      const from = path.join(REPO_ROOT, 'node_modules', entry.name);
      const to = path.join(modules, entry.name);
      if (entry.isDirectory()) await fs.symlink(from, to, 'junction');
      else await fs.copyFile(from, to);
    }

    // node + the CLI's own entry point rather than npx: no shell quoting, no PATH lookup, and
    // no chance of npx deciding to fetch something from the network mid-test.
    const run = (cli: string, args: string[]) =>
      spawnSync(process.execPath, [path.join(modules, ...cli.split('/')), ...args], {
        cwd: dir,
        encoding: 'utf8',
      });

    const ingest = run('tsx/dist/cli.mjs', ['scripts/ingest.ts']);
    expect(ingest.stderr ?? '').not.toMatch(/Error/);
    expect(ingest.status, ingest.stdout + ingest.stderr).toBe(0);
    expect(ingest.stdout).toContain('1 note(s)');
    const artifact = JSON.parse(await fs.readFile(path.join(dir, 'data', 'forge.json'), 'utf8'));
    expect(artifact.repos).toEqual([]);
    expect(artifact.notes).toHaveLength(1);
    expect(artifact.warnings).toEqual([]);

    const build = run('astro/bin/astro.mjs', ['build']);
    expect(build.status, build.stdout + build.stderr).toBe(0);
    for (const page of ['index.html', 'repos/index.html', 'notes/welcome/index.html']) {
      await expect(fs.stat(path.join(dir, 'dist', ...page.split('/')))).resolves.toBeTruthy();
    }
    // The profile page really rendered the scaffolded markdown, not a placeholder.
    const home = await fs.readFile(path.join(dir, 'dist', 'index.html'), 'utf8');
    expect(home).toContain('Your Name');
  }, 300_000);
});
