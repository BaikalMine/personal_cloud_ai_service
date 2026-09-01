const path = require("node:path");
const { test, expect } = require("@playwright/test");
const AxeBuilder = require("@axe-core/playwright").default;
const { installPreviewClock, settlePage, assertNoViewportOverflow, expectFocusInside } = require("./helpers.cjs");

const visualStyle = path.join(__dirname, "visual-stability.css");
const responsiveRoutes = [
  { route: "/preview/generate", snapshot: "generation-step-1.png" },
  { route: "/preview/gallery", snapshot: "gallery.png" },
  { route: "/preview/profile", snapshot: "profile.png" },
  { route: "/preview/invites", snapshot: "invitations.png" },
  { route: "/preview/invite", snapshot: "invite-registration.png" },
  { route: "/preview/users", snapshot: "users.png" },
  { route: "/preview/user", snapshot: "user-detail.png" },
  { route: "/preview/admin", snapshot: "admin-dashboard.png" },
  { route: "/preview/content", snapshot: "ai-content.png" },
  { route: "/preview/suggestions", snapshot: "suggestions.png" },
  { route: "/preview/admin-suggestions", snapshot: "admin-suggestions.png" },
];

const open = async (page, route) => {
  await installPreviewClock(page);
  await page.goto(route, { waitUntil: "domcontentloaded" });
  await settlePage(page);
};

const desktopOnly = (testInfo) => {
  test.skip(testInfo.project.name !== "desktop-1440", "interaction and axe checks run once in the canonical desktop project");
};

test("key surfaces fit every supported viewport", async ({ page }, testInfo) => {
  for (const { route, snapshot } of responsiveRoutes) {
    await open(page, route);
    await assertNoViewportOverflow(page, `${testInfo.project.name} ${route}`);
    await expect(page).toHaveScreenshot(snapshot, { fullPage: true, stylePath: visualStyle });
  }
});

test("suggestion intake and review expose one clear next action", async ({ page }, testInfo) => {
  desktopOnly(testInfo);

  await open(page, "/preview/suggestions");
  await expect(page.locator('.suggestion-kind-picker input[name="kind"]')).toHaveCount(4);
  await expect(page.getByRole("button", { name: "Сохранить черновик" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Отправить", exact: true })).toBeVisible();
  await page.locator('input[name="workflow_json"]').setInputFiles({
    name: "portrait-workflow.json",
    mimeType: "application/json",
    buffer: Buffer.from('{"version": 1}'),
  });
  await expect(page.locator("[data-suggestion-file-name]")).toHaveText("portrait-workflow.json");
  await expect(page.locator(".suggestion-user-item")).toHaveCount(3);
  await expect(page.locator(".suggestion-user-item").nth(2)).toContainText("Комментарий администратора");

  await open(page, "/preview/admin-suggestions");
  await expect(page.locator(".admin-suggestion-item")).toHaveCount(3);
  await expect(page.getByRole("link", { name: "Скачать проверенный JSON" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Принять в работу" })).toHaveCount(1);
  await expect(page.locator(".admin-suggestion-no-files")).toContainText("только описание");
  await page.locator(".admin-suggestion-diagnostics").first().getByText("Диагностика VirusTotal").click();
  await expect(page.locator(".admin-suggestion-diagnostics").first()).toHaveAttribute("open", "");
});

test("generation wizard covers Krea2, Flux2 and MiniMax", async ({ page }, testInfo) => {
  desktopOnly(testInfo);

  await open(page, "/preview/generate");
  await page.getByRole("button", { name: /Текст в изображение/ }).click();
  await expect(page.locator(".generation-workflow-choice.is-selected")).toContainText("PhotoFlow Krea2");
  await expect(page.locator("#generation-model")).toHaveValue("krea2:test");
  await page.getByRole("button", { name: "Продолжить" }).click();
  await expect(page.locator("#generation-summary")).toContainText("Текст в изображение");

  await open(page, "/preview/generate");
  await page.getByRole("button", { name: /Фото и промт/ }).click();
  await page.getByRole("button", { name: /Flux2 Редактирование/ }).click();
  await expect(page.locator("#generation-model")).toHaveValue("flux2:test");
  const primaryGallery = page.locator('[data-image-slot="1"] [data-gallery-image-picker-open]');
  await primaryGallery.click();
  await page.getByRole("button", { name: "Выбрать AI-Gateway-Krea2-portrait.png" }).click();
  await expect(page.locator('[data-image-slot="1"] [data-image-name]')).toHaveText("AI-Gateway-Krea2-portrait.png");
  await page.locator("#workflow-next").click();
  await expect(page.locator("#generation-summary")).toContainText("Flux 2 / Klein 9B");
  await expect(page.locator("#generation-summary")).toContainText("1 фото (1 из галереи)");
  const promptBox = await page.locator("#positive-prompt").boundingBox();
  const launchBox = await page.locator("#generation-run-dock").boundingBox();
  expect(launchBox.y, "launch actions must stay below the prompt instead of covering it").toBeGreaterThan(promptBox.y + promptBox.height);
  await expect(page).toHaveScreenshot("generation-flux-summary.png", { fullPage: true, stylePath: visualStyle });

  await open(page, "/preview/generate");
  await page.getByRole("button", { name: /Видео Создаёт/ }).click();
  await expect(page.locator("#generation-mode-guide-title")).toHaveText("Видео полностью по вашему описанию");
  await page.locator('[data-image-slot="1"] [data-gallery-image-picker-open]').click();
  await page.getByRole("button", { name: "Выбрать AI-Gateway-Krea2-portrait.png" }).click();
  await expect(page.locator("#generation-mode-guide-title")).toHaveText("Видео начинается с выбранного фото");
  await page.locator('[data-image-slot="2"] [data-gallery-image-picker-open]').click();
  await page.getByRole("button", { name: "Выбрать AI-Gateway-Krea2-product.png" }).click();
  await expect(page.locator("#generation-mode-guide-title")).toHaveText("Переход между двумя точными кадрами");
  await page.locator("#minimax-video-mode label").filter({ hasText: "Промт + свободные референсы" }).click();
  await expect(page.locator("#generation-mode-guide-title")).toHaveText("Видео по промту и выбранным ориентирам");
  await expect(page.locator(".source-image-card:visible")).toHaveCount(4);
  await expect(page.locator("#minimax-audio-reference")).toBeVisible();
  await expect(page.locator("#minimax-video-reference")).toBeVisible();
});

test("video reference sources use equal tiles at every viewport", async ({ page }) => {
  await open(page, "/preview/generate");
  await page.getByRole("button", { name: /Видео Создаёт/ }).click();

  const visibleSlots = page.locator(".source-image-card:visible");
  await expect(visibleSlots).toHaveCount(2);
  const tileBoxes = [];
  for (let index = 0; index < 2; index += 1) {
    const slot = visibleSlots.nth(index);
    const uploadBox = await slot.locator(".upload-zone").boundingBox();
    const galleryBox = await slot.locator(".source-gallery-button").boundingBox();
    tileBoxes.push(uploadBox, galleryBox);
  }
  for (const [index, tileBox] of tileBoxes.entries()) {
    expect(Math.abs(tileBox.width - tileBoxes[0].width), `tile ${index + 1} width`).toBeLessThanOrEqual(1);
    expect(Math.abs(tileBox.height - tileBoxes[0].height), `tile ${index + 1} height`).toBeLessThanOrEqual(1);
  }
  await expect(page.locator("#image-source-fields")).toHaveScreenshot("generation-video-reference-sources.png", { stylePath: visualStyle });
});

test("repeat restores the active workflow LoRA stack and strengths", async ({ page }, testInfo) => {
  desktopOnly(testInfo);

  await open(page, "/preview/generate?variant=preview-1");
  await expect(page.locator("#generation-repeat-notice")).toContainText("Параметры перенесены");
  await expect(page.locator('.krea-lora-row select[name="lora_1"]')).toHaveValue("lenovo_krea2.safetensors");
  await expect(page.locator('.krea-lora-row input[name="lora_model_strength_1"]')).toHaveValue("0.72");
  await expect(page.locator('.krea-lora-row input[name="lora_clip_strength_1"]')).toHaveValue("0.91");
  await expect(page.locator('.krea-lora-row select[name="lora_2"]')).toHaveValue("Krea2-realism-V2.safetensors");
  await expect(page.locator('.krea-lora-row input[name="lora_model_strength_2"]')).toHaveValue("1.15");
  await expect(page.locator('.krea-lora-row input[name="lora_clip_strength_2"]')).toHaveValue("0.84");
  await page.locator("details.generation-advanced > summary").click();
  await expect(page.locator('.krea-lora-row[data-lora-slots="krea"]').nth(1)).toBeVisible();
});

test("controlled generation batches stay clear and usable at every viewport", async ({ page }, testInfo) => {
  await open(page, "/preview/generate");
  await page.getByRole("button", { name: /Текст в изображение/ }).click();
  await page.getByRole("button", { name: "Продолжить" }).click();

  await page.locator("#generation-batch-enabled").check();
  await page.locator(".generation-batch-mode .ui-segment").filter({ hasText: "Один параметр" }).click();
  await page.locator("#generation-batch-count").fill("3");
  await page.locator("#generation-batch-parameter").selectOption("steps");
  await page.locator("#generation-batch-from").fill("7");
  await page.locator("#generation-batch-to").fill("9");
  await expect(page.locator("#generation-submit")).toHaveText("Запустить 3 варианта");
  await expect(page.locator("#generation-batch-error")).toBeHidden();
  await page.locator("body").evaluate((body) => body.classList.add("visualize-generation-batches"));

  const group = page.locator('section.generation-batch-group[data-batch-id="batch-krea-steps"]');
  await expect(group).toBeVisible();
  await expect(group.locator(".generation-job--batch")).toHaveCount(3);
  await expect(group.locator(".generation-batch-progress")).toContainText("2 из 3");
  await expect(group.getByRole("button", { name: "Новая ветка" })).toBeVisible();
  await expect(group).toHaveScreenshot("generation-batch-group.png", { stylePath: visualStyle });
  await assertNoViewportOverflow(page, `${testInfo.project.name} generation batch workbench`);
  await page.evaluate(() => window.scrollTo(0, 0));
  await settlePage(page);
  await expect(page).toHaveScreenshot("generation-batch-workbench.png", { fullPage: true, stylePath: visualStyle });

  const compareChecks = group.locator('.generation-batch-compare-toggle input[type="checkbox"]');
  await compareChecks.nth(0).check();
  await compareChecks.nth(1).check();
  await group.getByRole("button", { name: "Сравнить 2 варианта" }).click();
  await expect(page.locator("#generation-batch-compare")).toBeVisible();
  await expect(page.locator(".generation-batch-comparison-item")).toHaveCount(2);
  await expect(page.locator(".generation-batch-differences")).toContainText("Вариант 1: 7");
  await assertNoViewportOverflow(page, `${testInfo.project.name} generation batches`);
  await expect(page).toHaveScreenshot("generation-batches.png", { stylePath: visualStyle });
  await page.locator("#generation-batch-compare-close").click();
  await expect(page.locator("#generation-batch-compare")).toBeHidden();
});

test("prompt assistant review stays readable at every viewport", async ({ page }, testInfo) => {
  await open(page, "/preview/generate?template=image-to-image&workflow=photoflow-flux2-edit&media=1&slot=1&role=identity");
  await expect(page.locator('.generation-workflow-choice.is-selected[data-preset-id="photoflow-flux2-edit"]')).toBeVisible();
  await expect(page.locator('[data-image-slot="1"] [data-image-name]')).toHaveText("AI-Gateway-Krea2-portrait.png");
  await page.locator("#workflow-next").click();
  await page.locator("#positive-prompt").fill("Сохранить внешность и композицию, заменить куртку на красную кожаную.");
  await page.locator("#prompt-assistant-enabled").check();
  await page.locator("#prompt-assistant-improve").click();

  const review = page.locator("#prompt-assistant-review");
  await expect(review).toBeVisible();
  await expect(page.locator("#prompt-assistant-reference-list li")).toHaveCount(1);
  await expect(page.locator("#prompt-assistant-reference-count")).toHaveText("1 источник");
  await expect(page.locator("#prompt-assistant-review-meta")).toContainText("460 токенов");
  await expect(page.locator("#prompt-assistant-diff-suggestion ins").first()).toBeVisible();
  await expect(page.locator("#prompt-assistant-draft")).toHaveValue(/Preserve the subject/);
  await assertNoViewportOverflow(page, `${testInfo.project.name} prompt assistant review`);
  await page.evaluate(() => {
    if (document.activeElement instanceof HTMLElement) document.activeElement.blur();
    window.scrollTo(0, 0);
  });
  await expect(page).toHaveScreenshot("prompt-assistant-review.png", { fullPage: true, stylePath: visualStyle });

  await page.locator("#prompt-assistant-apply").click();
  await expect(review).toBeHidden();
  await expect(page.locator("#prompt-assistant-state")).toContainText("Вариант ассистента применён");
});

test("media library is shared by Krea2, Flux2 and MiniMax", async ({ page }, testInfo) => {
  desktopOnly(testInfo);

  await open(page, "/preview/gallery");
  await expect(page.locator("[data-gallery-item]")).toHaveCount(6);
  await page.locator("[data-gallery-search]").fill("Flux 2");
  await expect(page.locator("[data-gallery-item]:visible")).toHaveCount(1);
  await page.locator("[data-gallery-search]").fill("");

  await page.locator("[data-gallery-use-open]").first().click();
  await expect(page.locator("#gallery-use-dialog")).toBeVisible();
  await expect(page.locator('#gallery-use-dialog input[name="workflow"]')).toHaveCount(3);
  await page.locator('#gallery-use-dialog input[value="photoflow-krea2-edit"]').check();
  await expect(page.locator('#gallery-use-dialog select[name="slot"] option[value="3"]')).toHaveAttribute("disabled", "");
  await page.locator('#gallery-use-dialog input[value="photoflow-flux2-edit"]').check();
  await expect(page.locator('#gallery-use-dialog select[name="slot"] option[value="4"]')).toBeEnabled();
  await page.locator('#gallery-use-dialog input[value="minimax-h3-video"]').check();
  await expect(page.locator("[data-gallery-use-hint]")).toContainText("MiniMax H3");
  await page.locator("[data-gallery-use-close]").last().click();

  await page.locator("[data-gallery-compare]").first().click();
  await expect(page.locator("[data-gallery-compare-grid] figure")).toHaveCount(2);
  await page.keyboard.press("Escape");
  await expect(page.locator("#gallery-compare-dialog")).toBeHidden();

  await page.locator("[data-gallery-select]").first().check();
  await expect(page.locator("[data-gallery-selection-bar]")).toBeVisible();

  for (const target of [
    { workflow: "photoflow-krea2-edit", template: "image-to-image", slot: 2 },
    { workflow: "photoflow-flux2-edit", template: "image-to-image", slot: 4 },
    { workflow: "minimax-h3-video", template: "minimax-h3-video", slot: 3 },
  ]) {
    await open(page, `/preview/generate?template=${target.template}&workflow=${target.workflow}&media=1&slot=${target.slot}&role=style`);
    await expect(page.locator(`.generation-workflow-choice.is-selected[data-preset-id="${target.workflow}"]`)).toBeVisible();
    await expect(page.locator(`[data-image-slot="${target.slot}"] [data-image-name]`)).toHaveText("AI-Gateway-Krea2-portrait.png");
  }
});

test("notification center fits every viewport", async ({ page }, testInfo) => {
  await open(page, "/preview/generate");
  const trigger = page.getByRole("button", { name: /Задачи: активных 2, непрочитанных 2/ });
  await trigger.click();
  const panel = page.getByRole("dialog", { name: "Задачи и уведомления" });
  await expect(panel).toBeVisible();
  await expect(panel).toContainText("Результат Krea2 сохранён");
  await expect(panel).toContainText("Flux2 не удалось подготовить");
  const box = await panel.boundingBox();
  const viewport = page.viewportSize();
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.y).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(viewport.width);
  expect(box.y + box.height).toBeLessThanOrEqual(viewport.height);
  if (viewport.width === 390) {
    const topbar = await page.locator(".user-topbar").boundingBox();
    expect(topbar.height, "the task trigger must not push the mobile menu onto a second row").toBeLessThanOrEqual(70);
  }
  await assertNoViewportOverflow(page, `${testInfo.project.name} notification center`);
  await expect(page).toHaveScreenshot("notification-center.png", { stylePath: visualStyle });
});

test("notification actions open the exact generation", async ({ page }, testInfo) => {
  desktopOnly(testInfo);
  await open(page, "/preview/generate");
  const trigger = page.getByRole("button", { name: /Задачи: активных 2, непрочитанных 2/ });
  await trigger.click();
  await expect(page.getByRole("button", { name: "Закрыть уведомления" })).toBeFocused();
  await page.keyboard.press("Shift+Tab");
  await expect(page.getByRole("link", { name: "Настроить" })).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(page.getByRole("button", { name: "Закрыть уведомления" })).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog", { name: "Задачи и уведомления" })).toBeHidden();
  await expect(trigger).toBeFocused();

  await trigger.click();
  const readRequest = page.waitForRequest((request) => request.url().endsWith("/notifications/read") && request.method() === "POST");
  await page.getByRole("button", { name: "Прочитать все" }).click();
  await readRequest;

  await page.getByRole("link", { name: /Генерация готова/ }).click();
  await expect(page).toHaveURL(/job=job-image-completed/);
  const target = page.locator('[data-job-id="job-image-completed"]');
  await expect(target).toHaveClass(/is-notification-target/);
  await expect(target.locator(".generation-job-details")).toHaveAttribute("open", "");
  await expect(target.locator(".generation-job-timeline li")).toHaveCount(8);
});

test("notification settings disable browser delivery with in-app notifications", async ({ page }, testInfo) => {
  desktopOnly(testInfo);
  await open(page, "/preview/profile");
  const inApp = page.locator('[name="in_app_enabled"]');
  const browser = page.locator('[name="browser_enabled"]');
  await expect(inApp).toBeChecked();
  await inApp.uncheck();
  await expect(browser).not.toBeChecked();
  await expect(browser).toBeDisabled();
  await expect(page.locator("[data-browser-notification-status]")).toContainText(/Разрешение|Заблокированы|не поддерживает/);
});

test("AI content keeps one live card for the whole generation task", async ({ page }, testInfo) => {
  desktopOnly(testInfo);

  await open(page, "/preview/content");
  await expect(page.locator("[data-content-task-key]")).toHaveCount(4);
  await expect(page.getByText("Учётная запись удалена")).toBeVisible();
  await expect(page.locator("[data-content-live-label]")).toContainText("задания обновляются сразу");

  const taskTrigger = page.locator('[data-content-task-key="job-preview-krea"] [data-content-task-open]');
  await taskTrigger.click();
  await expect(page.locator("#content-detail-dialog")).toBeVisible();
  await expectFocusInside(page, "#content-detail-dialog");
  await page.keyboard.press("Tab");
  await expectFocusInside(page, "#content-detail-dialog");
  await expect(page.getByRole("heading", { name: "Стадии задания" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Запрос пользователя" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Ответ ассистента" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Применённый итоговый промт" })).toBeVisible();
  await expect(page).toHaveScreenshot("ai-content-task-detail.png", { stylePath: visualStyle });
  await page.keyboard.press("Escape");
  await expect(taskTrigger).toBeFocused();

  await page.locator('[data-content-task-key="job-preview-error"] [data-content-task-open]').click();
  await expect(page.locator("#content-detail-dialog")).toContainText("Выбранная модель больше не доступна в ComfyUI");
});

test("AI content updates only the changed task after an SSE revision", async ({ page }, testInfo) => {
  desktopOnly(testInfo);

  await page.addInitScript(() => {
    class PreviewEventSource {
      constructor() {
        this.listeners = new Map();
        window.__previewContentEvents = this;
      }
      addEventListener(type, listener) {
        const listeners = this.listeners.get(type) || [];
        listeners.push(listener);
        this.listeners.set(type, listeners);
      }
      emit(type, data) {
        (this.listeners.get(type) || []).forEach((listener) => listener({ data }));
      }
      close() {}
    }
    window.EventSource = PreviewEventSource;
  });
  await page.route("**/preview/content?live=1", async (route) => {
    const response = await route.fetch();
    const updated = (await response.text())
      .replace('data-content-revision="45"', 'data-content-revision="46"')
      .replace(/(data-content-task-key="job-preview-krea" data-content-version=")[^"]+/, "$1live-46")
      .replace("Krea2 / Raw INT8 Mixed", "Krea2 / Live update");
    await route.fulfill({ response, body: updated });
  });

  await open(page, "/preview/content");
  const unchangedTask = page.locator("[data-content-task-key]").nth(1);
  await unchangedTask.evaluate((element) => { element.dataset.domSentinel = "preserved"; });
  await page.evaluate(() => window.__previewContentEvents.emit("content", "46"));

  await expect(page.locator('[data-content-task-key="job-preview-krea"] .content-gallery-model')).toHaveText("Krea2 / Live update");
  await expect(unchangedTask).toHaveAttribute("data-dom-sentinel", "preserved");
  await expect(page.locator("[data-content-task-key]")).toHaveCount(4);
});

test("wizard, image picker and lightbox work from the keyboard", async ({ page }, testInfo) => {
  desktopOnly(testInfo);

  await open(page, "/preview/generate");
  const imageScenario = page.getByRole("button", { name: /Фото и промт/ });
  await imageScenario.focus();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("heading", { name: "Выберите workflow и модель" })).toBeVisible();
  const fluxWorkflow = page.getByRole("button", { name: /Flux2 Редактирование/ });
  await fluxWorkflow.focus();
  await page.keyboard.press("Enter");
  const pickerTrigger = page.locator('[data-image-slot="1"] [data-gallery-image-picker-open]');
  await pickerTrigger.focus();
  await page.keyboard.press("Enter");
  await expect(page.locator("#generation-image-picker")).toBeVisible();
  await expectFocusInside(page, "#generation-image-picker");
  await page.keyboard.press("Escape");
  await expect(page.locator("#generation-image-picker")).toBeHidden();
  await expect(pickerTrigger).toBeFocused();

  await open(page, "/preview/gallery");
  const mediaTrigger = page.locator("[data-gallery-open]").first();
  await mediaTrigger.focus();
  await page.keyboard.press("Enter");
  await expect(page.locator("#gallery-lightbox")).toBeVisible();
  await expectFocusInside(page, "#gallery-lightbox");
  await page.keyboard.press("Tab");
  await expectFocusInside(page, "#gallery-lightbox");
  await page.keyboard.press("Escape");
  await expect(page.locator("#gallery-lightbox")).toBeHidden();
  await expect(mediaTrigger).toBeFocused();
});

test("preview exposes loading, empty, error, sensitive, offline, queued and completed states", async ({ page }, testInfo) => {
  desktopOnly(testInfo);

  await open(page, "/preview/components");
  await expect(page.locator(".ui-status-banner.is-loading")).toBeVisible();
  await expect(page.locator(".ui-status-banner.is-error")).toBeVisible();

  await open(page, "/preview/gallery-empty");
  await expect(page.locator(".ui-empty-state, .empty-state")).toBeVisible();

  await open(page, "/preview/gallery");
  await expect(page.locator("[data-sensitive-media]")).toHaveCount(1);
  await expect(page.getByText("Контент 18+")).toBeVisible();

  await open(page, "/preview/admin");
  await expect(page.locator(".dependency-state.offline").first()).toContainText("Нет связи");

  await open(page, "/preview/generate");
  await expect(page.locator("#generation-variant-list")).toContainText("В очереди");
  await expect(page.locator("#generation-variant-list")).toContainText("Готово");
  await expect(page.locator("#generation-variant-list")).toContainText("Ошибка");
});

test("operations center puts live work and failures before analytics", async ({ page }, testInfo) => {
  desktopOnly(testInfo);

  await open(page, "/preview/admin");
  await expect(page.getByRole("heading", { name: "Операционный центр" })).toBeVisible();
  await expect(page.locator("[data-ops-attention] .operations-attention-row")).toHaveCount(5);
  await expect(page.locator("[data-active-job]")).toHaveCount(2);
  await expect(page.locator(".operations-queue")).toContainText("ComfyUI выполняет текущую задачу");
  await expect(page.locator(".operations-job-row.is-overdue")).toContainText("Без изменений");
  await expect(page.locator(".system-history-summary > div")).toHaveCount(4);
  await expect(page.locator(".system-history-markers line")).toHaveCount(3);

  const workers = page.locator(".operations-worker-details");
  await workers.locator("summary").focus();
  await page.keyboard.press("Enter");
  await expect(workers).toHaveAttribute("open", "");
  await expect(workers.locator("[data-worker-key]").first()).toBeVisible();
});

test("critical product surfaces have no serious axe violations", async ({ page }, testInfo) => {
  desktopOnly(testInfo);
  const routes = ["/preview/components", "/preview/generate", "/preview/gallery", "/preview/profile", "/preview/invites", "/preview/users", "/preview/content", "/preview/admin", "/preview/suggestions", "/preview/admin-suggestions"];
  const routeViolations = [];

  for (const route of routes) {
    await open(page, route);
    const result = await new AxeBuilder({ page })
      .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"])
      .analyze();
    const violations = result.violations
      .filter((entry) => entry.impact === "critical" || entry.impact === "serious")
      .map((entry) => ({
        id: entry.id,
        impact: entry.impact,
        help: entry.help,
        targets: entry.nodes.map((node) => node.target.join(" ")),
      }));
    if (violations.length) routeViolations.push({ route, violations });
  }
  expect(routeViolations, JSON.stringify(routeViolations, null, 2)).toEqual([]);
});
