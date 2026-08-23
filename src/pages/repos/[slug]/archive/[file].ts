/**
 * Static archive endpoint: serves the source zips produced by ingest with `git archive`
 * (stored at `<outDir>/archives/<slug>/<ref-slug>.zip`) at /repos/<slug>/archive/<ref-slug>.zip.
 */
import fs from 'node:fs';
import path from 'node:path';
import type { APIRoute } from 'astro';
import { refSlug } from '../../../../lib/routes';
import { getConfig, getData } from '../../../../lib/site';

export async function getStaticPaths() {
  const data = await getData();
  return data.repos.flatMap((repo) =>
    repo.archives.map((a) => ({
      params: { slug: repo.slug, file: `${refSlug(a.ref)}.zip` },
      props: { file: a.file },
    })),
  );
}

export const GET: APIRoute = async ({ props }) => {
  const { file } = props as { file: string };
  const cfg = await getConfig();
  const abs = path.join(cfg.outDir, file);
  if (!fs.existsSync(abs)) return new Response('not found', { status: 404 });
  return new Response(new Uint8Array(fs.readFileSync(abs)), {
    headers: { 'content-type': 'application/zip' },
  });
};
