const { test, expect } = require('@playwright/test');
const { settlePage, chooseWorkflow } = require('./helpers.cjs');
const { installFrozenAssets } = require('./frozen-assets.cjs');
test.beforeEach(async ({page}) => installFrozenAssets(page));

test('AIHUB resolution summaries follow quality and draft changes', async ({ page }) => {
  await page.goto('/preview/generate');
  await settlePage(page);
  await page.locator('[data-workflow-id="minimax-h3-video"]').click();
  await chooseWorkflow(page, "aihub-minimax-h3");
  const quality = page.locator('#minimax-video-quality');
  const upper = page.locator('#minimax-video-resolution-preview');
  const lower = page.locator('#generation-summary-title');
  const same = async () => {
    await expect(upper).toHaveText(await lower.textContent());
  };
  await quality.selectOption('720');
  await same();
  const before = await upper.textContent();
  await quality.selectOption('1080');
  await same();
  await expect(upper).not.toHaveText(before);
  await page.locator('[name="aihub_half_resolution"]').check();
  await same();
  await page.locator('[name="aihub_half_resolution"]').uncheck();
  await same();
});
