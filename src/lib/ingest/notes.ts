/**
 * Note collection (schema v4): turn the plain folder at `notes.dir` into `Note[]` plus blobs
 * for the shared content-addressed store.
 *
 * This is the one part of ingest that reads the filesystem directly instead of going through
 * git plumbing, and that is correct: `notes.dir` is not a repository, so there is no committed
 * tree to read and no working copy to avoid. The "ingest never reads the working tree" rule in
 * `src/lib/data/schema.ts` is about git repos.
 *
 * Shape of the folder:
 *
 * ```
 * <notes.dir>/
 * ├── quick-tip.md          → a 'file' note, one NoteFile
 * ├── dotfiles/             → a 'folder' note, every file under it (recursively)
 * │   ├── index.md            frontmatter + title come from here (or README.md)
 * │   └── bin/tool.sh         NoteFile.path = "bin/tool.sh"
 * ├── .drafts/              → ignored (leading '.')
 * └── _scratch.md           → ignored (leading '_')
 * ```
 *
 * Everything here is deliberately dependency-free: directory entries are sorted before they
 * are walked (readdir order is filesystem-dependent), paths are built with `path.join` and
 * only converted to forward slashes for the artifact, and frontmatter is read through
 * `src/lib/frontmatter.ts`, which covers a small, explicitly documented subset of YAML rather
 * than pulling in a parser. That module is shared with the note viewer on purpose: both halves
 * have to agree on where a frontmatter block ends, and two regexes drifted apart once already.
 */
import { createHash } from 'node:crypto';
import fs from 'node:fs';
import fsp from 'node:fs/promises';
import path from 'node:path';
import type { ResolvedConfig } from '../config/index';
import { compareNotes, type Note, type NoteFile, type Warning } from '../data/schema';
import { firstHeading, parseFrontmatter } from '../frontmatter';
import { isMarkdownPath } from '../markdown';
import { isRawServable } from '../routes';
import { looksBinary } from './git';
import { detectLanguage } from './languages';

/** What `collectNotes` hands back to `ingest()`. */
export interface CollectNotesResult {
  /**
   * Every note, already in artifact order: `date` desc with null dates last, then `title` asc
   * (use `compareNotes` from `../data/schema`). Slugs are unique — a collision is resolved by
   * suffixing `-2`, `-3`, … in walk order and raising `note-slug-collision`.
   */
  notes: Note[];
  /**
   * Content for every `NoteFile` with `stored: true`, keyed by `NoteFile.sha`
   * (`sha1('note <len>\0' + bytes)` — see `NOTE_HASH_DOMAIN`). `ingest()` merges this into the
   * same map `writeArtifact` persists to `<outDir>/blobs/`, so nothing extra has to be written
   * here.
   */
  blobs: Map<string, Buffer>;
  /**
   * Site-level warnings (`repo: null`): `notes-dir-missing`, `note-slug-collision`,
   * `note-file-unservable`. Order must be deterministic — emit them in directory walk order.
   */
  warnings: Warning[];
}

/** Overrides for `collectNotes`; every field defaults to the matching config value. */
export interface CollectNotesOptions {
  /** Absolute notes folder. Default: `config.notesDir`. */
  dir?: string;
  /** Byte cap above which a file is `tooLarge` and not stored. Default: `config.notes.maxFileBytes ?? config.ingest.maxBlobBytes`. */
  maxFileBytes?: number;
  /** Use filesystem mtime when frontmatter has no `date`. Default: `config.notes.useMtime`. */
  useMtime?: boolean;
}

/* ---- title / date derivation --------------------------------------------- */

/** Drop the last extension: `check-determinism.ps1` → `check-determinism`, `.env` stays whole. */
function stripExtension(name: string): string {
  const dot = name.lastIndexOf('.');
  return dot > 0 ? name.slice(0, dot) : name;
}

/**
 * File/folder name → display title: separators become spaces and the first letter is
 * upper-cased, leaving existing capitalisation alone (`static-host-configs` → "Static host
 * configs", `XDG_dirs` → "XDG dirs").
 */
export function humaniseName(name: string): string {
  const words = stripExtension(name)
    .replace(/[-_.]+/g, ' ')
    .replace(/\s+/g, ' ')
    .trim();
  if (!words) return '';
  return words[0]!.toUpperCase() + words.slice(1);
}

/**
 * File/folder name → URL slug, with the same normalisation as repo slugs (`slugify` in
 * `./meta`). Not a call to that helper because its empty-input fallback is `'repo'`, which
 * would be a lie on a note page.
 */
export function noteSlug(name: string): string {
  const slug = stripExtension(name)
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '');
  return slug.length ? slug : 'note';
}

/** `YYYY-MM-DD`, the whole value. */
const DATE_ONLY = /^(\d{4})-(\d{2})-(\d{2})$/;

/**
 * `YYYY-MM-DD` + a time, separated by `T` or a space, with an OPTIONAL `Z`/`±HH:MM` zone.
 * Fractional seconds are accepted and then dropped, like every other date in the artifact.
 */
const DATE_TIME = /^(\d{4}-\d{2}-\d{2})[T ](\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?)(Z|[+-]\d{2}:\d{2})?$/;

/**
 * Frontmatter `date` → `IsoDate`, or null when it is not a date the artifact can carry.
 *
 * Only two shapes are accepted, and both are read the same way on every machine:
 *
 *  - `YYYY-MM-DD` → midnight UTC;
 *  - `YYYY-MM-DD` + a time, with or without a zone → that instant, seconds precision, UTC.
 *    **A missing zone means UTC, not the build machine's zone.** This is the determinism rule
 *    doing real work: `Date.parse('2026-03-04 10:00:00')` is local time per ECMA-262, so
 *    deferring to it made the same frontmatter emit `…T17:00:00Z` in Edmonton and
 *    `…T10:00:00Z` in CI — a different `Note.date`, a different note order and a different
 *    forge.json from identical inputs.
 *
 * Everything else — `March 4, 2026`, `03/04/2026`, an RFC 2822 string — is dropped for the
 * same reason: `Date.parse` handles those implementation-defined and zone-dependently. Invalid
 * values are dropped silently (no warning code): a mistyped date must not fail a build, and
 * the note simply renders as undated.
 */
export function normaliseNoteDate(value: string): string | null {
  const t = value.trim();
  if (!t) return null;

  const ymd = DATE_ONLY.exec(t);
  if (ymd) {
    // `Date.parse` silently rolls "2026-02-31" over into March, so the calendar is checked by
    // round-tripping the components instead of trusting the parse.
    const [y, m, d] = [Number(ymd[1]), Number(ymd[2]), Number(ymd[3])];
    const parsed = new Date(Date.UTC(y, m - 1, d));
    const ok = parsed.getUTCFullYear() === y && parsed.getUTCMonth() === m - 1 && parsed.getUTCDate() === d;
    return ok ? `${t}T00:00:00Z` : null;
  }

  const dt = DATE_TIME.exec(t);
  if (!dt) return null;
  // Rebuilt as a strict ISO string so the parse is the spec's zone-independent path, and so a
  // bare date-time picks up the explicit `Z` rather than the host zone.
  const ms = Date.parse(`${dt[1]}T${dt[2]}${dt[3] ?? 'Z'}`);
  if (Number.isNaN(ms)) return null; // out-of-range calendar or clock ("2026-02-31T00:00:00Z")
  return new Date(ms).toISOString().replace(/\.\d{3}Z$/, 'Z');
}

/* ---- filesystem walk ------------------------------------------------------ */

/** Bytes inspected when deciding whether an oversized file is binary (git's own heuristic). */
const BINARY_SNIFF_BYTES = 8000;

/** Names starting with `.` or `_` are private to the author and never published. */
function isIgnoredEntry(name: string): boolean {
  return name.startsWith('.') || name.startsWith('_');
}

/** Code-point order — never `localeCompare`, whose result depends on the build machine's ICU. */
function byName(a: { name: string }, b: { name: string }): number {
  return a.name < b.name ? -1 : a.name > b.name ? 1 : 0;
}

/** Directory entries, ignored names removed, in a stable order (readdir order is not one). */
async function sortedEntries(dir: string): Promise<fs.Dirent[]> {
  const entries = await fsp.readdir(dir, { withFileTypes: true });
  return entries.filter((e) => !isIgnoredEntry(e.name)).sort(byName);
}

/** One file found under a note root, before it is hashed. */
interface FoundFile {
  /** Absolute path on disk. */
  abs: string;
  /** Path relative to the note root, forward slashes (the artifact's `NoteFile.path`). */
  rel: string;
}

/**
 * Every regular file under `root`, recursively, in sorted walk order. Symlinks and other
 * non-regular entries are skipped: following them would let a note escape `notes.dir` (or
 * loop forever), and the site has nothing to render for a device node.
 */
async function walkFiles(root: string, dir: string = root): Promise<FoundFile[]> {
  const out: FoundFile[] = [];
  for (const entry of await sortedEntries(dir)) {
    const abs = path.join(dir, entry.name);
    if (entry.isDirectory()) out.push(...(await walkFiles(root, abs)));
    else if (entry.isFile()) out.push({ abs, rel: path.relative(root, abs).split(path.sep).join('/') });
  }
  return out;
}

/** A hashed note file plus what the note builder needs beyond the artifact fields. */
interface ScannedFile {
  file: NoteFile;
  /** Raw bytes, or null when the file is over the cap (then it is not stored either). */
  content: Buffer | null;
  /** Modification time in ms, only consulted when `notes.useMtime` is on. */
  mtimeMs: number;
}

/**
 * Domain separator for note-blob hashes, mirroring git's own `blob <len>\0<bytes>` prefix.
 *
 * Notes and repo files share ONE content-addressed store, and repo keys are git object shas.
 * Hashing note bytes bare would put both in the same namespace over different pre-images, so a
 * note file whose raw bytes happen to be `blob <len>\0<content>` would hash to the git sha of
 * `<content>` and — notes are merged into the map last — overwrite that repo file's stored
 * bytes. Prefixing with a distinct domain makes the two namespaces provably disjoint: no note
 * key can ever be produced from a `blob …` pre-image.
 */
const NOTE_HASH_DOMAIN = 'note';

/** The pre-image header a note blob is hashed over: `note <byteLength>\0`. */
function noteHashHeader(byteLength: number): Buffer {
  return Buffer.from(`${NOTE_HASH_DOMAIN} ${byteLength}\0`, 'utf8');
}

/**
 * Hash and classify one file exactly the way `scanTree` classifies a repo blob: same binary
 * heuristic, same "within the cap ⇒ stored, binary included" rule, same language map. The
 * only difference is the hash — `sha1('note <len>\0' + bytes)` rather than git's
 * `sha1('blob <len>\0' + bytes)`, see `NOTE_HASH_DOMAIN`.
 *
 * Oversized files are streamed so a huge note file cannot blow up the build's memory: the sha
 * still covers every byte, and only the first 8 KB are kept for the binary sniff. The header
 * needs the byte length up front, which for the streamed path comes from `stat.size`; a file
 * that changes size mid-build would produce a header that disagrees with its own content, so
 * the streamed length is verified below and the file dropped if it moved.
 */
async function scanFile(found: FoundFile, maxFileBytes: number): Promise<ScannedFile | null> {
  let stat: fs.Stats;
  try {
    stat = await fsp.stat(found.abs);
  } catch {
    return null; // vanished or unreadable between the walk and here
  }
  const name = found.rel.slice(found.rel.lastIndexOf('/') + 1);
  const hash = createHash('sha1');
  let content: Buffer | null = null;
  let size: number;
  let binary: boolean;

  if (stat.size <= maxFileBytes) {
    try {
      content = await fsp.readFile(found.abs);
    } catch {
      return null;
    }
    size = content.length;
    hash.update(noteHashHeader(size));
    hash.update(content);
    binary = looksBinary(content);
  } else {
    const prefix: Buffer[] = [];
    let prefixBytes = 0;
    size = 0;
    hash.update(noteHashHeader(stat.size));
    try {
      for await (const chunk of fs.createReadStream(found.abs)) {
        const buf = chunk as Buffer;
        hash.update(buf);
        size += buf.length;
        if (prefixBytes < BINARY_SNIFF_BYTES) {
          prefix.push(buf);
          prefixBytes += buf.length;
        }
      }
    } catch {
      return null;
    }
    if (size !== stat.size) return null; // the file changed under us; its sha would be a lie
    binary = looksBinary(Buffer.concat(prefix));
  }

  const tooLarge = content === null;
  return {
    file: {
      name,
      path: found.rel,
      sha: hash.digest('hex'),
      size,
      binary,
      tooLarge,
      stored: !tooLarge,
      language: detectLanguage(found.rel),
      markdown: isMarkdownPath(found.rel),
    },
    content,
    mtimeMs: stat.mtimeMs,
  };
}

/* ---- note assembly -------------------------------------------------------- */

/** Where a folder note's frontmatter is read from, in priority order (lower-cased names). */
const FRONTMATTER_FILES = ['index.md', 'index.markdown', 'readme.md', 'readme.markdown'];

/** The file a folder note takes its frontmatter from: `index.md`, else `README.md`, at its root. */
function frontmatterFileFor(files: ScannedFile[]): ScannedFile | null {
  for (const candidate of FRONTMATTER_FILES) {
    const hit = files.find((f) => f.file.path.toLowerCase() === candidate);
    if (hit) return hit;
  }
  return null;
}

/** Text of a scanned file, or null when it is binary or was too large to keep. */
function textOf(scanned: ScannedFile | null): string | null {
  if (!scanned || !scanned.content || scanned.file.binary) return null;
  return scanned.content.toString('utf8');
}

/** Prose of a scanned markdown file — its text with any frontmatter block removed. */
function bodyOf(scanned: ScannedFile | null): string | null {
  const text = textOf(scanned);
  return text === null ? null : parseFrontmatter(text).body;
}

/**
 * Build one note from its scanned files. `entryName` is the file or folder name as it appears
 * in `notes.dir`; `slug` has already been de-duplicated by the caller.
 */
function buildNote(
  entryName: string,
  slug: string,
  kind: 'file' | 'folder',
  files: ScannedFile[],
  useMtime: boolean,
): Note {
  const sorted = [...files].sort((a, b) => (a.file.path < b.file.path ? -1 : a.file.path > b.file.path ? 1 : 0));

  // Frontmatter: the file itself for a single-file note, `index.md`/`README.md` for a folder.
  // Other files in a folder keep their frontmatter as content — the docs are explicit about it.
  const fmFile = kind === 'file' ? (sorted[0]!.file.markdown ? sorted[0]! : null) : frontmatterFileFor(sorted);
  const fmText = textOf(fmFile);
  const fm = fmText === null ? { data: {}, body: '' } : parseFrontmatter(fmText);

  // H1 fallback: the frontmatter file when there is one, else the first markdown file by path.
  // Either way the scan runs on PROSE, never on raw file text: a `#` line inside a frontmatter
  // block is a YAML comment, and `firstHeading` would happily promote it to the note's title.
  const headingFile = fmFile ?? sorted.find((f) => f.file.markdown) ?? null;
  const headingText = headingFile === fmFile ? fm.body : bodyOf(headingFile);

  const fmTitle = typeof fm.data.title === 'string' ? fm.data.title.trim() : '';
  const heading = headingText ? firstHeading(headingText) : null;
  const title = fmTitle || heading || humaniseName(entryName) || slug;

  const fmDescription = typeof fm.data.description === 'string' ? fm.data.description.trim() : '';
  const rawTags = fm.data.tags;
  const tags: string[] = [];
  for (const tag of Array.isArray(rawTags) ? rawTags : typeof rawTags === 'string' ? [rawTags] : []) {
    const t = tag.trim();
    if (t && !tags.includes(t)) tags.push(t);
  }

  let date = typeof fm.data.date === 'string' ? normaliseNoteDate(fm.data.date) : null;
  if (date === null && useMtime && sorted.length > 0) {
    // Newest file in the note, so editing any file in a folder note refreshes its date.
    const newest = sorted.reduce((max, f) => Math.max(max, f.mtimeMs), 0);
    date = new Date(newest).toISOString().replace(/\.\d{3}Z$/, 'Z');
  }

  return {
    slug,
    title,
    description: fmDescription || null,
    tags,
    date,
    kind,
    files: sorted.map((f) => f.file),
    totalBytes: sorted.reduce((sum, f) => sum + f.file.size, 0),
  };
}

/**
 * Read `notes.dir` into notes + blobs. Never throws for a missing or unreadable folder — that
 * is a `notes-dir-missing` warning and an empty result, because one bad path must not fail a
 * build (same contract as every other ingest source).
 *
 * @param config Resolved site config; supplies `notesDir` and the `notes.*` settings.
 * @param options Per-call overrides, used by the unit tests to point at a temp folder.
 */
export async function collectNotes(
  config: ResolvedConfig,
  options: CollectNotesOptions = {},
): Promise<CollectNotesResult> {
  const dir = options.dir ?? config.notesDir;
  const maxFileBytes = options.maxFileBytes ?? config.notes.maxFileBytes ?? config.ingest.maxBlobBytes;
  const useMtime = options.useMtime ?? config.notes.useMtime;

  const blobs = new Map<string, Buffer>();
  const warnings: Warning[] = [];

  let entries: fs.Dirent[];
  try {
    const stat = await fsp.stat(dir);
    if (!stat.isDirectory()) throw new Error('not a directory');
    entries = await sortedEntries(dir);
  } catch {
    // Only warn when the folder was actually asked for: either the caller named one, or the
    // user config declared a `notes` block. `notes.dir` is defaulted, so warning unconditionally
    // would put a permanent "1 ingest warning" in the footer of every site that has no notes.
    if (options.dir !== undefined || config.notesConfigured) {
      warnings.push({
        code: 'notes-dir-missing',
        repo: null,
        message: `notes directory ${dir} does not exist or is not readable; no notes were collected`,
      });
    }
    return { notes: [], blobs, warnings };
  }

  const notes: Note[] = [];
  const taken = new Set<string>();

  for (const entry of entries) {
    const abs = path.join(dir, entry.name);
    const kind: 'file' | 'folder' | null = entry.isFile() ? 'file' : entry.isDirectory() ? 'folder' : null;
    if (kind === null) continue; // symlinks, sockets, … — see walkFiles

    let scanned: ScannedFile[];
    if (kind === 'file') {
      const one = await scanFile({ abs, rel: entry.name }, maxFileBytes);
      if (!one) continue;
      scanned = [one];
    } else {
      scanned = [];
      for (const found of await walkFiles(abs)) {
        const file = await scanFile(found, maxFileBytes);
        if (file) scanned.push(file);
      }
      // "Only files are notes; there is nothing to collect from an empty folder" — data-model.md.
      if (scanned.length === 0) continue;
    }

    // Slug de-duplication happens in walk order, so which note keeps the bare slug is stable.
    const base = noteSlug(entry.name);
    let slug = base;
    for (let n = 2; taken.has(slug); n++) slug = `${base}-${n}`;
    if (slug !== base) {
      warnings.push({
        code: 'note-slug-collision',
        repo: null,
        message: `note '${entry.name}' resolves to slug '${base}', already taken; using '${slug}'`,
      });
    }
    taken.add(slug);

    // A note file whose name carries `#` or `%` can be shown but not *served*: see
    // `isRawServable`. Warn once per file, in walk order, so the author learns why the Raw and
    // Download buttons are missing instead of finding a dead link.
    for (const f of scanned) {
      if (f.file.stored && !isRawServable(f.file.path)) {
        warnings.push({
          code: 'note-file-unservable',
          repo: null,
          message:
            `note '${slug}' file '${f.file.path}' contains '#' or '%', which a static raw URL ` +
            `cannot round-trip; it renders on the note page but has no raw/download link`,
        });
      }
    }

    for (const f of scanned) if (f.file.stored && f.content) blobs.set(f.file.sha, f.content);
    notes.push(buildNote(entry.name, slug, kind, scanned, useMtime));
  }

  notes.sort(compareNotes);
  return { notes, blobs, warnings };
}
