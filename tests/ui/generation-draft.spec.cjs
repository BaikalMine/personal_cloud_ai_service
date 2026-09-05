const { test, expect } = require("@playwright/test");
const { installPreviewClock, settlePage, assertNoViewportOverflow, openStudioOptions } = require("./helpers.cjs");

const open = async (page) => {
  await installPreviewClock(page);
  await page.goto("/preview/generate");
  await settlePage(page);
  await expect(page.locator("#generation-draft-status")).not.toHaveText("Загружаем...");
  await page.locator(".studio-saved > summary").click();
};
const save = async (page) => {
  if (await page.locator("#studio-settings").isVisible()) await page.keyboard.press("Escape");
  if (!(await page.locator(".studio-saved").getAttribute("open") !== null)) await page.locator(".studio-saved > summary").click();
  await page.locator("#generation-draft-save").click();
  await expect(page.locator("#generation-draft-status")).toHaveText("Сохранено");
};

test("draft restores prompt, LoRA, seed, optional settings and manual assistant edits", async ({ page }) => {
  await open(page);
  await page.getByRole("button", { name: /^Изображение$/ }).click();
  await page.locator("#positive-prompt").fill("A white ceramic vase in soft window light.");
  await openStudioOptions(page);
  await page.locator('.krea-lora-row select[name="lora_1"]').selectOption({ label: "Krea2 Realism V2" });
  await page.locator('.krea-lora-row input[name="lora_model_strength_1"]').fill("0.65");
  await page.locator('input[name="seed"]:enabled').fill("123456");
  await page.locator('input[name="detail_enabled"]').uncheck();
  await page.keyboard.press("Escape");
  await page.locator("#prompt-assistant-enabled").check();
  await page.locator("#prompt-assistant-improve").click();
  await expect(page.locator("#prompt-assistant-review")).toBeVisible();
  await page.locator("#prompt-assistant-draft").fill("My edited draft: a white ceramic vase, fine texture.");
  await save(page);
  await page.reload();
  await expect(page.locator("#generation-draft-status")).toHaveText("Сохранено");
  await expect(page.locator("#positive-prompt")).toHaveValue("A white ceramic vase in soft window light.");
  await expect(page.locator('.krea-lora-row select[name="lora_1"]')).toHaveValue("Krea2-realism-V2.safetensors");
  await expect(page.locator('.krea-lora-row input[name="lora_model_strength_1"]')).toHaveValue("0.65");
  await expect(page.locator('input[name="seed"]:enabled')).toHaveValue("123456");
  await expect(page.locator('input[name="detail_enabled"]')).not.toBeChecked();
  await expect(page.locator("#prompt-assistant-draft")).toHaveValue("My edited draft: a white ceramic vase, fine texture.");
  await assertNoViewportOverflow(page, "restored generation draft");
});

test("draft restores all four REF2VA references and their roles", async ({ page }) => {
  await open(page);
  await page.getByRole("button", { name: /^Видео$/ }).click();
  await page.locator("#minimax-video-mode label").filter({ hasText: "По референсам" }).click();
  for (let index = 1; index <= 4; index += 1) {
    await page.locator(`[data-image-slot="${index}"] [data-gallery-image-picker-open]`).click();
    await page.getByRole("button", { name: "Выбрать AI-Gateway-Krea2-product.png", exact: true }).click();
  }
  await page.locator('[data-image-slot="4"] [data-image-role]').selectOption("background");
  await save(page);
  await page.reload();
  await expect(page.locator("#generation-draft-status")).toHaveText("Сохранено");
  await expect(page.getByRole("radio", { name: /^По референсам/ })).toBeChecked();
  for (let index = 1; index <= 4; index += 1) {
    await expect(page.locator(`[data-image-slot="${index}"] [data-image-name]`)).toHaveText("AI-Gateway-Krea2-product.png");
    await expect(page.locator(`[data-image-slot="${index}"] [data-image-state]`)).toHaveText("Восстановлено из черновика");
  }
  await expect(page.locator('[data-image-slot="4"] [data-image-role]')).toHaveValue("background");
  await expect(page.locator("#positive-prompt")).toBeVisible();
});

test("two tabs resolve conflicts explicitly and owner can delete the draft", async ({ page, context }) => {
  await open(page);
  await page.getByRole("button", { name: /^Изображение$/ }).click();
  await page.locator("#positive-prompt").fill("First prompt");
  await save(page);
  const other = await context.newPage();
  await open(other);
  await page.locator("#positive-prompt").fill("Changed on first tab");
  await save(page);
  await other.locator("#positive-prompt").fill("Changed on second tab");
  await other.locator("#generation-draft-save").click();
  await expect(other.locator("#generation-draft-remote")).toBeVisible();
  await expect(other.locator("#positive-prompt")).toHaveValue("Changed on second tab");
  await other.locator("#generation-draft-local").click();
  await expect(other.locator("#generation-draft-status")).toHaveText("Сохранено");
  await page.locator("#positive-prompt").fill("Stale third edit");
  await page.locator("#generation-draft-save").click();
  await expect(page.locator("#generation-draft-remote")).toBeVisible();
  await assertNoViewportOverflow(page, "draft conflict");
  await page.locator("#generation-draft-remote").click();
  await expect(page.locator("#positive-prompt")).toHaveValue("Changed on second tab");
  page.once("dialog", (dialog) => dialog.accept());
  await page.locator("#generation-draft-delete").click();
  await expect(page.locator("#generation-draft-status")).toHaveText("Нет сохранённого черновика");
  await expect(page.locator("#positive-prompt")).toHaveValue("Changed on second tab");
  await page.reload();
  await expect(page.locator("#positive-prompt")).toHaveValue("");
  await other.close();
});

test("expired draft reference stays visible and must be replaced or removed", async ({ page }) => {
  await open(page);
  await page.getByRole("button", { name: /^Видео$/ }).click();
  await page.locator('[data-image-slot="1"] [data-gallery-image-picker-open]').click();
  await page.getByRole("button", { name: "Выбрать AI-Gateway-Krea2-product.png", exact: true }).click();
  await page.locator("#positive-prompt").fill("A slow camera movement around a ceramic vase.");
  await save(page);
  await page.route("**/generate/draft", async (route) => {
    const response = await route.fetch();
    const payload = await response.json();
    if (route.request().method() === "GET" && payload.draft) {
      payload.draft.assets = payload.draft.assets.map(({ value, url, ...asset }) => ({ ...asset, available: false }));
    }
    await route.fulfill({ response, json: payload });
  });
  await page.reload();
  await expect(page.locator("#generation-draft-status")).toContainText("Недоступных материалов: 1");
  await expect(page.locator("#positive-prompt")).toHaveValue("A slow camera movement around a ceramic vase.");
  const slot = page.locator('[data-image-slot="1"]');
  await expect(slot.locator("[data-image-name]")).toHaveText("AI-Gateway-Krea2-product.png");
  await expect(slot.locator("[data-image-state]")).toContainText("Замените материал или удалите его.");
  await slot.locator("[data-remove-image]").click();
  await save(page);
  await expect(page.locator("#generation-draft-status")).toHaveText("Сохранено");
});

test("draft restores optional audio and video references", async ({ page }) => {
  await page.route("**/generate/upload/audio", (route) => route.fulfill({ json: { name: "voice.wav", subfolder: "users/preview" } }));
  await page.route("**/generate/upload/video", (route) => route.fulfill({ json: { name: "motion.mp4", subfolder: "users/preview" } }));
  await open(page);
  await page.getByRole("button", { name: /^Видео$/ }).click();
  await page.locator("#minimax-video-mode label").filter({ hasText: "По референсам" }).click();
  await page.locator("#minimax-audio-file").setInputFiles({ name: "voice.wav", mimeType: "audio/wav", buffer: Buffer.from("preview audio fixture") });
  await page.locator("#minimax-video-file").setInputFiles({ name: "motion.mp4", mimeType: "video/mp4", buffer: Buffer.from("preview video fixture") });
  await save(page);
  await page.reload();
  await expect(page.locator("#generation-draft-status")).toHaveText("Сохранено");
  await expect(page.locator("#minimax-audio-name")).toHaveText("voice.wav");
  await expect(page.locator("#minimax-video-name")).toHaveText("motion.mp4");
  await expect(page.locator("#minimax-audio-state")).toHaveText("Восстановлено из черновика");
  await expect(page.locator("#minimax-video-state")).toHaveText("Восстановлено из черновика");
  await expect(page.locator("#positive-prompt")).toBeVisible();
});
