const { test, expect } = require("@playwright/test");
const { settlePage, openStudioOptions, assertNoViewportOverflow, chooseWorkflow } = require("./helpers.cjs");

test("Ultra is separate, preserves native resolution and restores the normal workflow", async ({ page }) => {
  await page.goto("/generate?draft=none");
  await settlePage(page);
  await chooseWorkflow(page, "krea2-ultra");
  await expect(page.locator("#generation-model")).toHaveValue("krea2:turbo");
  await expect(page.locator('#generation-steps')).toHaveValue("10");
  await expect(page.locator('#generation-cfg')).toHaveValue("2");
  await openStudioOptions(page);
  await expect(page.locator(".krea-ultra-settings")).toBeVisible();
  await expect(page.locator('[name="krea_ultra_speed_boost"]')).not.toBeChecked();
  await expect(page.locator('[name="krea_ultra_step_swap"]')).toHaveValue("6");
  await expect(page.locator(".krea-photoflow-settings")).toBeHidden();
  await expect(page.locator('[name="negative_prompt"]')).toBeEnabled();
  await assertNoViewportOverflow(page, "Krea2 Ultra settings");
  await page.screenshot({ path: test.info().outputPath("krea-ultra.png"), fullPage: true });
  await page.keyboard.press("Escape");
  await expect(page.locator("#studio-settings")).not.toBeVisible();
  await chooseWorkflow(page, "photoflow-krea2");
  await expect(page.locator(".krea-ultra-settings")).toBeHidden();
  await expect(page.locator('[name="krea_ultra_step_swap"]')).toBeDisabled();
  await expect(page.locator('[name="negative_prompt"]')).toBeDisabled();
});

test("Full Ultra accepts fractional skin settings without rounding or step errors", async ({ page }) => {
  await page.goto("/generate?draft=none");
  await settlePage(page);
  await chooseWorkflow(page, "krea2-pro");
  await openStudioOptions(page);
  const controls = page.locator('[data-krea-pro-controls]');
  const skinToggle = controls.locator('[data-krea-pro-toggle="3658"]');
  await controls.locator('details').filter({ has: page.locator('summary', { hasText: /^Кожа$/ }) }).locator(':scope > summary').click();
  await skinToggle.check();
  const values = { coolness: 0.22, brightness: 0.12, rosy: 0.08, evenness: 0.18, smooth: 0.06, texture_preserve: 0.88, saturation: -0.08 };
  for (const [name, value] of Object.entries(values)) {
    const input = controls.locator(`[data-krea-pro-node="3658"][data-krea-pro-field="${name}"]`);
    await expect(input).toHaveAttribute('step', 'any');
    await expect(input).toHaveAttribute('inputmode', 'decimal');
    await expect(input).toHaveValue(String(value));
    expect(await input.evaluate(element => element.checkValidity())).toBe(true);
    await input.fill(String(value));
    await input.dispatchEvent('change');
  }
  const coolness = controls.locator('[data-krea-pro-node="3658"][data-krea-pro-field="coolness"]');
  await coolness.fill('0.223');
  await coolness.press('Tab');
  await expect.poll(() => page.locator('[name="krea_pro_settings"]').inputValue().then(JSON.parse)).toMatchObject({ nodes: { '3658': { ...values, coolness: 0.223 } } });
  await coolness.fill('1.01');
  expect(await coolness.evaluate(element => element.validity.rangeOverflow)).toBe(true);
  await coolness.fill('');
  expect(await coolness.evaluate(element => element.validity.valueMissing)).toBe(true);
  await coolness.fill('0.22');
  await coolness.press('Tab');
  await assertNoViewportOverflow(page, 'Ultra fractional skin settings');
  await coolness.scrollIntoViewIfNeeded();
  await page.locator('#studio-settings').screenshot({ path: test.info().outputPath('ultra-skin-fractions.png') });
});

test("Every numeric Full Ultra default is valid, including enabled optional effects", async ({ page }) => {
  await page.goto("/generate?draft=none");
  await settlePage(page);
  await chooseWorkflow(page, "krea2-pro");
  await openStudioOptions(page);
  const controls = page.locator('[data-krea-pro-controls]');
  await expect(controls.locator('[data-krea-pro-toggle="3658"]')).toHaveCount(1);
  await controls.evaluate(element => {
    for (const disclosure of element.querySelectorAll('details')) disclosure.open = true;
    for (const toggle of element.querySelectorAll('[data-krea-pro-toggle]')) {
      if (!toggle.checked) { toggle.checked = true; toggle.dispatchEvent(new Event('change', { bubbles: true })); }
    }
  });
  const numeric = controls.locator('input[type="number"]');
  expect(await numeric.count()).toBeGreaterThan(100);
  const invalid = await numeric.evaluateAll(inputs => inputs.filter(input => input.disabled || !input.checkValidity()).map(input => ({
    node: input.dataset.kreaProNode, field: input.dataset.kreaProField, value: input.value, step: input.step, disabled: input.disabled,
  })));
  expect(invalid).toEqual([]);
  const fontSize = controls.locator('[data-krea-pro-node="3622"][data-krea-pro-field="font_size"]');
  await fontSize.fill('64.5');
  expect(await fontSize.evaluate(input => input.validity.stepMismatch)).toBe(true);
});
