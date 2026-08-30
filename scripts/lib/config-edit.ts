/**
 * Textual editing of frznforge.config.ts (0.2.0, the wizard's engine).
 *
 * The contract is inherited from `insertRepos` in scripts/cli.ts and is load-bearing: the
 * config file is the user's hand-written TypeScript, roughly 60% comments, with expression
 * values like `512 * 1024`. Re-serialising it would destroy all of that, so every editor
 * here is a SPLICE — only the bytes of the field actually being changed move, and
 * everything else (comments, formatting, expressions in untouched fields) survives
 * byte-for-byte. Navigation is comment- and string-aware (the walkers below), never plain
 * regex over the whole file.
 *
 * The primitives (`matchBracket`, `stripComments`, …) moved here from scripts/cli.ts —
 * which re-exports them — so the repo picker and the wizard's field editors share one
 * implementation.
 */

/* ------------------------------------------------------------------ string rendering */

/**
 * Single-quoted JS string literal, matching the style of frznforge.config.ts.
 *
 * Line terminators matter as much as quotes here: a raw `\n` inside `'…'` is a syntax
 * error in JS/TS, so a value with a newline in it would leave the user's config
 * unparseable — the damage is not injection (the quote escaping holds) but a config that
 * no longer builds. U+2028/U+2029 terminate a line for older parsers, so they go too.
 */
export function quote(value: string): string {
  const escaped = value
    .replace(/\\/g, '\\\\')
    .replace(/'/g, "\\'")
    .replace(/\n/g, '\\n')
    .replace(/\r/g, '\\r')
    .replace(/\u2028/g, '\\u2028')
    .replace(/\u2029/g, '\\u2029');
  return `'${escaped}'`;
}

const IDENTIFIER = /^[A-Za-z_$][\w$]*$/;

/**
 * Render a JSON-shaped value as one line of config source. Strings go through `quote`;
 * object keys must be plain identifiers (every key the wizard writes is a schema key).
 */
export function renderValue(value: unknown): string {
  if (value === null) return 'null';
  if (typeof value === 'boolean' || typeof value === 'number') return String(value);
  if (typeof value === 'string') return quote(value);
  if (Array.isArray(value)) return `[${value.map(renderValue).join(', ')}]`;
  if (typeof value === 'object') {
    const parts = Object.entries(value as Record<string, unknown>)
      .filter(([, v]) => v !== undefined)
      .map(([k, v]) => {
        if (!IDENTIFIER.test(k)) throw new Error(`not a plain identifier key: ${JSON.stringify(k)}`);
        return `${k}: ${renderValue(v)}`;
      });
    return parts.length === 0 ? '{}' : `{ ${parts.join(', ')} }`;
  }
  throw new Error(`cannot render a ${typeof value} into config source`);
}

/* ------------------------------------------------------------------ source walkers */

/** Walk `text` from `open` (an opening bracket) to its match, skipping strings and comments. */
export function matchBracket(text: string, open: number): number {
  const pairs: Record<string, string> = { '[': ']', '{': '}', '(': ')' };
  const closer = pairs[text[open]!];
  if (!closer) return -1;
  let depth = 0;
  for (let i = open; i < text.length; i += 1) {
    const ch = text[i]!;
    const next = text[i + 1];
    if (ch === '/' && next === '/') {
      const nl = text.indexOf('\n', i);
      i = nl === -1 ? text.length : nl;
      continue;
    }
    if (ch === '/' && next === '*') {
      const end = text.indexOf('*/', i + 2);
      i = end === -1 ? text.length : end + 1;
      continue;
    }
    if (ch === "'" || ch === '"' || ch === '`') {
      for (let j = i + 1; j < text.length; j += 1) {
        if (text[j] === '\\') {
          j += 1;
          continue;
        }
        if (text[j] === ch) {
          i = j;
          break;
        }
        if (j === text.length - 1) i = j;
      }
      continue;
    }
    if (ch === '[' || ch === '{' || ch === '(') depth += 1;
    else if (ch === ']' || ch === '}' || ch === ')') {
      depth -= 1;
      if (depth === 0) return ch === closer ? i : -1;
    }
  }
  return -1;
}

/** Index of the last character in `body` that is neither whitespace nor part of a comment. */
export function lastMeaningfulIndex(body: string): number {
  let last = -1;
  for (let i = 0; i < body.length; i += 1) {
    const ch = body[i]!;
    const next = body[i + 1];
    if (ch === '/' && next === '/') {
      const nl = body.indexOf('\n', i);
      i = nl === -1 ? body.length : nl;
      continue;
    }
    if (ch === '/' && next === '*') {
      const end = body.indexOf('*/', i + 2);
      i = end === -1 ? body.length : end + 1;
      continue;
    }
    if (ch === "'" || ch === '"' || ch === '`') {
      for (let j = i + 1; j < body.length; j += 1) {
        if (body[j] === '\\') {
          j += 1;
          continue;
        }
        if (body[j] === ch) {
          i = j;
          break;
        }
        if (j === body.length - 1) i = j;
      }
      last = i;
      continue;
    }
    if (!/\s/.test(ch)) last = i;
  }
  return last;
}

/** One top-level element of an array/object body, with the span it occupies. */
export interface ItemSpan {
  /** Start of the element's slice (right after the previous top-level comma, or 0). */
  start: number;
  /** End of the slice (index of the next top-level comma, or body.length). */
  end: number;
  /** Index of the trailing top-level comma, or -1 when this is the last, comma-less item. */
  comma: number;
  text: string;
}

/**
 * Split an array/object body into its top-level elements with byte spans, skipping strings
 * and comments. The spans are what make `removeFromArray` a splice: a removed item takes
 * exactly its own bytes (plus one separating comma) with it.
 */
export function topLevelItemSpans(body: string): ItemSpan[] {
  const spans: ItemSpan[] = [];
  let depth = 0;
  let start = 0;
  const push = (end: number, comma: number) => {
    const text = body.slice(start, end);
    if (text.trim() !== '') spans.push({ start, end, comma, text });
  };
  for (let i = 0; i < body.length; i += 1) {
    const ch = body[i]!;
    const next = body[i + 1];
    if (ch === '/' && next === '/') {
      const nl = body.indexOf('\n', i);
      i = nl === -1 ? body.length : nl;
      continue;
    }
    if (ch === '/' && next === '*') {
      const end = body.indexOf('*/', i + 2);
      i = end === -1 ? body.length : end + 1;
      continue;
    }
    if (ch === "'" || ch === '"' || ch === '`') {
      for (let j = i + 1; j < body.length; j += 1) {
        if (body[j] === '\\') {
          j += 1;
          continue;
        }
        if (body[j] === ch) {
          i = j;
          break;
        }
        if (j === body.length - 1) i = j;
      }
      continue;
    }
    if (ch === '[' || ch === '{' || ch === '(') depth += 1;
    else if (ch === ']' || ch === '}' || ch === ')') depth -= 1;
    else if (ch === ',' && depth === 0) {
      push(i, i);
      start = i + 1;
    }
  }
  push(body.length, -1);
  return spans;
}

/** Split an array body into its top-level elements (text only — see `topLevelItemSpans`). */
export function topLevelItems(body: string): string[] {
  return topLevelItemSpans(body).map((s) => s.text);
}

/**
 * Strip line and block comments out of one array element, leaving string literals intact.
 *
 * `topLevelItems` skips comments when it looks for separators, but the slice it hands back
 * still contains them — and `readField` takes the *first* match, so a commented-out entry
 * sitting above a real one is read instead of the real one. frznforge.config.ts ships with
 * comments inside `repos: [ … ]`, so this is the normal case.
 */
export function stripComments(text: string): string {
  let out = '';
  for (let i = 0; i < text.length; i += 1) {
    const ch = text[i]!;
    const next = text[i + 1];
    if (ch === '/' && next === '/') {
      const nl = text.indexOf('\n', i);
      if (nl === -1) return out;
      out += '\n';
      i = nl;
      continue;
    }
    if (ch === '/' && next === '*') {
      const end = text.indexOf('*/', i + 2);
      if (end === -1) return out;
      out += ' ';
      i = end + 1;
      continue;
    }
    if (ch === "'" || ch === '"' || ch === '`') {
      let j = i + 1;
      for (; j < text.length; j += 1) {
        if (text[j] === '\\') {
          j += 1;
          continue;
        }
        if (text[j] === ch) break;
      }
      out += text.slice(i, Math.min(j, text.length - 1) + 1);
      i = j;
      continue;
    }
    out += ch;
  }
  return out;
}

/** Pull `key: 'value'` (single or double quoted) out of one array element's source text. */
export function readField(item: string, key: string): string | undefined {
  const m = new RegExp(`\\b${key}\\s*:\\s*(['"\`])([^'"\`]*)\\1`).exec(item);
  return m ? m[2] : undefined;
}

/* ------------------------------------------------------------------ object navigation */

interface KeyRange {
  /** Absolute index of the key token's first character. */
  keyStart: number;
  /** Absolute index of the value's first character. */
  valueStart: number;
  /** Absolute index one past the value's last meaningful character. */
  valueEnd: number;
}

/**
 * Find `key:` at the TOP level of the object body `source[open+1 .. close)` and return the
 * key + value spans. Comment- and string-aware; a `key:` inside a nested object, array,
 * string or comment never matches.
 */
function findKeyRange(source: string, open: number, close: number, key: string): KeyRange | null {
  let depth = 0;
  let expectKey = true; // at body start and after each top-level comma
  for (let i = open + 1; i < close; i += 1) {
    const ch = source[i]!;
    const next = source[i + 1];
    if (ch === '/' && next === '/') {
      const nl = source.indexOf('\n', i);
      i = nl === -1 ? close : Math.min(nl, close);
      continue;
    }
    if (ch === '/' && next === '*') {
      const end = source.indexOf('*/', i + 2);
      i = end === -1 ? close : Math.min(end + 1, close);
      continue;
    }
    if (ch === "'" || ch === '"' || ch === '`') {
      for (let j = i + 1; j < close; j += 1) {
        if (source[j] === '\\') {
          j += 1;
          continue;
        }
        if (source[j] === ch) {
          i = j;
          break;
        }
        if (j === close - 1) i = j;
      }
      continue;
    }
    if (ch === '[' || ch === '{' || ch === '(') {
      depth += 1;
      continue;
    }
    if (ch === ']' || ch === '}' || ch === ')') {
      depth -= 1;
      continue;
    }
    if (ch === ',' && depth === 0) {
      expectKey = true;
      continue;
    }
    if (depth === 0 && expectKey && !/\s/.test(ch)) {
      // A key token starts here: `key:` or `'key':`. Anything else (a spread, a computed
      // key) just flips expectKey off until the next comma.
      const rest = source.slice(i, close);
      const m = /^(['"]?)([A-Za-z_$][\w$-]*)\1\s*:/.exec(rest);
      expectKey = false;
      if (m && m[2] === key) {
        const keyStart = i;
        let valueStart = i + m[0].length;
        while (valueStart < close && /\s/.test(source[valueStart]!)) valueStart += 1;
        const valueEnd = findValueEnd(source, valueStart, close);
        return { keyStart, valueStart, valueEnd };
      }
      // skip the rest of this token so a key containing the search key as a prefix cannot
      // partially re-match
      if (m) i += m[0].length - 1;
    }
  }
  return null;
}

/** One past the value's last meaningful character, walking to the next top-level comma. */
function findValueEnd(source: string, valueStart: number, close: number): number {
  let depth = 0;
  let i = valueStart;
  for (; i < close; i += 1) {
    const ch = source[i]!;
    const next = source[i + 1];
    if (ch === '/' && next === '/') {
      const nl = source.indexOf('\n', i);
      i = nl === -1 ? close : Math.min(nl, close);
      continue;
    }
    if (ch === '/' && next === '*') {
      const end = source.indexOf('*/', i + 2);
      i = end === -1 ? close : Math.min(end + 1, close);
      continue;
    }
    if (ch === "'" || ch === '"' || ch === '`') {
      for (let j = i + 1; j < close; j += 1) {
        if (source[j] === '\\') {
          j += 1;
          continue;
        }
        if (source[j] === ch) {
          i = j;
          break;
        }
        if (j === close - 1) i = j;
      }
      continue;
    }
    if (ch === '[' || ch === '{' || ch === '(') depth += 1;
    else if (ch === ']' || ch === '}' || ch === ')') depth -= 1;
    else if (ch === ',' && depth === 0) break;
  }
  const slice = source.slice(valueStart, i);
  const last = lastMeaningfulIndex(slice);
  return last === -1 ? valueStart : valueStart + last + 1;
}

/**
 * The root `defineConfig({ … })` object's brace span, or null.
 *
 * The match is comment- and string-aware: a `defineConfig({` that only appears inside a
 * comment or a string literal (the classic case — an old config block commented out above the
 * real one) is skipped, so edits never land in commented-out bytes. This is the walker the
 * module header's "never plain regex over the whole file" rule is about.
 */
function findRootObject(source: string): { open: number; close: number } | null {
  const needle = /defineConfig\s*\(\s*\{/y;
  for (let i = 0; i < source.length; i += 1) {
    const ch = source[i]!;
    const next = source[i + 1];
    if (ch === '/' && next === '/') {
      const nl = source.indexOf('\n', i);
      if (nl === -1) return null;
      i = nl;
      continue;
    }
    if (ch === '/' && next === '*') {
      const end = source.indexOf('*/', i + 2);
      if (end === -1) return null;
      i = end + 1;
      continue;
    }
    if (ch === "'" || ch === '"' || ch === '`') {
      let j = i + 1;
      for (; j < source.length; j += 1) {
        if (source[j] === '\\') {
          j += 1;
          continue;
        }
        if (source[j] === ch) break;
      }
      i = j;
      continue;
    }
    // Only anchor at a word boundary so `myDefineConfig(` cannot match.
    if (ch === 'd' && (i === 0 || !/[\w$]/.test(source[i - 1]!))) {
      needle.lastIndex = i;
      const m = needle.exec(source);
      if (m) {
        const open = i + m[0].length - 1;
        const close = matchBracket(source, open);
        return close === -1 ? null : { open, close };
      }
    }
  }
  return null;
}

/** Insert `line` (already rendered, no indent, no comma) into the body `open..close`. */
function appendToBody(source: string, open: number, close: number, line: string, indent: string): string {
  const body = source.slice(open + 1, close);
  const last = lastMeaningfulIndex(body);
  let newBody: string;
  if (last === -1) {
    newBody = `\n${indent}${line},\n${indent.slice(0, Math.max(0, indent.length - 2))}`;
  } else {
    const head = body.slice(0, last + 1);
    const comma = body[last] === ',' ? '' : ',';
    const tail = body.slice(last + 1);
    const trailing = /\s*$/.exec(tail)![0];
    const keep = tail.slice(0, tail.length - trailing.length);
    const closeGap = trailing.includes('\n') ? trailing : `\n${indent.slice(0, Math.max(0, indent.length - 2))}`;
    newBody = `${head}${comma}${keep}\n${indent}${line}${closeGap}`;
  }
  return `${source.slice(0, open + 1)}${newBody}${source.slice(close)}`;
}

export interface EditResult {
  text: string;
  changed: boolean;
}

/**
 * Set one field of the config object to `valueSource` (already-rendered config source, from
 * `renderValue`). `path` is the key chain from the root, e.g. `['theme', 'heat', 'hot']`.
 *
 * Only the field's own value bytes are replaced; a trailing same-line comment after the
 * value survives, and so does every other byte of the file. Missing intermediate blocks
 * (and the leaf) are created — appended at the end of their parent, two-space indented per
 * level, matching the shipped config's style. Returns null when the file has no
 * `defineConfig({…})` root or an intermediate key's value is not an object literal (a
 * spread or a computed value — nothing this editor can safely descend into).
 */
export function setObjectField(source: string, path: readonly string[], valueSource: string): EditResult | null {
  if (path.length === 0) return null;
  const root = findRootObject(source);
  if (!root) return null;

  let { open, close } = root;
  for (let depth = 0; depth < path.length - 1; depth += 1) {
    const key = path[depth]!;
    const range = findKeyRange(source, open, close, key);
    if (range === null) {
      // Create the whole missing chain in one append: `key: { key2: { leaf: value } }`.
      let wrapped = `${path[path.length - 1]}: ${valueSource}`;
      for (let k = path.length - 2; k >= depth; k -= 1) wrapped = `${path[k]}: { ${wrapped} }`;
      const indent = '  '.repeat(depth + 1);
      return { text: appendToBody(source, open, close, `${wrapped},`, indent), changed: true };
    }
    if (source[range.valueStart] !== '{') return null;
    open = range.valueStart;
    close = matchBracket(source, open);
    if (close === -1) return null;
  }

  const leaf = path[path.length - 1]!;
  const range = findKeyRange(source, open, close, leaf);
  if (range === null) {
    const indent = '  '.repeat(path.length);
    return { text: appendToBody(source, open, close, `${leaf}: ${valueSource},`, indent), changed: true };
  }
  const current = source.slice(range.valueStart, range.valueEnd);
  if (current === valueSource) return { text: source, changed: false };
  return {
    text: `${source.slice(0, range.valueStart)}${valueSource}${source.slice(range.valueEnd)}`,
    changed: true,
  };
}

/**
 * Append one rendered item to the array at `path` (creating the array — and any missing
 * parent blocks — when absent). Dedup is the caller's job.
 */
export function insertIntoArray(source: string, path: readonly string[], itemSource: string): EditResult | null {
  const root = findRootObject(source);
  if (!root) return null;

  let { open, close } = root;
  for (let depth = 0; depth < path.length - 1; depth += 1) {
    const range = findKeyRange(source, open, close, path[depth]!);
    if (range === null) {
      // Missing parent chain: create it with the array + item in one go.
      let wrapped = `${path[path.length - 1]}: [${itemSource}]`;
      for (let k = path.length - 2; k > depth; k -= 1) wrapped = `${path[k]}: { ${wrapped} }`;
      const indent = '  '.repeat(depth + 1);
      return { text: appendToBody(source, open, close, `${path[depth]}: { ${wrapped} },`, indent), changed: true };
    }
    if (source[range.valueStart] !== '{') return null;
    open = range.valueStart;
    close = matchBracket(source, open);
    if (close === -1) return null;
  }

  const key = path[path.length - 1]!;
  const range = findKeyRange(source, open, close, key);
  if (range === null) {
    const indent = '  '.repeat(path.length);
    return { text: appendToBody(source, open, close, `${key}: [${itemSource}],`, indent), changed: true };
  }
  if (source[range.valueStart] !== '[') return null;
  const arrOpen = range.valueStart;
  const arrClose = matchBracket(source, arrOpen);
  if (arrClose === -1) return null;
  const indent = '  '.repeat(path.length + 1);
  return { text: appendToBody(source, arrOpen, arrClose, `${itemSource},`, indent), changed: true };
}

export interface RemoveResult extends EditResult {
  removed: number;
}

/**
 * Remove every element of the array at `path` whose (comment-stripped) source carries ALL
 * of `match`'s `key: 'value'` fields. Each removed item takes exactly its own bytes plus
 * one separating comma with it; everything else stays put.
 */
export function removeFromArray(
  source: string,
  path: readonly string[],
  match: Record<string, string>,
): RemoveResult | null {
  const root = findRootObject(source);
  if (!root) return null;

  let { open, close } = root;
  for (let depth = 0; depth < path.length - 1; depth += 1) {
    const range = findKeyRange(source, open, close, path[depth]!);
    if (range === null) return { text: source, changed: false, removed: 0 };
    if (source[range.valueStart] !== '{') return null;
    open = range.valueStart;
    close = matchBracket(source, open);
    if (close === -1) return null;
  }

  const range = findKeyRange(source, open, close, path[path.length - 1]!);
  if (range === null) return { text: source, changed: false, removed: 0 };
  if (source[range.valueStart] !== '[') return null;
  const arrOpen = range.valueStart;
  const arrClose = matchBracket(source, arrOpen);
  if (arrClose === -1) return null;

  const body = source.slice(arrOpen + 1, arrClose);
  const spans = topLevelItemSpans(body);
  const doomed = spans.filter((s) => {
    const stripped = stripComments(s.text);
    return Object.entries(match).every(([k, v]) => readField(stripped, k) === v);
  });
  if (doomed.length === 0) return { text: source, changed: false, removed: 0 };

  // Delete back-to-front so earlier spans keep their offsets. A span with a trailing comma
  // takes it along; the last (comma-less) item takes the PRECEDING comma instead, so the
  // survivor before it does not end up with a dangling one.
  let newBody = body;
  for (const span of [...doomed].reverse()) {
    if (span.comma !== -1) {
      newBody = newBody.slice(0, span.start) + newBody.slice(span.comma + 1);
    } else {
      const before = newBody.slice(0, span.start);
      const prevComma = before.lastIndexOf(',');
      const cut = prevComma !== -1 && topLevelItemSpans(before).some((s) => s.comma === prevComma) ? prevComma : span.start;
      newBody = newBody.slice(0, cut) + newBody.slice(span.end);
    }
  }
  if (newBody.trim() === '') newBody = '';
  return {
    text: `${source.slice(0, arrOpen + 1)}${newBody}${source.slice(arrClose)}`,
    changed: true,
    removed: doomed.length,
  };
}

/**
 * Remove the array element at `index` (creating nothing). This is the position-safe removal the
 * wizard uses: content matching (`removeFromArray`) cannot tell two entries apart when one's
 * fields are a subset of the other's — `[{ repo: 'x' }, { repo: 'x', slug: 'y' }]` — so a
 * "remove `{ repo: 'x' }`" would delete both. The page knows exactly which row it rendered, so
 * it removes by position instead.
 *
 * `expect` is a safety net, not the selector: the caller passes the identifying fields it
 * believes live at `index`, and if the element there does not carry them all (the array was
 * reordered, or holds a spread that shifts positions between source text and evaluated value),
 * the removal is refused with `null` rather than deleting the wrong entry. Returns
 * `{ changed: false, removed: 0 }` when the path is absent, `null` when the structure cannot be
 * edited safely.
 */
export function removeArrayItemAt(
  source: string,
  path: readonly string[],
  index: number,
  expect: Record<string, string> = {},
): RemoveResult | null {
  const root = findRootObject(source);
  if (!root) return null;

  let { open, close } = root;
  for (let depth = 0; depth < path.length - 1; depth += 1) {
    const range = findKeyRange(source, open, close, path[depth]!);
    if (range === null) return { text: source, changed: false, removed: 0 };
    if (source[range.valueStart] !== '{') return null;
    open = range.valueStart;
    close = matchBracket(source, open);
    if (close === -1) return null;
  }

  const range = findKeyRange(source, open, close, path[path.length - 1]!);
  if (range === null) return { text: source, changed: false, removed: 0 };
  if (source[range.valueStart] !== '[') return null;
  const arrOpen = range.valueStart;
  const arrClose = matchBracket(source, arrOpen);
  if (arrClose === -1) return null;

  const body = source.slice(arrOpen + 1, arrClose);
  const spans = topLevelItemSpans(body);
  if (!Number.isInteger(index) || index < 0 || index >= spans.length) return null;
  const span = spans[index]!;
  const stripped = stripComments(span.text);
  // Safety, not selection: the element must be an object literal (never a spread, whose
  // evaluated position would not line up with its source position), and any `expect` field the
  // text actually carries as a literal must match. Fields written as expressions (`owner: OWNER`)
  // are simply not checked — they cannot be, and index already pins the element.
  if (!stripped.trimStart().startsWith('{')) return null;
  for (const [k, v] of Object.entries(expect)) {
    const got = readField(stripped, k);
    if (got !== undefined && got !== v) return null;
  }

  let newBody: string;
  if (span.comma !== -1) {
    newBody = body.slice(0, span.start) + body.slice(span.comma + 1);
  } else {
    const before = body.slice(0, span.start);
    const prevComma = before.lastIndexOf(',');
    const cut = prevComma !== -1 && topLevelItemSpans(before).some((s) => s.comma === prevComma) ? prevComma : span.start;
    newBody = body.slice(0, cut) + body.slice(span.end);
  }
  if (newBody.trim() === '') newBody = '';
  return {
    text: `${source.slice(0, arrOpen + 1)}${newBody}${source.slice(arrClose)}`,
    changed: true,
    removed: 1,
  };
}
