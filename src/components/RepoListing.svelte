<script lang="ts">
  /**
   * Repo listing island. Server-renders the initial query (so page 1 works with JS off);
   * with JS, filters/sort/search/pagination update in place and sync to the URL.
   */
  import { onMount } from 'svelte';
  import RepoCard from './RepoCard.svelte';
  import { DEFAULT_HEAT, type HeatThresholds } from '../lib/format';
  import {
    applyListing,
    facets as computeFacets,
    parseQuery,
    toSearchParams,
    SORT_KEYS,
    SORT_LABELS,
    type ListingQuery,
    type RepoSummary,
  } from '../lib/listing';

  interface Props {
    repos: RepoSummary[];
    pageSize: number;
    now: number;
    /** Recency-accent day boundaries (`theme.heat`), forwarded to every card. */
    heatDays?: HeatThresholds;
    /** Initial query string (from the build: empty). */
    initialSearch?: string;
    /**
     * Path this listing lives at, used by every link it emits that is not a bare query string
     * (tag chips, and the pager's "no filters left" href). Defaults to the site-wide listing;
     * `/orgs/<slug>/repos/` passes its own so filtering inside an organization stays inside it.
     * Must end in a slash.
     */
    basePath?: string;
  }
  let { repos, pageSize, now, heatDays = DEFAULT_HEAT, initialSearch = '', basePath = '/repos/' }: Props = $props();

  let query = $state<ListingQuery>(parseQuery(new URLSearchParams(initialSearch), pageSize));
  let hydrated = $state(false);

  const facets = computeFacets(repos);
  const result = $derived(applyListing(repos, query));

  onMount(() => {
    // Adopt the real URL once on the client (e.g. /repos/?lang=Go shared link).
    query = parseQuery(new URLSearchParams(location.search), pageSize);
    hydrated = true;
    const onPop = () => (query = parseQuery(new URLSearchParams(location.search), pageSize));
    window.addEventListener('popstate', onPop);
    return () => window.removeEventListener('popstate', onPop);
  });

  $effect(() => {
    if (!hydrated) return;
    const qs = toSearchParams(query).toString();
    const next = qs ? `?${qs}` : location.pathname;
    if (location.search !== (qs ? `?${qs}` : '')) history.replaceState(null, '', next);
  });

  function update(patch: Partial<ListingQuery>, resetPage = true) {
    query = { ...query, ...patch, ...(resetPage ? { page: 1 } : {}) };
  }
  function toggle(list: 'languages' | 'tags', value: string) {
    const cur = query[list];
    update({ [list]: cur.includes(value) ? cur.filter((v) => v !== value) : [...cur, value] });
  }
  function clearAll() {
    update({ q: '', languages: [], tags: [], kind: 'all', sort: 'updated-desc' });
  }
  const activeFilterCount = $derived(query.languages.length + query.tags.length + (query.kind !== 'all' ? 1 : 0) + (query.q ? 1 : 0));

  /** Page link href (works without JS: server renders page 1; links hit the same page with ?page=N). */
  function pageHref(p: number): string {
    const qs = toSearchParams({ ...query, page: p }).toString();
    return qs ? `?${qs}` : basePath;
  }
  /** Tag chip href: the same listing, filtered to one tag. */
  function tagHref(tag: string): string {
    return `${basePath}?tag=${encodeURIComponent(tag)}`;
  }
  function goto(p: number, e: MouseEvent) {
    e.preventDefault();
    update({ page: p }, false);
    document.getElementById('listing-top')?.scrollIntoView({ block: 'start', behavior: 'smooth' });
  }
</script>

<div class="hf-listing" id="listing-top">
  <div class="hf-listing-toolbar">
    <label class="hf-input hf-input--search">
      <svg class="hf-i"><use href="#i-search" /></svg>
      <input
        type="search"
        placeholder="Search repositories…"
        value={query.q}
        oninput={(e) => update({ q: (e.currentTarget as HTMLInputElement).value })}
        aria-label="Search repositories"
      />
    </label>
    <label class="hf-select">
      <span>Sort</span>
      <select value={query.sort} onchange={(e) => update({ sort: (e.currentTarget as HTMLSelectElement).value as ListingQuery['sort'] })} aria-label="Sort repositories">
        {#each SORT_KEYS as k}<option value={k}>{SORT_LABELS[k]}</option>{/each}
      </select>
    </label>
    <div class="hf-seg" role="group" aria-label="Repository kind">
      {#each [['all', 'All'], ['normal', 'Repos'], ['template', 'Templates']] as [k, label]}
        <button type="button" class:is-active={query.kind === k} onclick={() => update({ kind: k as ListingQuery['kind'] })}>{label}{k === 'template' && facets.templates ? ` (${facets.templates})` : ''}</button>
      {/each}
    </div>
    <span class="hf-listing-count" aria-live="polite">{result.total} of {repos.length}</span>
    {#if activeFilterCount > 0}
      <button type="button" class="hf-btn hf-btn--ghost hf-btn--sm" onclick={clearAll}>Clear</button>
    {/if}
  </div>

  {#if facets.languages.length || facets.tags.length}
    <div class="hf-facets">
      {#if facets.languages.length}
        <div class="hf-facet-group">
          <span class="hf-facet-label">Language</span>
          {#each facets.languages as f}
            <button type="button" class="hf-chip" class:is-active={query.languages.includes(f.name)} onclick={() => toggle('languages', f.name)} aria-pressed={query.languages.includes(f.name)}>
              {f.name} <small>{f.count}</small>
            </button>
          {/each}
        </div>
      {/if}
      {#if facets.tags.length}
        <div class="hf-facet-group">
          <span class="hf-facet-label">Tags</span>
          {#each facets.tags as f}
            <button type="button" class="hf-chip" class:is-active={query.tags.includes(f.name)} onclick={() => toggle('tags', f.name)} aria-pressed={query.tags.includes(f.name)}>
              {f.name} <small>{f.count}</small>
            </button>
          {/each}
        </div>
      {/if}
    </div>
  {/if}

  {#if result.items.length === 0}
    <div class="hf-empty">
      <svg class="hf-i"><use href="#i-repo" /></svg>
      {#if repos.length === 0}
        <p><strong>No repositories yet.</strong> Add some to <code>frznforge.config.ts</code> and run <code>npm run ingest</code>.</p>
      {:else}
        <p><strong>Nothing matches.</strong> Try fewer filters.</p>
        <button type="button" class="hf-btn hf-btn--ghost hf-btn--sm" onclick={clearAll}>Clear filters</button>
      {/if}
    </div>
  {:else}
    <div class="hf-repo-grid hf-repo-grid--listing">
      {#each result.items as repo (repo.slug)}
        <RepoCard {repo} {now} {tagHref} {heatDays} />
      {/each}
    </div>
  {/if}

  {#if result.pageCount > 1}
    <nav class="hf-pager" aria-label="Pagination">
      <a class="hf-btn hf-btn--ghost hf-btn--sm" href={pageHref(result.page - 1)} aria-disabled={result.page === 1} onclick={(e) => result.page > 1 && goto(result.page - 1, e)}>← Prev</a>
      <span>Page {result.page} of {result.pageCount}</span>
      <a class="hf-btn hf-btn--ghost hf-btn--sm" href={pageHref(result.page + 1)} aria-disabled={result.page === result.pageCount} onclick={(e) => result.page < result.pageCount && goto(result.page + 1, e)}>Next →</a>
    </nav>
  {/if}
</div>
