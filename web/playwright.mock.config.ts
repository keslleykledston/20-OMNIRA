import { defineConfig, devices } from '@playwright/test';

// Browser tests with a MOCKED API (page.route): they exercise the built SPA only, never a database, NATS or WAHA.
// Used for UI surfaces whose backend is covered by Go integration tests (for example the topic panel, ADR-0017).
export default defineConfig({
  testDir: './e2e',
  testMatch: /.*\.mock\.spec\.ts/,
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [['list']],
  timeout: 45_000,
  use: { baseURL: 'http://127.0.0.1:4174', trace: 'retain-on-failure' },
  webServer: {
    command: 'npx vite preview --port 4174 --host 127.0.0.1 --strictPort',
    url: 'http://127.0.0.1:4174/',
    reuseExistingServer: false,
    timeout: 30_000,
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'], viewport: { width: 1440, height: 900 } } }],
});
