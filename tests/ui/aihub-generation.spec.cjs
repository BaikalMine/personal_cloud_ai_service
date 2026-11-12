const { test, expect } = require("@playwright/test");
const { settlePage, chooseWorkflow } = require("./helpers.cjs");

test("AIHUB regular model, clear prompt order and nine assistant references", async ({ page }, info) => {
  let request;
  await page.route("**/generate/prompt-assistant/jobs", route => {
    request=new URLSearchParams(route.request().postData());
    const id=request.get('request_id');
    return route.fulfill({json:{id,state:'completed',context:request.get('context'),original:request.get('prompt'),result:{prompt:"A complete scene.",correlation_id:id,references:[],model:"fixture"}}});
  });
  await page.goto("/preview/generate");await settlePage(page);
  await page.locator('[data-workflow-id="minimax-h3-video"]').click();
  await chooseWorkflow(page, "aihub-minimax-h3");
  await page.locator('[name="aihub_mode"][value="photo"]').check();
  await page.locator('#generation-model').selectOption('minimax:aihub-regular');
  await expect(page.locator('[name="aihub_steps"]')).toHaveValue('25');
  await expect(page.locator('[name="aihub_steps"]')).toHaveAttribute('min','20');
  await expect(page.locator('#generation-positive-prompt-label')).toHaveText('Основной промпт · сцена и действие');
  expect(await page.evaluate(()=>{
    const selectors=['.studio-models','.aihub-settings','.source-image-grid','.generation-prompt-fields','.aihub-audio-settings'];
    const nodes=selectors.map(s=>document.querySelector(s));
    return nodes.every((node,i)=>node && (!i || Boolean(nodes[i-1].compareDocumentPosition(node)&Node.DOCUMENT_POSITION_FOLLOWING)));
  })).toBe(true);
  await expect(page.locator('.aihub-advanced')).not.toHaveAttribute('open');
  for(let slot=1;slot<=9;slot++) {
    await page.locator(`[data-image-slot="${slot}"] [data-gallery-image-picker-open]`).click();
    await page.getByRole('button',{name:'Выбрать AI-Gateway-Krea2-product.png',exact:true}).click();
  }
  await page.locator('[name="aihub_audio_source"]').selectOption('ltx');
  await page.locator('[name="aihub_audio_reference"]').check();
  await page.locator('[name="aihub_t8"]').check();
  await page.locator('[name="aihub_audio_prompt"]').fill('A woman says: Hello.');
  await page.locator('#positive-prompt').fill('Она улыбается и приветствует зрителя.');
  await page.locator('#prompt-assistant-open').click();
  await expect(page.locator('#prompt-assistant-source-list li')).toHaveCount(9);
  await page.locator('#prompt-assistant-improve').click();
  await expect.poll(()=>request?.get('input_image_9')).toBeTruthy();
  expect(request.get('aihub_audio_prompt')).toBe('A woman says: Hello.');
  expect(request.get('aihub_audio_reference')).toBe('true');
  expect(request.get('aihub_t8')).toBe('true');
  expect(request.get('aihub_task')).toBe('reference');
  expect(request.get('aihub_mode')).toBe('photo');
  expect(request.get('input_video')).toBeNull();
  await expect(page.locator('#prompt-assistant-apply')).toBeEnabled();
  await page.locator('#prompt-assistant-close').click();
  await page.locator('[name="aihub_audio_prompt"]').fill('Different words.');
  await page.locator('#prompt-assistant-open').click();
  await expect(page.locator('#prompt-assistant-apply')).toBeDisabled();
  await expect(page.locator('#prompt-assistant-stale')).toBeVisible();
  await page.screenshot({path:info.outputPath('aihub-assistant-nine.png')});
  await page.locator('#prompt-assistant-close').click();
  await page.locator('#generation-model').selectOption('minimax:aihub');
  await expect(page.locator('[name="aihub_steps"]')).toHaveValue('6');
  await expect(page.locator('[name="aihub_steps"]')).toHaveAttribute('max','8');
});

test("AIHUB photo video with LTX excludes retained video and restores its draft", async ({ page }, info) => {
  let body;
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  await page.route("**/generate/preflight", route => {
    body = new URLSearchParams(route.request().postData());
    return route.fulfill({ json: { ok: true, checks: [] } });
  });
  await page.route("**/generate/upload/video", route => route.fulfill({ json: { name: "motion.mp4", subfolder: "users/preview" } }));
  await page.goto("/preview/generate");
  await settlePage(page);
  await page.locator('[data-workflow-id="minimax-h3-video"]').click();
  await chooseWorkflow(page, "aihub-minimax-h3");
  await page.locator('[data-image-slot="1"] [data-gallery-image-picker-open]').click();
  await page.getByRole("button", { name: "Выбрать AI-Gateway-Krea2-product.png", exact: true }).click();
  await page.locator("#minimax-video-file").setInputFiles({ name: "motion.mp4", mimeType: "video/mp4", buffer: Buffer.from("preview video") });
  await page.locator("#positive-prompt").fill("The toy car moves forward slowly.");
  await page.locator("#generation-preflight-button").click();
  await expect.poll(() => body?.get("input_video")).toBeTruthy();
  const retainedVideo = body.get("input_video");
  await page.locator('[name="aihub_audio_source"]').selectOption("original");
  await page.locator(".aihub-advanced > summary").click();
  await page.locator('[name="aihub_mask"]').selectOption("sam3");
  await page.locator('[name="aihub_mode"][value="photo"]').check();
  if (info.project.name === "mobile-390") {
    await page.setViewportSize({ width: 320, height: 800 });
    const modes = page.locator(".aihub-settings .minimax-mode-segments");
    expect(await modes.evaluate(element => {
      const bounds = element.getBoundingClientRect();
      return [...element.querySelectorAll(".minimax-mode-option")].every(option => {
        const box = option.getBoundingClientRect();
        return box.left >= bounds.left && box.right <= bounds.right + 1;
      });
    })).toBe(true);
    await modes.screenshot({ path: info.outputPath("aihub-modes-320.png") });
    await page.setViewportSize(info.project.use.viewport);
  }
  await expect(page.locator("#minimax-video-reference")).toBeHidden();
  await expect(page.locator('[name="aihub_mask"]')).toBeDisabled();
  await expect(page.locator('[name="aihub_mask_prompt"]')).toBeDisabled();
  await expect(page.locator('[name="aihub_audio_source"]')).toHaveValue("native");
  await expect(page.locator('[name="aihub_audio_source"] option[value="original"]')).toHaveJSProperty("disabled", true);
  await page.locator('[name="aihub_audio_source"]').selectOption("ltx");
  await page.locator('[name="aihub_audio_prompt"]').fill("Soft mechanical sounds and quiet music.");
  await page.locator('[name="aihub_t8"]').check();
  await page.locator('[name="aihub_voice_conversion"]').check();
  await expect(page.locator("#minimax-audio-reference")).toBeVisible();
  await page.locator('[name="aihub_voice_conversion"]').uncheck();
  body = undefined;
  await page.locator("#generation-preflight-button").click();
  await expect.poll(() => body?.get("aihub_mode")).toBe("photo");
  expect(body.get("aihub_audio_source")).toBe("ltx");
  expect(body.get("aihub_audio_prompt")).toContain("mechanical");
  expect(body.get("input_image")).toBeTruthy();
  expect(body.get("input_video") || "").toBe("");
  expect(body.get("aihub_mask")).toBeNull();
  expect(body.get("aihub_mask_prompt")).toBeNull();
  expect(body.get("aihub_t8")).toBe("true");
  await page.locator(".studio-saved > summary").click();
  await page.locator("#generation-draft-save").click();
  await expect(page.locator("#generation-draft-status")).toHaveText("Сохранено");
  await page.reload();
  await expect(page.locator('[name="aihub_mode"][value="photo"]')).toBeChecked();
  await expect(page.locator('[name="aihub_audio_source"]')).toHaveValue("ltx");
  await expect(page.locator('[name="aihub_audio_prompt"]')).toHaveValue("Soft mechanical sounds and quiet music.");
  await expect(page.locator("#minimax-video-reference")).toBeHidden();
  expect(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)).toBe(false);
  await expect(page.locator("#image-source-note")).toHaveText("Фото 1 обязательно · до 9 фото-референсов");
  await expect(page.locator('[data-image-slot="1"] [data-image-slot-label]')).toHaveText("Фото 1 · обязательно");
  await page.locator(".aihub-settings").scrollIntoViewIfNeeded();
  await page.screenshot({ path: info.outputPath("aihub-photo-ltx.png"), fullPage: true });
  await page.screenshot({ path: info.outputPath("aihub-photo-viewport.png") });
  await page.locator('[name="aihub_mode"][value="video"]').check();
  await expect(page.locator("#minimax-video-reference")).toBeVisible();
  await expect(page.locator('[name="input_video"]')).toHaveValue(retainedVideo);
  expect(errors).toEqual([]);
});

test("AIHUB retains nine photos in launch and saved draft", async ({ page }) => {
  let body;
  await page.route("**/generate/preflight", route => {
    body = new URLSearchParams(route.request().postData());
    return route.fulfill({ json: { ok: true, checks: [] } });
  });
  await page.route("**/generate/upload/video", route => route.fulfill({ json: { name: "motion.mp4", subfolder: "users/preview" } }));
  await page.goto("/preview/generate");
  await settlePage(page);
  await page.locator('[data-workflow-id="minimax-h3-video"]').click();
  await chooseWorkflow(page, "aihub-minimax-h3");
  for (let slot = 1; slot <= 9; slot++) {
    await page.locator('[data-image-slot="' + slot + '"] [data-gallery-image-picker-open]').click();
    await page.getByRole("button", { name: "Выбрать AI-Gateway-Krea2-product.png", exact: true }).click();
  }
  await page.locator("#minimax-video-file").setInputFiles({ name: "motion.mp4", mimeType: "video/mp4", buffer: Buffer.from("preview video") });
  await page.locator("#positive-prompt").fill("A red toy car turns on a tabletop.");
  await page.locator("#generation-preflight-button").click();
  await expect.poll(() => body?.get("input_image_9")).toBeTruthy();
  expect(body.get("generation_workflow")).toBe("aihub-minimax-h3");
  expect(body.get("video_mode")).toBe("references");
  expect(body.get("aihub_mode")).toBe("video");
  await page.locator(".studio-saved > summary").click();
  await page.locator("#generation-draft-save").click();
  await expect(page.locator("#generation-draft-status")).toHaveText("Сохранено");
  await page.reload();
  await expect(page.locator('[data-preset-id="aihub-minimax-h3"]')).toHaveClass(/is-selected/);
  await expect(page.locator('[data-image-slot="9"] [data-image-name]')).toHaveText("AI-Gateway-Krea2-product.png");
});

test("AIHUB modes, media and existing H3 remain independent", async ({ page }, info) => {
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  await page.goto("/preview/generate");
  await settlePage(page);
  await page.locator('[data-workflow-id="minimax-h3-video"]').click();
  await chooseWorkflow(page, "aihub-minimax-h3");
  await expect(page.locator(".aihub-settings")).toBeVisible();
  await expect(page.locator('[data-image-slot]:visible')).toHaveCount(1);
  await expect(page.locator("#minimax-video-reference")).toBeVisible();
  await expect(page.locator("#minimax-audio-reference")).toBeHidden();
  await expect(page.locator('[name="video_steps"]')).toBeDisabled();
  await page.locator('[name="aihub_audio_source"]').selectOption("ltx");
  await expect(page.locator('[name="aihub_audio_prompt"]')).toBeVisible();
  await page.locator('[name="aihub_voice_conversion"]').check();
  await expect(page.locator("#minimax-audio-reference")).toBeVisible();
  await page.locator('[name="aihub_mode"][value="carousel"]').check();
  await expect(page.locator('[data-image-slot]:visible')).toHaveCount(1);
  await expect(page.locator('[name="aihub_audio_prompt"]')).toBeDisabled();
  await expect(page.locator("#minimax-audio-reference")).toBeHidden();
  await page.locator(".aihub-advanced > summary").click();
  await page.locator('[name="aihub_carousel_pipeline"]').selectOption('legacy');
  await expect(page.locator('[name="aihub_photo_stride"]')).toBeVisible();
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth);
  expect(overflow).toBe(false);
  await page.screenshot({ path: info.outputPath("aihub-carousel.png"), fullPage: true });
  await chooseWorkflow(page, "minimax-h3-video");
  await expect(page.locator(".aihub-settings")).toBeHidden();
  await page.locator('[name="video_mode"][value="frames"]').check();
  await expect(page.locator('[data-image-slot]:visible')).toHaveCount(2);
  await expect(page.locator('[name="aihub_photo_stride"]')).toBeDisabled();
  expect(errors).toEqual([]);
});
