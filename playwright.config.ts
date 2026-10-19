import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./tests/e2e",
  fullyParallel: true,
  forbidOnly: Boolean(process.env.CI),
  retries: 0,
  use: {
    baseURL: "http://127.0.0.1:18473",
    viewport: { width: 1280, height: 800 },
    trace: "retain-on-failure",
  },
  webServer: {
    command:
      process.platform === "win32" ? "dist\\agentos.exe" : "./dist/agentos",
    url: "http://127.0.0.1:18473/api/health",
    env: { AGENTOS_HTTP_ADDR: "127.0.0.1:18473" },
    reuseExistingServer: false,
  },
});
