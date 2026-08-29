#!/usr/bin/env tsx
/**
 * Regenerate `public/logo.png` (512px) and `public/favicon.ico` (16 + 32 + 48, PNG-encoded
 * entries) from the vector master `src/assets/logo.svg`. Run after editing the master:
 *
 *   npx tsx scripts/render-logo.ts
 *
 * Rendering goes through Playwright's chromium (already a devDependency for e2e) so the
 * project takes on no image toolchain; the .ico is packed by hand — its format is a 6-byte
 * header, one 16-byte directory entry per image, then the image blobs, and PNG-compressed
 * entries have been valid since Windows Vista and are what every browser reads.
 */
import fs from 'node:fs';
import path from 'node:path';
import { chromium, type Page } from '@playwright/test';

const ROOT = path.resolve(import.meta.dirname, '..');
const svg = fs.readFileSync(path.join(ROOT, 'src', 'assets', 'logo.svg'), 'utf8');

async function renderPng(page: Page, size: number): Promise<Buffer> {
  await page.setViewportSize({ width: size + 10, height: size + 10 });
  await page.setContent(
    '<!doctype html><style>html,body{margin:0;background:transparent}</style>' +
      svg.replace('<svg ', `<svg width="${size}" height="${size}" `),
  );
  return page.locator('svg').screenshot({ omitBackground: true, type: 'png' });
}

function packIco(images: Array<{ size: number; png: Buffer }>): Buffer {
  const header = Buffer.alloc(6);
  header.writeUInt16LE(0, 0); // reserved
  header.writeUInt16LE(1, 2); // type: icon
  header.writeUInt16LE(images.length, 4);
  const entries: Buffer[] = [];
  let offset = 6 + images.length * 16;
  for (const { size, png } of images) {
    const e = Buffer.alloc(16);
    e.writeUInt8(size >= 256 ? 0 : size, 0); // width (0 = 256)
    e.writeUInt8(size >= 256 ? 0 : size, 1); // height
    e.writeUInt16LE(1, 4); // colour planes
    e.writeUInt16LE(32, 6); // bits per pixel
    e.writeUInt32LE(png.length, 8);
    e.writeUInt32LE(offset, 12);
    entries.push(e);
    offset += png.length;
  }
  return Buffer.concat([header, ...entries, ...images.map((i) => i.png)]);
}

const browser = await chromium.launch();
const page = await browser.newPage();

const logo = await renderPng(page, 512);
fs.writeFileSync(path.join(ROOT, 'public', 'logo.png'), logo);
console.log(`public/logo.png     ${(logo.length / 1024).toFixed(1)} kB (512px)`);

const icoImages: Array<{ size: number; png: Buffer }> = [];
for (const size of [16, 32, 48]) icoImages.push({ size, png: await renderPng(page, size) });
const ico = packIco(icoImages);
fs.writeFileSync(path.join(ROOT, 'public', 'favicon.ico'), ico);
console.log(`public/favicon.ico  ${(ico.length / 1024).toFixed(1)} kB (16+32+48)`);

await browser.close();
