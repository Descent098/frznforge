<script lang="ts">
  import type { RepoSummary } from '../lib/listing';
  import { withBase } from '../lib/base';
  import { DEFAULT_HEAT, type HeatThresholds, heatFor, relativeTime } from '../lib/format';

  interface Props {
    repo: RepoSummary;
    /** Reference time in ms (build time), so server + client render identically. */
    now: number;
    /** Base path for tag filter links. */
    tagHref?: (tag: string) => string;
    /** Recency-accent day boundaries (`theme.heat`), passed down like `now`. */
    heatDays?: HeatThresholds;
  }
  let { repo, now, tagHref = (t) => withBase(`/repos/?tag=${encodeURIComponent(t)}`), heatDays = DEFAULT_HEAT }: Props = $props();

  const nowDate = $derived(new Date(now));
  const heat = $derived(heatFor(repo.updatedAt, nowDate, heatDays));
  const langs = $derived(repo.languages.slice(0, 3));
</script>

<article class="hf-repo-card heat-{heat}" data-slug={repo.slug}>
  <div class="hf-repo-top">
    <span class="hf-repo-icon"><svg class="hf-i"><use href="#i-repo" /></svg></span>
    <a class="hf-repo-name" href={withBase(`/repos/${repo.slug}/`)}>{repo.name}</a>
    {#if repo.template}<span class="hf-tag hf-tag--template">Template</span>{/if}
  </div>
  <p class="hf-repo-desc">{repo.description ?? (repo.empty ? 'Empty repository.' : 'No description.')}</p>
  <div class="hf-repo-tags">
    {#each repo.tags as tag}<a class="hf-tag" href={tagHref(tag)}>{tag}</a>{/each}
  </div>
  <div class="hf-repo-foot">
    <span class="hf-langs">
      {#each langs as l}
        <span><i class="hf-lang-dot" style:background={l.color ?? 'var(--hf-lang-other)'}></i>{l.name} {l.percent}%</span>
      {:else}
        <span class="hf-muted">{repo.empty ? 'No commits' : 'No code detected'}</span>
      {/each}
    </span>
    <span class="hf-age t-{heat}" title={repo.updatedAt ?? ''}>{repo.updatedAt ? relativeTime(repo.updatedAt, nowDate) : 'never'}</span>
  </div>
</article>
