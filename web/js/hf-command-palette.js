/**
 * `<hf-command-palette>` — the Ctrl/Cmd+K palette. Mounted once by the site shell.
 *
 * Unlike the listing, this has no server-rendered half to enhance: the old Svelte island
 * rendered nothing until it was opened, and the e2e suite pins that — `a11y.spec.ts` asserts
 * `.hf-palette` has count **0** after Escape, so the dialog must be created on open and
 * removed on close, not hidden. Its markup therefore lives here rather than in a `<template>`
 * the layout emits, which also means the (later) Go renderer has nothing to reproduce for it.
 *
 * The search index is fetched from `/search-index.json` on first open, so a visitor who never
 * opens the palette pays nothing for it.
 *
 * Listeners are attached at module evaluation — module scripts run before `load`, so a click
 * or keypress that arrives as soon as the page is ready always lands on a live handler.
 *
 * See `web/js/format.js` for the rules this folder follows.
 */
import { withBase } from './base.js';
import { search } from './search.js';

/** @typedef {import('./search.js').SearchDoc} SearchDoc */
/** @typedef {import('./search.js').ScoredDoc} ScoredDoc */

/** @type {Record<SearchDoc['kind'], string>} */
const KIND_LABEL = {
  repo: 'Repositories',
  org: 'Organizations',
  note: 'Notes',
  file: 'Files',
  page: 'Pages',
  action: 'Actions',
};

/** @type {Record<SearchDoc['kind'], string>} */
const KIND_ICON = {
  repo: '#i-repo',
  org: '#i-layers',
  note: '#i-note',
  file: '#i-file',
  page: '#i-home',
  action: '#i-bolt',
};

/**
 * How many docs of each kind the empty-query view shows; a kind absent from this map is
 * hidden entirely. Files and notes are numerous and carry dates that would sort them above
 * the repos, so the default list stays a short "where do I go" menu — the Notes /
 * Organizations index pages are in it as `page` docs.
 *
 * Quotas rather than one flat slice: organizations carry no `date`, so under a single
 * recency sort every dated repo (plus the actions and page docs, which come first in the
 * array) pushed them past the cut and the ORGANIZATIONS group never appeared at all.
 */
const DEFAULT_VIEW_QUOTA = { action: 2, repo: 5, org: 3, page: 4 };

/** Display order of the groups. */
const GROUP_ORDER = ['action', 'repo', 'org', 'note', 'file', 'page'];

/**
 * @param {string} tag
 * @param {Record<string, string>} [attrs]
 * @param {Array<Node | string>} [children]
 */
function el(tag, attrs = {}, children = []) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) node.setAttribute(k, v);
  node.append(...children);
  return node;
}

/** An `<svg class="hf-i"><use href="#i-x"/></svg>` sprite reference. */
function icon(href) {
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.setAttribute('class', 'hf-i');
  const use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
  use.setAttribute('href', href);
  svg.append(use);
  return svg;
}

class HfCommandPalette extends HTMLElement {
  constructor() {
    super();
    /** @type {SearchDoc[] | null} */
    this.docs = null;
    this.loadError = false;
    /** @type {SearchDoc[]} */
    this.actions = [];
    this.query = '';
    this.active = 0;
    /** @type {ScoredDoc[]} */
    this.flat = [];
    /** @type {HTMLElement | null} */
    this.overlay = null;
    /**
     * The control that had focus when the palette opened, so closing can hand focus back.
     *
     * Without this, closing drops focus onto `<body>` and the next Tab starts again from the
     * top of the page — a keyboard user forty tab stops down a commits list who opens the
     * palette and changes their mind loses their place entirely (WCAG 2.4.3 Focus Order).
     */
    this.returnTo = null;
  }

  get isOpen() {
    return this.overlay !== null;
  }

  /** Page-context actions, rebuilt on open (theme toggle everywhere; clone URL on repo pages). */
  actionDocs() {
    const out = [
      { kind: 'action', title: 'Toggle theme', detail: 'Switch light / dark', url: '#action:theme', keywords: 'dark light mode' },
    ];
    const clone = document.querySelector('[data-clone-url]')?.dataset.cloneUrl;
    if (clone) out.push({ kind: 'action', title: 'Copy clone URL', detail: clone, url: '#action:clone', keywords: 'git clone copy' });
    return out;
  }

  async open() {
    if (this.isOpen) return;
    this.returnTo = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    // `query` and `active` deliberately survive a close/open cycle, as they did when this was
    // component state: reopening shows the last search, and `select()` below is what makes
    // typing replace it. Clearing here would feel tidier and would be a behaviour change.
    this.actions = this.actionDocs();
    this.build();
    this.input.value = this.query;
    this.renderResults();
    this.input.focus();
    this.input.select();

    if (!this.docs && !this.loadError) {
      try {
        const res = await fetch(withBase('/search-index.json'));
        this.docs = (await res.json()).docs;
      } catch {
        this.loadError = true;
      }
      if (this.isOpen) this.renderResults();
    }
  }

  /**
   * Close and put focus back where it came from.
   *
   * `navigating` skips the restore for the one case where it would be wrong: `run()` is about
   * to replace the document, and focusing a control on a page being torn down just fights the
   * new page for it.
   *
   * @param {boolean} [navigating]
   */
  close(navigating = false) {
    if (!this.isOpen) return;
    this.overlay.remove();
    this.overlay = null;
    if (navigating) return;
    const target = this.returnTo;
    this.returnTo = null;
    if (target && target.isConnected) target.focus();
  }

  /** Create the dialog and put it in the document. */
  build() {
    this.input = el('input', {
      type: 'text',
      placeholder: 'Search repos, notes, files, actions…',
      'aria-label': 'Search repositories, notes, organizations, files and actions',
      autocomplete: 'off',
      spellcheck: 'false',
    });
    this.input.addEventListener('input', () => {
      this.query = this.input.value;
      this.active = 0; // a new query always starts at the top hit
      this.renderResults();
    });
    this.input.addEventListener('keydown', (e) => this.onKeydown(e));

    this.results = el('div', { class: 'hf-palette-results' });

    const dialog = el('div', { class: 'hf-palette', role: 'dialog', 'aria-modal': 'true', 'aria-label': 'Command palette' }, [
      el('div', { class: 'hf-palette-input' }, [icon('#i-search'), this.input, el('kbd', { class: 'hf-kbd' }, ['Esc'])]),
      this.results,
      el('div', { class: 'hf-palette-foot' }, [
        el('span', {}, [el('kbd', { class: 'hf-kbd' }, ['↑']), el('kbd', { class: 'hf-kbd' }, ['↓']), ' navigate']),
        el('span', {}, [el('kbd', { class: 'hf-kbd' }, ['↵']), ' open']),
        el('span', { class: 'hf-spacer' }),
        (this.footCount = el('span')),
      ]),
    ]);

    this.overlay = el('div', { class: 'hf-palette-overlay', role: 'presentation' }, [dialog]);
    this.overlay.addEventListener('click', (e) => {
      if (e.target === e.currentTarget) this.close();
    });
    document.body.append(this.overlay);
  }

  /** Rank the current query and rebuild the result list. */
  renderResults() {
    const all = [...this.actions, ...(this.docs ?? [])];
    /** @type {ScoredDoc[]} */
    let scored;
    if (!this.query.trim()) {
      // empty query: actions, repos (by recency), organizations and the index pages
      const left = new Map(Object.entries(DEFAULT_VIEW_QUOTA));
      const dflt = all.filter((d) => left.has(d.kind));
      dflt.sort((a, b) => (b.date ?? '').localeCompare(a.date ?? ''));
      scored = [];
      for (const doc of dflt) {
        const remaining = left.get(doc.kind);
        if (remaining === 0) continue;
        left.set(doc.kind, remaining - 1);
        scored.push({ doc, score: 0 });
      }
    } else {
      scored = search(all, this.query, 12);
    }

    const groups = GROUP_ORDER.map((kind) => ({
      kind,
      label: KIND_LABEL[kind],
      items: scored.filter((r) => r.doc.kind === kind),
    })).filter((g) => g.items.length > 0);
    this.flat = groups.flatMap((g) => g.items);
    if (this.active > this.flat.length - 1) this.active = Math.max(0, this.flat.length - 1);

    if (this.loadError) {
      this.results.replaceChildren(el('p', { class: 'hf-palette-msg' }, ['Search index unavailable.']));
    } else if (this.query.trim() && this.flat.length === 0) {
      this.results.replaceChildren(el('p', { class: 'hf-palette-msg' }, [`No matches for “${this.query}”.`]));
    } else {
      let idx = 0;
      const nodes = groups.map((group) => {
        // Group labels inside the dialog, not sections of the page: as headings they injected
        // h4s into whatever page the palette was opened on. The list of buttons carries the
        // name instead.
        const box = el('div', { class: 'hf-palette-group', role: 'group', 'aria-label': group.label }, [
          el('p', { class: 'hf-palette-grouphead', 'aria-hidden': 'true' }, [group.label]),
        ]);
        for (const r of group.items) {
          const here = idx++;
          const text = el('span', { class: 'hf-palette-item-text' }, [el('strong', {}, [r.doc.title])]);
          if (r.doc.detail) text.append(el('small', {}, [r.doc.detail]));
          const item = el(
            'button',
            { type: 'button', class: here === this.active ? 'hf-palette-item is-active' : 'hf-palette-item' },
            [icon(KIND_ICON[r.doc.kind]), text, el('kbd', { class: 'hf-kbd' }, ['↵'])],
          );
          item.addEventListener('click', () => this.run(r.doc));
          item.addEventListener('mousemove', () => this.setActive(here));
          box.append(item);
        }
        return box;
      });
      this.results.replaceChildren(...nodes);
    }

    this.footCount.textContent = `${this.flat.length} result${this.flat.length === 1 ? '' : 's'}`;
  }

  /** Move the highlight without rebuilding the list. */
  setActive(next) {
    if (next === this.active) return;
    this.active = next;
    const items = this.results.querySelectorAll('.hf-palette-item');
    items.forEach((item, i) => item.classList.toggle('is-active', i === next));
  }

  scrollActive() {
    this.results.querySelector('.is-active')?.scrollIntoView({ block: 'nearest' });
  }

  /** @param {SearchDoc} doc */
  run(doc) {
    if (doc.url === '#action:theme') {
      document.getElementById('hf-theme-toggle')?.click();
      this.close();
      return;
    }
    if (doc.url === '#action:clone') {
      const clone = document.querySelector('[data-clone-url]')?.dataset.cloneUrl;
      if (clone) navigator.clipboard?.writeText(clone).catch(() => {});
      this.close();
      return;
    }
    this.close(true);
    location.href = doc.url;
  }

  /** @param {KeyboardEvent} e */
  onKeydown(e) {
    if (e.key === 'Escape') {
      this.close();
    } else if (e.key === 'ArrowDown') {
      e.preventDefault();
      this.setActive(Math.min(this.active + 1, this.flat.length - 1));
      this.scrollActive();
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      this.setActive(Math.max(this.active - 1, 0));
      this.scrollActive();
    } else if (e.key === 'Enter') {
      e.preventDefault();
      const r = this.flat[this.active];
      if (r) this.run(r.doc);
    }
  }
}

customElements.define('hf-command-palette', HfCommandPalette);

/**
 * Global shortcuts, bound to the window rather than to the element: the palette has no
 * presence on the page until it is opened, so there is nothing else to listen on.
 *
 * `palette()` resolves the element lazily because this module may evaluate before the custom
 * element in the body has been parsed.
 */
const palette = () => document.querySelector('hf-command-palette');

addEventListener('keydown', (e) => {
  const p = palette();
  if (!p) return;
  const tag = e.target instanceof HTMLElement ? e.target.tagName : '';
  if ((e.key === 'k' || e.key === 'K') && (e.ctrlKey || e.metaKey)) {
    e.preventDefault();
    p.isOpen ? p.close() : p.open();
  } else if (e.key === '/' && !p.isOpen && !/input|textarea|select/i.test(tag)) {
    e.preventDefault();
    p.open();
  }
});

addEventListener('frznforge:palette', () => palette()?.open());
