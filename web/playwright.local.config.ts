import { defineConfig, devices } from '@playwright/test';

// The mock browser specs (e2e/*.mock.spec.ts) answer the API themselves, so they can run against THIS working tree instead of the
// deployed site (playwright.config.ts points at the production URL: a run with it checks the bundle that is already deployed, not
// the code in front of you). Builds once, serves with `vite preview` on its own port (3187; 3000 is the deployed web container).
//   npm run test:e2e:local -- e2e/hub.mock.spec.ts
const PORT = 3187;
export default defineConfig({
  testDir: './e2e',
  testMatch: /.*\.mock\.spec\.ts/,
  fullyParallel: true,
  reporter: 'line',
  outputDir: '.e2e-local-results', // not test-results: that folder may belong to another user after a containerised run
  use: { baseURL: `http://127.0.0.1:${PORT}`, ignoreHTTPSErrors: true },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: {
    command: `npx vite build --outDir .e2e-local-dist --emptyOutDir && npx vite preview --outDir .e2e-local-dist --host 127.0.0.1 --port ${PORT} --strictPort`,
    url: `http://127.0.0.1:${PORT}`,
    reuseExistingServer: false,
    timeout: 180_000,
  },
});
