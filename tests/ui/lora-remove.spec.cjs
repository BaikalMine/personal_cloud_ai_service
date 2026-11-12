const { test, expect } = require("@playwright/test");
const { settlePage, installPreviewClock, assertNoViewportOverflow, chooseWorkflow } = require("./helpers.cjs");

test.beforeEach(async ({ page }) => {
  page.on("dialog", d => d.type() === "beforeunload" ? d.accept() : d.dismiss());
  await installPreviewClock(page);
  await page.goto("/preview/generate");
  await settlePage(page);
  await page.locator('[data-studio-settings-target="lora"]').click();
});

const rows = page => page.locator('[data-lora-slots="krea"]');
const names = ["lenovo_krea2.safetensors", "Krea2-realism-V2.safetensors", "lenovo_krea2.safetensors"];
const weights = ["-0.25", "0.65", "-1.2"];
const clips = ["0", "0.7", "-0.8"];
async function fill(page) {
  for (let i = 0; i < 3; i++) {
    await rows(page).nth(i).locator("select").selectOption(names[i]);
    await rows(page).nth(i).locator(".generation-lora-model").fill(weights[i]);
    await rows(page).nth(i).locator('[name^="lora_clip_strength_"]').fill(clips[i]);
  }
}
async function check(page, indices) {
  for (let i = 0; i < indices.length; i++) {
    const row = rows(page).nth(i);
    await expect(row.locator("select")).toHaveValue(names[indices[i]]);
    await expect(row.locator(".generation-lora-model")).toHaveValue(weights[indices[i]]);
    await expect(row.locator('[name^="lora_clip_strength_"]')).toHaveValue(clips[indices[i]]);
    await expect(row.locator(".generation-lora-remove")).toBeEnabled();
    await expect(row.locator(".generation-lora-remove svg")).toHaveCount(1);
  }
  await expect(rows(page).nth(indices.length).locator("select")).toHaveValue("");
  await expect(rows(page).nth(indices.length).locator(".generation-lora-remove")).toBeHidden();
  await expect(page.locator('[data-lora-slots="krea"]:not([hidden])')).toHaveCount(indices.length + 1);
}

for (const removed of [0, 1, 2]) {
  test(`remove slot ${removed + 1} preserves remaining weights, payload and draft`, async ({ page }, info) => {
    await fill(page);
    const button = rows(page).nth(removed).locator(".generation-lora-remove");
    const box = await button.boundingBox();
    expect(box.width).toBeGreaterThanOrEqual(44);
    expect(box.height).toBeGreaterThanOrEqual(44);
    await button.click();
    const remaining = [0, 1, 2].filter(i => i !== removed);
    await check(page, remaining);
    await expect(page.locator(".generation-lora-remove:focus")).toHaveCount(1);
    await assertNoViewportOverflow(page, "LoRA removal");
    await page.screenshot({ path: info.outputPath("lora-remove.png") });
    await page.keyboard.press("Escape");
    await page.locator("#positive-prompt").fill("A ceramic vase.");
    let body;
    await page.route("**/generate/preflight", route => {
      body = new URLSearchParams(route.request().postData());
      return route.fulfill({ json: { ok: true, checks: [] } });
    });
    await page.locator("#generation-preflight-button").click();
    await expect.poll(() => body?.get("lora_1")).toBe(names[remaining[0]]);
    for (let i = 0; i < 2; i++) {
      expect(body.get(`lora_model_strength_${i + 1}`)).toBe(weights[remaining[i]]);
      expect(body.get(`lora_clip_strength_${i + 1}`)).toBe(clips[remaining[i]]);
    }
    if (await page.locator(".studio-saved").getAttribute("open") === null) await page.locator(".studio-saved > summary").click();
    await page.locator("#generation-draft-save").click();
    await expect(page.locator("#generation-draft-status")).toHaveText("Сохранено");
    await page.reload();
    await settlePage(page);
    await page.locator('[data-studio-settings-target="lora"]').click();
    await check(page, remaining);
  });
}

test("dropdown clearing and repeated removal leave one reusable empty slot", async ({ page }) => {
  await fill(page);
  await rows(page).nth(1).locator("select").selectOption("");
  await check(page, [0, 2]);
  await rows(page).nth(0).locator(".generation-lora-remove").click();
  await check(page, [2]);
  await rows(page).nth(0).locator(".generation-lora-remove").click();
  await check(page, []);
  await expect(rows(page).nth(0).locator("select")).toBeFocused();
  await rows(page).nth(0).locator("select").selectOption(names[1]);
  await expect(rows(page).nth(1)).toBeVisible();
  await expect(rows(page).nth(0).locator(".generation-lora-remove")).toBeVisible();
});

for (const [kind, template, workflow] of [["flux", "image-to-image", "photoflow-flux2-edit"], ["minimax", "minimax-h3-video", "minimax-h3-video"]]) {
  test(`${kind} removal preserves later slots and does not modify Krea`, async ({ page }, info) => {
    await page.keyboard.press("Escape");
    await page.locator(`[data-workflow-id="${template}"]`).click();
    await chooseWorkflow(page, workflow);
    await page.locator('[data-studio-settings-target="lora"]').click();
    const familyRows = page.locator(`[data-lora-slots="${kind}"]`);
    const options = await familyRows.first().locator("select option").evaluateAll(options => options.map(option => option.value).filter(Boolean));
    const kreaBefore = await rows(page).locator("select").evaluateAll(selects => selects.map(select => select.value));
    for (let i = 0; i < 3; i++) {
      await familyRows.nth(i).locator("select").selectOption(options[i % options.length]);
      await familyRows.nth(i).locator(".generation-lora-model").fill(weights[i]);
    }
    await familyRows.nth(1).locator(".generation-lora-remove").click();
    await expect(familyRows.nth(1).locator("select")).toHaveValue(options[2 % options.length]);
    await expect(familyRows.nth(1).locator(".generation-lora-model")).toHaveValue(weights[2]);
    expect(await rows(page).locator("select").evaluateAll(selects => selects.map(select => select.value))).toEqual(kreaBefore);
    await assertNoViewportOverflow(page, `${kind} LoRA removal`);
    await page.screenshot({ path: info.outputPath(`${kind}-lora.png`) });
  });
}
