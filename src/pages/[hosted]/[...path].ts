/**
 * Hosted static sites (schema v7): every servable file of every `hosting.sites` entry,
 * emitted at its literal path under `/<slug>/…` — the raw-endpoint pattern (bytes from the
 * blob store, content type from `src/lib/mime.ts`), enumerated through `hostedFiles()` so
 * the exhaustive-route sync contract keeps holding. `index.html` files land where a static
 * host's directory-index resolution finds them, so `/<slug>/` just works.
 *
 * A top-level dynamic segment can never shadow the forge's own pages — Astro gives static
 * segments priority, and the reserved-slug config error refuses those names anyway. The
 * user-owned `public/` directory is checked at INGEST (`resolveHosting`), where the config
 * root is real — inside this built bundle, path resolution points at the output tree, not
 * the project.
 */
import type { APIRoute } from 'astro';
import { readBlobBuffer } from '../../lib/data/load';
import { mimeFor } from '../../lib/mime';
import { hostedFiles } from '../../lib/routes';
import { getConfig, getData } from '../../lib/site';

export async function getStaticPaths() {
  const data = await getData();
  return hostedFiles(data).map(({ site, path: filePath, sha }) => ({
    params: { hosted: site.slug, path: filePath },
    props: { sha, path: filePath },
  }));
}

export const GET: APIRoute = async ({ props }) => {
  const { sha, path: filePath } = props as { sha: string; path: string };
  const cfg = await getConfig();
  const bytes = readBlobBuffer(cfg.outDir, sha);
  if (!bytes) return new Response('not found', { status: 404 });
  return new Response(new Uint8Array(bytes), {
    headers: { 'content-type': mimeFor(filePath) },
  });
};
