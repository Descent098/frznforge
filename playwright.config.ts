import { defineConfig } from '@playwright/test';

const PORT = 4399;
/** The base-path build (0.2.0): the same fixture artifact served under /mysite. */
const BASE_PORT = 4398;

/**
 * The ports the two fixture servers listen on. The servers themselves are started by
 * global-setup.ts, NOT by Playwright's `webServer`.
 *
 * That is not a style preference. Playwright launches `webServer` BEFORE `globalSetup`, so a
 * `frznforge dev` named here would be asked to serve a directory that global-setup has not
 * built yet: on a clean checkout the run dies with "the system cannot find the path specified"
 * before setup ever executes. It only ever appeared to work because a previous run had left
 * tests/.tmp behind — which is exactly the kind of green that means nothing.
 *
 * globalSetup builds, then starts both servers, then returns a teardown that stops them.
 */
export default defineConfig({
  testDir: './tests/e2e',
  globalSetup: './tests/e2e/global-setup.ts',
  timeout: 60_000,
  // The wizard spec's assertions can wait on a freshly spawned binary (config-load); the server
  // itself budgets 20s for that, so the default 5s assertion timeout would be flaky on a slow
  // box. Raising the ceiling only extends how long a *failing* assertion waits — passing ones
  // (the static-server specs) still resolve immediately.
  expect: { timeout: 15_000 },
  fullyParallel: true,
  reporter: process.env.CI ? 'github' : 'list',
  use: {
    baseURL: `http://localhost:${PORT}`,
    trace: 'retain-on-failure',
  },
  projects: [{ name: 'chromium', use: { browserName: 'chromium' } }],
});
