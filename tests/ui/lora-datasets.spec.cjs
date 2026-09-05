const fs = require("node:fs");
const path = require("node:path");
const { test, expect } = require("@playwright/test");
const AxeBuilder = require("@axe-core/playwright").default;
const { installPreviewClock, settlePage, assertNoViewportOverflow } = require("./helpers.cjs");
const { installCaptionFixture } = require("./caption-fixture.cjs");

const source = (name) => fs.readFileSync(path.join(__dirname, "../../docs/frontend/prototype/assets", name));
const items = (page) => page.locator(".lora-dataset-item");
const captions = (page) => items(page).locator("textarea");
const saved = async (page) => expect(page.locator("[data-dataset-save-state]")).toHaveAttribute("data-state", "saved");
const open = async (page) => { await installPreviewClock(page); await page.goto("/preview/lora-training"); await settlePage(page); };
const fill = async (page, count = 5) => {
  await page.locator('[name="name"]').fill("Портретный набор");
  await page.locator('[name="trigger_word"]').fill("portrait_person");
  await page.locator("[data-lora-images]").setInputFiles(Array.from({ length: count }, (_, i) => ({ name: `portrait-${i + 1}.jpg`, mimeType: "image/jpeg", buffer: source(i % 2 ? "portrait-2.jpg" : "portrait.jpg") })));
  await expect(items(page)).toHaveCount(count);
  await saved(page);
  for (let i = 0; i < count; i++) await captions(page).nth(i).fill(`portrait_person, frame ${i + 1}, soft daylight, light hair, neutral background`);
  await saved(page);
};
const desktopOnly = (info) => test.skip(info.project.name !== "desktop-1440", "state transitions run once on desktop");

test("dataset saves and reloads when the browser has no randomUUID", async ({ page }) => {
  await page.addInitScript(() => Object.defineProperty(crypto, "randomUUID", { value: undefined, configurable: true }));
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  await open(page);
  await fill(page, 2);
  await page.reload();
  await settlePage(page);
  await expect(items(page)).toHaveCount(2);
  await expect(captions(page).first()).toHaveValue(/frame 1/);
  expect(errors).toEqual([]);
});

test("dataset editor, versions and gallery fit the viewport", async ({ page }, info) => {
  await open(page); await fill(page, 2);
  await page.locator("h1").click();
  await page.evaluate(() => window.scrollTo(0, 0));
  await assertNoViewportOverflow(page, "filled dataset");
  await page.screenshot({ path: info.outputPath("dataset-filled.png"), fullPage: true });
  await page.locator("[data-dataset-versions]").click();
  await page.getByRole("button", { name: "Зафиксировать версию" }).click();
  await expect(page.locator(".dataset-version-row")).toHaveCount(1);
  await assertNoViewportOverflow(page, "versions dialog");
  await page.screenshot({ path: info.outputPath("dataset-versions.png") });
  await page.keyboard.press("Escape");
  await expect(page.locator("[data-dataset-versions]")).toBeFocused();
  await page.locator("[data-dataset-gallery]").click();
  await expect(page.locator(".dataset-gallery-item img")).toHaveCount(3);
  await page.locator(".dataset-gallery-item img").evaluateAll((images) => Promise.all(images.map((image) => image.decode())));
  await assertNoViewportOverflow(page, "gallery dialog");
  await page.screenshot({ path: info.outputPath("dataset-gallery.png") });
  if (info.project.name === "desktop-1440") {
    const result = await new AxeBuilder({ page }).include("[data-lora-training-page]").withTags(["wcag2a", "wcag2aa", "wcag21aa"]).analyze();
    expect(result.violations.filter((v) => v.impact === "serious" || v.impact === "critical")).toEqual([]);
  }
});

test("dataset reload preserves reorder, exclusion and settings", async ({ page }, info) => {
  desktopOnly(info); await open(page); await fill(page);
  await items(page).nth(1).getByRole("button", { name: "Переместить выше" }).click();
  await items(page).first().getByRole("checkbox", { name: "В обучении" }).uncheck();
  await saved(page); await page.reload(); await settlePage(page);
  await expect(captions(page).first()).toHaveValue(/frame 2/);
  await expect(items(page).first().getByRole("checkbox")).not.toBeChecked();
  await expect(page.locator('[name="trigger_word"]')).toHaveValue("portrait_person");
  await expect(items(page)).toHaveCount(5);
});

test("dataset immutable version restores a copy without losing working edits", async ({ page }, info) => {
  desktopOnly(info); await open(page); await fill(page, 2);
  await page.locator("[data-dataset-versions]").click();
  await page.getByRole("button", { name: "Зафиксировать версию" }).click();
  await expect(page.locator(".dataset-version-row")).toHaveCount(1); await page.keyboard.press("Escape");
  await captions(page).first().fill("manual edit after version"); await saved(page);
  const original = await page.locator("[data-dataset-select]").inputValue();
  await page.locator("[data-dataset-versions]").click();
  await page.getByRole("button", { name: "Восстановить копию" }).click();
  await expect(page.locator("[data-dataset-dialog]")).not.toBeVisible();
  await expect(captions(page).first()).toHaveValue(/frame 1/);
  expect(await page.locator("[data-dataset-select]").inputValue()).not.toBe(original);
  await page.locator("[data-dataset-select]").selectOption(original);
  await expect(captions(page).first()).toHaveValue("manual edit after version");
});

test("dataset ZIP and gallery reuse preserve the editor state", async ({ page }, info) => {
  desktopOnly(info); await open(page); await fill(page, 2);
  await items(page).last().getByRole("checkbox").uncheck(); await saved(page);
  const downloadEvent = page.waitForEvent("download"); await page.locator("[data-dataset-export]").click();
  const download = await downloadEvent; const zipPath = info.outputPath("dataset.zip"); await download.saveAs(zipPath);
  await page.locator("[data-dataset-new]").click(); await expect(items(page)).toHaveCount(0);
  await page.locator("[data-dataset-zip]").setInputFiles(zipPath);
  await expect(items(page)).toHaveCount(2); await saved(page);
  await expect(captions(page).first()).toHaveValue(/frame 1/); await expect(items(page).last().getByRole("checkbox")).not.toBeChecked();
  await page.locator("[data-dataset-gallery]").click(); await page.locator(".dataset-gallery-item").last().click();
  await expect(items(page)).toHaveCount(3); await page.keyboard.press("Escape"); await saved(page);
  await page.reload(); await settlePage(page); await expect(items(page)).toHaveCount(3);
});

test("dataset two-tab conflict can preserve both variants", async ({ page, context }, info) => {
  desktopOnly(info); await open(page); await fill(page, 1);
  const other = await context.newPage(); await open(other);
  await captions(page).first().fill("saved from first tab"); await saved(page);
  await captions(other).first().fill("local second tab");
  await expect(other.locator("[data-dataset-conflict]")).toBeVisible();
  await expect(captions(other).first()).toHaveValue("local second tab");
  await other.locator("[data-dataset-fork]").click(); await saved(other);
  await page.reload(); await settlePage(page);
  const options = await page.locator("[data-dataset-select] option").count(); expect(options).toBe(2);
  await expect(captions(other).first()).toHaveValue("local second tab");
  await other.close();
});

test("dataset late assistant response never overwrites a manual caption", async ({ page, context }, info) => {
  desktopOnly(info); await open(page); await fill(page, 1);
  const queue = await installCaptionFixture(context);
  await items(page).first().getByRole("button", { name: "Описать кадр" }).click();
  await expect.poll(() => queue.jobs.length).toBe(1);
  await captions(page).first().fill("manual caption while assistant waits"); queue.jobs[0].state = "completed"; await queue.refresh(page);
  await expect(items(page).first().locator("[data-lora-caption-item-status]")).toContainText("Ответ не применён");
  await expect(captions(page).first()).toHaveValue("manual caption while assistant waits"); await saved(page);
});

test("caption series survives closing the page and keeps already applied results", async ({ page, context }, info) => {
  desktopOnly(info); const queue = await installCaptionFixture(context); await open(page); await fill(page, 3);
  for (let i = 0; i < 3; i++) await captions(page).nth(i).fill(""); await saved(page);
  await page.getByRole("button", { name: "Описать пустые" }).click(); await expect.poll(() => queue.jobs.length).toBe(3);
  queue.jobs[0].state = "completed"; await queue.refresh(page); await expect(captions(page).first()).toHaveValue(/separately analyzed/); await saved(page);
  await page.close(); queue.jobs[1].state = "completed";
  const reopened = await context.newPage(); await open(reopened);
  await expect(captions(reopened).nth(1)).toHaveValue(/separately analyzed/); await saved(reopened);
  expect(queue.posts).toHaveLength(1); expect(queue.actions).toHaveLength(0);
  await expect(reopened.getByRole("button", { name: "Остановить серию" })).toBeVisible();
  queue.jobs[2].state = "completed"; await queue.refresh(reopened);
  await expect(reopened.locator("[data-lora-caption-status]")).toContainText("Готово 3 из 3"); await saved(reopened);
  await reopened.reload(); await settlePage(reopened);
  await expect(captions(reopened).first()).toHaveValue(/frame 1/); expect(queue.posts).toHaveLength(1); await reopened.close();
});

test("caption errors, cancellation and reconnect remain usable at every viewport", async ({ page, context }, info) => {
  const queue = await installCaptionFixture(context); await open(page); await fill(page, 2);
  for (let i = 0; i < 2; i++) await captions(page).nth(i).fill(""); await saved(page);
  await page.getByRole("button", { name: "Описать пустые" }).click(); await expect.poll(() => queue.jobs.length).toBe(2);
  queue.jobs[0].state = "failed"; queue.jobs[0].error = "Не удалось подготовить описание. Повторите этот кадр."; await queue.refresh(page);
  await expect(items(page).first().getByRole("button", { name: "Повторить описание кадра" })).toBeVisible();
  await assertNoViewportOverflow(page, "caption failure");
  await page.screenshot({ path: info.outputPath("caption-failure.png"), fullPage: true });
  await items(page).first().getByRole("button", { name: "Повторить описание кадра" }).click();
  expect(queue.actions).toEqual([{ id: queue.jobs[0].job_id, action: "retry" }]); expect(queue.posts).toHaveLength(1);
  await page.getByRole("button", { name: "Остановить серию" }).click();
  await expect(page.locator("[data-lora-caption-status]")).toContainText("Отменено: 2");
  queue.offline = true; await queue.refresh(page);
  await expect(page.locator("[data-lora-caption-status]")).toContainText("Повтор через 3 сек.");
  await expect(page.locator("[data-lora-caption-status]")).toContainText("Повтор через 2 сек.");
  await expect(captions(page).first()).toBeEnabled();
  queue.offline = false; await queue.refresh(page);
  await expect(page.locator("[data-lora-caption-status]")).toContainText("Отменено: 2");
  await items(page).last().getByRole("button", { name: "Повторить описание кадра" }).click();
  await items(page).last().getByRole("button", { name: "Отменить описание кадра" }).click();
  expect(queue.actions.at(-1)).toEqual({ id: queue.jobs[1].job_id, action: "cancel" });
});

test("dataset training uses the saved set and appends one history entry", async ({ page }, info) => {
  desktopOnly(info); await open(page); await fill(page);
  const before = await page.locator("[data-lora-job]").count();
  await page.locator("[data-lora-submit]").click();
  await expect(page.locator("[data-dataset-feedback]")).toContainText("Обучение поставлено в очередь");
  await expect(page.locator("[data-lora-job]")).toHaveCount(before + 1);
  await expect(items(page)).toHaveCount(5);
});

test("dataset with 100 images survives closing the page", async ({ page, context }, info) => {
  desktopOnly(info); test.setTimeout(120000); await open(page);
  await page.locator('[name="name"]').fill("Сто кадров");
  await page.locator("[data-lora-images]").setInputFiles(Array.from({ length: 100 }, (_, i) => ({ name: `frame-${i + 1}.jpg`, mimeType: "image/jpeg", buffer: source("portrait.jpg") })));
  await expect(items(page)).toHaveCount(100);
  await expect(page.locator("[data-dataset-save-state]")).toHaveAttribute("data-state", "saved", { timeout: 60000 });
  await captions(page).first().fill("first manual caption");
  await captions(page).last().fill("last manual caption"); await saved(page);
  await page.close(); const reopened = await context.newPage(); await open(reopened);
  await expect(items(reopened)).toHaveCount(100);
  await expect(captions(reopened).first()).toHaveValue("first manual caption");
  await expect(captions(reopened).last()).toHaveValue("last manual caption");
  await reopened.locator("[data-lora-images]").setInputFiles({ name: "extra.jpg", mimeType: "image/jpeg", buffer: source("portrait.jpg") });
  await expect(reopened.locator("[data-dataset-feedback]")).toContainText("не больше 100");
  await expect(items(reopened)).toHaveCount(100); await reopened.close();
});

test("dataset load failure exposes a working retry without accepting edits", async ({ page }, info) => {
  desktopOnly(info);
  let offline = true;
  await page.route("**/api/lora-datasets", (route) => offline ? route.fulfill({ status: 503, json: { error: "Временная ошибка загрузки" } }) : route.continue());
  await open(page);
  await expect(page.locator('[name="name"]')).toBeDisabled();
  await expect(page.locator("[data-dataset-status]")).toContainText("Временная ошибка загрузки");
  offline = false; await page.locator("[data-dataset-save]").click();
  await expect(page.locator('[name="name"]')).toBeEnabled();
  await page.locator('[name="name"]').fill("После восстановления связи"); await saved(page);
});
