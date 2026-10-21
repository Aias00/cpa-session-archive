import { defineConfig } from "@playwright/test";

const port = Number(process.env.CPA_E2E_PORT || 4173);
if (!Number.isInteger(port) || port < 1024 || port > 65535) {
  throw new Error("CPA_E2E_PORT must be an integer from 1024 to 65535");
}
const baseURL = `http://127.0.0.1:${port}`;

export default defineConfig({
  testDir: "./e2e",
  timeout: 30_000,
  expect: { timeout: 8_000 },
  use: {
    baseURL,
    locale: "zh-CN",
    timezoneId: "Asia/Shanghai",
    trace: "retain-on-failure",
  },
  webServer: {
    command: "pnpm exec tsx e2e/mock-server.ts",
    url: `${baseURL}/healthz`,
    reuseExistingServer: false,
  },
});
