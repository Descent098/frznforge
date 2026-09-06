/**
 * Reference importer for the Go port's parity tests: runs a TypeScript provider importer over
 * the recorded HTTP fixtures and prints its normalised answers as JSON on stdout.
 *
 * Invoked as `npx tsx internal/ingest/testdata/ts-import.ts <request.json>` from the project
 * root. The request file names the configured source, the fixture routes to serve, and whether
 * the releases endpoint should be called.
 *
 * No network: every response comes from `tests/fixtures/http` through `fixtureFetch`, which is
 * the same harness the TypeScript unit tests use, and the same recorded bytes the Go test feeds
 * its own client.
 *
 * This exists only for tests. Nothing in the shipped site imports it.
 */
import fs from 'node:fs';
import type { RepoSourceConfig } from '../../../src/lib/config/schema';
import { createImporter } from '../../../src/lib/importers/index';
import { fixtureFetch, loadFixture, type FixtureRoute } from '../../../tests/fixtures/http/index';

const requestPath = process.argv[2];
if (!requestPath) {
  console.error('usage: tsx ts-import.ts <request.json>');
  process.exit(2);
}

interface RouteRequest {
  /** Path under tests/fixtures/http, e.g. "github/repo.json". */
  fixture?: string;
  /** Literal JSON body, for the cases that are constructed rather than recorded. */
  body?: unknown;
  status?: number;
  headers?: Record<string, string>;
}

const request = JSON.parse(fs.readFileSync(requestPath, 'utf8')) as {
  source: RepoSourceConfig;
  /** URL substring → canned response, in declaration order (most specific first). */
  routes: Array<{ pattern: string } & RouteRequest>;
  wantReleases: boolean;
};

const routes: Record<string, FixtureRoute> = {};
for (const route of request.routes) {
  routes[route.pattern] = {
    ...(route.status !== undefined ? { status: route.status } : {}),
    ...(route.fixture !== undefined ? { json: loadFixture(route.fixture) } : {}),
    ...(route.body !== undefined ? { json: route.body } : {}),
    ...(route.headers !== undefined ? { headers: route.headers } : {}),
  };
}

// token: null explicitly — the importers must never reach for the ambient environment here, and
// a token would change the headers the fixtures are recorded against.
const importer = createImporter(request.source, { fetchImpl: fixtureFetch(routes), token: null });
if (!importer) {
  console.error(`no importer for source type ${request.source.type}`);
  process.exit(2);
}

const meta = await importer.fetchMeta();
const imported = request.wantReleases
  ? await importer.fetchReleases()
  : { releases: [], truncated: false };

process.stdout.write(
  JSON.stringify(
    { provider: importer.provider, meta, releases: imported.releases, truncated: imported.truncated },
    null,
    2,
  ),
);
