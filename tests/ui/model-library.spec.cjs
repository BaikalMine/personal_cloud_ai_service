const { test, expect } = require('@playwright/test');
const AxeBuilder = require('@axe-core/playwright').default;
const { assertNoViewportOverflow } = require('./helpers.cjs');

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => { window.EventSource = class extends EventTarget { close() {} }; });
});

test('catalog and description fit both themes and retain touch targets', async ({ page }, info) => {
  const writes = [];
  page.on('request', request => { if (request.method() === 'POST' && request.url().includes('/models/api/')) writes.push(request.url()); });
  for (const theme of ['light', 'dark']) {
    await page.emulateMedia({ colorScheme: theme });
    await page.goto('/preview/model-library');
    await expect(page.locator('.model-card')).toHaveCount(3);
    await expect.poll(() => page.locator('.model-card img').first().evaluate(img => img.complete && img.naturalWidth > 100)).toBe(true);
    await assertNoViewportOverflow(page, 'model catalog');
    await page.screenshot({ path: info.outputPath(`model-catalog-${theme}.png`) });
    await page.locator('.model-card button').first().click();
    await expect(page.locator('[data-description]')).toContainText('Мягкий студийный свет');
    await expect(page.locator('[data-words]')).toContainText('studio light');
    await expect.poll(() => page.locator('[data-detail-media] img').evaluate(img => img.complete && img.naturalWidth > 100)).toBe(true);
    await assertNoViewportOverflow(page, 'model detail');
    const footer = await page.locator('[data-detail] footer').boundingBox();
    expect(footer.y + footer.height).toBeLessThanOrEqual(page.viewportSize().height);
    const close = page.locator('[data-detail] [data-close]');
    await expect(close).toBeInViewport();
    await page.screenshot({ path: info.outputPath(`model-detail-${theme}.png`) });
    const violations = (await new AxeBuilder({ page }).analyze()).violations.filter(v => ['serious', 'critical'].includes(v.impact));
    expect(violations).toEqual([]);
    await close.click();
    await expect(page.locator('.model-card button').first()).toBeFocused();
  }
  expect(writes).toEqual([]);
});

test('ordinary user can only view the catalog', async ({ page }) => {
  const adminReads = [];
  page.on('request', request => { if (/\/models\/api\/(settings|installs|versions)/.test(request.url())) adminReads.push(request.url()); });
  await page.goto('/preview/model-catalog');
  await expect(page.locator('.model-card')).toHaveCount(3);
  await expect(page.locator('[data-open-install]')).toHaveCount(0);
  await expect(page.locator('[data-open-settings]')).toHaveCount(0);
  await page.locator('.model-card button').first().click();
  await expect(page.locator('[data-description]')).toContainText('Мягкий студийный свет');
  await expect(page.locator('[data-detail-versions]')).toHaveCount(0);
  expect(adminReads).toEqual([]);
});

test('failed read retains the catalog and recovers without reload', async ({ page }) => {
  await page.goto('/preview/model-library');
  await expect(page.locator('.model-card')).toHaveCount(3);
  await page.route('**/models/api/catalog?**', route => route.fulfill({ status: 503, body: 'Нет связи с каталогом.' }));
  await page.locator('[data-refresh]').click();
  await expect(page.locator('[data-message]').first()).toContainText('Нет связи');
  await expect(page.locator('.model-card')).toHaveCount(3);
  await page.unroute('**/models/api/catalog?**');
  await page.locator('[data-refresh]').click();
  await expect(page.locator('[data-message]').first()).toBeHidden();
});

test('administrator sees configured roots but never receives a key value', async ({ page }) => {
  await page.goto('/preview/model-library');
  await page.locator('[data-open-settings]').click();
  await expect(page.locator('[data-key-status]')).toHaveText('API-ключ задан');
  await expect(page.locator('[data-roots]')).toContainText('models/diffusion_models');
  await expect(page.locator('[data-key-form] input')).toHaveValue('');
});

async function prepareInstall(page) {
  await page.route('**/models/api/versions?**', route => route.fulfill({ json: { name: 'Studio light', type: 'LORA', description: 'Neutral test model', modelVersions: [{ id: 51, name: 'v1', baseModel: 'Krea 2', files: [{ id: 7, name: 'studio.safetensors', type: 'Model', sizeKB: 1024, metadata: { format: 'SafeTensor' } }] }] } }));
  await page.route('**/models/api/quote', route => route.fulfill({ json: { quote: 'opaque-fixture-quote', destination: 'C:/Work/ComfyUI/models/loras/Krea2/Civitai-51', filename: 'studio.safetensors', size: 1048576, free_bytes: 100000000000 } }));
  await page.goto('/preview/model-library');
  await page.locator('[data-open-install]').click();
  await page.locator('[data-source-form] input').fill('https://civitai.com/models/42/studio-light');
  await page.locator('[data-source-form] button').click();
  await expect(page.locator('[data-version-fields]')).toBeVisible();
  await page.locator('[data-quote]').click();
  await expect(page.locator('[data-quote-facts]')).toContainText('loras/Krea2/Civitai-51');
}

test('lost acceptance response is recovered from the server with one POST', async ({ page }, info) => {
  let job = null, posts = 0;
  await page.route('**/models/api/installs**', async route => {
    if (route.request().method() === 'POST') { posts += 1; const body = route.request().postDataJSON(); job = { id: body.id, name: 'Studio light', state: 'running', progress: 15 }; await route.abort('failed'); return; }
    if (new URL(route.request().url()).searchParams.has('id')) { await route.fulfill(job ? { json: job } : { status: 404, body: 'Not found' }); return; }
    await route.fulfill({ json: { jobs: job ? [job] : [] } });
  });
  await prepareInstall(page);
  await page.screenshot({ path: info.outputPath('model-install-quote.png') });
  await page.locator('[data-confirm]').click();
  await expect(page.locator('[data-install]')).not.toBeVisible();
  await expect(page.locator('[data-download-notifications]')).toContainText('Studio light');
  await page.evaluate(() => { window.dispatchEvent(new Event('pagehide')); window.dispatchEvent(new Event('pageshow')); });
  await expect(page.locator('[data-jobs]')).toContainText('Studio light');
  expect(posts).toBe(1);
  expect(await page.evaluate(() => localStorage.getItem('gateway:model-install:1'))).toBeNull();
});

test('unaccepted request can be closed without replaying installation', async ({ page }) => {
  let id = '', closed = false, posts = 0;
  await page.route('**/models/api/installs**', async route => {
    if (route.request().method() === 'POST') { posts += 1; id = route.request().postDataJSON().id; await route.abort('failed'); return; }
    if (new URL(route.request().url()).searchParams.has('id')) { await route.fulfill(closed ? { json: { id, state: 'cancelled' } } : { status: 404, body: 'Not found' }); return; }
    await route.fulfill({ json: { jobs: [] } });
  });
  await page.route('**/models/api/close-request', async route => { expect(route.request().postDataJSON().id).toBe(id); closed = true; await route.fulfill({ json: { closed: true } }); });
  await prepareInstall(page);
  await page.locator('[data-confirm]').click();
  await expect(page.locator('[data-close-request]')).toBeVisible();
  await page.locator('[data-close-request]').click();
  await expect(page.locator('[data-install] [data-message]')).toContainText('Запрос отменён');
  expect(posts).toBe(1);
});

test('UNet selection uses diffusion folder and invalidates a previous quote', async ({ page }) => {
  await prepareInstall(page);
  await page.route('**/models/api/versions?**', route => route.fulfill({ json: { name: 'Checkpoint fixture', type: 'Checkpoint', modelVersions: [{ id: 51, name: 'v1', files: [{ id: 7, name: 'model.safetensors', type: 'Model', sizeKB: 1024, metadata: { format: 'SafeTensor' } }, { id: 8, name: 'unet.safetensors', type: 'UNet', sizeKB: 1024, metadata: { format: 'SafeTensor' } }] }] } }));
  await page.locator('[data-source-form] button').click();
  await expect(page.locator('[data-remote-name]')).toHaveText('Checkpoint fixture');
  await page.locator('[data-target]').selectOption('checkpoint');
  await page.locator('[data-quote]').click();
  await expect(page.locator('[data-confirm]')).toBeEnabled();
  await page.locator('[data-file]').selectOption('8');
  await expect(page.locator('[data-target]')).toHaveValue('diffusion');
  await expect(page.locator('[data-target] option[value="checkpoint"]')).toHaveJSProperty('disabled', true);
  await expect(page.locator('[data-confirm]')).toBeDisabled();
  await expect(page.locator('[data-quote-view]')).toBeHidden();
});

test('mature preview requires explicit reveal and failed images keep dialog usable', async ({ page }) => {
  const matureReads=[];
  page.on('request',request=>{if(request.url().includes('/models/api/preview?token=fixture-mature'))matureReads.push(request.url());});
  await page.goto('/preview/model-catalog');
  await expect(page.locator('.model-card')).toHaveCount(3);
  await page.locator('.model-card button').last().click();
  await expect(page.getByRole('button',{name:'Показать превью 18+'})).toBeVisible();
  expect(matureReads).toHaveLength(0);
  await page.route('**/models/api/preview?token=fixture-mature',route=>route.fulfill({status:404,body:'Not found'}));
  await page.getByRole('button',{name:'Показать превью 18+'}).click();
  await expect(page.locator('[data-detail-media]')).toHaveText('Превью недоступно.');
  await expect(page.locator('[data-detail] [data-close]')).toBeInViewport();
  expect(matureReads).toHaveLength(1);
});

test('accepted installation closes its dialog and shows real telemetry in a dismissible notice', async ({ page }, info) => {
  let job = null, posts = 0;
  await page.route('**/models/api/installs**', async route => {
    if (route.request().method() === 'POST') {
      posts++;
      job = { id: route.request().postDataJSON().id, name: 'Studio light', state: 'running', progress: 62, metrics_available: true, bytes_downloaded: 665719930, total_bytes: 1073741824, bytes_per_second: 25794970 };
      await route.fulfill({ json: job });
    } else await route.fulfill({ json: { jobs: job ? [job] : [] } });
  });
  await prepareInstall(page);
  await page.locator('[data-confirm]').click();
  await expect(page.locator('[data-install]')).not.toBeVisible();
  const notice = page.locator('.model-download-notice');
  await expect(notice).toContainText('62 %');
  await expect(notice).toContainText('634,9 МБ / 1 ГБ');
  await expect(notice).toContainText('24,6 МБ/с');
  await expect(notice).toContainText('Осталось ≈ 16 сек');
  await expect(notice.locator('progress')).toHaveJSProperty('value', 62);
  await assertNoViewportOverflow(page, 'download notification');
  const box = await notice.boundingBox();
  expect(box.x + box.width).toBeLessThanOrEqual(page.viewportSize().width);
  expect(box.y).toBeGreaterThanOrEqual(64);
  await page.screenshot({ path: info.outputPath('model-download-notice.png') });
  const violations = (await new AxeBuilder({ page }).analyze()).violations.filter(v => ['serious', 'critical'].includes(v.impact));
  expect(violations).toEqual([]);
  await page.locator('[data-download-notice-toggle]').click();
  await expect(notice).not.toBeVisible();
  await page.locator('[data-download-open]').click();
  await notice.getByRole('button', { name: 'Скрыть уведомление: Studio light' }).click();
  await expect(page.locator('[data-download-notifications]')).toBeHidden();
  await page.locator('[data-refresh-jobs]').click();
  await expect(page.locator('[data-jobs]')).toContainText('Studio light');
  await expect(page.locator('[data-download-notifications]')).toBeHidden();
  expect(posts).toBe(1);
});

test('verification is not completion and completed notice disappears while history remains', async ({ page }) => {
  let state = 'running', progress = 100;
  await page.clock.install();
  await page.route('**/models/api/installs**', route => route.fulfill({ json: { jobs: [{ id: 'fixture', name: 'Studio light', state, progress, bytes_per_second: 50000000 }] } }));
  await page.goto('/preview/model-library');
  await page.locator('[data-download-open]').click();
  const notice = page.locator('.model-download-notice');
  await expect(notice).toContainText('Проверяем файл');
  await expect(notice).not.toContainText('МБ/с');
  await expect(notice).not.toContainText('Установлено');
  state = 'completed';
  await page.locator('[data-refresh-jobs]').click();
  await expect(notice).toContainText('Установлено');
  await page.clock.fastForward(8500);
  await expect(page.locator('[data-download-notifications]')).toBeHidden();
  await expect(page.locator('[data-jobs]')).toContainText('Установлено');
});

test('connection loss removes stale speed and polling recovers without reloading', async ({ page }) => {
  let unavailable = false;
  await page.route('**/models/api/installs**', route => route.fulfill(unavailable ? { status: 503, body: 'Нет связи' } : { json: { jobs: [{ id: 'fixture', name: 'Studio light', state: 'running', progress: 62, bytes_per_second: 25794970 }] } }));
  await page.goto('/preview/model-library');
  await page.locator('[data-download-open]').click();
  const notice = page.locator('.model-download-notice');
  await expect(notice).toContainText('24,6 МБ/с');
  unavailable = true;
  await page.locator('[data-refresh-jobs]').click();
  await expect(page.locator('[data-download-connection]')).toBeVisible();
  await expect(notice).not.toContainText('МБ/с');
  await expect(notice).toContainText('62 %');
  unavailable = false;
  await page.locator('[data-refresh-jobs]').click();
  await expect(page.locator('[data-download-connection]')).toBeHidden();
  await expect(notice).toContainText('24,6 МБ/с');
});

test('unknown telemetry stays indeterminate instead of inventing progress or speed', async ({ page }) => {
  await page.route('**/models/api/installs**', route => route.fulfill({ json: { jobs: [{ id: 'fixture', name: 'Studio light', state: 'running', progress: 0 }] } }));
  await page.goto('/preview/model-library');
  await page.locator('[data-download-open]').click();
  const notice = page.locator('.model-download-notice');
  await expect(notice).toContainText('Ожидание данных');
  await expect(notice).toContainText('Скорость недоступна');
  await expect(notice.locator('progress')).not.toHaveAttribute('value');
  await expect(notice).not.toContainText('0 Б/с');
});

test('a stale queue read cannot erase an installation accepted while it was in flight', async ({ page }) => {
  let releaseRead, job, reads = 0, posts = 0;
  await page.route('**/models/api/installs**', async route => {
    if (route.request().method() === 'POST') {
      posts++;
      job = { id: route.request().postDataJSON().id, name: 'Studio light', state: 'running', progress: 17 };
      await route.fulfill({ json: job });
      releaseRead();
    } else if (++reads === 1) {
      await new Promise(resolve => { releaseRead = resolve; });
      await route.fulfill({ json: { jobs: [] } });
    } else await route.fulfill({ json: { jobs: job ? [job] : [] } });
  });
  await prepareInstall(page);
  await page.locator('[data-confirm]').click();
  await expect(page.locator('[data-install]')).not.toBeVisible();
  await expect(page.locator('[data-jobs]')).toContainText('Studio light');
  await expect(page.locator('.model-download-notice')).toContainText('17 %');
  expect(posts).toBe(1);
});

for (const fixture of [
  { initial: 'loras', correct: 'checkpoints', type: 'Checkpoint', message: 'Это модель, а не LoRA. В поле «Тип каталога» выберите «Модель» и нажмите «Найти версии».' },
  { initial: 'checkpoints', correct: 'loras', type: 'LORA', message: 'Это LoRA, а не модель. В поле «Тип каталога» выберите «LoRA» и нажмите «Найти версии».' },
]) {
  test(`wrong catalog ${fixture.initial} explains the choice and recovers without installation`, async ({ page }, info) => {
    const writes = [], kinds = [];
    page.on('request', request => { if (request.method() === 'POST' && request.url().includes('/models/api/')) writes.push(request.url()); });
    await page.route('**/models/api/versions?**', route => {
      const kind = new URL(route.request().url()).searchParams.get('kind');
      kinds.push(kind);
      return route.fulfill(kind === fixture.initial ? { status: 422, contentType: 'text/plain; charset=utf-8', body: fixture.message } : {
        json: { name: 'Neutral studio model', type: fixture.type, modelVersions: [{ id: 51, name: 'v1', baseModel: 'Krea 2', files: [{ id: 7, name: 'studio.safetensors', type: 'Model', sizeKB: 1024, metadata: { format: 'SafeTensor' } }] }] },
      });
    });
    await page.goto('/preview/model-library');
    await page.locator('[data-open-install]').click();
    const form = page.locator('[data-source-form]');
    await form.locator('select[name="kind"]').selectOption(fixture.initial);
    await form.locator('input[name="source"]').fill('42');
    await form.locator('button').click();
    await expect(page.locator('[data-install] [data-message]')).toHaveText(fixture.message);
    await expect(page.locator('[data-version-fields]')).toBeHidden();
    await expect(page.locator('[data-confirm]')).toBeDisabled();
    await assertNoViewportOverflow(page, 'catalog mismatch');
    await page.screenshot({ path: info.outputPath(`catalog-mismatch-${fixture.initial}.png`) });
    const violations = (await new AxeBuilder({ page }).analyze()).violations.filter(v => ['serious', 'critical'].includes(v.impact));
    expect(violations).toEqual([]);
    await form.locator('select[name="kind"]').selectOption(fixture.correct);
    await form.locator('button').click();
    await expect(page.locator('[data-version-fields]')).toBeVisible();
    await expect(page.locator('[data-remote-name]')).toHaveText('Neutral studio model');
    await expect(page.locator('[data-confirm]')).toBeDisabled();
    expect(kinds).toEqual([fixture.initial, fixture.correct]);
    expect(writes).toEqual([]);
  });
}
