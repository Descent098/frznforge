import { defineConfig } from '@playwright/test';

const PORT = 4399;
/** The base-path build (0.2.0): the same fixture artifact served under /mysite. */
const BASE_PORT = 4398;

export default defineConfig({
  testDir: './tests/e2e',
  globalSetup: './tests/e2e/global-setup.ts',
  timeout: 60_000,
  // The wizard spec's assertions can wait on a cold tsx child (config-load); the server itself
  // budgets 20s for that, so the default 5s assertion timeout would be flaky on a slow box.
  // Raising the ceiling only extends how long a *failing* assertion waits — passing ones (the
  // static-server specs) still resolve immediately.
  expect: { timeout: 15_000 },
  fullyParallel: true,
  reporter: process.env.CI ? 'github' : 'list',
  use: {
    baseURL: `http://localhost:${PORT}`,
    trace: 'retain-on-failure',
  },
  webServer: [
    {
      // tsx, so the server can import the same `src/lib/mime.ts` the site's raw endpoints use.
      command: `npx tsx tests/e2e/serve.ts tests/.tmp/e2e/dist ${PORT}`,
      port: PORT,
      reuseExistingServer: false,
      timeout: 30_000,
    },
    {
      command: `npx tsx tests/e2e/serve.ts tests/.tmp/e2e/dist-base ${BASE_PORT} /mysite`,
      port: BASE_PORT,
      reuseExistingServer: false,
      timeout: 30_000,
    },
  ],
  projects: [{ name: 'chromium', use: { browserName: 'chromium' } }],
});
