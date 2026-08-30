/**
 * Phase 6 sync tests: notes and organizations must be *routable* and *searchable*.
 *
 * Runs the real ingest over throwaway git repos plus a throwaway notes folder, then asserts
 * the artifact against the very functions the pages use (`allRoutes`, `getNoteRoutes`,
 * `noteRawRoutes`, `getOrgRoutes`, `buildSearchIndex`) — same contract as `site-sync.test.ts`
 * and `phase3-sync.test.ts`, extended to the schema-v4 objects.
 */
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import { resolveConfig, type ResolvedConfig } from '../../src/lib/config/index';
import { loadForgeData, readBlobBuffer, resetForgeDataCache } from '../../src/lib/data/load';
import type { ForgeData } from '../../src/lib/data/schema';
import { compareNotes } from '../../src/lib/data/schema';
import { ingest, writeArtifact } from '../../src/lib/ingest';
import {
  notesIndexUrl,
  orgsIndexUrl,
  allRoutes,
  getNoteRoutes,
  getOrgRoutes,
  isRawServable,
  noteRawRoutes,
  noteRawUrl,
  noteUrl,
  orgReposUrl,
  orgUrl,
  reposInOrg,
} from '../../src/lib/routes';
import { buildSearchIndex } from '../../src/lib/search';
import { FixtureRepo } from './helpers/fixture-repo';

/** Bigger than the `notes.maxFileBytes` below, so exactly one note file is never stored. */
const OVERSIZE_BYTES = 4096;
const MAX_NOTE_BYTES = 512;

/**
 * Build the throwaway notes folder. Mirrors the shipped `content/notes` layout: single files,
 * a folder note with an `index.md`, a folder note with no markdown at all, ignored entries,
 * and one file over the size cap.
 */
function writeNotesDir(dir: string): void {
  const put = (rel: string, content: string) => {
    const abs = path.join(dir, ...rel.split('/'));
    fs.mkdirSync(path.dirname(abs), { recursive: true });
    fs.writeFileSync(abs, content);
  };
  put('heat-buckets.md', '---\ntitle: Heat buckets\ndescription: Age as temperature.\ndate: 2026-06-02\ntags: [design, css]\n---\n\n# Heat buckets\n\nProse.\n');
  put('check-determinism.ps1', '# no frontmatter here\nWrite-Host "hi"\n');
  put('deploying-a-frozen-forge/index.md', '---\ntitle: Deploying a frozen forge\ndate: 2026-07-11\ntags: [deployment]\n---\n\nProse.\n');
  put('deploying-a-frozen-forge/deploy.sh', '#!/bin/sh\necho deploy\n');
  put('deploying-a-frozen-forge/ci/pages.yml', 'name: pages\n');
  put('deploying-a-frozen-forge/huge.txt', 'x'.repeat(OVERSIZE_BYTES));
  put('static-host-configs/vercel.json', '{ "trailingSlash": true }\n');
  // File names an author can legally type but a URL has opinions about: the first two must be
  // encoded into a working raw route, the last two have no static URL at all.
  put('awkward-names/index.md', '---\ntitle: Awkward names\n---\n\nProse.\n');
  put('awkward-names/read me.txt', 'spaces\n');
  put('awkward-names/a&b+c.txt', 'punctuation\n');
  put('awkward-names/c#-tips.md', '# sharp\n');
  put('awkward-names/50% off.txt', 'percent\n');
  put('.hidden-note.md', '# ignored\n');
  put('_draft.md', '# ignored\n');
}

describe('phase 6: notes & organizations ↔ site sync', () => {
  let alpha: FixtureRepo;
  let beacon: FixtureRepo;
  let gamma: FixtureRepo;
  let notesDir: string;
  let outDir: string;
  let config: ResolvedConfig;
  let data: ForgeData;
  let routes: Set<string>;

  beforeAll(async () => {
    alpha = FixtureRepo.create('alpha');
    alpha.writeAndCommit({ 'README.md': '# Alpha\n', 'src/a.ts': 'export const a = 1;\n' }, 'init');
    beacon = FixtureRepo.create('beacon');
    beacon.writeAndCommit({ 'README.md': '# Beacon\n' }, 'init');
    gamma = FixtureRepo.create('gamma');
    gamma.writeAndCommit({ 'README.md': '# Gamma\n' }, 'init');

    notesDir = fs.mkdtempSync(path.join(os.tmpdir(), 'frznforge-notes-'));
    writeNotesDir(notesDir);
    outDir = fs.mkdtempSync(path.join(os.tmpdir(), 'frznforge-phase6-'));

    config = resolveConfig(
      {
        owner: { name: 'Tester', handle: 'tester' },
        repos: [
          // membership declared by the repo …
          { type: 'local', path: alpha.dir, org: 'canadian-coding' },
          // … by the organization (see `organizations` below) …
          { type: 'local', path: beacon.dir },
          // … and a dangling reference, which must warn and not fail
          { type: 'local', path: gamma.dir, org: 'no-such-org' },
        ],
        organizations: [
          { slug: 'canadian-coding', name: 'Canadian Coding', description: 'Sturdy tools.', repos: ['beacon', 'ghost-repo'] },
          { slug: 'lonely', name: 'Lonely' },
        ],
        notes: { dir: notesDir, maxFileBytes: MAX_NOTE_BYTES },
        ingest: { outDir, maxBlobBytes: 1024 * 1024, maxCommits: null, concurrency: 3 },
      },
      os.tmpdir(),
    );
    const result = await ingest(config);
    await writeArtifact(result.data, result.blobs, result.archives, outDir);
    resetForgeDataCache();
    data = loadForgeData(outDir);
    routes = new Set(allRoutes(data));
  });

  afterAll(() => {
    alpha.cleanup();
    beacon.cleanup();
    gamma.cleanup();
    for (const d of [notesDir, outDir]) fs.rmSync(d, { recursive: true, force: true, maxRetries: 5 });
  });

  /* ---- notes: routes ------------------------------------------------------ */

  it('collects the notes folder: ignored entries stay out, folders become one note each', () => {
    expect(data.notes.map((n) => n.slug)).toEqual([
      'deploying-a-frozen-forge',
      'heat-buckets',
      'awkward-names',
      'check-determinism',
      'static-host-configs',
    ]);
    expect(data.notes.map((n) => n.kind)).toEqual(['folder', 'file', 'folder', 'file', 'folder']);
    // frontmatter title wins; no frontmatter → humanised name and a null date
    const byslug = new Map(data.notes.map((n) => [n.slug, n]));
    expect(byslug.get('heat-buckets')!.title).toBe('Heat buckets');
    expect(byslug.get('heat-buckets')!.date).toBe('2026-06-02T00:00:00Z');
    expect(byslug.get('check-determinism')!.date).toBeNull();
    expect(byslug.get('static-host-configs')!.date).toBeNull();
    expect(data.notes.some((n) => n.slug.startsWith('.') || n.slug.includes('draft'))).toBe(false);
  });

  it('note slugs are unique and the artifact is in compareNotes order', () => {
    const slugs = data.notes.map((n) => n.slug);
    expect(new Set(slugs).size).toBe(slugs.length);
    expect([...data.notes].sort(compareNotes).map((n) => n.slug)).toEqual(slugs);
  });

  it('every note in the artifact has exactly one /notes/<slug>/ route', () => {
    const noteRoutes = getNoteRoutes(data);
    expect(noteRoutes.map((r) => r.slug)).toEqual(data.notes.map((n) => n.slug));
    expect(new Set(noteRoutes.map((r) => r.url)).size).toBe(noteRoutes.length);
    for (const r of noteRoutes) {
      expect(r.url).toBe(noteUrl(r.slug));
      expect(routes.has(r.url)).toBe(true);
    }
    expect(routes.has(notesIndexUrl())).toBe(true);
  });

  it('every stored note file has a raw route with readable bytes; unstored files have none', () => {
    const rawUrls = new Set(noteRawRoutes(data).map((r) => r.url));
    let stored = 0;
    let skipped = 0;
    for (const note of data.notes) {
      for (const f of note.files) {
        const url = noteRawUrl(note.slug, f.path);
        // a raw route needs BOTH bytes to serve and a path a static URL can round-trip
        const servable = f.stored && isRawServable(f.path);
        expect(rawUrls.has(url)).toBe(servable);
        expect(routes.has(url)).toBe(servable);
        if (f.stored) {
          stored++;
          expect(readBlobBuffer(outDir, f.sha)).not.toBeNull();
        } else {
          skipped++;
        }
      }
      // `totalBytes` is the sum of every file, stored or not
      expect(note.totalBytes).toBe(note.files.reduce((n, f) => n + f.size, 0));
      expect(note.files.map((f) => f.path)).toEqual([...note.files.map((f) => f.path)].sort());
    }
    expect(stored).toBeGreaterThan(0);
    // exactly one fixture file (huge.txt) is over notes.maxFileBytes
    expect(skipped).toBe(1);
    const huge = data.notes.flatMap((n) => n.files).find((f) => f.name === 'huge.txt')!;
    expect(huge.tooLarge).toBe(true);
    expect(huge.stored).toBe(false);
  });

  it('encodes raw URLs, and withholds the route for a name no static URL can reach', () => {
    const note = data.notes.find((n) => n.slug === 'awkward-names')!;
    const rawUrls = new Set(noteRawRoutes(data).map((r) => r.url));

    // encodable: a route exists, and the URL decodes back to the name on disk
    for (const path of ['read me.txt', 'a&b+c.txt', 'index.md']) {
      const url = noteRawUrl(note.slug, path);
      expect(url).not.toMatch(/[ &+]/);
      expect(decodeURIComponent(url.slice(`/notes/${note.slug}/raw/`.length))).toBe(path);
      expect(rawUrls.has(url)).toBe(true);
      expect(routes.has(url)).toBe(true);
    }

    // unservable: still collected, stored and renderable — just no raw route, plus a warning
    for (const path of ['c#-tips.md', '50% off.txt']) {
      const file = note.files.find((f) => f.path === path)!;
      expect(file.stored).toBe(true);
      expect(readBlobBuffer(outDir, file.sha)).not.toBeNull();
      expect([...rawUrls].some((u) => u.includes(encodeURIComponent(path)))).toBe(false);
    }
    const unservable = data.warnings.filter((w) => w.code === 'note-file-unservable');
    expect(unservable).toHaveLength(2);
    expect(unservable.every((w) => w.repo === null)).toBe(true);
  });

  it('folder notes keep nested relative paths with forward slashes', () => {
    const folder = data.notes.find((n) => n.slug === 'deploying-a-frozen-forge')!;
    expect(folder.files.map((f) => f.path)).toContain('ci/pages.yml');
    for (const f of folder.files) expect(f.path).not.toContain('\\');
    expect(routes.has('/notes/deploying-a-frozen-forge/raw/ci/pages.yml')).toBe(true);
  });

  /* ---- organizations: routes + membership --------------------------------- */

  it('every organization has an overview route and a scoped repo-listing route', () => {
    expect(data.organizations.map((o) => o.slug)).toEqual(['canadian-coding', 'lonely']);
    expect(routes.has(orgsIndexUrl())).toBe(true);
    for (const org of data.organizations) {
      expect(routes.has(orgUrl(org.slug))).toBe(true);
      expect(routes.has(orgReposUrl(org.slug))).toBe(true);
    }
    const orgRoutes = getOrgRoutes(data);
    expect(orgRoutes.map((r) => r.slug)).toEqual(data.organizations.map((o) => o.slug));
    // an org with no members still gets a page — it just resolves to no repos
    expect(orgRoutes.find((r) => r.slug === 'lonely')!.repos).toEqual([]);
  });

  it('membership is the union of both directions, and only ever names real repos', () => {
    const cc = data.organizations.find((o) => o.slug === 'canadian-coding')!;
    // alpha claimed the org; beacon was claimed by it; both appear exactly once, sorted
    expect(cc.repos).toEqual(['alpha', 'beacon']);
    // the dangling `repos: ['ghost-repo']` entry is dropped from membership
    expect(cc.repos).not.toContain('ghost-repo');
    // gamma named an org that does not exist, so it belongs to none
    for (const org of data.organizations) expect(org.repos).not.toContain('gamma');
    // and the reverse: every member slug resolves to a repo in the artifact
    const slugs = new Set(data.repos.map((r) => r.slug));
    for (const org of data.organizations) {
      for (const s of org.repos) expect(slugs.has(s)).toBe(true);
      expect(reposInOrg(data, org).map((r) => r.slug)).toEqual(org.repos);
    }
    // no repo is a member of two organizations here
    const seen = data.organizations.flatMap((o) => o.repos);
    expect(new Set(seen).size).toBe(seen.length);
  });

  it('dangling references on either side are warnings, not failures', () => {
    const codes = data.warnings.map((w) => w.code);
    expect(codes).toContain('org-unknown-repo');
    expect(codes).toContain('repo-unknown-org');
    expect(data.warnings.find((w) => w.code === 'org-unknown-repo')!.repo).toBeNull();
    expect(data.warnings.find((w) => w.code === 'repo-unknown-org')!.repo).toBe('gamma');
  });

  /* ---- searchable ---------------------------------------------------------- */

  it('every note and organization is in the search index', () => {
    const docs = buildSearchIndex(data).docs;
    const urls = new Set(docs.map((d) => d.url));
    for (const n of data.notes) expect(urls.has(noteUrl(n.slug))).toBe(true);
    for (const o of data.organizations) expect(urls.has(orgUrl(o.slug))).toBe(true);
    expect(docs.filter((d) => d.kind === 'note')).toHaveLength(data.notes.length);
    expect(docs.filter((d) => d.kind === 'org')).toHaveLength(data.organizations.length);
    // every indexed URL is a route the site actually emits
    for (const d of docs) {
      if (d.kind === 'note' || d.kind === 'org' || d.kind === 'repo') expect(routes.has(d.url)).toBe(true);
    }
    // a note file's name finds its note
    const noteDoc = docs.find((d) => d.url === noteUrl('deploying-a-frozen-forge'))!;
    expect(noteDoc.keywords).toContain('ci/pages.yml');
  });

  it('re-ingesting the same inputs produces an identical notes/organizations artifact', async () => {
    const again = await ingest(config);
    expect(again.data.notes).toEqual(data.notes);
    expect(again.data.organizations).toEqual(data.organizations);
  });
});
