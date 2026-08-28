#!/usr/bin/env tsx
/**
 * `npm run ingest` — scan the repos in frznforge.config.ts and write the JSON artifact
 * (+ blob store) that `astro build` reads. Exit code is 0 even when warnings are emitted:
 * empty repos, empty trees, an unreachable forge, etc. are reported but never fail the
 * build. Exit code 1 only for hard failures (bad config, unwritable outDir, git missing).
 */
import { loadConfig } from '../src/lib/config/index';
import { ingest, parseIngestArgs, writeArtifact } from '../src/lib/ingest';

let args;
try {
  args = parseIngestArgs(process.argv.slice(2));
} catch (e) {
  console.error(`frznforge ingest: ${e instanceof Error ? e.message : String(e)}`);
  process.exit(1);
}

const started = performance.now();
const config = await loadConfig();
if (args.noCache) {
  // ingest() forces a full fetch and reads no provider/scan caches for this run; fresh
  // results are still recorded (under the real config's hash) for the next ordinary run.
  console.log('  --no-cache: fetching everything; provider/scan caches ignored for this run');
}

console.log(`frznforge ingest → ${config.outDir}`);
if (config.repos.length === 0) {
  console.log('  (no repos configured — writing an empty artifact)');
}

const remoteCount = config.repos.filter((r) => r.type !== 'local').length;
if (remoteCount > 0) {
  console.log(
    `  ${remoteCount} remote source(s) — cache ${config.cacheDir} ` +
      `(fetch: ${args.noCache ? 'always [--no-cache]' : config.ingest.fetch})`,
  );
}

const { data, blobs, archives, remotes } = await ingest(
  config,
  {
    onRepoStart: (slug) => console.log(`  ▸ ${slug}`),
    onRemote: ({ slug, provider, action }) => console.log(`    ⇄ ${slug} (${provider}: ${action})`),
    onRepoDone: (repo) =>
      console.log(
        `    ✓ ${repo.slug}: ${repo.commitCount} commits, ${repo.branches.length} branches, ` +
          `${repo.gitTags.length} tags, ${Object.keys(repo.files).length} files` +
          (repo.empty ? ' (empty)' : ''),
      ),
  },
  { noCache: args.noCache },
);

try {
  await writeArtifact(data, blobs, archives, config.outDir);
} catch (e) {
  // writeArtifact validates before it writes, so this is an ingest bug rather than a warning
  // — but a bare ZodError stack says nothing about which repo produced the bad value.
  console.error('frznforge ingest: the artifact failed validation and was not written.');
  console.error(`  repos in this run: ${data.repos.map((r) => r.slug).join(', ') || '(none)'}`);
  throw e;
}

for (const w of data.warnings) {
  console.warn(`  ⚠ [${w.code}]${w.repo ? ` ${w.repo}:` : ''} ${w.message}`);
}

// Remote trouble is a warning, never an error — say so plainly so a stale build is obvious.
const cached = remotes.filter((r) => r.action === 'cached');
const skipped = remotes.filter((r) => r.skipped);
if (cached.length > 0 || skipped.length > 0) {
  const parts = [
    cached.length > 0 ? `${cached.length} served from cache (${cached.map((r) => r.slug).join(', ')})` : null,
    skipped.length > 0 ? `${skipped.length} skipped (${skipped.map((r) => r.slug).join(', ')})` : null,
  ].filter(Boolean);
  console.log(`  ! remote sources: ${parts.join('; ')} — see the warnings above; the build continued.`);
}

const ms = Math.round(performance.now() - started);
// Notes and organizations are reported only when there are any: most sites configure neither,
// and a permanent "0 note(s), 0 organization(s)" would just be noise in every run.
const extras = [
  data.notes.length > 0 ? `${data.notes.length} note(s)` : null,
  data.organizations.length > 0 ? `${data.organizations.length} organization(s)` : null,
]
  .filter(Boolean)
  .join(', ');
console.log(
  `done: ${data.repos.length} repo(s), ${extras ? extras + ', ' : ''}${blobs.size} blob(s), ` +
    `${archives.size} archive(s), ${data.warnings.length} warning(s) in ${ms}ms`,
);
