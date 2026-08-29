/**
 * Brand asset guards (0.2.0). The old logo.png was a 1254×1254, 1.2 MB illustration served
 * as a favicon on every page load; the mark is now rendered from the vector master
 * `src/assets/logo.svg` by `scripts/render-logo.ts`. These tests keep the regression out:
 * the shipped icons must stay small and stay real image files of the right format.
 */
import fs from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';

const ROOT = path.resolve(import.meta.dirname, '..', '..');
const read = (...p: string[]) => fs.readFileSync(path.join(ROOT, ...p));

describe('brand assets', () => {
  it('ships a vector master', () => {
    const svg = read('src', 'assets', 'logo.svg').toString('utf8');
    expect(svg).toContain('<svg');
    expect(svg).toContain('viewBox="0 0 128 128"');
  });

  it('logo.png is a real PNG and small enough to be a favicon', () => {
    const png = read('public', 'logo.png');
    expect(png.subarray(0, 8)).toEqual(Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]));
    expect(png.length).toBeLessThan(100 * 1024); // the 1.2 MB favicon must not return
  });

  it('favicon.ico is a real ICO with the three classic sizes', () => {
    const ico = read('public', 'favicon.ico');
    expect(ico.readUInt16LE(0)).toBe(0); // reserved
    expect(ico.readUInt16LE(2)).toBe(1); // type: icon
    expect(ico.readUInt16LE(4)).toBe(3); // 16 + 32 + 48
    expect([ico.readUInt8(6), ico.readUInt8(22), ico.readUInt8(38)].sort((a, b) => a - b)).toEqual([16, 32, 48]);
    expect(ico.length).toBeLessThan(50 * 1024);
  });
});
