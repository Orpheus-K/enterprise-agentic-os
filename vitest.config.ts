import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    environment: "jsdom",
    include: ["apps/**/*.test.tsx", "packages/**/*.test.ts"],
    restoreMocks: true,
  },
});
