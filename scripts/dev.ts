#!/usr/bin/env tsx
/**
 * `npm run dev` — serve the site that the most recent `npm run build` produced.
 *
 * This is deliberately **not** `astro dev`. A frznforge page reads `data/forge.json` through
 * `loadForgeData`, which memoises the artifact for the lifetime of the process
 * (`src/lib/data/load.ts:13-28`), so an Astro dev server keeps serving whatever artifact
 * existed when it started: re-run `npm run ingest` and the new pages 404 until you restart it.
 * A dev server that cannot pick up the data it exists to display is worse than no dev server,
 * so `dev` now says plainly where its content comes from and runs `astro preview` over `dist/`.
 *
 * The raw Astro dev server is still one command away — `npm run astro dev` — for anyone who
 * wants HMR on components and styles and accepts the stale artifact that comes with it.
 *
 * Guard rail: `astro preview` on a project that has never been built fails with a message
 * about a missing directory that says nothing about what to do. Both inputs are checked here
 * first, and a missing one prints the command to run and exits non-zero without spawning
 * anything.
 */
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import { constants as osConstants } from 'node:os';
import path from 'node:path';
import { createRequire } from 'node:module';
import { pathToFileURL } from 'node:url';
import { PROJECT_ROOT, loadConfig } from '../src/lib/config/index';
import { ARTIFACT_FILENAME } from '../src/lib/data/load';

/** Astro's own default; nothing in `astro.config.ts` overrides it. */
export const DIST_DIRNAME = 'dist';

export interface DevIo {
  log: (line: string) => void;
  error: (line: string) => void;
  /** Injected so the preflight can be tested without a filesystem. */
  exists: (absPath: string) => boolean;
}

export interface PreflightInput {
  /** Absolute project root — everything is reported relative to it. */
  root: string;
  /** Absolute `dist/` (what `astro preview` serves). */
  distDir: string;
  /** Absolute `<ingest.outDir>/forge.json` (what the pages were rendered from). */
  artifactFile: string;
}

/** Project-relative, forward-slashed — the form a reader can paste into a shell. */
function rel(root: string, target: string): string {
  const r = path.relative(root, target).replace(/\\/g, '/');
  return r === '' || r.startsWith('..') ? target.replace(/\\/g, '/') : r;
}

/**
 * The notice, printed before the server starts on every run.
 *
 * Its whole job is to stop the reader wondering why an edit did not show up: this server
 * renders nothing, and the one command that does is named twice.
 */
export function noticeLines(input: PreflightInput): string[] {
  const dist = rel(input.root, input.distDir);
  const artifact = rel(input.root, input.artifactFile);
  return [
    `frznforge dev — serving ${dist}/ from the most recent \`npm run build\`.`,
    '',
    `  Nothing is rebuilt here. This is \`astro preview\` over the static files already in`,
    `  ${dist}/, rendered from ${artifact} as it stood at that build. Editing a page,`,
    `  a component, a style, content/ or frznforge.config.ts changes nothing you see until`,
    '  you build again — file changes are not watched.',
    '',
    '    npm run build       refresh everything (ingest → data/forge.json → astro build → dist/)',
    '    npm run astro dev   the raw Astro dev server, if you want HMR on components and',
    '                        styles — it reads the artifact once at startup and never again,',
    '                        so re-ingested repos will 404 there until you restart it.',
    '',
  ];
}

/**
 * Check the two things `astro preview` silently depends on. Returns the missing ones (in the
 * order a reader would fix them) plus the lines to print; `ok` is the exit decision.
 */
export function preflight(
  input: PreflightInput,
  exists: (absPath: string) => boolean,
): { ok: boolean; missing: string[]; lines: string[] } {
  const missing: string[] = [];
  if (!exists(input.artifactFile)) missing.push(rel(input.root, input.artifactFile));
  if (!exists(input.distDir)) missing.push(rel(input.root, input.distDir));
  if (missing.length === 0) return { ok: true, missing, lines: [] };

  const lines = [
    'frznforge dev: there is no built site to serve yet.',
    '',
    ...missing.map((m) => `  missing: ${m}`),
    '',
    '  Run the build first — it does both halves:',
    '',
    '    npm run build       ingest (git → data/forge.json) then astro build → dist/',
    '',
    '  Then `npm run dev` again.',
  ];
  return { ok: false, missing, lines };
}

/**
 * The argv handed to Astro. Everything after `npm run dev --` is passed straight through, so
 * `npm run dev -- --port 4400 --host` works exactly as it does for `astro preview`.
 */
export function previewArgv(extra: string[]): string[] {
  return ['preview', ...extra];
}

/** Absolute path to Astro's CLI entry, resolved through the installed package. */
function astroBin(root: string): string {
  const require_ = createRequire(path.join(root, 'noop.js'));
  const pkgPath = require_.resolve('astro/package.json');
  const bin = (JSON.parse(fs.readFileSync(pkgPath, 'utf8')) as { bin?: Record<string, string> | string }).bin;
  const entry = typeof bin === 'string' ? bin : (bin?.astro ?? './bin/astro.mjs');
  return path.resolve(path.dirname(pkgPath), entry);
}

/**
 * Resolve where the artifact lives. `ingest.outDir` is configurable (and overridable with
 * `FRZNFORGE_OUT_DIR`), so the guard must ask the config rather than hard-code `data/`. A
 * config that will not load is not this script's problem to diagnose — `npm run build` reports
 * it properly — so fall back to the documented default and let the build speak.
 */
async function resolvePaths(): Promise<PreflightInput> {
  const root = PROJECT_ROOT;
  let outDir = path.join(root, 'data');
  try {
    outDir = (await loadConfig()).outDir;
  } catch {
    /* fall back to the default; the missing-artifact message is still actionable */
  }
  return { root, distDir: path.join(root, DIST_DIRNAME), artifactFile: path.join(outDir, ARTIFACT_FILENAME) };
}

export async function main(argv: string[], io: DevIo): Promise<number> {
  const paths = await resolvePaths();

  const check = preflight(paths, io.exists);
  if (!check.ok) {
    for (const line of check.lines) io.error(line);
    return 1;
  }
  for (const line of noticeLines(paths)) io.log(line);

  return await new Promise<number>((resolve) => {
    // Spawned through node with Astro's own entry file rather than the `astro` shim: the shim
    // is only on PATH when npm put it there, and on Windows it is a `.cmd` that modern Node
    // refuses to spawn without a shell.
    const child = spawn(process.execPath, [astroBin(paths.root), ...previewArgv(argv)], {
      cwd: paths.root,
      stdio: 'inherit',
    });
    // Ctrl-C already reaches the child (it shares the terminal's process group), but taking
    // the signal here too keeps the parent alive until the child has actually exited, so its
    // status — not ours — is what the shell sees.
    const forward = (signal: NodeJS.Signals) => () => {
      if (child.exitCode === null && child.signalCode === null) child.kill(signal);
    };
    const onInt = forward('SIGINT');
    const onTerm = forward('SIGTERM');
    process.on('SIGINT', onInt);
    process.on('SIGTERM', onTerm);
    const done = (code: number) => {
      process.off('SIGINT', onInt);
      process.off('SIGTERM', onTerm);
      resolve(code);
    };
    child.on('error', (e) => {
      io.error(`frznforge dev: could not start astro preview: ${e.message}`);
      done(1);
    });
    child.on('close', (code, signal) => {
      // Shells report a signalled child as 128 + signum; mirror that so a Ctrl-C out of
      // preview is indistinguishable from a Ctrl-C out of `npm run preview`.
      if (signal) return done(128 + ((osConstants.signals as Record<string, number>)[signal] ?? 0));
      done(code ?? 0);
    });
  });
}

const invokedDirectly =
  typeof process.argv[1] === 'string' && pathToFileURL(process.argv[1]).href === import.meta.url;
if (invokedDirectly) {
  const io: DevIo = {
    log: (line) => console.log(line),
    error: (line) => console.error(line),
    exists: (p) => fs.existsSync(p),
  };
  void main(process.argv.slice(2), io).then((code) => {
    process.exitCode = code;
  });
}
