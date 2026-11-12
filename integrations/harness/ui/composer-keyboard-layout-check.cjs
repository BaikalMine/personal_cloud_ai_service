const { chromium } = require('/opt/ai-gateway-ui-tests/node_modules/playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');

(async () => {
  const browser = await chromium.launch({headless:true, args:['--no-sandbox']});
  try {
    for (const visible of [844, 380, 300]) {
      const page = await browser.newPage({viewport:{width:390,height:844}});
      await page.setContent(`<style>
        :root { --gateway-viewport-height:${visible}px; --dsw-alias-border-main:#59616c; --dsw-alias-label-primary:#eee; --dsw-alias-bg-base:#17191d; }
        body { margin:0; background:#111; color:#eee; font:16px sans-serif; }
        .frame { position:fixed; top:0; height:var(--gateway-viewport-height); width:100%; display:flex; flex-direction:column; justify-content:flex-end; }
        [data-composer-card] { display:flex; flex-direction:column; gap:12px; background:#292c30; padding:8px; }
        [data-input-scroll] { max-height:300px; overflow-y:auto; }
        [data-composer-input] { min-height:48px; white-space:pre-wrap; }
        .controls { flex:none; display:flex; justify-content:flex-end; }
        ${fs.readFileSync('/src/integrations/harness/ui/mobile.css','utf8')}
      </style><body data-gateway-harness><main class="frame"><div data-composer-card><div data-input-scroll><div data-composer-input contenteditable="true" role="textbox"></div></div><div class="controls"><button aria-label="Отправить">&#8593;</button></div></div></main></body>`);
      const editor = page.locator('[data-composer-input]');
      await editor.focus();
      await editor.fill(Array.from({length:40},(_,i)=>`Line ${i+1}`).join('\n'));
      await editor.press('Control+End');
      const scroll = await page.locator('[data-input-scroll]').evaluate(el=>({top:el.scrollTop, height:el.clientHeight, full:el.scrollHeight}));
      const send = await page.getByRole('button',{name:'Отправить'}).boundingBox();
      const field = await page.locator('[data-input-scroll]').boundingBox();
      assert.ok(scroll.full > scroll.height, 'long message should scroll');
      assert.ok(scroll.top > 0, 'cursor should be reachable at the end');
      assert.ok(field.y >= 0 && send.y >= field.y + field.height && send.y + send.height <= visible);
      await page.screenshot({path:`/qa/composer-keyboard-${visible}.png`});
      console.log(`visible ${visible}: scroll ${scroll.height}/${scroll.full}, send visible`);
      await page.close();
    }
  } finally { await browser.close(); }
})().catch(error=>{console.error(error);process.exit(1)});
