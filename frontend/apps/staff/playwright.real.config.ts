import { defineConfig } from "@playwright/test";
export default defineConfig({
  testDir: "./tests/real",
  workers: 1,
  retries: 0,
  timeout: 60_000,
  reporter: [["./tests/real/safe-reporter.ts"]],
  outputDir: "/artifacts/staff",
  use: {
    baseURL: "https://staff.localhost:18444",
    browserName: "chromium",
    ignoreHTTPSErrors: false,
    clientCertificates: [
      {
        origin: "https://staff.localhost:18444",
        certPath: "/staff-fixture/browser.crt",
        keyPath: "/staff-fixture/browser.key",
      },
    ],
    trace: "off",
    video: "off",
    screenshot: "off",
  },
});
