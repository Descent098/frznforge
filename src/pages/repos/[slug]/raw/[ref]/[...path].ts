/**
 * Static raw-file endpoint: the exact bytes of every STORED blob, at
 * /repos/<slug>/raw/<ref-slug>/<path>. Content type comes from `src/lib/mime.ts`, shared with
 * the notes raw endpoint (the static file host ultimately decides at serve time; this covers
 * `astro preview` and any host that honours the built response).
 */
import type { APIRoute } from 'astro';
import { rawRoutes } from '../../../../../lib/routes';
import { readBlobBuffer } from '../../../../../lib/data/load';
import { getConfig, getData } from '../../../../../lib/site';
import { mimeFor } from '../../../../../lib/mime';

export async function getStaticPaths() {
  const data = await getData();
  return data.repos.flatMap((repo) =>
    rawRoutes(repo).map(({ ref, entry }) => ({
      params: { slug: repo.slug, ref: ref.slugged, path: entry.path },
      props: { sha: ref.files[entry.path]!.sha, path: entry.path },
    })),
  );
}

export const GET: APIRoute = async ({ props }) => {
  const { sha, path } = props as { sha: string; path: string };
  const cfg = await getConfig();
  const bytes = readBlobBuffer(cfg.outDir, sha);
  if (!bytes) return new Response('not found', { status: 404 });
  return new Response(new Uint8Array(bytes), {
    headers: { 'content-type': mimeFor(path) },
  });
};
