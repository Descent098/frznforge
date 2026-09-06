/**
 * `npm run build`'s wrapper (0.3.0). The chained `npm run ingest && astro build` could not be
 * steered: in a chained npm script every `--` argument lands on the LAST command, so there
 * was no way to skip the ingest or configure it.
 *
 * Only the pure parts are unit-tested — argument routing and the two refusals. Spawning is
 * covered by running the real command, which is what the accompanying manual check does.
 */
import { describe, expect, it } from 'vitest';
import { conflictMessage, noIngestPreflight, parseBuildArgs } from '../../scripts/build';

describe('parseBuildArgs', () => {
  it('defaults to ingest-then-render with nothing forwarded', () => {
    expect(parseBuildArgs([])).toEqual({ noIngest: false, ingestArgs: [], astroArgs: [] });
  });

  it('takes --no-ingest for itself rather than passing it to Astro', () => {
    expect(parseBuildArgs(['--no-ingest'])).toEqual({ noIngest: true, ingestArgs: [], astroArgs: [] });
  });

  it('routes ingest flags to ingest and everything else to Astro', () => {
    expect(parseBuildArgs(['--backfill-metadata'])).toEqual({
      noIngest: false,
      ingestArgs: ['--backfill-metadata'],
      astroArgs: [],
    });
    expect(parseBuildArgs(['--no-cache'])).toEqual({ noIngest: false, ingestArgs: ['--no-cache'], astroArgs: [] });
    // Unknown flags go to Astro rather than being rejected: this wrapper must never be the
    // reason a valid `astro build` option stops working.
    expect(parseBuildArgs(['--verbose', '--silent'])).toEqual({
      noIngest: false,
      ingestArgs: [],
      astroArgs: ['--verbose', '--silent'],
    });
  });

  it('splits a mixed line to all three destinations, preserving order', () => {
    expect(parseBuildArgs(['--verbose', '--backfill-metadata', '--no-ingest', '--outDir', 'x'])).toEqual({
      noIngest: true,
      ingestArgs: ['--backfill-metadata'],
      astroArgs: ['--verbose', '--outDir', 'x'],
    });
  });
});

describe('conflictMessage', () => {
  it('refuses --no-ingest together with a flag that configures the ingest', () => {
    // Asking for an ingest behaviour AND for no ingest means one of the two was a mistake;
    // silently dropping either would be guessing which.
    const msg = conflictMessage(parseBuildArgs(['--no-ingest', '--backfill-metadata']));
    expect(msg).toMatch(/--no-ingest cannot be combined with --backfill-metadata/);
    expect(conflictMessage(parseBuildArgs(['--no-ingest', '--no-cache']))).toMatch(/--no-cache/);
  });

  it('allows each of them on its own, and unknown flags alongside --no-ingest', () => {
    expect(conflictMessage(parseBuildArgs(['--no-ingest']))).toBeNull();
    expect(conflictMessage(parseBuildArgs(['--backfill-metadata']))).toBeNull();
    expect(conflictMessage(parseBuildArgs([]))).toBeNull();
    expect(conflictMessage(parseBuildArgs(['--no-ingest', '--verbose']))).toBeNull();
  });
});

describe('noIngestPreflight', () => {
  const root = '/proj';
  const artifact = '/proj/data/forge.json';

  it('passes when the artifact it would render exists', () => {
    expect(noIngestPreflight(root, artifact, () => true)).toEqual({ ok: true, lines: [] });
  });

  it('refuses when there is no artifact, instead of building an empty site', () => {
    // `loadForgeData` warns and returns an EMPTY artifact when the file is missing, so
    // without this check `--no-ingest` would succeed and replace a good dist/ with a site
    // containing no repos at all.
    const got = noIngestPreflight(root, artifact, () => false);
    expect(got.ok).toBe(false);
    const text = got.lines.join('\n');
    expect(text).toContain('data/forge.json'); // project-relative, pasteable
    expect(text).toContain('npm run build'); // and names the command that fixes it
  });
});
