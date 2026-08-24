/**
 * Per-repo insights (schema v5): monthly commit/contributor counts plus a sampled code-size
 * series over the default branch's history.
 *
 * Rules the implementation honours:
 *
 *  - **Deterministic.** Checkpoints are derived from the commit list (dates and order that
 *    are already in the artifact), never from `Date.now()` or the filesystem. The same repo
 *    at the same commits picks the same checkpoints and emits byte-identical output.
 *  - **Committed content only.** Git is read through `src/lib/ingest/git.ts` (`readBlobs`)
 *    and `src/lib/ingest/tree.ts` (`listTree`). Never the working tree.
 *  - **Bounded.** `commits` is free — it buckets the commit list already passed in, no extra
 *    git calls. `codeSize` runs at most `options.samples` checkpoints, each one exactly one
 *    `git ls-tree -r -l -t -z <commit>` plus at most one `git cat-file --batch` that reads at
 *    most `options.maxBytesPerSample` bytes of content. Nothing here scales with history
 *    length beyond that.
 *  - **A WIDER file population than language stats.** Vendored paths (`isVendoredPath` in
 *    `languages.ts`) and binary blobs (the first-8000-bytes NUL rule `looksBinary` in
 *    `git.ts`, the same one `tree.ts` classifies with) are skipped — but nothing else is.
 *    Prose files and files with no detected language are kept, where `languages` drops both
 *    (`countsTowardStats` in `languages.ts`), because this series measures the size of the
 *    tracked tree, not the language split. A checkpoint's `bytes` is therefore normally
 *    LARGER than `sum(Repo.languages[].bytes)` — on frznforge itself by about a third — and
 *    anything user-facing must say so rather than claim the two agree.
 *  - **Warnings carry `repo: null`.** `scanRepo` stamps the slug on, exactly as it does for
 *    every other extractor's warnings.
 *
 * WORST CASE COST (defaults `samples: 24`, `maxBytesPerSample: 20 MiB`): 24 checkpoints ×
 * (1 `ls-tree` process + 1 `cat-file --batch` process) = 48 git processes, ≤ 480 MiB read
 * from git in total and ≤ 20 MiB resident at any moment (one checkpoint's batch buffer is
 * released before the next checkpoint starts). A repo with ten thousand commits over twenty
 * years costs exactly the same as one with three hundred commits over two years.
 */
import type { CodeSizePoint, Commit, CommitPoint, RepoInsights, Warning } from '../data/schema';
import { looksBinary, readBlobs } from './git';
import { isVendoredPath } from './languages';
import { listTree } from './tree';

/**
 * Insights knobs, resolved from `ingest.insights` in the site config.
 *
 * Mirrors that config block one-for-one; `scanRepo` passes it straight through, defaulting to
 * `DEFAULT_INSIGHTS_OPTIONS` when the caller supplies none.
 */
export interface InsightsOptions {
  /** False ⇒ `computeInsights` returns `{ insights: null, warnings: [] }` immediately. */
  enabled: boolean;
  /** Maximum monthly code-size checkpoints (first and last month are always among them). */
  samples: number;
  /** Byte budget for line counting at one checkpoint; past it, that point gets `lines: null`. */
  maxBytesPerSample: number;
}

/** Matches the `ingest.insights` defaults in `src/lib/config/schema.ts`. */
export const DEFAULT_INSIGHTS_OPTIONS: InsightsOptions = {
  enabled: true,
  samples: 24,
  maxBytesPerSample: 20 * 1024 * 1024,
};

/**
 * Everything `computeInsights` gets from the scanner. Deliberately plain data the scanner
 * already holds — no re-reading of refs, no second commit load.
 */
export interface ComputeInsightsArgs {
  /**
   * Every commit in the artifact, keyed by sha — exactly `Repo.commits`.
   *
   * The two series read two different clocks on purpose: the commit/contributor buckets use
   * `Commit.authorDate` (who wrote code when), while the code-size checkpoints use
   * `Commit.commitDate` (when that tree landed on the branch). See `monthlyCheckpoints`.
   */
  commits: Record<string, Commit>;
  /**
   * Shas of the default branch, **newest first**, exactly `Branch.commits` for the default
   * branch (already truncated by `ingest.maxCommits` when that is set). `null` when the repo
   * is empty or has no default branch — the implementation then returns `insights: null`.
   *
   * This is the history insights describe: only the default branch, never all refs.
   */
  branchCommits: readonly string[] | null;
  /** Default branch head sha (`branchCommits[0]`), or `null` for an empty repo. */
  head: string | null;
  /** Resolved insights knobs; never undefined — the scanner defaults them. */
  options: InsightsOptions;
}

/** What the scanner splices into the `Repo` and its warning list. */
export interface ComputeInsightsResult {
  /** `null` for an empty repo or when `options.enabled` is false. */
  insights: RepoInsights | null;
  /** Warnings with `repo: null`; `scanRepo` stamps the slug. Only `insights-approximate` today. */
  warnings: Warning[];
}

/* ---- month arithmetic ---------------------------------------------------- */

/** `YYYY-MM` of an ISO-8601 UTC instant (`authorDate` is already normalised to `…Z`). */
function monthOf(isoUtc: string): string {
  return isoUtc.slice(0, 7);
}

/** `YYYY-MM` → months since year 0, so gaps can be filled by counting. */
function monthIndex(month: string): number {
  return Number.parseInt(month.slice(0, 4), 10) * 12 + (Number.parseInt(month.slice(5, 7), 10) - 1);
}

/** Inverse of `monthIndex`. */
function monthFromIndex(index: number): string {
  const year = Math.floor(index / 12);
  const month = (index % 12) + 1;
  return `${String(year).padStart(4, '0')}-${String(month).padStart(2, '0')}`;
}

/**
 * Lines in a text blob, counted the way an editor does — and, crucially, the way the rest of
 * the site does.
 *
 * A trailing newline closes the last line rather than opening a new one, and a file that ends
 * without one still ends in a line. Counting raw newlines instead undercounts by exactly one
 * for every file with no final newline, which made a blob page print "2 lines" while insights
 * counted it as 1.
 *
 * This is `countLines()` from `src/lib/highlight.ts` transposed to bytes. It is replicated
 * rather than imported because that module pulls in shiki, which has no business inside
 * ingest — keep the two rules in step.
 */
function countLines(buf: Buffer): number {
  if (buf.length === 0) return 0;
  let count = 0;
  let at = -1;
  for (;;) {
    at = buf.indexOf(0x0a, at + 1);
    if (at === -1) break;
    count++;
  }
  return buf[buf.length - 1] === 0x0a ? count : count + 1;
}

/**
 * Thin `items` down to at most `max` entries, evenly spaced, always keeping the first and the
 * last. Indices are computed by rounding, so the choice depends only on the list length —
 * two runs over the same history pick the same entries.
 */
function pickEvenly<T>(items: readonly T[], max: number): T[] {
  if (items.length === 0) return [];
  if (max >= items.length) return items.slice();
  // A cap of one cannot hold both ends; keep the newest, which is the checkpoint that matches
  // the repo's current tree.
  if (max <= 1) return [items[items.length - 1]!];
  const picked: T[] = [];
  let previous = -1;
  for (let i = 0; i < max; i++) {
    const index = Math.round((i * (items.length - 1)) / (max - 1));
    if (index === previous) continue; // rounding collision; indices are non-decreasing
    previous = index;
    picked.push(items[index]!);
  }
  return picked;
}

/* ---- the two series ------------------------------------------------------ */

/**
 * Exact monthly commit and contributor counts over `branchCommits`, oldest month first.
 *
 * Months inside the span with no commits are emitted with zeros rather than omitted, so a
 * chart drawn straight from this array shows quiet periods as quiet instead of closing the
 * gap and implying steady activity.
 */
function bucketCommits(commits: Record<string, Commit>, branchCommits: readonly string[]): CommitPoint[] {
  const perMonth = new Map<string, { commits: number; emails: Set<string> }>();
  for (const sha of branchCommits) {
    const commit = commits[sha];
    if (!commit) continue;
    const month = monthOf(commit.authorDate);
    let bucket = perMonth.get(month);
    if (!bucket) {
      bucket = { commits: 0, emails: new Set<string>() };
      perMonth.set(month, bucket);
    }
    bucket.commits++;
    bucket.emails.add(commit.author.email.trim().toLowerCase());
  }
  if (perMonth.size === 0) return [];
  const present = Array.from(perMonth.keys()).map(monthIndex);
  const first = Math.min(...present);
  const last = Math.max(...present);
  const points: CommitPoint[] = [];
  for (let i = first; i <= last; i++) {
    const month = monthFromIndex(i);
    const bucket = perMonth.get(month);
    points.push({
      month,
      commits: bucket?.commits ?? 0,
      contributors: bucket?.emails.size ?? 0,
    });
  }
  return points;
}

/** One monthly checkpoint: the commit whose tree gets measured, and the month it lands in. */
interface Checkpoint {
  month: string;
  sha: string;
}

/**
 * One checkpoint per month, oldest month first, guaranteed to be in true history order.
 *
 * Two decisions here, both because this series measures **trees**, not authorship:
 *
 *  - **Bucketed by `commitDate`, not `authorDate`.** A commit's tree is the state of the
 *    branch at the point that commit was *applied*. `authorDate` says when the patch was
 *    written, which a rebase or a cherry-pick leaves in the past while the tree it produces
 *    is brand new. Bucketing a replayed commit by `authorDate` plots a tree state in a month
 *    where it never existed. (`bucketCommits` keeps `authorDate` — for "who wrote code when"
 *    it is the right clock.)
 *  - **Ranked by history position, not by date.** Within a month the commit closest to the
 *    branch head wins (`branchCommits` is newest first, so the lowest index), and months
 *    whose pick sits *newer* in history than a later month's pick are dropped rather than
 *    plotted out of order. Months newer than the head's own month are dropped for the same
 *    reason: nothing on this branch is newer than its head.
 *
 * The result is monotone by construction — walk the returned array and the history index
 * strictly decreases — so the code-size series can never run backwards in time. The head is
 * always the last checkpoint, so the newest point is always the repo as it stands.
 *
 * Both inputs are already in the artifact, so the choice is reproducible without touching a
 * clock or the filesystem.
 */
function monthlyCheckpoints(commits: Record<string, Commit>, branchCommits: readonly string[]): Checkpoint[] {
  const best = new Map<string, { sha: string; index: number }>();
  /** Month of the commit closest to the head — the newest month any tree may claim. */
  let headMonth: string | null = null;
  for (let index = 0; index < branchCommits.length; index++) {
    const sha = branchCommits[index]!;
    const commit = commits[sha];
    if (!commit) continue;
    const month = monthOf(commit.commitDate);
    if (headMonth === null) headMonth = month;
    const current = best.get(month);
    if (!current || index < current.index) best.set(month, { sha, index });
  }
  if (headMonth === null) return [];
  const headMonthIndex = monthIndex(headMonth);

  const kept: Checkpoint[] = [];
  let newestKeptIndex = -1;
  const newestFirst = Array.from(best.entries())
    .filter(([month]) => monthIndex(month) <= headMonthIndex)
    .sort((a, b) => monthIndex(b[0]) - monthIndex(a[0]));
  for (const [month, pick] of newestFirst) {
    // Walking back in time, each older month must sit further from the head. A pick that is
    // closer to the head than a *later* month's pick would draw the series backwards.
    if (pick.index <= newestKeptIndex) continue;
    newestKeptIndex = pick.index;
    kept.push({ month, sha: pick.sha });
  }
  return kept.reverse();
}

/** Result of measuring one checkpoint's tree. */
interface Measurement {
  bytes: number;
  lines: number | null;
}

/**
 * Measure the tracked code at one commit.
 *
 * One `ls-tree` lists the tree; candidate paths are its blobs and symlinks minus vendored
 * locations. Content is then read in a single `cat-file --batch` over the candidates in path
 * order, stopping before the read would exceed `maxBytes`. Blobs that were read are
 * classified with `looksBinary` and excluded from `bytes` when binary, and their newlines are
 * summed into `lines`.
 *
 * When the budget cannot cover every candidate the checkpoint is *approximate*: the unread
 * blobs contribute their `ls-tree` size to `bytes` without a binary check (so `bytes` can be
 * inflated by binaries), and `lines` is `null` because a partial newline count would be a
 * lie. The caller turns that into `approximate: true` plus an `insights-approximate` warning.
 */
async function measureCheckpoint(repoPath: string, sha: string, maxBytes: number): Promise<Measurement> {
  const entries = (await listTree(repoPath, sha)).filter(
    (e) => (e.type === 'blob' || e.type === 'symlink') && !isVendoredPath(e.path),
  );

  // Split candidates into "we can afford to read this" and "we cannot", in path order. A sha
  // that appears at several paths is read once and costs its bytes once.
  const readable: typeof entries = [];
  const unread: typeof entries = [];
  const charged = new Set<string>();
  let budget = 0;
  for (const entry of entries) {
    const size = entry.size ?? 0;
    if (charged.has(entry.sha)) {
      readable.push(entry);
      continue;
    }
    if (budget + size <= maxBytes) {
      charged.add(entry.sha);
      budget += size;
      readable.push(entry);
    } else {
      unread.push(entry);
    }
  }

  const contents = await readBlobs(repoPath, charged);

  let bytes = 0;
  let lines = 0;
  for (const entry of readable) {
    const content = contents.get(entry.sha);
    // A symlink's "blob" is its target path; an unreadable sha (submodule gitlink slipping
    // through, corrupt object) contributes nothing rather than throwing.
    if (content === undefined) continue;
    if (looksBinary(content)) continue;
    bytes += entry.size ?? 0;
    lines += countLines(content);
  }

  if (unread.length === 0) return { bytes, lines };
  for (const entry of unread) bytes += entry.size ?? 0;
  return { bytes, lines: null };
}

/**
 * Compute the insights series for one repository.
 *
 * @param repoPath Absolute path to the git repository (a checkout or a bare mirror), the same
 *   value every other extractor in `src/lib/ingest/` takes as its first argument.
 * @param args See `ComputeInsightsArgs`.
 */
export async function computeInsights(
  repoPath: string,
  args: ComputeInsightsArgs,
): Promise<ComputeInsightsResult> {
  const { commits, branchCommits, head, options } = args;
  if (!options.enabled) return { insights: null, warnings: [] };
  if (!branchCommits || branchCommits.length === 0 || head === null) return { insights: null, warnings: [] };

  const commitPoints = bucketCommits(commits, branchCommits);
  if (commitPoints.length === 0) return { insights: null, warnings: [] };

  const candidates = monthlyCheckpoints(commits, branchCommits);
  const chosen = pickEvenly(candidates, Math.max(1, Math.trunc(options.samples)));

  const codeSize: CodeSizePoint[] = [];
  const approximateMonths: string[] = [];
  for (const checkpoint of chosen) {
    const measured = await measureCheckpoint(repoPath, checkpoint.sha, options.maxBytesPerSample);
    if (measured.lines === null) approximateMonths.push(checkpoint.month);
    codeSize.push({ month: checkpoint.month, bytes: measured.bytes, lines: measured.lines });
  }

  const approximate = approximateMonths.length > 0;
  const warnings: Warning[] = [];
  if (approximate) {
    const shown = approximateMonths.slice(0, 5).join(', ');
    const rest = approximateMonths.length > 5 ? ` and ${approximateMonths.length - 5} more` : '';
    warnings.push({
      code: 'insights-approximate',
      repo: null,
      message:
        `${approximateMonths.length} of ${codeSize.length} code-size checkpoints (${shown}${rest}) hold more ` +
        `content than ingest.insights.maxBytesPerSample (${options.maxBytesPerSample} bytes), so their line ` +
        `counts were skipped and their byte totals may include binary files (the blobs past the budget were ` +
        `never read, so they could not be classified)`,
    });
  }

  return {
    insights: {
      commits: commitPoints,
      codeSize,
      // "Sampled" means checkpoints were thinned — fewer months measured than months that
      // actually have commits. Zero-filled quiet months in `commits` never had a tree of
      // their own to measure, so they do not count here.
      sampled: codeSize.length < candidates.length,
      sampleCount: codeSize.length,
      approximate,
    },
    warnings,
  };
}
