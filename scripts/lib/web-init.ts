/**
 * `npm run frznforge -- init --web` — the same wizard as the terminal flow, in a browser tab.
 *
 * Picking twelve repositories out of an account of ninety is miserable as a numbered list, and
 * pleasant as a filterable table with checkboxes. So this module stands up a tiny local HTTP
 * server, opens a page against it, and reuses `scripts/cli.ts` for every decision that matters:
 * the listing (`listRepos`), the entry shapes (`entriesFor`), the textual splice
 * (`insertRepos`, and `scripts/lib/config-edit.ts` for every other field). The browser is a
 * *view*; nothing new is decided in it.
 *
 * Since 0.2.0 the wizard is a whole-config editor, not a one-shot repo picker: the session
 * stays up across any number of writes (settings, organizations, hosted sites, the profile
 * page) until `/api/done`, `/api/cancel`, Ctrl-C or the idle timer ends it. Every write runs
 * through one serialised queue, and the first write to each file takes the session's single
 * timestamped `.bak` for that file — so however many saves a session makes, "the state before
 * the wizard touched anything" is always one file away.
 *
 * The security model, because a page on localhost is still a page:
 *
 *  - **Bound to 127.0.0.1 only.** Never `0.0.0.0`: the wizard can write a file, so it must not
 *    be reachable from the network the machine happens to be on.
 *  - **Per-run session token.** Minted with `node:crypto`, printed in the URL, and required on
 *    every request — page and API. Without it another local process, or any web page the user
 *    happens to have open, could drive the wizard: `127.0.0.1` is same-origin-ish to nobody,
 *    but it *is* reachable by a `fetch()` from any site in the browser. The token is the thing
 *    that makes that fail.
 *  - **Host and Origin pinning.** A `Host` header that is not `127.0.0.1:<port>` /
 *    `localhost:<port>` is refused, which is what stops DNS rebinding (an attacker's name
 *    resolving to 127.0.0.1 arrives with *their* host header). API requests carrying a foreign
 *    `Origin` are refused too.
 *  - **The provider token never reaches the browser, and only ever goes to a host the terminal
 *    authorised.** The page asks the server for a listing; the server calls the provider.
 *    `/api/context` reports the *name* of the environment variable a token came from and
 *    nothing else, and every error is scrubbed before it is sent. The host is the subtle part:
 *    the page has a free-text "Custom API host" field, so a host typed (or posted) there would
 *    otherwise receive an `Authorization: Bearer <PAT>` for a URL the user never vetted. A
 *    listing therefore only carries the token when its host is the provider's own default, or
 *    the exact `--host` the user put on their own command line for that same provider
 *    (`hostTrustedForToken`). Any other host is listed **unauthenticated**, and the page is
 *    told so.
 *  - **The browser never names the file.** Writes always go to the path resolved here, from
 *    `--config` or `findConfigFile()`.
 */
import { spawn } from 'node:child_process';
import { randomBytes, timingSafeEqual } from 'node:crypto';
import fs from 'node:fs/promises';
import http from 'node:http';
import { createRequire } from 'node:module';
import type { AddressInfo } from 'node:net';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { FrznforgeConfigSchema, Palette, type FrznforgeConfig } from '../../src/lib/config/schema';
import { redactToken } from '../../src/lib/importers/types';
import { isMarkdownPath, renderMarkdown } from '../../src/lib/markdown';
import {
  EXCLUDE_FILTERS,
  PROVIDERS,
  PROVIDER_NAMES,
  SAFE_FIELD,
  assertSafeHost,
  entriesFor,
  findConfigFile,
  insertRepos,
  listRepos,
  renderSnippet,
  tokenStatus,
  writeBackup,
  type Io,
  type ProviderName,
  type RemoteRepo,
  type RepoEntry,
} from '../cli';
import { insertIntoArray, removeArrayItemAt, renderValue, setObjectField } from './config-edit';

/* ------------------------------------------------------------------ options */

export interface WebInitOptions {
  provider?: ProviderName;
  host?: string;
  account?: string;
  releases?: 'provider' | 'tags';
  /** Config file to write; when omitted the server resolves it with findConfigFile(). */
  configPath?: string;
  /** 0 or undefined = pick a free ephemeral port. */
  port?: number;
  /** Skip launching a browser (tests, headless boxes). */
  noOpen?: boolean;
  io: Io;
  fetchImpl?: typeof fetch;
}

/** A forgotten tab must not leave a writable server running forever. */
export const INACTIVITY_MS = 15 * 60 * 1000;

/** Bodies bigger than this are refused outright — the real ones are a few kilobytes. */
const MAX_BODY_BYTES = 1024 * 1024;

/** Exit codes `runWebInit` can resolve to. 130 is the conventional SIGINT code. */
const EXIT = { ok: 0, failed: 1, interrupted: 130 } as const;

/* ------------------------------------------------------------------ request guards */

/**
 * `Host` must name the loopback interface we are actually listening on.
 *
 * This is the DNS-rebinding guard: `evil.example` re-resolved to 127.0.0.1 still sends
 * `Host: evil.example`, so pinning the header keeps the wizard reachable only through a URL
 * the user could have typed themselves.
 */
export function hostAllowed(header: string | undefined, port: number): boolean {
  if (!header) return false;
  return header === `127.0.0.1:${port}` || header === `localhost:${port}` || header === `[::1]:${port}`;
}

/** An absent `Origin` is fine (a typed-in URL has none); a foreign one never is. */
export function originAllowed(header: string | undefined, port: number): boolean {
  if (header === undefined || header === '' || header === 'null') return true;
  return header === `http://127.0.0.1:${port}` || header === `http://localhost:${port}` || header === `http://[::1]:${port}`;
}

/** Compare two API bases the way `entryKey` does: no trailing slash, case-insensitive. */
function sameHost(a: string | null | undefined, b: string | null | undefined): boolean {
  if (!a || !b) return false;
  return a.replace(/\/+$/, '').toLowerCase() === b.replace(/\/+$/, '').toLowerCase();
}

/**
 * May the environment's provider token be sent to this host?
 *
 * Only two hosts qualify: the provider's own default API base, and the `--host` the user typed
 * on their own command line *for that same provider*. Everything else — including anything the
 * browser types into "Custom API host" — is listed without credentials. Binding the CLI host to
 * the CLI provider matters: without it, `--provider=gitea --host=https://intranet` would let
 * the page switch to GitHub and post the GitHub PAT to that same intranet box.
 */
export function hostTrustedForToken(
  provider: ProviderName,
  host: string,
  cli: { provider?: ProviderName; host?: string } = {},
): boolean {
  if (sameHost(host, PROVIDERS[provider].defaultHost)) return true;
  return cli.provider === provider && sameHost(host, cli.host);
}

/** Constant-time token comparison, so a wrong guess leaks nothing through timing. */
function tokenMatches(presented: string | null | undefined, expected: string): boolean {
  if (typeof presented !== 'string') return false;
  const a = Buffer.from(presented, 'utf8');
  const b = Buffer.from(expected, 'utf8');
  if (a.length !== b.length) return false;
  return timingSafeEqual(a, b);
}

/* ------------------------------------------------------------------ payload validation */

/**
 * `SAFE_FIELD` (shared with `entryFor`, which applies it to listing-derived values) is what
 * stops a typo or a hostile API response from putting nonsense in a config file the user will
 * read for years. `renderEntry` escaping quotes, backslashes and line terminators is the other
 * half; neither is trusted to be the only one.
 */
const MAX_ENTRIES = 500;

function badRequest(message: string): Error {
  const error = new Error(message);
  (error as { status?: number }).status = 400;
  return error;
}

/** 409: the request was well-formed but the state of the files refuses it. */
function conflict(message: string): Error {
  const error = new Error(message);
  (error as { status?: number }).status = 409;
  return error;
}

function asRecord(value: unknown): Record<string, unknown> {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) throw badRequest('expected a JSON object');
  return value as Record<string, unknown>;
}

function asProvider(value: unknown): ProviderName {
  if (typeof value === 'string' && (PROVIDER_NAMES as string[]).includes(value)) return value as ProviderName;
  throw badRequest(`unknown provider: ${JSON.stringify(value)}`);
}

function asReleases(value: unknown): 'provider' | 'tags' {
  if (value === undefined || value === 'provider') return 'provider';
  if (value === 'tags') return 'tags';
  throw badRequest("releases must be 'provider' or 'tags'");
}

/** Host as the browser typed it: http(s) only, no credentials, no query. */
function asHost(value: unknown, required: boolean): string {
  if (typeof value !== 'string' || value.trim() === '') {
    if (required) throw badRequest('a host is required for this provider');
    return '';
  }
  const text = value.trim().replace(/\/+$/, '');
  // `assertSafeHost` validates the *text*, not just what `new URL` makes of it: the WHATWG
  // parser strips tabs and newlines before parsing, so `https://a.com/x\ny` parses fine and the
  // newline would survive into the config file's string literal and break it.
  try {
    assertSafeHost(text);
  } catch (error) {
    throw badRequest(error instanceof Error ? error.message : String(error));
  }
  return text;
}

/**
 * An account is a user, an org, or a GitLab group path. `listRepos` URL-encodes it, so a `..`
 * cannot escape anything — but a config full of `../../etc` is nobody's intent, so say no here
 * rather than send it to a provider and report its 404.
 */
function asAccount(value: unknown): string {
  if (typeof value !== 'string' || value.trim() === '') throw badRequest('an account is required');
  const text = value.trim();
  if (!SAFE_FIELD.test(text)) throw badRequest(`account contains characters that cannot be in a path: ${text}`);
  const segments = text.split('/');
  if (segments.some((s) => s === '' || s === '.' || s === '..')) throw badRequest(`not an account name: ${text}`);
  return text;
}

function asField(value: unknown, key: string): string | undefined {
  if (value === undefined || value === null) return undefined;
  if (typeof value !== 'string' || !SAFE_FIELD.test(value)) throw badRequest(`invalid ${key}: ${JSON.stringify(value)}`);
  return value;
}

/** Validate one browser-supplied entry before it can reach the config file. */
function asEntry(value: unknown): RepoEntry {
  const raw = asRecord(value);
  const entry: RepoEntry = { type: asProvider(raw.type) };
  const host = raw.host === undefined || raw.host === null ? '' : asHost(raw.host, false);
  if (host) entry.host = host;
  entry.owner = asField(raw.owner, 'owner');
  entry.repo = asField(raw.repo, 'repo');
  entry.project = asField(raw.project, 'project');
  entry.slug = asField(raw.slug, 'slug');
  if (raw.releases !== undefined) entry.releases = asReleases(raw.releases);
  if (!entry.project && !(entry.owner && entry.repo)) throw badRequest('an entry needs owner+repo, or project');
  return entry;
}

/* ------------------------------------------------------------------ settings operations (0.2.0) */

/**
 * The whole-config editor speaks in *operations* — `set`/`unset` a scalar field, `add` to /
 * `remove` from one of the three config arrays — and every operation is checked against these
 * allow-lists before it can touch the file. The browser proposes; this table disposes. A path
 * not listed here (`repos` entries' own fields, or anything the schema grows later) simply
 * cannot be written through the wizard, which is the property that keeps "arbitrary JSON from
 * a local web page" from becoming "arbitrary edits to a TypeScript file".
 *
 * Values are validated twice: shape here (primitives and string lists only, no control
 * characters), and *meaning* by applying the operations to a clone of the loaded config and
 * running the result through `FrznforgeConfigSchema` — so `theme.heat` ordering, reserved
 * hosting slugs, or a bad `site.base` are refused with the schema's own message before a
 * byte of the file moves.
 */
export const SET_PATHS = new Set([
  'site.title', 'site.url', 'site.description', 'site.base',
  'owner.name', 'owner.handle', 'owner.profile',
  'theme.palette', 'theme.heat.hot', 'theme.heat.warm', 'theme.heat.neutral', 'theme.heat.cool',
  'markdown.mermaid',
  'content.orgs',
  'listing.pageSize',
  'notes.dir', 'notes.useMtime', 'notes.maxFileBytes',
  'hosting.maxFileBytes',
  'ingest.outDir', 'ingest.maxBlobBytes', 'ingest.maxCommits', 'ingest.maxCommitAgeDays',
  'ingest.concurrency', 'ingest.tagTrees', 'ingest.branchTrees', 'ingest.archives',
  'ingest.cacheDir', 'ingest.fetch',
  'ingest.reuse.enabled', 'ingest.reuse.maxAgeMinutes',
  'ingest.insights.enabled', 'ingest.insights.samples', 'ingest.insights.maxBytesPerSample',
]);

/**
 * Optional fields the page may clear. `unset` writes a literal `field: undefined`, which the
 * schema treats exactly like an absent key — there is no textual "delete this line" operation,
 * because deleting lines is how a comment beside the field gets destroyed.
 */
export const UNSET_PATHS = new Set(['site.url', 'site.description', 'site.base', 'notes.maxFileBytes']);

interface ArrayItemSpec {
  required: string[];
  optional: string[];
  /** Keys whose value is a list of strings rather than a string. */
  lists: string[];
}

const ARRAY_ADD_SPECS: Record<string, ArrayItemSpec> = {
  organizations: { required: ['slug', 'name'], optional: ['description'], lists: ['repos'] },
  'hosting.sites': { required: ['repo'], optional: ['slug', 'branch'], lists: [] },
};

/** `repos` entries are added by the picker (`/api/write`), so `add` is deliberately absent here. */
const ARRAY_REMOVE_KEYS: Record<string, string[]> = {
  organizations: ['slug', 'name'],
  'hosting.sites': ['repo', 'slug', 'branch'],
  repos: ['type', 'host', 'owner', 'repo', 'project', 'path', 'slug'],
};

const MAX_OPERATIONS = 50;

/** No control characters: these strings end up inside single quotes in a file people read. */
const SAFE_SETTING_STRING = /^[^\u0000-\u001f\u007f]*$/;

function asSettingString(value: unknown, label: string): string {
  if (typeof value !== 'string' || value.length > 2000 || !SAFE_SETTING_STRING.test(value)) {
    throw badRequest(`invalid ${label}: expected a plain string`);
  }
  return value;
}

function asSettingValue(value: unknown, label: string): unknown {
  if (value === null || typeof value === 'boolean') return value;
  if (typeof value === 'number') {
    if (!Number.isFinite(value) || Math.abs(value) > 1e15) throw badRequest(`invalid ${label}: not a usable number`);
    return value;
  }
  if (typeof value === 'string') return asSettingString(value, label);
  if (Array.isArray(value)) {
    if (value.length > 200) throw badRequest(`invalid ${label}: too many items`);
    return value.map((v, i) => asSettingString(v, `${label}[${i}]`));
  }
  throw badRequest(`invalid ${label}: expected a primitive or a list of strings`);
}

export type SettingsOp =
  | { op: 'set'; path: string[]; value: unknown }
  | { op: 'unset'; path: string[] }
  | { op: 'add'; path: string[]; item: Record<string, unknown> }
  | { op: 'removeAt'; path: string[]; index: number; expect: Record<string, string> };

function asArrayItem(value: unknown, spec: ArrayItemSpec, label: string): Record<string, unknown> {
  const raw = asRecord(value);
  const item: Record<string, unknown> = {};
  for (const key of Object.keys(raw)) {
    if (raw[key] === undefined || raw[key] === null || raw[key] === '') continue;
    if (spec.lists.includes(key)) {
      const list = raw[key];
      if (!Array.isArray(list) || list.length > 200) throw badRequest(`invalid ${label}.${key}: expected a list of strings`);
      item[key] = list.map((v, i) => asSettingString(v, `${label}.${key}[${i}]`));
    } else if (spec.required.includes(key) || spec.optional.includes(key)) {
      item[key] = asSettingString(raw[key], `${label}.${key}`);
    } else {
      throw badRequest(`unknown field in ${label}: ${key}`);
    }
  }
  for (const key of spec.required) {
    if (item[key] === undefined) throw badRequest(`${label} needs a ${key}`);
  }
  return item;
}

export function asOperations(value: unknown): SettingsOp[] {
  if (!Array.isArray(value) || value.length === 0) throw badRequest('expected a non-empty "operations" array');
  if (value.length > MAX_OPERATIONS) throw badRequest('too many operations in one request');
  return value.map((raw, index) => {
    const record = asRecord(raw);
    const label = `operations[${index}]`;
    const pathText = typeof record.path === 'string' ? record.path : '';
    switch (record.op) {
      case 'set': {
        if (!SET_PATHS.has(pathText)) throw badRequest(`${label}: '${pathText}' is not a field the wizard can set`);
        return { op: 'set', path: pathText.split('.'), value: asSettingValue(record.value, `${label}.value`) };
      }
      case 'unset': {
        if (!UNSET_PATHS.has(pathText)) throw badRequest(`${label}: '${pathText}' is not a field the wizard can clear`);
        return { op: 'unset', path: pathText.split('.') };
      }
      case 'add': {
        const spec = ARRAY_ADD_SPECS[pathText];
        if (!spec) throw badRequest(`${label}: '${pathText}' is not a list the wizard can add to`);
        return { op: 'add', path: pathText.split('.'), item: asArrayItem(record.item, spec, `${label}.item`) };
      }
      case 'removeAt': {
        // Removal is by POSITION, not by content match: two entries can be
        // indistinguishable by fields when one's are a subset of the other's
        // (`[{ repo: 'x' }, { repo: 'x', slug: 'y' }]`), and a content match would delete both.
        // The page knows which row it rendered, so it sends that row's index; `expect` is a
        // safety check the engine applies to the element actually at that index.
        const keys = ARRAY_REMOVE_KEYS[pathText];
        if (!keys) throw badRequest(`${label}: '${pathText}' is not a list the wizard can remove from`);
        if (typeof record.index !== 'number' || !Number.isInteger(record.index) || record.index < 0) {
          throw badRequest(`${label}: index must be a non-negative integer`);
        }
        const raw = record.expect === undefined ? {} : asRecord(record.expect);
        const expect: Record<string, string> = {};
        for (const key of Object.keys(raw)) {
          if (!keys.includes(key)) throw badRequest(`${label}: cannot match on '${key}'`);
          expect[key] = asSettingString(raw[key], `${label}.expect.${key}`);
        }
        return { op: 'removeAt', path: pathText.split('.'), index: record.index, expect };
      }
      default:
        throw badRequest(`${label}: unknown op ${JSON.stringify(record.op)}`);
    }
  });
}

/* ---- applying operations: once to a clone (for schema validation), once to the text ---- */

function deepGet(target: unknown, path: string[]): unknown {
  let value: unknown = target;
  for (const key of path) {
    if (typeof value !== 'object' || value === null || Array.isArray(value)) return undefined;
    value = (value as Record<string, unknown>)[key];
  }
  return value;
}

function deepSet(target: Record<string, unknown>, path: string[], value: unknown): void {
  let obj = target;
  for (const key of path.slice(0, -1)) {
    const next = obj[key];
    if (typeof next !== 'object' || next === null || Array.isArray(next)) {
      obj[key] = {};
      obj = obj[key] as Record<string, unknown>;
    } else {
      obj = next as Record<string, unknown>;
    }
  }
  obj[path[path.length - 1]!] = value;
}

/**
 * The candidate the schema judges: the loaded config's raw input with the operations applied
 * as plain object mutations. Returns `null` when the input cannot be cloned (a config built
 * from something exotic) — the textual edit then proceeds on the strength of the allow-list
 * and the post-write verify alone.
 */
export function applyOpsToInput(input: unknown, ops: SettingsOp[]): Record<string, unknown> | null {
  let clone: unknown;
  try {
    clone = structuredClone(input);
  } catch {
    return null;
  }
  if (typeof clone !== 'object' || clone === null || Array.isArray(clone)) return null;
  const target = clone as Record<string, unknown>;
  for (const op of ops) {
    if (op.op === 'set') deepSet(target, op.path, op.value);
    else if (op.op === 'unset') deepSet(target, op.path, undefined);
    else if (op.op === 'add') {
      const list = deepGet(target, op.path);
      if (Array.isArray(list)) list.push(op.item);
      else deepSet(target, op.path, [op.item]);
    } else {
      // removeAt: drop the element at op.index (mirroring the text engine, which pins by
      // position too). Out of range leaves the array untouched — the text edit then also
      // no-ops or errors, and the post-write equality check catches any divergence.
      const list = deepGet(target, op.path);
      if (Array.isArray(list) && op.index >= 0 && op.index < list.length) list.splice(op.index, 1);
    }
  }
  return target;
}

/** Apply the same operations to the file's text, through the comment-preserving engine. */
export function applyOpsToSource(source: string, ops: SettingsOp[]): { text: string; changed: boolean } | { error: string } {
  let text = source;
  let changed = false;
  for (const op of ops) {
    const result =
      op.op === 'set'
        ? setObjectField(text, op.path, renderValue(op.value))
        : op.op === 'unset'
          ? setObjectField(text, op.path, 'undefined')
          : op.op === 'add'
            ? insertIntoArray(text, op.path, renderValue(op.item))
            : removeArrayItemAt(text, op.path, op.index, op.expect);
    if (!result) return { error: `could not apply ${op.op} at ${op.path.join('.')} — the file's structure was not recognised; edit it by hand` };
    text = result.text;
    changed = changed || result.changed;
  }
  return { text, changed };
}

/* ------------------------------------------------------------------ profile splitting */

/**
 * Byte offset where a markdown file's body starts: after the leading `---` frontmatter block
 * (closed by `---` or `...`), or `0` when there is none — an unterminated block included, the
 * same stance `src/lib/frontmatter.ts` takes. Offset-based rather than reusing
 * `splitFrontmatter` on purpose: that helper normalises line endings for *parsing*, while the
 * profile editor must round-trip the frontmatter block byte-for-byte — the wizard edits the
 * body and has no business reformatting metadata it does not understand.
 */
export function frontmatterEndOffset(text: string): number {
  const open = /^\uFEFF?---[ \t]*\r?\n/.exec(text);
  if (!open) return 0;
  let idx = open[0].length;
  for (;;) {
    const nl = text.indexOf('\n', idx);
    const line = (nl === -1 ? text.slice(idx) : text.slice(idx, nl)).replace(/\r$/, '');
    if (/^(---|\.\.\.)[ \t]*$/.test(line)) return nl === -1 ? text.length : nl + 1;
    if (nl === -1) return 0;
    idx = nl + 1;
  }
}

/**
 * Reattach an edited body to a preserved frontmatter head. When the head ends at a bare
 * terminator (`---` as the file's last line, no trailing newline), gluing the body straight on
 * would fuse them into `---body`, and on the next read that line no longer closes the block \u2014
 * the whole frontmatter would be swallowed into the body. A single separating newline keeps the
 * terminator on its own line; the frontmatter bytes themselves are untouched.
 */
export function joinProfile(head: string, body: string): string {
  if (head === '') return body;
  return head.endsWith('\n') ? head + body : `${head}\n${body}`;
}

/** Defaults the page shows for fields the config file leaves unset. Owner has no defaults. */
const CONFIG_DEFAULTS = FrznforgeConfigSchema.parse({ owner: { name: 'Owner', handle: 'owner' } });

/* ------------------------------------------------------------------ child-process config load */

const CONFIG_LOAD_HELPER = fileURLToPath(new URL('./config-load.ts', import.meta.url));

/** How long a config import may take before the wizard calls it hung. Generous: tsx cold-start. */
const CONFIG_LOAD_TIMEOUT_MS = 20_000;

/**
 * Run `config-load.ts` under tsx against `file` and hand back its default export. See
 * `importConfig` in `runWebInit` for why this is a child process and not an `import()`.
 */
export function loadConfigInChild(file: string): Promise<{ ok: true; input: unknown } | { ok: false; error: string }> {
  return new Promise((resolve) => {
    let tsxCli: string;
    try {
      tsxCli = createRequire(import.meta.url).resolve('tsx/cli');
    } catch (error) {
      resolve({ ok: false, error: `could not resolve tsx: ${error instanceof Error ? error.message : String(error)}` });
      return;
    }
    let child: ReturnType<typeof spawn>;
    try {
      child = spawn(process.execPath, [tsxCli, CONFIG_LOAD_HELPER, file], {
        stdio: ['ignore', 'pipe', 'pipe'],
        windowsHide: true,
      });
    } catch (error) {
      resolve({ ok: false, error: error instanceof Error ? error.message : String(error) });
      return;
    }
    let stdout = '';
    let stderr = '';
    let timedOut = false;
    let settled = false;
    const settle = (result: { ok: true; input: unknown } | { ok: false; error: string }): void => {
      if (settled) return;
      settled = true;
      resolve(result);
    };
    child.stdout!.setEncoding('utf8').on('data', (chunk: string) => (stdout += chunk));
    child.stderr!.setEncoding('utf8').on('data', (chunk: string) => (stderr += chunk));
    const timer = setTimeout(() => {
      timedOut = true;
      child.kill();
      // `close` waits for the stdio pipes to drain too, and a config whose top-level code
      // spawned a detached grandchild inheriting stdout can hold them open forever — which
      // would wedge the whole write queue. A short grace period after the kill settles the
      // promise regardless, so a pathological config can never hang the wizard.
      const after = setTimeout(
        () => settle({ ok: false, error: 'loading the config timed out — does it block on top-level work?' }),
        500,
      );
      after.unref?.();
    }, CONFIG_LOAD_TIMEOUT_MS);
    timer.unref?.();
    child.on('error', (error) => {
      clearTimeout(timer);
      settle({ ok: false, error: error.message });
    });
    child.on('close', (code) => {
      clearTimeout(timer);
      if (timedOut) {
        settle({ ok: false, error: 'loading the config timed out — does it block on top-level work?' });
        return;
      }
      if (code !== 0) {
        settle({ ok: false, error: (stderr.trim() || `the config loader exited with code ${code}`).slice(0, 2000) });
        return;
      }
      try {
        settle({ ok: true, input: JSON.parse(stdout) });
      } catch {
        settle({ ok: false, error: 'the config loader produced no readable output' });
      }
    });
  });
}

/* ------------------------------------------------------------------ the page */

const PAGE_FILE = fileURLToPath(new URL('./web-init-page.html', import.meta.url));
let pageCache: string | null = null;

/** The wizard's HTML. Static — nothing is interpolated into it, so it cannot carry an injection. */
export async function renderPage(): Promise<string> {
  if (pageCache === null) pageCache = await fs.readFile(PAGE_FILE, 'utf8');
  return pageCache;
}

/* ------------------------------------------------------------------ server */

interface Listing {
  key: string;
  provider: ProviderName;
  host: string;
  account: string;
  repos: RemoteRepo[];
}

function listingKey(provider: string, host: string, account: string): string {
  return `${provider}|${host.toLowerCase()}|${account.toLowerCase()}`;
}

/** `https://api.github.com` → `api.github.com`, for a label the page can show. */
function hostLabel(host: string): string {
  try {
    return new URL(host).host;
  } catch {
    return host;
  }
}

export async function runWebInit(opts: WebInitOptions): Promise<number> {
  const io = opts.io;
  const configPath = opts.configPath ? path.resolve(io.cwd, opts.configPath) : await findConfigFile(io.cwd);
  const sessionToken = randomBytes(24).toString('base64url');

  let listing: Listing | null = null;
  /**
   * Never two writes at once — and since 0.2.0, any number in sequence.
   *
   * Every write is read-modify-write on a file, and nothing binds the session to a single tab:
   * the URL is printed in the terminal and `openBrowser` may already have opened one, so two
   * tabs can both offer a working save button. Un-serialised, each request read the original
   * file, spliced its own change, and wrote — every one answering 200 while only the last
   * one's change survived. Queueing every write through this chain makes each read see the
   * previous write; `/api/done` and `/api/cancel` also drain it, so the process never exits
   * with a save still in flight.
   */
  let writeChain: Promise<unknown> = Promise.resolve();
  const serialize = <T,>(task: () => Promise<T>): Promise<T> => {
    const run = writeChain.then(task, task);
    writeChain = run.then(
      () => undefined,
      () => undefined,
    );
    return run;
  };

  /**
   * One `.bak` per file per session. The first write to a file backs up its pre-wizard bytes;
   * every later write to the same file reuses that backup, so "undo everything this session
   * did" stays a single copy instead of a breadcrumb trail of intermediate states.
   */
  const sessionBackups = new Map<string, string | null>();
  let writesDone = 0;
  const backupOnce = async (file: string, source: string): Promise<string | null> => {
    const existing = sessionBackups.get(file);
    if (existing !== undefined) return existing;
    const backup = await writeBackup(file, source, new Date());
    sessionBackups.set(file, backup);
    return backup;
  };
  /**
   * Record that a file was *created* by this session, so a later write to it does not back up
   * the wizard's own first output as if it were the "pre-wizard state". A created file has no
   * pre-wizard bytes, so its session backup is `null` (no `.bak`), not a mid-session snapshot.
   */
  const markCreated = (file: string): void => {
    if (!sessionBackups.has(file)) sessionBackups.set(file, null);
  };

  /** Bumped after every write, so the memo below cannot answer with a pre-edit config. */
  let configGen = 0;
  interface LoadedConfig {
    input: unknown;
    parsed: FrznforgeConfig | null;
    issues: string[];
  }
  let configCache: { gen: number; loaded: LoadedConfig } | null = null;
  let configInflight: { gen: number; promise: Promise<LoadedConfig> } | null = null;

  /**
   * Load the config file the way the site does: import it and let the schema judge it. This
   * *executes* the config — the exact trust already extended by every `npm run build`.
   *
   * The import happens in a short-lived child process (`config-load.ts` under tsx), not in
   * this one: tsx's loader caches modules by path and ignores a query-string cache-buster, so
   * an in-process re-import after a write answered with the PRE-edit module — the user saved,
   * reloaded, and saw their old values. A fresh process has no module cache, and the file's
   * own relative imports still resolve against its real location. Memoised per `configGen`, and
   * concurrent reads at the same generation share one spawn (the page loads config and profile
   * together on boot) rather than each starting a tsx process.
   */
  const importConfig = async (): Promise<LoadedConfig> => {
    if (!configPath) return { input: undefined, parsed: null, issues: ['no config file was found'] };
    if (configCache && configCache.gen === configGen) return configCache.loaded;
    if (configInflight && configInflight.gen === configGen) return configInflight.promise;
    const gen = configGen;
    const promise = (async (): Promise<LoadedConfig> => {
      const fresh = await loadConfigInChild(configPath);
      let loaded: LoadedConfig;
      if (!fresh.ok) {
        loaded = { input: undefined, parsed: null, issues: [scrub(fresh.error)] };
      } else {
        const result = FrznforgeConfigSchema.safeParse(fresh.input);
        loaded = result.success
          ? { input: fresh.input, parsed: result.data, issues: [] }
          : { input: fresh.input, parsed: null, issues: result.error.issues.map((i) => `${i.path.join('.')}: ${i.message}`) };
      }
      // Only cache if no write bumped the generation while we were loading.
      if (gen === configGen) configCache = { gen, loaded };
      return loaded;
    })();
    configInflight = { gen, promise };
    try {
      return await promise;
    } finally {
      if (configInflight && configInflight.promise === promise) configInflight = null;
    }
  };

  /**
   * The profile file the wizard edits — always resolved here, never named by the browser.
   *
   * Contained, because `owner.profile` is a browser-settable config field: the resolved path
   * must stay inside the project (no `..` escape, no absolute path elsewhere), must be a
   * markdown file (the profile body is markdown — writing it to a `.ts` file is never
   * legitimate and would otherwise let a crafted `owner.profile` overwrite the config itself,
   * which the wizard then *executes*), and must not be the config file. Any of those fails →
   * `null`, and the profile endpoints decline rather than write. `parsed` null (config does not
   * load) also yields `null`: without a validated config there is no trustworthy profile path.
   */
  const profilePathFor = (parsed: FrznforgeConfig | null): string | null => {
    if (!configPath || parsed === null) return null;
    const root = path.dirname(configPath);
    const file = path.resolve(root, parsed.owner.profile);
    const rel = path.relative(root, file);
    if (rel === '' || rel.startsWith('..') || path.isAbsolute(rel)) return null;
    if (file === configPath) return null;
    if (!isMarkdownPath(file.replace(/\\/g, '/'))) return null;
    return file;
  };

  let settled = false;
  let resolveDone!: (code: number) => void;
  const done = new Promise<number>((resolve) => {
    resolveDone = resolve;
  });

  /* ---------------------------------------------------------------- helpers bound to the run */

  /** Never let a provider token reach the browser, even through an error message. */
  const scrub = (value: unknown, token?: string | null): string => {
    const text = value instanceof Error ? value.message : String(value);
    return redactToken(text, token ?? null);
  };

  const send = (res: http.ServerResponse, status: number, type: string, body: string, last = false): void => {
    const buffer = Buffer.from(body, 'utf8');
    res.writeHead(status, {
      'content-type': type,
      'content-length': String(buffer.byteLength),
      'cache-control': 'no-store',
      'x-content-type-options': 'nosniff',
      'referrer-policy': 'no-referrer',
      ...(last ? { connection: 'close' } : {}),
    });
    res.end(buffer);
  };

  const sendJson = (res: http.ServerResponse, status: number, body: unknown, last = false): void =>
    send(res, status, 'application/json; charset=utf-8', JSON.stringify(body), last);

  const server = http.createServer((req, res) => {
    handle(req, res).catch((error) => {
      const status = typeof (error as { status?: number }).status === 'number' ? (error as { status: number }).status : 500;
      if (!res.headersSent) sendJson(res, status, { error: scrub(error) });
      else res.end();
    });
  });

  const port = (): number => (server.address() as AddressInfo | null)?.port ?? 0;

  /* ---------------------------------------------------------------- lifecycle */

  let idleTimer: NodeJS.Timeout | null = null;
  const bump = (): void => {
    if (idleTimer) clearTimeout(idleTimer);
    idleTimer = setTimeout(() => {
      finish(
        EXIT.failed,
        `No activity for ${Math.round(INACTIVITY_MS / 60000)} minutes — the init server has stopped. ` +
          `${wroteSummary()} Anything typed into the page but not saved is gone with it.`,
      );
    }, INACTIVITY_MS);
    idleTimer.unref?.();
  };

  /** "Nothing was written." or how much was, for every way the session can end. */
  const wroteSummary = (): string =>
    writesDone === 0 ? 'Nothing was written.' : `${writesDone} write${writesDone === 1 ? ' was' : 's were'} already saved.`;

  const onSigint = (): void => finish(EXIT.interrupted, `\nInterrupted. ${wroteSummary()}`);

  /**
   * Stop listening, then hang up. `close()` alone waits on idle keep-alive sockets the page
   * left behind, so idle ones are dropped immediately and the rest get a short grace period.
   */
  const shutdown = (): Promise<void> =>
    new Promise((resolve) => {
      let once = false;
      const finished = (): void => {
        if (once) return;
        once = true;
        resolve();
      };
      server.close(finished);
      server.closeIdleConnections?.();
      const grace = setTimeout(() => {
        server.closeAllConnections?.();
        finished();
      }, 400);
      grace.unref?.();
    });

  function finish(code: number, message?: string): void {
    if (settled) return;
    settled = true;
    if (idleTimer) clearTimeout(idleTimer);
    process.off('SIGINT', onSigint);
    if (message) io.log(message);
    void shutdown().then(() => resolveDone(code));
  }

  /** Close only once the response has actually left the socket. */
  const finishAfter = (res: http.ServerResponse, code: number, message?: string): void => {
    res.on('close', () => finish(code, message));
    if (res.writableEnded) finish(code, message);
  };

  /* ---------------------------------------------------------------- request handling */

  async function readBody(req: http.IncomingMessage): Promise<unknown> {
    const chunks: Buffer[] = [];
    let size = 0;
    for await (const chunk of req) {
      size += (chunk as Buffer).byteLength;
      if (size > MAX_BODY_BYTES) throw badRequest('request body is too large');
      chunks.push(chunk as Buffer);
    }
    const text = Buffer.concat(chunks).toString('utf8');
    if (text.trim() === '') return {};
    try {
      return JSON.parse(text);
    } catch {
      throw badRequest('body is not JSON');
    }
  }

  /** Entries either come pre-built (`entries`) or are named against the cached listing. */
  function entriesFrom(body: Record<string, unknown>): RepoEntry[] {
    if (Array.isArray(body.entries)) {
      if (body.entries.length > MAX_ENTRIES) throw badRequest('too many entries');
      return body.entries.map(asEntry);
    }
    const provider = asProvider(body.provider);
    const host = asHost(body.host, PROVIDERS[provider].hostRequired) || PROVIDERS[provider].defaultHost || '';
    const account = asAccount(body.account);
    const releases = asReleases(body.releases);
    const select = Array.isArray(body.select) ? body.select : null;
    if (!select) throw badRequest('expected "entries" or "select"');
    if (select.length > MAX_ENTRIES) throw badRequest('too many entries');

    const key = listingKey(provider, host, account);
    if (!listing || listing.key !== key) throw badRequest('that repository list is no longer loaded — load it again');
    const wanted = new Set(select.filter((s): s is string => typeof s === 'string'));
    const picked = listing.repos.filter((r) => wanted.has(r.fullName));
    if (picked.length === 0) throw badRequest('nothing selected');
    return entriesFor(provider, host, picked, releases);
  }

  async function previewFor(entries: RepoEntry[]): Promise<{ snippet: string; changed: boolean; added: number; skipped: number }> {
    const snippet = renderSnippet(entries);
    if (!configPath) return { snippet, changed: false, added: entries.length, skipped: 0 };
    const source = await fs.readFile(configPath, 'utf8').catch(() => null);
    const result = source === null ? null : insertRepos(source, entries);
    if (!result) return { snippet, changed: false, added: entries.length, skipped: 0 };
    return { snippet, changed: result.changed, added: result.added.length, skipped: result.skipped.length };
  }

  /**
   * Put `source` back after a write that must be undone, and throw a conflict explaining why. If
   * the restore write *itself* fails (a lock, a full disk), say that plainly and name the `.bak`
   * — the one thing that is guaranteed to still hold the pre-wizard bytes — rather than letting
   * a raw fs error surface as an opaque 500 over a now-broken file.
   */
  async function restoreAndFail(source: string, why: string): Promise<never> {
    try {
      await fs.writeFile(configPath!, source, 'utf8');
      configGen += 1;
    } catch (restoreError) {
      configGen += 1;
      const bak = sessionBackups.get(configPath!);
      throw conflict(
        `${why}, and restoring the previous bytes also failed (${scrub(restoreError)})` +
          (bak ? ` — the pre-wizard copy is at ${bak}` : '') +
          '. The config file may be broken; check it before building.',
      );
    }
    throw conflict(`${why} — the file was restored unchanged`);
  }

  async function handle(req: http.IncomingMessage, res: http.ServerResponse): Promise<void> {
    const bound = port();
    if (!hostAllowed(req.headers.host, bound)) {
      send(res, 403, 'text/plain; charset=utf-8', 'forbidden: this server only answers on 127.0.0.1\n');
      return;
    }
    const url = new URL(req.url ?? '/', `http://127.0.0.1:${bound}`);
    const presented = url.searchParams.get('s') ?? (req.headers['x-frznforge-session'] as string | undefined) ?? null;
    if (!tokenMatches(presented, sessionToken)) {
      send(res, 403, 'text/plain; charset=utf-8', 'forbidden: missing or wrong session key — open the URL printed in the terminal\n');
      return;
    }
    if (url.pathname.startsWith('/api/') && !originAllowed(req.headers.origin, bound)) {
      send(res, 403, 'text/plain; charset=utf-8', 'forbidden: cross-origin request\n');
      return;
    }
    bump();

    if (url.pathname === '/' || url.pathname === '/index.html') {
      if (req.method !== 'GET' && req.method !== 'HEAD') {
        sendJson(res, 405, { error: 'method not allowed' });
        return;
      }
      const html = await renderPage();
      const buffer = Buffer.from(html, 'utf8');
      res.writeHead(200, {
        'content-type': 'text/html; charset=utf-8',
        'content-length': String(buffer.byteLength),
        'cache-control': 'no-store',
        'x-content-type-options': 'nosniff',
        'referrer-policy': 'no-referrer',
        // The page is self-contained; this makes "no external requests" a rule the browser enforces.
        'content-security-policy':
          "default-src 'none'; connect-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; img-src data:; form-action 'none'; base-uri 'none'; frame-ancestors 'none'",
      });
      res.end(req.method === 'HEAD' ? undefined : buffer);
      return;
    }

    if (url.pathname === '/api/context') {
      const providers: Record<string, unknown> = {};
      for (const name of PROVIDER_NAMES) {
        const info = PROVIDERS[name];
        const status = tokenStatus(name, io.env);
        providers[name] = {
          label: info.label,
          defaultHost: info.defaultHost,
          hostRequired: info.hostRequired,
          hostSuggestion: info.hostSuggestion,
          accountLabel: info.accountLabel,
          tokenScopes: info.tokenScopes,
          // Names only. `status.token` is deliberately not forwarded.
          token: { names: status.names, from: status.from, found: status.token !== null },
        };
      }
      sendJson(res, 200, {
        order: PROVIDER_NAMES,
        providers,
        defaults: {
          provider: opts.provider ?? 'github',
          host: opts.host ?? null,
          account: opts.account ?? null,
          releases: opts.releases ?? 'provider',
        },
        configPath,
        configName: configPath ? path.basename(configPath) : null,
      });
      return;
    }

    if (url.pathname === '/api/config') {
      // The parsed config (defaults applied) plus the schema's own defaults, so the page can
      // show current values and label what "unset" would mean. No secret ever lives in the
      // config file — tokens are environment-only — so `current` is safe to serialise whole.
      const loaded = await importConfig();
      // `sources` is each repos entry's string fields, in array order, for display and for the
      // `expect` safety check on a positional remove. These are the *evaluated* values (the
      // config is executed to load it), so an entry written with an expression shows its
      // computed string; removal is by index, so that does not matter.
      const rawRepos = (loaded.input as Record<string, unknown> | null | undefined)?.repos;
      const sources = (Array.isArray(rawRepos) ? rawRepos : []).map((entry) => {
        const record = typeof entry === 'object' && entry !== null ? (entry as Record<string, unknown>) : {};
        const picked: Record<string, string> = {};
        for (const key of ARRAY_REMOVE_KEYS.repos!) {
          if (typeof record[key] === 'string') picked[key] = record[key] as string;
        }
        return picked;
      });
      sendJson(res, 200, {
        configPath,
        configName: configPath ? path.basename(configPath) : null,
        readable: loaded.parsed !== null,
        issues: loaded.issues,
        current: loaded.parsed,
        sources,
        defaults: CONFIG_DEFAULTS,
        palettes: Palette.options,
        writes: writesDone,
      });
      return;
    }

    if (url.pathname === '/api/profile') {
      const loaded = await importConfig();
      const file = profilePathFor(loaded.parsed);
      if (!file) {
        sendJson(res, 200, { available: false });
        return;
      }
      const text = await fs.readFile(file, 'utf8').catch(() => null);
      const cut = text === null ? 0 : frontmatterEndOffset(text);
      sendJson(res, 200, {
        available: true,
        path: file,
        exists: text !== null,
        // The frontmatter block rides along read-only; only the body is editable, and the
        // write below re-attaches these exact bytes.
        frontmatter: text === null ? '' : text.slice(0, cut),
        body: text === null ? '' : text.slice(cut),
      });
      return;
    }

    if (req.method !== 'POST') {
      sendJson(res, 405, { error: 'method not allowed' });
      return;
    }
    const body = asRecord(await readBody(req));

    if (url.pathname === '/api/repos') {
      const provider = asProvider(body.provider);
      const info = PROVIDERS[provider];
      const host = asHost(body.host, info.hostRequired) || info.defaultHost || '';
      const account = asAccount(body.account);
      const status = tokenStatus(provider, io.env);
      // The one rule that keeps a PAT out of a stranger's logs: a host the terminal did not
      // authorise gets an anonymous request, never an Authorization header.
      const trusted = hostTrustedForToken(provider, host, { provider: opts.provider, host: opts.host });
      const token = trusted ? status.token : null;
      const warnings: string[] = [];
      if (!trusted && status.token !== null) {
        warnings.push(
          `${hostLabel(host)} is not ${PROVIDERS[provider].label}'s own API host, so $${status.from} was not sent to it — ` +
            `only public repositories are listed. Restart with --host=${host} if that host really is your instance.`,
        );
      }
      let repos: RemoteRepo[];
      try {
        repos = await listRepos(provider, {
          host,
          account,
          token,
          fetchImpl: opts.fetchImpl ?? io.fetchImpl,
          warn: (line) => warnings.push(line),
        });
      } catch (error) {
        sendJson(res, 502, { error: scrub(error, status.token) });
        return;
      }
      listing = { key: listingKey(provider, host, account), provider, host, account, repos };
      sendJson(res, 200, {
        provider,
        host,
        hostLabel: hostLabel(host),
        account,
        warnings,
        tokenSent: trusted && status.token !== null,
        // Which quick filters this listing can actually answer for. GitLab's project listings
        // report neither forks nor (for users) archived state, and a toggle that silently does
        // nothing is worse than one that is visibly unavailable.
        flags: Object.fromEntries(EXCLUDE_FILTERS.map((f) => [f.one, repos.every((r) => f.known(r))])),
        repos,
      });
      return;
    }

    if (url.pathname === '/api/preview') {
      sendJson(res, 200, { ...(await previewFor(entriesFrom(body))), configPath });
      return;
    }

    if (url.pathname === '/api/write') {
      if (!configPath) {
        sendJson(res, 409, { error: 'no frznforge.config.ts was found — copy the snippet into your config by hand' });
        return;
      }
      // Validated before queueing, so a malformed request cannot occupy the write chain.
      const entries = entriesFrom(body);
      let result:
        | { error: string }
        | { backup: string | null; added: RepoEntry[]; skipped: RepoEntry[]; changed: boolean };
      try {
        result = await serialize(async () => {
          const source = await fs.readFile(configPath, 'utf8');
          const spliced = insertRepos(source, entries);
          if (!spliced) return { error: `could not find a repos: [ … ] array in ${configPath} — paste the snippet in by hand` };
          if (!spliced.changed) return { ...spliced, backup: null };
          const backup = await backupOnce(configPath, source);
          await fs.writeFile(configPath, spliced.text, 'utf8');
          configGen += 1;
          writesDone += 1;
          return { ...spliced, backup };
        });
      } catch (error) {
        sendJson(res, 409, { error: `could not write ${configPath}: ${scrub(error)}` });
        return;
      }
      if ('error' in result) {
        sendJson(res, 409, result);
        return;
      }
      sendJson(res, 200, {
        configPath,
        backup: result.backup,
        added: result.added.length,
        skipped: result.skipped.length,
        changed: result.changed,
      });
      if (result.changed) {
        io.log(`Wrote ${result.added.length} entr${result.added.length === 1 ? 'y' : 'ies'} to ${configPath}.`);
        if (result.backup) io.log(`Backup: ${result.backup}`);
      } else {
        io.log(`Everything selected was already in ${configPath} — nothing written.`);
      }
      return;
    }

    if (url.pathname === '/api/config/write') {
      if (!configPath) {
        sendJson(res, 409, { error: 'no frznforge.config.ts was found — nothing to edit' });
        return;
      }
      const ops = asOperations(body.operations);
      let result: { changed: boolean; backup?: string | null };
      try {
        result = await serialize(async () => {
          const loaded = await importConfig();
          if (loaded.parsed === null) {
            throw conflict(`the current config does not load cleanly (${loaded.issues.join('; ')}) — fix it by hand first`);
          }
          // Judge the change on a clone before touching a byte of the file. The parsed clone is
          // also the yardstick the post-write verify holds the re-loaded file against.
          const candidate = applyOpsToInput(loaded.input, ops);
          let expected: FrznforgeConfig | null = null;
          if (candidate !== null) {
            const check = FrznforgeConfigSchema.safeParse(candidate);
            if (!check.success) {
              throw badRequest(
                `that change is not a valid config: ${check.error.issues.map((i) => `${i.path.join('.')}: ${i.message}`).join('; ')}`,
              );
            }
            expected = check.data;
          }
          const source = await fs.readFile(configPath, 'utf8');
          const edited = applyOpsToSource(source, ops);
          if ('error' in edited) throw conflict(edited.error);
          if (!edited.changed) return { changed: false };
          const backup = await backupOnce(configPath, source);
          await fs.writeFile(configPath, edited.text, 'utf8');
          configGen += 1;
          const verify = await importConfig();
          // 1. The written file must still load and parse — never leave a broken config.
          if (verify.parsed === null) {
            await restoreAndFail(source, `the edit did not produce a loadable config (${verify.issues.join('; ')})`);
          }
          // 2. It must load to the config the schema pre-check approved. If the textual edit
          //    diverged from the intended change — an edit that landed in a comment, a quoted
          //    or spread key that got appended as a dead duplicate, a match that hit nothing —
          //    the re-loaded config differs from `expected`, and the write is rolled back
          //    rather than silently applying the wrong thing.
          if (expected !== null && JSON.stringify(verify.parsed) !== JSON.stringify(expected)) {
            await restoreAndFail(
              source,
              'the edit did not change the config the way it should have (the file may use comments, quoted keys or a spread the editor cannot follow)',
            );
          }
          writesDone += 1;
          return { changed: true, backup };
        });
      } catch (error) {
        const status = typeof (error as { status?: number }).status === 'number' ? (error as { status: number }).status : 500;
        sendJson(res, status, { error: scrub(error) });
        return;
      }
      if (result.changed) {
        io.log(`Updated ${configPath} (${ops.length} change${ops.length === 1 ? '' : 's'}).`);
        if (result.backup) io.log(`Backup: ${result.backup}`);
      }
      sendJson(res, 200, { ...result, configPath });
      return;
    }

    if (url.pathname === '/api/profile/preview') {
      if (typeof body.body !== 'string') throw badRequest('expected a "body" string');
      // Trusted render, like the site gives the owner's own profile; no mermaid — the wizard
      // page ships no diagram bundle, so the fence stays an honest code block here.
      sendJson(res, 200, { html: renderMarkdown(body.body, { mermaid: false }) });
      return;
    }

    if (url.pathname === '/api/profile/write') {
      const nextBody = body.body;
      if (typeof nextBody !== 'string') throw badRequest('expected a "body" string');
      if (nextBody.includes('\u0000')) throw badRequest('the profile body cannot contain NUL bytes');
      let result: { changed: boolean; path: string; backup?: string | null };
      try {
        result = await serialize(async () => {
          const loaded = await importConfig();
          const file = profilePathFor(loaded.parsed);
          if (!file) {
            throw conflict(
              loaded.parsed === null
                ? `the config does not load cleanly (${loaded.issues.join('; ')}), so the profile location is not known — fix the config first`
                : 'the configured profile is not an editable markdown file inside the project',
            );
          }
          const existing = await fs.readFile(file, 'utf8').catch(() => null);
          const head = existing === null ? '' : existing.slice(0, frontmatterEndOffset(existing));
          const next = joinProfile(head, nextBody);
          if (existing === next) return { changed: false, path: file };
          let backup: string | null = null;
          if (existing === null) {
            await fs.mkdir(path.dirname(file), { recursive: true });
            // A file the wizard creates has no pre-wizard state, so its session backup is "none"
            // — recorded so a second save does not snapshot this first write as if it were it.
            markCreated(file);
          } else {
            backup = await backupOnce(file, existing);
          }
          await fs.writeFile(file, next, 'utf8');
          writesDone += 1;
          return { changed: true, path: file, backup };
        });
      } catch (error) {
        const status = typeof (error as { status?: number }).status === 'number' ? (error as { status: number }).status : 500;
        sendJson(res, status, { error: scrub(error) });
        return;
      }
      if (result.changed) {
        io.log(`Updated ${result.path}.`);
        if (result.backup) io.log(`Backup: ${result.backup}`);
      }
      sendJson(res, 200, result);
      return;
    }

    if (url.pathname === '/api/done') {
      await serialize(async () => undefined); // drain queued writes before saying goodbye
      sendJson(res, 200, { done: true, writes: writesDone }, true);
      finishAfter(
        res,
        EXIT.ok,
        writesDone === 0
          ? 'Done — nothing was changed.'
          : `Done — ${writesDone} write${writesDone === 1 ? '' : 's'} this session. Next: npm run build`,
      );
      return;
    }

    if (url.pathname === '/api/cancel') {
      await serialize(async () => undefined);
      sendJson(res, 200, { cancelled: true, writes: writesDone }, true);
      finishAfter(
        res,
        EXIT.ok,
        writesDone === 0 ? 'Cancelled — nothing was written.' : `Stopped. ${wroteSummary()}`,
      );
      return;
    }

    sendJson(res, 404, { error: `no such endpoint: ${url.pathname}` });
  }

  /* ---------------------------------------------------------------- go */

  await new Promise<void>((resolve, reject) => {
    server.once('error', reject);
    server.listen(opts.port && opts.port > 0 ? opts.port : 0, '127.0.0.1', () => {
      server.off('error', reject);
      resolve();
    });
  });

  const url = `http://127.0.0.1:${port()}/?s=${sessionToken}`;
  io.log('frznforge init — the wizard is open in your browser.');
  io.log(`  ${url}`);
  io.log('  Only this machine can reach it, and only with the key in that URL. Ctrl-C to stop.');
  if (!configPath) io.log('  No frznforge.config.ts found — the wizard will show a snippet to paste instead.');
  bump();
  process.once('SIGINT', onSigint);
  server.on('error', (error) => finish(EXIT.failed, `error: ${scrub(error)}`));
  if (!opts.noOpen) openBrowser(url, io);

  return done;
}

/**
 * Best-effort browser launch. A failure is not an error: the URL is on stdout either way, which
 * is also the only thing that works over SSH.
 */
function openBrowser(url: string, io: Io): void {
  const [command, args] =
    process.platform === 'win32'
      ? [process.env.ComSpec ?? 'cmd.exe', ['/d', '/s', '/c', 'start', '', url]]
      : process.platform === 'darwin'
        ? ['open', [url]]
        : ['xdg-open', [url]];
  try {
    const child = spawn(command, args, { detached: true, stdio: 'ignore', windowsHide: true });
    child.on('error', () => io.log('  (could not open a browser automatically — open the URL above)'));
    child.unref();
  } catch {
    io.log('  (could not open a browser automatically — open the URL above)');
  }
}
