/**
 * YAML frontmatter for note markdown: where the block ends, and the flat subset of YAML the
 * keys inside it may use.
 *
 * This lives outside `src/lib/ingest/` on purpose. Both halves of the notes feature need the
 * *same* answer to "where does the frontmatter block stop": ingest reads the keys out of it,
 * and `NoteFileView.astro` has to hide exactly the same span before rendering the body as
 * markdown. While the two carried separate regexes they drifted — a block closed with YAML's
 * `...` terminator parsed correctly during ingest and then rendered its own metadata as prose
 * on the page. One implementation, imported by both, makes that class of bug unreachable.
 *
 * Dependency-free and browser-safe (no node built-ins), like `format.ts` and `listing.ts`:
 * an Astro component may import it without dragging the ingest pipeline into the site build.
 */

/**
 * A frontmatter value. Only strings and string lists are produced: the artifact needs
 * `title`/`description`/`date` (strings) and `tags` (a list), and everything else is skipped
 * rather than guessed at.
 */
export type FrontmatterValue = string | string[];

/** Result of splitting a markdown file into its frontmatter block and the rest. */
export interface Frontmatter {
  /** Keys the parser understood. Unsupported constructs are absent, never partially parsed. */
  data: Record<string, FrontmatterValue>;
  /**
   * Everything after the closing delimiter, joined with `\n`. Only used to look for an H1 and
   * to render the preview, so normalising line endings here costs nothing — the stored blob is
   * always the raw bytes.
   */
  body: string;
}

/** A frontmatter block located inside a file, before its keys are parsed. */
export interface FrontmatterSplit {
  /**
   * The lines between the delimiters, joined with `\n`; empty when the file has no
   * frontmatter block at all. Never includes the `---` / `...` delimiter lines.
   */
  raw: string;
  /** Everything after the closing delimiter, joined with `\n`; the whole file when there is none. */
  body: string;
  /** False when the file opens with no `---`, or opens one that is never closed. */
  present: boolean;
}

/** YAML closes a document with `---` (a new one starts) or `...` (this one ends). */
function isCloser(trimmed: string): boolean {
  return trimmed === '---' || trimmed === '...';
}

/**
 * Split a markdown file into its leading YAML frontmatter block and everything after it.
 *
 * A file whose first line is not `---`, or whose block is never closed, is treated as having
 * no frontmatter at all: its whole text is the body. Silently swallowing an unterminated block
 * would hide the author's typo behind an empty-looking note.
 *
 * @param src File text (a leading UTF-8 BOM is tolerated, and stripped from `body`).
 */
export function splitFrontmatter(src: string): FrontmatterSplit {
  const text = src.charCodeAt(0) === 0xfeff ? src.slice(1) : src;
  const lines = text.split(/\r\n|\n|\r/);
  if (lines.length === 0 || lines[0]!.trim() !== '---') return { raw: '', body: text, present: false };

  let end = -1;
  for (let i = 1; i < lines.length; i++) {
    if (isCloser(lines[i]!.trim())) {
      end = i;
      break;
    }
  }
  if (end === -1) return { raw: '', body: text, present: false };
  return { raw: lines.slice(1, end).join('\n'), body: lines.slice(end + 1).join('\n'), present: true };
}

/* ---- the supported YAML subset -------------------------------------------- */

/** A YAML comment starts at a `#` that follows whitespace (or begins the line). */
function stripPlainComment(value: string): string {
  const m = /(^|\s)#/.exec(value);
  return m ? value.slice(0, m.index + (m[1] ? m[1].length : 0)) : value;
}

/**
 * Parse one scalar: a double-quoted string (with `\\`, `\"`, `\n`, `\r`, `\t` escapes), a
 * single-quoted string (`''` escapes a quote), or a plain scalar with any trailing comment
 * removed. Returns null for an empty value or an unterminated quote — the caller then drops
 * the key instead of inventing a value.
 */
function parseScalar(raw: string): string | null {
  const value = raw.trim();
  if (!value) return null;
  if (value.startsWith('"')) {
    let out = '';
    for (let i = 1; i < value.length; i++) {
      const ch = value[i]!;
      if (ch === '\\') {
        const next = value[++i];
        if (next === undefined) return null;
        out += next === 'n' ? '\n' : next === 'r' ? '\r' : next === 't' ? '\t' : next;
        continue;
      }
      if (ch === '"') return out;
      out += ch;
    }
    return null; // unterminated
  }
  if (value.startsWith("'")) {
    let out = '';
    for (let i = 1; i < value.length; i++) {
      const ch = value[i]!;
      if (ch === "'") {
        if (value[i + 1] === "'") {
          out += "'";
          i++;
          continue;
        }
        return out;
      }
      out += ch;
    }
    return null; // unterminated
  }
  const plain = stripPlainComment(value).trim();
  return plain.length ? plain : null;
}

/**
 * Split a flow sequence body (`a, "b, c", d`) on commas that are not inside quotes. Returns
 * null when a nested collection appears — a list of lists or maps is outside the supported
 * subset.
 */
function splitFlowItems(body: string): string[] | null {
  const items: string[] = [];
  let current = '';
  let quote: '"' | "'" | null = null;
  for (let i = 0; i < body.length; i++) {
    const ch = body[i]!;
    if (quote) {
      current += ch;
      if (ch === '\\' && quote === '"') {
        const next = body[++i];
        if (next !== undefined) current += next;
        continue;
      }
      if (ch === quote) quote = null;
      continue;
    }
    if (ch === '"' || ch === "'") {
      quote = ch;
      current += ch;
      continue;
    }
    if (ch === '[' || ch === '{' || ch === ']' || ch === '}') return null;
    if (ch === ',') {
      items.push(current);
      current = '';
      continue;
    }
    current += ch;
  }
  if (quote) return null; // unterminated quote
  items.push(current);
  return items;
}

/**
 * `[a, b]` → `['a', 'b']`; null when the value is not a flat flow sequence. A trailing comment
 * is allowed (`[a, b] # why`): the closing bracket is taken to be the last `]` on the line, so
 * a `#` inside a quoted item does not truncate the list.
 */
function parseFlowSequence(raw: string): string[] | null {
  const close = raw.lastIndexOf(']');
  if (close === -1) return null;
  const rest = raw.slice(close + 1).trim();
  if (rest && !rest.startsWith('#')) return null;
  const inner = raw.slice(1, close);
  const parts = splitFlowItems(inner);
  if (parts === null) return null;
  const out: string[] = [];
  for (const part of parts) {
    const item = parseScalar(part);
    if (item !== null) out.push(item);
  }
  return out;
}

/** A block-sequence item that is itself a mapping (`- name: x`) — outside the supported subset. */
const NESTED_ITEM = /^[A-Za-z_][\w.-]*\s*:(\s|$)/;

/**
 * A block-sequence item that opens a nested collection (`- [a, b]`, `- {k: v}`).
 *
 * Checked separately from `NESTED_ITEM` because a flow collection is still one "item" to the
 * line splitter: without this it would be captured as the plain scalar `"[a, b]"` and shown to
 * a reader as a tag chip reading `[a, b]`, which is precisely the partial parse this file's
 * contract rules out.
 */
function opensCollection(value: string): boolean {
  return value.startsWith('[') || value.startsWith('{');
}

/** `key:` or `key: value` at the top level of the block (no leading indentation). */
const KEY_LINE = /^([A-Za-z_][\w.-]*)[ \t]*:(?:[ \t]+(.*))?$/;

/** `- item`, at any indentation. */
const ITEM_LINE = /^[ \t]*-[ \t]+(.*)$/;

/**
 * Parse the flat YAML subset frznforge's note frontmatter uses: `key: scalar`, `key: [a, b]`
 * and `key:` followed by `- item` lines. Quoted scalars, comments and CRLF line endings are
 * handled; anything else (nested mappings, anchors, block scalars, multi-document files) makes
 * the parser drop that key rather than guess at its meaning.
 *
 * @param src File text (a leading UTF-8 BOM is tolerated).
 */
export function parseFrontmatter(src: string): Frontmatter {
  const { raw, body, present } = splitFrontmatter(src);
  if (!present) return { data: {}, body };

  const lines = raw.split('\n');
  const data: Record<string, FrontmatterValue> = {};
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]!;
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith('#')) continue;
    const m = KEY_LINE.exec(line);
    if (!m) continue; // indented continuation, list item without a key, or anything fancier
    const key = m[1]!;
    const rawValue = (m[2] ?? '').trim();

    if (rawValue && !rawValue.startsWith('#')) {
      if (rawValue.startsWith('{')) {
        continue; // flow mapping — outside the supported subset
      } else if (rawValue.startsWith('[')) {
        const list = parseFlowSequence(rawValue);
        if (list) data[key] = list;
      } else {
        const scalar = parseScalar(rawValue);
        if (scalar !== null) data[key] = scalar;
      }
      continue;
    }

    // `key:` with nothing after it — a block sequence, or something unsupported. Either way the
    // indented lines belong to this key and must not be re-read as keys of their own.
    const items: string[] = [];
    let supported = true;
    let j = i + 1;
    for (; j < lines.length; j++) {
      const next = lines[j]!;
      const nextTrimmed = next.trim();
      if (!nextTrimmed || nextTrimmed.startsWith('#')) continue;
      const item = ITEM_LINE.exec(next);
      if (!item) {
        if (/^\s/.test(next)) supported = false; // indented, but not `- item`: a nested mapping
        break;
      }
      const value = item[1]!.trim();
      if (NESTED_ITEM.test(value) || opensCollection(value)) {
        supported = false;
        break;
      }
      const scalar = parseScalar(value);
      if (scalar !== null) items.push(scalar);
    }
    // Consume the block either way: on the unsupported path its lines must not become keys.
    while (j < lines.length && /^\s/.test(lines[j]!) && lines[j]!.trim()) j++;
    i = j - 1;
    if (supported && items.length > 0) data[key] = items;
  }

  return { data, body };
}

/* ---- headings -------------------------------------------------------------- */

/** Fence openers/closers in a markdown body; H1s inside a code block are not headings. */
const FENCE = /^\s{0,3}(```+|~~~+)/;

/**
 * The text of the first ATX H1 (`# Title`) outside a fenced code block, or null. Setext
 * headings (`Title` over `====`) are deliberately not recognised: they are rare in note
 * frontmatter-style files and the two-line lookahead is one more thing to get wrong.
 *
 * @param body Prose only — pass `parseFrontmatter(src).body`, never the raw file. A `#` line
 *   inside a frontmatter block is a YAML comment, and scanning raw text turns it into a title.
 */
/**
 * A document-opening level-1 heading, ATX (`# Title`) or setext (`Title` over `===`), with the
 * heading's own text captured in group 1 or 2.
 */
const LEADING_H1 = /^[ \t]*(?:\r?\n)*(?:#[ \t]+([^\r\n]*?)[ \t]*#*[ \t]*(?:\r?\n|$)|([^\r\n]+)\r?\n=+[ \t]*(?:\r?\n|$))/;

/**
 * Drop a document-opening H1 from a markdown body, but ONLY when it is `title` repeating
 * itself. Returns `body` unchanged otherwise.
 *
 * A note page prints the note's title as the page's one `<h1>`, and when that title came from
 * the H1 fallback (frontmatter → first H1 → humanised name) rendering the heading again echoes
 * the title immediately under itself. That is the only case worth removing. Removing the first
 * heading unconditionally deletes real content instead — a file whose frontmatter set a
 * different `title`, or the second markdown file of a folder note, loses its opening heading
 * from the reading view with nothing to show it was ever there.
 *
 * @param body Prose (`splitFrontmatter(src).body`), not the raw file.
 * @param title The heading text that counts as a duplicate; compared trimmed.
 */
export function stripLeadingHeading(body: string, title: string): string {
  const m = LEADING_H1.exec(body);
  if (!m) return body;
  const headingText = (m[1] ?? m[2] ?? '').trim();
  return headingText && headingText === title.trim() ? body.slice(m[0].length) : body;
}

export function firstHeading(body: string): string | null {
  let fence: string | null = null;
  for (const raw of body.split(/\r\n|\n|\r/)) {
    const fenceMatch = FENCE.exec(raw);
    if (fenceMatch) {
      const marker = fenceMatch[1]![0]!;
      if (fence === null) fence = marker;
      else if (fence === marker) fence = null;
      continue;
    }
    if (fence !== null) continue;
    const m = /^\s{0,3}#[ \t]+(.+?)[ \t]*#*\s*$/.exec(raw);
    if (m) {
      const title = m[1]!.trim();
      if (title) return title;
    }
  }
  return null;
}
