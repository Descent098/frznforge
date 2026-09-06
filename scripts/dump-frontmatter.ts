/** Dump parseFrontmatter/firstHeading/stripLeadingHeading results as the Go port's golden. */
import fs from 'node:fs';
import { firstHeading, parseFrontmatter, splitFrontmatter, stripLeadingHeading } from '../src/lib/frontmatter';

const CASES: Array<{ name: string; src: string; title?: string }> = [
  { name: 'none', src: '# Just a body\n\ntext' },
  { name: 'simple', src: '---\ntitle: Hello\ndate: 2026-01-02\n---\nbody' },
  { name: 'dots-terminator', src: '---\ntitle: Hello\n...\nbody' },
  { name: 'unterminated', src: '---\ntitle: Hello\nbody without close' },
  { name: 'flow-seq', src: '---\ntags: [a, b, "c, d"]\n---\nx' },
  { name: 'flow-seq-comment', src: '---\ntags: [a, b] # why\n---\nx' },
  { name: 'block-seq', src: '---\ntags:\n  - one\n  - two\n---\nx' },
  { name: 'block-seq-nested-map', src: '---\npeople:\n  - name: x\n  - name: y\n---\nx' },
  { name: 'block-seq-nested-flow', src: '---\nmatrix:\n  - [a, b]\n---\nx' },
  { name: 'nested-mapping', src: '---\nforges:\n  github: https://x\n---\nx' },
  { name: 'quoted-double', src: '---\ntitle: "A \\"quoted\\" thing\nnewline"\n---\nx' },
  { name: 'quoted-single', src: "---\ntitle: 'it''s here'\n---\nx" },
  { name: 'unterminated-quote', src: '---\ntitle: "never closed\n---\nx' },
  { name: 'plain-comment', src: '---\ntitle: Hello # a comment\n---\nx' },
  { name: 'hash-not-comment', src: '---\ntitle: C#-tips\n---\nx' },
  { name: 'comment-line', src: '---\n# just a comment\ntitle: Hello\n---\nx' },
  { name: 'flow-map', src: '---\nobj: {a: 1}\n---\nx' },
  { name: 'crlf', src: '---\r\ntitle: Hello\r\ntags: [a]\r\n---\r\nbody\r\nmore' },
  { name: 'bom', src: '\uFEFF---\ntitle: Hello\n---\nx' },
  { name: 'empty-value', src: '---\ntitle:\ndescription: set\n---\nx' },
  { name: 'dotted-key', src: '---\nsome.key: v\nother-key: w\n---\nx' },
  { name: 'blank-lines', src: '---\n\ntitle: Hello\n\ntags:\n\n  - a\n\n---\nx' },
];

const HEADING_CASES = [
  { name: 'atx', body: '# Title\n\ntext' },
  { name: 'atx-closed', body: '# Title ###\n\ntext' },
  { name: 'setext', body: 'Title\n=====\n\ntext' },
  { name: 'in-fence', body: '```\n# Not a heading\n```\n\n# Real\n' },
  { name: 'tilde-fence', body: '~~~\n# Not a heading\n~~~\n# Real\n' },
  { name: 'indented', body: '   # Indented heading\n' },
  { name: 'none', body: 'no headings here\n' },
  { name: 'h2-only', body: '## Second level\n' },
];

const STRIP_CASES = [
  { name: 'matching-atx', body: '# Title\n\nrest', title: 'Title' },
  { name: 'matching-setext', body: 'Title\n===\n\nrest', title: 'Title' },
  { name: 'different', body: '# Other\n\nrest', title: 'Title' },
  { name: 'no-heading', body: 'rest', title: 'Title' },
  { name: 'leading-blank', body: '\n\n# Title\nrest', title: 'Title' },
];

const out = {
  parse: CASES.map((c) => {
    const split = splitFrontmatter(c.src);
    const fm = parseFrontmatter(c.src);
    return { name: c.name, src: c.src, present: split.present, raw: split.raw, body: fm.body, data: fm.data };
  }),
  firstHeading: HEADING_CASES.map((c) => ({ name: c.name, body: c.body, out: firstHeading(c.body) })),
  stripLeadingHeading: STRIP_CASES.map((c) => ({ name: c.name, body: c.body, title: c.title, out: stripLeadingHeading(c.body, c.title) })),
};
fs.writeFileSync(process.argv[2], JSON.stringify(out, null, 2) + '\n');
console.log('parse cases:', out.parse.length, 'heading:', out.firstHeading.length, 'strip:', out.stripLeadingHeading.length);
