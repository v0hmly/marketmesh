import { defineConfig } from '@playwright/test';
export default defineConfig({
  testDir: './tests/bundles',
  workers: 1,
  retries: 0,
  reporter: 'list',
  use: { baseURL: 'https://127.0.0.1:4176', ignoreHTTPSErrors: true, browserName: 'chromium' },
  webServer: {
    command: 'pnpm exec vite preview --host 127.0.0.1 --port 4176',
    url: 'https://127.0.0.1:4176',
    ignoreHTTPSErrors: true,
    reuseExistingServer: false,
  },
});
