/**
 * Minimal static file server for the built fixture site (dist/). No dependencies beyond node.
 *
 * TypeScript, run through `tsx`, so it can share `src/lib/mime.ts` with the two raw endpoints
 * instead of keeping a third extension table. That sharing is not cosmetic: this server is what
 * the e2e raw-file assertions actually see, and while it had its own map the two disagreed —
 * a `.ps1` note file was served as `application/octet-stream` here and `text/plain` by the
 * site's own endpoint. One table means the harness cannot drift from production again.
 */
import http from 'node:http';
import fs from 'node:fs';
import path from 'node:path';
import { mimeFor } from '../../src/lib/mime';

const [, , rootArg, portArg, baseArg] = process.argv;
const root = path.resolve(rootArg ?? 'dist');
const port = Number(portArg ?? 4399);
/**
 * Optional mount prefix (0.2.0 base-path e2e): with `/mysite`, the dist is served ONLY
 * under that prefix and everything outside it 404s — exactly how a real sub-path deploy
 * behaves, which is what makes a root-absolute link leak fail loudly in the specs.
 */
const base = (baseArg ?? '').replace(/\/+$/, '');

http
  .createServer((req, res) => {
    const url = new URL(req.url ?? '/', 'http://x');
    let p = decodeURIComponent(url.pathname);
    if (base) {
      if (p !== base && !p.startsWith(`${base}/`)) {
        res.writeHead(404, { 'content-type': 'text/plain' });
        return res.end(`not under the deploy base ${base}`);
      }
      p = p.slice(base.length) || '/';
    }
    let file = path.join(root, p);
    if (file.endsWith(path.sep) || !path.extname(file)) file = path.join(file, 'index.html');
    if (!file.startsWith(root)) {
      res.writeHead(403);
      return res.end();
    }
    fs.readFile(file, (err, buf) => {
      if (err) {
        fs.readFile(path.join(root, '404.html'), (e2, nf) => {
          res.writeHead(404, { 'content-type': 'text/html; charset=utf-8' });
          res.end(e2 ? 'not found' : nf);
        });
        return;
      }
      res.writeHead(200, { 'content-type': mimeFor(file) });
      res.end(buf);
    });
  })
  .listen(port, () => console.log(`serving ${root} on http://localhost:${port}`));
