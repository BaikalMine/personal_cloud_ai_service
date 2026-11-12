const fs = require("node:fs");
const path = require("node:path");
const { test, expect } = require("@playwright/test");
const AxeBuilder = require("@axe-core/playwright").default;
const { installPreviewClock, settlePage, assertNoViewportOverflow } = require("./helpers.cjs");

const cards = page => page.locator(".lora-dataset-item");
const status = page => page.locator("[data-dataset-quality-status]");
const start = page => page.locator("[data-dataset-quality-start]");
const saved = page => expect(page.locator("[data-dataset-save-state]")).toHaveAttribute("data-state", "saved");
const open = async page => {
  await installPreviewClock(page); await page.goto("/preview/lora-training"); await settlePage(page);
  await page.locator('[name="name"]').fill("Проверка качества набора");
  await page.locator('[name="trigger_word"]').fill("test_person");
  await page.locator("[data-lora-images]").setInputFiles(["portrait.jpg", "portrait-2.jpg", "interior.jpg"].map(name => ({ name, mimeType: "image/jpeg", buffer: fs.readFileSync(path.join(__dirname, "../../docs/frontend/prototype/assets", name)) })));
  await expect(cards(page)).toHaveCount(3); await saved(page);
  for (let i = 0; i < 3; i++) await cards(page).nth(i).locator("textarea").fill(`manual caption ${i + 1}`);
  await saved(page);
  const id = await page.locator("[data-dataset-select]").inputValue();
  return (await page.request.get(`/api/lora-datasets/${id}`)).json();
};
const fakeResponse = asset => ({ asset_id: asset.id, sha256: asset.sha256, quality: { version: 1, detail_score: 1, low_detail: true, colors: Array(192).fill(100) } });

for (const theme of ["light", "dark"]) test(`quality recommendations remain optional and readable in ${theme}`, async ({ page, context, baseURL }, info) => {
  const errors = []; page.on("pageerror", error => errors.push(error.message));
  await context.addCookies([{ name: "ai_gateway_theme", value: theme, url: baseURL }]);
  const view = await open(page); const calls = [];
  await page.route("**/api/lora-datasets/assets/*/quality", async route => {
    const id = route.request().url().split("/").at(-2); calls.push(id);
    await route.fulfill({ json: fakeResponse(view.assets[id]) });
  });
  await start(page).click(); await expect(status(page)).toContainText("Проверено: 3 из 3");
  await expect(page.locator("[data-dataset-quality-background]")).toBeVisible();
  await expect(page.locator("[data-dataset-quality-note]:visible")).toHaveCount(3);
  for (let i = 0; i < 3; i++) {
    await expect(cards(page).nth(i).getByRole("checkbox")).toBeChecked();
    await expect(cards(page).nth(i).locator("textarea")).toHaveValue(`manual caption ${i + 1}`);
  }
  await expect(page.locator("[data-lora-submit]")).toBeEnabled();
  await start(page).click(); await expect(status(page)).toContainText("Проверено: 3 из 3"); expect(calls).toHaveLength(3);
  await assertNoViewportOverflow(page, `quality ${theme}`);
  await page.locator("[data-dataset-quality]").scrollIntoViewIfNeeded();
  await page.screenshot({ path: info.outputPath(`quality-${theme}.png`), fullPage: true });
  await page.locator("[data-dataset-quality]").screenshot({ path: info.outputPath(`quality-controls-${theme}.png`) });
  await cards(page).first().screenshot({ path: info.outputPath(`quality-card-${theme}.png`) });
  if (info.project.name === "desktop-1440") {
    const result = await new AxeBuilder({ page }).include(".lora-dataset-section").withTags(["wcag2a", "wcag2aa", "wcag21aa"]).analyze();
    expect(result.violations.filter(v => ["critical", "serious"].includes(v.impact))).toEqual([]);
  }
  await cards(page).last().getByRole("checkbox").uncheck(); await saved(page);
  await expect(status(page)).toContainText("Проверено: 2 из 2");
  await expect(page.locator("[data-dataset-quality-background]")).not.toBeVisible();
  await expect(cards(page).last().locator("[data-dataset-quality-note]")).not.toBeVisible();
  await page.reload(); await settlePage(page);
  await expect(status(page)).toContainText("Проверка необязательна");
  await expect(cards(page).last().getByRole("checkbox")).not.toBeChecked();
  expect(calls).toHaveLength(3); expect(errors).toEqual([]);
});

test("quality cancellation and failure retain captions and resume only missing photos", async ({ page }, info) => {
  test.skip(info.project.name !== "desktop-1440", "state transitions run once");
  const view = await open(page); const calls = []; let release; let failed = false;
  await page.route("**/api/lora-datasets/assets/*/quality", async route => {
    const id = route.request().url().split("/").at(-2); calls.push(id);
    if (calls.length === 1) await new Promise(resolve => { release = resolve; });
    else if (failed) { failed = false; await route.fulfill({ status: 429, json: { error: "Обработчик изображений занят." } }); return; }
    await route.fulfill({ json: fakeResponse(view.assets[id]) }).catch(() => {});
  });
  await start(page).click(); await expect(status(page)).toContainText("Проверяем фото: 0 из 3");
  await page.locator("[data-dataset-quality-cancel]").click(); release();
  await expect(status(page)).toContainText("Проверка остановлена"); expect(calls).toHaveLength(1);
  failed = true; await start(page).click(); await expect(status(page)).toContainText("Обработчик изображений занят");
  await expect(cards(page).first().locator("textarea")).toHaveValue("manual caption 1");
  await start(page).click(); await expect(status(page)).toContainText("Проверено: 3 из 3");
  expect(calls).toHaveLength(5);
  await page.locator("[data-dataset-new]").click(); await expect(cards(page)).toHaveCount(0);
  await expect(page.locator("[data-dataset-quality]")).not.toBeVisible();
});

test("preview exercises the real CPU measurements without model requests", async ({ page }, info) => {
  test.skip(info.project.name !== "desktop-1440", "real analysis runs once");
  const calls = []; page.on("request", request => { if (request.method() === "POST") calls.push(request.url()); });
  await open(page); calls.length = 0;
  await start(page).click(); await expect(status(page)).toContainText("Проверено: 3 из 3");
  expect(calls).toHaveLength(3); expect(calls.every(url => url.endsWith("/quality"))).toBe(true);
  await page.reload(); await settlePage(page);
  await expect(status(page)).toContainText("Проверено: 3 из 3");
  await start(page).click(); await expect(status(page)).toContainText("Проверено: 3 из 3");
  expect(calls).toHaveLength(3);
});
