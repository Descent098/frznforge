/**
 * Temp git repositories for tests, with deterministic author/committer dates so that
 * shas and snapshots are stable across runs and machines.
 */
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

// Isolate from the developer's global git config (signing, hooks, templates, …).
const globalConfig = path.join(fs.mkdtempSync(path.join(os.tmpdir(), 'frznforge-gitconfig-')), 'gitconfig');
fs.writeFileSync(globalConfig, '');
process.env.GIT_CONFIG_GLOBAL = globalConfig;
process.env.GIT_CONFIG_NOSYSTEM = '1';
process.env.GIT_TERMINAL_PROMPT = '0';

export const EPOCH = Date.UTC(2024, 0, 1, 0, 0, 0);

/** ISO date `n` seconds after 2024-01-01T00:00:00Z. */
export function at(n: number): string {
  return new Date(EPOCH + n * 1000).toISOString().replace(/\.\d{3}Z$/, 'Z');
}

export interface CommitOptions {
  date?: string;
  /**
   * Committer date, when it must differ from `date` (the author date).
   *
   * Defaults to `date`, which is what a plain `git commit` produces. Set it to model a
   * rebase or a cherry-pick: an old patch replayed onto a new tip keeps its author date and
   * gets a fresh committer date.
   */
  committerDate?: string;
  author?: { name: string; email: string };
  /** Set to true to write a multi-line message verbatim (subject + body). */
  raw?: boolean;
}

export class FixtureRepo {
  readonly dir: string;
  private seq = 0;

  private constructor(dir: string) {
    this.dir = dir;
  }

  static create(name = 'repo', branch = 'main'): FixtureRepo {
    const parent = fs.mkdtempSync(path.join(os.tmpdir(), 'frznforge-fixture-'));
    const dir = path.join(parent, name);
    fs.mkdirSync(dir);
    const r = new FixtureRepo(dir);
    r.git('init', '-q', '-b', branch);
    r.git('config', 'user.name', 'Test User');
    r.git('config', 'user.email', 'test@example.com');
    r.git('config', 'commit.gpgsign', 'false');
    r.git('config', 'tag.gpgsign', 'false');
    r.git('config', 'core.autocrlf', 'false');
    return r;
  }

  /** A bare repository (no work tree). */
  static createBare(name = 'repo.git', branch = 'main'): FixtureRepo {
    const parent = fs.mkdtempSync(path.join(os.tmpdir(), 'frznforge-fixture-'));
    const dir = path.join(parent, name);
    fs.mkdirSync(dir);
    const r = new FixtureRepo(dir);
    r.git('init', '-q', '--bare', '-b', branch);
    return r;
  }

  /** Run git in the fixture; returns trimmed stdout. */
  git(...args: string[]): string {
    return this.gitWith({}, ...args);
  }

  gitWith(env: Record<string, string>, ...args: string[]): string {
    return execFileSync('git', ['-C', this.dir, ...args], {
      env: { ...process.env, GIT_TERMINAL_PROMPT: '0', ...env },
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'pipe'],
    }).trim();
  }

  /** Write files into the work tree (no git operations). */
  write(files: Record<string, string | Buffer>): void {
    for (const [rel, content] of Object.entries(files)) {
      const abs = path.join(this.dir, rel);
      fs.mkdirSync(path.dirname(abs), { recursive: true });
      fs.writeFileSync(abs, content);
    }
  }

  /** Stage paths (or everything). */
  add(...paths: string[]): void {
    this.git('add', '-A', ...(paths.length ? ['--', ...paths] : []));
  }

  /** Remove tracked paths. */
  rm(...paths: string[]): void {
    this.git('rm', '-r', '-q', '--', ...paths);
  }

  /** Commit whatever is staged with deterministic dates; returns the sha. */
  commit(message: string, opts: CommitOptions = {}): string {
    const date = opts.date ?? at(this.seq++ * 60);
    const env: Record<string, string> = { GIT_AUTHOR_DATE: date, GIT_COMMITTER_DATE: opts.committerDate ?? date };
    if (opts.author) {
      env.GIT_AUTHOR_NAME = opts.author.name;
      env.GIT_AUTHOR_EMAIL = opts.author.email;
    }
    this.gitWith(env, 'commit', '-q', '--allow-empty', '--no-verify', '-m', message);
    return this.git('rev-parse', 'HEAD');
  }

  /** Write + stage + commit. */
  writeAndCommit(files: Record<string, string | Buffer>, message: string, opts: CommitOptions = {}): string {
    this.write(files);
    this.add();
    return this.commit(message, opts);
  }

  tag(name: string, opts: { annotated?: boolean; message?: string; date?: string } = {}): void {
    const date = opts.date ?? at(this.seq++ * 60);
    const env = { GIT_COMMITTER_DATE: date, GIT_AUTHOR_DATE: date };
    if (opts.annotated) this.gitWith(env, 'tag', '-a', name, '-m', opts.message ?? `tag ${name}`);
    else this.gitWith(env, 'tag', name);
  }

  branch(name: string, start?: string): void {
    this.git('branch', name, ...(start ? [start] : []));
  }

  checkout(name: string, create = false): void {
    this.git('checkout', '-q', ...(create ? ['-b'] : []), name);
  }

  head(): string {
    return this.git('rev-parse', 'HEAD');
  }

  rev(ref: string): string {
    return this.git('rev-parse', ref);
  }

  /** Point HEAD at a (possibly unborn) branch without touching the work tree. */
  setHead(branch: string): void {
    this.git('symbolic-ref', 'HEAD', `refs/heads/${branch}`);
  }

  cleanup(): void {
    fs.rmSync(path.dirname(this.dir), { recursive: true, force: true, maxRetries: 5 });
  }
}
