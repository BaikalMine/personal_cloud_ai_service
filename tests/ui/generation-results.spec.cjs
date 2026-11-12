const { test, expect } = require("@playwright/test");
const AxeBuilder = require("@axe-core/playwright").default;
const { installPreviewClock, settlePage, assertNoViewportOverflow } = require("./helpers.cjs");
const imageJob = (id = "history-image-1", extra = {}) => ({
  job_id: id, state: "completed", template_id: "text-to-image", model_name: "Krea2 / preview.safetensors", seed: 42,
  prompt: "A portrait with natural light and carefully preserved detail.", message: "Результат сохранён.", retryable: true,
  created_at: "2026-09-01T07:10:00Z", expires_at: "2026-09-02T07:10:00Z", duration_seconds: 12,
  media: [{ id: 501, media_type: "image", url: "/preview/studio-result.jpg", filename: "history-portrait.jpg" }], ...extra,
});
const card = (page, id = "history-image-1") => page.locator(`#generation-variant-list [data-job-id="${id}"]`);
async function wire(page) {
  const state = { jobs: [imageJob()], batches: [], older: [], jobsStatus: 200, batchStatus: 200, detailStatus: 200, reads: 0, detailReads: 0 };
  await installPreviewClock(page);
  await page.addInitScript(() => {
    window.historyStreams = [];
    window.EventSource = class extends EventTarget {
      constructor(url) { super(); this.url = String(url); this.closed = false; window.historyStreams.push(this); }
      close() { this.closed = true; }
    };
  });
  await page.route("**/generate/jobs", route => {
    state.reads++;
    return route.fulfill({ status: state.jobsStatus, json: state.jobsStatus === 200 ? { jobs: state.jobs, revision: state.reads } : { error: "Не удалось обновить задания." } });
  });
  await page.route("**/generate/batches", route => route.fulfill({ status: state.batchStatus, json: state.batchStatus === 200 ? { batches: state.batches } : { error: "Пакеты пока не обновлены." } }));
  await page.route("**/generate/variants", route => route.fulfill({ json: { variants: [] } }));
  await page.route("**/generate/jobs/detail?**", route => {
    state.detailReads++;
    const id = new URL(route.request().url()).searchParams.get("job_id"), job = [...state.jobs, ...state.older, ...state.batches.flatMap(batch => batch.jobs)].find(job => job.job_id === id);
    return route.fulfill({ status: state.detailStatus, json: state.detailStatus === 200
      ? { job, transitions: [{ state: job?.state || "completed", created_at: "2026-09-01T07:10:00Z", message: "Этап сохранён." }] }
      : { error: "Этапы пока недоступны." } });
  });
  return state;
}
async function open(page, route = "/preview/generate") {
  await page.goto(route); await settlePage(page);
  if (page.viewportSize().width < 900 && !await page.locator("#generation-recent-inspector").isVisible()) await page.locator("#studio-result-tab").click();
  await expect.poll(() => page.evaluate(() => window.historyStreams.some(stream => !stream.closed && stream.url.includes("/generate/jobs/events")))).toBe(true);
  if (!route.includes("?") && await page.locator("#generation-history-content").isVisible()) {
    const first = page.locator("#generation-recent-list [data-recent-key]").first();
    if (await first.count()) await first.click();
  }
}
async function update(page, state) {
  const before = state.reads;
  await page.evaluate(() => window.historyStreams.filter(stream => !stream.closed && stream.url.includes("/generate/jobs/events")).forEach(stream => stream.dispatchEvent(new MessageEvent("jobs", { data: "{}" }))));
  await expect.poll(() => state.reads).toBeGreaterThan(before);
}
test.beforeEach(async ({ page }) => page.on("dialog", dialog => dialog.type() === "beforeunload" ? dialog.accept() : dialog.dismiss()));

for (const colorScheme of ["dark", "light"]) test.describe(colorScheme, () => {
test.use({ colorScheme });

test("live reads retain media nodes, expanded details and keyboard focus", async ({ page }, info) => {
  const state = await wire(page); await open(page);
  const current = card(page);
  const timeline = page.locator(".generation-single-view-details .generation-batch-viewer-timeline");
  await expect(current.locator("img")).toBeVisible();
  await expect.poll(() => current.locator("img").evaluate(node => node.naturalWidth)).toBeGreaterThan(0);
  await current.locator("img").evaluate(node => { node.dataset.retainedNode = "yes"; });
  await page.locator("#generation-batch-viewer-more").click();
  await timeline.locator("summary").click();
  await expect(timeline.locator(".generation-job-timeline")).toContainText("Этап сохранён.");
  await update(page, state);
  await expect(current.locator("img")).toHaveAttribute("data-retained-node", "yes");
  await expect(timeline.locator("summary")).toBeFocused();
  state.jobs = [imageJob("history-image-1", { message: "Данные результата обновлены." })];
  await update(page, state);
  await expect(page.locator(".generation-single-view-details .generation-batch-viewer-meta")).toContainText("Данные результата обновлены.");
  await expect(timeline).toHaveAttribute("open", "");
  await expect(timeline.locator("summary")).toBeFocused();
  await expect(timeline.locator(".generation-job-timeline")).toContainText("Этап сохранён.");
  await assertNoViewportOverflow(page, "updated generation history");
  await page.screenshot({ path: info.outputPath("history-expanded.png") });
});

const batchFixture = () => ({
  batch_id: "history-batch-1", mode: "parameter", parameter_name: "steps", parameter_label: "Шаги",
  template_id: "text-to-image", model_name: "Krea2", state: "running", total_count: 3, finished_count: 2,
  completed_count: 2, progress_percent: 67, cancellable: true, estimated_finish_seconds: 24,
  created_at: "2026-09-01T07:10:00Z",
  jobs: [imageJob("batch-job-1", { batch_position: 1, experiment_value: "7" }), imageJob("batch-job-2", { batch_position: 2, experiment_value: "9" }),
    imageJob("batch-job-3", { batch_position: 3, state: "running", media: [], cancellable: true })],
  differences: [{ name: "steps", label: "Шаги", values: [{ job_id: "batch-job-1", position: 1, value: "7" }, { job_id: "batch-job-2", position: 2, value: "9" }] }],
});
const group = page => page.locator('section.generation-batch-group[data-batch-id="history-batch-1"]');

test("completed batch opens as a focused viewer and returns to its tile", async ({ page }) => {
  const state = await wire(page);
  state.batches = [{ ...batchFixture(), state: "completed", finished_count: 3, completed_count: 3, progress_percent: 100,
    jobs: [imageJob("batch-job-1", { batch_position: 1 }), imageJob("batch-job-2", { batch_position: 2 }),
      imageJob("batch-job-3", { batch_position: 3 })] }];
  await open(page);
  const inspector = page.locator("#generation-recent-inspector");
  await expect(inspector).toHaveAttribute("aria-modal", "true");
  await expect(page.locator("#generation-batch-viewer-status")).toHaveText("Готово · 3 изображения");
  await expect(page.locator("body")).toHaveClass(/batch-viewer-open/);
  await page.locator("#generation-batch-viewer-more").click();
  await expect(page.locator("#generation-batch-viewer-more")).toHaveAttribute("aria-expanded", "true");
  await expect(group(page).getByRole("link", { name: "Открыть задачу" })).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(inspector).toBeHidden();
  await expect(page.locator('[data-recent-key="batch:history-batch-1"]')).toBeFocused();
});

test("swiping right advances a package or photo series and swiping left returns", async ({ page }) => {
  const state = await wire(page);
  state.batches = [batchFixture()];
  await open(page);
  const swipe = async (selector, from, to) => {
    const target = page.locator(selector);
    await target.dispatchEvent("pointerdown", { pointerId: 1, isPrimary: true, button: 0, clientX: from, clientY: 250 });
    await target.dispatchEvent("pointerup", { pointerId: 1, isPrimary: true, button: 0, clientX: to, clientY: 250 });
  };
  const batchCounter = group(page).locator(".generation-photo-counter");
  await swipe(".generation-batch-stage .generation-batch-slide:not([hidden]):not(.is-leaving) .generation-job-preview", 60, 240);
  await expect(batchCounter).toHaveText("Вариант 2 / 3");
  await swipe(".generation-batch-stage .generation-batch-slide:not([hidden]):not(.is-leaving) .generation-job-preview", 240, 60);
  await expect(batchCounter).toHaveText("Вариант 1 / 3");
  await page.locator("#generation-recent-close").click();
  state.batches = [];
  state.jobs = [imageJob("photo-series", { media: [
    { id: 1, media_type: "image", url: "/preview/studio-result.jpg", filename: "one.jpg" },
    { id: 2, media_type: "image", url: "/preview/studio-result.jpg", filename: "two.jpg" },
  ] })];
  await update(page, state);
  await page.locator('[data-recent-key="job:photo-series"]').click();
  const photoCounter = page.locator(".generation-single-view .generation-photo-counter");
  await swipe(".generation-photo-stage .generation-output:not([hidden]) .generation-output-preview", 60, 240);
  await expect(photoCounter).toHaveText("2 / 2");
  await swipe(".generation-photo-stage .generation-output:not([hidden]) .generation-output-preview", 240, 60);
  await expect(photoCounter).toHaveText("1 / 2");
});

test("photo series download follows the active frame and waits for sensitive reveal", async ({ page }) => {
  const state = await wire(page);
  state.jobs = [imageJob("photo-series-sensitive", { media: [
    { id: 1, media_type: "image", url: "/preview/studio-result.jpg?frame=1", filename: "first.jpg" },
    { id: 2, media_type: "image", url: "/preview/studio-result.jpg?frame=2", filename: "second.jpg", sensitive: true },
  ] })];
  await open(page);
  const save = page.locator(".generation-single-view-dock .generation-batch-viewer-download");
  await expect(save).toHaveAttribute("download", "first.jpg");
  await page.locator(".generation-single-view .generation-photo-arrow[aria-label='Следующее фото']").click();
  await expect(save).toBeHidden();
  const preview = page.locator(".generation-single-view .generation-output:not([hidden]) .generation-output-preview");
  await preview.click();
  await expect(preview.locator("..")).toHaveClass(/is-revealed/);
  await expect(save).toBeVisible();
  await expect(save).toHaveAttribute("download", "second.jpg");
});

test("batch carousel keeps the chosen variant during live updates", async ({ page }) => {
  const state = await wire(page); state.batches = [batchFixture()]; await open(page);
  await expect(card(page, "batch-job-1")).toBeVisible();
  await expect(card(page, "batch-job-2")).toBeHidden();
  await group(page).getByRole("button", { name: "Следующий вариант" }).click();
  await expect(card(page, "batch-job-2")).toBeVisible();
  await expect(group(page).locator(".generation-photo-counter")).toHaveText("Вариант 2 / 3");
  state.batches[0].estimated_finish_seconds = 18;
  await update(page, state);
  await expect(card(page, "batch-job-2")).toBeVisible();
  await group(page).getByRole("button", { name: "Вариант 3: В работе" }).click();
  await expect(card(page, "batch-job-3")).toBeVisible();
  await group(page).getByRole("button", { name: "Предыдущий вариант" }).focus();
  await page.keyboard.press("Home");
  await expect(card(page, "batch-job-1")).toBeVisible();
  await assertNoViewportOverflow(page, "batch carousel");
});

test("batch thumbnails do not load sensitive results before reveal", async ({ page }) => {
  const state = await wire(page); state.batches = [batchFixture()];
  state.batches[0].jobs[0].media[0].sensitive = true;
  await open(page);
  const firstThumb = group(page).getByRole("button", { name: "Вариант 1: Готово" });
  await expect(firstThumb.locator("img")).toHaveCount(0);
  await expect(card(page, "batch-job-1").locator(".generation-job-preview img")).not.toHaveAttribute("src", /.+/);
  await group(page).getByRole("button", { name: "Следующий вариант" }).click();
  await expect(group(page).locator(".generation-batch-peek img")).toHaveCount(0);
  await group(page).getByRole("button", { name: "Предыдущий вариант" }).click();
  await expect(firstThumb.locator("img")).toHaveCount(0);
  const save = group(page).locator(".generation-batch-viewer-download");
  await expect(save).toBeHidden();
  await card(page, "batch-job-1").locator(".generation-job-preview").click();
  await expect(save).toBeVisible();
});

test("batch deck animates without leaving an old variant above the active result", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "no-preference" });
  const state = await wire(page); state.batches = [batchFixture()]; await open(page);
  const deck = group(page).locator(".generation-batch-stage");
  await expect(deck.locator(".generation-batch-peek")).toHaveCount(2);
  const animated = await page.evaluate(() => {
    const group = document.querySelector('[data-batch-id="history-batch-1"].generation-batch-group');
    group.querySelector('[data-result-action="batch-next"]').click();
    return group.querySelector(".generation-batch-slide.is-leaving .generation-job-preview")?.getAnimations().length > 0;
  });
  expect(animated).toBe(true);
  await group(page).getByRole("button", { name: "Следующий вариант" }).click();
  await expect(group(page).locator(".generation-photo-counter")).toHaveText("Вариант 3 / 3");
  await expect.poll(() => deck.locator(".generation-batch-slide.is-leaving").count()).toBe(0);
  await expect(card(page, "batch-job-1")).toBeHidden();
  await expect(card(page, "batch-job-2")).toBeHidden();
  await expect(card(page, "batch-job-3")).toBeVisible();
  await assertNoViewportOverflow(page, "animated batch deck");
});

test("batch selection, comparison, focus and pending winner survive live reads", async ({ page }, info) => {
  const state = await wire(page); state.batches = [batchFixture()];
  let winnerPosts = 0, releaseWinner;
  const hold = new Promise(resolve => { releaseWinner = resolve; });
  await page.route("**/generate/batches/winner", async route => {
    winnerPosts++; await hold;
    state.batches[0].winner_job_id = "batch-job-1";
    await route.fulfill({ json: { message: "Лучший вариант сохранён." } });
  });
  await open(page);
  await card(page, "batch-job-1").getByRole("checkbox").check();
  await group(page).getByRole("button", { name: "Следующий вариант" }).click();
  await card(page, "batch-job-2").getByRole("checkbox").check();
  await group(page).getByRole("button", { name: "Предыдущий вариант" }).click();
  await page.locator("#generation-batch-viewer-more").click();
  const timelineSummary = group(page).locator(".generation-batch-viewer-timeline > summary");
  await timelineSummary.click();
  await expect(group(page).locator(".generation-batch-viewer-timeline")).toHaveAttribute("data-active-job-id", "batch-job-1");
  await expect.poll(() => state.detailReads).toBeGreaterThan(0);
  await expect(group(page).locator(".generation-batch-viewer-timeline .generation-job-timeline")).toBeVisible();
  state.batches[0].estimated_finish_seconds = 21;
  await update(page, state);
  await expect(timelineSummary).toBeFocused();
  await expect(group(page).getByRole("progressbar")).toHaveAttribute("aria-valuenow", "67");
  await expect(group(page).locator('[data-result-action="compare"] svg')).toHaveCount(1);
  await group(page).locator('[data-result-action="compare"]').click();
  await expect(page.locator(".generation-batch-comparison-item")).toHaveCount(2);
  await expect(page.locator(".generation-batch-differences")).toContainText("Вариант 1: 7");
  await assertNoViewportOverflow(page, "history comparison");
  await page.screenshot({ path: info.outputPath("history-comparison.png") });
  await page.locator("#generation-batch-compare-close").click();
  await group(page).locator(".generation-batch-viewer-details").getByRole("button", { name: "Выбрать лучшим" }).click();
  await expect.poll(() => winnerPosts).toBe(1);
  await update(page, state);
  await expect(group(page).locator(".generation-batch-viewer-details [data-result-action='winner']")).toHaveCount(1);
  await group(page).getByRole("button", { name: "Следующий вариант" }).click();
  await expect(group(page).locator(".generation-batch-viewer-details").getByRole("button", { name: "Сохраняем..." })).toBeDisabled();
  releaseWinner();
  await group(page).getByRole("button", { name: "Предыдущий вариант" }).click();
  await expect(group(page).locator(".generation-batch-viewer-details").getByRole("button", { name: "Выбран", exact: true })).toBeDisabled();
  await group(page).getByRole("button", { name: "Следующий вариант" }).click();
  await expect(card(page, "batch-job-2").getByRole("checkbox")).toBeChecked();
  await expect(group(page).getByRole("button", { name: "Новая ветка" })).toBeVisible();
  expect(winnerPosts).toBe(1);
});

test("batch cancellation stays pending through refresh and never repeats the POST", async ({ page }) => {
  const state = await wire(page); state.batches = [batchFixture()];
  let posts = 0, releaseCancel;
  const hold = new Promise(resolve => { releaseCancel = resolve; });
  await page.route("**/generate/batches/cancel", async route => {
    posts++; await hold; state.batches[0].state = "cancelled"; state.batches[0].cancellable = false;
    await route.fulfill({ json: { message: "Отмена принята." } });
  });
  await open(page);
  await page.locator("#generation-batch-viewer-more").click();
  await group(page).getByRole("button", { name: "Отменить пакет" }).click();
  await expect.poll(() => posts).toBe(1); await update(page, state);
  await expect(group(page).getByRole("button", { name: "Отменяем..." })).toBeDisabled();
  releaseCancel();
  await expect(group(page).locator(".generation-batch-state")).toHaveText("Отменён");
  expect(posts).toBe(1);
});

test("late branch preparation preserves manual prompt changes", async ({ page }) => {
  const state = await wire(page); state.batches = [{ ...batchFixture(), winner_job_id: "batch-job-1" }];
  let posts = 0, releaseBranch;
  const hold = new Promise(resolve => { releaseBranch = resolve; });
  await page.route("**/generate/jobs/retry", async route => {
    posts++; await hold;
    await route.fulfill({ json: { parent_job_id: "batch-job-1", values: { template_id: "text-to-image", positive_prompt: "Branch server prompt" } } });
  });
  await open(page);
  await page.locator("#generation-batch-viewer-more").click();
  await group(page).getByRole("button", { name: "Новая ветка" }).click();
  await expect.poll(() => posts).toBe(1);
  if (page.viewportSize().width < 900) await page.locator("#studio-configure-tab").click();
  await page.locator("#positive-prompt").fill("My later manual prompt");
  releaseBranch();
  await expect(page.locator("#generation-repeat-notice")).toContainText("Параметры или материалы изменились");
  await expect(page.locator("#positive-prompt")).toHaveValue("My later manual prompt");
  expect(posts).toBe(1);
});

test("partial batch errors preserve known groups and a saved collapsed preference", async ({ page }) => {
  const state = await wire(page); state.batches = [batchFixture()]; await open(page);
  state.batchStatus = 503; await update(page, state);
  await expect(group(page)).toBeVisible();
  await expect(page.locator("#generation-history-error")).toContainText("Пакеты пока не обновлены");
  await page.locator("#generation-recent-close").click();
  await page.locator("#generation-history-toggle").click();
  await expect(page.locator("#generation-history-content")).toBeHidden();
  state.batchStatus = 200; await page.reload(); await settlePage(page);
  if (page.viewportSize().width < 900) await page.locator("#studio-result-tab").click();
  await expect(page.locator("#generation-history-content")).toBeHidden();
  await page.locator("#generation-history-toggle").click();
  await page.locator('[data-recent-key="batch:history-batch-1"]').click();
  await expect(group(page)).toBeVisible();
});

test("an older linked job is hydrated and revealed outside the recent list", async ({ page }) => {
  const state = await wire(page); state.older = [imageJob("history-older")];
  await page.addInitScript(() => localStorage.setItem("ai-gateway.generation-history-collapsed", "true"));
  await open(page, "/preview/generate?job=history-older");
  await expect(card(page, "history-older")).toBeVisible();
  await expect(page.locator(".generation-single-view-details")).toHaveAttribute("open", "");
  await expect(page.locator(".generation-single-view-details .generation-job-timeline")).toBeVisible();
});

test("read failure keeps the selected result with an explicit retry", async ({ page }, info) => {
  const state = await wire(page); await open(page);
  state.jobsStatus = 503; await update(page, state);
  await expect(page.locator("#generation-history-error")).toBeVisible();
  await expect(card(page)).toBeVisible();
  await expect(page.locator('[data-recent-key="job:history-image-1"]')).toHaveAttribute("aria-pressed", "true");
  await expect(page.locator("#generation-variant-count")).toContainText("не обновлены");
  await page.locator("#generation-recent-close").click();
  await page.locator("#generation-history-error").scrollIntoViewIfNeeded();
  await assertNoViewportOverflow(page, "history read error");
  await page.screenshot({ path: info.outputPath("history-read-error.png") });
  const axe = await new AxeBuilder({ page }).include("#generation-variants").analyze();
  expect(axe.violations.filter(value => ["serious", "critical"].includes(value.impact))).toEqual([]);
  state.jobsStatus = 200;
  await page.locator("#generation-history-refresh").click();
  await expect(page.locator("#generation-history-error")).toBeHidden();
  await page.locator('[data-recent-key="job:history-image-1"]').click();
  await expect(card(page)).toBeVisible();
});

test("access loss clears history, active output and an open viewer without another read", async ({ page }) => {
  const state = await wire(page); await open(page);
  await card(page).locator(".generation-job-preview").click();
  await expect(page.locator("#generation-lightbox")).toBeVisible();
  state.jobsStatus = 401; await update(page, state);
  await expect(page.locator("#generation-lightbox")).toBeHidden();
  await expect(page.locator("#generation-variant-list [data-job-id]")).toHaveCount(0);
  await expect(page.locator("#generation-output-grid img")).toHaveCount(0);
  await expect(page.locator("#generation-history-login")).toBeVisible();
  const reads = state.reads;
  await page.evaluate(() => document.dispatchEvent(new Event("visibilitychange")));
  await expect(page.locator("#generation-history-error")).toContainText("Сеанс завершён");
  expect(state.reads).toBe(reads);
});

test("timeline failure can be retried without collapsing the job", async ({ page }) => {
  const state = await wire(page); state.detailStatus = 503; await open(page);
  await page.locator("#generation-batch-viewer-more").click();
  const timeline = page.locator(".generation-single-view-details .generation-batch-viewer-timeline");
  await timeline.locator("summary").click();
  await expect(timeline).toContainText("Этапы пока недоступны.");
  state.detailStatus = 200;
  await timeline.getByRole("button", { name: "Повторить запрос" }).click();
  await expect(timeline.locator(".generation-job-timeline")).toContainText("Этап сохранён.");
  await expect(timeline).toHaveAttribute("open", "");
  expect(state.detailReads).toBe(2);
});

test("video previews decode, open the player and release playback on page departure", async ({ page }, info) => {
  const state = await wire(page);
  state.jobs = [imageJob("history-video", { template_id: "minimax-h3-video", media: [{ id: 502, media_type: "video", url: "/preview/task-video.mp4", filename: "history-video.mp4" }] })];
  await open(page);
  const preview = card(page, "history-video").locator("video");
  await preview.scrollIntoViewIfNeeded();
  await expect.poll(() => preview.evaluate(video => video.videoWidth)).toBeGreaterThan(0);
  await card(page, "history-video").getByRole("button", { name: "Открыть видео" }).click();
  const viewer = page.locator("#generation-lightbox");
  await expect(viewer).toBeVisible();
  await expect(viewer.locator("#generation-lightbox-zoom")).toBeHidden();
  await expect(viewer.locator("#generation-lightbox-download")).toBeVisible();
  await expect(viewer.locator("#generation-lightbox-download")).toHaveAttribute("href", "/preview/task-video.mp4?download=1");
  await expect.poll(() => viewer.locator("video").evaluate(video => video.readyState)).toBeGreaterThanOrEqual(2);
  await expect.poll(() => viewer.locator("video").evaluate(video => video.currentTime)).toBeGreaterThan(0);
  await page.screenshot({ path: info.outputPath("history-video.png") });
  await page.evaluate(() => window.dispatchEvent(new Event("pagehide")));
  await expect(viewer).toBeHidden();
  await expect(viewer.locator("video")).not.toHaveAttribute("src", /.+/);
  expect(await preview.evaluate(video => video.paused)).toBe(true);
});

test("sensitive history image reveals before opening the viewer", async ({ page }) => {
  const state = await wire(page);
  state.jobs = [imageJob("history-sensitive", { media: [{ id: 503, media_type: "image", url: "/preview/studio-result.jpg", filename: "sensitive.jpg", sensitive: true }] })];
  await open(page);
  const preview = card(page, "history-sensitive").locator(".generation-job-preview");
  await expect(preview.locator(".sensitive-media-cover")).toBeVisible();
  await preview.click();
  await expect(preview).toHaveClass(/is-revealed/);
  await expect(page.locator("#generation-lightbox")).toBeHidden();
  await preview.click();
  await expect(page.locator("#generation-lightbox")).toBeVisible();
  await expect.poll(() => page.locator("#generation-lightbox-image").evaluate(image => image.naturalWidth)).toBeGreaterThan(0);
});

test("image zoom stays inside the viewer and resets between results", async ({ page }, info) => {
  await wire(page); await open(page);
  const preview = card(page).locator(".generation-job-preview");
  await preview.scrollIntoViewIfNeeded();
  const client = await page.context().newCDPSession(page);
  await client.send("Emulation.setTouchEmulationEnabled", { enabled: true, maxTouchPoints: 2 });
  const previewBox = await preview.boundingBox();
  const previewPoint = (id, offset) => ({ id, x: Math.round(previewBox.x + previewBox.width / 2 + offset), y: Math.round(previewBox.y + previewBox.height / 2) });
  await client.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [previewPoint(1, -20), previewPoint(2, 20)] });
  const viewer = page.locator("#generation-lightbox");
  await expect(viewer).toBeVisible();
  await client.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
  expect(await page.evaluate(() => window.visualViewport.scale)).toBe(1);
  const viewport = viewer.locator("#generation-lightbox-viewport");
  const scale = viewer.locator("#generation-lightbox-scale");
  await expect.poll(() => viewer.locator("#generation-lightbox-image").evaluate(image => image.naturalWidth)).toBeGreaterThan(0);
  await expect(scale).toHaveText("100%");
  await viewer.getByRole("button", { name: "Увеличить" }).click();
  await expect(scale).toHaveText("130%");
  await viewer.getByRole("button", { name: "Вписать изображение" }).click();
  await expect(scale).toHaveText("100%");

  const box = await viewport.boundingBox();
  const point = (id, offset) => ({ id, x: Math.round(box.x + box.width / 2 + offset), y: Math.round(box.y + box.height / 2) });
  await client.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [point(1, -35), point(2, 35)] });
  await client.send("Input.dispatchTouchEvent", { type: "touchMove", touchPoints: [point(1, -80), point(2, 80)] });
  await client.send("Input.dispatchTouchEvent", { type: "touchMove", touchPoints: [point(1, -110), point(2, 110)] });
  await client.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
  await expect.poll(async () => Number((await scale.textContent()).replace("%", ""))).toBeGreaterThan(100);
  expect(await page.evaluate(() => window.visualViewport.scale)).toBe(1);
  const beforePan = await viewer.locator("#generation-lightbox-image").evaluate(image => image.style.transform);
  await client.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [point(1, 0)] });
  await client.send("Input.dispatchTouchEvent", { type: "touchMove", touchPoints: [{ ...point(1, 0), x: point(1, 0).x - 35 }] });
  await client.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
  expect(await viewer.locator("#generation-lightbox-image").evaluate(image => image.style.transform)).not.toBe(beforePan);
  await page.screenshot({ path: info.outputPath("image-zoom.png") });

  await viewer.locator(".generation-lightbox-close").click();
  await card(page).locator(".generation-job-preview").click();
  await expect(scale).toHaveText("100%");
  await expect(viewport).toHaveClass(/is-image/);
  await viewer.getByRole("button", { name: "Исходный размер 1:1" }).click();
  await expect(viewport.locator("#generation-lightbox-image")).toBeVisible();
});
});
