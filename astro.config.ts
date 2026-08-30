import { defineConfig } from 'astro/config';
import svelte from '@astrojs/svelte';
import { loadConfig } from './src/lib/config/index';

// The one bridge between frznforge.config.ts and Astro: `site.base` (0.2.0) becomes
// Astro's `base`, which Vite then inlines as import.meta.env.BASE_URL — the value
// src/lib/base.ts reads everywhere a URL is emitted. Defaults and the FRZNFORGE_BASE env
// override (used by the e2e sub-path build) are resolveConfig's business, not duplicated
// here.
const cfg = await loadConfig();

// https://astro.build/config
export default defineConfig({
  integrations: [svelte()],
  base: cfg.site.base ?? '/',
  // Render two pages concurrently: measured ~8% faster than the default 1 on the
  // self-build (≈19.4 s → ≈17.9 s), with 4 measurably worse — the pages' blob reads are
  // synchronous, so there is little I/O to overlap. Numbers and method in
  // docs/dev/performance.md § "Measured: astro render concurrency".
  build: { concurrency: 2 },
});
