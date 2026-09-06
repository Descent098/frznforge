/**
 * Sidebar search button → the command palette.
 *
 * A custom event rather than a direct call, so the sidebar knows nothing about the palette's
 * implementation and the palette can be absent (or not yet upgraded) without this throwing.
 *
 * See `web/js/format.js` for the rules this folder follows.
 */
document.getElementById('hf-search-open')?.addEventListener('click', (e) => {
  e.preventDefault();
  dispatchEvent(new CustomEvent('frznforge:palette'));
});
