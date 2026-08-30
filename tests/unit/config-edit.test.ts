/**
 * The wizard's config-edit engine (0.2.0). The contract under test is the textual-edit
 * rule inherited from `insertRepos`: ONLY the bytes of the field being changed move —
 * comments, formatting, and expression values in untouched fields survive byte-for-byte.
 */
import { describe, expect, it } from 'vitest';
import {
  insertIntoArray,
  matchBracket,
  quote,
  removeArrayItemAt,
  removeFromArray,
  renderValue,
  setObjectField,
} from '../../scripts/lib/config-edit';

const FIXTURE = `import { defineConfig } from './src/lib/config/schema';

// the whole site, hand-written, full of comments worth keeping
export default defineConfig({
  site: {
    title: 'My Forge', // shown in the sidebar
    url: 'https://example.com',
  },
  owner: { name: 'Kieran doesn\\'t', handle: 'kieran' },
  theme: { palette: 'hearth' },
  // site: { title: 'trap in a comment' },
  repos: [
    // local things live here
    { type: 'local', path: '../useful' },
    { type: 'github', owner: 'a', repo: 'b' },
  ],
  organizations: [
    { slug: 'cc', name: 'Canadian Coding', repos: ['useful'] },
  ],
  ingest: {
    maxBlobBytes: 512 * 1024, // half a meg, kept as an expression on purpose
    maxCommits: null,
  },
});
`;

describe('renderValue / quote', () => {
  it('renders primitives, strings, arrays and flat objects as config source', () => {
    expect(renderValue(42)).toBe('42');
    expect(renderValue(true)).toBe('true');
    expect(renderValue(null)).toBe('null');
    expect(renderValue("it's")).toBe("'it\\'s'");
    expect(renderValue(['a', 'b'])).toBe("['a', 'b']");
    expect(renderValue({ hot: 3, name: "o'brien" })).toBe("{ hot: 3, name: 'o\\'brien' }");
    expect(quote('a\nb')).toBe("'a\\nb'");
  });
});

describe('setObjectField', () => {
  it('replaces exactly the targeted value, trailing comment included', () => {
    const out = setObjectField(FIXTURE, ['site', 'title'], quote('New Name'))!;
    expect(out.changed).toBe(true);
    expect(out.text).toBe(FIXTURE.replace("'My Forge'", "'New Name'"));
  });

  it('replaces a null and leaves the expression field (and its comment) alone', () => {
    const out = setObjectField(FIXTURE, ['ingest', 'maxCommits'], '50')!;
    expect(out.text).toBe(FIXTURE.replace('maxCommits: null', 'maxCommits: 50'));
    expect(out.text).toContain('512 * 1024, // half a meg');
  });

  it('flattens an expression only when that exact field is targeted', () => {
    const out = setObjectField(FIXTURE, ['ingest', 'maxBlobBytes'], '1024')!;
    expect(out.text).toBe(FIXTURE.replace('512 * 1024', '1024'));
    expect(out.text).toContain('1024, // half a meg'); // the comment survives the flatten
  });

  it('reports changed: false (and identical bytes) for a no-op', () => {
    const out = setObjectField(FIXTURE, ['theme', 'palette'], "'hearth'")!;
    expect(out.changed).toBe(false);
    expect(out.text).toBe(FIXTURE);
  });

  it('is not fooled by keys inside comments or strings', () => {
    // the fixture carries `// site: { title: 'trap in a comment' }` and a URL string
    const out = setObjectField(FIXTURE, ['site', 'title'], quote('Real')!)!;
    expect(out.text).toContain("title: 'Real', // shown in the sidebar");
    expect(out.text).toContain("// site: { title: 'trap in a comment' }");
  });

  it('creates a missing leaf inside an existing block', () => {
    const out = setObjectField(FIXTURE, ['theme', 'heat'], renderValue({ hot: 3, warm: 30, neutral: 180, cool: 365 }))!;
    expect(out.changed).toBe(true);
    expect(out.text).toContain('heat: { hot: 3, warm: 30, neutral: 180, cool: 365 }');
    // everything outside the theme block is untouched
    expect(out.text.split('theme:')[0]).toBe(FIXTURE.split('theme:')[0]);
    expect(out.text).toContain('512 * 1024, // half a meg');
  });

  it('creates a whole missing chain at the root', () => {
    const out = setObjectField(FIXTURE, ['markdown', 'mermaid'], 'false')!;
    expect(out.text).toContain('markdown: { mermaid: false }');
    // still one balanced defineConfig object
    const open = out.text.indexOf('{', out.text.indexOf('defineConfig'));
    expect(matchBracket(out.text, open)).toBeGreaterThan(open);
  });

  it('refuses files without a defineConfig root', () => {
    expect(setObjectField('module.exports = {}', ['site', 'title'], "'x'")).toBeNull();
  });

  it('ignores a defineConfig that only appears inside a comment or string', () => {
    const src = `import { defineConfig } from './schema';
// old: export default defineConfig({ site: { title: 'COMMENTED' } })
const example = "defineConfig({ site: { title: 'STRING' } })";
export default defineConfig({
  site: { title: 'Real' },
});
`;
    const out = setObjectField(src, ['site', 'title'], quote('Edited'))!;
    expect(out.text).toContain("title: 'Edited'");
    expect(out.text).toContain("title: 'COMMENTED'"); // the comment is untouched
    expect(out.text).toContain("title: 'STRING'"); // the string is untouched
  });
});

describe('removeArrayItemAt', () => {
  const HOSTING = `import { defineConfig } from './schema';
export default defineConfig({
  hosting: {
    sites: [
      { repo: 'docs' },
      { repo: 'docs', slug: 'documentation', branch: 'docs' },
    ],
  },
});
`;

  it('removes exactly the element at the index, not every subset-matching sibling', () => {
    const out = removeArrayItemAt(HOSTING, ['hosting', 'sites'], 0, { repo: 'docs' })!;
    expect(out.removed).toBe(1);
    expect(out.text).not.toContain('{ repo: \'docs\' },');
    expect(out.text).toContain("{ repo: 'docs', slug: 'documentation', branch: 'docs' },");
  });

  it('removes the second row when that is the one pointed at', () => {
    const out = removeArrayItemAt(HOSTING, ['hosting', 'sites'], 1, { repo: 'docs', slug: 'documentation' })!;
    expect(out.removed).toBe(1);
    expect(out.text).toContain('{ repo: \'docs\' },');
    expect(out.text).not.toContain('documentation');
  });

  it('refuses (null) when the element at the index does not carry the expected fields', () => {
    // A stale index/expect (the array changed under the caller) must never delete the wrong row.
    expect(removeArrayItemAt(HOSTING, ['hosting', 'sites'], 0, { repo: 'other' })).toBeNull();
    expect(removeArrayItemAt(HOSTING, ['hosting', 'sites'], 9, {})).toBeNull();
  });
});

describe('insertIntoArray', () => {
  it('appends to an existing array, leaving present items byte-identical', () => {
    const item = renderValue({ slug: 'new-org', name: 'New Org' });
    const out = insertIntoArray(FIXTURE, ['organizations'], item)!;
    expect(out.changed).toBe(true);
    expect(out.text).toContain("{ slug: 'cc', name: 'Canadian Coding', repos: ['useful'] },");
    expect(out.text).toContain("{ slug: 'new-org', name: 'New Org' },");
    expect(out.text).toContain('// local things live here'); // repos array untouched
  });

  it('creates a missing array (and its parent chain)', () => {
    const item = renderValue({ repo: 'my-site', branch: 'gh-pages' });
    const out = insertIntoArray(FIXTURE, ['hosting', 'sites'], item)!;
    expect(out.text).toContain("hosting: { sites: [{ repo: 'my-site', branch: 'gh-pages' }] }");
  });
});

describe('removeFromArray', () => {
  it('removes exactly the matching item, keeping its neighbours (comments included)', () => {
    const out = removeFromArray(FIXTURE, ['repos'], { type: 'github', owner: 'a', repo: 'b' })!;
    expect(out.removed).toBe(1);
    expect(out.text).toContain('// local things live here');
    expect(out.text).toContain("{ type: 'local', path: '../useful' },");
    expect(out.text).not.toContain("owner: 'a'");
    // nothing outside the repos array moved
    expect(out.text.split('repos:')[0]).toBe(FIXTURE.split('repos:')[0]);
    expect(out.text).toContain('512 * 1024, // half a meg');
  });

  it('empties an array cleanly when the sole item matches', () => {
    const out = removeFromArray(FIXTURE, ['organizations'], { slug: 'cc' })!;
    expect(out.removed).toBe(1);
    expect(out.text).toContain('organizations: [],');
  });

  it('is a no-op (identical bytes) when nothing matches', () => {
    const out = removeFromArray(FIXTURE, ['repos'], { type: 'github', owner: 'zzz' })!;
    expect(out.removed).toBe(0);
    expect(out.changed).toBe(false);
    expect(out.text).toBe(FIXTURE);
  });

  it('a match must satisfy EVERY field', () => {
    const out = removeFromArray(FIXTURE, ['repos'], { type: 'github', owner: 'a', repo: 'wrong' })!;
    expect(out.removed).toBe(0);
  });
});

describe('composition', () => {
  it('several edits stay balanced and each touches only its own field', () => {
    let text = FIXTURE;
    text = setObjectField(text, ['site', 'title'], quote('Composed'))!.text;
    text = setObjectField(text, ['theme', 'heat'], renderValue({ hot: 2, warm: 30, neutral: 180, cool: 365 }))!.text;
    text = insertIntoArray(text, ['organizations'], renderValue({ slug: 'x', name: 'X' }))!.text;
    text = removeFromArray(text, ['repos'], { type: 'github', owner: 'a', repo: 'b' })!.text;
    expect(text).toContain("title: 'Composed', // shown in the sidebar");
    expect(text).toContain('hot: 2');
    expect(text).toContain("{ slug: 'x', name: 'X' },");
    expect(text).toContain('512 * 1024, // half a meg');
    const open = text.indexOf('{', text.indexOf('defineConfig'));
    const close = matchBracket(text, open);
    expect(close).toBeGreaterThan(open);
    expect(text.slice(close)).toContain('});');
  });
});
