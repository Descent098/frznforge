/**
 * Reference scanner for the Go port's parity tests: runs the TypeScript `scanRepo` over a
 * repository and prints the resulting `Repo` as JSON on stdout.
 *
 * Invoked as `npx tsx internal/ingest/testdata/ts-scan.ts <request.json>` from the project
 * root. The request file holds the two `scanRepo` arguments plus the two things that cannot
 * survive JSON: `hostedRequests` entries are `null` where the TypeScript wants `undefined`,
 * and `contributors` is a plain array that is indexed here rather than a Map.
 *
 * Blob and archive bytes are summarised rather than printed — the Go side compares those
 * separately, and a JSON dump of every blob would dwarf the record under test.
 *
 * This exists only for tests. Nothing in the shipped site imports it.
 */
import fs from 'node:fs';
import type { ContributorConfig } from '../../../src/lib/config/schema';
import { contributorIndex } from '../../../src/lib/ingest/contributors';
import { scanRepo, type ScanOptions, type ScanSource } from '../../../src/lib/ingest/scan';

const requestPath = process.argv[2];
if (!requestPath) {
  console.error('usage: tsx ts-scan.ts <request.json>');
  process.exit(2);
}

const request = JSON.parse(fs.readFileSync(requestPath, 'utf8')) as {
  source: ScanSource & { hostedRequests?: Array<string | null> };
  options: Omit<ScanOptions, 'contributors'>;
  contributors?: ContributorConfig[];
};

const source: ScanSource = {
  ...request.source,
  hostedRequests: request.source.hostedRequests?.map((r) => (r === null ? undefined : r)),
};
const options: ScanOptions = {
  ...request.options,
  contributors: contributorIndex(request.contributors ?? []),
};

const result = await scanRepo(source, options);
if ('skipped' in result) {
  process.stdout.write(JSON.stringify({ skipped: true, warning: result.warning }, null, 2) + '\n');
} else {
  process.stdout.write(
    JSON.stringify(
      {
        repo: result.repo,
        blobs: Object.fromEntries(
          Array.from(result.blobs.entries())
            .sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
            .map(([sha, buf]) => [sha, buf.length]),
        ),
        archives: result.archives.map((a) => ({ file: a.file, bytes: a.data.length })),
      },
      null,
      2,
    ) + '\n',
  );
}
