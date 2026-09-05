const fs = require("node:fs");
const path = require("node:path");
const { test, expect } = require("@playwright/test");
const { settlePage, installPreviewClock, assertNoViewportOverflow } = require("./helpers.cjs");

async function open(page, theme = "dark") {
  await page.context().clearCookies({ name: "preview_generation_draft" });
  await installPreviewClock(page);
  await page.addInitScript(value => localStorage.setItem("ai_gateway_theme", value), theme);
  await page.goto("/preview/generate");
  await settlePage(page);
  await expect(page.locator("#positive-prompt")).toBeVisible();
}
test.beforeEach(async ({ page }) => page.on("dialog", d => d.type() === "beforeunload" ? d.accept() : d.dismiss()));

test("studio primary task and all modes fit in both themes", async ({ page }, info) => {
  for (const theme of ["dark", "light"]) {
    const errors = [];
    page.on("pageerror", e => errors.push(e.message));
    await open(page, theme);
    await expect(page.locator("#generation-model")).not.toHaveValue("");
    await expect(page.locator("#positive-prompt")).toBeVisible();
    const prompt = await page.locator("#positive-prompt").boundingBox();
    expect(prompt.y + 100).toBeLessThan(page.viewportSize().height);
    const editor = await page.locator("#studio-editor").boundingBox();
    const dock = await page.locator("#generation-run-dock").boundingBox();
    expect(editor.y + editor.height).toBeLessThanOrEqual(dock.y);
    await expect(page.locator("[data-step], #workflow-next")).toHaveCount(0);
    await assertNoViewportOverflow(page, `${theme} studio`);
    await page.screenshot({ path: info.outputPath(`studio-${theme}.png`) });
    for (const mode of ["frames", "references"]) {
      await page.locator('[data-workflow-id="minimax-h3-video"]').click();
      await page.locator(`[name="video_mode"][value="${mode}"]`).check();
      await expect(page.locator("[data-image-slot]:visible")).toHaveCount(mode === "frames" ? 2 : 4);
      await assertNoViewportOverflow(page, `${theme} ${mode}`);
      await page.locator("#studio-settings-open").click();
      await expect(page.locator("#studio-settings")).toBeVisible();
      await assertNoViewportOverflow(page, `${theme} settings ${mode}`);
      await page.screenshot({ path: info.outputPath(`studio-${theme}-${mode}-settings.png`) });
      await page.keyboard.press("Escape");
      await expect(page.locator("#studio-settings-open")).toBeFocused();
    }
    await page.locator('[data-workflow-id="image-to-image"]').click();
    for (const preset of ["photoflow-krea2-edit", "photoflow-flux2-edit"]) {
      await page.locator(`[data-preset-id="${preset}"]`).click();
      await assertNoViewportOverflow(page, `${theme} ${preset}`);
      await page.locator("#studio-settings-open").click();
      await assertNoViewportOverflow(page, `${theme} ${preset} settings`);
      await page.keyboard.press("Escape");
    }
    expect(errors).toEqual([]);
  }
});

test("inactive references stay in the draft but not in generation or assistant requests", async ({ page }) => {
  await open(page);
  const image = fs.readFileSync(path.join(__dirname, "../../docs/frontend/prototype/assets/portrait.jpg"));
  await page.route("**/generate/upload", route => route.fulfill({ json: { value: "studio-reference.jpg" } }));
  await page.locator('[data-workflow-id="minimax-h3-video"]').click();
  await page.locator('[name="video_mode"][value="references"]').check();
  await page.locator("#source-image-3").setInputFiles({ name: "third.jpg", mimeType: "image/jpeg", buffer: image });
  await page.locator('[data-image-slot="3"] [data-image-role]').selectOption("wardrobe_object");
  await page.locator('[name="video_mode"][value="frames"]').check();
  await expect(page.locator("#studio-held-media")).toContainText("Фото 3");
  await page.locator("#positive-prompt").fill("A person walks through a sunny garden.");
  let body;
  await page.route("**/generate/preflight", route => {
    body = new URLSearchParams(route.request().postData());
    return route.fulfill({ json: { ok: true, checks: [] } });
  });
  await page.locator("#generation-preflight-button").click();
  await expect.poll(() => body?.get("input_image_3")).toBe("");
  await page.locator('[name="video_mode"][value="references"]').check();
  await expect(page.locator('[data-image-slot="3"] [data-image-name]')).toHaveText("third.jpg");
  await expect(page.locator('[data-image-slot="3"] [data-image-role]')).toHaveValue("wardrobe_object");
  await page.locator("#generation-preflight-button").click();
  await expect.poll(() => body?.get("input_image_3")).toBeTruthy();
  await page.locator('[data-workflow-id="text-to-image"]').click();
  await page.locator("#generation-preflight-button").click();
  await expect.poll(() => body?.get("input_image_3")).toBe("");
  let assistantBody;
  await page.route("**/generate/prompt-assistant", route => {
    assistantBody = new URLSearchParams(route.request().postData());
    return route.fulfill({ json: { prompt: "A person in a sunny garden.", model: "preview" } });
  });
  await page.locator("#prompt-assistant-enabled").check();
  await page.locator("#prompt-assistant-improve").click();
  await expect(page.locator("#prompt-assistant-review")).toBeVisible();
  expect(assistantBody.has("input_image_3")).toBe(false);
});

test("first reference role survives an edit-mode draft reload", async ({ page }) => {
  await open(page);
  await page.locator('[data-workflow-id="minimax-h3-video"]').click();
  await page.locator('[name="video_mode"][value="references"]').check();
  await page.locator('[data-image-slot="1"] [data-gallery-image-picker-open]').click();
  await page.getByRole("button", { name: "Выбрать AI-Gateway-Krea2-product.png", exact: true }).click();
  const role = page.locator('[data-image-slot="1"] [data-image-role]');
  await role.selectOption("wardrobe_object");
  await page.locator('[data-workflow-id="image-to-image"]').click();
  await expect(role).toHaveValue("base_scene");
  await expect(role).toBeDisabled();
  await page.locator(".studio-saved > summary").click();
  await page.locator("#generation-draft-save").click();
  await expect(page.locator("#generation-draft-status")).toHaveText("Сохранено");
  await page.reload();
  await expect(page.locator("#generation-draft-status")).toHaveText("Сохранено");
  await expect(role).toHaveValue("base_scene");
  await page.locator('[data-workflow-id="minimax-h3-video"]').click();
  await page.locator('[name="video_mode"][value="references"]').check();
  await expect(role).toHaveValue("wardrobe_object");
  await page.locator('[data-workflow-id="text-to-image"]').click();
  const size = page.locator('input[name="width"]');
  const textModeWidth = await size.inputValue();
  const aspect = await page.locator("#generation-aspect").inputValue();
  await page.locator('[data-image-slot="1"] [data-image-preview-image]').evaluate(image => image.dispatchEvent(new Event("load")));
  await expect(size).toHaveValue(textModeWidth);
  await expect(page.locator("#generation-aspect")).toHaveValue(aspect);
});

test("one launch waits for uploads and preserves the previous result", async ({ page }) => {
  await open(page);
  const previous = await page.locator("#generation-output-grid img").getAttribute("src");
  await page.locator('[data-workflow-id="image-to-image"]').click();
  await page.locator('#source-image').setInputFiles({ name: "portrait.jpg", mimeType: "image/jpeg", buffer: fs.readFileSync(path.join(__dirname, "../../docs/frontend/prototype/assets/portrait.jpg")) });
  await page.locator("#positive-prompt").fill("Change the jacket to green, preserve the person and scene.");
  let calls = 0;
  let launched;
  await page.route("**/generate/preflight", async route => {
    await new Promise(resolve => setTimeout(resolve, 200));
    return route.fulfill({ json: { ok: true, checks: [] } });
  });
  await page.route("**/generate/run", async route => {
    calls += 1;
    launched = new URLSearchParams(route.request().postData());
    return route.fulfill({ status: 400, json: { error: "Test-only launch stopped before GPU execution" } });
  });
  await page.locator("#generation-submit").dblclick();
  await expect.poll(() => calls).toBe(1);
  expect(launched.get("input_image")).toBeTruthy();
  await expect(page.locator("#generation-status")).toContainText("Test-only launch");
  await expect(page.locator("#generation-output-grid img")).toHaveAttribute("src", previous);
  if (page.viewportSize().width < 900) {
    await expect(page.locator("#studio-result-tab")).toHaveAttribute("aria-selected", "true");
    await page.locator("#studio-configure-tab").click();
  }
  await expect(page.locator("#positive-prompt")).toHaveValue("Change the jacket to green, preserve the person and scene.");
});
