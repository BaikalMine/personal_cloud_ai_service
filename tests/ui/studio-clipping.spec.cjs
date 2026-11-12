const { test, expect } = require('@playwright/test');
const { settlePage, chooseWorkflow } = require('./helpers.cjs');

for (const scenario of ['image', 'aihub']) {
  test(`studio keeps batch contents inside their section: ${scenario}`, async ({page}, info) => {
    await page.goto('/preview/generate');
    await settlePage(page);
    if (scenario === 'aihub') {
      await page.locator('[data-workflow-id="minimax-h3-video"]').click();
      await chooseWorkflow(page, "aihub-minimax-h3");
    }
    const section = page.locator('#generation-batch-builder');
    const toggle = page.locator('#generation-batch-enabled');
    await section.scrollIntoViewIfNeeded();
    await page.screenshot({path:info.outputPath('batch-collapsed.png')});
    const contained = () => section.evaluate(element => {
      const outer=element.getBoundingClientRect();
      return [...element.querySelectorAll('label,input,select,strong,small')].filter(node => node.getClientRects().length).every(node => {
        const box=node.getBoundingClientRect();
        return box.top >= outer.top - 1 && box.bottom <= outer.bottom + 1;
      });
    });
    expect(await contained()).toBe(true);
    await toggle.check();
    const count=page.locator('#generation-batch-count');
    await count.fill('6');
    await count.blur();
    await expect(count).toHaveValue('6');
    expect(await contained()).toBe(true);
    await count.scrollIntoViewIfNeeded();
    expect(await count.evaluate(element => {
      const box=element.getBoundingClientRect();
      const hit=document.elementFromPoint(box.left+box.width/2,box.top+box.height/2);
      return hit===element || element.contains(hit);
    })).toBe(true);
    await page.screenshot({path:info.outputPath('batch-expanded.png')});
    await toggle.uncheck();
    expect(await contained()).toBe(true);
  });
}
