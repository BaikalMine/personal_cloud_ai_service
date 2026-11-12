const { test, expect } = require("@playwright/test");
const { settlePage, chooseWorkflow } = require("./helpers.cjs");

test("AIHUB returns to ordinary MiniMax mode and image generation without losing text", async ({ page }, info) => {
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  await page.goto("/preview/generate");
  await settlePage(page);
  const ordinary = page.locator('[data-preset-id="minimax-h3-video"]');
  const aihub = page.locator('[data-preset-id="aihub-minimax-h3"]');
  await page.locator('[data-workflow-id="minimax-h3-video"]').click();
  await page.locator('[name="video_mode"][value="frames"]').check();
  await page.locator("#positive-prompt").fill("A quiet seaside scene.");
  for (const mode of ["photo", "video", "carousel"]) {
    await chooseWorkflow(page, "aihub-minimax-h3");
    await expect(aihub).toHaveAttribute("aria-pressed", "true");
    await page.locator(`[name="aihub_mode"][value="${mode}"]`).check();
    await chooseWorkflow(page, "minimax-h3-video");
    await expect(ordinary).toHaveAttribute("aria-pressed", "true");
    await expect(aihub).toHaveAttribute("aria-pressed", "false");
    await expect(page.locator(".aihub-settings")).toBeHidden();
    await expect(page.locator('[name="video_mode"][value="frames"]')).toBeChecked();
    await expect(page.locator('[name="video_mode"][value="frames"]')).toBeEnabled();
    await expect(page.locator("#positive-prompt")).toHaveValue("A quiet seaside scene.");
  }
  await page.locator('[name="video_mode"][value="references"]').check();
  await chooseWorkflow(page, "aihub-minimax-h3");
  await chooseWorkflow(page, "minimax-h3-video");
  await expect(page.locator('[name="video_mode"][value="references"]')).toBeChecked();
  await chooseWorkflow(page, "aihub-minimax-h3");
  await page.locator("#studio-workflow-open").scrollIntoViewIfNeeded();
  await page.screenshot({ path: info.outputPath("aihub-modes.png") });
  await page.locator('[data-workflow-id="text-to-image"]').click();
  await expect(page.locator('[data-preset-id="photoflow-krea2"]')).toHaveAttribute("aria-pressed", "true");
  await expect(page.locator(".aihub-settings")).toBeHidden();
  await expect(page.locator("#positive-prompt")).toHaveValue("A quiet seaside scene.");
  expect(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)).toBe(false);
  expect(errors).toEqual([]);
});

test("download picker uses normal video upload even without a MIME type", async ({ page }, info) => {
  let uploaded = false, body;
  await page.route("**/generate/upload/video", route => {
    uploaded = route.request().postDataBuffer().includes(Buffer.from("download-fixture"));
    return route.fulfill({ json: { name: "download.mp4", subfolder: "users/preview" } });
  });
  await page.route("**/generate/preflight", route => {
    body = new URLSearchParams(route.request().postData());
    return route.fulfill({ json: { ok: true, checks: [] } });
  });
  await page.goto("/preview/generate");
  await settlePage(page);
  await page.locator('[data-workflow-id="minimax-h3-video"]').click();
  await chooseWorkflow(page, "aihub-minimax-h3");
  await page.locator('[name="aihub_mode"][value="video"]').check();
  const picker = page.locator("#minimax-video-browse");
  await expect(picker).not.toHaveAttribute("accept");
  await expect(picker).not.toHaveAttribute("capture");
  const chooserPromise = page.waitForEvent("filechooser");
  await page.locator('label[for="minimax-video-browse"]').click();
  await (await chooserPromise).setFiles({ name: "download.mp4", mimeType: "", buffer: Buffer.from("download-fixture") });
  await expect(page.locator("#minimax-video-name")).toHaveText("download.mp4");
  await expect(page.locator("#minimax-video-preview")).toBeVisible();
  await page.locator("#positive-prompt").fill("Ocean waves move slowly.");
  await page.locator("#generation-preflight-button").click();
  await expect.poll(() => uploaded).toBe(true);
  await expect.poll(() => body?.get("input_video")).toContain("download.mp4");
  await page.locator("#minimax-video-preview").scrollIntoViewIfNeeded();
  await page.screenshot({ path: info.outputPath("download-video.png") });
  await page.locator("#minimax-video-remove").click();
  await expect(page.locator("#minimax-video-preview")).toBeHidden();
  expect(await page.locator("#minimax-video-file").evaluate(input => input.files.length)).toBe(0);
  await picker.setInputFiles({ name: "download.mp4", mimeType: "application/octet-stream", buffer: Buffer.from("download-fixture") });
  await expect(page.locator("#minimax-video-name")).toHaveText("download.mp4");
});
