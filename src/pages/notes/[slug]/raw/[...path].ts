/**
 * Static raw-file endpoint for notes: the exact bytes of every STORED note file, at
 * /notes/<slug>/raw/<file-path>.
 *
 * Content type comes from `src/lib/mime.ts`, the same table the repo raw endpoint uses, so
 * the two can never disagree about how a given extension is served.
 */
import type { APIRoute } from 'astro';
import { noteRawRoutes } from '../../../../lib/routes';
import { readBlobBuffer } from '../../../../lib/data/load';
import { getConfig, getData } from '../../../../lib/site';
import { mimeFor } from '../../../../lib/mime';

export async function getStaticPaths() {
  const data = await getData();
  return noteRawRoutes(data).map(({ note, file }) => ({
    params: { slug: note.slug, path: file.path },
    props: { sha: file.sha, path: file.path },
  }));
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
