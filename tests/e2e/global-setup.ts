/**
 * Playwright global setup: build the site from a deterministic FIXTURE artifact.
 *  1. create three git repos under tests/.tmp/e2e/repos (content, template, empty)
 *  2. run the real ingest pipeline on them → tests/.tmp/e2e/data
 *  3. `astro build` with FRZNFORGE_OUT_DIR pointing at that data → tests/.tmp/e2e/dist
 * The webServer in playwright.config.ts then serves that dist.
 */
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { resolveConfig } from '../../src/lib/config/index';
import userConfig from '../../frznforge.config';
import { ingest, writeArtifact } from '../../src/lib/ingest';

const ROOT = path.resolve(import.meta.dirname, '..', '..');
const TMP = path.join(ROOT, 'tests', '.tmp', 'e2e');
const REPOS = path.join(TMP, 'repos');
const DATA = path.join(TMP, 'data');
const DIST = path.join(TMP, 'dist');

const gitEnv = (date: string) => ({
  ...process.env,
  GIT_AUTHOR_NAME: 'Fixture Author',
  GIT_AUTHOR_EMAIL: 'fixture@example.com',
  GIT_COMMITTER_NAME: 'Fixture Author',
  GIT_COMMITTER_EMAIL: 'fixture@example.com',
  GIT_AUTHOR_DATE: date,
  GIT_COMMITTER_DATE: date,
  GIT_CONFIG_GLOBAL: path.join(TMP, 'gitconfig-empty'),
  GIT_CONFIG_NOSYSTEM: '1',
});

function git(cwd: string, args: string[], date = '2024-01-01T00:00:00Z') {
  return execFileSync('git', args, { cwd, env: gitEnv(date), stdio: 'pipe' }).toString();
}

function commitAll(cwd: string, message: string, date: string) {
  git(cwd, ['add', '-A'], date);
  git(cwd, ['commit', '-q', '-m', message], date);
}

function makeRepo(name: string, init: (dir: string) => void) {
  const dir = path.join(REPOS, name);
  fs.mkdirSync(dir, { recursive: true });
  git(dir, ['init', '-q', '-b', 'main']);
  init(dir);
  return dir;
}

export default async function globalSetup() {
  fs.rmSync(TMP, { recursive: true, force: true });
  fs.mkdirSync(TMP, { recursive: true });
  fs.writeFileSync(path.join(TMP, 'gitconfig-empty'), '');

  // alpha — normal repo with README, .frznforge.json, tags, two languages, recent-ish date
  const alpha = makeRepo('alpha', (d) => {
    fs.writeFileSync(path.join(d, 'README.md'), '# Alpha\n\nA **fixture** repo for e2e tests.\n\n- bullet one\n- bullet two\n');
    fs.writeFileSync(path.join(d, '.frznforge.json'), JSON.stringify({ description: 'Alpha fixture: a static site generator.', tags: ['ssg', 'astro'], links: { homepage: 'https://example.com/alpha', upstream: 'https://github.com/example/alpha' } }, null, 2));
    fs.writeFileSync(path.join(d, 'LICENSE'), 'MIT License\n\nCopyright (c) 2024 Fixture\n\nPermission is hereby granted, free of charge, to any person obtaining a copy...');
    fs.mkdirSync(path.join(d, 'src'));
    fs.writeFileSync(path.join(d, 'src', 'index.ts'), 'export const answer: number = 42;\n'.repeat(20));
    fs.writeFileSync(path.join(d, 'src', 'style.css'), 'body { margin: 0; }\n'.repeat(5));
    commitAll(d, 'initial commit', '2024-01-01T00:00:00Z');
    fs.writeFileSync(path.join(d, 'src', 'index.ts'), 'export const answer: number = 43;\n'.repeat(20));
    commitAll(d, 'bump the answer', '2024-02-01T00:00:00Z');
    git(d, ['tag', '-a', 'v1.0.0', '-m', 'First release'], '2024-02-01T00:00:00Z');
  });

  // bravo — template repo, Go, tags
  makeRepo('bravo', (d) => {
    fs.writeFileSync(path.join(d, 'README.md'), '# Bravo template\n\nClone me.\n');
    fs.writeFileSync(path.join(d, '.frznforge.json'), JSON.stringify({ description: 'Bravo fixture: a Go CLI template.', tags: ['cli', 'go'], template: true }, null, 2));
    fs.writeFileSync(path.join(d, 'main.go'), 'package main\n\nfunc main() {}\n'.repeat(10));
    commitAll(d, 'scaffold', '2023-06-01T00:00:00Z');
  });

  // empty — no commits at all
  makeRepo('empty', () => {});

  // uncommitted noise in alpha: must NOT show up anywhere
  fs.writeFileSync(path.join(alpha, 'UNTRACKED-SECRET.txt'), 'should never be published');
  fs.writeFileSync(path.join(alpha, 'README.md'), '# MODIFIED BUT NOT COMMITTED\n');

  const cfg = resolveConfig(
    {
      ...userConfig,
      repos: [
        { type: 'local', path: path.join(REPOS, 'alpha') },
        { type: 'local', path: path.join(REPOS, 'bravo') },
        { type: 'local', path: path.join(REPOS, 'empty') },
      ],
      ingest: { ...(userConfig.ingest ?? {}), outDir: DATA },
    },
    ROOT,
  );
  delete process.env.FRZNFORGE_OUT_DIR; // resolveConfig honours the env; be explicit here
  const { data, blobs } = await ingest({ ...cfg, outDir: DATA });
  await writeArtifact(data, blobs, DATA);

  execFileSync('npx', ['astro', 'build', '--outDir', DIST], {
    cwd: ROOT,
    stdio: 'pipe',
    shell: true,
    env: { ...process.env, FRZNFORGE_OUT_DIR: DATA },
  });
}
