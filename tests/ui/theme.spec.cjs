const { test, expect } = require('@playwright/test');
const AxeBuilder = require('@axe-core/playwright').default;
const { settlePage, assertNoViewportOverflow, installPreviewClock } = require('./helpers.cjs');

const routes = ['login', 'invite', 'generate', 'gallery', 'gallery-empty', 'lora-training', 'components', 'profile', 'password', 'account-sessions', 'admin', 'users', 'user', 'invites', 'content', 'media', 'admin-lora-training', 'metrics', 'storage', 'service', 'mining', 'updates', 'workflows', 'admin-sessions', 'audit', 'suggestions', 'admin-suggestions', 'bad-gateway'];
const visualRoutes = new Set(['login', 'generate', 'gallery', 'lora-training', 'components', 'users', 'admin']);

for (const theme of ['light', 'dark']) {
  test(`all Gateway surfaces support ${theme} theme`, async ({ page, context }, testInfo) => {
    test.setTimeout(180000);
    await context.addCookies([{ name: 'ai_gateway_theme', value: theme, url: testInfo.project.use.baseURL || process.env.UI_PREVIEW_URL || 'http://127.0.0.1:18080' }]);
    await installPreviewClock(page);
    page.on('dialog', dialog => dialog.type() === 'beforeunload' ? dialog.accept() : dialog.dismiss());
    const failures = [];
    for (const route of routes) {
      await page.goto(`/preview/${route}`, { waitUntil: 'domcontentloaded' });
      await settlePage(page);
      await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
      await assertNoViewportOverflow(page, `${route} ${theme}`);
      if (visualRoutes.has(route)) await page.screenshot({ path: testInfo.outputPath(`${route}-${theme}.png`), fullPage: true });
      if (testInfo.project.name === 'desktop-1440') {
        const { violations } = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze();
        failures.push(...violations.filter(v => ['serious', 'critical'].includes(v.impact)).map(v => ({ route, id: v.id, nodes: v.nodes.map(n => ({ target: n.target, summary: n.failureSummary })) })));
      }
    }
    expect(failures).toEqual([]);
  });
}

test('theme follows the system only until a manual preference is selected', async ({ page, context }) => {
  await page.emulateMedia({ colorScheme: 'light' });
  await page.goto('/preview/login');
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  const picker = page.getByRole('combobox', { name: 'Цветовая тема' });
  await expect(picker).toHaveValue('system');
  await page.emulateMedia({ colorScheme: 'dark' });
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  await picker.selectOption('light');
  await page.reload();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  await expect(picker).toHaveValue('light');
  const second = await context.newPage();
  await second.goto('/preview/login');
  await expect(second.locator('html')).toHaveAttribute('data-theme', 'light');
  await picker.selectOption('dark');
  await expect(second.locator('html')).toHaveAttribute('data-theme', 'dark');
  await page.emulateMedia({ colorScheme: 'light' });
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  await picker.selectOption('system');
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  await second.close();
});

test('server preference survives without JavaScript', async ({ browser, baseURL }) => {
  const context = await browser.newContext({ javaScriptEnabled: false, colorScheme: 'light' });
  await context.addCookies([{ name: 'ai_gateway_theme', value: 'dark', url: baseURL }]);
  const page = await context.newPage();
  await page.goto(`${baseURL}/preview/login`);
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  await expect(page.locator('html')).toHaveCSS('color-scheme', 'dark');
  await context.close();
});

test('theme stays usable when browser storage is blocked', async ({ page }) => {
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.addInitScript(() => {
    Object.defineProperty(window, 'localStorage', { get() { throw new DOMException('Blocked', 'SecurityError'); } });
    Object.defineProperty(document, 'cookie', { get() { throw new DOMException('Blocked', 'SecurityError'); }, set() { throw new DOMException('Blocked', 'SecurityError'); } });
  });
  await page.emulateMedia({ colorScheme: 'light' });
  await page.goto('/preview/login');
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  await page.getByRole('combobox', { name: 'Цветовая тема' }).selectOption('dark');
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  expect(errors).toEqual([]);
});

test('removing a stored preference resets other tabs and survives reload', async ({ page, context }) => {
  await page.goto('/preview/login');
  await page.getByRole('combobox', { name: 'Цветовая тема' }).selectOption('light');
  const second = await context.newPage();
  await second.goto('/preview/login');
  await second.evaluate(() => localStorage.removeItem('ai_gateway_theme'));
  await expect(page.locator('html')).toHaveAttribute('data-theme-preference', 'system');
  await page.reload();
  await expect(page.getByRole('combobox', { name: 'Цветовая тема' })).toHaveValue('system');
  await second.close();
});
