const { test, expect } = require('@playwright/test');
const { settlePage, chooseWorkflow, assertNoViewportOverflow } = require('./helpers.cjs');

for (const workflow of ['moss-image', 'krea2-ultra', 'krea2-pro', 'qwen-image21']) {
  test(`${workflow}: negative prompt is reviewed, edited, applied and restored`, async ({ page }) => {
    page.on('dialog', dialog => dialog.accept());
    await page.goto('/preview/generate?draft=none');
    await settlePage(page);
    await page.locator('.scenario-choice[data-workflow-id="text-to-image"]').click();
    await chooseWorkflow(page, workflow);
    if (workflow === 'qwen-image21') {
      await page.getByRole('button', {name:'Дополнительные параметры',exact:true}).click();
      await page.locator('#generation-cfg').fill('2');
      await page.locator('#generation-cfg').dispatchEvent('change');
      await page.getByRole('button', {name:'Закрыть параметры',exact:true}).click();
    }
    await page.locator('#positive-prompt').fill('A blue ceramic teapot on a wooden table.');
    await page.locator('#negative-prompt').fill('Original exclusions');
    const request = page.waitForRequest(request => request.url().endsWith('/generate/prompt-assistant/jobs') && request.method() === 'POST');
    await page.locator('#prompt-assistant-open').click();
    await page.locator('#prompt-assistant-improve').click();
    const fields = new URLSearchParams((await request).postData());
    expect(fields.get('generation_workflow')).toBe(workflow);
    expect(Number(fields.get('cfg'))).toBeGreaterThan(1);
    expect(fields.get('negative_prompt')).toBe('Original exclusions');
    await expect(page.locator('#prompt-assistant-negative-review')).toBeVisible();
    await expect(page.locator('#prompt-assistant-negative-draft')).toHaveValue('blurry, distorted anatomy');
    await page.locator('#prompt-assistant-negative-draft').fill('watermark, blurry lettering');
    await assertNoViewportOverflow(page, `${workflow} negative review`);
    await page.screenshot({path:test.info().outputPath(`${workflow}-negative-review.png`),fullPage:true});
    await page.locator('#prompt-assistant-apply').click();
    await expect(page.locator('#negative-prompt')).toHaveValue('watermark, blurry lettering');
    await page.locator('#prompt-assistant-restore-original').click();
    await expect(page.locator('#positive-prompt')).toHaveValue('A blue ceramic teapot on a wooden table.');
    await expect(page.locator('#negative-prompt')).toHaveValue('Original exclusions');
  });
}

test('an unchanged positive prompt still allows restoring an assistant-edited negative', async ({ page }) => {
  page.on('dialog', dialog => dialog.accept());
  await page.route('**/generate/prompt-assistant/jobs', async route => {
    const body = new URLSearchParams(route.request().postData());
    await route.fulfill({json:{id:body.get('request_id'),state:'completed',context:body.get('context'),original:body.get('prompt'),original_negative:body.get('negative_prompt'),
      result:{prompt:body.get('prompt'),negative_prompt:'watermark, blurry lettering',references:[],model:'neutral-fixture',correlation_id:body.get('request_id')}}});
  });
  await page.goto('/preview/generate?draft=none');
  await settlePage(page);
  await page.locator('.scenario-choice[data-workflow-id="text-to-image"]').click();
  await chooseWorkflow(page,'moss-image');
  await page.locator('#positive-prompt').fill('A blue ceramic teapot.');
  await page.locator('#negative-prompt').fill('Original exclusions');
  await page.locator('#prompt-assistant-open').click();
  await page.locator('#prompt-assistant-improve').click();
  await expect(page.locator('#prompt-assistant-negative-draft')).toHaveValue('watermark, blurry lettering');
  await page.locator('#prompt-assistant-apply').click();
  await expect(page.locator('#positive-prompt')).toHaveValue('A blue ceramic teapot.');
  await expect(page.locator('#prompt-assistant-restore-original')).toBeVisible();
  await page.locator('#prompt-assistant-restore-original').click();
  await expect(page.locator('#negative-prompt')).toHaveValue('Original exclusions');
});
