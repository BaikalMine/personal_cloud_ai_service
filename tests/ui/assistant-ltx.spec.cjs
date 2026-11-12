const {test, expect} = require('@playwright/test');
const {settlePage, assertNoViewportOverflow, chooseWorkflow } = require('./helpers.cjs');

test('LTX soundtrack is reviewed separately and applied with the visual prompt', async ({page}, info) => {
  const visual = 'A woman waves at the camera in soft daylight.';
  const audio = 'A woman calmly says: "Hello." Quiet room ambience, no music.';
  page.on('dialog', dialog => dialog.accept());
  await page.route('**/generate/prompt-assistant/jobs', async route => {
    const fields = new URLSearchParams(route.request().postData());
    expect(fields.get('aihub_audio_source')).toBe('ltx');
    expect(fields.get('aihub_audio_prompt')).toBe('Say "Hello"');
    await route.fulfill({json:{id:fields.get('request_id'), context:fields.get('context'), original:fields.get('prompt'), original_audio:fields.get('aihub_audio_prompt'), state:'completed',
      expires_at:new Date(Date.now()+86400000).toISOString(), result:{prompt:visual,audio_prompt:audio,references:[],correlation_id:fields.get('request_id')}}});
  });
  await page.route('**/generate/prompt-assistant/decision', route => route.fulfill({json:{decision:new URLSearchParams(route.request().postData()).get('decision')}}));
  await page.goto('/preview/generate'); await settlePage(page);
  await page.locator('[data-workflow-id="minimax-h3-video"]').click();
  await chooseWorkflow(page, "aihub-minimax-h3");
  await page.locator('[name="aihub_audio_source"]').selectOption('ltx');
  await page.locator('[name="aihub_audio_prompt"]').fill('Say "Hello"');
  await page.locator('#positive-prompt').fill('A woman waves and says hello.');
  await page.locator('#prompt-assistant-open').click();
  await page.locator('#prompt-assistant-improve').click();
  await expect(page.locator('#prompt-assistant-apply')).toBeEnabled();
  await expect(page.locator('#prompt-assistant-audio-text')).toHaveValue(audio);
  await expect(page.locator('#prompt-assistant-audio-text')).not.toBeFocused();
  await expect(page.locator('[name="aihub_audio_prompt"]')).toHaveValue('Say "Hello"');
  await page.locator('#prompt-assistant-audio-text').scrollIntoViewIfNeeded();
  await assertNoViewportOverflow(page, 'LTX review');
  await page.screenshot({path:info.outputPath('ltx-review.png')});
  await page.locator('#prompt-assistant-apply').click();
  await expect(page.locator('#positive-prompt')).toHaveValue(visual);
  await expect(page.locator('[name="aihub_audio_prompt"]')).toHaveValue(audio);
  await expect(page.locator('#prompt-assistant-launch-state')).not.toHaveText('Вариант неактуален');
  await page.locator('#prompt-assistant-open').click();
  await page.locator('#prompt-assistant-keep').click();
  await expect(page.locator('#prompt-assistant-restore-original')).toBeVisible();
  await page.locator('#prompt-assistant-restore-original').click();
  await expect(page.locator('#positive-prompt')).toHaveValue('A woman waves and says hello.');
  await expect(page.locator('[name="aihub_audio_prompt"]')).toHaveValue('Say "Hello"');
  await expect(page.locator('[name="aihub_audio_prompt"]')).not.toBeFocused();
});
