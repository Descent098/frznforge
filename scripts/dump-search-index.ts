/** Dump buildSearchIndex() for a fixture artifact, as the Go emitter's golden. */
import fs from 'node:fs';
import { parseForgeData } from '../src/lib/data/schema';
import { buildSearchIndex } from '../src/lib/search';
import { setSiteBase } from '../src/lib/base';

const data = parseForgeData(JSON.parse(fs.readFileSync(process.argv[2], 'utf8')));
setSiteBase(process.argv[4] ?? '');
fs.writeFileSync(process.argv[3], JSON.stringify(buildSearchIndex(data)));
console.log('docs:', buildSearchIndex(data).docs.length);
