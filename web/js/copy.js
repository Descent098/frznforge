/**
 * Wires every `button[data-copy]` on the page to the clipboard.
 *
 * One implementation for both places that used to carry their own copy of it — the shared
 * `CopyScript.astro` and the repo overview's inline block — because they had drifted into
 * being character-identical anyway, and the command palette's "Copy clone URL" action reads
 * the same `[data-clone-url]` contract.
 *
 * See `web/js/format.js` for the rules this folder follows.
 */
for (const btn of document.querySelectorAll('button[data-copy]')) {
  btn.addEventListener('click', async () => {
    try {
      await navigator.clipboard.writeText(btn.dataset.copy ?? '');
      btn.classList.add('is-copied');
      setTimeout(() => btn.classList.remove('is-copied'), 1200);
    } catch {}
  });
}
