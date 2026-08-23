/**
 * Static raw-file endpoint: the exact bytes of every STORED blob, at
 * /repos/<slug>/raw/<ref-slug>/<path>. Content type from a small extension map
 * (the static file host ultimately decides at serve time; this covers `astro preview`
 * and any host that honours the built response).
 */
import type { APIRoute } from 'astro';
import { rawRoutes } from '../../../../../lib/routes';
import { readBlobBuffer } from '../../../../../lib/data/load';
import { getConfig, getData } from '../../../../../lib/site';

const MIME: Record<string, string> = {
  png: 'image/png',
  jpg: 'image/jpeg',
  jpeg: 'image/jpeg',
  gif: 'image/gif',
  webp: 'image/webp',
  svg: 'image/svg+xml',
  ico: 'image/x-icon',
  json: 'application/json',
  pdf: 'application/pdf',
  zip: 'application/zip',
  wasm: 'application/wasm',
  html: 'text/html; charset=utf-8',
  htm: 'text/html; charset=utf-8',
  css: 'text/css; charset=utf-8',
  js: 'text/javascript; charset=utf-8',
  mjs: 'text/javascript; charset=utf-8',
  woff: 'font/woff',
  woff2: 'font/woff2',
  ttf: 'font/ttf',
  mp3: 'audio/mpeg',
  mp4: 'video/mp4',
  webm: 'video/webm',
};

function mimeFor(path: string): string {
  const dot = path.lastIndexOf('.');
  const ext = dot === -1 ? '' : path.slice(dot + 1).toLowerCase();
  return MIME[ext] ?? 'text/plain; charset=utf-8';
}

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
