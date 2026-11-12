const {test,expect}=require('@playwright/test');
const {settlePage,assertNoViewportOverflow}=require('./helpers.cjs');

test('live prompt, elapsed time and final usage survive reconnect without keyboard focus',async({page},info)=>{
 let job,posts=0;
 page.on('dialog',dialog=>dialog.accept());
 await page.route('**/generate/prompt-assistant/jobs**',async route=>{
  if(route.request().method()==='POST'){
   posts++;
   const fields=new URLSearchParams(route.request().postData());
   job={id:fields.get('request_id'),context:fields.get('context'),original:fields.get('prompt'),state:'running',created_at:new Date().toISOString(),
    expires_at:new Date(Date.now()+86400000).toISOString(),progress:{phase:'thinking',started_at:new Date(Date.now()-10000).toISOString()}};
  }
  await route.fulfill({json:job});
 });
 await page.goto('/preview/generate');await settlePage(page);
 await page.locator('#positive-prompt').fill('A ceramic vase on a table.');
 await page.locator('#prompt-assistant-open').click();await page.locator('#prompt-assistant-improve').click();
 await expect(page.locator('#prompt-assistant-live-phase')).toHaveText('Модель обдумывает запрос');
 await expect(page.locator('#prompt-assistant-live-tokens')).toContainText('ожидаем');
 job.progress.phase='writing';job.progress.prompt='A detailed ceramic vase';
 await expect(page.locator('#prompt-assistant-live-text')).toHaveText(job.progress.prompt);
 await expect(page.locator('#prompt-assistant-apply')).toBeDisabled();
 await expect(page.locator('#positive-prompt')).toHaveValue(job.original);
 await expect(page.locator('#prompt-assistant-live-text')).not.toBeFocused();
 await assertNoViewportOverflow(page,'streaming assistant');
 await page.screenshot({path:info.outputPath('assistant-stream.png')});
 await page.evaluate(()=>{window.dispatchEvent(new Event('pagehide'));window.dispatchEvent(new PageTransitionEvent('pageshow',{persisted:true}));});
 job.progress.prompt='A detailed ceramic vase in natural daylight.';
 await expect(page.locator('#prompt-assistant-live-text')).toHaveText(job.progress.prompt);
 job.state='completed';job.progress.finished_at=new Date().toISOString();
 job.result={correlation_id:job.id,prompt:job.progress.prompt,references:[],usage:{prompt_tokens:34,completion_tokens:12,completion_duration_ms:2000}};
 await expect(page.locator('#prompt-assistant-draft')).toHaveValue(job.result.prompt);
 await expect(page.locator('#prompt-assistant-apply')).toBeEnabled();
 await expect(page.locator('#prompt-assistant-live-speed')).toHaveText('6 ток/с');
 await expect(page.locator('#prompt-assistant-live-tokens')).toContainText('34 вход');
 await expect(page.locator('#prompt-assistant-draft')).not.toBeFocused();
 expect(posts).toBe(1);
});
