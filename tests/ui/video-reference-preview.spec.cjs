const { test, expect } = require('@playwright/test');
const fs = require('node:fs');
const path = require('node:path');
const { settlePage, chooseWorkflow } = require('./helpers.cjs');
const fixture = path.join(__dirname, '../../artifacts/video-preview-compatible.mp4');

test('unsupported reference gets a playable preview without changing the generation input', async ({ page }, info) => {
  test.skip(!fs.existsSync(fixture), 'CPU-generated compatible video fixture required');
  let previews = 0, requestBody;
  await page.route('**/generate/upload/video', route => route.fulfill({ json: { name: 'original-hevc.mp4', subfolder: 'users/preview' } }));
  await page.route('**/generate/preview/video', route => {
    previews++;
    requestBody = new URLSearchParams(route.request().postData());
    return route.fulfill({ contentType: 'video/mp4', headers: { 'X-Source-Video-Duration': '7.608667' }, body: fs.readFileSync(fixture) });
  });
  await page.goto('/preview/generate');
  await settlePage(page);
  await page.locator('[data-workflow-id="minimax-h3-video"]').click();
  await chooseWorkflow(page, "aihub-minimax-h3");
  await page.locator('[name="aihub_mode"][value="video"]').check();
  await page.locator('#aihub-match-duration').check();
  await page.locator('#minimax-video-file').setInputFiles({ name: 'unsupported.mp4', mimeType: 'video/mp4', buffer: Buffer.from('unsupported-video') });
  const video = page.locator('#minimax-video-preview-media');
  await expect.poll(() => video.evaluate(element => element.videoWidth)).toBeGreaterThan(0);
  await expect(page.locator('#minimax-video-playback-status')).toContainText('Совместимое превью');
  await expect(page.locator('#aihub-duration-status')).toContainText('7,609');
  expect(previews).toBe(1);
  expect(requestBody.get('input_video')).toBe('users/preview/original-hevc.mp4');
  await page.locator('#minimax-video-play').click();
  await expect.poll(() => video.evaluate(element => element.currentTime)).toBeGreaterThan(0.3);
  const pixels = () => video.evaluate(element => {
    const canvas = document.createElement('canvas'); canvas.width = canvas.height = 32;
    const ctx = canvas.getContext('2d'); ctx.drawImage(element, 0, 0, 32, 32);
    return Array.from(ctx.getImageData(0, 0, 32, 32).data);
  });
  const first = await pixels();
  await page.waitForTimeout(500);
  const second = await pixels();
  expect(first.some((value, i) => i % 4 !== 3 && value > 10)).toBe(true);
  expect(first).not.toEqual(second);
  await video.scrollIntoViewIfNeeded();
  await page.screenshot({ path: info.outputPath('compatible-preview.png') });
  await page.locator('#minimax-video-remove').click();
  await expect(page.locator('#minimax-video-preview')).toBeHidden();
  expect(await video.getAttribute('src')).toBeNull();
});
