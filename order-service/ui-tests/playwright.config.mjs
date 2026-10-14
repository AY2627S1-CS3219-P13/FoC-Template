import { defineConfig } from "@playwright/test";
export default defineConfig({
  testDir: ".",
  testMatch: ["browser.spec.mjs", "capacity.spec.mjs"],
  workers: 1,
  use: {
    baseURL: process.env.ORDER_UI_URL || "http://order-ui-fixture:8080",
    viewport: { width: 1365, height: 1000 },
  },
  reporter: "list",
  outputDir: "/tmp/order-ui-results",
});
