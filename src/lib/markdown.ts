/**
 * Render markdown strings (READMEs, release notes) to HTML at build time.
 * Content is the site owner's own — no sanitisation pass (same trust model as the
 * profile.md content collection). Relative links/images are left as-is for now; Phase 3
 * rewrites them to file routes.
 */
import { Marked } from 'marked';

const marked = new Marked({ gfm: true, breaks: false });

export function renderMarkdown(src: string): string {
  return marked.parse(src, { async: false }) as string;
}

/** Heuristic: treat .md/.markdown/.mdown and extensionless README as markdown. */
export function isMarkdownPath(path: string): boolean {
  return /(\.(md|markdown|mdown|mkd)$)|(^|\/)readme$/i.test(path);
}
