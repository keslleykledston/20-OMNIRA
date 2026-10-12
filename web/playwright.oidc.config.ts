import { defineConfig, devices } from '@playwright/test';

// Browser E2E of the REAL sign-in against a throwaway Keycloak (scripts/e2e-hub-keycloak.sh sets the environment and starts everything).
export default defineConfig({
  testDir: './e2e-oidc',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [['list']],
  outputDir: process.env.E2E_OUT ?? '/tmp/pw-oidc-out',
  timeout: 90_000,
  use: { baseURL: process.env.E2E_BASE_URL, trace: 'off', ignoreHTTPSErrors: true },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
});
