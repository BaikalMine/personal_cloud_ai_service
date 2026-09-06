const { test, expect } = require("@playwright/test");
const { settlePage, assertNoViewportOverflow } = require("./helpers.cjs");

async function launch(page, theme = "dark") {
  await page.route("**/generate/preflight", route => route.fulfill({ json: { ok: true, checks: [] } }));
  await page.addInitScript(value => localStorage.setItem("ai_gateway_theme", value), theme);
  await page.goto("/preview/generate");
  await settlePage(page);
  await page.locator('[name="positive_prompt"]').fill("A ceramic teapot on a table in daylight.");
  await page.locator("#generation-submit").click();
}

test("durable waiting has no progress bar and can be cancelled before ComfyUI", async ({ page }, info) => {
  let cancelled = false;
  let launches = 0;
  let requestID = "";
  const response = () => ({ job_id: "durable-job", request_id: requestID, state: cancelled ? "cancelled" : "submitting", job_state: cancelled ? "cancelled" : "waiting_for_resources", dispatch_waiting: !cancelled, message: cancelled ? "Задание отменено до запуска." : "Задание сохранено и ожидает запуска на сервере." });
  await page.route("**/generate/run", async route => {
    launches++;
    requestID = new URLSearchParams(route.request().postData()).get("client_request_id");
    await route.fulfill({ status: 202, json: response() });
  });
  await page.route("**/generate/recover?**", route => route.fulfill({ status: 202, json: response() }));
  await page.route("**/generate/jobs/cancel", async route => {
    expect(new URLSearchParams(route.request().postData()).get("job_id")).toBe("durable-job");
    cancelled = true;
    await route.fulfill({ json: { cancelled: true, job: { state: "cancelled" } } });
  });
  await launch(page);
  await expect(page.locator("#generation-result-title")).toHaveText("В очереди Gateway");
  await expect(page.locator("#generation-progressbar")).toBeHidden();
  await expect(page.locator("#generation-run-progress")).toBeHidden();
  await expect(page.locator("#generation-cancel")).toBeEnabled();
  await expect(page.locator("#generation-cancel")).toBeInViewport();
  await expect(page.locator("#generation-result img")).toHaveCount(0);
  await assertNoViewportOverflow(page, "durable queue");
  await page.screenshot({ path: info.outputPath("durable-queue-dark.png") });
  await page.locator("#generation-cancel").click();
  await expect(page.locator("#generation-result-title")).toHaveText("Генерация отменена");
  await expect.poll(() => page.evaluate(() => localStorage.getItem("ai-gateway.active-generation"))).toBeNull();
  await page.waitForTimeout(1700);
  await expect(page.locator("#generation-result-title")).toHaveText("Генерация отменена");
  expect(launches).toBe(1);
});

test("failed pre-dispatch job ends waiting and does not resend", async ({ page }, info) => {
  let failed = false;
  let launches = 0;
  let requestID = "";
  const response = () => ({ job_id: "failed-job", request_id: requestID, state: failed ? "error" : "submitting", dispatch_waiting: !failed, message: failed ? "Модель больше недоступна." : "Ожидаем запуска на сервере." });
  await page.route("**/generate/run", async route => {
    launches++;
    requestID = new URLSearchParams(route.request().postData()).get("client_request_id");
    await route.fulfill({ status: 202, json: response() });
  });
  await page.route("**/generate/recover?**", route => route.fulfill({ json: response() }));
  await launch(page, "light");
  await expect(page.locator("#generation-result-title")).toHaveText("В очереди Gateway");
  await expect(page.locator("#generation-progressbar")).toBeHidden();
  await page.screenshot({ path: info.outputPath("durable-queue-light.png") });
  failed = true;
  await expect(page.locator("#generation-result")).toHaveClass(/has-error/);
  await expect(page.locator("#generation-result-title")).toHaveText("Запуск завершён");
  await expect(page.locator("#generation-result")).toContainText("Модель больше недоступна.");
  await expect(page.locator("#generation-cancel")).toBeHidden();
  await expect(page.locator("#generation-submit")).toBeEnabled();
  expect(launches).toBe(1);
  await assertNoViewportOverflow(page, "dispatch failure");
});
