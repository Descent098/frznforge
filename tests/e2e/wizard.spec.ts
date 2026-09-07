/**
 * The init wizard driven through a real browser (0.2.0): the whole-config editor and the
 * profile editor, end to end against a real `frznforge init --web` process on an ephemeral port.
 *
 * Serial on purpose — the tests share one server and drive one session through its life:
 * refuse a tokenless visit, edit settings, edit the profile, remove a source, then Done. The
 * Go package tests (`internal/wizard`) own the API contract; this spec owns "the page actually
 * wires those endpoints to inputs a person can use".
 *
 * 0.4.0 changed the harness and nothing else. The wizard is no longer a TypeScript function this
 * file can call, so it is spawned as the built binary and its URL is read off stdout — the
 * Listen-then-Serve split in internal/wizard exists so that URL is printable before the process
 * disappears into Accept. The config fixture is JSONC rather than TypeScript for the same
 * reason. Every assertion below is the one that was here before.
 */
import { expect, test } from '@playwright/test';
import { execFileSync, spawn, type ChildProcess } from 'node:child_process';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';

const ROOT = path.resolve(import.meta.dirname, '..', '..');
/** tests/.tmp is the suite's scratch and is gitignored, so the binary is litter nobody commits. */
const BIN = path.join(ROOT, 'tests', '.tmp', 'wizard', `frznforge${process.platform === 'win32' ? '.exe' : ''}`);

/**
 * The token variables the wizard reads. Stripped from the child's environment so the run cannot
 * pick up the developer's own credentials — the `env: {}` this spec used to pass to `Io`.
 */
const TOKEN_VARS = ['GITHUB', 'GITLAB', 'GITEA', 'FORGEJO'].flatMap((p) => [`FRZNFORGE_${p}_TOKEN`, `${p}_TOKEN`]);

const CONFIG = `// hand-written config, full of comments worth keeping
{
  "site": {
    "title": "My Forge", // shown in the sidebar
  },
  "owner": { "name": "Kieran Wood", "handle": "kieran" },
  /** Two sources, so a remove has a neighbour it must leave alone. */
  "repos": [
    { "type": "local", "path": ".", "slug": "frznforge" },
    { "type": "github", "owner": "me", "repo": "old" },
  ],
  "ingest": {
    "maxBlobBytes": 524288, // 512 * 1024, half a meg
  },
}
`;

const PROFILE = '---\ntitle: Me\n---\n# Hi\n\nold body\n';

test.describe.configure({ mode: 'serial' });

let tmp: string;
let wizardUrl: string;
let child: ChildProcess | undefined;
let exit: Promise<number>;
let out: string[];

const configFile = (): string => path.join(tmp, 'frznforge.config.jsonc');
const profileFile = (): string => path.join(tmp, 'content', 'profile.md');

/** Split a stream into whole lines, so `out` holds one line per entry the way `Io.log` did. */
function collect(stream: NodeJS.ReadableStream): void {
  let pending = '';
  stream.setEncoding('utf8');
  stream.on('data', (chunk: string) => {
    pending += chunk;
    const lines = pending.split('\n');
    pending = lines.pop() ?? '';
    for (const line of lines) out.push(line.replace(/\r$/, ''));
  });
  stream.on('end', () => {
    if (pending) out.push(pending);
  });
}

test.beforeAll(async () => {
  tmp = await fs.mkdtemp(path.join(os.tmpdir(), 'frznforge-wizard-'));
  await fs.writeFile(configFile(), CONFIG, 'utf8');
  await fs.mkdir(path.dirname(profileFile()), { recursive: true });
  await fs.writeFile(profileFile(), PROFILE, 'utf8');
  out = [];

  // Built rather than `go run`: `go run` compiles inside the URL wait below (flaky on a cold
  // cache) and puts a parent process between the teardown's kill and the server holding the port.
  await fs.mkdir(path.dirname(BIN), { recursive: true });
  // No `shell: true` — libuv finds go.exe through PATHEXT on its own, and a shell would put the
  // argument quoting of a path with spaces back on us.
  execFileSync('go', ['build', '-o', BIN, './cmd/frznforge'], { cwd: ROOT, stdio: 'pipe' });

  const env = { ...process.env };
  for (const name of TOKEN_VARS) delete env[name];
  // cwd is the repo, not `tmp`: a process's working directory is locked against deletion on
  // Windows, and the teardown removes `tmp`. `--root` is what points the wizard at the fixture.
  child = spawn(BIN, ['init', '--web', '--no-open', '--port=0', `--root=${tmp}`], { cwd: ROOT, env });
  let spawnFailure = '';
  collect(child.stdout!);
  collect(child.stderr!);
  // 'close', not 'exit': 'exit' can fire while stdout still has buffered data, and the last
  // assertion reads the "Done — …" line the wizard prints on its way out.
  exit = new Promise<number>((resolve) => {
    child!.on('close', (code) => resolve(code ?? -1));
    child!.on('error', (err) => {
      spawnFailure = `\n${err.message}`;
      resolve(-1);
    });
  });

  for (let waited = 0; waited < 8000 && !out.some((l) => l.includes('http://127.0.0.1:')); waited += 25) {
    await new Promise((r) => setTimeout(r, 25));
  }
  const line = out.find((l) => l.includes('http://127.0.0.1:'));
  if (!line) throw new Error(`the wizard never printed its URL:\n${out.join('\n')}${spawnFailure}`);
  wizardUrl = line.trim();
});

test.afterAll(async () => {
  // If a test failed before Done, stop the server so the worker can exit.
  try {
    await fetch(`${new URL(wizardUrl).origin}/api/cancel?s=${new URL(wizardUrl).searchParams.get('s')}`, { method: 'POST' });
  } catch {
    /* already stopped */
  }
  // A child that never bound, or one whose shutdown hung, would hold its port and keep the
  // Playwright worker alive forever; give it a moment to go on its own, then end it.
  if (child) {
    await Promise.race([exit, new Promise((r) => setTimeout(r, 2000))]);
    if (child.exitCode === null && child.signalCode === null) child.kill();
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
  // The ingest group is collapsed but its input carries the value.
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
  expect(written).toContain('"title": "Wizard Forge", // shown in the sidebar');
  expect(written).toContain('524288, // 512 * 1024, half a meg');
});

test('adds an organization from the list editor', async ({ page }) => {
  await page.goto(wizardUrl);
  await expect(page.locator('#settings-card')).toBeVisible();
  await page.fill('#org-slug', 'cc');
  await page.fill('#org-name', 'Canadian Coding');
  await page.click('#org-add');
  await expect(page.locator('#org-rows .listrow')).toHaveCount(1);
  // The config had no organizations array, so the engine created one inline.
  expect(await fs.readFile(configFile(), 'utf8')).toContain('"organizations": [{ "slug": "cc", "name": "Canadian Coding" }]');
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
  expect(written).toContain('"name": "Canadian Coding Renamed"');
  expect(written).toContain('"description": "tools that keep working"');
  // the slug — the entry's identity, which repos point at — is deliberately not editable
  expect(written).toContain('"slug": "cc"');
  await expect(page.locator('#edit-organizations-0-slug')).toHaveCount(0);
});

test('adds a contributor and gives them a picture path', async ({ page }) => {
  await page.goto(wizardUrl);
  await page.fill('#contrib-name', 'Kieran Wood');
  await page.fill('#contrib-emails', 'k@example.com, work@example.com');
  await page.click('#contrib-add');
  await expect(page.locator('#contrib-rows .listrow')).toHaveCount(1);
  const written = await fs.readFile(configFile(), 'utf8');
  expect(written).toContain('"name": "Kieran Wood"');
  expect(written).toContain('"emails": ["k@example.com", "work@example.com"]');
});

test('removes a source, leaving its neighbour byte-identical', async ({ page }) => {
  await page.goto(wizardUrl);
  const rows = page.locator('#source-rows .listrow');
  await expect(rows).toHaveCount(2);
  // Rows carry a Remove button and (since 0.3.0) an Edit disclosure, so name the one we mean.
  await rows.filter({ hasText: 'github' }).getByRole('button', { name: 'Remove' }).click();
  await expect(page.locator('#source-rows .listrow')).toHaveCount(1);

  const written = await fs.readFile(configFile(), 'utf8');
  expect(written).not.toContain('"repo": "old"');
  expect(written).toContain('{ "type": "local", "path": ".", "slug": "frznforge" },');
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
  expect(await fs.readFile(configFile(), 'utf8')).toContain('"avatar": "images/owner.png"');
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
  expect(config).toContain('"title": "Saved By Done", // shown in the sidebar');
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
