#!/usr/bin/env tsx
/**
 * `npm run build` — ingest, then render.
 *
 * It exists as a script rather than the old `npm run ingest && astro build` chain so the two
 * halves can be controlled separately. In a chained npm script every `--` argument lands on
 * the LAST command, so there was no way to say "skip the ingest" or "ingest like this" without
 * running the two commands by hand and remembering which order they go in.
 *
 *   npm run build                        ingest, then render (unchanged default)
 *   npm run build -- --no-ingest         render the artifact already on disk
 *   npm run build -- --backfill-metadata ingest only the missing provider metadata, then render
 *
 * `--no-ingest` is the one to reach for after any command that has already written the
 * artifact — a `--backfill-metadata` run, or an ingest that was interrupted after it wrote —
 * and for iterating on templates, styles or components, none of which the artifact depends on.
 * It never *creates* an artifact, so the first build of a checkout cannot use it; the
 * preflight below says so rather than letting Astro fail with an empty site.
 */
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import { createRequire } from 'node:module';
import { constants as osConstants } from 'node:os';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { PROJECT_ROOT, loadConfig } from '../src/lib/config/index';
import { ARTIFACT_FILENAME } from '../src/lib/data/load';

/** Flags this script consumes itself. */
const OWN_FLAGS = new Set(['--no-ingest']);
/**
 * Flags that belong to ingest. Everything else goes to `astro build`, so
 * `npm run build -- --no-ingest --verbose` sends only `--verbose` onward.
 */
const INGEST_FLAGS = new Set(['--no-cache', '--backfill-metadata']);

export interface BuildArgs {
  /** Skip the ingest step and render whatever artifact is already on disk. */
  noIngest: boolean;
  /** Forwarded verbatim to `scripts/ingest.ts`. */
  ingestArgs: string[];
  /** Forwarded verbatim to `astro build`. */
  astroArgs: string[];
}

/**
 * Split `npm run build -- …` into the three destinations.
 *
 * Unknown flags go to Astro rather than being rejected: this wrapper should never be the
 * reason a valid `astro build` option stops working, and Astro reports its own bad input.
 */
export function parseBuildArgs(argv: string[]): BuildArgs {
  const args: BuildArgs = { noIngest: false, ingestArgs: [], astroArgs: [] };
  for (const a of argv) {
    if (OWN_FLAGS.has(a)) args.noIngest = true;
    else if (INGEST_FLAGS.has(a)) args.ingestArgs.push(a);
    else args.astroArgs.push(a);
  }
  return args;
}

/**
 * `--no-ingest` with ingest flags is a contradiction worth refusing: the caller has asked for
 * an ingest behaviour AND asked for no ingest, so one of the two was a mistake and silently
 * dropping either would be the wrong guess.
 */
export function conflictMessage(args: BuildArgs): string | null {
  if (!args.noIngest || args.ingestArgs.length === 0) return null;
  return (
    `frznforge build: --no-ingest cannot be combined with ${args.ingestArgs.join(' ')} — ` +
    'those configure an ingest that --no-ingest skips. Drop one.'
  );
}

/** Project-relative, forward-slashed — the form a reader can paste into a shell. */
function rel(root: string, target: string): string {
  const r = path.relative(root, target).replace(/\\/g, '/');
  return r === '' || r.startsWith('..') ? target.replace(/\\/g, '/') : r;
}

/**
 * `--no-ingest` renders the artifact on disk, so there has to be one. Without this check
 * Astro builds a valid, EMPTY site (`loadForgeData` warns and returns an empty artifact) —
 * a success that quietly replaces a good `dist/` with a site containing no repos.
 */
export function noIngestPreflight(
  root: string,
  artifactFile: string,
  exists: (p: string) => boolean,
): { ok: boolean; lines: string[] } {
  if (exists(artifactFile)) return { ok: true, lines: [] };
  return {
    ok: false,
    lines: [
      'frznforge build: --no-ingest needs an artifact to render, and there is none yet.',
      '',
      `  missing: ${rel(root, artifactFile)}`,
      '',
      '  Run the ingest once first:',
      '',
      '    npm run build       ingest, then render',
      '',
      '  After that, --no-ingest re-renders that artifact as often as you like.',
    ],
  };
}

/** Absolute path to a dependency's CLI entry, resolved through the installed package. */
function binOf(root: string, pkg: string, fallback: string, name?: string): string {
  const require_ = createRequire(path.join(root, 'noop.js'));
  const pkgPath = require_.resolve(`${pkg}/package.json`);
  const bin = (JSON.parse(fs.readFileSync(pkgPath, 'utf8')) as { bin?: Record<string, string> | string }).bin;
  const entry =
    typeof bin === 'string' ? bin : (bin?.[name ?? pkg] ?? Object.values(bin ?? {})[0] ?? fallback);
  return path.resolve(path.dirname(pkgPath), entry);
}

export interface BuildIo {
  log: (line: string) => void;
  error: (line: string) => void;
  exists: (absPath: string) => boolean;
}

/**
 * Run one child to completion, inheriting stdio. Spawned through node with the package's own
 * entry file rather than a shim on PATH — on Windows the shims are `.cmd` files that modern
 * Node refuses to spawn without a shell (the same reason `scripts/dev.ts` does this).
 */
function run(root: string, argv: string[], io: BuildIo, label: string): Promise<number> {
  return new Promise<number>((resolve) => {
    const child = spawn(process.execPath, argv, { cwd: root, stdio: 'inherit' });
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
      io.error(`frznforge build: could not start ${label}: ${e.message}`);
      done(1);
    });
    child.on('close', (code, signal) => {
      if (signal) return done(128 + ((osConstants.signals as Record<string, number>)[signal] ?? 0));
      done(code ?? 0);
    });
  });
}

export async function main(argv: string[], io: BuildIo): Promise<number> {
  const args = parseBuildArgs(argv);
  const conflict = conflictMessage(args);
  if (conflict) {
    io.error(conflict);
    return 1;
  }

  const root = PROJECT_ROOT;
  let outDir = path.join(root, 'data');
  try {
    outDir = (await loadConfig()).outDir;
  } catch {
    /* a config that will not load is reported properly by ingest; fall back to the default */
  }
  const artifactFile = path.join(outDir, ARTIFACT_FILENAME);

  if (args.noIngest) {
    const check = noIngestPreflight(root, artifactFile, io.exists);
    if (!check.ok) {
      for (const line of check.lines) io.error(line);
      return 1;
    }
    io.log(`frznforge build: --no-ingest — rendering ${rel(root, artifactFile)} as it stands; no repo is fetched or scanned.`);
  } else {
    const code = await run(
      root,
      [binOf(root, 'tsx', './dist/cli.mjs'), path.join(root, 'scripts', 'ingest.ts'), ...args.ingestArgs],
      io,
      'the ingest step',
    );
    // Ingest failing means the artifact is missing or stale, so rendering it would publish
    // something nobody asked for. Stop with ingest's own exit code.
    if (code !== 0) return code;
  }

  return await run(
    root,
    [binOf(root, 'astro', './bin/astro.mjs'), 'build', ...args.astroArgs],
    io,
    'astro build',
  );
}

const invokedDirectly =
  typeof process.argv[1] === 'string' && pathToFileURL(process.argv[1]).href === import.meta.url;
if (invokedDirectly) {
  const io: BuildIo = {
    log: (line) => console.log(line),
    error: (line) => console.error(line),
    exists: (p) => fs.existsSync(p),
  };
  void main(process.argv.slice(2), io).then((code) => {
    process.exitCode = code;
  });
}
