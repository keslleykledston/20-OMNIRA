import { defineConfig, devices } from '@playwright/test';

// Browser E2E against the REAL API and database (scripts/e2e-hub-browser.sh sets the environment and starts everything).
export default defineConfig({
  testDir: './e2e-real',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [['list']],
  outputDir: process.env.E2E_OUT ?? '/tmp/pw-real-out',
  timeout: 60_000,
  use: { baseURL: process.env.E2E_BASE_URL, trace: 'off', ignoreHTTPSErrors: true },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
});
