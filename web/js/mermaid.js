/**
 * Client-side renderer for ```mermaid fences (0.2.0).
 *
 * Loaded by a page only when its rendered markdown actually holds a diagram
 * (`containsMermaid()` in `src/lib/markdown.ts`) — mermaid is heavyweight, and that check is
 * what keeps it off every other page.
 *
 * How it stays honest with the rest of the site:
 *  - the `<pre class="hf-mermaid">` emitted at build time IS the page: no-JS readers get the
 *    diagram source as a code block, and an invalid diagram keeps it;
 *  - mermaid is vendored under `web/vendor/mermaid/` (see the README there) and imported
 *    lazily — the browser fetches the entry, and the entry fetches only the chunks these
 *    diagrams need, once one nears the viewport. No bundler, and no third-party host;
 *  - diagrams re-render when the theme toggles (`data-theme` on <html>, or the OS scheme
 *    while no explicit choice is set), because mermaid bakes its colours into the SVG;
 *  - render ids are seeded per container position, so ids are unique on multi-diagram pages
 *    and stable across re-renders (the duplicate-id a11y probe holds).
 *
 * See `web/js/format.js` for the rules this folder follows.
 */
const containers = Array.from(document.querySelectorAll('pre.hf-mermaid'));

if (containers.length > 0) {
  /** @type {Map<Element, string>} */
  const sources = new Map();
  for (const el of containers) sources.set(el, el.textContent ?? '');

  const dark = () => {
    const set = document.documentElement.getAttribute('data-theme');
    return set ? set === 'dark' : matchMedia('(prefers-color-scheme: dark)').matches;
  };

  let started = false;
  // Sequential queue: a fast theme-toggle must not interleave two render passes (ids would
  // collide and half-finished SVGs would race the swap). The pass catches its own failures
  // and the queue chains through both settle states, so a mermaid load that fails once
  // (offline, flaky host) leaves the code-block fallback quietly and keeps the queue
  // settled — a later theme-toggle render() retries the import instead of chaining onto a
  // poisoned rejection forever.
  let queue = Promise.resolve();
  const render = () => {
    started = true;
    const pass = async () => {
      let mermaid;
      try {
        // Relative to this file: web/js/ → web/vendor/. The browser resolves it literally,
        // which is the whole point of the no-build rule.
        mermaid = (await import('../vendor/mermaid/mermaid.esm.min.mjs')).default;
        mermaid.initialize({ startOnLoad: false, securityLevel: 'strict', theme: dark() ? 'dark' : 'default' });
      } catch {
        return; // the source code blocks stay — their own honest fallback
      }
      for (let i = 0; i < containers.length; i++) {
        const el = containers[i];
        try {
          const { svg } = await mermaid.render(`hf-mermaid-${i}`, sources.get(el) ?? '');
          el.innerHTML = svg;
          el.setAttribute('role', 'img');
          el.setAttribute('aria-label', 'Diagram');
          el.classList.add('hf-mermaid--rendered');
        } catch {
          // Invalid diagram source: the code block stays, which is its own honest fallback.
        }
      }
    };
    queue = queue.then(pass, pass);
  };

  // Load nothing until a diagram approaches the viewport.
  const io = new IntersectionObserver(
    (entries) => {
      if (entries.some((e) => e.isIntersecting)) {
        io.disconnect();
        render();
      }
    },
    { rootMargin: '200px' },
  );
  for (const el of containers) io.observe(el);

  // Re-render on theme changes — but only once rendering has started at all.
  new MutationObserver(() => {
    if (started) render();
  }).observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
  matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
    if (started) render();
  });
}
