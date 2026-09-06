/** Dump allRoutes() for a fixture artifact, at two deploy bases, as the Go port's golden. */
import fs from 'node:fs';
import { parseForgeData } from '../src/lib/data/schema';
import { allRoutes } from '../src/lib/routes';
import { setSiteBase } from '../src/lib/base';

const raw = fs.readFileSync(process.argv[2], 'utf8');
const data = parseForgeData(JSON.parse(raw));
const out: Record<string, string[]> = {};
for (const base of ['', '/mysite']) {
  setSiteBase(base);
  out[base === '' ? 'root' : 'mysite'] = allRoutes(data);
}
fs.writeFileSync(process.argv[3], JSON.stringify(out, null, 2) + '\n');
console.log('root routes:', out.root.length, ' mysite routes:', out.mysite.length);
