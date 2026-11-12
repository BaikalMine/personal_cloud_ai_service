const { test, expect } = require("@playwright/test");
const { settlePage, assertNoViewportOverflow, chooseWorkflow } = require("./helpers.cjs");

test("edit assistant follows Krea2, FLUX.2 and Qwen workflow selection", async ({ page }) => {
  await page.goto("/preview/generate");
  await settlePage(page);
  await page.locator('[data-workflow-id="image-to-image"]').click();
  for (const [workflow, profile] of [
    ["photoflow-krea2-edit", "krea2-edit"],
    ["photoflow-flux2-edit", "flux-edit"],
    ["qwen-image21-edit", "qwen21-edit"],
    ["photoflow-krea2-edit", "krea2-edit"],
  ]) {
    await chooseWorkflow(page, workflow);
    await expect(page.locator("#prompt-assistant-template")).toHaveValue(profile);
    await page.locator("#prompt-assistant-open").click();
    await expect(page.locator("#prompt-assistant-mode")).toBeVisible();
    await assertNoViewportOverflow(page, `${workflow} assistant`);
    await page.keyboard.press("Escape");
  }
});

test("Qwen text and edit expose the right model, references and assistant profile", async ({ page }, info) => {
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  await page.goto("/preview/generate");
  await settlePage(page);
  await chooseWorkflow(page, "qwen-image21");
  await expect(page.locator("#generation-model")).toHaveValue("qwen21:test");
  await expect(page.locator("#generation-dimension-multiple")).toHaveValue("32");
  expect(Number(await page.locator("#generation-width").inputValue()) % 32).toBe(0);
  expect(Number(await page.locator("#generation-height").inputValue()) % 32).toBe(0);
  await expect(page.locator("#prompt-assistant-mode")).toContainText("Qwen Image 2.1");
  await expect(page.locator("[data-image-slot]:visible")).toHaveCount(0);

  await page.locator('[data-workflow-id="image-to-image"]').click();
  await chooseWorkflow(page, "qwen-image21-edit");
  await expect(page.locator("#generation-model")).toHaveValue("qwen21:test");
  await expect(page.locator("#generation-dimension-multiple")).toHaveValue("32");
  await expect(page.locator("[data-image-slot]:visible")).toHaveCount(10);
  await expect(page.locator("#prompt-assistant-template")).toHaveValue("qwen21-edit");
  await expect(page.locator("#generation-editor-profile-description")).toContainText("<image10>");
  await assertNoViewportOverflow(page, "Qwen edit");
  await page.screenshot({ path: info.outputPath("qwen21-edit.png"), fullPage: false });
  expect(errors).toEqual([]);
});

test("Qwen edit resolution follows the selected megapixels and source aspect", async ({ page }) => {
  await page.goto("/preview/generate");
  await settlePage(page);
  await page.locator('[data-workflow-id="image-to-image"]').click();
  await chooseWorkflow(page, "qwen-image21-edit");
  await page.locator('[data-image-slot="1"] [data-gallery-image-picker-open]').click();
  await page.getByRole("button", { name: "Выбрать AI-Gateway-Krea2-portrait.png" }).click();
  await expect(page.locator("#generation-qwen-edit-megapixels")).toHaveValue("1");
  const standard = await page.locator("#generation-width").inputValue();
  await page.locator("#generation-qwen-edit-megapixels").fill("1.5");
  await page.locator("#generation-qwen-edit-megapixels").dispatchEvent("change");
  await expect(page.locator("#generation-output-megapixels")).toHaveValue("1.5");
  await expect.poll(() => page.locator("#generation-width").inputValue()).not.toBe(standard);
  expect(await page.evaluate(() => window.AIGatewayGeneration.output.qwen21EditSize({ width: 3072, height: 4096 }, 1.5)))
    .toEqual({ width: 1088, height: 1440, resolution: 1248 });
  await page.locator("#generation-qwen-edit-megapixels").fill("4.27");
  await page.locator("#generation-qwen-edit-megapixels").dispatchEvent("change");
  await expect(page.locator("#generation-output-megapixels")).toHaveValue("4.27");
  await expect.poll(async () => Number(await page.locator("#generation-width").inputValue())).toBeGreaterThan(1536);
  const width = Number(await page.locator("#generation-width").inputValue());
  const height = Number(await page.locator("#generation-height").inputValue());
  expect(width * height).toBeLessThanOrEqual(4477419);
  expect(width % 32).toBe(0);
  expect(height % 32).toBe(0);
  expect(await page.evaluate(() => window.AIGatewayGeneration.output.qwen21EditSize({ width: 3072, height: 4096 }, 4.27)))
    .toEqual({ width: 1824, height: 2432, resolution: 2112 });
  await assertNoViewportOverflow(page, "Qwen edit size");
});

test("Qwen edit assistant applies and restores both prompts only when CFG enables negative conditioning", async ({ page }) => {
  page.on("dialog", dialog => dialog.accept());
  await page.goto("/preview/generate");
  await settlePage(page);
  await page.locator('[data-workflow-id="image-to-image"]').click();
  await chooseWorkflow(page, "qwen-image21-edit");
  await expect(page.locator("#generation-negative-cfg-note")).toBeVisible();
  await expect(page.locator("#generation-negative-prompt-field")).toBeHidden();
  await page.getByRole("button", { name: "Дополнительные параметры" }).click();
  await page.locator("#generation-cfg").fill("2");
  await page.locator("#generation-cfg").dispatchEvent("change");
  await page.getByRole("button", { name: "Закрыть параметры" }).click();
  await expect(page.locator("#generation-negative-prompt-field")).toBeVisible();
  await page.locator('[data-image-slot="1"] [data-gallery-image-picker-open]').click();
  await page.getByRole("button", { name: "Выбрать AI-Gateway-Krea2-portrait.png" }).click();
  await page.locator("#positive-prompt").fill("Change the coat to red.");
  await page.locator("#negative-prompt").fill("muddy texture");
  await page.locator("#prompt-assistant-open").click();
  await page.locator("#prompt-assistant-improve").click();
  await expect(page.locator("#prompt-assistant-negative-review")).toBeVisible();
  await expect(page.locator("#prompt-assistant-negative-draft")).toHaveValue("blurry, distorted anatomy");
  await page.locator("#prompt-assistant-negative-draft").fill("blurry, muddy texture");
  await page.locator("#prompt-assistant-apply").click();
  await expect(page.locator("#negative-prompt")).toHaveValue("blurry, muddy texture");
  await page.locator("#prompt-assistant-restore-original").click();
  await expect(page.locator("#positive-prompt")).toHaveValue("Change the coat to red.");
  await expect(page.locator("#negative-prompt")).toHaveValue("muddy texture");
});
