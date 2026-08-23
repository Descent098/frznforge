// Minimal static file server for the built fixture site (dist/). No deps.
import http from 'node:http';
import fs from 'node:fs';
import path from 'node:path';

const [, , rootArg, portArg] = process.argv;
const root = path.resolve(rootArg ?? 'dist');
const port = Number(portArg ?? 4399);
const types = { '.html': 'text/html; charset=utf-8', '.js': 'text/javascript', '.css': 'text/css', '.json': 'application/json', '.svg': 'image/svg+xml', '.png': 'image/png', '.ico': 'image/x-icon', '.txt': 'text/plain' };

http
  .createServer((req, res) => {
    const url = new URL(req.url ?? '/', 'http://x');
    let p = decodeURIComponent(url.pathname);
    let file = path.join(root, p);
    if (file.endsWith(path.sep) || !path.extname(file)) file = path.join(file, 'index.html');
    if (!file.startsWith(root)) { res.writeHead(403); return res.end(); }
    fs.readFile(file, (err, buf) => {
      if (err) {
        fs.readFile(path.join(root, '404.html'), (e2, nf) => {
          res.writeHead(404, { 'content-type': 'text/html; charset=utf-8' });
          res.end(e2 ? 'not found' : nf);
        });
        return;
      }
      res.writeHead(200, { 'content-type': types[path.extname(file)] ?? 'application/octet-stream' });
      res.end(buf);
    });
  })
  .listen(port, () => console.log(`serving ${root} on http://localhost:${port}`));
