const http = require('node:http');
const { test, expect } = require('@playwright/test');

// Mirrors the existing Gateway response policy; the preview normally omits it.
const gatewayCSP = "default-src 'self'; img-src 'self' blob: data:; base-uri 'none'; form-action 'self'; frame-ancestors 'none'; object-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'";

async function startCSPPreview(upstream) {
  // A real loopback response preserves address-space identity, unlike route.fulfill.
  const server = http.createServer(async (request, response) => {
    try {
      const target = new URL(upstream);
      const incoming = new URL(request.url, upstream);
      target.pathname = incoming.pathname;
      target.search = incoming.search;
      const result = await fetch(target, { headers: { cookie: request.headers.cookie || '' } });
      const headers = Object.fromEntries(result.headers);
      delete headers['content-encoding'];
      delete headers['content-length'];
      delete headers['transfer-encoding'];
      headers['content-security-policy'] = gatewayCSP;
      response.writeHead(result.status, headers);
      response.end(Buffer.from(await result.arrayBuffer()));
    } catch (error) { response.writeHead(502); response.end(String(error)); }
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  return {
    url: `http://127.0.0.1:${server.address().port}`,
    async close() { server.closeAllConnections(); await new Promise(resolve => server.close(resolve)); },
  };
}

for (const browserName of ['chromium', 'firefox', 'webkit']) {
  test(`${browserName} preference works with Gateway CSP and CSS fallback`, async ({ playwright, baseURL }, testInfo) => {
    test.skip(testInfo.project.name !== 'desktop-1440', 'engine compatibility runs once per browser');
    test.skip(browserName !== 'chromium' && process.env.UI_THEME_CROSS_BROWSER !== '1', 'opt-in engine matrix requires Firefox and WebKit runtimes');
    const preview = await startCSPPreview(baseURL);
    let browser;
    try {
      browser = await playwright[browserName].launch();
      const page = await browser.newPage({ baseURL: preview.url });
      const errors = [];
      page.on('pageerror', error => errors.push(error.message));
      page.on('console', message => { if (message.type() === 'error') errors.push(message.text()); });
      page.on('requestfailed', request => errors.push(`${request.url()}: ${request.failure()?.errorText}`));
      await page.emulateMedia({ colorScheme: 'light' });
      await page.goto('/preview/login');
      await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
      try {
        await expect(page.locator('body')).toHaveCSS('background-color', 'rgb(245, 246, 248)');
      } catch (error) {
        const state = await page.evaluate(() => ({
          theme: document.documentElement.dataset.theme,
          root: { background: getComputedStyle(document.documentElement).backgroundColor, canvas: getComputedStyle(document.documentElement).getPropertyValue('--canvas') },
          body: { background: getComputedStyle(document.body).backgroundColor, canvas: getComputedStyle(document.body).getPropertyValue('--canvas'), scheme: getComputedStyle(document.body).colorScheme },
          styles: [...document.styleSheets].map(sheet => ({ href: sheet.href, count: sheet.cssRules.length, rules: [...sheet.cssRules].filter(rule => /^(body|:root)\s*\{/.test(rule.cssText)).map(rule => rule.cssText) })),
        }));
        await testInfo.attach('browser-state', { body: Buffer.from(JSON.stringify({ errors, state }, null, 2)), contentType: 'application/json' });
        throw error;
      }
      await page.getByRole('combobox', { name: 'Цветовая тема' }).selectOption('dark');
      await page.reload();
      await expect(page.locator('body')).toHaveCSS('background-color', 'rgb(23, 25, 29)');
      await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
      expect(errors).toEqual([]);

      for (const preference of ['light', 'dark', 'system']) {
        const context = await browser.newContext({ javaScriptEnabled: false, colorScheme: 'dark' });
        try {
          await context.addCookies([{ name: 'ai_gateway_theme', value: preference, url: preview.url }]);
          const fallback = await context.newPage();
          await fallback.goto(`${preview.url}/preview/login`);
          await expect(fallback.locator('body')).toHaveCSS('background-color', preference === 'light' ? 'rgb(245, 246, 248)' : 'rgb(23, 25, 29)');
          await fallback.emulateMedia({ colorScheme: 'light' });
          await expect(fallback.locator('body')).toHaveCSS('background-color', preference === 'dark' ? 'rgb(23, 25, 29)' : 'rgb(245, 246, 248)');
        } finally { await context.close(); }
      }
    } finally {
      if (browser) await browser.close();
      await preview.close();
    }
  });
}
