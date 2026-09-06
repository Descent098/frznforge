/**
 * `<hf-repo-listing>` — search / filter / sort / paginate for a repo listing.
 *
 * **Enhancement, not hydration.** The server has already rendered the complete default view
 * (see `src/components/RepoListing.astro`); this element adopts that DOM, wires the controls
 * it finds, and only re-renders once the visitor changes something — or once it discovers a
 * query string in the URL that the static build could not have known about.
 *
 * Cards are built by cloning the `<template>`s the server emitted, so this file contains no
 * card markup: change `RepoCard.astro` and the browser follows, because the template lives
 * beside it.
 *
 * See `web/js/format.js` for the rules this folder follows.
 */
import { withBase } from './base.js';
import { DEFAULT_HEAT, heatFor, relativeTime } from './format.js';
import { applyListing, defaultQuery, parseQuery, toSearchParams } from './listing.js';

/** @typedef {import('./listing.js').ListingQuery} ListingQuery */
/** @typedef {import('./listing.js').RepoSummary} RepoSummary */
/** @typedef {import('./format.js').HeatThresholds} HeatThresholds */

class HfRepoListing extends HTMLElement {
  connectedCallback() {
    // Guard against a double upgrade (the element is defined once, but a page could in
    // principle move the node); everything below assumes it runs exactly once.
    if (this.wired) return;
    this.wired = true;

    const payload = this.querySelector('script[data-hf-repos]');
    if (!payload) return; // no data: leave the server's HTML exactly as it is

    /** @type {RepoSummary[]} */
    this.repos = JSON.parse(payload.textContent ?? '[]');
    this.basePath = this.dataset.basePath || withBase('/repos/');
    this.pageSize = Number.parseInt(this.dataset.pageSize ?? '50', 10) || 50;
    this.now = new Date(Number.parseInt(this.dataset.now ?? '0', 10) || Date.now());
    /** @type {HeatThresholds} */
    this.heatDays = this.dataset.heat ? JSON.parse(this.dataset.heat) : DEFAULT_HEAT;

    this.grid = this.querySelector('[data-hf-grid]');
    this.empty = this.querySelector('[data-hf-empty]');
    this.count = this.querySelector('[data-hf-count]');
    this.pager = this.querySelector('[data-hf-pager]');
    this.pageInfo = this.querySelector('[data-hf-pageinfo]');
    this.prev = this.querySelector('[data-hf-prev]');
    this.next = this.querySelector('[data-hf-next]');
    this.searchInput = this.querySelector('[data-hf-q]');
    this.sortSelect = this.querySelector('[data-hf-sort]');
    this.cardTpl = this.querySelector('template[data-hf-card]');
    this.tagTpl = this.querySelector('template[data-hf-card-tag]');
    this.langTpl = this.querySelector('template[data-hf-card-lang]');
    this.noLangTpl = this.querySelector('template[data-hf-card-nolang]');

    /** @type {ListingQuery} */
    this.query = defaultQuery(this.pageSize);

    this.bind();

    // Adopt the real URL (e.g. a shared /repos/?lang=Go link). The server rendered the
    // default query, so only a non-default one needs a re-render — skipping it keeps the
    // server's markup untouched in the common case.
    const fromUrl = parseQuery(new URLSearchParams(location.search), this.pageSize);
    if (toSearchParams(fromUrl).toString() !== '') {
      this.query = fromUrl;
      this.render();
    }

    addEventListener('popstate', () => {
      this.query = parseQuery(new URLSearchParams(location.search), this.pageSize);
      this.render();
    });
  }

  /** Wire every control the server rendered. */
  bind() {
    this.searchInput?.addEventListener('input', (e) => {
      this.update({ q: /** @type {HTMLInputElement} */ (e.currentTarget).value });
    });
    this.sortSelect?.addEventListener('change', (e) => {
      this.update({ sort: /** @type {HTMLSelectElement} */ (e.currentTarget).value });
    });
    for (const btn of this.querySelectorAll('[data-hf-kind]')) {
      btn.addEventListener('click', () => this.update({ kind: btn.dataset.hfKind }));
    }
    for (const chip of this.querySelectorAll('[data-hf-facet]')) {
      chip.addEventListener('click', () => this.toggle(chip.dataset.hfFacet, chip.dataset.hfValue ?? ''));
    }
    for (const btn of this.querySelectorAll('[data-hf-clear]')) {
      btn.addEventListener('click', () => this.update({ q: '', languages: [], tags: [], kind: 'all', sort: 'updated-desc' }));
    }
    this.prev?.addEventListener('click', (e) => this.goto(e, this.result ? this.result.page - 1 : 1));
    this.next?.addEventListener('click', (e) => this.goto(e, this.result ? this.result.page + 1 : 2));
  }

  /**
   * @param {Partial<ListingQuery>} patch
   * @param {boolean} [resetPage] Any change but paging returns to page 1.
   */
  update(patch, resetPage = true) {
    this.query = { ...this.query, ...patch, ...(resetPage ? { page: 1 } : {}) };
    this.render();
  }

  /**
   * @param {'languages' | 'tags'} list
   * @param {string} value
   */
  toggle(list, value) {
    const cur = this.query[list];
    this.update({ [list]: cur.includes(value) ? cur.filter((v) => v !== value) : [...cur, value] });
  }

  /**
   * @param {Event} e
   * @param {number} page
   */
  goto(e, page) {
    e.preventDefault();
    if (!this.result || page < 1 || page > this.result.pageCount) return;
    this.update({ page }, false);
    this.querySelector('#listing-top')?.scrollIntoView({ block: 'start', behavior: 'smooth' });
  }

  /** Href for a page link — kept working without JS, so it is a real URL, not `#`. */
  pageHref(page) {
    const qs = toSearchParams({ ...this.query, page }).toString();
    return qs ? `?${qs}` : this.basePath;
  }

  render() {
    const result = applyListing(this.repos, this.query);
    this.result = result;

    // Controls reflect the query (they may have been driven from the URL, not a click).
    if (this.searchInput && this.searchInput.value !== this.query.q) this.searchInput.value = this.query.q;
    if (this.sortSelect && this.sortSelect.value !== this.query.sort) this.sortSelect.value = this.query.sort;
    for (const btn of this.querySelectorAll('[data-hf-kind]')) {
      btn.classList.toggle('is-active', btn.dataset.hfKind === this.query.kind);
    }
    for (const chip of this.querySelectorAll('[data-hf-facet]')) {
      const on = this.query[chip.dataset.hfFacet].includes(chip.dataset.hfValue ?? '');
      chip.classList.toggle('is-active', on);
      chip.setAttribute('aria-pressed', String(on));
    }

    const active =
      this.query.languages.length + this.query.tags.length + (this.query.kind !== 'all' ? 1 : 0) + (this.query.q ? 1 : 0);
    const clear = this.querySelector('.hf-listing-toolbar [data-hf-clear]');
    if (clear) clear.hidden = active === 0;

    if (this.count) this.count.textContent = `${result.total} of ${this.repos.length}`;

    // Results.
    if (this.grid) {
      this.grid.replaceChildren(...result.items.map((repo) => this.card(repo)));
      this.grid.hidden = result.items.length === 0;
    }
    if (this.empty) this.empty.hidden = result.items.length > 0;

    // Pager.
    if (this.pager) {
      this.pager.hidden = result.pageCount <= 1;
      if (this.pageInfo) this.pageInfo.textContent = `Page ${result.page} of ${result.pageCount}`;
      if (this.prev) {
        this.prev.setAttribute('aria-disabled', String(result.page === 1));
        this.prev.setAttribute('href', this.pageHref(Math.max(1, result.page - 1)));
      }
      if (this.next) {
        this.next.setAttribute('aria-disabled', String(result.page === result.pageCount));
        this.next.setAttribute('href', this.pageHref(Math.min(result.pageCount, result.page + 1)));
      }
    }

    this.syncUrl();
  }

  /** Mirror the query into the address bar so the view is shareable and Back works. */
  syncUrl() {
    const qs = toSearchParams(this.query).toString();
    const want = qs ? `?${qs}` : '';
    if (location.search !== want) history.replaceState(null, '', qs ? `?${qs}` : location.pathname);
  }

  /**
   * Build one card by cloning the server's template. Mirrors `RepoCard.astro` — the two are
   * checked against each other by the e2e suite, which asserts on the same classes for
   * server-rendered and client-rendered cards alike.
   *
   * @param {RepoSummary} repo
   * @returns {Element}
   */
  card(repo) {
    const node = this.cardTpl.content.firstElementChild.cloneNode(true);
    const heat = heatFor(repo.updatedAt, this.now, this.heatDays);
    node.className = `hf-repo-card heat-${heat}`;
    node.dataset.slug = repo.slug;

    const name = node.querySelector('.hf-repo-name');
    name.textContent = repo.name;
    name.setAttribute('href', withBase(`/repos/${repo.slug}/`));

    const badge = node.querySelector('.hf-tag--template');
    if (!repo.template) badge.remove();

    node.querySelector('.hf-repo-desc').textContent =
      repo.description ?? (repo.empty ? 'Empty repository.' : 'No description.');

    const tags = node.querySelector('.hf-repo-tags');
    tags.replaceChildren(
      ...repo.tags.map((tag) => {
        const a = this.tagTpl.content.firstElementChild.cloneNode(true);
        a.textContent = tag;
        a.setAttribute('href', `${this.basePath}?tag=${encodeURIComponent(tag)}`);
        return a;
      }),
    );

    const langs = node.querySelector('.hf-langs');
    const top = repo.languages.slice(0, 3);
    if (top.length > 0) {
      langs.replaceChildren(
        ...top.map((l) => {
          const span = this.langTpl.content.firstElementChild.cloneNode(true);
          span.querySelector('.hf-lang-dot').style.background = l.color ?? 'var(--hf-lang-other)';
          span.append(`${l.name} ${l.percent}%`);
          return span;
        }),
      );
    } else {
      const span = this.noLangTpl.content.firstElementChild.cloneNode(true);
      span.textContent = repo.empty ? 'No commits' : 'No code detected';
      langs.replaceChildren(span);
    }

    const age = node.querySelector('.hf-age');
    age.className = `hf-age t-${heat}`;
    age.setAttribute('title', repo.updatedAt ?? '');
    age.textContent = repo.updatedAt ? relativeTime(repo.updatedAt, this.now) : 'never';

    return node;
  }
}

customElements.define('hf-repo-listing', HfRepoListing);
