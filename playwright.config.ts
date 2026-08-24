import { defineConfig } from '@playwright/test';

const PORT = 4399;

export default defineConfig({
  testDir: './tests/e2e',
  globalSetup: './tests/e2e/global-setup.ts',
  timeout: 60_000,
  fullyParallel: true,
  reporter: process.env.CI ? 'github' : 'list',
  use: {
    baseURL: `http://localhost:${PORT}`,
    trace: 'retain-on-failure',
  },
  webServer: {
    // tsx, so the server can import the same `src/lib/mime.ts` the site's raw endpoints use.
    command: `npx tsx tests/e2e/serve.ts tests/.tmp/e2e/dist ${PORT}`,
    port: PORT,
    reuseExistingServer: false,
    timeout: 30_000,
  },
  projects: [{ name: 'chromium', use: { browserName: 'chromium' } }],
});
