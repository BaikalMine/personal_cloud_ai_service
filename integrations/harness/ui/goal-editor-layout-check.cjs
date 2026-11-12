const { chromium } = require('/opt/ai-gateway-ui-tests/node_modules/playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');

(async () => {
  const browser = await chromium.launch({headless: true, args: ['--no-sandbox']});
  try {
    for (const viewport of [{width: 390, height: 844, visible: 844}, {width: 390, height: 844, visible: 380}, {width: 1280, height: 800, visible: 800}]) {
      const page = await browser.newPage({viewport});
      await page.setContent(`<style>
        :root { --gateway-viewport-height:${viewport.visible}px; --dsw-alias-border-main:#59616c; --dsw-alias-label-primary:#eee; --dsw-alias-bg-base:#17191d; }
        body { margin:0; background:#111; color:#eee; font:16px sans-serif; }
        .nLMEza_dock { width:calc(100% - 32px); margin:16px auto; max-width:760px; }
        .nLMEza_bar { display:flex; width:100%; box-sizing:border-box; border:1px solid #555; }
        .nLMEza_actions { display:flex; gap:10px; }
        .nLMEza_iconBtn { width:44px; height:44px; }
        ${fs.readFileSync('/src/integrations/harness/ui/mobile.css', 'utf8')}
        </style><body data-gateway-harness><div class="nLMEza_dock" data-goal-bar><div class="nLMEza_bar nLMEza_editBar"><textarea class="gateway-goal-editor" aria-label="Описание цели">Первая строка\nВторая строка</textarea><div class="nLMEza_actions"><button class="nLMEza_iconBtn" aria-label="Сохранить">&#10003;</button><button class="nLMEza_iconBtn" aria-label="Отмена">&#215;</button></div></div></div></body>`);
      const editor = page.locator('textarea');
      const box = await editor.boundingBox();
      const save = await page.getByRole('button', {name: 'Сохранить'}).boundingBox();
      assert.ok(box.width >= (viewport.width < 500 ? viewport.width - 70 : 650));
      assert.ok(box.height >= 140 && box.height <= viewport.visible);
      assert.ok(box.y >= 0 && save.y >= box.y + box.height && save.y + save.height <= viewport.visible && save.x + save.width <= viewport.width);
      await editor.press('Control+End'); await editor.press('Enter'); await page.keyboard.type('third line');
      assert.match(await editor.inputValue(), /\nthird line$/);
      await page.screenshot({path: `/qa/goal-editor-${viewport.width}x${viewport.visible}.png`});
      console.log(`${viewport.width}x${viewport.visible}: editor ${Math.round(box.width)}x${Math.round(box.height)}, controls visible`);
      await page.close();
    }
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exit(1); });
