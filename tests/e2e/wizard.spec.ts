/**
 * The init wizard driven through a real browser (0.2.0): the whole-config editor and the
 * profile editor, end to end against the real `runWebInit` server on an ephemeral port.
 *
 * Serial on purpose — the tests share one server and drive one session through its life:
 * refuse a tokenless visit, edit settings, edit the profile, remove a source, then Done. The
 * unit tests (`tests/unit/web-init.test.ts`) own the API contract; this spec owns "the page
 * actually wires those endpoints to inputs a person can use".
 */
import { expect, test } from '@playwright/test';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import type { Io } from '../../scripts/cli';
import { runWebInit } from '../../scripts/lib/web-init';

const CONFIG = `// hand-written config, full of comments worth keeping
function defineConfig(c: unknown) { return c; }

export default defineConfig({
  site: {
    title: 'My Forge', // shown in the sidebar
  },
  owner: { name: 'Kieran Wood', handle: 'kieran' },
  repos: [
    { type: 'local', path: '.', slug: 'frznforge' },
    { type: 'github', owner: 'me', repo: 'old' },
  ],
  ingest: {
    maxBlobBytes: 512 * 1024, // half a meg, kept as an expression
  },
});
`;

const PROFILE = '---\ntitle: Me\n---\n# Hi\n\nold body\n';

test.describe.configure({ mode: 'serial' });

let tmp: string;
let wizardUrl: string;
let exit: Promise<number>;
let out: string[];

const configFile = (): string => path.join(tmp, 'frznforge.config.ts');
const profileFile = (): string => path.join(tmp, 'content', 'profile.md');

test.beforeAll(async () => {
  tmp = await fs.mkdtemp(path.join(os.tmpdir(), 'frznforge-wizard-'));
  await fs.writeFile(configFile(), CONFIG, 'utf8');
  await fs.mkdir(path.dirname(profileFile()), { recursive: true });
  await fs.writeFile(profileFile(), PROFILE, 'utf8');
  out = [];
  const io: Io = {
    log: (line) => out.push(line),
    error: (line) => out.push(line),
    isTty: false,
    env: {},
    cwd: tmp,
  };
  exit = runWebInit({ noOpen: true, port: 0, io });
  exit.catch(() => undefined);
  for (let waited = 0; waited < 8000 && !out.some((l) => l.includes('http://127.0.0.1:')); waited += 25) {
    await new Promise((r) => setTimeout(r, 25));
  }
  const line = out.find((l) => l.includes('http://127.0.0.1:'));
  if (!line) throw new Error(`the wizard never printed its URL:\n${out.join('\n')}`);
  wizardUrl = line.trim();
});

test.afterAll(async () => {
  // If a test failed before Done, stop the server so the worker can exit.
  try {
    await fetch(`${new URL(wizardUrl).origin}/api/cancel?s=${new URL(wizardUrl).searchParams.get('s')}`, { method: 'POST' });
  } catch {
    /* already stopped */
  }
  await fs.rm(tmp, { recursive: true, force: true });
});

test('refuses a visit without the session key', async ({ page }) => {
  const bare = new URL(wizardUrl);
  bare.searchParams.delete('s');
  const response = await page.goto(bare.href);
  expect(response!.status()).toBe(403);
  await expect(page.locator('body')).toContainText('session key');
});

test('shows the config file’s own values in the settings card', async ({ page }) => {
  await page.goto(wizardUrl);
  await expect(page.locator('#settings-card')).toBeVisible();
  await expect(page.locator('#set-site-title')).toHaveValue('My Forge');
  await expect(page.locator('#set-owner-name')).toHaveValue('Kieran Wood');
  // The expression evaluated: the ingest group is collapsed but its input carries the value.
  await expect(page.locator('#set-ingest-maxBlobBytes')).toHaveValue(String(512 * 1024));
  // Both repos entries listed as sources.
  await expect(page.locator('#source-rows .listrow')).toHaveCount(2);
});

test('saves an edited setting and keeps the file’s comments', async ({ page }) => {
  await page.goto(wizardUrl);
  await expect(page.locator('#set-site-title')).toHaveValue('My Forge');
  await page.fill('#set-site-title', 'Wizard Forge');
  await page.click('#settings-save');
  await expect(page.locator('#settings-status')).toHaveText('Saved.');

  const written = await fs.readFile(configFile(), 'utf8');
  expect(written).toContain("title: 'Wizard Forge', // shown in the sidebar");
  expect(written).toContain('512 * 1024, // half a meg, kept as an expression');
});

test('adds an organization from the list editor', async ({ page }) => {
  await page.goto(wizardUrl);
  await expect(page.locator('#settings-card')).toBeVisible();
  await page.fill('#org-slug', 'cc');
  await page.fill('#org-name', 'Canadian Coding');
  await page.click('#org-add');
  await expect(page.locator('#org-rows .listrow')).toHaveCount(1);
  // The config had no organizations array, so the engine created one inline.
  expect(await fs.readFile(configFile(), 'utf8')).toContain("organizations: [{ slug: 'cc', name: 'Canadian Coding' }]");
});

test('removes a source, leaving its neighbour byte-identical', async ({ page }) => {
  await page.goto(wizardUrl);
  const rows = page.locator('#source-rows .listrow');
  await expect(rows).toHaveCount(2);
  await rows.filter({ hasText: 'github' }).locator('button').click();
  await expect(page.locator('#source-rows .listrow')).toHaveCount(1);

  const written = await fs.readFile(configFile(), 'utf8');
  expect(written).not.toContain("repo: 'old'");
  expect(written).toContain("{ type: 'local', path: '.', slug: 'frznforge' },");
});

test('edits the profile body; the frontmatter block rides along untouched', async ({ page }) => {
  await page.goto(wizardUrl);
  await expect(page.locator('#profile-card')).toBeVisible();
  await expect(page.locator('#profile-fm')).toContainText('title: Me');
  await expect(page.locator('#profile-body')).toHaveValue('# Hi\n\nold body\n');

  await page.fill('#profile-body', '# New heading\n\nnew body\n');
  await page.click('#profile-preview');
  await expect(page.locator('#profile-prev')).toContainText('New heading');

  await page.click('#profile-save');
  await expect(page.locator('#profile-status')).toHaveText('Saved.');
  expect(await fs.readFile(profileFile(), 'utf8')).toBe('---\ntitle: Me\n---\n# New heading\n\nnew body\n');
});

test('Done stops the wizard and reports the session', async ({ page }) => {
  await page.goto(wizardUrl);
  await expect(page.locator('#settings-card')).toBeVisible();
  await page.click('#done');
  await expect(page.locator('#done-card')).toBeVisible();
  await expect(page.locator('#done-title')).toHaveText('Done');
  expect(await exit).toBe(0);
  expect(out.join('\n')).toContain('Done — ');
  // One backup per edited file, holding the pre-wizard bytes.
  const backups = (await fs.readdir(tmp)).filter((f) => f.endsWith('.bak'));
  expect(backups).toHaveLength(1);
  expect(await fs.readFile(path.join(tmp, backups[0]!), 'utf8')).toBe(CONFIG);
  const profileBackups = (await fs.readdir(path.dirname(profileFile()))).filter((f) => f.endsWith('.bak'));
  expect(profileBackups).toHaveLength(1);
  expect(await fs.readFile(path.join(path.dirname(profileFile()), profileBackups[0]!), 'utf8')).toBe(PROFILE);
});
