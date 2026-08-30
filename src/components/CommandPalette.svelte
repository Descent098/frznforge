<script lang="ts">
  /**
   * Ctrl/Cmd+K command palette. Mounted on every page (Base layout). The search index is
   * fetched lazily from /search-index.json on first open, so it costs nothing until used.
   * Fully keyboard operable: Ctrl+K / Cmd+K / `/` open, Esc closes, ↑↓ navigate, Enter opens.
   */
  import { onMount, tick } from 'svelte';
  import { withBase } from '../lib/base';
  import { search, type ScoredDoc, type SearchDoc } from '../lib/search';

  let open = $state(false);
  let query = $state('');
  let active = $state(0);
  let docs: SearchDoc[] | null = $state(null);
  let loadError = $state(false);
  let inputEl: HTMLInputElement | undefined = $state();
  let listEl: HTMLElement | undefined = $state();

  /** Page-context actions, rebuilt on open (theme toggle everywhere; clone URL on repo pages). */
  function actionDocs(): SearchDoc[] {
    const out: SearchDoc[] = [
      { kind: 'action', title: 'Toggle theme', detail: 'Switch light / dark', url: '#action:theme', keywords: 'dark light mode' },
    ];
    const clone = document.querySelector<HTMLElement>('[data-clone-url]')?.dataset.cloneUrl;
    if (clone) out.push({ kind: 'action', title: 'Copy clone URL', detail: clone, url: '#action:clone', keywords: 'git clone copy' });
    return out;
  }

  let actions: SearchDoc[] = $state([]);

  const KIND_LABEL: Record<SearchDoc['kind'], string> = {
    repo: 'Repositories',
    org: 'Organizations',
    note: 'Notes',
    file: 'Files',
    page: 'Pages',
    action: 'Actions',
  };
  const KIND_ICON: Record<SearchDoc['kind'], string> = {
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
   * Quotas rather than one flat `slice`: organizations carry no `date`, so under a single
   * recency sort every dated repo (plus the actions and page docs, which come first in the
   * array) pushed them past the cut and the ORGANIZATIONS group never appeared at all.
   */
  const DEFAULT_VIEW_QUOTA: Partial<Record<SearchDoc['kind'], number>> = {
    action: 2,
    repo: 5,
    org: 3,
    page: 4,
  };

  const results: ScoredDoc[] = $derived.by(() => {
    const all = [...actions, ...(docs ?? [])];
    if (!query.trim()) {
      // empty query: actions, repos (by recency), organizations and the index pages
      const left = new Map<SearchDoc['kind'], number>(
        Object.entries(DEFAULT_VIEW_QUOTA) as Array<[SearchDoc['kind'], number]>,
      );
      const dflt = all.filter((d) => left.has(d.kind));
      dflt.sort((a, b) => (b.date ?? '').localeCompare(a.date ?? ''));
      const out: ScoredDoc[] = [];
      for (const doc of dflt) {
        const remaining = left.get(doc.kind)!;
        if (remaining === 0) continue;
        left.set(doc.kind, remaining - 1);
        out.push({ doc, score: 0 });
      }
      return out;
    }
    return search(all, query, 12);
  });

  /** Group results for display while preserving rank order inside groups. */
  const groups = $derived.by(() => {
    const order: SearchDoc['kind'][] = ['action', 'repo', 'org', 'note', 'file', 'page'];
    return order
      .map((kind) => ({ kind, label: KIND_LABEL[kind], items: results.filter((r) => r.doc.kind === kind) }))
      .filter((g) => g.items.length > 0);
  });
  /** Flattened display order (group by group) for keyboard navigation. */
  const flat = $derived(groups.flatMap((g) => g.items));

  $effect(() => {
    query;
    active = 0;
  });

  /**
   * The control that had focus when the palette opened, so closing can hand focus back.
   *
   * Without this, `open = false` drops focus onto `<body>` and the next Tab starts again from
   * the top of the page — a keyboard user forty tab stops down a commits list who opens the
   * palette and changes their mind loses their place entirely (WCAG 2.4.3 Focus Order).
   */
  let returnTo: HTMLElement | null = null;

  async function openPalette() {
    if (!open) returnTo = (document.activeElement as HTMLElement | null) ?? null;
    open = true;
    actions = actionDocs();
    if (!docs && !loadError) {
      try {
        const res = await fetch(withBase('/search-index.json'));
        docs = ((await res.json()) as { docs: SearchDoc[] }).docs;
      } catch {
        loadError = true;
      }
    }
    await tick();
    inputEl?.focus();
    inputEl?.select();
  }
  /**
   * Close and put focus back where it came from.
   *
   * `navigating` skips the restore for the one case where it would be wrong: `run()` is about
   * to replace the document, and focusing a control on a page that is being torn down just
   * fights the new page for it.
   */
  function close(navigating = false) {
    open = false;
    if (navigating) return;
    const target = returnTo;
    returnTo = null;
    // after the {#if} tears the dialog down, or the focus lands inside a removed subtree
    tick().then(() => target?.isConnected && target.focus());
  }

  function run(doc: SearchDoc) {
    if (doc.url === '#action:theme') {
      document.getElementById('hf-theme-toggle')?.click();
      close();
      return;
    }
    if (doc.url === '#action:clone') {
      const clone = document.querySelector<HTMLElement>('[data-clone-url]')?.dataset.cloneUrl;
      if (clone) navigator.clipboard?.writeText(clone).catch(() => {});
      close();
      return;
    }
    close(true);
    window.location.href = doc.url;
  }

  function onKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape') { close(); return; }
    if (e.key === 'ArrowDown') { e.preventDefault(); active = Math.min(active + 1, flat.length - 1); scrollActive(); }
    else if (e.key === 'ArrowUp') { e.preventDefault(); active = Math.max(active - 1, 0); scrollActive(); }
    else if (e.key === 'Enter') { e.preventDefault(); const r = flat[active]; if (r) run(r.doc); }
  }
  function scrollActive() {
    tick().then(() => listEl?.querySelector('.is-active')?.scrollIntoView({ block: 'nearest' }));
  }

  onMount(() => {
    const onGlobal = (e: KeyboardEvent) => {
      const tag = (e.target as HTMLElement)?.tagName ?? '';
      if ((e.key === 'k' || e.key === 'K') && (e.ctrlKey || e.metaKey)) { e.preventDefault(); open ? close() : openPalette(); }
      else if (e.key === '/' && !open && !/input|textarea|select/i.test(tag)) { e.preventDefault(); openPalette(); }
    };
    const onOpenEvent = () => openPalette();
    window.addEventListener('keydown', onGlobal);
    window.addEventListener('frznforge:palette', onOpenEvent);
    return () => {
      window.removeEventListener('keydown', onGlobal);
      window.removeEventListener('frznforge:palette', onOpenEvent);
    };
  });
</script>

{#if open}
  <div class="hf-palette-overlay" role="presentation" onclick={(e) => e.target === e.currentTarget && close()}>
    <div class="hf-palette" role="dialog" aria-modal="true" aria-label="Command palette">
      <div class="hf-palette-input">
        <svg class="hf-i"><use href="#i-search" /></svg>
        <input
          bind:this={inputEl}
          bind:value={query}
          onkeydown={onKeydown}
          type="text"
          placeholder="Search repos, notes, files, actions…"
          aria-label="Search repositories, notes, organizations, files and actions"
          autocomplete="off"
          spellcheck="false"
        />
        <kbd class="hf-kbd">Esc</kbd>
      </div>
      <div class="hf-palette-results" bind:this={listEl}>
        {#if loadError}
          <p class="hf-palette-msg">Search index unavailable.</p>
        {:else if query.trim() && flat.length === 0}
          <p class="hf-palette-msg">No matches for “{query}”.</p>
        {:else}
          {#each groups as group (group.kind)}
            <!-- Group labels inside the dialog, not sections of the page: as headings they
                 injected h4s into whatever page the palette was opened on. The list of
                 buttons carries the name instead. -->
            <div class="hf-palette-group" role="group" aria-label={group.label}>
              <p class="hf-palette-grouphead" aria-hidden="true">{group.label}</p>
              {#each group.items as r (r.doc.url)}
                {@const idx = flat.indexOf(r)}
                <button type="button" class="hf-palette-item" class:is-active={idx === active} onclick={() => run(r.doc)} onmousemove={() => (active = idx)}>
                  <svg class="hf-i"><use href={KIND_ICON[r.doc.kind]} /></svg>
                  <span class="hf-palette-item-text"><strong>{r.doc.title}</strong>{#if r.doc.detail}<small>{r.doc.detail}</small>{/if}</span>
                  <kbd class="hf-kbd">↵</kbd>
                </button>
              {/each}
            </div>
          {/each}
        {/if}
      </div>
      <div class="hf-palette-foot">
        <span><kbd class="hf-kbd">↑</kbd><kbd class="hf-kbd">↓</kbd> navigate</span>
        <span><kbd class="hf-kbd">↵</kbd> open</span>
        <span class="hf-spacer"></span>
        <span>{flat.length} result{flat.length === 1 ? '' : 's'}</span>
      </div>
    </div>
  </div>
{/if}
