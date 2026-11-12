const {test, expect} = require('@playwright/test');
const AxeBuilder = require('@axe-core/playwright').default;
const {installPreviewClock, settlePage, assertNoViewportOverflow, chooseWorkflow} = require('./helpers.cjs');

const open = async (page, theme='dark') => {
  await installPreviewClock(page);
  await page.addInitScript(value => localStorage.setItem('ai_gateway_theme', value), theme);
  await page.goto('/preview/generate');
  await settlePage(page);
};

test('compact workflow selection fits both themes without pushing the prompt away', async ({page}, info) => {
  for (const theme of ['dark','light']) {
    await open(page, theme);
    const trigger = page.locator('#studio-workflow-open');
    await expect(trigger).toContainText('PhotoFlow Krea2');
    await expect(page.locator('.studio-tasks button').first()).toHaveAttribute('data-workflow-id','text-to-image');
    await expect(page.locator('.studio-tasks button')).toHaveCount(3);
    await expect(page.locator('.studio-tasks button > svg')).toHaveCount(3);
    await expect(page.locator('.generation-workflow-choice:visible')).toHaveCount(0);
    const bounds = await trigger.boundingBox();
    expect(bounds.height).toBeLessThanOrEqual(64);
    const prompt = await page.locator('#positive-prompt').boundingBox();
    expect(prompt.y).toBeLessThan(page.viewportSize().height - 160);
    await assertNoViewportOverflow(page, `${theme} selection`);
    await page.screenshot({path:info.outputPath(`selection-${theme}.png`)});
    await trigger.click();
    const dialog = page.locator('#studio-workflow-dialog');
    await expect(dialog).toBeVisible();
    await expect(dialog.locator('.generation-workflow-choice:visible')).toHaveCount(5);
    const close = await page.locator('#studio-workflow-close').boundingBox();
    expect(close.y).toBeGreaterThanOrEqual(0);
    expect(close.y + close.height).toBeLessThanOrEqual(page.viewportSize().height);
    await assertNoViewportOverflow(page, `${theme} selection menu`);
    await page.screenshot({path:info.outputPath(`menu-${theme}.png`)});
    await page.keyboard.press('Escape');
    await expect(trigger).toBeFocused();
  }
});

test('workflow selection has no serious accessibility violations', async ({page}) => {
  await open(page);
  const check = async selector => {
    const result = await new AxeBuilder({page}).include(selector).withTags(['wcag2a','wcag2aa','wcag21a','wcag21aa']).analyze();
    expect(result.violations.filter(item => ['critical','serious'].includes(item.impact))).toEqual([]);
  };
  await check('.studio-models');
  await page.locator('#studio-workflow-open').click();
  await check('#studio-workflow-dialog');
});

test('workflow menu preserves text, skips disabled choices and supports keyboard selection', async ({page}) => {
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  await open(page);
  const prompt = page.locator('#positive-prompt');
  await prompt.fill('A blue glazed ceramic teapot on a wooden table.');
  const before = await page.locator('[name="generation_workflow"]').inputValue();
  await page.locator('[data-preset-id="moss-image"]').evaluate(button => { button.disabled = true; });
  await page.locator('#studio-workflow-open').click();
  await page.keyboard.press('End');
  await expect(page.locator('[data-preset-id="qwen-image21"]')).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(page.locator('[name="generation_workflow"]')).toHaveValue(before);
  await expect(prompt).toHaveValue('A blue glazed ceramic teapot on a wooden table.');
  await page.locator('#studio-workflow-open').click();
  await page.keyboard.press('Home');
  await page.keyboard.press('ArrowDown');
  await page.keyboard.press('Enter');
  await expect(page.locator('#studio-workflow-dialog')).not.toBeVisible();
  await expect(page.locator('[name="generation_workflow"]')).toHaveValue('krea2-ultra');
  await expect(page.locator('#studio-workflow-current')).toHaveText('Krea2 · Ультра');
  await expect(page.locator('#studio-workflow-open')).toBeFocused();
  await page.locator('[data-workflow-id="minimax-h3-video"]').click();
  await chooseWorkflow(page, 'aihub-minimax-h3');
  await expect(page.locator('#studio-workflow-current')).toHaveText('AIHUB');
  await expect(prompt).toHaveValue('A blue glazed ceramic teapot on a wooden table.');
  expect(errors).toEqual([]);
});

test('model display keeps the exact selection and narrow phones remain usable', async ({page}, info) => {
  if (info.project.name === 'mobile-390') await page.setViewportSize({width:320,height:760});
  await open(page);
  const model = page.locator('#generation-model');
  const id = await model.inputValue();
  await model.evaluate(select => {
    select.selectedOptions[0].textContent = 'Krea2 / cyberrealisticKrea2_v40_3245336';
    select.dispatchEvent(new Event('change',{bubbles:true}));
  });
  await expect(model).toHaveValue(id);
  await expect(model).toHaveAttribute('title','Krea2 / cyberrealisticKrea2_v40_3245336');
  expect(await model.locator('option:checked').textContent()).toBe('Cyberrealistic Krea2 v40 3245336');
  await chooseWorkflow(page, 'krea2-pro');
  await expect(page.locator('#studio-workflow-current')).toHaveText('Krea2 · Ультра полный');
  await assertNoViewportOverflow(page, 'long workflow and model');
  await page.screenshot({path:info.outputPath('selection-narrow-long-name.png')});
});

test('landscape menu scrolls without hiding its close button and backdrop cancels safely', async ({page}, info) => {
  test.skip(info.project.name !== 'mobile-390', 'One landscape touch viewport is sufficient.');
  await page.setViewportSize({width:844,height:390});
  await open(page);
  const trigger = page.locator('#studio-workflow-open');
  await trigger.click();
  await page.keyboard.press('End');
  await expect(page.locator('[data-preset-id="moss-image"]')).toBeInViewport({ratio:1});
  await expect(page.locator('#studio-workflow-close')).toBeInViewport({ratio:1});
  await page.screenshot({path:info.outputPath('menu-landscape.png')});
  await page.mouse.click(2,2);
  await expect(page.locator('#studio-workflow-dialog')).not.toBeVisible();
  await expect(trigger).toBeFocused();
  await expect(page.locator('#studio-workflow-current')).toHaveText('PhotoFlow Krea2');
  await expect(page.locator('body')).not.toHaveClass(/studio-picker-open/);
});
