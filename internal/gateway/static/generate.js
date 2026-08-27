(() => {
  const root = document.querySelector("[data-comfy-generation]");
  if (!root) return;

  const form = document.getElementById("generation-form");
  const templateID = document.getElementById("template-id");
  const generationWorkflowID = document.getElementById("generation-workflow-id");
  const inputImages = [
    document.getElementById("input-image"),
    document.getElementById("input-image-2"),
    document.getElementById("input-image-3"),
    document.getElementById("input-image-4"),
  ];
  const imageSlots = [...root.querySelectorAll("[data-image-slot]")].map((slot) => ({
    slot,
    index: Number(slot.dataset.imageSlot),
    input: slot.querySelector("[data-image-file]"),
    preview: slot.querySelector("[data-image-preview]"),
    previewImage: slot.querySelector("[data-image-preview-image]"),
    name: slot.querySelector("[data-image-name]"),
    state: slot.querySelector("[data-image-state]"),
    remove: slot.querySelector("[data-remove-image]"),
    label: slot.querySelector("[data-image-slot-label]"),
  }));
  const workflowNext = document.getElementById("workflow-next");
  const imageSourceFields = document.getElementById("image-source-fields");
  const imageSourceGrid = root.querySelector(".source-image-grid");
  const workflowNote = document.getElementById("generation-workflow-note");
  const positive = document.getElementById("positive-prompt");
  const model = document.getElementById("generation-model");
  const steps = document.getElementById("generation-steps");
  const cfg = document.getElementById("generation-cfg");
  const denoise = document.getElementById("generation-denoise");
  const sampler = document.getElementById("generation-sampler");
  const scheduler = document.getElementById("generation-scheduler");
  const quality = document.getElementById("generation-quality");
  const qualityField = document.getElementById("generation-quality-field");
  const width = document.getElementById("generation-width");
  const height = document.getElementById("generation-height");
  const aspect = document.getElementById("generation-aspect");
  const outputMegapixels = document.getElementById("generation-output-megapixels");
  const dimensionMultiple = document.getElementById("generation-dimension-multiple");
  const maxSide = document.getElementById("generation-max-side");
  const resolutionPreview = document.getElementById("generation-resolution-preview");
  const baseMegapixels = document.getElementById("generation-base-megapixels");
  const upscaleSteps = document.getElementById("generation-upscale-steps");
  const upscaleDenoise = document.getElementById("generation-upscale-denoise");
  const autoDenoise = document.getElementById("generation-auto-denoise");
  const detailSteps = document.getElementById("generation-detail-steps");
  const detailDenoise = document.getElementById("generation-detail-denoise");
  const result = document.getElementById("generation-result");
  const resultTitle = document.getElementById("generation-result-title");
  const resultStatus = document.getElementById("generation-status");
  const runProgress = document.getElementById("generation-run-progress");
  const generationStage = document.getElementById("generation-stage");
  const generationStageDetail = document.getElementById("generation-stage-detail");
  const generationProgressbar = document.getElementById("generation-progressbar");
  const generationProgressbarFill = document.getElementById("generation-progressbar-fill");
  const generationQueue = document.getElementById("generation-queue");
  const generationQueueTitle = document.getElementById("generation-queue-title");
  const generationQueueDetails = document.getElementById("generation-queue-details");
  const outputGrid = document.getElementById("generation-output-grid");
  const editorProfile = document.getElementById("generation-editor-profile");
  const editorProfileTitle = document.getElementById("generation-editor-profile-title");
  const editorProfileDescription = document.getElementById("generation-editor-profile-description");
  const selectedImageSummary = document.getElementById("generation-selected-image");
  const selectedImagePreview = document.getElementById("generation-selected-image-preview");
  const selectedImageName = document.getElementById("generation-selected-image-name");
  const selectedImageDetails = document.getElementById("generation-selected-image-details");
  const editSourceTitle = document.getElementById("generation-edit-source-title");
  const editSourceDescription = document.getElementById("generation-edit-source-description");
  const mainPassTitle = document.getElementById("generation-main-pass-title");
  const mainPassDescription = document.getElementById("generation-main-pass-description");
  const preserveOriginalSize = form.elements.preserve_original_size;
  const originalResolution = document.getElementById("generation-original-resolution");
  const sourceMegapixels = form.elements.source_megapixels;
  const fluxUpscaleMode = form.elements.flux_upscale_mode;
  const referenceBoost = form.elements.reference_boost;
  const groundingPixels = form.elements.grounding_pixels;
  const upscaleFactor = form.elements.upscale_factor;
  const lightbox = document.getElementById("generation-lightbox");
  const lightboxImage = document.getElementById("generation-lightbox-image");
  const lightboxName = document.getElementById("generation-lightbox-name");
  const lightboxDownload = document.getElementById("generation-lightbox-download");
  const panels = [...root.querySelectorAll("[data-step]")];
  const progress = [...root.querySelectorAll("[data-progress]")];
  let currentStep = 1;
  let requiresImage = false;
  let uploadInFlight = false;
  const previewURLs = new Map();
  const uploadedImages = new Map();
  let primaryImageSize = null;
  let progressSocket = null;
  let liveProgressReceived = false;

  const clamp = (value, min, max) => Math.min(max, Math.max(min, value));
  const roundToMultiple = (value, multiple) => Math.max(256, Math.floor(value / multiple) * multiple);

  const updateOriginalResolution = () => {
    if (!originalResolution) return;
    if (!primaryImageSize) {
      originalResolution.textContent = "Выберите основное фото: его размер будет подставлен автоматически.";
      return;
    }
    const { width: sourceWidth, height: sourceHeight } = primaryImageSize;
    const sourceMegapixelsValue = sourceWidth * sourceHeight / (1024 * 1024);
    const scale = Math.min(1, 4096 / Math.max(sourceWidth, sourceHeight));
    const targetWidth = roundToMultiple(sourceWidth * scale, 8);
    const targetHeight = roundToMultiple(sourceHeight * scale, 8);
    const capped = scale < 1 || targetWidth !== sourceWidth || targetHeight !== sourceHeight;
    originalResolution.textContent = capped
      ? `Исходник: ${sourceWidth} × ${sourceHeight} · ${sourceMegapixelsValue.toFixed(2).replace(".", ",")} Мп. Для ComfyUI будет использовано ${targetWidth} × ${targetHeight}.`
      : `Исходник: ${sourceWidth} × ${sourceHeight} · ${sourceMegapixelsValue.toFixed(2).replace(".", ",")} Мп. Размер будет сохранён.`;
  };

  const applyOriginalResolution = () => {
    if (!preserveOriginalSize?.checked || !primaryImageSize) return;
    const scale = Math.min(1, 4096 / Math.max(primaryImageSize.width, primaryImageSize.height));
    const targetWidth = roundToMultiple(primaryImageSize.width * scale, 8);
    const targetHeight = roundToMultiple(primaryImageSize.height * scale, 8);
    width.value = String(targetWidth);
    height.value = String(targetHeight);
    aspect.value = "custom";
    outputMegapixels.value = (targetWidth * targetHeight / (1024 * 1024)).toFixed(2);
    if (sourceMegapixels) sourceMegapixels.value = clamp(targetWidth * targetHeight / (1024 * 1024), 0.25, 16).toFixed(2);
    if (maxSide) maxSide.value = "4096";
    updateOriginalResolution();
    updateResolutionPreview();
  };

  const syncSelectedImageSummary = () => {
    const primary = imageSlots[0];
    const file = primary?.input?.files?.[0];
    const previewURL = previewURLs.get(1);
    const visible = Boolean(requiresImage && file && previewURL);
    if (selectedImageSummary) selectedImageSummary.hidden = !visible;
    if (!visible) return;
    if (selectedImagePreview) selectedImagePreview.src = previewURL;
    if (selectedImageName) selectedImageName.textContent = file.name;
    if (selectedImageDetails) {
      selectedImageDetails.textContent = primaryImageSize
        ? `${primaryImageSize.width} × ${primaryImageSize.height} · ${(primaryImageSize.width * primaryImageSize.height / (1024 * 1024)).toFixed(2).replace(".", ",")} Мп`
        : "Определяем разрешение";
    }
  };

  const updateAutoDenoise = () => {
    if (!autoDenoise?.checked || !upscaleDenoise) return;
    const pixels = Math.max(1, Number(width.value) * Number(height.value));
    const calculated = Math.floor((0.14 * (Math.sqrt(pixels) / 1024) - 0.01) * 100) / 100;
    upscaleDenoise.value = clamp(calculated, 0.01, 0.5).toFixed(2);
  };

  const updateResolutionPreview = () => {
    if (!resolutionPreview) return;
    const actualMP = Number(width.value) * Number(height.value) / (1024 * 1024);
    resolutionPreview.textContent = `${width.value} × ${height.value} · ${actualMP.toFixed(2).replace(".", ",")} Мп`;
    updateAutoDenoise();
  };

  const calculateResolution = () => {
    if (!aspect || aspect.value === "custom") {
      updateResolutionPreview();
      return;
    }
    const [ratioWidth, ratioHeight] = aspect.value.split(":").map(Number);
    const megapixels = clamp(Number(outputMegapixels.value) || 1.9, 0.1, 16);
    const multiple = Number(dimensionMultiple.value) || 16;
    const targetPixels = megapixels * 1024 * 1024;
    let nextWidth = roundToMultiple(Math.sqrt(targetPixels * ratioWidth / ratioHeight), multiple);
    let nextHeight = roundToMultiple(Math.sqrt(targetPixels * ratioHeight / ratioWidth), multiple);
    const limit = Number(maxSide.value) || 0;
    if (limit && Math.max(nextWidth, nextHeight) > limit) {
      const scale = limit / Math.max(nextWidth, nextHeight);
      nextWidth = roundToMultiple(nextWidth * scale, multiple);
      nextHeight = roundToMultiple(nextHeight * scale, multiple);
    }
    width.value = String(nextWidth);
    height.value = String(nextHeight);
    updateResolutionPreview();
  };

  const setGenerationProgress = (stage, detail = "", percent = null) => {
    if (!runProgress || !generationStage || !generationStageDetail || !generationProgressbarFill) return;
    runProgress.hidden = false;
    generationStage.textContent = stage;
    generationStageDetail.textContent = detail;
    if (percent === null) {
      generationProgressbar.removeAttribute("aria-valuenow");
      generationProgressbarFill.classList.add("is-indeterminate");
      generationProgressbarFill.style.width = "34%";
      return;
    }
    const bounded = clamp(Math.round(percent), 0, 100);
    generationProgressbar.setAttribute("aria-valuenow", String(bounded));
    generationProgressbarFill.classList.remove("is-indeterminate");
    generationProgressbarFill.style.width = `${bounded}%`;
  };

  const queuePositionDetail = (position, total) => {
    const safePosition = Number(position) || 0;
    const safeTotal = Number(total) || 0;
    if (!safePosition || !safeTotal) return "Ожидаем свободный слот";
    const ahead = Math.max(0, safePosition - 1);
    return ahead > 0
      ? `Ваше место: ${safePosition} из ${safeTotal}. Перед вами: ${ahead}.`
      : `Ваше место: 1 из ${safeTotal}. Генерация начнётся следующей.`;
  };

  const renderQueueOverview = (queue) => {
    if (!generationQueue) return;
    const running = Math.max(0, Number(queue?.running) || 0);
    const pending = Math.max(0, Number(queue?.pending) || 0);
    generationQueue.hidden = running + pending === 0;
    if (generationQueue.hidden) return;
    if (generationQueueTitle) generationQueueTitle.textContent = running > 0 ? "Сервер занят" : "Ожидают запуска";
    if (generationQueueDetails) {
      const parts = [];
      if (running > 0) parts.push(`Выполняется: ${running}`);
      if (pending > 0) parts.push(`В очереди: ${pending}`);
      generationQueueDetails.textContent = parts.join(" · ");
    }
  };

  const refreshQueueOverview = async () => {
    try {
      const response = await fetch("/generate/queue", { credentials: "same-origin" });
      if (!response.ok) return;
      renderQueueOverview(await response.json());
    } catch (_) {
      // The queue indicator is informational; generation status has its own error handling.
    }
  };

  const closeProgressSocket = () => {
    if (!progressSocket) return;
    progressSocket.onclose = null;
    progressSocket.close();
    progressSocket = null;
  };

  const workflowStage = (node) => {
    const stages = {
      "9": "Основная генерация",
      "10": "Декодирование основы",
      "11": "Подготовка к апскейлу",
      "12": "Кодирование для апскейла",
      "13": "Апскейл",
      "14": "Финальная детализация",
      "15": "Декодирование результата",
      "16": "Сохранение результата",
      "20": "Перенос цвета",
      gateway_flux_ultimate: "Ultimate SD Upscale",
      gateway_flux_seedvr2: "SeedVR2 Upscale",
    };
    return stages[String(node)] || "ComfyUI выполняет workflow";
  };

  const connectProgressSocket = (promptID) => {
    closeProgressSocket();
    liveProgressReceived = false;
    const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
    try {
      progressSocket = new WebSocket(`${protocol}//${window.location.host}/ws?gateway_service=comfyui`);
    } catch (_) {
      return;
    }
    progressSocket.onmessage = (event) => {
      let message;
      try { message = JSON.parse(event.data); } catch (_) { return; }
      const data = message?.data || {};
      if (data.prompt_id && data.prompt_id !== promptID) return;
      switch (message.type) {
        case "execution_start":
          liveProgressReceived = true;
          setGenerationProgress("ComfyUI начал генерацию", "Подготавливаем workflow", null);
          break;
        case "executing":
          liveProgressReceived = true;
          if (data.node === null) {
            setGenerationProgress("Завершаем", "ComfyUI передаёт результат", 100);
          } else {
            setGenerationProgress(workflowStage(data.node), "Выполняется текущий этап", null);
          }
          break;
        case "progress": {
          liveProgressReceived = true;
          const max = Number(data.max) || 0;
          const value = Number(data.value) || 0;
          const percent = max > 0 ? value / max * 100 : null;
          const detail = max > 0 ? `${value} из ${max} шагов` : "Выполняется";
          setGenerationProgress(workflowStage(data.node), detail, percent);
          break;
        }
      }
    };
    progressSocket.onclose = () => { progressSocket = null; };
  };

  const closeLightbox = () => {
    if (!lightbox || lightbox.hidden) return;
    lightbox.hidden = true;
    lightboxImage.removeAttribute("src");
    document.body.classList.remove("generation-lightbox-open");
  };

  const openLightbox = (output) => {
    if (!lightbox || output.media_type === "video") return;
    lightboxImage.src = output.url;
    lightboxName.textContent = output.filename;
    lightboxDownload.href = output.url;
    lightboxDownload.download = output.filename;
    lightbox.hidden = false;
    document.body.classList.add("generation-lightbox-open");
    lightbox.querySelector(".generation-lightbox-close")?.focus();
  };

  lightbox?.querySelectorAll("[data-lightbox-close]").forEach((button) => button.addEventListener("click", closeLightbox));
  document.addEventListener("keydown", (event) => { if (event.key === "Escape") closeLightbox(); });

  const showStep = (step) => {
    currentStep = step;
    panels.forEach((panel) => panel.classList.toggle("is-visible", Number(panel.dataset.step) === step));
    progress.forEach((item) => {
      const number = Number(item.dataset.progress);
      item.classList.toggle("is-active", number === step);
      item.classList.toggle("is-complete", number < step);
    });
    window.scrollTo({ top: 0, behavior: "smooth" });
  };

  const selectedChoice = () => root.querySelector(".scenario-choice.is-selected");
  const selectedGenerationWorkflow = () => root.querySelector(".generation-workflow-choice.is-selected");
  const maxInputImages = () => Math.max(1, Number(selectedGenerationWorkflow()?.dataset.maxInputImages || 1));

  const clearImageSlot = (item) => {
    if (!item) return;
    const oldURL = previewURLs.get(item.index);
    if (oldURL) URL.revokeObjectURL(oldURL);
    previewURLs.delete(item.index);
    if (item.input) item.input.value = "";
    if (item.index === 1) {
      primaryImageSize = null;
      updateOriginalResolution();
      syncSelectedImageSummary();
    }
    uploadedImages.delete(item.index);
    if (inputImages[item.index - 1]) inputImages[item.index - 1].value = "";
    if (item.previewImage) item.previewImage.removeAttribute("src");
    if (item.preview) item.preview.hidden = true;
    if (item.name) item.name.textContent = "";
    if (item.state) item.state.textContent = "Готово к загрузке";
  };

  const syncImageSlots = () => {
    const maximum = requiresImage ? maxInputImages() : 0;
    const isKrea = selectedGenerationWorkflow()?.dataset.family === "krea2";
    if (imageSourceGrid) imageSourceGrid.dataset.visibleSlots = String(maximum);
    imageSlots.forEach((item) => {
      const visible = item.index <= maximum;
      item.slot.hidden = !visible;
      if (!visible) clearImageSlot(item);
      if (!item.label) return;
      if (item.index === 1) item.label.textContent = "Фото 1 · обязательно";
      else item.label.textContent = isKrea && item.index === 2 ? "Фото 2 · дополнительное" : `Фото ${item.index} · референс`;
    });
    const note = document.getElementById("image-source-note");
    if (note) note.textContent = maximum > 1
      ? `Первое фото обязательно. Можно добавить ещё до ${maximum - 1} ${maximum === 2 ? "референса" : "референсов"}.`
      : "Загрузите исходное фото для редактирования.";
  };

  const chooseScenario = (button) => {
    root.querySelectorAll(".scenario-choice").forEach((item) => item.classList.remove("is-selected"));
    button.classList.add("is-selected");
    templateID.value = button.dataset.workflowId;
    requiresImage = button.dataset.requiresImage === "true";
    generationWorkflowID.value = "";
    root.querySelectorAll(".generation-workflow-choice").forEach((item) => item.classList.remove("is-selected"));
    updateWorkflowCompatibility();
    showStep(2);
  };

  const updateWorkflowNext = () => {
    if (!workflowNext) return;
    const selected = selectedGenerationWorkflow();
    const hasWorkflow = Boolean(selected && selected.dataset.available === "true");
    const primary = imageSlots[0];
    const hasImage = Boolean(primary?.input?.files?.[0] || uploadedImages.get(1));
    workflowNext.disabled = !hasWorkflow || (requiresImage && !hasImage);
    workflowNext.textContent = requiresImage ? "Загрузить фото и продолжить" : "Продолжить";
  };

  const updateWorkflowCompatibility = () => {
    let visible = 0;
    root.querySelectorAll(".generation-workflow-choice").forEach((item) => {
      const matches = item.dataset.templateId === templateID.value;
      item.hidden = !matches;
      if (matches) visible += 1;
    });
    if (imageSourceFields) imageSourceFields.hidden = !requiresImage;
    if (workflowNote) workflowNote.hidden = visible > 0;
    syncImageSlots();
    updateWorkflowNext();
  };

  const chooseGenerationWorkflow = (button) => {
    if (button.disabled || button.hidden) return;
    root.querySelectorAll(".generation-workflow-choice").forEach((item) => item.classList.remove("is-selected"));
    button.classList.add("is-selected");
    generationWorkflowID.value = button.dataset.presetId;
    updateQuickModelOptions(button);
    applyQuality();
    syncImageSlots();
    updateWorkflowNext();
  };

  const updateQuickModelOptions = (workflow) => {
    if (!model || !workflow) return;
    [...model.options].forEach((option) => {
      if (!option.value) return;
      const allowed = option.dataset.family === workflow.dataset.family;
      option.hidden = !allowed;
      option.disabled = option.dataset.available !== "true" || !allowed;
    });
    const defaultID = workflow.dataset.defaultModelId || "";
    const defaultOption = [...model.options].find((option) => option.value === defaultID && !option.disabled);
    model.value = defaultOption ? defaultID : "";
  };

  const syncAdaptiveLoraSlots = (kind) => {
    const rows = [...root.querySelectorAll(`[data-lora-slots="${kind}"]`)];
    rows.forEach((row, index) => {
      const previous = rows[index - 1];
      const previousSelect = previous?.querySelector(".generation-lora-select");
      const visible = index === 0 || Boolean(previousSelect?.value);
      row.hidden = !visible;
      row.querySelectorAll("input, select").forEach((control) => { control.disabled = !visible; });
      if (visible) return;
      const select = row.querySelector(".generation-lora-select");
      const strength = row.querySelector(".generation-lora-model");
      if (select) select.value = "";
      if (strength) strength.value = "0";
    });
  };

  const syncWorkflowFields = () => {
    const preset = selectedGenerationWorkflow();
    const family = preset?.dataset.family || model?.selectedOptions?.[0]?.dataset.family || "";
    const isEdit = preset?.dataset.templateId === "image-to-image";
    const isKrea = family === "krea2";
    const isKreaText = isKrea && !isEdit;
    const isKreaEdit = isKrea && isEdit;
    const isFluxEdit = family === "flux2" && isEdit;
    if (isFluxEdit && maxSide?.value === "0") maxSide.value = "2160";
    const setFieldState = (selector, visible) => {
      root.querySelectorAll(selector).forEach((field) => {
        field.hidden = !visible;
        field.querySelectorAll("input, select, textarea, button").forEach((control) => { control.disabled = !visible; });
      });
    };
    setFieldState(".image-edit-field", isEdit);
    setFieldState(".krea-edit-field", isKreaEdit);
    setFieldState(".flux-edit-field", isFluxEdit);
    root.querySelectorAll(".size-workflow-field").forEach((field) => {
      field.hidden = Boolean(isEdit && preserveOriginalSize?.checked);
    });
    setFieldState(".krea-workflow-field", isKrea);
    setFieldState(".krea-text-workflow-field", isKreaText);
    setFieldState(".flux-edit-settings", isFluxEdit);
    setFieldState(".krea-edit-settings", isKreaEdit);
    setFieldState(".standard-main-settings", !isEdit);
    if (isFluxEdit) syncAdaptiveLoraSlots("flux");
    if (isKreaText) syncAdaptiveLoraSlots("krea");
    if (qualityField) qualityField.hidden = isEdit;
    if (editorProfile) editorProfile.hidden = !isEdit;
    syncSelectedImageSummary();
    if (isFluxEdit) {
      if (editorProfileTitle) editorProfileTitle.textContent = "Flux2: фото и промт";
      if (editorProfileDescription) editorProfileDescription.textContent = "До четырёх изображений: основной кадр и до трёх референсов. Flux2 работает с латентами исходных изображений.";
      if (editSourceTitle) editSourceTitle.textContent = "Исходник и референсы Flux2";
      if (editSourceDescription) editSourceDescription.textContent = "Выберите детализацию входных фото. Размер результата меняется только при включённой настройке кадра.";
      if (mainPassTitle) mainPassTitle.textContent = "Параметры Flux2";
      if (mainPassDescription) mainPassDescription.textContent = "Шаги, guidance, denoise и планировщик оригинального Flux2 workflow.";
    } else if (isKreaEdit) {
      if (editorProfileTitle) editorProfileTitle.textContent = "Krea 2: фото и промт";
      if (editorProfileDescription) editorProfileDescription.textContent = "Основное фото и один дополнительный референс. Krea2 сохраняет идентичность через Identity Edit.";
      if (editSourceTitle) editSourceTitle.textContent = "Привязка Krea2 к исходнику";
      if (editSourceDescription) editSourceDescription.textContent = "Сила сохранения исходника и анализ фото управляют тем, насколько строго Krea2 держится за оригинал.";
      if (mainPassTitle) mainPassTitle.textContent = "Параметры Krea2 Identity Edit";
      if (mainPassDescription) mainPassDescription.textContent = "Параметры редактирования до отдельного качественного апскейла Krea2.";
    } else {
      if (mainPassTitle) mainPassTitle.textContent = "Основной проход";
      if (mainPassDescription) mainPassDescription.textContent = "Параметры основного семплирования выбранного workflow.";
    }
  };

  const applyQuality = () => {
    const option = model?.selectedOptions[0];
    if (!option?.value) return;
    const baseSteps = Number(option.dataset.defaultSteps || 25);
    const mode = quality?.value || "balanced";
    const isKrea = option.dataset.family === "krea2";
    const isEdit = selectedGenerationWorkflow()?.dataset.templateId === "image-to-image";
    const isFluxEdit = option.dataset.family === "flux2" && isEdit;
    if (steps) steps.value = isKrea ? (mode === "fast" ? 4 : baseSteps) : (mode === "fast" ? Math.max(4, Math.round(baseSteps * 0.65)) : mode === "maximum" ? Math.min(100, Math.round(baseSteps * 1.5)) : baseSteps);
    if (cfg && option.dataset.defaultCfg) cfg.value = isFluxEdit ? "1" : option.dataset.defaultCfg;
    if (sampler && option.dataset.defaultSampler) sampler.value = option.dataset.defaultSampler;
    if (scheduler && option.dataset.defaultScheduler) scheduler.value = option.dataset.defaultScheduler;
    if (isKrea && !isEdit) {
      baseMegapixels.value = mode === "fast" ? "0.75" : mode === "maximum" ? "1.5" : "1";
      outputMegapixels.value = mode === "fast" ? "1" : mode === "maximum" ? "4" : "1.9";
      upscaleSteps.value = mode === "fast" ? "3" : mode === "maximum" ? "8" : "5";
      detailSteps.value = mode === "fast" ? "1" : mode === "maximum" ? "3" : "2";
      detailDenoise.value = mode === "fast" ? "0.02" : mode === "maximum" ? "0.035" : "0.03";
      if (isEdit) {
        if (steps) steps.value = mode === "fast" ? "6" : mode === "maximum" ? "12" : "8";
        if (upscaleSteps) upscaleSteps.value = mode === "fast" ? "3" : mode === "maximum" ? "6" : "4";
        if (upscaleDenoise) upscaleDenoise.value = mode === "fast" ? "0.12" : mode === "maximum" ? "0.18" : "0.15";
        if (referenceBoost) referenceBoost.value = "4";
        if (groundingPixels) groundingPixels.value = mode === "maximum" ? "1024" : "768";
        if (upscaleFactor) upscaleFactor.value = mode === "fast" ? "1.2" : "1.5";
        if (maxSide) maxSide.value = mode === "maximum" ? "3072" : "2048";
      }
    }
    if (isFluxEdit) {
      if (steps) steps.value = mode === "fast" ? "16" : mode === "maximum" ? "32" : "25";
      if (cfg) cfg.value = "1";
      if (denoise) denoise.value = mode === "fast" ? "0.80" : "0.90";
      if (scheduler) scheduler.value = "normal";
      if (maxSide) maxSide.value = mode === "maximum" ? "3072" : "2160";
      if (sourceMegapixels) sourceMegapixels.value = mode === "maximum" ? "2" : "1";
    }
    syncWorkflowFields();
    calculateResolution();
  };

  model?.addEventListener("change", applyQuality);
  quality?.addEventListener("change", applyQuality);
  [aspect, outputMegapixels, dimensionMultiple, maxSide].forEach((input) => input?.addEventListener("change", calculateResolution));
  outputMegapixels?.addEventListener("input", calculateResolution);
  autoDenoise?.addEventListener("change", updateAutoDenoise);
  preserveOriginalSize?.addEventListener("change", () => {
    if (preserveOriginalSize.checked) {
      if (fluxUpscaleMode?.value !== "none") fluxUpscaleMode.value = "none";
      applyOriginalResolution();
    }
    syncWorkflowFields();
  });
  fluxUpscaleMode?.addEventListener("change", () => {
    if (fluxUpscaleMode.value !== "none" && preserveOriginalSize?.checked) {
      preserveOriginalSize.checked = false;
      updateOriginalResolution();
    }
    syncWorkflowFields();
  });
  [width, height].forEach((input) => input?.addEventListener("change", () => {
    const value = clamp(Number(input.value) || 1024, 256, 4096);
    input.value = String(Math.round(value / 8) * 8);
    aspect.value = "custom";
    outputMegapixels.value = (Number(width.value) * Number(height.value) / (1024 * 1024)).toFixed(2);
    updateResolutionPreview();
  }));
  root.querySelectorAll(".generation-lora-select").forEach((select) => {
    select.addEventListener("change", () => {
      const strength = select.closest(".lora-row")?.querySelector(".generation-lora-model");
      if (strength) strength.value = select.value ? (select.selectedOptions[0]?.dataset.defaultStrength || "1") : "0";
      const kind = select.closest("[data-lora-slots]")?.dataset.loraSlots;
      if (kind) syncAdaptiveLoraSlots(kind);
    });
  });

  root.querySelectorAll(".scenario-choice").forEach((button) => {
    button.addEventListener("click", () => chooseScenario(button));
  });
  root.querySelectorAll(".generation-workflow-choice").forEach((button) => {
    button.addEventListener("click", () => chooseGenerationWorkflow(button));
  });

  root.querySelectorAll(".generation-back").forEach((button) => {
    button.addEventListener("click", () => showStep(Math.max(1, currentStep - 1)));
  });

  imageSlots.forEach((item) => {
    item.input?.addEventListener("change", () => {
      const file = item.input.files?.[0];
      const previousURL = previewURLs.get(item.index);
      if (previousURL) URL.revokeObjectURL(previousURL);
      previewURLs.delete(item.index);
      uploadedImages.delete(item.index);
      if (inputImages[item.index - 1]) inputImages[item.index - 1].value = "";
      if (!file) {
        if (item.preview) item.preview.hidden = true;
        updateWorkflowNext();
        return;
      }
      const url = URL.createObjectURL(file);
      previewURLs.set(item.index, url);
      if (item.previewImage) {
        item.previewImage.onload = () => {
          if (item.index !== 1) return;
          primaryImageSize = { width: item.previewImage.naturalWidth, height: item.previewImage.naturalHeight };
          applyOriginalResolution();
          updateOriginalResolution();
          syncSelectedImageSummary();
        };
        item.previewImage.src = url;
        item.previewImage.onerror = () => {
          if (item.state) item.state.textContent = "Не удалось показать предпросмотр, но файл будет загружен";
        };
      }
      if (item.name) item.name.textContent = file.name;
      if (item.state) item.state.textContent = "Готово к загрузке";
      if (item.preview) item.preview.hidden = false;
      syncSelectedImageSummary();
      updateWorkflowNext();
    });
    item.remove?.addEventListener("click", () => {
      clearImageSlot(item);
      updateWorkflowNext();
    });
  });

  workflowNext?.addEventListener("click", async () => {
    if (!generationWorkflowID.value) return;
    if (!requiresImage) {
      showStep(3);
      positive?.focus({ preventScroll: true });
      return;
    }
    const selectedSlots = imageSlots.filter((item) => item.index <= maxInputImages() && item.input?.files?.[0]);
    if (!selectedSlots.some((item) => item.index === 1) || uploadInFlight) return;
    uploadInFlight = true;
    workflowNext.disabled = true;
    workflowNext.classList.add("is-loading");
    try {
      inputImages.forEach((input) => { if (input) input.value = ""; });
      uploadedImages.clear();
      for (const item of selectedSlots) {
        const file = item.input.files[0];
        if (item.state) item.state.textContent = "Загружаем в вашу сессию...";
        const body = new FormData();
        body.append("image", file, file.name);
        body.append("type", "input");
        body.append("overwrite", "true");
        const response = await fetch("/generate/upload/image", { method: "POST", body, credentials: "same-origin" });
        const payload = await response.json().catch(() => ({}));
        if (!response.ok || !payload.name) throw new Error(payload.error || "ComfyUI не принял фото");
        const value = [payload.subfolder, payload.name].filter(Boolean).join("/");
        uploadedImages.set(item.index, value);
        if (inputImages[item.index - 1]) inputImages[item.index - 1].value = value;
        if (item.state) item.state.textContent = "Загружено в вашу сессию";
      }
      showStep(3);
      positive?.focus({ preventScroll: true });
    } catch (error) {
      const failed = selectedSlots.find((item) => !uploadedImages.get(item.index));
      if (failed?.state) failed.state.textContent = error.message || "Не удалось загрузить фото";
      updateWorkflowNext();
    } finally {
      uploadInFlight = false;
      workflowNext.classList.remove("is-loading");
    }
  });

  const renderOutputs = (outputs) => {
    outputGrid.replaceChildren();
    outputs.forEach((output) => {
      const media = output.media_type === "video" ? document.createElement("video") : document.createElement("img");
      media.src = output.url;
      media.controls = output.media_type === "video";
      media.loading = "lazy";
      media.alt = output.filename;
      const card = document.createElement("figure");
      card.className = "generation-output";
      const previewLink = document.createElement(output.media_type === "video" ? "div" : "button");
      previewLink.className = "generation-output-preview";
      if (output.media_type !== "video") {
        previewLink.type = "button";
        previewLink.title = "Открыть на весь экран";
        previewLink.addEventListener("click", () => openLightbox(output));
      }
      const caption = document.createElement("figcaption");
      const filename = document.createElement("span");
      filename.textContent = output.filename;
      const download = document.createElement("a");
      download.className = "generation-output-download";
      download.href = output.url;
      download.download = output.filename;
      download.textContent = "Скачать файл";
      previewLink.append(media);
      caption.append(filename, download);
      card.append(previewLink, caption);
      outputGrid.append(card);
    });
  };

  const formatExpiry = (milliseconds) => {
    if (milliseconds <= 0) return "Удаляется при следующей очистке";
    const totalMinutes = Math.ceil(milliseconds / 60000);
    const hours = Math.floor(totalMinutes / 60);
    const minutes = totalMinutes % 60;
    if (hours >= 24) return `Удалится через ${Math.floor(hours / 24)} дн. ${hours % 24} ч.`;
    if (hours > 0) return `Удалится через ${hours} ч. ${minutes} мин.`;
    return `Удалится через ${Math.max(1, minutes)} мин.`;
  };

  const refreshExpiryLabels = () => {
    document.querySelectorAll("[data-generation-expiry]").forEach((element) => {
      const expiresAt = Number(element.dataset.generationExpiry);
      element.textContent = Number.isFinite(expiresAt) ? formatExpiry(expiresAt - Date.now()) : "Срок хранения неизвестен";
    });
  };

  const createLibraryCard = (item) => {
    const card = document.createElement("figure");
    card.className = "generation-output generation-library-item";
    const isVideo = item.media_type === "video";
    const preview = document.createElement(isVideo ? "video" : "button");
    preview.className = "generation-output-preview";
    if (isVideo) {
      preview.controls = true;
      preview.preload = "metadata";
      preview.src = item.url;
    } else {
      preview.type = "button";
      preview.dataset.generationLibraryItem = "";
      preview.dataset.url = item.url;
      preview.dataset.filename = item.filename;
      preview.title = "Открыть на весь экран";
      const image = document.createElement("img");
      image.loading = "lazy";
      image.src = item.url;
      image.alt = "Результат генерации";
      preview.append(image);
      preview.addEventListener("click", () => openLightbox({ filename: item.filename, media_type: "image", url: item.url }));
    }
    const caption = document.createElement("figcaption");
    const filename = document.createElement("span");
    filename.textContent = item.filename;
    const actions = document.createElement("div");
    actions.className = "generation-card-actions";
    const expiry = document.createElement("time");
    expiry.className = "generation-expiry";
    expiry.dataset.generationExpiry = String(item.expires_unix);
    const hideForm = document.createElement("form");
    hideForm.method = "post";
    hideForm.action = "/generate/library/hide";
		hideForm.dataset.generationHideForm = "";
    const csrf = document.createElement("input");
    csrf.type = "hidden";
    csrf.name = "csrf";
    csrf.value = form.querySelector("input[name='csrf']")?.value || "";
    const mediaID = document.createElement("input");
    mediaID.type = "hidden";
    mediaID.name = "media_id";
    mediaID.value = String(item.id);
    const hide = document.createElement("button");
    hide.className = "generation-hide";
    hide.type = "submit";
    hide.textContent = "Убрать из моей галереи";
    hideForm.append(csrf, mediaID, hide);
    actions.append(expiry, hideForm);
    caption.append(filename, actions);
    card.append(preview, caption);
    return card;
  };

  const renderLibrary = (items) => {
    let library = document.getElementById("my-results");
    if (!items.length) {
      library?.remove();
      return;
    }
    if (!library) {
      library = document.createElement("section");
      library.id = "my-results";
      library.className = "generation-library panel";
      const heading = document.createElement("div");
      heading.className = "panel-heading";
      heading.innerHTML = "<div><p class=\"section-kicker\">Моя галерея</p><h2>Последние результаты</h2><p class=\"panel-intro\">Результаты доступны в вашем профиле 24 часа, затем удаляются без возможности восстановления.</p></div>";
      const grid = document.createElement("div");
      grid.className = "generation-output-grid";
      library.append(heading, grid);
      result.insertAdjacentElement("afterend", library);
    }
    const grid = library.querySelector(".generation-output-grid");
    grid.replaceChildren(...items.map(createLibraryCard));
    refreshExpiryLabels();
  };

  const refreshLibrary = async () => {
    const response = await fetch("/generate/library/recent", { credentials: "same-origin" });
    const payload = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(payload.error || "Не удалось обновить галерею");
    renderLibrary(payload.media || []);
  };

  const removeLibraryCard = async (hideForm) => {
    const card = hideForm.closest(".generation-library-item");
    const button = hideForm.querySelector(".generation-hide");
    if (!card || !button || button.disabled) return;
    const originalText = button.textContent;
    button.disabled = true;
    button.textContent = "Убираем...";
    try {
      const response = await fetch(hideForm.action, {
        method: "POST",
        headers: { "Accept": "application/json", "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8" },
        body: new URLSearchParams(new FormData(hideForm)),
        credentials: "same-origin",
      });
      const payload = await response.json().catch(() => ({}));
      if (!response.ok || !payload.removed) throw new Error(payload.error || "Не удалось убрать результат");
      card.classList.add("is-removing");
      window.setTimeout(() => {
        const library = card.closest(".generation-library");
        card.remove();
        if (!library?.querySelector(".generation-library-item")) library.remove();
      }, 180);
    } catch (error) {
      button.disabled = false;
      button.textContent = error.message || originalText;
      window.setTimeout(() => { if (!button.disabled) button.textContent = originalText; }, 2400);
    }
  };

  root.addEventListener("submit", (event) => {
    const hideForm = event.target.closest("[data-generation-hide-form]");
    if (!hideForm) return;
    event.preventDefault();
    removeLibraryCard(hideForm);
  });

  root.querySelectorAll("[data-generation-library-item]").forEach((button) => {
    button.addEventListener("click", () => openLightbox({
      filename: button.dataset.filename || "Результат генерации",
      media_type: "image",
      url: button.dataset.url,
    }));
  });
  refreshExpiryLabels();
  window.setInterval(refreshExpiryLabels, 30000);

  const poll = async (promptID) => {
    const deadline = Date.now() + 10 * 60 * 1000;
    while (Date.now() < deadline) {
      const response = await fetch(`/generate/status?prompt_id=${encodeURIComponent(promptID)}`, { credentials: "same-origin" });
      const payload = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(payload.error || "Не удалось получить статус");
      resultStatus.textContent = payload.message || "Проверяем состояние...";
      if (!liveProgressReceived) {
        if (payload.state === "queued") {
          const detail = queuePositionDetail(payload.queue_position, payload.queue_total);
          setGenerationProgress("В очереди ComfyUI", detail, null);
        } else if (payload.state === "running") {
          setGenerationProgress("ComfyUI выполняет workflow", "Стадия будет показана сразу после подключения", null);
        }
      }
      if (payload.state === "completed") {
        resultTitle.textContent = "Готово";
        setGenerationProgress("Готово", "Результат подготовлен", 100);
        renderOutputs(payload.outputs || []);
		await refreshLibrary();
        result.scrollIntoView({ block: "start", behavior: "smooth" });
        return;
      }
      if (payload.state === "error") throw new Error(payload.message || "ComfyUI завершил генерацию с ошибкой");
      await new Promise((resolve) => window.setTimeout(resolve, 2000));
    }
    throw new Error("Генерация выполняется слишком долго. Проверьте результат позже в ComfyUI.");
  };

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (!selectedChoice() || !selectedGenerationWorkflow() || !model?.value || !positive.value.trim()) return;
    const submit = document.getElementById("generation-submit");
    submit.disabled = true;
    submit.classList.add("is-loading");
    result.hidden = false;
    resultTitle.textContent = "Генерация выполняется";
    resultStatus.textContent = "Ставим workflow в очередь ComfyUI...";
    outputGrid.replaceChildren();
    result.classList.remove("has-error");
    runProgress.hidden = false;
    setGenerationProgress("Подготовка", "Проверяем параметры workflow", null);
    result.scrollIntoView({ block: "start", behavior: "smooth" });
    try {
      const body = new FormData(form);
      body.set("template_id", selectedChoice()?.dataset.workflowId || "");
      body.set("generation_workflow", selectedGenerationWorkflow()?.dataset.presetId || "");
      ["input_image", "input_image_2", "input_image_3", "input_image_4"].forEach((name, index) => {
        body.set(name, uploadedImages.get(index + 1) || "");
      });
      const response = await fetch("/generate/run", { method: "POST", headers: { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8" }, body: new URLSearchParams(body), credentials: "same-origin" });
      const payload = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(payload.error || "Не удалось запустить генерацию");
      if (payload.state === "queued") {
        setGenerationProgress("В очереди ComfyUI", queuePositionDetail(payload.queue_position, payload.queue_total), null);
      } else if (payload.state === "running") {
        setGenerationProgress("ComfyUI начал генерацию", "Подготавливаем workflow", null);
      }
      refreshQueueOverview();
      connectProgressSocket(payload.prompt_id);
      await poll(payload.prompt_id);
    } catch (error) {
      resultTitle.textContent = "Не удалось выполнить генерацию";
      resultStatus.textContent = error.message || "Неизвестная ошибка";
      result.classList.add("has-error");
    } finally {
      closeProgressSocket();
      submit.disabled = false;
      submit.classList.remove("is-loading");
    }
  });

  const initialID = root.dataset.selectedWorkflow || "";
  const initial = initialID ? root.querySelector(`[data-workflow-id="${CSS.escape(initialID)}"]`) : null;
  if (initial) chooseScenario(initial);
  if (root.dataset.previewOutput) {
    result.hidden = false;
    resultTitle.textContent = "Готово";
    resultStatus.textContent = "Пример отображения результата";
    setGenerationProgress("Готово", "Результат подготовлен", 100);
    renderOutputs([{ filename: "AI-Gateway-Krea2_00002_.png", media_type: "image", url: root.dataset.previewOutput }]);
  }
  calculateResolution();
  refreshQueueOverview();
  window.setInterval(refreshQueueOverview, 5000);
})();
