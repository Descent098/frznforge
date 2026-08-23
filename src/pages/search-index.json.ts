/**
 * Build-time search index for the command palette. Served as a static JSON file; the
 * palette fetches it lazily on first open.
 */
import type { APIRoute } from 'astro';
import { buildSearchIndex } from '../lib/search';
import { getData } from '../lib/site';

export const GET: APIRoute = async () => {
  const data = await getData();
  return new Response(JSON.stringify(buildSearchIndex(data)), {
    headers: { 'content-type': 'application/json; charset=utf-8' },
  });
};
