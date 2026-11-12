const { test, expect } = require('@playwright/test');
const AxeBuilder = require('@axe-core/playwright').default;
const { settlePage, assertNoViewportOverflow, installPreviewClock } = require('./helpers.cjs');

const jobID = 'comparison-fixture-001';
const assetID = '11111111111111111111111111111111';
const room = (color, width = 900, height = 1200) => `<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="${height}" viewBox="0 0 900 1200">
<rect width="900" height="1200" fill="#e2e5e1"/><path d="M0 880H900V1200H0" fill="#a2a7a0"/>
<path d="M50 100H360V700H50Z" fill="#f7f8f3"/><path d="M205 100V700M50 400H360" stroke="#b6bdb4" stroke-width="12"/>
<rect x="460" y="170" width="260" height="300" fill="#596e65"/><rect x="480" y="190" width="220" height="260" fill="#c3d1c3"/>
<path d="M600 450L690 260L650 450M490 450L550 320L600 450" fill="#7b9883"/>
<rect x="90" y="660" width="720" height="290" rx="40" fill="${color}"/><rect x="135" y="600" width="310" height="240" rx="35" fill="${color}"/>
<rect x="455" y="600" width="310" height="240" rx="35" fill="${color}"/><path d="M130 945V1010M770 945V1010" stroke="#52574e" stroke-width="26"/>
<path d="M450 665V910M140 830H760" stroke="#111a14" stroke-opacity=".18" stroke-width="6"/>
<ellipse cx="440" cy="1045" rx="210" ry="65" fill="#dadcd5"/><path d="M320 1045V1190M560 1045V1190" stroke="#5f685e" stroke-width="20"/>
<path d="M60 875L75 515M70 660Q-30 620 25 550M72 610Q155 530 145 480" stroke="#3d6250" stroke-width="20" fill="none"/>
</svg>`;

test.use({ hasTouch: true });
test.beforeEach(async ({ page }) => {
  await installPreviewClock(page);
  page.on('dialog', dialog => dialog.dismiss());
});

async function fixtures(page, { available = true, status = 200, aspect = false } = {}) {
  const state = { reads: [], sourceLoads: 0, status, available, delay: null };
  await page.route('**/comparison-edited.svg', route => route.fulfill({ contentType: 'image/svg+xml', body: room('#527960') }));
  await page.route(`**/generate/draft/asset?id=${assetID}`, route => {
    state.sourceLoads++;
    return route.fulfill({ contentType: 'image/svg+xml', body: room('#c6cbbf', aspect ? 1200 : 900, aspect ? 900 : 1200) });
  });
  await page.route('**/generate/jobs/comparison?**', async route => {
    state.reads.push(new URL(route.request().url()).searchParams.get('job'));
    if (state.delay) await state.delay;
    return route.fulfill({ status: state.status, json: state.status === 200 ? { available: state.available,
      original: { url: `/generate/draft/asset?id=${assetID}`, label: 'Фото 1' } } : { error: 'comparison_unavailable' } });
  });
  return state;
}

async function studio(page, options) {
  const state = await fixtures(page, options);
  const job = { job_id: jobID, state: 'completed', job_state: 'completed', template_id: 'image-to-image',
    model_name: 'Krea2', seed: 42, prompt: 'Change the sofa upholstery to green velvet.', retryable: true,
    created_at: new Date().toISOString(), media: [{ id: 501, media_type: 'image', url: '/comparison-edited.svg', filename: 'sofa-edited.png' }] };
  await page.route('**/generate/jobs', route => route.fulfill({ json: { jobs: [job], revision: 1 } }));
  await page.route('**/generate/jobs/detail?**', route => route.fulfill({ json: { job, transitions: [] } }));
  await page.route('**/generate/batches', route => route.fulfill({ json: { batches: [] } }));
  await page.route('**/generate/variants', route => route.fulfill({ json: { variants: [] } }));
  await page.goto(`/preview/generate?job=${jobID}`);
  await settlePage(page);
  if (page.viewportSize().width < 900 && !await page.locator('#generation-recent-inspector').isVisible()) await page.locator('#studio-result-tab').click();
  const preview = page.locator(`#generation-variant-list [data-job-id="${jobID}"] .generation-job-preview`);
  if (!await preview.isVisible()) await page.locator(`[data-recent-key="job:${jobID}"]`).click();
  await preview.click();
  await expect(page.locator('#generation-lightbox')).toBeVisible();
  return state;
}

async function synced(page, host, selector) {
  const geometry = await page.locator(host).evaluate((node, selector) => {
    const result = node.querySelector(selector), original = node.querySelector('.image-comparison-image');
    const style = media => ({ width: media.style.width, height: media.style.height, transform: media.style.transform });
    return { result: style(result), original: style(original), pageScale: visualViewport.scale };
  }, selector);
  expect(geometry.original).toEqual(geometry.result);
  expect(geometry.pageScale).toBe(1);
}

async function touchDrag(page, locator, dx) {
  const box = await locator.boundingBox(), x = box.x + box.width / 2, y = box.y + box.height / 2;
  const session = await page.context().newCDPSession(page);
  await session.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x, y, id: 1 }] });
  await session.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x: x + dx, y, id: 1 }] });
  await session.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
  await session.detach();
}

for (const theme of ['dark', 'light']) test(`registered before/after, keyboard and zoom in ${theme}`, async ({ page }, info) => {
  await page.addInitScript(theme => localStorage.setItem('ai_gateway_theme', theme), theme);
  const errors = []; page.on('pageerror', error => errors.push(error.message));
  const state = await studio(page);
  const root = page.locator('#generation-lightbox'), slider = root.getByRole('slider');
  await expect(slider).toBeVisible();
  await expect(root.locator('#generation-lightbox-title')).toHaveText('Сравнение');
  await expect(slider).toHaveAttribute('aria-valuenow', '50');
  expect(state.reads).toEqual([jobID]); expect(state.sourceLoads).toBe(1);
  await synced(page, '#generation-lightbox-viewport', '#generation-lightbox-image');
  await root.screenshot({ path: info.outputPath(`comparison-${theme}.png`) });
  await slider.focus(); await page.keyboard.press('Home');
  await expect(slider).toHaveAttribute('aria-valuenow', '0');
  await page.keyboard.press('End'); await expect(slider).toHaveAttribute('aria-valuenow', '100');
  await page.keyboard.press('PageDown'); await expect(slider).toHaveAttribute('aria-valuenow', '90');
  await page.keyboard.press('Shift+ArrowLeft'); await expect(slider).toHaveAttribute('aria-valuenow', '80');
  await root.locator('[data-lightbox-zoom="in"]').click();
  await synced(page, '#generation-lightbox-viewport', '#generation-lightbox-image');
  const transform = await root.locator('#generation-lightbox-image').getAttribute('style');
  await touchDrag(page, slider, -60);
  expect(Number(await slider.getAttribute('aria-valuenow'))).toBeLessThan(80);
  expect(await root.locator('#generation-lightbox-image').getAttribute('style')).toBe(transform);
  await root.locator('#generation-lightbox-compare').click(); await expect(slider).toBeHidden();
  await root.locator('#generation-lightbox-compare').click(); await expect(slider).toBeVisible();
  await expect(root.locator('#generation-lightbox-download')).toHaveAttribute('href', '/comparison-edited.svg?download=1');
  await assertNoViewportOverflow(page, 'comparison viewer');
  const audit = await new AxeBuilder({ page }).include('#generation-lightbox').analyze();
  expect(audit.violations.filter(item => ['serious', 'critical'].includes(item.impact))).toEqual([]);
  await page.keyboard.press('Escape'); await expect(root).toBeHidden();
  await expect(page.locator(`#generation-variant-list [data-job-id="${jobID}"] .generation-job-preview`)).toBeFocused();
  expect(errors).toEqual([]);
});

test('real mobile pinch and pan keep both layers registered without page zoom', async ({ page }, info) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await studio(page);
  await expect(page.locator('.image-comparison-divider')).toBeVisible();
  const viewport = page.locator('#generation-lightbox-viewport'), box = await viewport.boundingBox();
  const x = box.x + box.width / 2, y = box.y + box.height / 2;
  const session = await page.context().newCDPSession(page);
  await session.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x: x - 45, y, id: 1 }, { x: x + 45, y, id: 2 }] });
  await session.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x: x - 100, y: y - 35, id: 1 }, { x: x + 100, y: y + 35, id: 2 }] });
  await session.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
  await session.detach();
  await expect(page.locator('#generation-lightbox-scale')).not.toHaveText('100%');
  await synced(page, '#generation-lightbox-viewport', '#generation-lightbox-image');
  const before = await page.locator('#generation-lightbox-image').getAttribute('style');
  const pan = await page.context().newCDPSession(page);
  await pan.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x: x - 75, y: y + 100, id: 1 }] });
  await pan.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x: x - 20, y: y + 150, id: 1 }] });
  await pan.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] }); await pan.detach();
  expect(await page.locator('#generation-lightbox-image').getAttribute('style')).not.toBe(before);
  await synced(page, '#generation-lightbox-viewport', '#generation-lightbox-image');
  await page.screenshot({ path: info.outputPath('mobile-pinch.png') });
  await page.setViewportSize({ width: 844, height: 390 });
  await synced(page, '#generation-lightbox-viewport', '#generation-lightbox-image');
  await assertNoViewportOverflow(page, 'landscape comparison');
});

test('unavailable and temporarily failed originals keep ordinary results usable', async ({ page }) => {
  const state = await studio(page, { available: false });
  await expect(page.locator('#generation-lightbox-image')).toBeVisible();
  await expect(page.locator('#generation-lightbox-compare')).toBeHidden();
  expect(state.sourceLoads).toBe(0);
  await page.keyboard.press('Escape');
  state.available = true; state.status = 503;
  await page.locator(`#generation-variant-list [data-job-id="${jobID}"] .generation-job-preview`).click();
  const retry = page.locator('#generation-lightbox-compare');
  await expect(retry).toHaveAccessibleName('Повторить загрузку оригинала');
  await expect(page.locator('.image-comparison-notice')).toContainText('Результат доступен');
  state.status = 200; await retry.click();
  await expect(page.locator('.image-comparison-divider')).toBeVisible();
  expect(state.sourceLoads).toBe(1);
});

test('gallery uses the same curtain and releases it when navigating', async ({ page }, info) => {
  const state = await fixtures(page);
  await page.route(`**/generate/jobs/passport?job=${jobID}`, route => route.fulfill({ json: {
    version: 1, job_id: jobID, state: 'completed', evidence_state: 'not_recorded',
  } }));
  await page.goto('/preview/gallery'); await settlePage(page);
  const card = page.locator('[data-gallery-item][data-media-type="image"][data-sensitive="false"]').first();
  await card.evaluate((node, jobID) => {
    node.dataset.templateId = 'image-to-image'; node.dataset.jobId = jobID;
    node.querySelector('[data-gallery-open]').dataset.url = '/comparison-edited.svg';
  }, jobID);
  await card.locator('[data-gallery-open]').click();
  const root = page.locator('#gallery-lightbox'), slider = root.getByRole('slider');
  await expect(slider).toBeVisible();
  await root.locator('[data-zoom="in"]').click();
  await synced(page, '[data-viewer-stage]', '[data-gallery-lightbox-image]');
  await touchDrag(page, slider, 55);
  expect(Number(await slider.getAttribute('aria-valuenow'))).toBeGreaterThan(50);
  await root.screenshot({ path: info.outputPath('gallery-comparison.png') });
  await page.setViewportSize({ width: 320, height: 740 });
  await assertNoViewportOverflow(page, 'gallery comparison at 320px');
  const controls = await root.locator('.gallery-viewer-header button:visible, .gallery-viewer-actions > :visible').evaluateAll(nodes => nodes.map(node => {
    const rect = node.getBoundingClientRect(); return { width: rect.width, height: rect.height, left: rect.left, right: rect.right };
  }));
  for (const control of controls) {
    expect(control.left).toBeGreaterThanOrEqual(0); expect(control.right).toBeLessThanOrEqual(321);
    expect(control.width).toBeGreaterThanOrEqual(44); expect(control.height).toBeGreaterThanOrEqual(44);
  }
  await root.screenshot({ path: info.outputPath('gallery-comparison-320.png') });
  await root.locator('[data-viewer-next]').click();
  await expect(root.getByRole('slider')).toHaveCount(0);
  await expect(root.locator('[data-viewer-original]')).toBeHidden();
  expect(state.reads).toEqual([jobID]);
});

test('different aspect ratios preserve the original without stretching', async ({ page }) => {
  await studio(page, { aspect: true });
  await expect(page.locator('.image-comparison-divider')).toBeVisible();
  await expect(page.locator('.image-comparison-image')).toHaveCSS('object-fit', 'contain');
  await expect(page.locator('.image-comparison-notice')).toContainText('Пропорции изменились');
});

test('320px viewer keeps controls and the full curtain grip reachable at both edges', async ({ page }, info) => {
  await page.setViewportSize({ width: 320, height: 740 });
  await studio(page);
  const root = page.locator('#generation-lightbox'), slider = root.getByRole('slider');
  await expect(slider).toBeVisible();
  for (const key of ['Home', 'End']) {
    await slider.focus(); await page.keyboard.press(key);
    const grip = await slider.locator('.image-comparison-grip').boundingBox();
    expect(grip.x).toBeGreaterThanOrEqual(0); expect(grip.x + grip.width).toBeLessThanOrEqual(321);
    expect(grip.width).toBeGreaterThanOrEqual(44);
    await touchDrag(page, slider.locator('.image-comparison-grip'), key === 'Home' ? 2 : -2);
    const value = Number(await slider.getAttribute('aria-valuenow'));
    expect(key === 'Home' ? value : 100 - value).toBeLessThanOrEqual(2);
  }
  const buttons = await root.locator('.generation-lightbox-toolbar button:visible, .generation-lightbox-controls button:visible, .generation-lightbox-controls a:visible').evaluateAll(nodes => nodes.map(node => {
    const rect = node.getBoundingClientRect(); return { width: rect.width, height: rect.height, left: rect.left, right: rect.right };
  }));
  for (const button of buttons) {
    expect(button.width).toBeGreaterThanOrEqual(44); expect(button.height).toBeGreaterThanOrEqual(44);
    expect(button.left).toBeGreaterThanOrEqual(0); expect(button.right).toBeLessThanOrEqual(321);
  }
  await root.screenshot({ path: info.outputPath('comparison-320.png') });
});

test('closing fences a late original response and reopening binds a fresh read', async ({ page }) => {
  const state = await studio(page, { available: false });
  await expect.poll(() => state.reads.length).toBe(1);
  await page.keyboard.press('Escape');
  let release;
  state.available = true; state.delay = new Promise(resolve => { release = resolve; });
  await page.locator(`#generation-variant-list [data-job-id="${jobID}"] .generation-job-preview`).click();
  await expect.poll(() => state.reads.length).toBe(2);
  await page.keyboard.press('Escape'); release();
  state.delay = null;
  await page.locator(`#generation-variant-list [data-job-id="${jobID}"] .generation-job-preview`).click();
  await expect(page.locator('#generation-lightbox').getByRole('slider')).toBeVisible();
  expect(state.sourceLoads).toBe(1); expect(state.reads.length).toBe(3);
});
