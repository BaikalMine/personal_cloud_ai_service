const { test, expect } = require('@playwright/test');
const AxeBuilder = require('@axe-core/playwright').default;
const { settlePage, assertNoViewportOverflow, expectFocusInside, installPreviewClock } = require('./helpers.cjs');
const open = async page => { await installPreviewClock(page); await page.goto('/preview/gallery'); await settlePage(page); };
const viewer = page => page.locator('#gallery-lightbox');
const card = (page, id) => page.locator('[data-gallery-item]').filter({ has: page.locator(`[data-media-id="${id}"]`) });
const mainImageReady = page => expect.poll(() => viewer(page).locator('[data-gallery-lightbox-image]').evaluate(image => image.complete && image.naturalWidth > 0)).toBe(true);
const openMenu = async (page, id) => { await card(page, id).locator('.gallery-card-menu summary').click(); return card(page, id); };

for (const theme of ['light', 'dark']) test(`results viewer, inspector and layout in ${theme}`, async ({ page }, testInfo) => {
  await page.addInitScript(value => localStorage.setItem('ai_gateway_theme', value), theme);
  const errors = []; page.on('pageerror', error => errors.push(error.message));
  await open(page);
  for (const media of await page.locator('[data-gallery-item]:not([data-sensitive="true"]) [data-gallery-preview-src]').all()) {
    await media.scrollIntoViewIfNeeded();
    await expect.poll(() => media.evaluate(item => item.tagName === 'VIDEO' ? item.readyState >= 2 : item.complete && item.naturalWidth > 0)).toBe(true);
  }
  await page.evaluate(() => window.scrollTo(0, 0));
  await expect(card(page, '5').locator('.media-library-play')).toHaveCSS('width', '40px');
  if (testInfo.project.name === 'tablet-768') {
    const first = await card(page, '1').boundingBox(), second = await card(page, '2').boundingBox();
    expect(Math.abs(first.y - second.y)).toBeLessThan(2);
  }
  await expect.poll(() => card(page, '1').locator('img').evaluate(image => image.complete && image.naturalWidth > 0)).toBe(true);
  await assertNoViewportOverflow(page, `library ${theme}`);
  expect((await new AxeBuilder({ page }).include('.user-gallery-page').analyze()).violations).toEqual([]);
  await page.screenshot({ path: testInfo.outputPath(`library-${theme}.png`), fullPage: true });
  await card(page, '1').locator('[data-gallery-open]').click(); await mainImageReady(page);
  const stage = viewer(page).locator('[data-viewer-stage]');
  const contained = await stage.evaluate(host => { const media = host.querySelector('[data-gallery-lightbox-image]').getBoundingClientRect(), box = host.getBoundingClientRect(); return media.left >= box.left - 1 && media.right <= box.right + 1 && media.top >= box.top - 1 && media.bottom <= box.bottom + 1; });
  expect(contained).toBe(true);
  await expect(viewer(page).locator('[data-inspector-status]')).toContainText('записаны');
  await expect(viewer(page).locator('[data-inspector-measurement-status]')).toContainText('Измерено');
  await page.screenshot({ path: testInfo.outputPath(`viewer-${theme}.png`) });
  await viewer(page).locator('[data-zoom="actual"]').click();
  await expect(viewer(page).locator('[data-zoom-value]')).toHaveText('100%');
  await viewer(page).locator('[data-zoom="in"]').click();
  await expect(viewer(page).locator('[data-zoom-value]')).toHaveText('125%');
  await viewer(page).locator('[data-zoom="fit"]').click();
  if (await viewer(page).locator('[data-viewer-tab="facts"]').isVisible()) await viewer(page).locator('[data-viewer-tab="facts"]').click();
  await expect(viewer(page).locator('[data-inspector-parameters]')).toContainText('284797972294826');
  await viewer(page).locator('[data-inspector-technical] summary').click();
  await expect(viewer(page).locator('[data-inspector-raw]')).toHaveAttribute('tabindex', '0');
  await expect(viewer(page).locator('[data-inspector-download-passport]')).toHaveAttribute('href', /^blob:/);
  await assertNoViewportOverflow(page, `inspector ${theme}`);
  await page.screenshot({ path: testInfo.outputPath(`inspector-${theme}.png`) });
  const violations = await new AxeBuilder({ page }).include('#gallery-lightbox').analyze();
  expect(violations.violations).toEqual([]);
  await page.keyboard.press('Escape'); await expect(viewer(page)).not.toBeVisible();
  await expect(card(page, '1').locator('[data-gallery-open]')).toBeFocused();
  expect(errors).toEqual([]);
});

test('results filters expose active constraints, preserve owner selection and reset', async ({ page }) => {
  await open(page);
  await card(page, '1').locator('[data-gallery-select]').check();
  await page.locator('[data-gallery-search]').fill('предмет');
  await expect(page.locator('[data-gallery-visible-count]')).toHaveText('1');
  await expect(page.locator('[data-gallery-active-filters]')).toContainText('предмет');
  await page.reload(); await settlePage(page);
  await expect(page.locator('[data-gallery-search]')).toHaveValue('предмет');
  await expect(card(page, '1').locator('[data-gallery-select]')).toBeChecked();
  await page.locator('[data-gallery-active-filters] button').click();
  await expect(page.locator('[data-gallery-visible-count]')).toHaveText('6');
  await expect(page.locator('[data-gallery-active-filters]')).not.toBeVisible();
});

test('results state uses the rendered account and ignores unowned or other-account state', async ({ page }) => {
  await page.addInitScript(() => {
    for (const owner of ['none', '2']) sessionStorage.setItem(`ai-gateway:gallery:v2:${owner}`, JSON.stringify({ filters: { search: 'other-account-filter' }, selected: ['2'] }));
  });
  await open(page);
  const root = page.locator('main[data-user-gallery]');
  await expect(root).toHaveAttribute('data-owner', '1');
  await expect(root).toHaveAttribute('data-can-train', 'true');
  await expect(root).toHaveAttribute('data-owner', await page.locator('[data-notification-center]').getAttribute('data-notification-owner'));
  await expect(page.locator('[data-gallery-search]')).toHaveValue('');
  await expect(card(page, '2').locator('[data-gallery-select]')).not.toBeChecked();
  await page.locator('[data-gallery-search]').fill('own-account-filter');
  await expect.poll(() => page.evaluate(() => JSON.parse(sessionStorage.getItem('ai-gateway:gallery:v2:1') || 'null')?.filters.search)).toBe('own-account-filter');
  expect(await page.evaluate(() => JSON.parse(sessionStorage.getItem('ai-gateway:gallery:v2:none')).filters.search)).toBe('other-account-filter');
});

test('results without an account scope do not restore or persist shared state', async ({ page }) => {
  await page.addInitScript(() => sessionStorage.setItem('ai-gateway:gallery:v2:none', JSON.stringify({ filters: { search: 'unowned-filter' }, selected: ['2'] })));
  await page.route('**/preview/gallery', async route => {
    const response = await route.fetch();
    const body = await response.text();
    expect(body).toContain('data-user-gallery data-owner="1"');
    await route.fulfill({ response, body: body.replace('data-user-gallery data-owner="1"', 'data-user-gallery data-owner=""') });
  });
  await open(page);
  await expect(page.locator('[data-gallery-search]')).toHaveValue('');
  await expect(card(page, '2').locator('[data-gallery-select]')).not.toBeChecked();
  await page.locator('[data-gallery-search]').fill('not-persisted');
  expect(await page.evaluate(() => JSON.parse(sessionStorage.getItem('ai-gateway:gallery:v2:none')).filters.search)).toBe('unowned-filter');
  expect(await page.evaluate(() => Object.keys(sessionStorage).filter(key => key.startsWith('ai-gateway:gallery:v2:')))).toEqual(['ai-gateway:gallery:v2:none']);
});

test('results sensitive outputs are not requested before explicit reveal', async ({ page }) => {
  const requests = [];
  page.on('request', request => requests.push(request.url()));
  await open(page);
  await expect(card(page, '3').locator('img')).not.toHaveAttribute('src', /.+/);
  await card(page, '3').locator('[data-gallery-open]').click();
  await expect(viewer(page).locator('[data-viewer-stage] button')).toHaveText('Показать контент 18+');
  expect(requests.filter(url => url.includes('passport?job=gallery-preview-job-002'))).toHaveLength(0);
  await viewer(page).locator('[data-viewer-stage] button').click(); await mainImageReady(page);
  await expect(viewer(page).locator('[data-inspector-status]')).toContainText('записаны');
  expect(requests.filter(url => url.includes('passport?job=gallery-preview-job-002'))).toHaveLength(1);
});

test('results inspector rejects late replies and preserves exact raw seed', async ({ page }, testInfo) => {
  let release; const waiting = new Promise(resolve => { release = resolve; });
  let entered; const firstEntered = new Promise(resolve => { entered = resolve; });
  await page.route('**/generate/jobs/passport?job=gallery-preview-job-000', async route => { entered(); await waiting; await route.fulfill({ json: { version: 1, job_id: 'gallery-preview-job-000', seed: '1', outputs: [], execution: { resolved_input: { Positive: 'STALE SOURCE' } } } }).catch(() => {}); });
  await page.route('**/generate/jobs/passport?job=gallery-preview-job-001', route => route.fulfill({ contentType: 'application/json', body: '{"version":1,"job_id":"gallery-preview-job-001","seed":"9007199254740993","evidence_state":"submission_recorded","submission_confirmed":true,"execution":{"seed":"9007199254740993","resolved_input":{"Positive":"CURRENT SOURCE"},"graph":{"seed":9007199254740993}},"outputs":[{"media_id":"2"}]}' }));
  await open(page); await card(page, '1').locator('[data-gallery-open]').click(); await firstEntered;
  await viewer(page).locator('[data-viewer-next]').click(); release();
  await expect(viewer(page).locator('[data-inspector-prompt]')).toHaveText('CURRENT SOURCE');
  await expect(viewer(page).locator('[data-inspector-parameters]')).toContainText('9007199254740993');
  if (await viewer(page).locator('[data-viewer-tab="facts"]').isVisible()) await viewer(page).locator('[data-viewer-tab="facts"]').click();
  await viewer(page).locator('[data-inspector-technical] summary').click();
  const raw = await viewer(page).locator('[data-inspector-download-passport]').evaluate(async link => (await fetch(link.href)).text());
  expect(raw).toContain('"graph":{"seed":9007199254740993}');
  await page.screenshot({ path: testInfo.outputPath('exact-seed.png') });
});

test('results paired image scale and pan are linked', async ({ page }, testInfo) => {
  await open(page);
  await card(page, '1').locator('[data-gallery-select]').check(); await card(page, '2').locator('[data-gallery-select]').check();
  await page.locator('[data-gallery-bulk-compare]').click();
  const compare = page.locator('#gallery-compare-dialog');
  await expect(compare).toBeVisible();
  await expect.poll(() => compare.locator('img').evaluateAll(images => images.length === 2 && images.every(image => image.naturalWidth > 0))).toBe(true);
  await compare.locator('[data-zoom="actual"]').click();
  const scales = await compare.locator('img').evaluateAll(images => images.map(image => image.getBoundingClientRect().width / image.naturalWidth));
  expect(scales).toEqual([1, 1]);
  const panes = compare.locator('.gallery-media-viewport');
  await panes.first().focus(); await page.keyboard.press('Shift+ArrowRight');
  await expectFocusInside(page, '#gallery-compare-dialog');
  await compare.locator('[data-zoom="fit"]').click();
  await assertNoViewportOverflow(page, 'comparison');
  await page.screenshot({ path: testInfo.outputPath('compare-images.png') });
  await page.keyboard.press('Escape'); await expect(compare).not.toBeVisible();
  await expect(compare.locator('img,video')).toHaveCount(0);
});

test('results paired videos use common play, pause and seeking', async ({ page }, testInfo) => {
  await open(page);
  // Use two real copies of the existing video fixture, preserving separate IDs.
  await card(page, '2').evaluate(card => { card.dataset.mediaType = 'video'; const button = card.querySelector('[data-gallery-open]'); button.dataset.mediaType = 'video'; button.dataset.url = '/preview/task-video.mp4'; button.dataset.filename = 'second-video.mp4'; });
  await card(page, '2').locator('[data-gallery-select]').check(); await card(page, '5').locator('[data-gallery-select]').check(); await page.locator('[data-gallery-bulk-compare]').click();
  const compare = page.locator('#gallery-compare-dialog');
  await expect(compare.locator('[data-compare-play]')).toBeEnabled();
  await compare.locator('video').evaluateAll(videos => {
    window.compareMediaEvents = [];
    videos.forEach((video, index) => ['playing', 'waiting', 'pause', 'seeking', 'seeked', 'error', 'ended'].forEach(type => video.addEventListener(type, () => {
      window.compareMediaEvents.push({ index, type, time: video.currentTime, ready: video.readyState, paused: video.paused, error: video.error?.message });
    })));
  });
  await compare.locator('[data-compare-play]').click();
  try { await expect.poll(() => compare.locator('video').evaluateAll(videos => videos.every(video => video.currentTime > 0.1))).toBe(true); }
  catch (error) {
    const events = JSON.stringify(await page.evaluate(() => window.compareMediaEvents), null, 2);
    console.error(events);
    await testInfo.attach('media-events.json', { body: events, contentType: 'application/json' }); throw error;
  }
  await compare.locator('[data-compare-play]').click();
  const time = await compare.locator('video').evaluateAll(videos => videos.map(video => ({ time: video.currentTime, paused: video.paused })));
  expect(time.every(item => item.paused)).toBe(true); expect(Math.abs(time[0].time - time[1].time)).toBeLessThan(0.15);
  await compare.locator('[data-compare-time]').fill('500');
  await expect.poll(() => compare.locator('video').evaluateAll(videos => Math.abs(videos[0].currentTime - videos[1].currentTime))).toBeLessThan(0.05);
  await page.screenshot({ path: testInfo.outputPath('compare-videos.png') });
  await page.keyboard.press('Escape');
});

test('results compatible destinations transfer an exact last frame slot', async ({ page }) => {
  await open(page); await card(page, '1').locator('[data-gallery-use-open]').click();
  const use = page.locator('#gallery-use-dialog');
  await use.getByText('Видео: последний кадр', { exact: true }).click();
  await expect(use.locator('select[name="workflow"] option')).toHaveCount(1);
  await expect(use.locator('select[name="slot"]')).toHaveValue('2');
  await expect(use.locator('[data-gallery-role-field]')).not.toBeVisible();
  const navigation = page.waitForURL(url => url.pathname === '/preview/generate' || url.pathname === '/generate');
  await use.locator('[data-gallery-use-submit]').click(); await navigation;
  await expect(page.locator('#generation-repeat-notice')).toContainText('слот 2');
  await expect(page.locator('#generation-model')).toHaveValue('minimax:h3');
});

test('results import creates one dataset and opens its exact image', async ({ page }) => {
  await open(page); await card(page, '1').locator('[data-gallery-use-open]').click();
  const use = page.locator('#gallery-use-dialog');
  await use.getByText('Датасет LoRA', { exact: true }).click(); await use.locator('[name="dataset_name"]').fill('Gallery import');
  await use.locator('[data-gallery-use-submit]').click();
  await expect(use.locator('[data-gallery-use-status]')).toHaveText('Изображение добавлено в датасет.');
  const href = await use.locator('[data-gallery-use-dataset-link]').getAttribute('href'); expect(href).toMatch(/^\/train-lora\?dataset=.+&image=.+/);
  const list = await page.request.get('/api/lora-datasets'); const data = await list.json(); expect(data.datasets).toHaveLength(1); expect(data.datasets[0].image_count).toBe(1);
});

test('results import lost acknowledgement restores through GET without another POST', async ({ page }) => {
  let posts = 0;
  await page.route('**/api/lora-datasets/gallery-import', async route => { if (route.request().method() !== 'POST') return route.continue(); posts++; await route.fetch(); await route.abort(); });
  await open(page); await card(page, '1').locator('[data-gallery-use-open]').click();
  const use = page.locator('#gallery-use-dialog'); await use.getByText('Датасет LoRA', { exact: true }).click(); await use.locator('[data-gallery-use-submit]').click();
  await expect(use.locator('[data-gallery-import-recovery]')).toBeVisible();
  await page.reload();
  await expect(use.locator('[data-gallery-use-status]')).toHaveText('Изображение добавлено в датасет.');
  expect(posts).toBe(1);
});

test('results unknown import can be fenced off before another transfer', async ({ page }) => {
  let posts = 0;
  await page.route('**/api/lora-datasets/gallery-import', route => { if (route.request().method() === 'POST') { posts++; return route.abort(); } return route.continue(); });
  await open(page); await card(page, '1').locator('[data-gallery-use-open]').click();
  const use = page.locator('#gallery-use-dialog'); await use.getByText('Датасет LoRA', { exact: true }).click(); await use.locator('[data-gallery-use-submit]').click();
  await expect(use.locator('[data-gallery-use-status]')).toContainText('неизвестен');
  await use.locator('[data-gallery-import-check]').click(); await expect(use.locator('[data-gallery-use-status]')).toContainText('не доказывает');
  await use.locator('[data-gallery-import-discard]').click(); await expect(use.locator('[data-gallery-use-status]')).toContainText('Перенос отменён');
  expect(posts).toBe(1);
});

test('results editor resolves old media by exact ID and rejects invalid slots', async ({ page }) => {
  let lookups = 0;
  await page.route('**/generate/library/images?media=201', route => { lookups++; return route.fulfill({ json: { media_id: '201', images: [{ id: 201, url: '/preview/studio-result.jpg', filename: 'older-result.jpg', expires_unix: Date.now() + 86400000 }] } }); });
  await page.goto('/preview/generate?template=image-to-image&workflow=photoflow-flux2-edit&library_model=flux2:test&media=201&slot=3&role=style');
  await expect(page.locator('#generation-repeat-notice')).toContainText('older-result.jpg');
  await expect(page.locator('#generation-repeat-notice')).toContainText('слот 3'); expect(lookups).toBe(1);
  page.on('dialog', dialog => dialog.accept());
  await page.goto('/preview/generate?template=image-to-image&workflow=photoflow-krea2-edit&media=201&slot=999&role=style');
  await expect(page.locator('#generation-repeat-notice')).toContainText('Слот недоступен'); expect(lookups).toBe(1);
  await page.goto('/preview/generate?template=minimax-h3-video&workflow=minimax-h3-video&media=201&slot=1&role=last_frame');
  await expect(page.locator('#generation-repeat-notice')).toContainText('Роль не соответствует слоту'); expect(lookups).toBe(1);
  await page.goto('/preview/generate?template=minimax-h3-video&workflow=minimax-h3-video&library_model=minimax:eros&media=201&slot=2&role=last_frame');
  await expect(page.locator('#generation-repeat-notice')).toContainText('Роль недоступна'); expect(lookups).toBe(1);
  await page.goto('/preview/generate?template=image-to-image&workflow=photoflow-flux2-edit&library_model=removed:model&media=201&slot=3&role=style');
  await expect(page.locator('#generation-repeat-notice')).toContainText('Модель недоступна'); expect(lookups).toBe(1);
});

test('results import works without randomUUID and refuses dispatch without recovery storage', async ({ page }) => {
  await page.addInitScript(() => {
    Object.defineProperty(crypto, 'randomUUID', { value: undefined });
    const set = Storage.prototype.setItem;
    Storage.prototype.setItem = function (key, value) {
      if (key.startsWith('ai-gateway:gallery-import:') && !window.allowImportStorage) throw new DOMException('Denied', 'SecurityError');
      return set.call(this, key, value);
    };
  });
  let posts = 0;
  page.on('request', request => { if (request.method() === 'POST' && request.url().endsWith('/api/lora-datasets/gallery-import')) posts++; });
  await open(page); await card(page, '1').locator('[data-gallery-use-open]').click();
  const use = page.locator('#gallery-use-dialog');
  await use.getByText('Датасет LoRA', { exact: true }).click();
  await use.locator('[data-gallery-use-submit]').click();
  await expect(use.locator('[data-gallery-use-status]')).toContainText('Запрос не отправлен');
  expect(posts).toBe(0);
  await page.evaluate(() => { window.allowImportStorage = true; });
  await use.locator('[data-gallery-use-submit]').click();
  await expect(use.locator('[data-gallery-use-status]')).toContainText('Изображение добавлено');
  expect(posts).toBe(1);
});

test('results 320px controls and native dialogs stay in viewport', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'mobile-390'); await page.setViewportSize({ width: 320, height: 740 }); await open(page);
  await assertNoViewportOverflow(page, '320 library');
  await card(page, '1').locator('[data-gallery-open]').click(); await mainImageReady(page);
  await page.screenshot({ path: testInfo.outputPath('viewer-320.png') });
  await viewer(page).locator('[data-viewer-tab="facts"]').click(); await assertNoViewportOverflow(page, '320 inspector');
  await page.screenshot({ path: testInfo.outputPath('inspector-320.png') }); await page.keyboard.press('Escape');
  await card(page, '1').locator('[data-gallery-use-open]').click(); await page.locator('#gallery-use-dialog').getByText('Датасет LoRA', { exact: true }).click();
  await assertNoViewportOverflow(page, '320 use'); await page.screenshot({ path: testInfo.outputPath('use-320.png') });
});

test('results existing dataset import retains both images and rejects duplicates', async ({ page }) => {
  await open(page);
  const use = page.locator('#gallery-use-dialog');
  const add = async (media, dataset) => {
    await card(page, media).locator('[data-gallery-use-open]').click();
    await use.getByText('Датасет LoRA', { exact: true }).click();
    if (dataset) await use.locator('[name="dataset"]').selectOption(dataset);
    await use.locator('[data-gallery-use-submit]').click();
    await expect(use.locator('[data-gallery-use-dataset-link]')).toBeVisible();
    const target = new URL(await use.locator('[data-gallery-use-dataset-link]').getAttribute('href'), page.url());
    return { dataset: target.searchParams.get('dataset'), image: target.searchParams.get('image') };
  };
  const first = await add('1'); await page.keyboard.press('Escape');
  const second = await add('2', first.dataset); expect(second.dataset).toBe(first.dataset); expect(second.image).not.toBe(first.image);
  await page.keyboard.press('Escape');
  const duplicate = await add('1', first.dataset); expect(duplicate).toEqual(first);
  await expect(use.locator('[data-gallery-use-status]')).toContainText('Повтор не добавлен');
  const data = await (await page.request.get('/api/lora-datasets')).json();
  expect(data.datasets).toHaveLength(1); expect(data.datasets[0].image_count).toBe(2);
});

test('results late metadata response cannot overwrite another open editor', async ({ page }) => {
  let finish;
  await page.route('**/generate/library/metadata', route => new Promise(resolve => { finish = async () => { await route.fulfill({ json: { ok: true } }); resolve(); }; }));
  await open(page);
  await (await openMenu(page, '1')).locator('[data-gallery-metadata-open]').click();
  const dialog = page.locator('#gallery-metadata-dialog');
  await dialog.locator('[name="tags"]').fill('sunlight; linen'); await dialog.locator('[type="submit"]').click();
  await expect.poll(() => !!finish).toBe(true); await page.keyboard.press('Escape');
  await (await openMenu(page, '2')).locator('[data-gallery-metadata-open]').click();
  await dialog.locator('[name="tags"]').fill('unsaved-second');
  await finish();
  await expect(dialog).toBeVisible(); await expect(dialog.locator('[name="tags"]')).toHaveValue('unsaved-second');
  await expect(card(page, '1').locator('[data-gallery-tag]')).toHaveText(['sunlight', 'linen']);
  await page.keyboard.press('Escape');
  await expect(card(page, '2').locator('.gallery-card-menu summary')).toBeFocused();
});

test('results inspector recovers from failed reads and removes expired media', async ({ page }) => {
  let reject = true;
  await page.route('**/generate/jobs/passport?**', route => reject ? route.fulfill({ status: 503, json: { error: 'unavailable' } }) : route.continue());
  await open(page); await card(page, '1').locator('[data-gallery-open]').click(); await mainImageReady(page);
  if (await viewer(page).locator('[data-viewer-tab="facts"]').isVisible()) await viewer(page).locator('[data-viewer-tab="facts"]').click();
  await expect(viewer(page).locator('[data-inspector-status]')).toContainText('Не удалось');
  reject = false; await viewer(page).locator('[data-inspector-retry]').click();
  await expect(viewer(page).locator('[data-inspector-status]')).toContainText('записаны');
  await page.keyboard.press('Escape');
  await card(page, '1').locator('[data-gallery-open]').evaluate(button => { button.dataset.expires = String(Date.now() - 1); });
  await card(page, '1').locator('[data-gallery-open]').click();
  await expect(viewer(page).locator('[data-inspector-status]')).toContainText('срок хранения истёк');
  await expect(viewer(page).locator('img,video')).toHaveCount(0);
  await expect(viewer(page).locator('[data-viewer-use]')).toBeDisabled();
});

test('results keyboard and 200 percent reflow preserve reachable actions', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop-1440');
  await page.setViewportSize({ width: 640, height: 450 }); await open(page);
  const trigger = card(page, '1').locator('[data-gallery-open]'); await trigger.focus(); await page.keyboard.press('Enter'); await mainImageReady(page);
  await expectFocusInside(page, '#gallery-lightbox');
  const facts = viewer(page).locator('[data-viewer-tab="facts"]'); await facts.focus(); await page.keyboard.press('Home');
  await expect(viewer(page).locator('[data-viewer-tab="media"]')).toBeFocused();
  await page.keyboard.press('End'); await expect(facts).toBeFocused();
  await assertNoViewportOverflow(page, '200 percent equivalent reflow');
  await page.screenshot({ path: testInfo.outputPath('inspector-reflow.png') });
  await page.keyboard.press('Escape'); await expect(trigger).toBeFocused();
  await card(page, '1').locator('[data-gallery-use-open]').click();
  const use = page.locator('#gallery-use-dialog');
  await use.getByText('Датасет LoRA', { exact: true }).click();
  await use.locator('[name="dataset_name"]').fill('Keyboard dataset');
  await expect(use.locator('[data-gallery-use-submit]')).toBeInViewport();
  await expectFocusInside(page, '#gallery-use-dialog');
  await page.screenshot({ path: testInfo.outputPath('use-reflow.png') });
});
