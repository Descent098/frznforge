/**
 * Thin wrapper around the `git` CLI (no npm deps). Every call is `git -C <repo> …` with a
 * non-interactive environment. Only plumbing that reads *committed* objects is used by the
 * extractors — the working tree and the index are never read.
 */
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';

export class GitError extends Error {
  constructor(
    public readonly args: string[],
    public readonly code: number | null,
    public readonly stderr: string,
  ) {
    super(`git ${args.join(' ')} failed (exit ${code}): ${stderr.trim()}`);
    this.name = 'GitError';
  }
}

export interface GitRunOptions {
  /** Data written to git's stdin (e.g. for `cat-file --batch`, `log --stdin`). */
  input?: Buffer | string;
  /** Resolve with stdout even on a non-zero exit; caller inspects `code`. */
  allowFailure?: boolean;
  /** Stop reading stdout after this many bytes and kill the process. */
  maxBytes?: number;
}

export interface GitRunResult {
  stdout: Buffer;
  stderr: string;
  code: number | null;
}

function gitEnv(): NodeJS.ProcessEnv {
  return {
    ...process.env,
    GIT_TERMINAL_PROMPT: '0',
    GIT_CONFIG_NOSYSTEM: '1',
    GIT_OPTIONAL_LOCKS: '0',
    LC_ALL: 'C',
    LANG: 'C',
    GIT_PAGER: 'cat',
  };
}

/** Run git with the given args inside `repo`. Resolves stdout as a Buffer. */
export function gitRaw(repo: string, args: string[], opts: GitRunOptions = {}): Promise<GitRunResult> {
  return new Promise((resolve, reject) => {
    const fullArgs = ['-C', repo, ...args];
    const child = spawn('git', fullArgs, {
      env: gitEnv(),
      stdio: ['pipe', 'pipe', 'pipe'],
      windowsHide: true,
    });
    const out: Buffer[] = [];
    const err: Buffer[] = [];
    let outBytes = 0;
    let truncated = false;
    let settled = false;

    child.stdout.on('data', (chunk: Buffer) => {
      if (truncated) return;
      out.push(chunk);
      outBytes += chunk.length;
      if (opts.maxBytes !== undefined && outBytes >= opts.maxBytes) {
        truncated = true;
        child.stdout.destroy();
        child.kill();
      }
    });
    child.stderr.on('data', (chunk: Buffer) => err.push(chunk));
    const fail = (e: Error) => {
      if (settled) return;
      settled = true;
      reject(new Error(`failed to run git (is it installed and on PATH?): ${e.message}`));
    };
    // An 'error' on a stdio stream with no listener is an uncaught exception in Node, which
    // would kill ingest instead of rejecting this promise. `stdout` is destroyed deliberately
    // by the maxBytes path above, so that case must not be reported as a failure.
    child.stdout.on('error', (e: Error) => {
      if (!truncated) fail(e);
    });
    child.stderr.on('error', fail);
    child.on('error', fail);
    child.on('close', (code) => {
      if (settled) return;
      settled = true;
      let stdout = Buffer.concat(out);
      if (opts.maxBytes !== undefined && stdout.length > opts.maxBytes) stdout = stdout.subarray(0, opts.maxBytes);
      const stderr = Buffer.concat(err).toString('utf8');
      if (truncated || code === 0 || opts.allowFailure) resolve({ stdout, stderr, code: truncated ? 0 : code });
      else reject(new GitError(fullArgs, code, stderr));
    });
    if (opts.input !== undefined) {
      child.stdin.on('error', () => {
        /* EPIPE when git exits early — ignore, the close handler reports the real error */
      });
      child.stdin.end(opts.input);
    } else {
      child.stdin.end();
    }
  });
}

/** Run git and return stdout as a UTF-8 string (throws GitError on failure). */
export async function git(repo: string, args: string[], opts: GitRunOptions = {}): Promise<string> {
  const r = await gitRaw(repo, args, opts);
  return r.stdout.toString('utf8');
}

/** Run git and return stdout as a Buffer (throws GitError on failure). */
export async function gitBuffer(repo: string, args: string[], opts: GitRunOptions = {}): Promise<Buffer> {
  const r = await gitRaw(repo, args, opts);
  return r.stdout;
}

/** Run git; `null` when it exits non-zero (for "does this exist?" style queries). */
export async function gitMaybe(repo: string, args: string[]): Promise<string | null> {
  const r = await gitRaw(repo, args, { allowFailure: true });
  return r.code === 0 ? r.stdout.toString('utf8') : null;
}

function sameDir(a: string, b: string): boolean {
  const norm = (p: string) => {
    let r: string;
    try {
      r = fs.realpathSync.native(p);
    } catch {
      r = path.resolve(p);
    }
    r = r.replace(/\\/g, '/').replace(/\/+$/, '');
    return process.platform === 'win32' ? r.toLowerCase() : r;
  };
  return norm(a) === norm(b);
}

/**
 * True when `absPath` is the top level of a git work tree or a bare repository. A path
 * *inside* a work tree (or a non-directory) is not considered a repo.
 */
export async function isGitRepo(absPath: string): Promise<boolean> {
  let st: fs.Stats;
  try {
    st = fs.statSync(absPath);
  } catch {
    return false;
  }
  if (!st.isDirectory()) return false;
  const r = await gitRaw(absPath, ['rev-parse', '--is-bare-repository'], { allowFailure: true });
  if (r.code !== 0) return false;
  if (r.stdout.toString('utf8').trim() === 'true') return true;
  const top = await gitMaybe(absPath, ['rev-parse', '--show-toplevel']);
  if (top === null) return false;
  return sameDir(top.trim(), absPath);
}

/** Split NUL-separated output into non-empty records. */
export function splitNul(buf: Buffer | string): string[] {
  const s = typeof buf === 'string' ? buf : buf.toString('utf8');
  return s.split('\0').filter((x) => x.length > 0);
}

/** Parse an ISO-8601 date with offset (git `%aI` / `iso-strict`) into "YYYY-MM-DDTHH:MM:SSZ". */
export function toIsoUtc(value: string): string {
  const t = value.trim();
  const ms = Date.parse(t);
  if (Number.isNaN(ms)) throw new Error(`unparseable git date: ${JSON.stringify(value)}`);
  return new Date(ms).toISOString().replace(/\.\d{3}Z$/, 'Z');
}

/** `<x@y>` → `x@y` (git `%(taggeremail)` style). */
export function stripAngleBrackets(email: string): string {
  const t = email.trim();
  return t.startsWith('<') && t.endsWith('>') ? t.slice(1, -1) : t;
}

/**
 * Read several blobs at once via `cat-file --batch`. Returns sha → content for every sha
 * that resolved to a blob; missing/non-blob shas are simply absent.
 */
export async function readBlobs(repo: string, shas: Iterable<string>): Promise<Map<string, Buffer>> {
  const list = Array.from(new Set(shas));
  const result = new Map<string, Buffer>();
  if (list.length === 0) return result;
  const out = await gitBuffer(repo, ['cat-file', '--batch'], { input: list.join('\n') + '\n' });
  let pos = 0;
  while (pos < out.length) {
    const nl = out.indexOf(0x0a, pos);
    if (nl === -1) break;
    const header = out.subarray(pos, nl).toString('utf8');
    pos = nl + 1;
    const parts = header.split(' ');
    if (parts.length < 3) continue; // "<sha> missing"
    const [sha, type, sizeStr] = parts as [string, string, string];
    const size = Number.parseInt(sizeStr, 10);
    if (!Number.isFinite(size)) continue;
    const content = out.subarray(pos, pos + size);
    pos += size + 1; // trailing LF after the object
    if (type === 'blob') result.set(sha, Buffer.from(content));
  }
  return result;
}

/** Read a single blob fully. */
export async function readBlob(repo: string, sha: string): Promise<Buffer> {
  return gitBuffer(repo, ['cat-file', 'blob', sha]);
}

/** Read at most `maxBytes` of a blob (for binary sniffing of oversized files). */
export async function readBlobPrefix(repo: string, sha: string, maxBytes: number): Promise<Buffer> {
  return gitBuffer(repo, ['cat-file', 'blob', sha], { maxBytes });
}

/** Whether the first 8000 bytes contain a NUL (git's own heuristic). */
export function looksBinary(buf: Buffer): boolean {
  const n = Math.min(buf.length, 8000);
  for (let i = 0; i < n; i++) if (buf[i] === 0) return true;
  return false;
}
