import { defineConfig, devices } from '@playwright/test';

export default defineConfig({
  testDir: './tests/browser',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: 'list',
  use: {
    baseURL: 'https://127.0.0.1:4174',
    // This disposable UI fixture uses a self-signed certificate, not a real backend.
    ignoreHTTPSErrors: true,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: {
    env: { VITE_ACCOUNT_ADDRESSES_ENABLED: 'true', VITE_ACCOUNT_SETTINGS_ENABLED: 'true' },
    command: 'pnpm exec vite --host 127.0.0.1 --port 4174',
    url: 'https://127.0.0.1:4174',
    ignoreHTTPSErrors: true,
    reuseExistingServer: false,
  },
});
