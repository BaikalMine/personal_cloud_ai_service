const { test, expect } = require('@playwright/test');
const AxeBuilder = require('@axe-core/playwright').default;
const { settlePage, assertNoViewportOverflow, installPreviewClock } = require('./helpers.cjs');

for (const theme of ['light', 'dark']) {
  test(`${theme} workspace navigation and account controls`, async ({ page, context, baseURL }, testInfo) => {
    await context.addCookies([{ name: 'ai_gateway_theme', value: theme, url: baseURL }]);
    await installPreviewClock(page);
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    for (const route of ['generate', 'users']) {
      await page.goto(`/preview/${route}`);
      await settlePage(page);
      await expect(page.locator('html')).toHaveClass(/shell-ready/);
      await expect(page.locator('.workspace-topbar')).toHaveCSS('height', page.viewportSize().width < 760 ? '60px' : '64px');
      const sidebar = page.locator('#workspace-navigation');
      const current = sidebar.locator('[aria-current="page"]');
      await expect(current).toHaveAttribute('href', route === 'generate' ? '/generate' : '/admin/users');
      if (page.viewportSize().width >= 1100) {
        await expect(page.getByRole('button', { name: 'Открыть меню', exact: true })).toBeHidden();
        const box = await sidebar.boundingBox();
        expect(box.width).toBe(224);
        const content = await page.locator('main').boundingBox();
        expect(content.x).toBeGreaterThanOrEqual(box.x + box.width);
      } else {
        const trigger = page.getByRole('button', { name: route === 'generate' && page.viewportSize().width < 760 ? 'Ещё' : 'Открыть меню', exact: true });
        await trigger.click();
        await expect(sidebar).toHaveAttribute('role', 'dialog');
        await expect(page.locator('main')).toHaveAttribute('inert', '');
        const close = page.getByRole('button', { name: 'Закрыть меню', exact: true });
        await expect(close).toBeFocused();
        await page.keyboard.press('Shift+Tab');
        await expect(sidebar.getByRole('link', { name: 'AI Gateway:', exact: false })).toBeFocused();
        await page.keyboard.press('Tab');
        await expect(close).toBeFocused();
        await page.screenshot({ path: testInfo.outputPath(`${route}-drawer-${theme}.png`) });
        await page.keyboard.press('Escape');
        await expect(trigger).toBeFocused();
        await expect(page.locator('main')).not.toHaveAttribute('inert', '');
      }
      await assertNoViewportOverflow(page, `${route} shell ${theme}`);
      await page.screenshot({ path: testInfo.outputPath(`${route}-shell-${theme}.png`) });
      const accountTrigger = page.getByRole('button', { name: 'Настройки аккаунта', exact: true });
      await accountTrigger.click();
      const account = page.getByRole('dialog', { name: 'Настройки аккаунта', exact: true });
      await expect(account).toBeVisible();
      await page.keyboard.press('Shift+Tab');
      await expect(page.getByRole('button', { name: 'Выйти из аккаунта', exact: true })).toBeFocused();
      await page.keyboard.press('Escape');
      await expect(accountTrigger).toBeFocused();
      await expect(account).toBeHidden();
      const audit = await new AxeBuilder({ page }).include('.workspace-topbar').include('#workspace-navigation').analyze();
      expect(audit.violations.filter(v => ['serious', 'critical'].includes(v.impact))).toEqual([]);
    }
    expect(errors).toEqual([]);
  });
}

test('task shortcuts share counts and return focus to the visible opener', async ({ page }) => {
  await installPreviewClock(page);
  await page.goto('/preview/generate');
  await settlePage(page);
  const nav = page.viewportSize().width < 760 ? page.locator('.workspace-mobile-nav') : page.locator('.workspace-navigation');
  const trigger = nav.getByRole('button', { name: 'Задачи', exact: true });
  await expect(trigger.locator('[data-shell-active-count]')).toHaveText('2');
  await trigger.click();
  const panel = page.getByRole('dialog', { name: 'Задачи и уведомления' });
  await expect(panel).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(trigger).toBeFocused();
  if (page.viewportSize().width < 1100) {
    await page.getByRole('button', { name: 'Открыть меню', exact: true }).click();
    await page.locator('.workspace-navigation').getByRole('button', { name: 'Задачи', exact: true }).click();
    await expect(panel).toBeVisible();
    await expect(page.locator('#workspace-navigation')).not.toHaveAttribute('role', 'dialog');
    await expect(page.locator('main')).not.toHaveAttribute('inert', '');
    await page.keyboard.press('Escape');
    await expect(page.locator(':focus')).toBeVisible();
  }
});

test('drawer resize and narrow account menu leave the workspace usable', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop-1440', 'explicit reflow matrix');
  await installPreviewClock(page);
  await page.goto('/preview/generate');
  await settlePage(page);
  for (const width of [320, 640, 768, 1280, 1920]) {
    await page.setViewportSize({ width, height: 720 });
    await assertNoViewportOverflow(page, `shell at ${width}`);
    if (width < 1100) {
      await page.getByRole('button', { name: 'Открыть меню', exact: true }).click();
      await page.setViewportSize({ width: 1440, height: 900 });
      await expect(page.locator('main')).not.toHaveAttribute('inert', '');
      await expect(page.locator('body')).not.toHaveClass(/workspace-overlay-open/);
      await expect(page.locator(':focus')).toBeVisible();
    }
    await page.getByRole('button', { name: 'Настройки аккаунта', exact: true }).click();
    await expect(page.getByRole('button', { name: 'Выйти из аккаунта' })).toBeVisible();
    await page.keyboard.press('Escape');
  }
});

test('navigation and logout remain available without JavaScript', async ({ browser, baseURL }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop-1440', 'explicit no-script matrix');
  for (const width of [390, 1280]) {
    const context = await browser.newContext({ javaScriptEnabled: false, viewport: { width, height: 900 } });
    const page = await context.newPage();
    await page.goto(`${baseURL}/preview/generate`);
    await expect(page.locator('.workspace-sidebar').getByRole('link', { name: 'Создать', exact: true })).toBeVisible();
    await expect(page.locator('.workspace-sidebar').getByRole('link', { name: 'Мои LoRA', exact: true })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Выйти из аккаунта', exact: true })).toBeVisible();
    await assertNoViewportOverflow(page, `no JS ${width}`);
    await context.close();
  }
});

test('gallery actions do not cover mobile navigation', async ({ page }, testInfo) => {
  await installPreviewClock(page);
  await page.goto('/preview/gallery');
  await settlePage(page);
  await page.locator('[data-gallery-select]').first().check();
  const actions = page.locator('[data-gallery-selection-bar]');
  await expect(actions).toBeVisible();
  if (page.viewportSize().width < 760) {
    for (const width of [320, 390, 640, 759]) {
      await page.setViewportSize({ width, height: 844 });
      const bar = await actions.boundingBox();
      const nav = await page.locator('.workspace-mobile-nav').boundingBox();
      expect(bar.y + bar.height, `gallery actions at ${width}px`).toBeLessThan(nav.y);
      await assertNoViewportOverflow(page, `gallery selection at ${width}px`);
      await page.screenshot({ path: testInfo.outputPath(`gallery-selection-${width}.png`) });
    }
  }
  await assertNoViewportOverflow(page, 'gallery selection with navigation');
});

test('account pages retain administration navigation', async ({ page, context, baseURL }, testInfo) => {
  for (const theme of ['light', 'dark']) {
    await context.addCookies([{ name: 'ai_gateway_theme', value: theme, url: baseURL }]);
    await page.goto('/preview/admin-password');
    await settlePage(page);
    await expect(page.locator('.workspace-mobile-nav')).toHaveCount(0);
    const sidebar = page.locator('#workspace-navigation');
    if (page.viewportSize().width < 1100) {
      await page.getByRole('button', { name: 'Открыть меню', exact: true }).click();
    }
    await expect(sidebar.getByRole('link', { name: 'Пользователи', exact: true })).toHaveAttribute('href', '/admin/users');
    await expect(sidebar.getByRole('link', { name: 'Перейти в студию', exact: true })).toHaveAttribute('href', /\/generate$/);
    await assertNoViewportOverflow(page, `admin account ${theme}`);
    await page.screenshot({ path: testInfo.outputPath(`admin-account-${theme}.png`) });
  }
});
