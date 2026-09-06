const fs = require("node:fs");
const path = require("node:path");
const { test, expect } = require("@playwright/test");
const { settlePage, openStudioOptions, assertNoViewportOverflow } = require("./helpers.cjs");

const fact = (page, label) => page.locator("#generation-summary-facts > div").filter({ has: page.locator("dt", { hasText: label }) }).locator("dd");
async function open(page, theme = "dark") {
  await page.context().clearCookies({ name: "preview_generation_draft" });
  await page.addInitScript(value => localStorage.setItem("ai_gateway_theme", value), theme);
  await page.goto("/preview/generate");
  await settlePage(page);
  await expect(page.locator("#generation-model")).not.toHaveValue("");
}
async function expandSummary(page) {
  if (await page.locator("#generation-summary").getAttribute("open") === null) await page.locator("#generation-summary > summary").click();
}
async function assertDock(page) {
  const editor = await page.locator("#studio-editor").boundingBox();
  const dock = await page.locator("#generation-run-dock").boundingBox();
  expect(editor.height).toBeGreaterThan(60);
  expect(editor.y + editor.height).toBeLessThanOrEqual(dock.y);
  await expect(page.locator("#generation-submit")).toBeInViewport();
  await assertNoViewportOverflow(page, "expanded launch summary");
}
test.beforeEach(async ({ page }) => page.on("dialog", d => d.type() === "beforeunload" ? d.accept() : d.dismiss()));

test("image summary separates base and output and stays usable in both themes", async ({ page }, info) => {
  for (const theme of ["dark", "light"]) {
    await open(page, theme);
    await page.locator("#generation-aspect").selectOption("3:4");
    await page.locator('[name="output_megapixels"]').fill("1.9");
    await page.locator('[name="base_megapixels"]').fill("1");
    await expandSummary(page);
    await expect(fact(page, "Базовый размер")).toHaveText("832 × 1152");
    await expect(fact(page, "Ожидаемый итог")).toHaveText("1216 × 1616");
    await expect(fact(page, "Количество")).toHaveText("1 фото");
    await expect(page.locator("#generation-summary-title")).toHaveText("1 фото · 1216 × 1616");
    await assertDock(page);
    await page.screenshot({ path: info.outputPath(`image-summary-${theme}.png`) });
    await page.locator(".studio-summary-body").focus();
    await page.keyboard.press("End");
    await expect(page.locator(".studio-summary-body")).toBeFocused();
  }
});

test("video summary tracks quality, optional processing and batch duration", async ({ page }, info) => {
  await open(page);
  await page.locator('[data-workflow-id="minimax-h3-video"]').click();
  await page.locator('[name="video_quality"]').selectOption("480");
  await page.locator('[name="video_aspect"]').selectOption("9:16");
  await page.locator('[name="video_duration_seconds"]').fill("10");
  await expandSummary(page);
  await expect(fact(page, "Базовый размер")).toHaveText("256 × 480");
  await expect(fact(page, "Частота кадров")).toHaveText("24 FPS");
  await openStudioOptions(page);
  await page.locator('[name="video_rife_enabled"]').check();
  await page.locator('[name="video_rife_multiplier"]').selectOption("2");
  await page.locator('[name="video_rtx_enabled"]').check();
  await page.locator('[name="video_rtx_scale"]').fill("2");
  await page.keyboard.press("Escape");
  await expect(fact(page, "Ожидаемый итог")).toHaveText("512 × 960");
  await expect(fact(page, "Базовый размер")).toHaveText("256 × 480");
  await expect(fact(page, "Частота кадров")).toHaveText("48 FPS");
  await expect(fact(page, "Длительность (задано)")).toHaveText("10 сек.");
  await assertDock(page);
  await page.screenshot({ path: info.outputPath("video-summary-dark.png") });
  await page.locator('[name="batch_enabled"]').check();
  await page.locator('[name="batch_mode"][value="parameter"]').check();
  await page.locator('[name="batch_mode"][value="parameter"]').focus();
  await page.keyboard.press("ArrowLeft");
  await expect(page.locator('[name="batch_mode"][value="seeds"]')).toBeChecked();
  await page.keyboard.press("ArrowRight");
  await expect(page.locator('[name="batch_mode"][value="parameter"]')).toBeChecked();
  await expect(page.locator('.generation-batch-mode .ui-segment:has(input:checked)')).not.toHaveCSS("box-shadow", "none");
  await page.locator('[name="batch_count"]').fill("3");
  await page.locator('[name="batch_parameter"]').selectOption("video_duration_seconds");
  await page.locator('[name="batch_from"]').fill("5");
  await page.locator('[name="batch_to"]').fill("15");
  await expect(fact(page, "Количество")).toHaveText("3 видео");
  await expect(fact(page, "Длительность (задано)")).toHaveText("5 → 15 сек.");
  await expect(fact(page, "В серии меняется")).toContainText("Длительность видео");
  await assertDock(page);
  await page.screenshot({ path: info.outputPath("video-batch-summary-dark.png") });
});

test("edit summary uses the chosen photo, family pipeline and clears removed sources", async ({ page }, info) => {
  await open(page, "light");
  await page.locator('[data-workflow-id="image-to-image"]').click();
  await page.locator('[data-preset-id="photoflow-krea2-edit"]').click();
  await expandSummary(page);
  await expect(fact(page, "Ожидаемый итог")).toHaveText("После выбора фото");
  const buffer = fs.readFileSync(path.join(__dirname, "../../docs/frontend/prototype/assets/portrait.jpg"));
  await page.locator("#source-image").setInputFiles({ name: "portrait.jpg", mimeType: "image/jpeg", buffer });
  await expect(fact(page, "Ожидаемый итог")).not.toHaveText("После выбора фото");
  await expect(fact(page, "Ожидаемый итог")).toHaveText(await fact(page, "Базовый размер").textContent());
  await page.locator('[data-preset-id="photoflow-flux2-edit"]').click();
  await openStudioOptions(page);
  await page.locator('[name="flux_upscale_mode"]').selectOption("both");
  await page.keyboard.press("Escape");
  await expect(fact(page, "Ожидаемый итог")).toContainText("≈");
  await expect(fact(page, "Ожидаемый итог")).toContainText("4098");
  await expect(fact(page, "Анализ референсов")).toContainText("Мп");
  await assertDock(page);
  await page.screenshot({ path: info.outputPath("flux-summary-light.png") });
  await page.locator('[data-image-slot="1"] [data-remove-image]').click();
  await expect(fact(page, "Базовый размер")).toHaveText("После выбора фото");
  await expect(fact(page, "Ожидаемый итог")).toHaveText("После выбора фото");
});
