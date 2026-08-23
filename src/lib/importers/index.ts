/**
 * Importer registry: config source → the provider client that can describe it.
 *
 * Ingest only ever needs `createImporter()`; the individual provider classes are exported
 * for tests that want to drive one directly with a recorded-fixture `fetchImpl`.
 */
import type { RepoSourceConfig } from '../config/schema';
import { ForgejoImporter } from './forgejo';
import { GiteaImporter } from './gitea';
import { GithubImporter } from './github';
import { GitlabImporter } from './gitlab';
import { resolveToken, type Importer, type ImporterContext } from './types';

export * from './types';
export { GithubImporter } from './github';
export { GitlabImporter } from './gitlab';
export { GiteaImporter } from './gitea';
export { ForgejoImporter } from './forgejo';

/**
 * Build the importer for a configured source, or `null` for `type: 'local'` (nothing to
 * import — the scanner reads the directory directly).
 *
 * When `ctx.token` is not given it is resolved from the environment via `resolveToken()`;
 * pass `token: null` explicitly to force an anonymous client.
 */
export function createImporter(source: RepoSourceConfig, ctx: ImporterContext = {}): Importer | null {
  if (source.type === 'local') return null;
  const resolved: ImporterContext = {
    ...ctx,
    token: ctx.token !== undefined ? ctx.token : resolveToken(source),
  };
  switch (source.type) {
    case 'github':
      return new GithubImporter(source, resolved);
    case 'gitlab':
      return new GitlabImporter(source, resolved);
    case 'gitea':
      return new GiteaImporter(source, resolved);
    case 'forgejo':
      return new ForgejoImporter(source, resolved);
  }
}
