const { test, expect } = require("@playwright/test");
const { settlePage, assertNoViewportOverflow, chooseWorkflow } = require("./helpers.cjs");

test("image and video switches restore primary video controls and preserve text", async ({ page }) => {
  await page.goto("/preview/generate");
  await settlePage(page);
  await page.locator("#positive-prompt").fill("A calm scene with controlled lighting.");
  for (const image of ["photoflow-krea2", "qwen-image21"]) {
    await page.locator('[data-workflow-id="text-to-image"]').click();
    await chooseWorkflow(page, image);
    await page.locator('[data-workflow-id="minimax-h3-video"]').click();
    for (const video of ["minimax-h3-video", "aihub-minimax-h3", "minimax-h3-video"]) {
      await chooseWorkflow(page, video);
      await expect(page.locator("#minimax-video-duration")).toBeVisible();
      await expect(page.locator("#minimax-video-duration")).toBeEnabled();
      await expect(page.locator("#minimax-video-quality")).toBeVisible();
      await expect(page.locator("#minimax-video-quality")).toBeEnabled();
      await expect(page.locator("#positive-prompt")).toHaveValue("A calm scene with controlled lighting.");
    }
  }
  await page.locator('[data-workflow-id="image-to-image"]').click();
  await chooseWorkflow(page, "qwen-image21-edit");
  await expect(page.locator("#minimax-video-duration")).not.toBeVisible();
  await page.locator('[data-workflow-id="minimax-h3-video"]').click();
  await expect(page.locator("#minimax-video-duration")).toBeVisible();
  await assertNoViewportOverflow(page, "video after image editing");
});

test("video settings do not depend on a successfully selected model", async ({ page }) => {
  await page.goto("/preview/generate");
  await settlePage(page);
  await page.locator("#generation-model").evaluate(select => {
    for (const option of [...select.options]) if (option.dataset.family === "minimax_h3") option.remove();
  });
  await page.locator('[data-preset-id="minimax-h3-video"]').evaluate(button => { button.disabled = true; });
  await page.locator('[data-workflow-id="minimax-h3-video"]').click();
  await chooseWorkflow(page, "aihub-minimax-h3");
  await expect(page.locator("#minimax-video-duration")).toBeVisible();
  await expect(page.locator("#minimax-video-quality")).toBeVisible();
  await expect(page.locator("#generation-model")).toHaveValue("");
  await expect(page.locator("[data-comfy-generation]")).toHaveAttribute("data-studio-ready", "false");
});

test("explicit prompt clearing is reversible and leaves generation settings intact", async ({ page }, info) => {
  await page.goto("/preview/generate");
  await settlePage(page);
  await page.locator("#positive-prompt").fill("The original user prompt.");
  await page.locator('[data-workflow-id="image-to-image"]').click();
  await chooseWorkflow(page, "photoflow-flux2-edit");
  await page.locator("#negative-prompt").fill("blur");
  const model = await page.locator("#generation-model").inputValue();
  await page.locator("#generation-prompt-clear").click();
  await expect(page.locator("#positive-prompt")).toHaveValue("");
  await expect(page.locator("#negative-prompt")).toHaveValue("");
  await expect(page.locator("#generation-model")).toHaveValue(model);
  await page.locator("#generation-prompt-undo-clear").click();
  await expect(page.locator("#positive-prompt")).toHaveValue("The original user prompt.");
  await expect(page.locator("#negative-prompt")).toHaveValue("blur");
  await page.locator("#generation-prompt-clear").click();
  await page.locator("#positive-prompt").fill("New prompt.");
  await expect(page.locator("#generation-prompt-undo-clear")).toBeHidden();
  await assertNoViewportOverflow(page, "prompt controls");
  await page.locator(".generation-prompt-fields").screenshot({ path: info.outputPath("prompt-controls.png") });
});
