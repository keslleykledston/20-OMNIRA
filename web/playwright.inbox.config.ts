import { defineConfig, devices } from '@playwright/test';

// Real-stack e2e for the Inbox vertical. Started by scripts/e2e-inbox.sh, which brings up
// Postgres DB + API + worker + the built SPA (vite preview, /api proxied to the API).
export default defineConfig({
  testDir: './e2e',
  testMatch: /(inbox|channels)\.spec\.ts/,
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [['list']],
  timeout: 60_000,
  use: {
    baseURL: process.env.E2E_BASE_URL || 'http://127.0.0.1:4173',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
});
