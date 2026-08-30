/**
 * Child-process config loader for the web wizard (`scripts/lib/web-init.ts`).
 *
 * The wizard needs to re-read frznforge.config.ts after every write, and an in-process
 * `import()` cannot do that: tsx's loader caches modules by *path*, ignoring a query-string
 * cache-buster, so the second import answers with the pre-edit module. A fresh process has no
 * module cache — and because the file is imported at its real path, its own relative imports
 * (`./src/lib/config/schema` in a real project) resolve exactly as they do in a build.
 *
 * Prints the default export as JSON on stdout and exits 0; any failure (unresolvable import,
 * syntax error, unstringifiable export) goes to stderr with exit 1. Run under tsx so a .ts
 * config imports cleanly.
 */
import { pathToFileURL } from 'node:url';

const target = process.argv[2];
if (!target) {
  process.stderr.write('usage: config-load <config-file>');
  process.exit(2);
}

/** Exit only once the write has actually flushed — `process.exit` would discard a pipe's
 *  buffered tail (a config whose JSON exceeds the ~64 KB kernel pipe buffer), so the parent
 *  would see truncated output and a spurious "config does not load" for a large config. */
function writeThenExit(stream: NodeJS.WriteStream, text: string, code: number): void {
  stream.write(text, () => process.exit(code));
}

import(pathToFileURL(target).href)
  .then((mod) => {
    const value = (mod as { default?: unknown }).default;
    // JSON.stringify itself can refuse (a circular structure, a BigInt) — that is a config
    // the wizard cannot edit, said the same way as any other load failure.
    writeThenExit(process.stdout, JSON.stringify(value === undefined ? null : value), 0);
  })
  .catch((error: unknown) => {
    writeThenExit(process.stderr, error instanceof Error ? error.message : String(error), 1);
  });
