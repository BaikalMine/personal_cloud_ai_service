const {test, expect} = require('@playwright/test');
const {installPreviewClock, settlePage, assertNoViewportOverflow, chooseWorkflow } = require('./helpers.cjs');

test.beforeEach(async ({page}) => {
  await installPreviewClock(page);
  await page.goto('/preview/generate');
  await settlePage(page);
  await page.locator('.scenario-choice[data-workflow-id="text-to-image"]').click();
  await chooseWorkflow(page, "moss-image");
});

test('MOSS has author defaults, no photo and responsive controls', async ({page}, testInfo) => {
  const root = page.locator('[data-comfy-generation]');
  await expect(root.locator('[name="model"]')).toHaveValue('moss:test');
  await expect(root.locator('[name="steps"]:enabled')).toHaveValue('30');
  await expect(root.locator('[name="cfg"]:enabled')).toHaveValue('3');
  await expect(root.locator('[name="negative_prompt"]')).toBeVisible();
  await expect(page.locator('[data-image-slot="1"]')).not.toBeVisible();
  await root.locator('[name="positive_prompt"]').fill('A blue ceramic teapot on a wooden table');
  await assertNoViewportOverflow(page);
  await page.screenshot({path:testInfo.outputPath('moss-form.png'),fullPage:true});
});

test('MOSS finishing and LoRA stay isolated when switching models', async ({page}, testInfo) => {
  await page.locator('[data-studio-settings-target="lora"]').click();
  const lora = page.locator('[data-lora-slots="moss"]').first().locator('select');
  await expect(lora).toBeEnabled();
  await lora.selectOption('MOSSImage/ceramic-style.safetensors');
  await expect(page.locator('[data-lora-slots="moss"]').nth(1)).toBeVisible();
  await page.keyboard.press('Escape');
  await page.locator('[data-studio-settings-target="processing"]').click();
  await page.locator('[name="moss_seedvr2"]').check();
  await expect(page.locator('[name="moss_upscale_resolution"]')).toBeEnabled();
  await page.locator('[name="moss_upscale_resolution"]').fill('2048');
  await page.locator('[name="moss_upscale_resolution"]').blur();
  await expect(page.locator('#generation-summary-title')).toContainText('2048');
  await assertNoViewportOverflow(page);
  await page.screenshot({path:testInfo.outputPath('moss-options.png'),fullPage:true});
  await page.keyboard.press('Escape');
  await chooseWorkflow(page, "photoflow-krea2");
  await expect(page.locator('[name="moss_seedvr2"]')).toBeDisabled();
  await expect(lora).toBeDisabled();
  await chooseWorkflow(page, "moss-image");
  await expect(lora).toHaveValue('MOSSImage/ceramic-style.safetensors');
});
