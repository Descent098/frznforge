/**
 * Config loader: validate, apply defaults, resolve paths. Both ingest and the Astro site use
 * `loadConfig()` so defaults and path resolution happen in exactly one place.
 */
import { createHash } from 'node:crypto';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import userConfig from '../../../frznforge.config';
import {
  FrznforgeConfigSchema,
  normalizeBase,
  type FrznforgeConfig,
  type FrznforgeConfigInput,
  type RemoteRepoSourceConfig,
  type RepoSourceConfig,
} from './schema';

export * from './schema';

/** Absolute path of the project root (directory containing frznforge.config.ts). */
export const PROJECT_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..', '..');

export interface ResolvedConfig extends FrznforgeConfig {
  /** Absolute project root. */
  root: string;
  /** Absolute ingest output directory. */
  outDir: string;
  /** Absolute mirror/response cache directory for remote sources. */
  cacheDir: string;
  /** Absolute path to the profile markdown file. */
  profilePath: string;
  /**
   * Absolute notes folder (`notes.dir`). May not exist — ingest then emits no notes, and warns
   * `notes-dir-missing` only when `notesConfigured` says the folder was actually asked for.
   */
  notesDir: string;
  /**
   * Whether the user config declared a `notes` block at all.
   *
   * `notes.dir` has a default, so `notesDir` always points somewhere; without this flag a site
   * that never opted into notes would raise `notes-dir-missing` on every single build and show
   * a permanent "1 ingest warning" in its footer. A warning has to mean "something you asked
   * for did not happen", so the missing-folder warning is reserved for the case where the user
   * configured notes and the path is wrong.
   */
  notesConfigured: boolean;
  /**
   * Absolute organization-profile folder (`content.orgs`). May not exist — orgs then render
   * from config alone. Named `orgsDir` rather than `contentOrgs` because that is what every
   * consumer calls it.
   */
  orgsDir: string;
  /**
   * Repos with an absolute path to scan. For `type: 'local'` that is the configured
   * directory; for remote sources it is the mirror clone inside `cacheDir`, which may not
   * exist yet — the importer creates it before the scanner runs. Downstream code therefore
   * has one shape for every source.
   */
  repos: Array<RepoSourceConfig & { absPath: string }>;
}

/**
 * Turn a host URL into one filesystem-safe path segment: scheme and any trailing slash are
 * dropped, everything outside `[a-z0-9.-]` (`:` and `/` in particular — illegal on Windows)
 * becomes `-`, runs collapse, and the result is lower-cased.
 *
 * `https://api.github.com` → `api.github.com`; `https://gitea.example.com:3000/git` →
 * `gitea.example.com-3000-git`. Not reversible: it only has to be stable and unique enough
 * to keep two hosts apart.
 */
export function hostSlug(host: string): string {
  const bare = host
    .trim()
    .toLowerCase()
    .replace(/^[a-z][a-z0-9+.-]*:\/\//, '')
    .replace(/\/+$/, '');
  const slug = bare.replace(/[^a-z0-9.-]+/g, '-').replace(/-+/g, '-').replace(/^[-.]+|[-.]+$/g, '');
  return slug || 'host';
}

/**
 * Basenames Windows refuses to create, with or without an extension. A path segment that
 * sanitises down to one of these is prefixed so the mirror is still clonable there.
 */
const WINDOWS_RESERVED = new Set([
  'con', 'prn', 'aux', 'nul',
  ...Array.from({ length: 9 }, (_, i) => `com${i + 1}`),
  ...Array.from({ length: 9 }, (_, i) => `lpt${i + 1}`),
]);

/** Longest a single sanitised segment may be, to keep deep namespaces under Windows' MAX_PATH. */
const MAX_SEGMENT = 48;

/** Same sanitising as `hostSlug`, applied to one owner/repo/namespace segment. */
function pathSegment(value: string): string {
  const slug = value
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9._-]+/g, '-')
    .replace(/-+/g, '-')
    .replace(/^[-.]+|[-.]+$/g, '')
    .slice(0, MAX_SEGMENT)
    .replace(/[-.]+$/g, '');
  if (!slug) return 'repo';
  return WINDOWS_RESERVED.has(slug) ? `_${slug}` : slug;
}

/**
 * The exact identity of a remote source, as one string. Not sanitised and not for display —
 * it is only ever hashed, so two sources that differ anywhere differ here too.
 */
function sourceIdentity(source: RemoteRepoSourceConfig): string {
  const id = source.type === 'gitlab' ? source.project : `${source.owner}/${source.repo}`;
  return [source.type, source.host.replace(/\/+$/, '').toLowerCase(), id].join('\u0000');
}

/**
 * Where a remote source is mirror-cloned:
 * `<cacheDir>/<provider>/<host-slug>/<owner>/<repo>-<digest>.git`, with GitLab's namespaced
 * project path expanded into nested directories (`group/sub/proj` → `group/sub/proj-<d>.git`).
 *
 * The `<digest>` is 8 hex characters of sha256 over the source's *unsanitised* identity
 * (provider, host, owner/repo or project). It is what makes the mapping injective:
 * `pathSegment` is deliberately lossy — case is folded and every non-ASCII name collapses to
 * the same slug — so without it two unrelated repos could share one mirror and each would be
 * published with the other's git content. It also lifts reserved Windows basenames
 * (`con.git`, `aux.git`) out of the way.
 *
 * Returns `null` for local sources, which have no cache entry. `cacheDir` should already be
 * absolute (`ResolvedConfig.cacheDir`); the result is built with `path.join`, so it is
 * Windows-safe.
 */
export function cachePathFor(cacheDir: string, source: RepoSourceConfig): string | null {
  if (source.type === 'local') return null;
  const segments =
    source.type === 'gitlab'
      ? source.project.split('/').filter(Boolean).map(pathSegment)
      : [pathSegment(source.owner), pathSegment(source.repo)];
  if (segments.length === 0) segments.push('repo');
  const last = segments.pop()!;
  const digest = createHash('sha256').update(sourceIdentity(source), 'utf8').digest('hex').slice(0, 8);
  return path.join(cacheDir, source.type, hostSlug(source.host), ...segments, `${last}-${digest}.git`);
}

/**
 * The repo name a remote source carries in the config, before any provider call: the `repo`
 * field, or the last segment of a GitLab `project` path. Used for the default slug and
 * display name so neither depends on the lossy cache directory name.
 */
export function sourceRepoName(source: RemoteRepoSourceConfig): string {
  if (source.type !== 'gitlab') return source.repo;
  const segments = source.project.split('/').filter(Boolean);
  return segments[segments.length - 1] ?? source.project;
}

/**
 * Validate, apply defaults, and resolve paths against the project root.
 * `FRZNFORGE_OUT_DIR` / `FRZNFORGE_CACHE_DIR` (env) override `ingest.outDir` and
 * `ingest.cacheDir`, and `FRZNFORGE_BASE` (0.2.0) overrides `site.base` — all three are
 * used by the e2e tests to build the site against fixture artifacts (and, for the base,
 * under a sub-path) without touching the real one. `notes.dir` and `content.orgs` have no
 * env override: nothing needs one (the tests point the config itself at a fixture folder),
 * and every extra env knob is another way for a build to differ from the config that is
 * checked in.
 */
export function resolveConfig(input: FrznforgeConfigInput, root: string = PROJECT_ROOT): ResolvedConfig {
  const cfg = FrznforgeConfigSchema.parse(input);
  const env = typeof process !== 'undefined' ? process.env : ({} as NodeJS.ProcessEnv);
  const cacheDir = path.resolve(root, env.FRZNFORGE_CACHE_DIR || cfg.ingest.cacheDir);
  if (env.FRZNFORGE_BASE !== undefined) {
    const base = normalizeBase(env.FRZNFORGE_BASE);
    cfg.site = { ...cfg.site, ...(base === undefined ? { base: undefined } : { base }) };
  }
  return {
    ...cfg,
    root,
    outDir: path.resolve(root, env.FRZNFORGE_OUT_DIR || cfg.ingest.outDir),
    cacheDir,
    profilePath: path.resolve(root, cfg.owner.profile),
    notesDir: path.resolve(root, cfg.notes.dir),
    // Read from the *raw* input: zod's `.prefault({})` makes an omitted `notes` block
    // indistinguishable from `notes: {}` once parsed.
    notesConfigured: input.notes !== undefined,
    orgsDir: path.resolve(root, cfg.content.orgs),
    repos: cfg.repos.map((r) => ({
      ...r,
      absPath: r.type === 'local' ? path.resolve(root, r.path) : cachePathFor(cacheDir, r)!,
    })),
  };
}

/** Load and resolve the project's frznforge.config.ts. */
export async function loadConfig(root: string = PROJECT_ROOT): Promise<ResolvedConfig> {
  return resolveConfig(userConfig, root);
}
