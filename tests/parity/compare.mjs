/**
 * Structural parity harness: does the Go renderer emit the same site as Astro?
 *
 * ## What it compares, and what it deliberately does not
 *
 * Not bytes. Astro's output carries its own runtime — the island loader, `astro-island`
 * wrappers, `_astro/` bundle links, scoped class hashes, a `<meta name="generator">` — none of
 * which the Go renderer emits and none of which it should. Comparing bytes would drown the real
 * differences in that noise.
 *
 * So it compares the DOM: element order and nesting, tag names, the attributes that carry
 * meaning, and normalized text. Attribute *order* and insignificant whitespace are not
 * differences. Byte identity stays where it is genuinely checkable — the artifact, and the Go
 * build's own reproducibility.
 *
 * ## Why a browser
 *
 * Because HTML parsing is the whole problem. A regex or a hand-rolled tokenizer would disagree
 * with a browser on exactly the malformed markup this is meant to catch, and the repository
 * already has Playwright for the e2e suite. Chromium's parser is the same one that will render
 * these pages, which makes "the same DOM" mean what a reader would mean by it.
 *
 * ## Usage
 *
 *   node tests/parity/compare.mjs <astro-dist> <go-dist> [--json=report.json] [--max=N]
 *
 * Exit code 0 when every compared page matches (modulo the declared exceptions), 1 otherwise.
 */
import fs from 'node:fs';
import path from 'node:path';
import { chromium } from '@playwright/test';

/* ---- the strip list ------------------------------------------------------ */

/**
 * Astro-only scaffolding, removed from the Astro side before comparing.
 *
 * Every entry is a decision, not a shrug: a strip rule is how a real difference hides. Each one
 * says what it removes and why the Go renderer is right not to emit it.
 */
const STRIP = [
  {
    what: 'astro-island / astro-slot wrappers',
    why: 'the island loader. The Go build ships web components that need no wrapper; the children are unwrapped in place so the content still compares.',
    apply: (doc) => {
      for (const el of doc.querySelectorAll('astro-island, astro-slot')) {
        el.replaceWith(...el.childNodes);
      }
    },
  },
  {
    what: 'stylesheet links, inline <style>, and external scripts, on BOTH sides',
    why: "asset delivery, not content. Astro emits one hashed bundle per page AND inlines any stylesheet under its size threshold (orgs.css, insights.css) straight into the HTML; the Go build links web/css and web/js verbatim, which is the whole point of the no-build rule. The two cannot be mapped onto each other. That the CSS and the scripts actually load is what the Playwright suite tests, on the real site.",
    symmetric: true,
  },
  {
    what: '<meta name="generator">',
    why: 'Astro stamps its own version. Nothing reads it.',
    apply: (doc) => {
      for (const el of doc.querySelectorAll('meta[name="generator"]')) el.remove();
    },
  },
  {
    what: 'astro-* scoped class hashes',
    why: 'Astro adds a per-component hash class to scoped styles. The Go build has no scoped styles — every rule is in web/css.',
    apply: (doc) => {
      for (const el of doc.querySelectorAll('[class]')) {
        const kept = [...el.classList].filter((c) => !/^astro-[a-z0-9]+$/i.test(c));
        if (kept.length !== el.classList.length) el.setAttribute('class', kept.join(' '));
      }
    },
  },
];

/**
 * Declared exceptions: subtrees compared shallowly, with a reason.
 *
 * Only ONE is expected — highlighted code. chroma's token classes and nesting differ from
 * Shiki's by an explicit owner decision, and the `<pre>`'s own class/style differ because the
 * themes moved out of the generator and into our stylesheet. The `<code>` element, the per-line
 * spans, the `L1…Ln` ids and the block's TEXT are still compared: line anchors are linkable
 * URLs and the text is the file itself.
 */
const EXCEPTIONS = [
  {
    selector: 'pre.shiki, pre.hf-chroma',
    what: 'highlighted code block internals',
    why: "chroma's tokens are not Shiki's (owner decision, 0.4.0). Text and line ids still compared.",
  },
];

/* ---- the signature ------------------------------------------------------- */

/**
 * Serialised in the browser. Produces a stable, diffable outline of a document.
 *
 * Attributes are restricted to the ones that carry meaning, and sorted, so attribute order is
 * not a difference. Text is collapsed to single spaces, because indentation is not a
 * difference either.
 */
const SIGNATURE_FN = `(exceptionSelectors) => {
  const MEANINGFUL = ['href', 'src', 'id', 'class', 'alt', 'title', 'type', 'name', 'value',
    'aria-label', 'aria-current', 'aria-pressed', 'aria-hidden', 'aria-modal', 'role',
    'hidden', 'selected', 'checked', 'disabled', 'colspan', 'rowspan', 'lang', 'width', 'height'];
  const isException = (el) => exceptionSelectors.some((s) => el.matches(s));

  const lines = [];
  const walk = (el, depth) => {
    const except = isException(el);
    const attrs = [];
    for (const name of MEANINGFUL) {
      if (!el.hasAttribute(name)) continue;
      // An exception element's own class is exempt too, not just its children: Shiki wrote a
      // shiki/shiki-themes/github-* class list plus an inline --shiki-* style, and the Go build
      // writes hf-chroma with the themes moved into web/css. Everything else about the element
      // — its id, its text, its line spans — is still compared.
      if (except && name === 'class') continue;
      let v = el.getAttribute(name);
      if (name === 'class') v = v.trim().split(/\\s+/).sort().join(' ');
      attrs.push(name + '=' + v);
    }
    for (const a of el.attributes) {
      if (!a.name.startsWith('data-')) continue;
      // data-now is the build clock, in milliseconds. Two builds run seconds apart differ in it
      // by construction, and comparing it compares the clock rather than the renderer. The
      // values DERIVED from it — every "3 hours ago" on the page — are still compared, and they
      // are what a reader sees.
      attrs.push(a.name + '=' + (a.name === 'data-now' ? '<clock>' : a.value));
    }
    attrs.sort();
    const own = Array.from(el.childNodes)
      .filter((n) => n.nodeType === 3)
      .map((n) => n.textContent.replace(/\\s+/g, ' ').trim())
      .filter(Boolean)
      .join(' ');
    lines.push('  '.repeat(depth) + el.tagName.toLowerCase() +
      (attrs.length ? ' [' + attrs.join(' ') + ']' : '') +
      (own ? ' "' + own + '"' : ''));
    if (except) {
      // Compare the block's text, not its token nodes.
      const text = el.textContent.replace(/\\s+$/, '');
      lines.push('  '.repeat(depth + 1) + '#exception-text ' + JSON.stringify(text));
      return;
    }
    for (const child of el.children) walk(child, depth + 1);
  };
  walk(document.documentElement, 0);
  return lines.join('\\n');
}`;

/* ---- driver -------------------------------------------------------------- */

function htmlFiles(root) {
  const out = [];
  const walk = (dir) => {
    for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
      const p = path.join(dir, e.name);
      if (e.isDirectory()) walk(p);
      else if (e.name.endsWith('.html')) out.push(path.relative(root, p).split(path.sep).join('/'));
    }
  };
  if (fs.existsSync(root)) walk(root);
  return out.sort();
}

/** Which page family a route belongs to, for the per-family report. */
function familyOf(route) {
  if (route === 'index.html') return 'profile';
  if (route === '404.html') return '404';
  if (route === 'repos/index.html') return 'repos-listing';
  if (route.startsWith('notes/')) return 'notes';
  if (route.startsWith('orgs/')) return 'orgs';
  const m = /^repos\/[^/]+\/(tree|blob|commits|commit|branches|tags|releases|insights)\b/.exec(route);
  if (m) return 'repo-' + m[1];
  if (/^repos\/[^/]+\/index\.html$/.test(route)) return 'repo-overview';
  return 'other';
}

async function main() {
  const [astroDir, goDir] = process.argv.slice(2).filter((a) => !a.startsWith('--'));
  if (!astroDir || !goDir) {
    console.error('usage: node tests/parity/compare.mjs <astro-dist> <go-dist> [--json=report.json] [--max=N]');
    process.exit(2);
  }
  const jsonArg = process.argv.find((a) => a.startsWith('--json='));
  const maxArg = process.argv.find((a) => a.startsWith('--max='));
  const max = maxArg ? Number(maxArg.slice(6)) : Infinity;

  const astroPages = htmlFiles(astroDir);
  const goPages = htmlFiles(goDir);
  const astroSet = new Set(astroPages);
  const goSet = new Set(goPages);

  const onlyAstro = astroPages.filter((p) => !goSet.has(p));
  const onlyGo = goPages.filter((p) => !astroSet.has(p));
  const shared = astroPages.filter((p) => goSet.has(p)).slice(0, max);

  const browser = await chromium.launch();
  const page = await browser.newPage();
  const selectors = EXCEPTIONS.map((e) => e.selector);

  // The strip pass runs in the page as one explicit function rather than by serialising the
  // STRIP array's closures: a serialised closure that silently fails to reconstruct would leave
  // the scaffolding in place and report false differences on every page.
  // Runs in the page as one explicit function rather than by serialising the STRIP array's
  // closures: a serialised closure that silently failed to reconstruct would leave the
  // scaffolding in place and report false differences on every page.
  //
  // `astroOnly` is the half that only makes sense on Astro's output. The rest is SYMMETRIC —
  // see the note on asset delivery in STRIP.
  const stripInPage = (astroOnly) => {
    if (astroOnly) {
      for (const el of document.querySelectorAll('astro-island, astro-slot')) el.replaceWith(...el.childNodes);
      for (const el of document.querySelectorAll('meta[name="generator"]')) el.remove();
      for (const el of document.querySelectorAll('[class]')) {
        const kept = [...el.classList].filter((c) => !/^astro-[a-z0-9]+$/i.test(c));
        if (kept.length !== el.classList.length) el.setAttribute('class', kept.join(' '));
      }
    }
    // Asset delivery, both sides. Astro emits one hashed bundle per page; the Go build links the
    // stylesheets and modules verbatim from web/. Neither list can be mapped onto the other, and
    // that difference IS the no-build rule working. What actually matters — that the CSS and the
    // scripts load and do their job — is what the Playwright suite tests, on the real site.
    for (const el of document.querySelectorAll('link[rel="stylesheet"], script[src], style')) el.remove();
  };

  const signature = async (file, astroOnly) => {
    const html = fs.readFileSync(file, 'utf8');
    await page.setContent(html, { waitUntil: 'domcontentloaded' });
    await page.evaluate(stripInPage, astroOnly);
    return page.evaluate(new Function('return ' + SIGNATURE_FN)(), selectors);
  };

  const diffs = [];
  const byFamily = new Map();
  for (const route of shared) {
    const fam = familyOf(route);
    const rec = byFamily.get(fam) ?? { compared: 0, differing: 0 };
    rec.compared++;
    const a = await signature(path.join(astroDir, route), true);
    const g = await signature(path.join(goDir, route), false);
    if (a !== g) {
      rec.differing++;
      if (diffs.length < 40) diffs.push({ route, family: fam, ...firstDifference(a, g) });
    }
    byFamily.set(fam, rec);
  }
  await browser.close();

  const report = {
    astroDir, goDir,
    pages: { astro: astroPages.length, go: goPages.length, compared: shared.length },
    onlyAstro: onlyAstro.slice(0, 40),
    onlyGo: onlyGo.slice(0, 40),
    families: Object.fromEntries([...byFamily].sort()),
    exceptions: EXCEPTIONS,
    diffs,
  };
  if (jsonArg) fs.writeFileSync(jsonArg.slice(7), JSON.stringify(report, null, 2) + '\n');

  console.log(`pages: ${astroPages.length} astro, ${goPages.length} go, ${shared.length} compared`);
  if (onlyAstro.length) console.log(`  MISSING from go (${onlyAstro.length}): ${onlyAstro.slice(0, 5).join(', ')}${onlyAstro.length > 5 ? ' …' : ''}`);
  if (onlyGo.length) console.log(`  EXTRA in go (${onlyGo.length}): ${onlyGo.slice(0, 5).join(', ')}${onlyGo.length > 5 ? ' …' : ''}`);
  console.log('\nper family:');
  for (const [fam, r] of [...byFamily].sort()) {
    const mark = r.differing === 0 ? 'ok  ' : 'DIFF';
    console.log(`  ${mark} ${fam.padEnd(16)} ${r.compared - r.differing}/${r.compared} match`);
  }
  for (const d of diffs.slice(0, 8)) {
    console.log(`\n--- ${d.route} (line ${d.line})`);
    console.log(`  astro: ${d.astro}`);
    console.log(`  go   : ${d.go}`);
  }
  const failed = diffs.length > 0 || onlyAstro.length > 0;
  console.log(failed ? '\nPARITY: differences found' : '\nPARITY: clean');
  process.exit(failed ? 1 : 0);
}

function firstDifference(a, b) {
  const al = a.split('\n');
  const bl = b.split('\n');
  for (let i = 0; i < Math.max(al.length, bl.length); i++) {
    if (al[i] !== bl[i]) return { line: i + 1, astro: al[i] ?? '(end)', go: bl[i] ?? '(end)' };
  }
  return { line: 0, astro: '', go: '' };
}

main().catch((err) => {
  console.error(err);
  process.exit(2);
});
