const path = require("node:path");
const { defineConfig } = require("@playwright/test");

const outputRoot = process.env.PLAYWRIGHT_OUTPUT_DIR || path.join(__dirname, "artifacts", "playwright");

module.exports = defineConfig({
  testDir: path.join(__dirname, "tests", "ui"),
  testMatch: "**/*.spec.cjs",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 45_000,
  expect: {
    timeout: 6_000,
    toHaveScreenshot: {
      animations: "disabled",
      caret: "hide",
      maxDiffPixelRatio: 0.002,
    },
  },
  reporter: [
    ["line"],
    ["html", { outputFolder: path.join(outputRoot, "report"), open: "never" }],
  ],
  outputDir: path.join(outputRoot, "test-results"),
  snapshotPathTemplate: path.join(__dirname, "tests", "ui", "__screenshots__", "{testFilePath}", "{projectName}", "{arg}{ext}"),
  use: {
    baseURL: process.env.UI_PREVIEW_URL || "http://127.0.0.1:18080",
    browserName: "chromium",
    colorScheme: "dark",
    locale: "ru-RU",
    timezoneId: "Europe/Moscow",
    reducedMotion: "reduce",
    actionTimeout: 7_000,
    navigationTimeout: 12_000,
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
  },
  projects: [
    { name: "mobile-390", use: { viewport: { width: 390, height: 844 } } },
    { name: "tablet-768", use: { viewport: { width: 768, height: 1024 } } },
    { name: "desktop-1440", use: { viewport: { width: 1440, height: 900 } } },
    { name: "wide-1920", use: { viewport: { width: 1920, height: 1080 } } },
  ],
});
