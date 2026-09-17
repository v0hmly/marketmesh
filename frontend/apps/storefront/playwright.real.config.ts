import { defineConfig } from '@playwright/test';

const baseURL = process.env.BASE_URL;
if (!baseURL || new URL(baseURL).protocol !== 'https:') throw new Error('BASE_URL must be HTTPS');
if (!/^[a-zA-Z0-9-]{8,64}$/.test(process.env.ACCOUNT_E2E_RUN_ID ?? ''))
  throw new Error('Missing unique ACCOUNT_E2E_RUN_ID');
if (!['pending', 'core'].includes(process.env.ACCOUNT_E2E_PHASE ?? ''))
  throw new Error('Invalid ACCOUNT_E2E_PHASE');

export default defineConfig({
  testDir: './tests/real',
  // Playwright removes outputDir before each run; the bind mount itself cannot be removed.
  outputDir: `${process.env.ACCOUNT_E2E_ARTIFACTS ?? '/artifacts'}/${process.env.ACCOUNT_E2E_PHASE}`,
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 120_000,
  expect: { timeout: 15_000 },
  reporter: [['./tests/real/safe-reporter.ts']],
  use: {
    baseURL,
    browserName: 'chromium',
    ignoreHTTPSErrors: false,
    trace: 'off',
    video: 'off',
    screenshot: 'off',
  },
});
