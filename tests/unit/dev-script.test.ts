/**
 * `npm run dev` (scripts/dev.ts) — the guard that runs before `astro preview` is spawned.
 *
 * The bug this replaces was `astro preview` failing with a message about a missing directory
 * on a project that had simply never been built, so the cases that matter are: which inputs
 * are checked, that the guidance names the command to run, and that nothing the caller typed
 * after `--` is dropped on the way to Astro.
 */
import { describe, expect, it } from 'vitest';
import path from 'node:path';
import { noticeLines, preflight, previewArgv, type PreflightInput } from '../../scripts/dev';

const ROOT = path.resolve('/project');
const INPUT: PreflightInput = {
  root: ROOT,
  distDir: path.join(ROOT, 'dist'),
  artifactFile: path.join(ROOT, 'data', 'forge.json'),
};

/** `exists` that answers true for exactly the given absolute paths. */
const only = (...present: string[]) => (p: string) => present.includes(p);

describe('preflight', () => {
  it('passes when both the artifact and dist/ are there', () => {
    const r = preflight(INPUT, only(INPUT.distDir, INPUT.artifactFile));
    expect(r.ok).toBe(true);
    expect(r.missing).toEqual([]);
    expect(r.lines).toEqual([]);
  });

  it('reports a missing dist/ and says what to run', () => {
    const r = preflight(INPUT, only(INPUT.artifactFile));
    expect(r.ok).toBe(false);
    expect(r.missing).toEqual(['dist']);
    const text = r.lines.join('\n');
    expect(text).toContain('missing: dist');
    expect(text).not.toContain('missing: data/forge.json');
    expect(text).toContain('npm run build');
  });

  it('reports a missing artifact even when dist/ is present', () => {
    const r = preflight(INPUT, only(INPUT.distDir));
    expect(r.ok).toBe(false);
    expect(r.missing).toEqual(['data/forge.json']);
    expect(r.lines.join('\n')).toContain('npm run build');
  });

  it('lists both when nothing has been built, artifact first', () => {
    const r = preflight(INPUT, only());
    expect(r.ok).toBe(false);
    expect(r.missing).toEqual(['data/forge.json', 'dist']);
  });

  it('reports paths relative to the project root, forward-slashed', () => {
    const elsewhere: PreflightInput = { ...INPUT, artifactFile: path.resolve('/other/data/forge.json') };
    const r = preflight(elsewhere, only(elsewhere.distDir));
    // Outside the root there is nothing to be relative to, so the absolute path is shown —
    // but it is still forward-slashed, and it is still a path the reader can act on.
    expect(r.missing[0]).toBe(path.resolve('/other/data/forge.json').replace(/\\/g, '/'));
    expect(r.missing[0]).toContain('/other/data/forge.json');
  });
});

describe('noticeLines', () => {
  it('says the content comes from the last build and names the refresh command', () => {
    const text = noticeLines(INPUT).join('\n');
    expect(text).toContain('most recent `npm run build`');
    expect(text).toContain('Nothing is rebuilt here');
    expect(text).toContain('npm run build');
    // The raw dev server stays reachable, and the notice has to say so — that is the escape
    // hatch for anyone who wanted HMR.
    expect(text).toContain('npm run astro dev');
  });

  it('names the configured artifact path, not a hard-coded data/', () => {
    const custom: PreflightInput = { ...INPUT, artifactFile: path.join(ROOT, 'artifact', 'forge.json') };
    expect(noticeLines(custom).join('\n')).toContain('artifact/forge.json');
  });
});

describe('previewArgv', () => {
  it('runs preview with no extra flags by default', () => {
    expect(previewArgv([])).toEqual(['preview']);
  });

  it('passes everything after `npm run dev --` straight through', () => {
    expect(previewArgv(['--port', '4400', '--host'])).toEqual(['preview', '--port', '4400', '--host']);
  });

  it('preserves order and duplicates rather than normalising them', () => {
    expect(previewArgv(['--open', '--port=4400', '--open'])).toEqual([
      'preview',
      '--open',
      '--port=4400',
      '--open',
    ]);
  });
});
