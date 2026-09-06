/**
 * Theme toggle: the button in the sidebar, and the `t` hotkey.
 *
 * The *bootstrap* half — reading `localStorage` and stamping `data-theme` before anything
 * paints — deliberately stays inline in the site shell (`src/layouts/Base.astro`). It exists
 * to run before first paint, and an external file would reintroduce the flash of the wrong
 * theme that it was written to prevent. This half runs whenever it likes.
 *
 * See `web/js/format.js` for the rules this folder follows.
 */
const btn = document.getElementById('hf-theme-toggle');

function current() {
  const t = document.documentElement.getAttribute('data-theme');
  if (t) return t;
  return matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

function toggle() {
  const next = current() === 'dark' ? 'light' : 'dark';
  document.documentElement.setAttribute('data-theme', next);
  try {
    localStorage.setItem('frznforge-theme', next);
  } catch {}
}

if (btn) {
  btn.addEventListener('click', toggle);
  document.addEventListener('keydown', (e) => {
    // The tag guard is what stops a `t` typed into the palette's search box — or any other
    // field — from flipping the theme underneath the person typing.
    const tag = e.target instanceof HTMLElement ? e.target.tagName : '';
    if (e.key === 't' && !e.ctrlKey && !e.metaKey && !e.altKey && !/input|textarea|select/i.test(tag)) toggle();
  });
}
