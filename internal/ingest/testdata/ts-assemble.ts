/**
 * Reference implementation for the Go port's assembly parity tests: runs the TypeScript
 * `collectNotes` or `resolveOrganizations` over an input the Go side also runs, and prints the
 * result as JSON on stdout.
 *
 * Invoked as `npx tsx internal/ingest/testdata/ts-assemble.ts <request.json>` from the project
 * root, the same way `ts-scan.ts` is. The request carries a `mode` plus whatever that mode
 * needs; note blob CONTENT is summarised as byte lengths, because the Go side compares the
 * bytes it already holds and a JSON dump of every blob would dwarf the records under test.
 *
 * This exists only for tests. Nothing in the shipped site imports it.
 */
import fs from 'node:fs';
import os from 'node:os';
import { resolveConfig } from '../../../src/lib/config/index';
import type { FrznforgeConfigInput } from '../../../src/lib/config/schema';
import { collectNotes } from '../../../src/lib/ingest/notes';
import { resolveOrganizations, type OrgRepoInput } from '../../../src/lib/ingest/orgs';

const requestPath = process.argv[2];
if (!requestPath) {
  console.error('usage: tsx ts-assemble.ts <request.json>');
  process.exit(2);
}

const request = JSON.parse(fs.readFileSync(requestPath, 'utf8')) as {
  mode: 'notes' | 'orgs';
  /** Site config as written; `owner` is filled in when the request omits it. */
  config?: Partial<FrznforgeConfigInput>;
  /** Project root the config resolves against. Defaults to the OS temp directory. */
  root?: string;
  /** notes mode: the folder to read and the two per-call overrides. */
  dir?: string;
  maxFileBytes?: number;
  useMtime?: boolean;
  /** orgs mode: the repos, already in final-slug order. */
  repos?: Array<{ slug: string; org: string | null }>;
};

const config = resolveConfig(
  { owner: { name: 'Tester', handle: 'tester' }, ...(request.config ?? {}) },
  request.root ?? os.tmpdir(),
);

if (request.mode === 'notes') {
  const options: { dir?: string; maxFileBytes?: number; useMtime?: boolean } = {};
  if (request.dir !== undefined) options.dir = request.dir;
  if (request.maxFileBytes !== undefined) options.maxFileBytes = request.maxFileBytes;
  if (request.useMtime !== undefined) options.useMtime = request.useMtime;
  const { notes, blobs, warnings } = await collectNotes(config, options);
  process.stdout.write(
    JSON.stringify(
      {
        notes,
        warnings,
        blobs: Object.fromEntries(
          Array.from(blobs.entries())
            .sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
            .map(([sha, buf]) => [sha, buf.length]),
        ),
      },
      null,
      2,
    ) + '\n',
  );
} else {
  const repos: OrgRepoInput[] = request.repos ?? [];
  const { organizations, warnings } = resolveOrganizations(config, repos);
  process.stdout.write(JSON.stringify({ organizations, warnings }, null, 2) + '\n');
}
