const { test, expect } = require('@playwright/test');
const AxeBuilder = require('@axe-core/playwright').default;
const { settlePage, assertNoViewportOverflow, installPreviewClock, expectFocusInside } = require('./helpers.cjs');

async function assertContrast(page, selector) {
  const { violations } = await new AxeBuilder({ page }).include(selector).withRules(['color-contrast']).analyze();
  expect(violations.map(v => v.nodes.map(n => ({ target: n.target, summary: n.failureSummary })))).toEqual([]);
}

async function mediaPixels(page, media, path) {
  await media.scrollIntoViewIfNeeded();
  const box = await media.boundingBox();
  // Compare unobstructed media, excluding the theme-aware selection checkbox overlay.
  return page.screenshot({ path, animations: 'disabled', clip: {
    x: Math.ceil(box.x + box.width / 4), y: Math.ceil(box.y + box.height / 4),
    width: Math.floor(box.width / 2), height: Math.floor(box.height / 2),
  } });
}

for (const theme of ['light', 'dark']) {
  test(`${theme} controls, dialogs and media retain their states`, async ({ page, context, baseURL }, testInfo) => {
    test.skip(testInfo.project.name !== 'desktop-1440', 'state checks run in the canonical desktop project');
    await context.addCookies([{ name: 'ai_gateway_theme', value: theme, url: baseURL }]);
    await installPreviewClock(page);
    await page.goto('/preview/components');
    await settlePage(page);
    for (const name of ['Применить', 'Сбросить', 'Удалить']) {
      const button = page.getByRole('button', { name, exact: true });
      await button.hover();
      await assertContrast(page, '.ui-toolbar');
      await button.focus();
      await assertContrast(page, '.ui-toolbar');
    }
    const picker = page.getByRole('combobox', { name: 'Цветовая тема' });
    await picker.focus();
    await expect(page.locator('.theme-picker')).toHaveCSS('outline-style', 'solid');
    await expect(page.locator('.theme-picker')).toHaveCSS('outline-width', '2px');
    await page.getByRole('button', { name: 'Настройки аккаунта', exact: true }).click();
    await page.getByRole('button', { name: 'Выйти из аккаунта' }).hover();
    await assertContrast(page, '#workspace-account');
    await page.screenshot({ path: testInfo.outputPath(`menu-${theme}.png`) });

    await page.goto('/preview/generate');
    await settlePage(page);
    await page.getByRole('button', { name: /Задачи: активных/ }).click();
    await expect(page.getByRole('dialog', { name: 'Задачи и уведомления' })).toBeVisible();
    await assertContrast(page, '#notification-panel');
    await assertNoViewportOverflow(page, `${theme} notifications`);
    await page.screenshot({ path: testInfo.outputPath(`notifications-${theme}.png`) });

    await page.goto('/preview/gallery');
    await settlePage(page);
    const trigger = page.locator('[data-gallery-open]').first();
    await trigger.click();
    await expect(page.locator('#gallery-lightbox')).toBeVisible();
    await expectFocusInside(page, '#gallery-lightbox');
    await assertContrast(page, '#gallery-lightbox');
    await page.screenshot({ path: testInfo.outputPath(`viewer-${theme}.png`) });
    await page.keyboard.press('Escape');
    await expect(trigger).toBeFocused();
    const media = trigger.locator('img').first();
    await page.locator('h1').click();
    const before = await mediaPixels(page, media, testInfo.outputPath('media-before.png'));
    await page.getByRole('combobox', { name: 'Цветовая тема' }).selectOption(theme === 'light' ? 'dark' : 'light');
    await page.keyboard.press('Escape');
    await page.locator('h1').click();
    const after = await mediaPixels(page, media, testInfo.outputPath('media-after.png'));
    expect(after.equals(before), 'the same rendered media must keep its colors after a theme change').toBe(true);
  });

  test(`${theme} theme controls fit narrow screens and zoom reflow`, async ({ page, context, baseURL }, testInfo) => {
    test.skip(testInfo.project.name !== 'desktop-1440', 'explicit small viewport matrix');
    await context.addCookies([{ name: 'ai_gateway_theme', value: theme, url: baseURL }]);
    await installPreviewClock(page);
    for (const viewport of [{ width: 320, height: 568 }, { width: 640, height: 450 }, { width: 1280, height: 900 }]) {
      await page.setViewportSize(viewport);
      for (const route of ['login', 'generate', 'users']) {
        await page.goto(`/preview/${route}`);
        await settlePage(page);
        const picker = page.getByRole('combobox', { name: 'Цветовая тема' });
        await picker.scrollIntoViewIfNeeded();
        await expect(picker).toBeVisible();
        await assertNoViewportOverflow(page, `${route} ${theme} ${viewport.width}`);
        if (route === 'login') {
          const control = await picker.boundingBox();
          const form = await page.locator('form').boundingBox();
          expect(control.y + control.height).toBeLessThanOrEqual(form.y);
        }
      }
    }
  });
}
