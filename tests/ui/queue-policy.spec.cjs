const { test, expect } = require("@playwright/test");
const AxeBuilder = require("@axe-core/playwright").default;
const { installPreviewClock, settlePage, assertNoViewportOverflow } = require("./helpers.cjs");

test("access forms keep queue priority and mining independent", async ({ page }, testInfo) => {
  await installPreviewClock(page);
  for (const route of ["/preview/invites", "/preview/user"]) {
    await page.goto(route);
    await settlePage(page);
    const priority = page.locator('main input[name="queue_priority"]');
    const mining = page.locator('main input[name="pause_mining_for_quick_generation"]');
    await expect(priority).toHaveCount(1);
    await expect(mining).toHaveCount(1);
    await priority.uncheck();
    await mining.check();
    await expect(priority).not.toBeChecked();
    await priority.focus();
    await page.keyboard.press("Space");
    await expect(priority).toBeChecked();
    await expect(mining).toBeChecked();
    await mining.uncheck();
    await expect(priority).toBeChecked();
    const values = await priority.evaluate(input => Object.fromEntries(new FormData(input.form)));
    expect(values.queue_policy_version).toBe("2");
    expect(values.queue_priority).toBe("on");
    expect(values.pause_mining_for_quick_generation).toBeUndefined();
    await assertNoViewportOverflow(page, route);
    const panel = page.locator(".invite-runtime-group");
    await testInfo.attach(`${route.split("/").pop()}-queue-policy`, { body: await panel.screenshot(), contentType: "image/png" });
    const audit = await new AxeBuilder({ page }).include(".invite-runtime-group").analyze();
    expect(audit.violations.filter(v => ["critical", "serious"].includes(v.impact))).toEqual([]);
  }
});

test("admin priority save leaves mining policy untouched and rolls back on error", async ({ page }, testInfo) => {
  await installPreviewClock(page);
  const requests = [];
  let fail = false;
  await page.route("**/account/quick-generation-priority", async route => {
    const form = new URLSearchParams(route.request().postData());
    requests.push(Object.fromEntries(form));
    await route.fulfill({ status: fail ? 503 : 200, contentType: "application/json", body: JSON.stringify(fail ? { error: "unavailable" } : { enabled: form.get("enabled") === "on" }) });
  });
  await page.goto("/preview/profile");
  await settlePage(page);
  const priority = page.locator('.header-priority-form input[name="enabled"]');
  const mining = page.locator('#generation-resources input[name="pause_mining_for_quick_generation"]');
  const originalMining = await mining.isChecked();
  const originalPriority = await priority.isChecked();
  await page.locator(".header-priority-toggle").click();
  await expect.poll(() => requests.length).toBe(1);
  await expect(priority).toBeEnabled();
  expect(await priority.isChecked()).toBe(!originalPriority);
  expect(await mining.isChecked()).toBe(originalMining);
  expect(requests[0].pause_mining_for_quick_generation).toBeUndefined();
  await expect(page.locator(".header-priority-form")).not.toHaveAttribute("title", /майнинг/);
  fail = true;
  await priority.focus();
  await page.keyboard.press("Space");
  await expect.poll(() => requests.length).toBe(2);
  await expect(priority).toBeEnabled();
  expect(await priority.isChecked()).toBe(!originalPriority);
  expect(await mining.isChecked()).toBe(originalMining);
  await mining.setChecked(!originalMining);
  expect(await priority.isChecked()).toBe(!originalPriority);
  const form = mining.locator("xpath=ancestor::form");
  await expect(form).toHaveAttribute("action", "/account/generation-mining");
  await assertNoViewportOverflow(page, "profile independent policy");
  await testInfo.attach("admin-mining-policy", { body: await page.locator("#generation-resources").screenshot(), contentType: "image/png" });
  const audit = await new AxeBuilder({ page }).include("#generation-resources").analyze();
  expect(audit.violations.filter(v => ["critical", "serious"].includes(v.impact))).toEqual([]);
});
