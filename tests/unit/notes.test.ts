/**
 * Notes ingest (schema v4): the frontmatter subset parser, the title/date fallback chains and
 * `collectNotes` over real folders in temp directories.
 */
import { createHash } from 'node:crypto';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterAll, describe, expect, it } from 'vitest';
import { resolveConfig, type ResolvedConfig } from '../../src/lib/config/index';
import { Note, Slug, emptyForgeData } from '../../src/lib/data/schema';
import { firstHeading, parseFrontmatter, splitFrontmatter, stripLeadingHeading } from '../../src/lib/frontmatter';
import { isRawServable, noteRawRoutes, noteRawUrl, notesRoutes } from '../../src/lib/routes';
import {
  collectNotes,
  humaniseName,
  normaliseNoteDate,
  noteSlug,
  type CollectNotesOptions,
} from '../../src/lib/ingest/notes';

const roots: string[] = [];

/** A temp notes folder populated from `files` (keys use '/' and may be nested). */
function makeNotesDir(files: Record<string, string | Buffer>): string {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'frznforge-notes-'));
  roots.push(dir);
  for (const [rel, content] of Object.entries(files)) {
    const abs = path.join(dir, ...rel.split('/'));
    fs.mkdirSync(path.dirname(abs), { recursive: true });
    fs.writeFileSync(abs, content);
  }
  return dir;
}

/** Minimal resolved config; every test points `collectNotes` at its own temp folder. */
function makeConfig(overrides: Record<string, unknown> = {}): ResolvedConfig {
  return resolveConfig({ owner: { name: 'Tester', handle: 'tester' }, ...overrides }, os.tmpdir());
}

const config = makeConfig();

/** Collect from `dir` with the shared config. */
function collect(dir: string, options: CollectNotesOptions = {}) {
  return collectNotes(config, { dir, ...options });
}

/** Bytes of a fixture, however the fixture spelled them. */
const bytesOf = (input: Buffer | string) => (Buffer.isBuffer(input) ? input : Buffer.from(input, 'utf8'));

/**
 * The blob-store key a note file must get: sha1 over `note <len>\0` + the bytes.
 *
 * The domain prefix is not decoration. Notes and repo files share one content-addressed
 * store, and repo keys are git object ids — see `gitSha` and the collision test below.
 */
const noteSha = (input: Buffer | string) => {
  const bytes = bytesOf(input);
  return createHash('sha1').update(Buffer.from(`note ${bytes.length}\0`, 'utf8')).update(bytes).digest('hex');
};

/** The git object id of the same bytes: `sha1("blob <len>\0" + bytes)`. */
const gitSha = (input: Buffer | string) => {
  const bytes = bytesOf(input);
  return createHash('sha1').update(Buffer.from(`blob ${bytes.length}\0`, 'utf8')).update(bytes).digest('hex');
};

afterAll(() => {
  for (const dir of roots) fs.rmSync(dir, { recursive: true, force: true });
});

describe('parseFrontmatter', () => {
  it('parses scalars, flow lists and block lists', () => {
    const { data, body } = parseFrontmatter(
      [
        '---',
        'title: Git plumbing: the short version',
        'description: "Quoted, with a comma and an escape: \\"yes\\""',
        "note: 'single ''quoted'' text'",
        'date: 2026-05-14',
        'tags: [git, "frznforge", ingest]',
        'people:',
        '  - Ada',
        '  - "Grace Hopper"',
        '---',
        '',
        '# Body heading',
        'text',
      ].join('\n'),
    );
    expect(data).toEqual({
      title: 'Git plumbing: the short version',
      description: 'Quoted, with a comma and an escape: "yes"',
      note: "single 'quoted' text",
      date: '2026-05-14',
      tags: ['git', 'frznforge', 'ingest'],
      people: ['Ada', 'Grace Hopper'],
    });
    expect(body).toBe('\n# Body heading\ntext');
  });

  it('handles CRLF files identically to LF ones', () => {
    const lf = '---\ntitle: CRLF\ntags:\n  - a\n  - b\n---\n\n# Heading\n';
    const crlf = lf.replace(/\n/g, '\r\n');
    expect(parseFrontmatter(crlf).data).toEqual(parseFrontmatter(lf).data);
    expect(parseFrontmatter(crlf).body).toBe('\n# Heading\n');
    expect(firstHeading(parseFrontmatter(crlf).body)).toBe('Heading');
  });

  it('tolerates a UTF-8 BOM and a "..." terminator', () => {
    expect(parseFrontmatter('﻿---\ntitle: BOM\n...\nbody\n').data).toEqual({ title: 'BOM' });
  });

  it('treats an unterminated block as no frontmatter at all', () => {
    const src = '---\ntitle: Never closed\ntags: [a]\n\n# Heading\n';
    expect(parseFrontmatter(src)).toEqual({ data: {}, body: src });
  });

  it('returns the whole file as the body when there is no frontmatter', () => {
    const src = '# Just markdown\n\n---\nnot frontmatter: it is a rule\n';
    expect(parseFrontmatter(src)).toEqual({ data: {}, body: src });
  });

  it('ignores comments, blank lines, empty values and unsupported constructs', () => {
    const { data } = parseFrontmatter(
      [
        '---',
        '# a leading comment',
        '',
        'title: Kept # trailing comment',
        'quoted: "kept # not a comment"',
        'empty:',
        'colonless value',
        'links:',
        '  github: https://example.com',
        '  email: mailto:a@b.c',
        'people:',
        '  - name: Ada',
        '    role: author',
        'nested: {a: b}',
        'after: still parsed',
        '---',
      ].join('\n'),
    );
    expect(data).toEqual({ title: 'Kept', quoted: 'kept # not a comment', after: 'still parsed' });
  });

  it('parses an empty flow list and a flow list with a trailing comment', () => {
    expect(parseFrontmatter('---\ntags: []\n---\n').data).toEqual({ tags: [] });
    expect(parseFrontmatter('---\ntags: [a, b] # why\n---\n').data).toEqual({ tags: ['a', 'b'] });
  });

  it('drops a key whose quoted scalar is never closed', () => {
    expect(parseFrontmatter('---\ntitle: "oops\nother: fine\n---\n').data).toEqual({ other: 'fine' });
  });

  it('drops a block-sequence item that opens a nested collection', () => {
    // `- [a, b]` used to be captured as the literal scalar "[a, b]", which then rendered as a
    // tag chip reading `[a, b]` — a partial parse, which this parser's contract rules out.
    expect(parseFrontmatter('---\ntitle: TN\ntags:\n  - [a, b]\n---\n').data).toEqual({ title: 'TN' });
    expect(parseFrontmatter('---\ntitle: TN\ntags:\n  - {k: v}\n---\n').data).toEqual({ title: 'TN' });
    // a flat block list beside a bracket-looking scalar still parses
    expect(parseFrontmatter('---\ntags:\n  - a\n  - b\n---\n').data).toEqual({ tags: ['a', 'b'] });
  });
});

describe('splitFrontmatter', () => {
  // The site's note viewer hides exactly the span this function reports, so any closer the
  // parser accepts has to be a closer the viewer hides. While the two had separate regexes,
  // a `...`-terminated block parsed correctly and then rendered its own metadata as prose.
  it('reports the same block for both closers, with or without a BOM', () => {
    for (const closer of ['---', '...']) {
      const src = `---\ntitle: Dots\ndate: 2026-02-02\n${closer}\nReal body text.\n`;
      expect(splitFrontmatter(src)).toEqual({
        raw: 'title: Dots\ndate: 2026-02-02',
        body: 'Real body text.\n',
        present: true,
      });
      expect(splitFrontmatter('﻿' + src).body).toBe('Real body text.\n');
    }
  });

  it('agrees with parseFrontmatter about where the body starts', () => {
    for (const src of [
      '---\ntitle: A\n---\nbody\n',
      '---\ntitle: A\n...\nbody\n',
      '---\r\ntitle: A\r\n---\r\nbody\r\n',
      '# no frontmatter\n',
      '---\nnever closed\n',
    ]) {
      expect(splitFrontmatter(src).body).toBe(parseFrontmatter(src).body);
    }
  });

  it('treats an unterminated or absent block as all body', () => {
    expect(splitFrontmatter('---\ntitle: open\n')).toEqual({ raw: '', body: '---\ntitle: open\n', present: false });
    expect(splitFrontmatter('# plain\n')).toEqual({ raw: '', body: '# plain\n', present: false });
  });
});

describe('stripLeadingHeading', () => {
  it('removes the opening heading only when it repeats the title', () => {
    expect(stripLeadingHeading('# Heat buckets\n\nProse.\n', 'Heat buckets')).toBe('\nProse.\n');
    expect(stripLeadingHeading('Heat buckets\n===\n\nProse.\n', 'Heat buckets')).toBe('\nProse.\n');
    expect(stripLeadingHeading('#   Heat buckets  ##\n\nProse.\n', 'Heat buckets')).toBe('\nProse.\n');
  });

  it('keeps a heading that is not the title — the reading view must not lose content', () => {
    // Frontmatter named the note something else, so this H1 is real prose, not an echo.
    const src = '# A Completely Different Heading\n\nBody text here.\n';
    expect(stripLeadingHeading(src, 'Frontmatter Title Wins')).toBe(src);
    // second file of a folder note: the note title came from index.md, this heading is its own
    expect(stripLeadingHeading('# Second File Heading\n\nmore\n', 'Probe Folder')).toBe('# Second File Heading\n\nmore\n');
  });

  it('leaves a body with no opening heading, or a deeper one, alone', () => {
    expect(stripLeadingHeading('## Only an H2\n', 'Only an H2')).toBe('## Only an H2\n');
    expect(stripLeadingHeading('Just prose.\n', 'Just prose.')).toBe('Just prose.\n');
    expect(stripLeadingHeading('', 'anything')).toBe('');
  });
});

describe('title, slug and date helpers', () => {
  it('finds the first H1 outside fenced code', () => {
    expect(firstHeading('intro\n\n# Real title\n\n# Second\n')).toBe('Real title');
    expect(firstHeading('```\n# not a heading\n```\n\n# Real\n')).toBe('Real');
    expect(firstHeading('## Only an H2\n')).toBeNull();
    expect(firstHeading('# Closed heading ##\n')).toBe('Closed heading');
  });

  it('humanises names and slugifies them', () => {
    expect(humaniseName('check-determinism.ps1')).toBe('Check determinism');
    expect(humaniseName('static-host-configs')).toBe('Static host configs');
    expect(humaniseName('xdg_base.dirs.txt')).toBe('Xdg base dirs');
    expect(noteSlug('Static Host Configs')).toBe('static-host-configs');
    expect(noteSlug('check-determinism.ps1')).toBe('check-determinism');
    expect(noteSlug('!!!.md')).toBe('note');
  });

  it('normalises dates to IsoDate and rejects nonsense', () => {
    expect(normaliseNoteDate('2026-05-14')).toBe('2026-05-14T00:00:00Z');
    expect(normaliseNoteDate('2026-05-14T09:30:00+02:00')).toBe('2026-05-14T07:30:00Z');
    expect(normaliseNoteDate('2026-02-31')).toBeNull();
    expect(normaliseNoteDate('someday')).toBeNull();
  });

  it("reads a zone-less date-time as UTC, not as the build machine's zone", () => {
    // `Date.parse('2026-03-04 10:00:00')` is LOCAL time per ECMA-262, so deferring to it made
    // the same frontmatter emit a different Note.date — and a different note order, and a
    // different forge.json — in every timezone. Both spellings of the separator are covered.
    expect(normaliseNoteDate('2026-03-04 10:00:00')).toBe('2026-03-04T10:00:00Z');
    expect(normaliseNoteDate('2026-03-04T10:00:00')).toBe('2026-03-04T10:00:00Z');
    expect(normaliseNoteDate('2026-03-04T10:00')).toBe('2026-03-04T10:00:00Z');
    expect(normaliseNoteDate('2026-03-04T10:00:00.250')).toBe('2026-03-04T10:00:00Z');
    // an explicit zone still wins
    expect(normaliseNoteDate('2026-03-04T10:00:00Z')).toBe('2026-03-04T10:00:00Z');
    expect(normaliseNoteDate('2026-03-04T10:00:00-07:00')).toBe('2026-03-04T17:00:00Z');
  });

  it('produces the same date in every host timezone', () => {
    const values = ['2026-03-04', '2026-03-04 10:00:00', '2026-03-04T10:00:00', '2026-03-04T10:00:00+02:00'];
    const original = process.env.TZ;
    /** Collect every result with the process pinned to one zone. */
    const inZone = (tz: string) => {
      process.env.TZ = tz;
      // proof the switch took effect, so a Node that ignored TZ could not make this pass
      const probe = Date.parse('2026-03-04T10:00:00');
      return { probe, out: values.map((v) => normaliseNoteDate(v)) };
    };
    try {
      const east = inZone('Australia/Sydney');
      const utc = inZone('UTC');
      const west = inZone('America/Edmonton');
      expect(new Set([east.probe, utc.probe, west.probe]).size).toBe(3);
      expect(utc.out).toEqual(east.out);
      expect(west.out).toEqual(east.out);
      expect(utc.out).toEqual([
        '2026-03-04T00:00:00Z',
        '2026-03-04T10:00:00Z',
        '2026-03-04T10:00:00Z',
        '2026-03-04T08:00:00Z',
      ]);
    } finally {
      if (original === undefined) delete process.env.TZ;
      else process.env.TZ = original;
    }
  });

  it('drops date spellings Date.parse reads implementation-defined or zone-dependently', () => {
    for (const value of ['March 4, 2026', '03/04/2026', 'Wed, 04 Mar 2026 10:00:00 GMT', '2026-03-04 10:00:00 PST']) {
      expect(normaliseNoteDate(value)).toBeNull();
    }
  });
});

describe('collectNotes', () => {
  it('warns and returns nothing when the folder is missing', async () => {
    const missing = path.join(os.tmpdir(), 'frznforge-notes-does-not-exist-12345');
    const res = await collect(missing);
    expect(res.notes).toEqual([]);
    expect(res.blobs.size).toBe(0);
    expect(res.warnings).toHaveLength(1);
    expect(res.warnings[0]).toMatchObject({ code: 'notes-dir-missing', repo: null });
    expect(res.warnings[0]!.message).toContain(missing);
  });

  it('warns when the configured path is a file rather than a directory', async () => {
    const dir = makeNotesDir({ 'a.md': 'x' });
    const res = await collect(path.join(dir, 'a.md'));
    expect(res.notes).toEqual([]);
    expect(res.warnings.map((w) => w.code)).toEqual(['notes-dir-missing']);
  });

  it('reads a single-file markdown note with full frontmatter', async () => {
    const body = '# Ignored, frontmatter wins\n\nprose\n';
    const src = `---\ntitle: Heat buckets\ndescription: Fire to ice.\ndate: 2026-06-02\ntags: [design, css, design]\n---\n\n${body}`;
    const dir = makeNotesDir({ 'heat-buckets.md': src });
    const { notes, blobs, warnings } = await collect(dir);
    expect(warnings).toEqual([]);
    expect(notes).toHaveLength(1);
    const note = notes[0]!;
    expect(note).toMatchObject({
      slug: 'heat-buckets',
      title: 'Heat buckets',
      description: 'Fire to ice.',
      tags: ['design', 'css'], // de-duplicated, author order kept
      date: '2026-06-02T00:00:00Z',
      kind: 'file',
      totalBytes: Buffer.byteLength(src),
    });
    expect(note.files).toHaveLength(1);
    expect(note.files[0]).toMatchObject({
      name: 'heat-buckets.md',
      path: 'heat-buckets.md',
      sha: noteSha(src),
      size: Buffer.byteLength(src),
      binary: false,
      tooLarge: false,
      stored: true,
      language: 'Markdown',
      markdown: true,
    });
    expect(blobs.get(note.files[0]!.sha)!.toString('utf8')).toBe(src);
    expect(Note.parse(note)).toBeTruthy();
  });

  it('falls back to the first H1, then to the humanised filename', async () => {
    const dir = makeNotesDir({
      'with-heading.md': 'intro\n\n# Actual Heading\n\nmore\n',
      'no-heading.md': 'just text, no heading\n',
    });
    const { notes } = await collect(dir);
    const byslug = Object.fromEntries(notes.map((n) => [n.slug, n]));
    expect(byslug['with-heading']!.title).toBe('Actual Heading');
    expect(byslug['no-heading']!.title).toBe('No heading');
    expect(notes.every((n) => n.date === null && n.description === null && n.tags.length === 0)).toBe(true);
  });

  it('treats a non-markdown file as a note with a detected language', async () => {
    const src = 'Write-Host "hi"\n';
    const dir = makeNotesDir({ 'check-determinism.ps1': src });
    const { notes } = await collect(dir);
    expect(notes[0]).toMatchObject({ slug: 'check-determinism', title: 'Check determinism', date: null, kind: 'file' });
    expect(notes[0]!.files[0]).toMatchObject({ language: 'PowerShell', markdown: false, stored: true });
  });

  it('builds a folder note from index.md frontmatter and keeps nested POSIX paths', async () => {
    const dir = makeNotesDir({
      'deploying/index.md': '---\ntitle: Deploying a frozen forge\ndate: 2026-07-11\ntags:\n  - deployment\n  - ci\n---\n\nprose\n',
      'deploying/deploy.sh': '#!/bin/sh\necho hi\n',
      'deploying/ci/pages.yml': 'name: pages\n',
      'deploying/ci/nested/serve.py': 'print(1)\n',
      'deploying/_draft.md': 'ignored',
      'deploying/.secret': 'ignored',
    });
    const { notes } = await collect(dir);
    expect(notes).toHaveLength(1);
    const note = notes[0]!;
    expect(note).toMatchObject({
      slug: 'deploying',
      title: 'Deploying a frozen forge',
      date: '2026-07-11T00:00:00Z',
      tags: ['deployment', 'ci'],
      kind: 'folder',
    });
    expect(note.files.map((f) => f.path)).toEqual(['ci/nested/serve.py', 'ci/pages.yml', 'deploy.sh', 'index.md']);
    expect(note.files.map((f) => f.name)).toEqual(['serve.py', 'pages.yml', 'deploy.sh', 'index.md']);
    expect(note.files.map((f) => f.language)).toEqual(['Python', 'YAML', 'Shell', 'Markdown']);
    expect(note.files.some((f) => f.path.includes('\\'))).toBe(false);
    expect(note.totalBytes).toBe(note.files.reduce((n, f) => n + f.size, 0));
  });

  it('reads folder frontmatter from README.md when there is no index.md', async () => {
    const dir = makeNotesDir({
      'guide/README.md': '---\ntitle: From the readme\n---\n\nbody\n',
      'guide/other.md': '---\ntitle: Not used\n---\n\n# Not used either\n',
    });
    const { notes } = await collect(dir);
    expect(notes[0]!.title).toBe('From the readme');
  });

  it('names a folder note with no markdown from the folder itself', async () => {
    const dir = makeNotesDir({
      'static-host-configs/vercel.json': '{"trailingSlash": true}\n',
      'static-host-configs/netlify.toml': '[build]\n',
    });
    const { notes } = await collect(dir);
    expect(notes[0]).toMatchObject({ slug: 'static-host-configs', title: 'Static host configs', date: null, kind: 'folder' });
    expect(notes[0]!.files.map((f) => f.language)).toEqual(['TOML', 'JSON']);
  });

  it('ignores dot- and underscore-prefixed entries and empty folders', async () => {
    const dir = makeNotesDir({
      'kept.md': '# Kept\n',
      '_wip.md': 'skip',
      '.gitkeep': '',
      '_drafts/a.md': 'skip',
      '.hidden/b.md': 'skip',
      'empty-ish/_only-drafts.md': 'skip',
    });
    const { notes, warnings } = await collect(dir);
    expect(notes.map((n) => n.slug)).toEqual(['kept']);
    expect(warnings).toEqual([]);
  });

  it('flags binary and over-cap files without storing the oversized one', async () => {
    const binary = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x00, 0x01, 0x02, 0x00]);
    const big = 'x'.repeat(200) + '\n';
    const dir = makeNotesDir({ 'assets/pixel.png': binary, 'assets/big.txt': big, 'assets/index.md': '# Assets\n' });
    const { notes, blobs } = await collect(dir, { maxFileBytes: 100 });
    const files = Object.fromEntries(notes[0]!.files.map((f) => [f.path, f]));
    expect(files['pixel.png']).toMatchObject({ binary: true, tooLarge: false, stored: true, language: null, size: 8 });
    expect(files['big.txt']).toMatchObject({ binary: false, tooLarge: true, stored: false, language: 'Text', size: 201 });
    expect(files['big.txt']!.sha).toBe(noteSha(big)); // hashed in full even though it is not stored
    expect(blobs.has(files['big.txt']!.sha)).toBe(false);
    expect(blobs.get(files['pixel.png']!.sha)).toEqual(binary);
    expect(notes[0]!.totalBytes).toBe(8 + 201 + '# Assets\n'.length);
  });

  it('detects binary content in an over-cap file by sniffing its first bytes', async () => {
    const buf = Buffer.concat([Buffer.from('lead'), Buffer.from([0]), Buffer.alloc(300, 0x41)]);
    const dir = makeNotesDir({ 'blob.bin': buf });
    const { notes, blobs } = await collect(dir, { maxFileBytes: 100 });
    expect(notes[0]!.files[0]).toMatchObject({ binary: true, tooLarge: true, stored: false, size: buf.length, sha: noteSha(buf) });
    expect(blobs.size).toBe(0);
  });

  it('suffixes colliding slugs in walk order and warns once per collision', async () => {
    const dir = makeNotesDir({
      'Heat Buckets/index.md': '# Folder one\n',
      'heat-buckets.md': '# File two\n',
      'heat.buckets.md': '# File three\n',
    });
    const { notes, warnings } = await collect(dir);
    // Walk order is code-point order on the entry name: 'H' (0x48) sorts before 'h' (0x68).
    expect(notes.map((n) => n.slug).sort()).toEqual(['heat-buckets', 'heat-buckets-2', 'heat-buckets-3']);
    const byTitle = Object.fromEntries(notes.map((n) => [n.title, n.slug]));
    expect(byTitle['Folder one']).toBe('heat-buckets');
    expect(byTitle['File two']).toBe('heat-buckets-2');
    expect(byTitle['File three']).toBe('heat-buckets-3');
    expect(warnings.map((w) => w.code)).toEqual(['note-slug-collision', 'note-slug-collision']);
    expect(warnings.every((w) => w.repo === null)).toBe(true);
    for (const n of notes) expect(Slug.safeParse(n.slug).success).toBe(true);
  });

  it('orders notes by date desc with undated ones last, then by title', async () => {
    const dated = (title: string, date: string) => `---\ntitle: ${title}\ndate: ${date}\n---\n`;
    const dir = makeNotesDir({
      'older.md': dated('Older', '2026-01-02'),
      'newer.md': dated('Newer', '2026-03-04'),
      'zebra.md': '# Zebra\n',
      'apple.md': '# Apple\n',
      'same-day-b.md': dated('Beta', '2026-03-04'),
    });
    const { notes } = await collect(dir);
    expect(notes.map((n) => n.title)).toEqual(['Beta', 'Newer', 'Older', 'Apple', 'Zebra']);
  });

  it('produces byte-identical output across runs and keys blobs by the file sha', async () => {
    const dir = makeNotesDir({
      'one.md': '---\ntitle: One\ndate: 2026-04-01\ntags: [a]\n---\n\n# One\n',
      'two/index.md': '# Two\n',
      'two/data.json': '{"a":1}\n',
      'three.txt': 'plain\n',
    });
    const first = await collect(dir);
    const second = await collect(dir);
    expect(JSON.stringify(second.notes)).toBe(JSON.stringify(first.notes));
    expect([...second.blobs.keys()]).toEqual([...first.blobs.keys()]);

    const stored = first.notes.flatMap((n) => n.files).filter((f) => f.stored);
    expect([...first.blobs.keys()].sort()).toEqual([...new Set(stored.map((f) => f.sha))].sort());
    for (const f of stored) expect(noteSha(first.blobs.get(f.sha)!)).toBe(f.sha);
  });

  it('leaves dates null unless useMtime is on', async () => {
    const dir = makeNotesDir({ 'undated.md': '# Undated\n' });
    const when = Date.UTC(2026, 1, 3, 4, 5, 6) / 1000;
    fs.utimesSync(path.join(dir, 'undated.md'), when, when);
    expect((await collect(dir)).notes[0]!.date).toBeNull();
    expect((await collect(dir, { useMtime: true })).notes[0]!.date).toBe('2026-02-03T04:05:06Z');
  });

  it('takes dir, cap and useMtime from the config when no options are passed', async () => {
    const dir = makeNotesDir({ 'capped.md': '# Capped\n' + 'y'.repeat(100) });
    const cfg = makeConfig({ notes: { dir, maxFileBytes: 16, useMtime: true } });
    const when = Date.UTC(2026, 6, 8, 9, 10, 11) / 1000;
    fs.utimesSync(path.join(dir, 'capped.md'), when, when);
    const { notes, blobs } = await collectNotes(cfg);
    expect(notes[0]).toMatchObject({ slug: 'capped', date: '2026-07-08T09:10:11Z' });
    // Over the cap: no content, so no H1 either — the title falls back to the filename.
    expect(notes[0]!.title).toBe('Capped');
    expect(notes[0]!.files[0]).toMatchObject({ tooLarge: true, stored: false });
    expect(blobs.size).toBe(0);
  });

  it('takes the H1 fallback from prose, never from a # comment inside frontmatter', async () => {
    // A folder note whose only markdown is neither index.md nor README.md: there is no
    // frontmatter file, so the heading scan falls back to that file — and used to scan its
    // RAW text, promoting the YAML comment below to the note's title.
    const dir = makeNotesDir({
      'guide/overview.md': '---\ntitle: Ignored by design\n# a note to self\ndate: 2026-01-01\n---\n\n# Real Heading\n\nbody\n',
      'plain-folder/notes.md': '---\ndate: 2026-01-01\n---\n\n# Plain Heading\n\nbody\n',
    });
    const { notes } = await collect(dir);
    const byslug = Object.fromEntries(notes.map((n) => [n.slug, n]));
    expect(byslug['guide']!.title).toBe('Real Heading');
    expect(byslug['plain-folder']!.title).toBe('Plain Heading');
    // that file is not the frontmatter file, so its keys never became note metadata
    expect(byslug['guide']!.date).toBeNull();
    expect(byslug['guide']!.tags).toEqual([]);
  });

  it('keys note blobs in their own namespace, never on a git object id', async () => {
    // The store is shared with repo files, whose keys are `sha1('blob <len>\0' + bytes)`. A
    // note file whose RAW BYTES are exactly that pre-image would, under a bare sha1, take the
    // repo file's key — and notes are merged last, so it would overwrite the repo's content.
    const inner = '0.1.0\n';
    const payload = Buffer.concat([Buffer.from(`blob ${Buffer.byteLength(inner)}\0`, 'utf8'), Buffer.from(inner)]);
    const dir = makeNotesDir({ 'payload.bin': payload });
    const { notes, blobs } = await collect(dir);
    const file = notes[0]!.files[0]!;
    expect(file.sha).toBe(noteSha(payload));
    expect(file.sha).not.toBe(gitSha(inner));
    expect([...blobs.keys()]).toEqual([file.sha]);
    // and the general rule: no note key is ever the git id of the same bytes
    for (const src of ['plain\n', '', 'x'.repeat(64)]) expect(noteSha(src)).not.toBe(gitSha(src));
  });

  it('warns about a note file no static raw URL can reach, and still renders it', async () => {
    const dir = makeNotesDir({
      'awkward/index.md': '# Awkward\n',
      'awkward/c#-tips.md': '# sharp\n',
      'awkward/50% off.txt': 'fifty\n',
      'awkward/read me.txt': 'fine\n',
    });
    const { notes, blobs, warnings } = await collect(dir);
    const note = notes[0]!;
    // every file is still collected, stored and shown — only the raw route is withheld
    expect(note.files.map((f) => f.path).sort()).toEqual(['50% off.txt', 'c#-tips.md', 'index.md', 'read me.txt']);
    expect(note.files.every((f) => f.stored)).toBe(true);
    expect(blobs.size).toBe(4);
    expect(warnings.map((w) => w.code)).toEqual(['note-file-unservable', 'note-file-unservable']);
    expect(warnings.every((w) => w.repo === null)).toBe(true);
    expect(warnings.map((w) => w.message).join(' ')).toContain('50% off.txt');
  });
});

describe('note raw URLs', () => {
  it('percent-encodes each path segment and keeps the separators', () => {
    expect(noteRawUrl('n', 'read me.md')).toBe('/notes/n/raw/read%20me.md');
    expect(noteRawUrl('n', 'a&b.txt')).toBe('/notes/n/raw/a%26b.txt');
    expect(noteRawUrl('n', 'plus+one.txt')).toBe('/notes/n/raw/plus%2Bone.txt');
    expect(noteRawUrl('n', 'naïve.txt')).toBe('/notes/n/raw/na%C3%AFve.txt');
    // directory separators stay literal, so the note's folder structure survives in the URL
    expect(noteRawUrl('n', 'ci/pages.yml')).toBe('/notes/n/raw/ci/pages.yml');
    expect(noteRawUrl('n', 'a b/c d.txt')).toBe('/notes/n/raw/a%20b/c%20d.txt');
  });

  it('every encoded URL decodes back to the file on disk', () => {
    for (const p of ['read me.md', 'a&b.txt', 'plus+one.txt', 'naïve.txt', 'ci/pages.yml', "it's.md"]) {
      const url = noteRawUrl('n', p);
      expect(decodeURIComponent(url.slice('/notes/n/raw/'.length))).toBe(p);
    }
  });

  it('rejects the two characters a static build cannot round-trip', () => {
    expect(isRawServable('plain.md')).toBe(true);
    expect(isRawServable('read me.md')).toBe(true);
    expect(isRawServable('naïve.txt')).toBe(true);
    // '#' is escaped in the emitted FILENAME; '%' aborts the build outright
    expect(isRawServable('c#-tips.md')).toBe(false);
    expect(isRawServable('50% off.txt')).toBe(false);
    expect(isRawServable('dir#1/a.md')).toBe(false);
  });

  it('omits the raw route for an unservable file but keeps its note', () => {
    const note = {
      slug: 'awkward',
      title: 'Awkward',
      description: null,
      tags: [],
      date: null,
      kind: 'folder' as const,
      totalBytes: 3,
      files: [
        { name: 'ok.md', path: 'ok.md', sha: 'a'.repeat(40), size: 1, binary: false, tooLarge: false, stored: true, language: 'Markdown', markdown: true },
        { name: 'c#.md', path: 'c#.md', sha: 'b'.repeat(40), size: 1, binary: false, tooLarge: false, stored: true, language: 'Markdown', markdown: true },
        { name: 'big.bin', path: 'big.bin', sha: 'c'.repeat(40), size: 1, binary: true, tooLarge: true, stored: false, language: null, markdown: false },
      ],
    };
    const data = { ...emptyForgeData(), notes: [note] };
    expect(noteRawRoutes(data).map((r) => r.url)).toEqual(['/notes/awkward/raw/ok.md']);
    expect(notesRoutes(data)).toEqual(['/notes/', '/notes/awkward/', '/notes/awkward/raw/ok.md']);
  });
});
