import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { defineConfig } from 'astro/config';
import type { AstroIntegration } from 'astro';
import type { Plugin as VitePlugin } from 'vite';
import { loadConfig } from './src/lib/config/index';

// The one bridge between frznforge.config.ts and Astro: `site.base` (0.2.0) becomes
// Astro's `base`, which Vite then inlines as import.meta.env.BASE_URL — the value
// src/lib/base.ts reads everywhere a URL is emitted. Defaults and the FRZNFORGE_BASE env
// override (used by the e2e sub-path build) are resolveConfig's business, not duplicated
// here.
const cfg = await loadConfig();

const ROOT = path.dirname(fileURLToPath(import.meta.url));
const WEB_DIR = path.join(ROOT, 'web');

/**
 * Serve and ship `web/` verbatim.
 *
 * `web/` is the no-build UI root (0.4.0): plain ES modules and, later, vendored assets that
 * the browser loads exactly as they are written on disk — no transpile, no bundle, no hash.
 * Astro has one `publicDir` and it is already `public/`, which belongs to the *site owner*
 * (avatars, images) rather than to the engine, so the two are kept apart and this integration
 * copies the engine's half into the output.
 *
 * **Transitional.** In 0.4.0 Phase 4 the Go renderer does this copy itself and both halves of
 * this integration are deleted along with Astro. Nothing else should grow into it.
 */
function webAssets(): AstroIntegration {
  return {
    name: 'frznforge-web-assets',
    hooks: {
      'astro:config:setup': ({ updateConfig }) => {
        updateConfig({
          vite: {
            plugins: [
              {
                name: 'frznforge-web-assets-dev',
                // `astro dev` only knows about publicDir; without this, /js/* 404s under HMR
                // while working perfectly in a real build — the worst kind of difference.
                configureServer(server) {
                  server.middlewares.use((req, res, next) => {
                    const base = cfg.site.base ?? '';
                    const url = (req.url ?? '').split('?')[0] ?? '';
                    const rel = base && url.startsWith(base) ? url.slice(base.length) : url;
                    // Only paths that exist under web/ are claimed; everything else falls
                    // through to Astro untouched.
                    const file = path.join(WEB_DIR, decodeURIComponent(rel));
                    if (!file.startsWith(WEB_DIR) || !fs.existsSync(file) || !fs.statSync(file).isFile()) return next();
                    res.setHeader('Content-Type', file.endsWith('.js') ? 'text/javascript' : 'application/octet-stream');
                    res.end(fs.readFileSync(file));
                  });
                },
              } satisfies VitePlugin,
            ],
          },
        });
      },
      'astro:build:done': ({ dir }) => {
        if (!fs.existsSync(WEB_DIR)) return;
        fs.cpSync(WEB_DIR, fileURLToPath(dir), { recursive: true });
      },
    },
  };
}

// https://astro.build/config
export default defineConfig({
  integrations: [webAssets()],
  base: cfg.site.base ?? '/',
  // Render two pages concurrently: measured ~8% faster than the default 1 on the
  // self-build (≈19.4 s → ≈17.9 s), with 4 measurably worse — the pages' blob reads are
  // synchronous, so there is little I/O to overlap. Numbers and method in
  // docs/dev/performance.md § "Measured: astro render concurrency".
  build: { concurrency: 2 },
});
