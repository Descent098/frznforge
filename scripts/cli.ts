#!/usr/bin/env tsx
/**
 * `npm run frznforge -- <command>` — the interactive side of frznforge.
 *
 * Today there is exactly one command, `init`: pick a provider, list an account's repos over
 * the provider's REST API, choose the ones you want, and get them written into
 * `frznforge.config.ts`.
 *
 * Rules this file lives by:
 *  - **Tokens are read from the environment and never written anywhere.** `init` reports
 *    *which* variable it consulted and whether a value was found, never the value itself,
 *    and it never offers to type one in.
 *  - **The config file is edited textually.** Re-serialising it would throw away the user's
 *    comments and formatting, so entries are spliced into the existing `repos: [...]` array
 *    and everything else is left byte-for-byte alone.
 *  - **Never hang.** When stdin is not a TTY and the flags do not fully describe the run, we
 *    print how to do it non-interactively and exit 1.
 *
 * The provider *listing* endpoints live here rather than in `src/lib/importers` on purpose:
 * the `Importer` contract is about one known repo (metadata + releases), and account
 * enumeration is a setup-time concern that no build step needs.
 */
import fs from 'node:fs/promises';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import {
  DEFAULT_USER_AGENT,
  redactToken,
  resolveToken,
  tokenEnvFor,
  type RepoSourceConfig,
} from '../src/lib/importers/types';
import { InputClosedError, Prompter } from './lib/prompt';

/* ------------------------------------------------------------------ providers */

export type ProviderName = 'github' | 'gitlab' | 'gitea' | 'forgejo';

export interface ProviderInfo {
  label: string;
  /** API base written into the config when the user does not override it. */
  defaultHost: string | null;
  /** Self-hosted providers must be asked for a host; the SaaS ones only offer it. */
  hostRequired: boolean;
  /** Suggested value for the host prompt. */
  hostSuggestion: string;
  accountLabel: string;
  /** Env var documented in the help text (the conventional one, not the FRZNFORGE_ alias). */
  tokenScopes: string;
}

export const PROVIDERS: Record<ProviderName, ProviderInfo> = {
  github: {
    label: 'GitHub',
    defaultHost: 'https://api.github.com',
    hostRequired: false,
    hostSuggestion: 'https://api.github.com',
    accountLabel: 'user or organisation',
    tokenScopes: 'public_repo (add repo for private repositories)',
  },
  gitlab: {
    label: 'GitLab',
    defaultHost: 'https://gitlab.com',
    hostRequired: false,
    hostSuggestion: 'https://gitlab.com',
    accountLabel: 'user or group path',
    tokenScopes: 'read_api',
  },
  gitea: {
    label: 'Gitea',
    defaultHost: null,
    hostRequired: true,
    hostSuggestion: 'https://gitea.example.com',
    accountLabel: 'user or organisation',
    tokenScopes: 'read:repository',
  },
  forgejo: {
    label: 'Forgejo',
    defaultHost: null,
    hostRequired: true,
    hostSuggestion: 'https://codeberg.org',
    accountLabel: 'user or organisation',
    tokenScopes: 'read:repository',
  },
};

export const PROVIDER_NAMES = Object.keys(PROVIDERS) as ProviderName[];

/** Anything that should stop the CLI with a message but no stack trace. */
export class CliError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'CliError';
  }
}

/* ------------------------------------------------------------------ arguments */

export interface CliFlags {
  provider?: ProviderName;
  host?: string;
  account?: string;
  select?: string;
  releases?: 'provider' | 'tags';
  /** Config file to edit; defaults to `frznforge.config.ts` next to package.json. */
  config?: string;
  /** Print the snippet and exit without touching any file. */
  print: boolean;
  /** Skip the write confirmation (scripting). */
  yes: boolean;
  help: boolean;
}

export interface ParsedArgs {
  command: string | null;
  flags: CliFlags;
  errors: string[];
}

const VALUE_FLAGS = ['provider', 'host', 'account', 'select', 'releases', 'config'] as const;
const BOOL_FLAGS = ['print', 'yes', 'help'] as const;

export const USAGE = `frznforge — static read-only forge site generator

Usage
  npm run frznforge -- init [options]
  npm run frznforge -- --help

Commands
  init        Discover repositories on a provider and add them to frznforge.config.ts.

Options (init)
  --provider=<github|gitlab|gitea|forgejo>   Provider to query.
  --host=<url>                               API/instance base URL (required for gitea/forgejo).
  --account=<name>                           User, organisation or GitLab group path.
  --select=<all|1,3,5-8|name,name>           Which of the listed repos to add.
  --releases=<provider|tags>                 Where releases come from (default: provider).
  --config=<path>                            Config file to edit (default: the nearest
                                             frznforge.config.ts at or above the cwd).
  --print                                    Print the snippet only; never write a file.
  --yes                                      Write without the confirmation prompt.
  --help, -h                                 Show this message.

Tokens are read from the environment only and are never written to the config:
  GitHub   FRZNFORGE_GITHUB_TOKEN  or GITHUB_TOKEN    scope: public_repo (repo for private)
  GitLab   FRZNFORGE_GITLAB_TOKEN  or GITLAB_TOKEN    scope: read_api
  Gitea    FRZNFORGE_GITEA_TOKEN   or GITEA_TOKEN     scope: read:repository
  Forgejo  FRZNFORGE_FORGEJO_TOKEN or FORGEJO_TOKEN   scope: read:repository

Without a token only public repositories are listed. See docs/user/importing.md.`;

/**
 * Parse `--key=value`, `--key value` and boolean flags. Unknown flags and bad enum values
 * are collected into `errors` rather than thrown, so the caller can print usage once.
 */
export function parseArgs(argv: string[]): ParsedArgs {
  const flags: CliFlags = { print: false, yes: false, help: false };
  const errors: string[] = [];
  let command: string | null = null;
  const raw: Record<string, string> = {};

  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i]!;
    if (arg === '-h') {
      flags.help = true;
      continue;
    }
    if (!arg.startsWith('--')) {
      if (command === null) command = arg;
      else errors.push(`unexpected argument: ${arg}`);
      continue;
    }
    const body = arg.slice(2);
    const eq = body.indexOf('=');
    const key = eq === -1 ? body : body.slice(0, eq);
    let value = eq === -1 ? undefined : body.slice(eq + 1);

    if ((BOOL_FLAGS as readonly string[]).includes(key)) {
      if (value !== undefined && value !== 'true' && value !== 'false') {
        errors.push(`--${key} does not take a value`);
        continue;
      }
      flags[key as (typeof BOOL_FLAGS)[number]] = value !== 'false';
      continue;
    }
    if (!(VALUE_FLAGS as readonly string[]).includes(key)) {
      errors.push(`unknown option: --${key}`);
      continue;
    }
    if (value === undefined) {
      const next = argv[i + 1];
      if (next === undefined || next.startsWith('--')) {
        errors.push(`--${key} needs a value`);
        continue;
      }
      value = next;
      i += 1;
    }
    raw[key] = value;
  }

  if (raw.provider !== undefined) {
    if ((PROVIDER_NAMES as string[]).includes(raw.provider)) flags.provider = raw.provider as ProviderName;
    else errors.push(`--provider must be one of ${PROVIDER_NAMES.join(', ')} (got ${JSON.stringify(raw.provider)})`);
  }
  if (raw.releases !== undefined) {
    if (raw.releases === 'provider' || raw.releases === 'tags') flags.releases = raw.releases;
    else errors.push(`--releases must be 'provider' or 'tags' (got ${JSON.stringify(raw.releases)})`);
  }
  if (raw.host !== undefined) flags.host = raw.host.replace(/\/+$/, '');
  if (raw.account !== undefined) flags.account = raw.account.trim();
  if (raw.select !== undefined) flags.select = raw.select;
  if (raw.config !== undefined) flags.config = raw.config;

  return { command, flags, errors };
}

/* ------------------------------------------------------------------ selection */

export type Result<T> = { ok: true; value: T } | { ok: false; error: string };

/**
 * Parse a numbered multi-select: `all`, `none`/empty, or a comma/space separated list of
 * 1-based indexes and `a-b` ranges. Returns sorted, de-duplicated 0-based indexes.
 */
export function parseSelection(spec: string, count: number): Result<number[]> {
  const text = spec.trim().toLowerCase();
  if (text === 'all' || text === '*') return { ok: true, value: Array.from({ length: count }, (_, i) => i) };
  if (text === '' || text === 'none') return { ok: true, value: [] };

  const picked = new Set<number>();
  for (const token of text.split(/[,\s]+/).filter(Boolean)) {
    const range = /^(\d+)\s*-\s*(\d+)$/.exec(token);
    if (range) {
      const from = Number(range[1]);
      const to = Number(range[2]);
      if (from > to) return { ok: false, error: `range ${token} runs backwards` };
      for (let n = from; n <= to; n += 1) {
        if (n < 1 || n > count) return { ok: false, error: `${n} is out of range (1-${count})` };
        picked.add(n - 1);
      }
      continue;
    }
    if (!/^\d+$/.test(token)) return { ok: false, error: `not a number: ${token}` };
    const n = Number(token);
    if (n < 1 || n > count) return { ok: false, error: `${n} is out of range (1-${count})` };
    picked.add(n - 1);
  }
  return { ok: true, value: [...picked].sort((a, b) => a - b) };
}

/**
 * Resolve a selection against a listing. Numeric tokens go through `parseSelection`; every
 * other token is matched against a repo's name or `owner/name` (case-insensitive), so
 * scripts can say `--select=ezcv,sdu` without depending on the API's ordering.
 */
export function selectRepos<T extends { name: string; fullName: string }>(repos: T[], spec: string): Result<T[]> {
  const text = spec.trim();
  const looksNumeric = text === '' || /^(all|none|\*)$/i.test(text) || /^[\d,\s-]+$/.test(text);
  if (looksNumeric) {
    const parsed = parseSelection(text, repos.length);
    return parsed.ok ? { ok: true, value: parsed.value.map((i) => repos[i]!) } : parsed;
  }

  const picked: T[] = [];
  const seen = new Set<T>();
  for (const token of text.split(/[,\s]+/).filter(Boolean)) {
    if (/^\d+$/.test(token)) {
      const n = Number(token);
      if (n < 1 || n > repos.length) return { ok: false, error: `${n} is out of range (1-${repos.length})` };
      const repo = repos[n - 1]!;
      if (!seen.has(repo)) {
        seen.add(repo);
        picked.push(repo);
      }
      continue;
    }
    const needle = token.toLowerCase();
    const match = repos.find((r) => r.name.toLowerCase() === needle || r.fullName.toLowerCase() === needle);
    if (!match) return { ok: false, error: `no repository named ${token}` };
    if (!seen.has(match)) {
      seen.add(match);
      picked.push(match);
    }
  }
  return { ok: true, value: picked };
}

/* ------------------------------------------------------------------ config entries */

export interface RepoEntry {
  type: ProviderName;
  host?: string;
  owner?: string;
  repo?: string;
  /** GitLab only: full namespaced path. */
  project?: string;
  slug?: string;
  releases?: 'provider' | 'tags';
}

const ENTRY_KEY_ORDER = ['type', 'host', 'owner', 'repo', 'project', 'slug', 'releases'] as const;

/** Single-quoted JS string literal, matching the style of frznforge.config.ts. */
function quote(value: string): string {
  return `'${value.replace(/\\/g, '\\\\').replace(/'/g, "\\'")}'`;
}

/** Repo directory basename → slug: lowercase, non-[a-z0-9] runs → '-', trimmed. */
export function slugify(name: string): string {
  const s = name.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '');
  return s.length ? s : 'repo';
}

/** One config array element, on one line: `{ type: 'github', owner: 'a', repo: 'b' }`. */
export function renderEntry(entry: RepoEntry): string {
  const parts: string[] = [];
  for (const key of ENTRY_KEY_ORDER) {
    const value = entry[key];
    if (value !== undefined) parts.push(`${key}: ${quote(value)}`);
  }
  return `{ ${parts.join(', ')} }`;
}

/** The `repos: [...]` fragment a user can paste by hand. */
export function renderSnippet(entries: RepoEntry[]): string {
  const body = entries.map((e) => `    ${renderEntry(e)},`).join('\n');
  return `  repos: [\n${body}\n  ],`;
}

/**
 * Identity of a source, ignoring cosmetic fields: two entries with the same key describe the
 * same remote repository and must never be added twice.
 */
export function entryKey(entry: RepoEntry): string {
  const host = (entry.host ?? PROVIDERS[entry.type].defaultHost ?? '').replace(/\/+$/, '').toLowerCase();
  const id = entry.project ?? `${entry.owner ?? ''}/${entry.repo ?? ''}`;
  return `${entry.type}|${host}|${id.toLowerCase()}`;
}

/** Build a config entry for one listed repo. */
export function entryFor(
  provider: ProviderName,
  host: string,
  repo: RemoteRepo,
  releases: 'provider' | 'tags',
  slug?: string,
): RepoEntry {
  const entry: RepoEntry = { type: provider };
  const normalisedHost = host.replace(/\/+$/, '');
  if (normalisedHost && normalisedHost !== PROVIDERS[provider].defaultHost) entry.host = normalisedHost;
  if (provider === 'gitlab') entry.project = repo.project ?? repo.fullName;
  else {
    entry.owner = repo.owner;
    entry.repo = repo.name;
  }
  if (slug) entry.slug = slug;
  entry.releases = releases;
  return entry;
}

/**
 * Entries for a whole selection, adding an explicit `slug` only where two picked repos would
 * otherwise collide (the default slug is the repo name).
 */
export function entriesFor(
  provider: ProviderName,
  host: string,
  repos: RemoteRepo[],
  releases: 'provider' | 'tags',
): RepoEntry[] {
  const counts = new Map<string, number>();
  for (const r of repos) {
    const s = slugify(r.name);
    counts.set(s, (counts.get(s) ?? 0) + 1);
  }
  return repos.map((r) => {
    const base = slugify(r.name);
    const slug = (counts.get(base) ?? 0) > 1 ? slugify(`${r.owner}-${r.name}`) : undefined;
    return entryFor(provider, host, r, releases, slug);
  });
}

/* ------------------------------------------------------------------ config editing */

/** Walk `text` from `open` (an opening bracket) to its match, skipping strings and comments. */
function matchBracket(text: string, open: number): number {
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
function lastMeaningfulIndex(body: string): number {
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

/** Split an array body into its top-level elements (used to read the entries already there). */
function topLevelItems(body: string): string[] {
  const items: string[] = [];
  let depth = 0;
  let start = 0;
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
      items.push(body.slice(start, i));
      start = i + 1;
    }
  }
  items.push(body.slice(start));
  return items.filter((s) => s.trim() !== '');
}

/**
 * Strip line and block comments out of one array element, leaving string literals intact.
 *
 * `topLevelItems` skips comments when it looks for separators, but the slice it hands back
 * still contains them — and `readField` takes the *first* match, so a commented-out entry
 * sitting above a real one is read instead of the real one. That makes `init` skip a repo
 * that is only mentioned in a comment, or add a duplicate of one that is really there.
 * frznforge.config.ts ships with comments inside `repos: [ … ]`, so this is the normal case.
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
function readField(item: string, key: string): string | undefined {
  const m = new RegExp(`\\b${key}\\s*:\\s*(['"\`])([^'"\`]*)\\1`).exec(item);
  return m ? m[2] : undefined;
}

/** Identity keys of the remote sources already present in an array body. */
function existingKeys(body: string): Set<string> {
  const keys = new Set<string>();
  for (const raw of topLevelItems(body)) {
    const item = stripComments(raw);
    const type = readField(item, 'type');
    if (!type || !(PROVIDER_NAMES as string[]).includes(type)) continue;
    keys.add(
      entryKey({
        type: type as ProviderName,
        host: readField(item, 'host'),
        owner: readField(item, 'owner'),
        repo: readField(item, 'repo'),
        project: readField(item, 'project'),
      }),
    );
  }
  return keys;
}

export interface InsertResult {
  text: string;
  added: RepoEntry[];
  /** Entries already present in the file (matched on `entryKey`). */
  skipped: RepoEntry[];
  changed: boolean;
}

/**
 * Splice entries into the `repos: [...]` array of a config file's source text.
 *
 * Purely textual on purpose: nothing outside the array is touched, so comments, formatting
 * and any exotic syntax the user has in there survive. Returns `null` when no `repos` array
 * can be found, which is the caller's cue to fall back to "paste this yourself".
 *
 * Idempotent: an entry whose `entryKey` already appears in the array is reported as
 * `skipped`, so running init twice adds nothing the second time.
 */
export function insertRepos(source: string, entries: RepoEntry[]): InsertResult | null {
  const match = /(^|[\n{,])([ \t]*)repos\s*:\s*\[/.exec(source);
  if (!match) return null;
  const open = match.index + match[0].length - 1;
  const close = matchBracket(source, open);
  if (close === -1) return null;

  const indent = match[2] ?? '  ';
  const itemIndent = `${indent}  `;
  const body = source.slice(open + 1, close);
  const present = existingKeys(body);

  const added: RepoEntry[] = [];
  const skipped: RepoEntry[] = [];
  for (const entry of entries) {
    const key = entryKey(entry);
    if (present.has(key)) skipped.push(entry);
    else {
      present.add(key);
      added.push(entry);
    }
  }
  if (added.length === 0) return { text: source, added, skipped, changed: false };

  const lines = added.map((e) => `${itemIndent}${renderEntry(e)},`).join('\n');
  let newBody: string;
  const last = lastMeaningfulIndex(body);
  if (last === -1) {
    newBody = `\n${lines}\n${indent}`;
  } else {
    const head = body.slice(0, last + 1);
    const comma = body[last] === ',' ? '' : ',';
    const tail = body.slice(last + 1);
    const trailing = /\s*$/.exec(tail)![0];
    const keep = tail.slice(0, tail.length - trailing.length);
    const closeGap = trailing.includes('\n') ? trailing : `\n${indent}`;
    newBody = `${head}${comma}${keep}\n${lines}${closeGap}`;
  }
  return { text: `${source.slice(0, open + 1)}${newBody}${source.slice(close)}`, added, skipped, changed: true };
}

/** `frznforge.config.ts` → `frznforge.config.ts.20260823T195100Z.bak` */
export function backupPathFor(file: string, now: Date = new Date()): string {
  const stamp = now.toISOString().replace(/[-:]/g, '').replace(/\.\d+Z$/, 'Z');
  return `${file}.${stamp}.bak`;
}

export interface WriteResult extends InsertResult {
  backup: string | null;
}

/**
 * Apply `insertRepos` to a file on disk, taking a timestamped `.bak` copy first. Writing is
 * skipped (and no backup is made) when nothing would change.
 */
export async function updateConfigFile(
  file: string,
  entries: RepoEntry[],
  options: { now?: Date } = {},
): Promise<WriteResult | null> {
  const source = await fs.readFile(file, 'utf8');
  const result = insertRepos(source, entries);
  if (!result) return null;
  if (!result.changed) return { ...result, backup: null };
  const backup = backupPathFor(file, options.now ?? new Date());
  await fs.writeFile(backup, source, 'utf8');
  await fs.writeFile(file, result.text, 'utf8');
  return { ...result, backup };
}

/** Nearest `frznforge.config.ts` at or above `from`. */
export async function findConfigFile(from: string): Promise<string | null> {
  let dir = path.resolve(from);
  for (;;) {
    const candidate = path.join(dir, 'frznforge.config.ts');
    try {
      await fs.access(candidate);
      return candidate;
    } catch {
      /* keep walking */
    }
    const parent = path.dirname(dir);
    if (parent === dir) return null;
    dir = parent;
  }
}

/* ------------------------------------------------------------------ tokens */

/** A `RepoSourceConfig` shaped just enough for `tokenEnvFor` to answer for a provider. */
function sourceStub(provider: ProviderName, tokenEnv?: string): RepoSourceConfig {
  const extra = tokenEnv ? { tokenEnv } : {};
  switch (provider) {
    case 'gitlab':
      return { type: 'gitlab', project: 'x/y', host: 'https://gitlab.com', ...extra };
    case 'github':
      return { type: 'github', owner: 'x', repo: 'y', host: 'https://api.github.com', ...extra };
    default:
      return { type: provider, owner: 'x', repo: 'y', host: 'https://example.com', ...extra };
  }
}

export interface TokenStatus {
  /** Env vars consulted, in order. */
  names: string[];
  /** The variable a value came from, or null. */
  from: string | null;
  token: string | null;
}

/** Which env vars a provider's token can come from, and whether one of them is set. */
export function tokenStatus(
  provider: ProviderName,
  env: Record<string, string | undefined> = process.env,
  tokenEnv?: string,
): TokenStatus {
  const source = sourceStub(provider, tokenEnv);
  const names = tokenEnvFor(source);
  const token = resolveToken(source, env);
  const from = token === null ? null : (names.find((n) => (env[n] ?? '').trim() === token) ?? null);
  return { names, from, token };
}

/**
 * The line printed before any API call. It names the variables and says whether a value was
 * found — it must never contain the value itself, and there is a test that asserts exactly
 * that.
 */
export function tokenMessage(status: TokenStatus): string {
  const list = status.names.map((n) => `$${n}`).join(', ');
  if (status.from) {
    return `Token: using $${status.from} (value never shown, never written to the config).`;
  }
  return `Token: none set (checked ${list}) — listing public repositories only.`;
}

/* ------------------------------------------------------------------ listing */

export interface RemoteRepo {
  /** Repo name (GitLab: the last path segment). */
  name: string;
  /** `owner/name`, or the full namespaced path on GitLab. */
  fullName: string;
  owner: string;
  /** GitLab only: full namespaced project path. */
  project?: string;
  description: string | null;
  archived: boolean;
  private: boolean;
  fork: boolean;
}

export interface ListOptions {
  host: string;
  account: string;
  token?: string | null;
  fetchImpl?: typeof fetch;
  userAgent?: string;
  /** Safety valve so a huge account cannot spin forever. */
  maxPages?: number;
  /** Where a non-fatal listing problem (a truncated page walk) is reported. */
  warn?: (line: string) => void;
}

function headersFor(provider: ProviderName, token: string | null | undefined, userAgent: string): HeadersInit {
  const headers: Record<string, string> = { accept: 'application/json', 'user-agent': userAgent };
  if (!token) return headers;
  if (provider === 'gitlab') headers['private-token'] = token;
  else if (provider === 'github') headers.authorization = `Bearer ${token}`;
  else headers.authorization = `token ${token}`;
  return headers;
}

/** GET one JSON page, turning every failure into a `CliError` with the token scrubbed out. */
async function getJson(
  url: string,
  provider: ProviderName,
  opts: ListOptions,
): Promise<{ status: number; body: unknown; headers: Headers }> {
  const doFetch = opts.fetchImpl ?? globalThis.fetch;
  const userAgent = opts.userAgent ?? DEFAULT_USER_AGENT;
  let response: Response;
  try {
    response = await doFetch(url, { headers: headersFor(provider, opts.token, userAgent) });
  } catch (cause) {
    const detail = redactToken(cause instanceof Error ? cause.message : String(cause), opts.token);
    throw new CliError(`could not reach ${url}: ${detail}`);
  }
  let body: unknown = null;
  const text = await response.text();
  if (text.trim() !== '') {
    try {
      body = JSON.parse(text);
    } catch {
      throw new CliError(`${url} did not return JSON (HTTP ${response.status})`);
    }
  }
  return { status: response.status, body, headers: response.headers };
}

/** Turn a non-2xx listing response into an actionable message. Never echoes the token. */
function listingError(url: string, status: number, headers: Headers, provider: ProviderName, hasToken: boolean): CliError {
  const envs = tokenEnvFor(sourceStub(provider)).map((n) => `$${n}`).join(' or ');
  if (headers.get('x-ratelimit-remaining') === '0' || status === 429) {
    return new CliError(
      `rate limited by ${new URL(url).host} (HTTP ${status}). ` +
        (hasToken ? 'Wait for the limit to reset and try again.' : `Set ${envs} to raise the limit.`),
    );
  }
  if (status === 401 || status === 403) {
    return new CliError(
      `not authorised (HTTP ${status}). ` +
        (hasToken ? `The token in ${envs} may be expired or missing a read scope.` : `Set ${envs} for private repositories.`),
    );
  }
  if (status === 404) {
    return new CliError(`no such account (HTTP 404): ${url}`);
  }
  return new CliError(`${url} failed with HTTP ${status}`);
}

/**
 * Page through a provider endpoint until a short page comes back.
 *
 * Stopping at `maxPages` while the last page was still full means the listing is incomplete.
 * That has to be said out loud: a caller cannot tell a truncated list from a complete one, and
 * `--select=1,3,5-8` written against a truncated listing silently picks the wrong repos.
 */
async function paginate(
  provider: ProviderName,
  opts: ListOptions,
  pageUrl: (page: number) => string,
  perPage: number,
): Promise<unknown[]> {
  const maxPages = opts.maxPages ?? 10;
  const out: unknown[] = [];
  let truncated = false;
  for (let page = 1; ; page += 1) {
    if (page > maxPages) {
      truncated = true;
      break;
    }
    const url = pageUrl(page);
    const { status, body, headers } = await getJson(url, provider, opts);
    if (status < 200 || status >= 300) throw listingError(url, status, headers, provider, Boolean(opts.token));
    if (!Array.isArray(body)) throw new CliError(`${url} returned ${typeof body}, expected a list`);
    out.push(...body);
    if (body.length < perPage) break;
  }
  if (truncated) {
    opts.warn?.(
      `warning: listing stopped after ${out.length} repositories (${maxPages}-page cap) and is incomplete — ` +
        `use --select=name,name to name the repositories you want.`,
    );
  }
  return out;
}

function str(value: unknown): string | null {
  return typeof value === 'string' && value !== '' ? value : null;
}

function bool(value: unknown): boolean {
  return value === true;
}

/**
 * List an account's repositories.
 *
 * User endpoints are tried before organisation/group ones because a personal account is the
 * common case; a 404 on the first is not an error, it just means "try the other one".
 * Results are sorted by full name so a `--select=1,2` in a script is stable.
 */
export async function listRepos(provider: ProviderName, opts: ListOptions): Promise<RemoteRepo[]> {
  const host = opts.host.replace(/\/+$/, '');
  const account = opts.account.trim();
  const enc = encodeURIComponent(account);

  const attempts: Array<(page: number) => string> = [];
  let perPage = 100;
  if (provider === 'github') {
    attempts.push(
      (p) => `${host}/users/${enc}/repos?per_page=100&page=${p}&sort=full_name`,
      (p) => `${host}/orgs/${enc}/repos?per_page=100&page=${p}&sort=full_name`,
    );
  } else if (provider === 'gitlab') {
    attempts.push(
      (p) => `${host}/api/v4/users/${enc}/projects?per_page=100&page=${p}&order_by=path&sort=asc`,
      (p) => `${host}/api/v4/groups/${enc}/projects?per_page=100&page=${p}&include_subgroups=true&order_by=path&sort=asc`,
    );
  } else {
    perPage = 50;
    attempts.push(
      (p) => `${host}/api/v1/users/${enc}/repos?limit=50&page=${p}`,
      (p) => `${host}/api/v1/orgs/${enc}/repos?limit=50&page=${p}`,
    );
  }

  let raw: unknown[] | null = null;
  let lastError: unknown = null;
  for (const build of attempts) {
    try {
      raw = await paginate(provider, opts, build, perPage);
      break;
    } catch (error) {
      lastError = error;
      if (error instanceof CliError && /HTTP 404/.test(error.message)) continue;
      throw error;
    }
  }
  if (raw === null) throw lastError instanceof Error ? lastError : new CliError(`no repositories found for ${account}`);

  const repos: RemoteRepo[] = [];
  for (const item of raw) {
    if (typeof item !== 'object' || item === null) continue;
    const r = item as Record<string, unknown>;
    if (provider === 'gitlab') {
      const full = str(r.path_with_namespace);
      if (!full) continue;
      const segments = full.split('/');
      repos.push({
        name: segments[segments.length - 1]!,
        fullName: full,
        owner: segments.slice(0, -1).join('/'),
        project: full,
        description: str(r.description),
        archived: bool(r.archived),
        private: str(r.visibility) !== null && r.visibility !== 'public',
        fork: r.forked_from_project !== undefined && r.forked_from_project !== null,
      });
      continue;
    }
    const name = str(r.name);
    if (!name) continue;
    const owner = typeof r.owner === 'object' && r.owner !== null ? str((r.owner as Record<string, unknown>).login) : null;
    const full = str(r.full_name) ?? (owner ? `${owner}/${name}` : name);
    repos.push({
      name,
      fullName: full,
      owner: owner ?? full.split('/')[0] ?? account,
      description: str(r.description),
      archived: bool(r.archived),
      private: bool(r.private),
      fork: bool(r.fork),
    });
  }
  repos.sort((a, b) => a.fullName.localeCompare(b.fullName));
  return repos;
}

/* ------------------------------------------------------------------ run planning */

export const NON_TTY_MESSAGE = `frznforge init is interactive, but stdin is not a terminal.

Run it from a terminal:
  npm run frznforge -- init

…or describe the whole run with flags (works in CI and scripts):
  npm run frznforge -- init --provider=github --account=YOU --select=all --print
  npm run frznforge -- init --provider=forgejo --host=https://codeberg.org --account=YOU --select=1-3 --yes

…or add the entries to frznforge.config.ts by hand:
  repos: [
    { type: 'github', owner: 'YOU', repo: 'REPO', releases: 'provider' },
  ]

See docs/user/importing.md for the full reference.`;

export interface InitPlan {
  provider: ProviderName;
  host: string;
  account: string;
  select: string;
  releases: 'provider' | 'tags';
}

/**
 * Can this run go ahead without asking anything? Returns the plan, or the list of answers
 * that are still missing (used to decide whether a non-TTY run must bail out).
 */
export function planFromFlags(flags: CliFlags): { ok: true; value: InitPlan } | { ok: false; missing: string[] } {
  const missing: string[] = [];
  if (!flags.provider) missing.push('--provider');
  const provider = flags.provider;
  const host = flags.host ?? (provider ? PROVIDERS[provider].defaultHost : null);
  if (provider && PROVIDERS[provider].hostRequired && !flags.host) missing.push('--host');
  if (!flags.account) missing.push('--account');
  if (flags.select === undefined) missing.push('--select');
  if (missing.length > 0 || !provider || !host) return { ok: false, missing };
  return {
    ok: true,
    value: {
      provider,
      host,
      account: flags.account!,
      select: flags.select!,
      releases: flags.releases ?? 'provider',
    },
  };
}

/* ------------------------------------------------------------------ init command */

export interface Io {
  log: (line: string) => void;
  error: (line: string) => void;
  isTty: boolean;
  env: Record<string, string | undefined>;
  cwd: string;
  fetchImpl?: typeof fetch;
}

function describeRepo(repo: RemoteRepo): string {
  const badges = [repo.private ? 'private' : null, repo.archived ? 'archived' : null, repo.fork ? 'fork' : null]
    .filter(Boolean)
    .join(', ');
  const suffix = badges ? ` (${badges})` : '';
  const desc = repo.description ? ` — ${repo.description.slice(0, 70)}` : '';
  return `${repo.fullName}${suffix}${desc}`;
}

async function runInit(flags: CliFlags, io: Io): Promise<number> {
  const planned = planFromFlags(flags);
  // Everything the run needs is on the command line → no prompts, no terminal required.
  if (!planned.ok && !io.isTty) {
    io.error(NON_TTY_MESSAGE);
    return 1;
  }
  const prompter = io.isTty ? new Prompter() : null;
  try {
    let plan: InitPlan;
    if (planned.ok) {
      plan = planned.value;
    } else {
      const p = prompter!;
      p.write('frznforge init — add repositories from a hosting provider.\n');
      const provider =
        flags.provider ??
        (await p.choose<ProviderName>(
          'Provider',
          PROVIDER_NAMES.map((name) => ({ value: name, label: PROVIDERS[name].label, hint: PROVIDERS[name].tokenScopes })),
        ));
      const chosen = PROVIDERS[provider];
      const host =
        flags.host ??
        (chosen.hostRequired
          ? (await p.askRequired(`${chosen.label} instance URL`, chosen.hostSuggestion)).replace(/\/+$/, '')
          : chosen.defaultHost!);
      const account = flags.account ?? (await p.askRequired(`Account (${chosen.accountLabel})`));
      plan = { provider, host, account, select: flags.select ?? '', releases: flags.releases ?? 'provider' };
    }

    const info = PROVIDERS[plan.provider];
    const status = tokenStatus(plan.provider, io.env);
    io.log(`Provider: ${info.label} (${plan.host})`);
    io.log(tokenMessage(status));
    io.log(`Listing repositories for ${plan.account}…`);

    const repos = await listRepos(plan.provider, {
      host: plan.host,
      account: plan.account,
      token: status.token,
      fetchImpl: io.fetchImpl,
      warn: (line) => io.error(line),
    });
    if (repos.length === 0) {
      io.error(`No repositories visible for ${plan.account}. ${status.from ? '' : 'A token may reveal private ones.'}`);
      return 1;
    }

    let picked: RemoteRepo[] = [];
    if (flags.select !== undefined) {
      const selection = selectRepos(repos, flags.select);
      if (!selection.ok) {
        io.error(`--select: ${selection.error}`);
        return 1;
      }
      picked = selection.value;
    } else {
      const p = prompter!;
      p.write('');
      repos.forEach((r, i) => p.write(`  ${String(i + 1).padStart(3)}. ${describeRepo(r)}`));
      p.write('');
      for (;;) {
        const spec = await p.ask('Select repositories (e.g. 1,3,5-8 or "all")', 'all');
        const selection = selectRepos(repos, spec);
        if (selection.ok && selection.value.length > 0) {
          picked = selection.value;
          break;
        }
        p.write(`  ${selection.ok ? 'nothing selected' : selection.error}.`);
      }
    }
    // Only ask when the run was not already fully described by flags, and never when --print
    // was asked for: both of those are the scriptable forms, and blocking on a prompt there
    // would make a terminal behave differently from CI over the very same command line.
    if (flags.releases === undefined && prompter && !planned.ok && !flags.print) {
      plan.releases = await prompter.choose<'provider' | 'tags'>('Where should releases come from?', [
        { value: 'provider', label: `${info.label} releases`, hint: 'imported over the API' },
        { value: 'tags', label: 'annotated git tags', hint: 'no API calls, works offline' },
      ]);
    }

    if (picked.length === 0) {
      io.log('Nothing selected — no changes made.');
      return 0;
    }

    const entries = entriesFor(plan.provider, plan.host, picked, plan.releases);
    io.log('');
    io.log(renderSnippet(entries));
    io.log('');

    if (flags.print) return 0;

    const configFile = flags.config ? path.resolve(io.cwd, flags.config) : await findConfigFile(io.cwd);
    if (!configFile) {
      io.log('No frznforge.config.ts found — paste the snippet above into your config.');
      return 0;
    }

    const source = await fs.readFile(configFile, 'utf8').catch(() => null);
    if (source === null) {
      io.log(`Could not read ${configFile} — paste the snippet above into your config.`);
      return 0;
    }
    const preview = insertRepos(source, entries);
    if (!preview) {
      io.log(`Could not find a repos: [...] array in ${configFile} — paste the snippet above by hand.`);
      return 0;
    }
    if (!preview.changed) {
      io.log(`All ${entries.length} entr${entries.length === 1 ? 'y is' : 'ies are'} already in ${configFile}. Nothing to do.`);
      return 0;
    }

    io.log(`${configFile}`);
    for (const entry of preview.added) io.log(`  + ${renderEntry(entry)},`);
    for (const entry of preview.skipped) io.log(`  = ${renderEntry(entry)},   (already present, skipped)`);
    io.log(`  a timestamped .bak copy is written first.`);
    io.log('');

    const confirmed = flags.yes || (prompter !== null && (await prompter.confirm(`Add ${preview.added.length} entr${preview.added.length === 1 ? 'y' : 'ies'} to the config?`)));
    if (!confirmed) {
      io.log('Not written. Re-run with --yes, or paste the snippet above.');
      return 0;
    }

    const written = await updateConfigFile(configFile, entries);
    if (!written || !written.changed) {
      io.log('Nothing written.');
      return 0;
    }
    io.log(`Backup: ${written.backup}`);
    io.log(`Wrote ${written.added.length} entr${written.added.length === 1 ? 'y' : 'ies'} to ${configFile}.`);
    io.log('Next: npm run build   (remote repos are mirror-cloned into .frznforge-cache/ on the first run)');
    return 0;
  } finally {
    prompter?.close();
  }
}

/* ------------------------------------------------------------------ entry point */

export function defaultIo(): Io {
  return {
    log: (line) => console.log(line),
    error: (line) => console.error(line),
    isTty: Boolean(process.stdin.isTTY),
    env: process.env,
    cwd: process.cwd(),
  };
}

/** Dispatch. Returns the process exit code instead of calling `process.exit` (testable). */
export async function main(argv: string[], io: Io = defaultIo()): Promise<number> {
  const { command, flags, errors } = parseArgs(argv);
  if (flags.help || command === 'help') {
    io.log(USAGE);
    return 0;
  }
  if (errors.length > 0) {
    for (const e of errors) io.error(`error: ${e}`);
    io.error('');
    io.error(USAGE);
    return 1;
  }
  if (command === null) {
    io.error(USAGE);
    return 1;
  }
  if (command !== 'init') {
    io.error(`error: unknown command "${command}"`);
    io.error('');
    io.error(USAGE);
    return 1;
  }
  try {
    return await runInit(flags, io);
  } catch (error) {
    if (error instanceof CliError) {
      io.error(`error: ${error.message}`);
      return 1;
    }
    if (error instanceof InputClosedError) {
      io.error('error: input ended — nothing was written.');
      return 1;
    }
    throw error;
  }
}

const invokedDirectly =
  typeof process.argv[1] === 'string' && pathToFileURL(process.argv[1]).href === import.meta.url;
if (invokedDirectly) {
  process.exitCode = await main(process.argv.slice(2));
}
