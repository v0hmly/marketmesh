import { defineConfig, devices } from "@playwright/test";
export default defineConfig({
  testDir: "./tests/browser",
  workers: 1,
  retries: 0,
  reporter: "list",
  use: {
    baseURL: "https://127.0.0.1:4175",
    ignoreHTTPSErrors: true,
    trace: "retain-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  webServer: {
    command: "pnpm exec vite --host 127.0.0.1 --port 4175",
    url: "https://127.0.0.1:4175",
    ignoreHTTPSErrors: true,
    reuseExistingServer: false,
  },
});
