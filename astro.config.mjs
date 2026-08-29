// @ts-check
import { defineConfig } from 'astro/config';

import svelte from '@astrojs/svelte';

// https://astro.build/config
export default defineConfig({
  integrations: [svelte()],
  // Render two pages concurrently: measured ~8% faster than the default 1 on the
  // self-build (≈19.4 s → ≈17.9 s), with 4 measurably worse — the pages' blob reads are
  // synchronous, so there is little I/O to overlap. Numbers and method in
  // docs/dev/performance.md § "Measured: astro render concurrency".
  build: { concurrency: 2 },
});