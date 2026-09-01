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

test('edits an organization in place instead of removing and re-adding it', async ({ page }) => {
  // 0.2.0 could only add and remove; "edit the information once entered" is the 0.3.0 ask.
  await page.goto(wizardUrl);
  const row = page.locator('#org-rows .listrow').first();
  await row.locator('details.editrow summary').click();
  await page.fill('#edit-organizations-0-name', 'Canadian Coding Renamed');
  await page.fill('#edit-organizations-0-description', 'tools that keep working');
  await row.locator('details.editrow button', { hasText: 'Save changes' }).click();
  await expect(page.locator('#settings-status')).toHaveText('Saved.');

  const written = await fs.readFile(configFile(), 'utf8');
  expect(written).toContain("name: 'Canadian Coding Renamed'");
  expect(written).toContain("description: 'tools that keep working'");
  // the slug — the entry's identity, which repos point at — is deliberately not editable
  expect(written).toContain("slug: 'cc'");
  await expect(page.locator('#edit-organizations-0-slug')).toHaveCount(0);
});

test('adds a contributor and gives them a picture path', async ({ page }) => {
  await page.goto(wizardUrl);
  await page.fill('#contrib-name', 'Kieran Wood');
  await page.fill('#contrib-emails', 'k@example.com, work@example.com');
  await page.click('#contrib-add');
  await expect(page.locator('#contrib-rows .listrow')).toHaveCount(1);
  const written = await fs.readFile(configFile(), 'utf8');
  expect(written).toContain("name: 'Kieran Wood'");
  expect(written).toContain("emails: ['k@example.com', 'work@example.com']");
});

test('removes a source, leaving its neighbour byte-identical', async ({ page }) => {
  await page.goto(wizardUrl);
  const rows = page.locator('#source-rows .listrow');
  await expect(rows).toHaveCount(2);
  // Rows carry a Remove button and (since 0.3.0) an Edit disclosure, so name the one we mean.
  await rows.filter({ hasText: 'github' }).getByRole('button', { name: 'Remove' }).click();
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

test('uploads a picture and fills the field with the path the server chose', async ({ page }) => {
  await page.goto(wizardUrl);
  await expect(page.locator('#settings-card')).toBeVisible();
  await page.locator('#settings-groups details.setgroup').first(); // groups rendered

  // A real PNG header — the server identifies the type from the bytes, not from the name.
  const png = Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    Buffer.from('fixture'),
  ]);
  await page.setInputFiles('#set-owner-avatar-file', {
    name: 'whatever-they-called-it.bin',
    mimeType: 'application/octet-stream',
    buffer: png,
  });

  // The field is filled in with the SERVER's path; the file is on disk; config is untouched
  // until the user saves.
  await expect(page.locator('#set-owner-avatar')).toHaveValue('images/owner.png', { timeout: 15000 });
  const onDisk = await fs.readFile(path.join(tmp, 'public', 'images', 'owner.png'));
  expect(onDisk.equals(png)).toBe(true);
  expect(await fs.readFile(configFile(), 'utf8')).not.toContain('avatar');

  await page.click('#settings-save');
  await expect(page.locator('#settings-status')).toHaveText('Saved.');
  expect(await fs.readFile(configFile(), 'utf8')).toContain("avatar: 'images/owner.png'");
});

test('Done saves pending edits before it stops the wizard', async ({ page }) => {
  await page.goto(wizardUrl);
  await expect(page.locator('#settings-card')).toBeVisible();

  // Two edits that are NEVER saved explicitly — before 0.3.0 both were silently discarded.
  await page.fill('#set-site-title', 'Saved By Done');
  const flushedBody = ['# Flushed', '', 'by done', ''].join('\n');
  await page.fill('#profile-body', flushedBody);

  await page.click('#done');
  await expect(page.locator('#done-card')).toBeVisible();
  await expect(page.locator('#done-title')).toHaveText('Done');

  // ...and both landed on disk rather than being thrown away with the session.
  const config = await fs.readFile(configFile(), 'utf8');
  expect(config).toContain("title: 'Saved By Done', // shown in the sidebar");
  expect(await fs.readFile(profileFile(), 'utf8')).toBe(`---\ntitle: Me\n---\n${flushedBody}`);

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
