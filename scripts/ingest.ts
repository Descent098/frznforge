#!/usr/bin/env tsx
/**
 * `npm run ingest` — scan the repos in frznforge.config.ts and write the JSON artifact
 * (+ blob store) that `astro build` reads. Exit code is 0 even when warnings are emitted:
 * empty repos, empty trees, etc. are reported but never fail the build.
 * Exit code 1 only for hard failures (bad config, unwritable outDir, git missing).
 */
import { loadConfig } from '../src/lib/config/index';
import { ingest, writeArtifact } from '../src/lib/ingest';

const started = performance.now();
const config = await loadConfig();

console.log(`frznforge ingest → ${config.outDir}`);
if (config.repos.length === 0) {
  console.log('  (no repos configured — writing an empty artifact)');
}

const { data, blobs } = await ingest(config, {
  onRepoStart: (slug) => console.log(`  ▸ ${slug}`),
  onRepoDone: (repo) =>
    console.log(
      `    ✓ ${repo.slug}: ${repo.commitCount} commits, ${repo.branches.length} branches, ` +
        `${repo.gitTags.length} tags, ${Object.keys(repo.files).length} files` +
        (repo.empty ? ' (empty)' : ''),
    ),
});

await writeArtifact(data, blobs, config.outDir);

for (const w of data.warnings) {
  console.warn(`  ⚠ [${w.code}]${w.repo ? ` ${w.repo}:` : ''} ${w.message}`);
}

const ms = Math.round(performance.now() - started);
console.log(
  `done: ${data.repos.length} repo(s), ${blobs.size} blob(s), ${data.warnings.length} warning(s) in ${ms}ms`,
);
