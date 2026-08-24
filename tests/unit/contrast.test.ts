/**
 * WCAG 1.4.3 (AA) contrast floor for the LIGHT palettes, read straight out of the stylesheet.
 *
 * Why this exists as a unit test rather than only in the browser: the light palettes shipped
 * with every brand hue failing AA — ember 2.7:1, amber 2.2:1, ice 3.1:1, on every page type —
 * and nothing caught it for six phases. The reason is mundane and worth writing down:
 * chrome-launcher inherits the host OS colour scheme, so every Lighthouse run this project
 * ever did on a dark-mode machine rendered the DARK palette and never once looked at the
 * light one. A gate that only runs a browser can be fooled by the machine it runs on; the
 * token values in the CSS cannot.
 *
 * The e2e suite (`tests/e2e/a11y.spec.ts`) does the complementary half — it pins
 * `data-theme` explicitly and measures what the pages actually render, catching pairs no one
 * thought to list here.
 */
import fs from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';

const ROOT = path.resolve(import.meta.dirname, '..', '..');
const GLOBAL_CSS = fs.readFileSync(path.join(ROOT, 'src', 'styles', 'global.css'), 'utf8');
const REPO_CSS = fs.readFileSync(path.join(ROOT, 'src', 'styles', 'repo.css'), 'utf8');

/* ---- colour maths (the WCAG formulae, nothing more) ----------------------- */

type RGB = readonly [number, number, number];

function parseHex(value: string): RGB {
  const h = value.trim().replace('#', '');
  const full = h.length === 3 ? h.split('').map((c) => c + c).join('') : h;
  return [0, 2, 4].map((i) => Number.parseInt(full.slice(i, i + 2), 16)) as unknown as RGB;
}

/** `rgba(r, g, b, a)` → its channels plus alpha. */
function parseRgba(value: string): { rgb: RGB; alpha: number } {
  const m = /rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)(?:[,\s/]+([\d.]+))?\s*\)/.exec(value);
  if (!m) throw new Error(`not an rgb(a) value: ${value}`);
  return {
    rgb: [Number(m[1]), Number(m[2]), Number(m[3])] as unknown as RGB,
    alpha: m[4] === undefined ? 1 : Number(m[4]),
  };
}

/** Flatten a translucent colour onto an opaque one — what the browser paints. */
function composite(fg: RGB, alpha: number, bg: RGB): RGB {
  return fg.map((v, i) => alpha * v + (1 - alpha) * bg[i]!) as unknown as RGB;
}

function relativeLuminance(c: RGB): number {
  const chan = (v: number) => {
    const s = v / 255;
    return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * chan(c[0]) + 0.7152 * chan(c[1]) + 0.0722 * chan(c[2]);
}

export function contrast(a: RGB, b: RGB): number {
  const [l1, l2] = [relativeLuminance(a), relativeLuminance(b)];
  return (Math.max(l1, l2) + 0.05) / (Math.min(l1, l2) + 0.05);
}

/* ---- reading the tokens out of the stylesheet ----------------------------- */

/** Every `--hf-*` declaration inside the block that starts with `selector {`. */
function tokenBlock(css: string, selector: string): Map<string, string> {
  const start = css.indexOf(`${selector} {`);
  if (start === -1) throw new Error(`no such block: ${selector}`);
  const open = css.indexOf('{', start);
  const end = css.indexOf('\n}', open);
  const body = css.slice(open + 1, end);
  const tokens = new Map<string, string>();
  for (const m of body.matchAll(/(--hf-[\w-]+)\s*:\s*([^;]+);/g)) tokens.set(m[1]!, m[2]!.trim());
  return tokens;
}

/** A palette's light tokens: the frost block only overrides, so it layers on the base. */
function lightPalette(name: 'hearth' | 'frost'): Map<string, string> {
  const base = tokenBlock(GLOBAL_CSS, ':root');
  if (name === 'hearth') return base;
  for (const [k, v] of tokenBlock(GLOBAL_CSS, ':root[data-palette="frost"]')) base.set(k, v);
  return base;
}

/* ---- the pairs that have to hold ------------------------------------------
 * Each entry is "this ink, on these backgrounds". The background list is not decorative:
 * it is where that colour is actually painted, checked against the stylesheet.
 */

/** WCAG AA for text below 18.66px bold / 24px regular — which every one of these is. */
const AA = 4.5;

interface Ink {
  /** Token name, or a literal for the two hard-coded diff colours. */
  ink: string;
  /** Token names of opaque backgrounds it is painted on. */
  on: string[];
  /**
   * Translucent tint tokens it is painted on, each flattened over `--hf-surface` and
   * `--hf-canvas` — a chip's tint sits on a card or on the page, and both have to clear.
   */
  onTints?: string[];
  why: string;
}

const INKS: Ink[] = [
  {
    ink: '--hf-ice',
    on: ['--hf-surface', '--hf-surface-2', '--hf-canvas', '--hf-canvas-2'],
    onTints: ['--hf-ice-soft'],
    why: '.hf-sha, .hf-age.t-cold, .hf-path-crumb links, .hf-tag, .hf-more, .hf-sha-link code',
  },
  {
    ink: '--hf-ember',
    on: ['--hf-surface', '--hf-surface-2', '--hf-canvas', '--hf-canvas-2'],
    onTints: ['--hf-ember-soft'],
    why: '.hf-age.t-hot, .hf-tag.hf-release-latest, .hf-tagkind--annotated',
  },
  {
    ink: '--hf-amber',
    on: ['--hf-surface', '--hf-surface-2', '--hf-canvas', '--hf-canvas-2'],
    onTints: ['--hf-amber-soft'],
    why: '.hf-footer-warn, .hf-release-pre, .hf-age.t-warm, .hf-tag--template',
  },
  {
    ink: '--hf-text-2',
    on: ['--hf-surface', '--hf-surface-2', '--hf-canvas', '--hf-canvas-2'],
    why: 'body copy in .hf-md, .hf-commit-author, .hf-blob-meta',
  },
  {
    ink: '--hf-text-3',
    on: ['--hf-surface', '--hf-surface-2', '--hf-canvas', '--hf-canvas-2'],
    onTints: ['--hf-code-bg'],
    why: '.hf-muted, table headers, .hf-kpi-sub, inline <code> in prose',
  },
];

describe.each(['hearth', 'frost'] as const)('light palette: %s', (name) => {
  const tokens = lightPalette(name);
  const rgb = (token: string) => parseHex(tokens.get(token)!);

  for (const entry of INKS) {
    it(`${entry.ink} clears AA where it is used (${entry.why})`, () => {
      const fg = rgb(entry.ink);
      for (const bg of entry.on) {
        expect(`${entry.ink} on ${bg}: ${contrast(fg, rgb(bg)).toFixed(2)}`).toBe(
          `${entry.ink} on ${bg}: ${Math.max(contrast(fg, rgb(bg)), AA).toFixed(2)}`,
        );
      }
      for (const tint of entry.onTints ?? []) {
        const { rgb: tintRgb, alpha } = parseRgba(tokens.get(tint)!);
        for (const under of ['--hf-surface', '--hf-canvas']) {
          const flattened = composite(tintRgb, alpha, rgb(under));
          const got = contrast(fg, flattened);
          expect(`${entry.ink} on ${tint} over ${under}: ${got.toFixed(2)}`).toBe(
            `${entry.ink} on ${tint} over ${under}: ${Math.max(got, AA).toFixed(2)}`,
          );
        }
      }
    });
  }

  /**
   * The diff colours are literals in repo.css rather than tokens — they are github's, not the
   * palette's — but they are still 12px text on a card and still have to clear AA on both
   * palettes. They live in `.hf-commit-row` / `.hf-commit-files`, whose backgrounds are the
   * card surface, its hover shade, and (on the single-commit head) the canvas.
   */
  it('diff +/- colours clear AA on both palettes', () => {
    const adds = /\.hf-adds \{ color: (#[0-9a-f]{6});/.exec(REPO_CSS)?.[1];
    const dels = /\.hf-dels \{ color: (#[0-9a-f]{6});/.exec(REPO_CSS)?.[1];
    expect(adds, '.hf-adds colour not found in repo.css').toBeDefined();
    expect(dels, '.hf-dels colour not found in repo.css').toBeDefined();
    for (const literal of [adds!, dels!]) {
      for (const bg of ['--hf-surface', '--hf-surface-2', '--hf-canvas']) {
        const got = contrast(parseHex(literal), rgb(bg));
        expect(`${literal} on ${bg}: ${got.toFixed(2)}`).toBe(
          `${literal} on ${bg}: ${Math.max(got, AA).toFixed(2)}`,
        );
      }
    }
  });

  /**
   * The code gutter is a `::before`, which means no browser-based checker will ever look at
   * it — axe and Lighthouse both skip pseudo-element text. It is still text, and it is still
   * `--hf-text-3` behind an `opacity`, so it is checked here or nowhere.
   */
  it('code line numbers clear AA through their opacity', () => {
    const opacity = Number(
      /\.hf-code \.line::before \{[\s\S]*?opacity: ([\d.]+);/.exec(REPO_CSS)?.[1] ?? 'NaN',
    );
    expect(opacity, 'line-number opacity not found in repo.css').toBeGreaterThan(0);
    for (const bg of ['--hf-surface', '--hf-surface-2']) {
      const painted = composite(rgb('--hf-text-3'), opacity, rgb(bg));
      const got = contrast(painted, rgb(bg));
      expect(`gutter on ${bg}: ${got.toFixed(2)}`).toBe(`gutter on ${bg}: ${Math.max(got, AA).toFixed(2)}`);
    }
  });

  /**
   * `--hf-accent` is the colour a primary button is painted IN, with `--hf-on-accent` written
   * on top of it. Darkening the accent for contrast in one direction must not break it in the
   * other.
   */
  it('button text clears AA on the accent fill', () => {
    const accent = tokens.get('--hf-accent')!.replace(/var\((--hf-[\w-]+)\)/, (_, t: string) => tokens.get(t)!);
    const onAccent = tokens.get('--hf-on-accent')!.replace(/var\((--hf-[\w-]+)\)/, (_, t: string) => tokens.get(t)!);
    const got = contrast(parseHex(accent), parseHex(onAccent));
    expect(`on-accent on accent: ${got.toFixed(2)}`).toBe(`on-accent on accent: ${Math.max(got, AA).toFixed(2)}`);
  });
});

/**
 * The DARK palettes were tuned when they were written and pass today; this pins them so a
 * future "let's unify the tokens" change cannot quietly undo that.
 */
describe.each([
  ['hearth', ':root[data-theme="dark"]'],
  ['frost', ':root[data-palette="frost"][data-theme="dark"]'],
] as const)('dark palette: %s', (_name, selector) => {
  const tokens = tokenBlock(GLOBAL_CSS, ':root');
  for (const [k, v] of tokenBlock(GLOBAL_CSS, selector)) tokens.set(k, v);
  const rgb = (token: string) => parseHex(tokens.get(token)!);

  for (const entry of INKS) {
    it(`${entry.ink} clears AA on the dark surfaces`, () => {
      const fg = rgb(entry.ink);
      for (const bg of entry.on) {
        const got = contrast(fg, rgb(bg));
        expect(`${entry.ink} on ${bg}: ${got.toFixed(2)}`).toBe(
          `${entry.ink} on ${bg}: ${Math.max(got, AA).toFixed(2)}`,
        );
      }
      for (const tint of entry.onTints ?? []) {
        const { rgb: tintRgb, alpha } = parseRgba(tokens.get(tint)!);
        for (const under of ['--hf-surface', '--hf-canvas']) {
          const got = contrast(fg, composite(tintRgb, alpha, rgb(under)));
          expect(`${entry.ink} on ${tint} over ${under}: ${got.toFixed(2)}`).toBe(
            `${entry.ink} on ${tint} over ${under}: ${Math.max(got, AA).toFixed(2)}`,
          );
        }
      }
    });
  }

  it('code line numbers clear AA through their opacity', () => {
    const opacity = Number(
      /\.hf-code \.line::before \{[\s\S]*?opacity: ([\d.]+);/.exec(REPO_CSS)?.[1] ?? 'NaN',
    );
    for (const bg of ['--hf-surface', '--hf-surface-2']) {
      const painted = composite(rgb('--hf-text-3'), opacity, rgb(bg));
      const got = contrast(painted, rgb(bg));
      expect(`gutter on ${bg}: ${got.toFixed(2)}`).toBe(`gutter on ${bg}: ${Math.max(got, AA).toFixed(2)}`);
    }
  });
});
