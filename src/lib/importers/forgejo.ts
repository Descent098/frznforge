/**
 * Forgejo importer. Forgejo speaks the Gitea REST API, so all of the behaviour lives in
 * {@link GiteaImporter}; this subclass exists to give Forgejo its own name and its own
 * narrowed source type, and it reports `provider: 'forgejo'` because that is what the source
 * it is constructed with says.
 *
 * Deliberately not a copy of the Gitea logic: the two APIs drift in fields, not in shape, and
 * those differences are feature-detected in one place (see `gitea.ts`).
 */
import type { ForgejoSourceConfig } from '../config/schema';
import { GiteaImporter } from './gitea';
import type { ImporterContext } from './types';

export class ForgejoImporter extends GiteaImporter {
  /** Narrowed from the base's `'gitea' | 'forgejo'`; the value still comes from `source.type`. */
  declare readonly provider: 'forgejo';

  constructor(source: ForgejoSourceConfig, ctx: ImporterContext = {}) {
    super(source, ctx);
  }
}
