const { test, expect } = require("@playwright/test");
const AxeBuilder = require("@axe-core/playwright").default;
const fs = require("node:fs");
const path = require("node:path");
const { settlePage, assertNoViewportOverflow, expectFocusInside } = require("./helpers.cjs");
const { installCaptionFixture } = require("./caption-fixture.cjs");
const panel = page => page.locator("[data-caption-panel]");
const field = page => page.locator("[data-caption-text]");
const cards = page => page.locator(".lora-dataset-item");
const saved = page => expect(page.locator("[data-dataset-save-state]")).toHaveAttribute("data-state", "saved");
const open = async (page, count = 3) => {
  await page.goto("/preview/lora-training"); await settlePage(page);
  await page.locator('[name="name"]').fill("Portrait review");
  await page.locator('[name="trigger_word"]').fill("person_x");
  await page.locator("[data-lora-images]").setInputFiles(Array.from({ length: count }, (_, index) => ({
    name: `frame-${index + 1}.jpg`, mimeType: "image/jpeg",
    buffer: fs.readFileSync(path.join(__dirname, "../../docs/frontend/prototype/assets", index % 2 ? "portrait-2.jpg" : "portrait.jpg")),
  })));
  await expect(cards(page)).toHaveCount(count); await saved(page);
  await cards(page).first().getByRole("button", { name: "Открыть кадр 1", exact: true }).click();
};
test.beforeEach(async ({ page }) => page.on("dialog", dialog => dialog.dismiss()));
test.beforeEach(async ({ page }) => {
  page.on("pageerror", error => { throw error; });
});

test("caption panel shows the complete photo and saves trigger-first edits across navigation", async ({ page }, info) => {
  await open(page);
  await expectFocusInside(page, "[data-caption-panel]");
  await expect(page.locator("[data-caption-previous]")).toBeDisabled();
  await expect(page.locator("[data-caption-trigger]")).toHaveText("person_x");
  await field(page).fill("Light hair, blue jacket, indoor daylight.");
  await page.locator("[data-caption-next]").click();
  await expect(page.locator("[data-caption-title]")).toHaveText("Кадр 2 из 3");
  await expect(field(page)).toHaveValue("");
  await field(page).fill("Side view, green coat, soft window light.");
  await page.locator("[data-caption-previous]").click();
  await expect(field(page)).toHaveValue("Light hair, blue jacket, indoor daylight.");
  await page.locator("[data-caption-save]").click(); await saved(page);
  for (const theme of ["light", "dark"]) {
    await page.evaluate(value => { localStorage.setItem("ai_gateway_theme", value); window.dispatchEvent(new StorageEvent("storage", { key: "ai_gateway_theme", newValue: value, storageArea: localStorage })); }, theme);
    await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
    await page.locator("[data-caption-image]").evaluate(img => img.decode());
    await expect(page.locator("[data-caption-image]")).toHaveCSS("object-fit", "contain");
    await assertNoViewportOverflow(page, "caption panel");
    await expect(page.locator("[data-caption-save]")).toBeInViewport();
    const result = await new AxeBuilder({ page }).include("[data-caption-panel]").analyze();
    expect(result.violations.filter(v => ["serious", "critical"].includes(v.impact))).toEqual([]);
    await page.screenshot({ path: info.outputPath(`caption-panel-${theme}.png`) });
  }
  await page.keyboard.press("Escape");
  await expect(cards(page).first().locator("[data-caption-open]")).toBeFocused();
  await expect(cards(page).first().locator("textarea")).toHaveValue("person_x, Light hair, blue jacket, indoor daylight.");
  await page.reload(); await settlePage(page);
  await cards(page).nth(1).locator("[data-caption-open]").click();
  await expect(field(page)).toHaveValue("Side view, green coat, soft window light.");
});

test("single-frame assistant preserves manual edits and supports retry and cancellation", async ({ page, context }) => {
  const queue = await installCaptionFixture(context); await open(page, 2);
  await page.locator("[data-caption-describe]").click();
  await expect.poll(() => queue.jobs.length).toBe(1);
  await expect(page.locator("[data-caption-action-label]")).toHaveText("Отменить описание кадра");
  await field(page).fill("Manual description of this frame.");
  queue.jobs[0].state = "completed"; await queue.refresh(page);
  await expect(page.locator("[data-caption-status]")).toContainText("Ответ не применён");
  await expect(field(page)).toHaveValue("Manual description of this frame.");
  await page.locator("[data-caption-next]").click();
  await page.locator("[data-caption-describe]").click(); await expect.poll(() => queue.jobs.length).toBe(2);
  queue.jobs[1].state = "failed"; queue.jobs[1].error = "Модель временно недоступна"; await queue.refresh(page);
  await expect(page.locator("[data-caption-status]")).toContainText("Модель временно недоступна");
  await page.locator("[data-caption-describe]").click();
  await expect.poll(() => queue.actions.at(-1)?.action).toBe("retry");
  await expect(page.locator("[data-caption-action-label]")).toHaveText("Отменить описание кадра");
  await page.locator("[data-caption-describe]").click();
  await expect.poll(() => queue.actions.at(-1)?.action).toBe("cancel");
  expect(queue.posts).toHaveLength(2);
});

test("caption series excludes filled frames and reports the current frame and skipped work", async ({ page, context }) => {
  const queue = await installCaptionFixture(context); await open(page, 3);
  await field(page).fill("Existing description."); await saved(page);
  await page.keyboard.press("Escape");
  await cards(page).last().getByRole("checkbox").uncheck(); await saved(page);
  await page.getByRole("button", { name: "Описать пустые", exact: true }).click();
  await expect.poll(() => queue.jobs.length).toBe(1);
  expect(queue.posts[0].only_empty).toBe(true);
  queue.jobs[0].state = "running"; await queue.refresh(page);
  await expect(page.locator("[data-lora-caption-status]")).toContainText("Анализируется кадр 2");
  await expect(page.locator("[data-lora-caption-status]")).toContainText("Пропущено заполненных или исключённых: 2");
  queue.jobs[0].state = "completed"; await queue.refresh(page);
  await cards(page).nth(1).locator("[data-caption-open]").click();
  await expect(field(page)).toHaveValue(/separately analyzed/);
  await expect(field(page)).not.toHaveValue(/^person_x/);
  await page.locator("[data-caption-next]").click();
  await expect(page.locator("[data-caption-describe]")).toBeDisabled();
  await expect(page.locator("[data-caption-meta]")).toContainText("Исключён");
  await expect(page.locator("[data-caption-next]")).toBeDisabled();
});

test("caption request error stays inside the panel and a retry succeeds", async ({ page, context }) => {
  const queue = await installCaptionFixture(context); await open(page, 1);
  let fail = true;
  await page.route("**/api/lora-datasets/*/captions", route => {
    if (fail && route.request().method() === "POST") return route.fulfill({ status: 504, json: { error: "Сервис временно недоступен" } });
    return route.fallback();
  });
  await page.locator("[data-caption-describe]").click();
  await expect(page.locator("[data-caption-feedback]")).toContainText("Сервис временно недоступен");
  await expect(panel(page)).toBeVisible();
  fail = false;
  await page.locator("[data-caption-describe]").click();
  await expect.poll(() => queue.jobs.length).toBe(1);
  await expect(page.locator("[data-caption-feedback]")).not.toBeVisible();
});

test("trigger replacement and an empty caption survive narrow-screen editing", async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 740 });
  await open(page, 1);
  await field(page).fill("person_x, portrait in daylight"); await saved(page);
  await assertNoViewportOverflow(page, "320px caption");
  await expect(page.locator("[data-caption-save]")).toBeInViewport();
  await page.keyboard.press("Escape");
  await page.locator('[name="trigger_word"]').fill("new_person");
  await page.locator("h1").click(); await saved(page);
  await cards(page).first().locator("[data-caption-open]").click();
  await expect(page.locator("[data-caption-trigger]")).toHaveText("new_person");
  await expect(field(page)).toHaveValue("portrait in daylight");
  await field(page).fill(""); await saved(page);
  await page.keyboard.press("Escape");
  await expect(cards(page).first().locator("textarea")).toHaveValue("");
});
