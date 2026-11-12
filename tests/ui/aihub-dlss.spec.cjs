const { test, expect } = require('@playwright/test');
const { settlePage, chooseWorkflow } = require('./helpers.cjs');

test('DLSS is opt-in, saved, and excluded from ordinary workflows', async ({ page }, info) => {
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.goto('/preview/generate');
  await settlePage(page);
  await page.locator('[data-workflow-id="minimax-h3-video"]').click();
  await chooseWorkflow(page, "aihub-minimax-h3");
  await page.locator('[name="aihub_mode"][value="photo"]').check();
  const draft = page.locator('[name="aihub_half_resolution"]');
  await expect(draft).toBeVisible();
  await expect(draft).not.toBeChecked();
  await expect(page.locator('.aihub-advanced')).not.toHaveAttribute('open');
  const toggle = page.locator('[name="aihub_dlss_enabled"]');
  const strength = page.locator('[name="aihub_dlss_strength"]');
  await page.locator('[data-studio-settings-target="processing"]').click();
  await expect(toggle).not.toBeChecked();
  await expect(strength).toBeHidden();
  await toggle.check();
  await expect(strength).toBeVisible();
  await expect(strength).toHaveValue('1');
  await strength.fill('0.55');
  await strength.dispatchEvent('change');
  const values = () => page.locator('#generation-form').evaluate(form => Object.fromEntries(new FormData(form)));
  expect((await values()).aihub_dlss_enabled).toBe('true');
  expect((await values()).aihub_dlss_strength).toBe('0.55');
  await toggle.scrollIntoViewIfNeeded();
  await page.screenshot({ path: info.outputPath('dlss-controls.png') });
  await page.locator('#studio-settings-close').click();
  await draft.check();
  expect(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)).toBe(false);
  await page.locator('.studio-saved > summary').click();
  await page.locator('#generation-draft-save').click();
  await expect(page.locator('#generation-draft-status')).toHaveText('Сохранено');
  await page.reload();
  await settlePage(page);
  await expect(toggle).toBeChecked();
  await expect(strength).toHaveValue('0.55');
  await expect(draft).toBeChecked();
  await expect(draft).toBeVisible();
  await chooseWorkflow(page, "minimax-h3-video");
  await expect(toggle).toBeDisabled();
  expect((await values()).aihub_dlss_enabled).toBeUndefined();
  expect((await values()).aihub_dlss_strength).toBeUndefined();
  await chooseWorkflow(page, "aihub-minimax-h3");
  await expect(toggle).toBeChecked();
  for (const mode of ['video', 'carousel', 'photo']) {
    await page.locator(`[name="aihub_mode"][value="${mode}"]`).check();
    if (mode === 'carousel') {
      await expect(draft).toBeHidden();
      await expect(toggle).toBeDisabled();
      await page.locator('.aihub-advanced > summary').click();
      await page.locator('[name="aihub_carousel_pipeline"]').selectOption('legacy');
    }
    else await expect(draft).toBeVisible();
    await expect(toggle).toBeEnabled();
    await expect(strength).toBeEnabled();
  }
  expect(errors).toEqual([]);
});
