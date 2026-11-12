const { test, expect } = require("@playwright/test");
const { settlePage, assertNoViewportOverflow } = require("./helpers.cjs");

async function fixture(page, theme = "dark", loseReceipt = false, url = "/preview/generate") {
  page.on("pageerror", error => console.log("Page error:", error.message));
  page.on("requestfailed", request => console.log("Request failed:", request.url(), request.failure()?.errorText));
  page.on("console", message => { if (message.type() === "error") console.log("Browser error:", message.text()); });
  const state = { job: null, posts: 0, gets: 0, cancels: 0 };
  await page.addInitScript(value => localStorage.setItem("ai_gateway_theme", value), theme);
  await page.route("**/preview/generate**", async route => {
    const response = await route.fetch();
    await route.fulfill({ response, body: (await response.text()).replace('data-durable-jobs="true"', 'data-durable-jobs="false"') });
  });
  await page.route("**/generate/prompt-assistant/jobs**", async route => {
    const request = route.request();
    if (request.url().endsWith("/cancel")) {
      state.cancels++;
      state.job.state = "cancelled"; state.job.status = "Запрос отменён";
    } else if (request.method() === "POST") {
      state.posts++;
      if (loseReceipt === "before" && state.posts === 1) { await route.abort(); return; }
      const body = new URLSearchParams(request.postData());
      state.job = { id: body.get("request_id"), context: body.get("context"), original: body.get("prompt"), state: "queued", status: "В очереди ассистента", expires_at: new Date(Date.now()+86400000).toISOString(),
        result: { prompt: "A detailed ceramic vase in natural daylight.", references: [], model: "fixture-text", correlation_id: body.get("request_id") } };
      if (loseReceipt === true) { await route.abort(); return; }
    } else state.gets++;
    if (!state.job) { await route.fulfill({ status: 404, json: { error: "Not accepted yet" } }); return; }
    await route.fulfill({ status: request.method() === "POST" ? 202 : 200, json: state.job });
  });
  await page.goto(url);
  await settlePage(page);
  await expect(page.locator("#prompt-assistant-improve")).toBeEnabled();
  await page.locator("#positive-prompt").fill("A ceramic vase on a table.");
  await page.locator("#prompt-assistant-open").click();
  return state;
}

async function save(page) {
  if (await page.locator("#prompt-assistant").isVisible()) await page.keyboard.press("Escape");
  if (await page.locator(".studio-saved").getAttribute("open") === null) await page.locator(".studio-saved > summary").click();
  await page.locator("#generation-draft-save").click();
  await expect(page.locator("#generation-draft-status")).toHaveText("Сохранено");
}

test.beforeEach(async ({ page }) => page.on("dialog", dialog => dialog.accept()));

test("expired draft assistant receipt releases the editor without a page reload", async ({ page }) => {
  await page.goto("/preview/generate");
  await settlePage(page);
  const owner = await page.locator("#prompt-assistant").getAttribute("data-owner-id");
  await page.evaluate(ownerID => localStorage.setItem(`ai_gateway_assistant_job_v1_${ownerID}`,
    JSON.stringify({ id: "missing_assistant_receipt_123456", expiresAt: Date.now() + 86400000 })), owner);
  let gets = 0;
  await page.route("**/generate/prompt-assistant/jobs?id=*", async route => {
    gets++;
    await route.fulfill({ status: 404, json: { error: "not found" } });
  });
  await page.reload();
  await settlePage(page);
  await page.locator("#prompt-assistant-open").click();
  await expect(page.locator("#prompt-assistant-improve")).toBeEnabled({ timeout: 15000 });
  await expect(page.locator("#prompt-assistant-state")).toContainText("не найден");
  expect(gets).toBeGreaterThanOrEqual(2);
  expect(await page.evaluate(ownerID => localStorage.getItem(`ai_gateway_assistant_job_v1_${ownerID}`), owner)).toBeNull();
});

test("pending assistant restores the same request and keeps applied text after another reload", async ({ page }, info) => {
  const state = await fixture(page, "light");
  await page.locator("#prompt-assistant-improve").click();
  await expect(page.locator("#prompt-assistant-cancel")).toBeVisible();
  await expect(page.locator("#prompt-assistant-improve")).not.toHaveClass(/is-loading/);
  const id = state.job.id;
  await save(page);
  await page.reload(); await settlePage(page);
  await page.locator("#prompt-assistant-open").click();
  await expect(page.locator("#prompt-assistant-cancel")).toBeVisible();
  await assertNoViewportOverflow(page, "assistant queue");
  await page.screenshot({ path: info.outputPath("assistant-queue-light.png") });
  state.job.state = "completed"; state.job.status = "Вариант готов";
  await expect(page.locator("#prompt-assistant-draft")).toHaveValue(state.job.result.prompt);
  await expect(page.locator("#positive-prompt")).toHaveValue("A ceramic vase on a table.");
  expect(state.posts).toBe(1); expect(state.job.id).toBe(id);
  await page.locator("#prompt-assistant-apply").click();
  await expect(page.locator("#positive-prompt")).toHaveValue(state.job.result.prompt);
  await save(page); await page.reload(); await settlePage(page);
  await expect(page.locator("#positive-prompt")).toHaveValue(state.job.result.prompt);
  expect(state.posts).toBe(1);
});

test("lost receipt has a real countdown and reconnects without another POST", async ({ page }, info) => {
  const state=await fixture(page,"dark",true);
  await page.locator("#prompt-assistant-improve").click();
  await expect(page.locator("#prompt-assistant-state")).toContainText("через 5 сек.");
  await expect(page.locator("#prompt-assistant-state")).toContainText("через 4 сек.");
  await page.screenshot({path:info.outputPath("assistant-reconnecting-dark.png")});
  state.job.state="completed";
  await expect(page.locator("#prompt-assistant-draft")).toHaveValue(state.job.result.prompt,{timeout:12000});
  expect(state.posts).toBe(1);
});

test("offline return receives cached answer without navigation or keyboard focus", async ({ page, context }, info) => {
  const state = await fixture(page);
  await expect(page.locator("#prompt-assistant")).toHaveAttribute("data-durable-jobs", "false");
  await page.locator("#prompt-assistant-improve").click();
  await expect(page.locator("#prompt-assistant-cancel")).toBeVisible();
  let navigations = 0;
  page.on("framenavigated", frame => { if (frame === page.mainFrame()) navigations++; });
  const id = state.job.id;
  await context.setOffline(true);
  state.job.state = "completed"; state.job.status = "Вариант готов";
  await context.setOffline(false);
  await expect(page.locator("#prompt-assistant-draft")).toHaveValue(state.job.result.prompt);
  await expect(page.locator("#prompt-assistant-draft")).not.toBeFocused();
  await expect(page.locator("#prompt-assistant-apply")).toBeEnabled();
  expect(state.posts).toBe(1); expect(state.job.id).toBe(id); expect(navigations).toBe(0);
  await assertNoViewportOverflow(page, "assistant recovered response");
  await page.screenshot({ path: info.outputPath("assistant-no-reload-recovery.png") });
});

test("request lost before acceptance recovers without reloading", async ({ page }) => {
  const state = await fixture(page, "light", "before");
  const ids = [];
  page.on("request", request => {
    if (request.method() === "POST" && request.url().endsWith("/prompt-assistant/jobs")) ids.push(new URLSearchParams(request.postData()).get("request_id"));
  });
  await page.locator("#prompt-assistant-improve").click();
  await expect.poll(() => state.posts, { timeout: 12000 }).toBe(2);
  state.job.state = "completed";
  await expect(page.locator("#prompt-assistant-draft")).toHaveValue(state.job.result.prompt);
  expect(ids).toHaveLength(2); expect(ids[0]).toBe(ids[1]);
});

test("late result stays separately readable and cannot replace a manually edited proposal", async ({ page },info) => {
  const state=await fixture(page);
  await page.locator("#prompt-assistant-improve").click();
  await expect(page.locator("#prompt-assistant-cancel")).toBeVisible();
  state.job.state="completed";
  await expect(page.locator("#prompt-assistant-draft")).toHaveValue(state.job.result.prompt);
  await page.locator("#prompt-assistant-improve").click();
  await expect(page.locator("#prompt-assistant-cancel")).toBeVisible();
  await page.locator("#prompt-assistant-draft").fill("Keep my manual proposal.");
  await save(page); await page.reload(); await settlePage(page);
  state.job.state="completed";
  await page.locator("#prompt-assistant-open").click();
  await expect(page.locator("#prompt-assistant-saved-response")).toBeVisible();
  await expect(page.locator("#prompt-assistant-draft")).toHaveValue("Keep my manual proposal.");
  await expect(page.locator("#prompt-assistant-apply")).toBeDisabled();
  await page.locator("#prompt-assistant-saved-response > summary").click();
  await expect(page.locator("#prompt-assistant-saved-text")).toHaveText(state.job.result.prompt);
  await assertNoViewportOverflow(page,"saved assistant response");
  await page.screenshot({path:info.outputPath("assistant-preserved-manual.png")});
  expect(state.posts).toBe(2);
});

test("uploaded reference context survives reload before a completed answer", async ({ page }) => {
  const state = await fixture(page, "light", false, "/preview/generate?template=image-to-image&workflow=photoflow-flux2-edit&media=1&slot=1&role=identity");
  await expect(page.locator("#prompt-assistant-source-list img")).toHaveCount(1);
  await page.locator("#prompt-assistant-improve").click();
  await expect(page.locator("#prompt-assistant-cancel")).toBeVisible();
  const context = state.job.context;
  await save(page);
  await page.goto("/preview/generate"); await settlePage(page);
  state.job.state = "completed";
  await page.locator("#prompt-assistant-open").click();
  await expect(page.locator("#prompt-assistant-source-list img")).toHaveCount(1);
  await expect(page.locator("#prompt-assistant-draft")).toHaveValue(state.job.result.prompt);
  await expect(page.locator("#prompt-assistant-apply")).toBeEnabled();
  expect(state.job.context).toBe(context); expect(state.posts).toBe(1);
});

test("changing the prompt while the panel is closed preserves focus and exposes the old answer separately", async ({ page }) => {
  const state = await fixture(page);
  await page.locator("#prompt-assistant-improve").click();
  await expect(page.locator("#prompt-assistant-cancel")).toBeVisible();
  await page.keyboard.press("Escape");
  await page.locator("#positive-prompt").fill("My new prompt.");
  state.job.state = "completed";
  await expect(page.locator("#prompt-assistant-improve")).toBeEnabled();
  await expect(page.locator("#positive-prompt")).toBeFocused();
  await expect(page.locator("#positive-prompt")).toHaveValue("My new prompt.");
  await page.locator("#prompt-assistant-open").click();
  await expect(page.locator("#prompt-assistant-saved-response")).toBeVisible();
  await expect(page.locator("#prompt-assistant-apply")).toBeDisabled();
  expect(state.posts).toBe(1);
});

test("cancel remains near status and persists across reload without clearing the prompt", async ({ page }) => {
  const state=await fixture(page);
  await page.locator("#prompt-assistant-improve").click();
  await expect(page.locator("#prompt-assistant-cancel")).toBeVisible();
  await page.locator("#prompt-assistant-cancel").click();
  await expect(page.locator("#prompt-assistant-state")).toHaveText("Запрос отменён");
  await expect(page.locator("#prompt-assistant-cancel")).not.toBeVisible();
  await expect(page.locator("#prompt-assistant-improve")).toBeEnabled();
  await save(page); await page.reload(); await settlePage(page);
  await page.locator("#prompt-assistant-open").click();
  await expect(page.locator("#prompt-assistant-state")).toContainText("отменён");
  await expect(page.locator("#positive-prompt")).toHaveValue("A ceramic vase on a table.");
  expect(state.posts).toBe(1); expect(state.cancels).toBe(1);
});
