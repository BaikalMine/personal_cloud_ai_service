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
    role: slot.querySelector("[data-image-role]"),
  }));
  const workflowNext = document.getElementById("workflow-next");
  const imageSourceFields = document.getElementById("image-source-fields");
  const imageSourceGrid = root.querySelector(".source-image-grid");
  const miniMaxVideoMode = document.getElementById("minimax-video-mode");
  const miniMaxVideoModeSelect = document.getElementById("minimax-video-mode-select");
  const miniMaxVideoModeHint = document.getElementById("minimax-video-mode-hint");
  const workflowNote = document.getElementById("generation-workflow-note");
  const positive = document.getElementById("positive-prompt");
  const promptAssistant = document.getElementById("prompt-assistant");
  const promptAssistantEnabled = document.getElementById("prompt-assistant-enabled");
  const promptAssistantControls = document.getElementById("prompt-assistant-controls");
  const promptAssistantTemplate = document.getElementById("prompt-assistant-template");
  const promptAssistantThink = document.getElementById("prompt-assistant-think");
  const promptAssistantImprove = document.getElementById("prompt-assistant-improve");
  const promptAssistantState = document.getElementById("prompt-assistant-state");
  const promptAssistantReview = document.getElementById("prompt-assistant-review");
  const promptAssistantDraft = document.getElementById("prompt-assistant-draft");
  const promptAssistantApply = document.getElementById("prompt-assistant-apply");
  const promptAssistantKeep = document.getElementById("prompt-assistant-keep");
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
  const resultActions = document.getElementById("generation-result-actions");
  const retryGeneration = document.getElementById("generation-retry");
  const cancelGeneration = document.getElementById("generation-cancel");
  const editorProfile = document.getElementById("generation-editor-profile");
  const editorProfileTitle = document.getElementById("generation-editor-profile-title");
  const editorProfileDescription = document.getElementById("generation-editor-profile-description");
  const selectedImageSummary = document.getElementById("generation-selected-image");
  const selectedImagePreview = document.getElementById("generation-selected-image-preview");
  const selectedImageName = document.getElementById("generation-selected-image-name");
  const selectedImageDetails = document.getElementById("generation-selected-image-details");
  const referenceMap = document.getElementById("generation-reference-map");
  const referenceMapList = document.getElementById("generation-reference-map-list");
  const editSourceTitle = document.getElementById("generation-edit-source-title");
  const editSourceDescription = document.getElementById("generation-edit-source-description");
  const mainPassTitle = document.getElementById("generation-main-pass-title");
  const mainPassDescription = document.getElementById("generation-main-pass-description");
  const preserveOriginalSize = form.elements.preserve_original_size;
  const preserveOriginalLabel = document.getElementById("generation-preserve-original-label");
  const originalResolution = document.getElementById("generation-original-resolution");
  const sourceMegapixels = form.elements.source_megapixels;
  const fluxUpscaleMode = form.elements.flux_upscale_mode;
  const referenceBoost = form.elements.reference_boost;
  const groundingPixels = form.elements.grounding_pixels;
  const upscaleFactor = form.elements.upscale_factor;
  const lightbox = document.getElementById("generation-lightbox");
  const lightboxImage = document.getElementById("generation-lightbox-image");
  const lightboxVideo = document.getElementById("generation-lightbox-video");
  const lightboxName = document.getElementById("generation-lightbox-name");
  const lightboxDownload = document.getElementById("generation-lightbox-download");
  const fieldHelps = [...root.querySelectorAll(".field-help[data-tooltip]")];
  const panels = [...root.querySelectorAll("[data-step]")];
  const progress = [...root.querySelectorAll("[data-progress]")];
  let currentStep = 1;
  let requiresImage = false;
  let allowsImages = false;
  let uploadInFlight = false;
  const previewURLs = new Map();
  const uploadedImages = new Map();
  let primaryImageSize = null;

  const krea2EditMaxBaseMegapixels = 4.7;
  const krea2EditMaxLongestSide = 4096;
  let progressSocket = null;
  let liveProgressReceived = false;
  let promptAssistantApproved = false;
  let promptAssistantOriginal = "";
  let promptAssistantSuggestion = "";
  let promptAssistantAction = "";
  let activeGenerationID = "";

  const clamp = (value, min, max) => Math.min(max, Math.max(min, value));
  const referenceRoleLabels = {
    base_scene: "Основной кадр и композиция",
    identity: "Внешность и лицо",
    wardrobe_object: "Одежда, предмет или материал",
    pose_composition: "Поза и ракурс",
    style: "Стиль, свет и цвет",
    background: "Фон и окружение",
    details: "Текст и мелкие детали",
  };
  const numericValue = (value, fallback = 0) => {
    const parsed = Number(String(value).replaceAll(",", "."));
    return Number.isFinite(parsed) ? parsed : fallback;
  };
  const roundToMultiple = (value, multiple) => Math.max(256, Math.floor(value / multiple) * multiple);

  const setGenerationActions = ({ retry = false, cancel = false } = {}) => {
    if (!resultActions) return;
    resultActions.hidden = !retry && !cancel;
    if (retryGeneration) retryGeneration.hidden = !retry;
    if (cancelGeneration) cancelGeneration.hidden = !cancel;
  };

  // Chrome treats a comma in <input type="number"> as an invalid value on
  // several Russian mobile keyboards. Fractional workflow controls therefore
  // use text inputs with a decimal keypad; the value is normalized only while
  // creating the request for ComfyUI.
  const localizedDecimalInputs = [...form.querySelectorAll('input[type="number"][step]')].filter((field) => {
    const step = field.getAttribute("step") || "";
    return step.includes(".") && Number.isFinite(Number(step));
  });
  localizedDecimalInputs.forEach((field) => {
    field.dataset.localizedDecimal = "true";
    field.type = "text";
    field.inputMode = "decimal";
    field.autocomplete = "off";

    const hint = document.createElement("small");
    hint.className = "localized-decimal-hint";
    hint.hidden = true;
    hint.setAttribute("role", "status");
    field.insertAdjacentElement("afterend", hint);
    const updateDecimalHint = () => {
      const value = field.value.trim();
      const hasComma = value.includes(",");
      const validNumber = value === "" || /^[+-]?(?:\d+(?:[.,]\d*)?|[.,]\d+)$/.test(value);
      const invalid = hasComma || !validNumber;
      field.classList.toggle("has-localized-decimal-error", invalid);
      field.setAttribute("aria-invalid", invalid ? "true" : "false");
      hint.hidden = !invalid;
      if (hasComma) {
        hint.textContent = "Используйте точку: например 0.22. Запятая будет исправлена при запуске.";
      } else if (!validNumber) {
        hint.textContent = "Введите число, например 0.22.";
      }
    };
    field.addEventListener("input", updateDecimalHint);
    field.addEventListener("blur", updateDecimalHint);
    updateDecimalHint();
  });

  const lutControl = root.querySelector("[data-lut-control]");
  const lutEnabled = root.querySelector("[data-lut-enabled]");
  const lutOptions = root.querySelector("[data-lut-options]");
  const lutName = root.querySelector("[data-lut-name]");
  const lutStrength = root.querySelector("[data-lut-strength]");
  const lutStrengthOutput = root.querySelector("[data-lut-strength-output]");
  const lutExplanation = root.querySelector("[data-lut-explanation]");
  const lutProfiles = {
    "LC_Crushed_Blacks.cube": { strength: 0.18, description: "Плотнее делает тёмные участки и добавляет сдержанный плёночный контраст. Хорош для портретов, вечернего света и драматичного настроения; начинайте с 18%." },
    "LC Highlights_Protection.cube": { strength: 0.2, description: "Смягчает яркие области и сохраняет больше деталей в светах. Полезен для оконного, дневного и контрастного света, когда кожа или небо выглядят слишком выбеленными." },
    "Cool_Natural_Breeze.cube": { strength: 0.16, description: "Немного охлаждает тени и оставляет спокойный, естественный баланс. Подходит для чистого современного портрета и предметов, если не нужна агрессивная стилизация." },
    "street.cube": { strength: 0.28, description: "Добавляет более заметный городской контраст и цветовой акцент. Уместен для fashion, улицы и клипового настроения; на лице обычно достаточно 20-30%." },
  };
  const renderLUT = ({ applyRecommendation = false } = {}) => {
    if (!lutControl || !lutEnabled || !lutOptions || !lutName || !lutStrength || !lutStrengthOutput || !lutExplanation) return;
    const enabled = lutEnabled.checked;
    const profile = lutProfiles[lutName.value] || lutProfiles["LC_Crushed_Blacks.cube"];
    if (enabled && applyRecommendation) lutStrength.value = String(profile.strength);
    lutOptions.hidden = !enabled;
    lutName.disabled = !enabled;
    lutStrength.disabled = !enabled;
    root.querySelectorAll("[data-lut-strength-preset]").forEach((button) => { button.disabled = !enabled; });
    if (!enabled) {
      lutExplanation.textContent = "Выключено: финальный кадр сохранит цвета, полученные после генерации и обработки, без общей тонировки. Включайте LUT только когда нужен единый художественный цветовой стиль.";
      return;
    }
    const strength = clamp(numericValue(lutStrength.value, profile.strength), 0, 0.7);
    lutStrength.value = strength.toFixed(2);
    lutStrengthOutput.textContent = `${Math.round(strength * 100)}%`;
    lutExplanation.textContent = profile.description;
    root.querySelectorAll("[data-lut-strength-preset]").forEach((button) => {
      button.classList.toggle("is-active", Math.abs(numericValue(button.dataset.lutStrengthPreset) - strength) < 0.005);
    });
  };
  lutEnabled?.addEventListener("change", () => renderLUT({ applyRecommendation: lutEnabled.checked }));
  lutName?.addEventListener("change", () => renderLUT({ applyRecommendation: true }));
  lutStrength?.addEventListener("input", () => renderLUT());
  root.querySelectorAll("[data-lut-strength-preset]").forEach((button) => {
    button.addEventListener("click", () => {
      if (!lutStrength || button.disabled) return;
      lutStrength.value = button.dataset.lutStrengthPreset || lutStrength.value;
      renderLUT();
    });
  });
  renderLUT();

  const updateOriginalResolution = () => {
    if (!originalResolution) return;
    if (!primaryImageSize) {
      originalResolution.textContent = "Выберите основное фото: его размер будет подставлен автоматически.";
      return;
    }
    const { width: sourceWidth, height: sourceHeight } = primaryImageSize;
    const sourceMegapixelsValue = sourceWidth * sourceHeight / (1024 * 1024);
    const selected = selectedGenerationWorkflow();
    const isKreaEdit = selected?.dataset.family === "krea2" && selected?.dataset.templateId === "image-to-image";
    const maximumPixels = isKreaEdit ? krea2EditMaxBaseMegapixels * 1024 * 1024 : Infinity;
    const maximumSide = isKreaEdit ? krea2EditMaxLongestSide : 4096;
    const scale = Math.min(1, maximumSide / Math.max(sourceWidth, sourceHeight), Math.sqrt(maximumPixels / (sourceWidth * sourceHeight)));
    const targetWidth = roundToMultiple(sourceWidth * scale, 8);
    const targetHeight = roundToMultiple(sourceHeight * scale, 8);
    const capped = scale < 1 || targetWidth !== sourceWidth || targetHeight !== sourceHeight;
    if (isKreaEdit) {
      originalResolution.textContent = `Исходник: ${sourceWidth} × ${sourceHeight} · ${sourceMegapixelsValue.toFixed(2).replace(".", ",")} Мп. Krea2 сохранит пропорции и обработает ${targetWidth} × ${targetHeight}: предел итогового кадра 4,7 Мп.`;
      return;
    }
    originalResolution.textContent = capped
      ? `Исходник: ${sourceWidth} × ${sourceHeight} · ${sourceMegapixelsValue.toFixed(2).replace(".", ",")} Мп. Для ComfyUI будет использовано ${targetWidth} × ${targetHeight}.`
      : `Исходник: ${sourceWidth} × ${sourceHeight} · ${sourceMegapixelsValue.toFixed(2).replace(".", ",")} Мп. Размер будет сохранён.`;
  };

  const applyOriginalResolution = () => {
    if (!preserveOriginalSize?.checked || !primaryImageSize) return;
    const selected = selectedGenerationWorkflow();
    const isKreaEdit = selected?.dataset.family === "krea2" && selected?.dataset.templateId === "image-to-image";
    const maximumPixels = isKreaEdit ? krea2EditMaxBaseMegapixels * 1024 * 1024 : Infinity;
    const maximumSide = isKreaEdit ? krea2EditMaxLongestSide : 4096;
    const scale = Math.min(1, maximumSide / Math.max(primaryImageSize.width, primaryImageSize.height), Math.sqrt(maximumPixels / (primaryImageSize.width * primaryImageSize.height)));
    const targetWidth = roundToMultiple(primaryImageSize.width * scale, 8);
    const targetHeight = roundToMultiple(primaryImageSize.height * scale, 8);
    width.value = String(targetWidth);
    height.value = String(targetHeight);
    aspect.value = "custom";
    outputMegapixels.value = (targetWidth * targetHeight / (1024 * 1024)).toFixed(2);
    if (sourceMegapixels) sourceMegapixels.value = clamp(targetWidth * targetHeight / (1024 * 1024), 0.25, 16).toFixed(2);
    if (maxSide) maxSide.value = isKreaEdit ? String(krea2EditMaxLongestSide) : "4096";
    updateOriginalResolution();
    updateResolutionPreview();
  };

  const syncSelectedImageSummary = () => {
    const primary = imageSlots[0];
    const file = primary?.input?.files?.[0];
    const previewURL = previewURLs.get(1);
    const visible = Boolean((requiresImage || allowsImages) && file && previewURL);
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
    const megapixels = clamp(numericValue(outputMegapixels.value, 1.9), 0.1, 16);
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
    if (lightboxVideo) {
      lightboxVideo.pause();
      lightboxVideo.removeAttribute("src");
      lightboxVideo.load();
    }
    document.body.classList.remove("generation-lightbox-open");
  };

  const closeFieldHelps = (except = null) => {
    fieldHelps.forEach((help) => {
      if (help === except) return;
      help.classList.remove("is-open");
      help.setAttribute("aria-expanded", "false");
    });
  };

  const positionFieldHelp = (help) => {
    const rect = help.getBoundingClientRect();
    const tooltipWidth = Math.min(250, Math.max(0, window.innerWidth - 24));
    const targetCenter = rect.left + rect.width / 2;
    const boundedCenter = Math.min(window.innerWidth - 12 - tooltipWidth / 2, Math.max(12 + tooltipWidth / 2, targetCenter));
    help.style.setProperty("--field-help-shift", `${Math.round(boundedCenter - targetCenter)}px`);
  };

  const downloadURL = (outputURL) => {
    const url = new URL(outputURL, window.location.origin);
    url.searchParams.set("download", "1");
    return url.pathname + url.search;
  };

  const openLightbox = (output) => {
    if (!lightbox) return;
    const isVideo = output.media_type === "video";
    lightboxImage.hidden = isVideo;
    if (lightboxVideo) lightboxVideo.hidden = !isVideo;
    if (isVideo && lightboxVideo) {
      lightboxVideo.src = output.url;
      lightboxVideo.muted = false;
      lightboxVideo.play().catch(() => {});
    } else {
      lightboxImage.src = output.url;
    }
    lightboxName.textContent = output.filename;
    lightboxDownload.href = downloadURL(output.url);
    lightboxDownload.download = output.filename;
    lightbox.hidden = false;
    document.body.classList.add("generation-lightbox-open");
    lightbox.querySelector(".generation-lightbox-close")?.focus();
  };

  lightbox?.querySelectorAll("[data-lightbox-close]").forEach((button) => button.addEventListener("click", closeLightbox));
  lightboxImage?.addEventListener("click", closeLightbox);
  document.addEventListener("keydown", (event) => { if (event.key === "Escape") closeLightbox(); });
  fieldHelps.forEach((help) => {
    help.setAttribute("role", "button");
    help.setAttribute("aria-expanded", "false");
    help.addEventListener("click", (event) => {
      event.preventDefault();
      event.stopPropagation();
      const open = !help.classList.contains("is-open");
      if (open) positionFieldHelp(help);
      closeFieldHelps(open ? help : null);
      help.classList.toggle("is-open", open);
      help.setAttribute("aria-expanded", String(open));
    });
    help.addEventListener("keydown", (event) => {
      if (event.key !== "Enter" && event.key !== " ") return;
      event.preventDefault();
      help.click();
    });
  });
  document.addEventListener("click", () => closeFieldHelps());
  document.addEventListener("keydown", (event) => { if (event.key === "Escape") closeFieldHelps(); });
  window.addEventListener("resize", () => fieldHelps.filter((help) => help.classList.contains("is-open")).forEach(positionFieldHelp));

  const wireVideoPreview = (button, output) => {
    const video = button?.querySelector("video");
    if (!video) return;
    const start = () => {
      video.muted = true;
      video.play().catch(() => {});
    };
    const stop = () => {
      video.pause();
      video.currentTime = 0;
    };
    button.addEventListener("pointerenter", start);
    button.addEventListener("pointerleave", stop);
    button.addEventListener("focusin", start);
    button.addEventListener("focusout", stop);
    button.addEventListener("click", () => openLightbox(output));
  };

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
  const isMiniMaxSelected = () => selectedGenerationWorkflow()?.dataset.family === "minimax_h3";
  const maxInputImages = () => Math.max(1, Number(selectedGenerationWorkflow()?.dataset.maxInputImages || (templateID.value === "minimax-h3-video" ? 4 : 1)));
  const activeMaxInputImages = () => isMiniMaxSelected() && miniMaxVideoModeSelect?.value === "frames" ? Math.min(2, maxInputImages()) : maxInputImages();

  const promptAssistantReferences = () => {
    if (templateID.value !== "image-to-image") return [];
    return imageSlots
      .filter((item) => item.index <= activeMaxInputImages() && item.input?.files?.[0])
      .map((item) => ({ number: item.index, role: item.role?.value || "base_scene" }));
  };

  const syncReferenceMap = () => {
    if (!referenceMap || !referenceMapList) return;
    const references = promptAssistantReferences();
    referenceMap.hidden = references.length === 0;
    referenceMapList.replaceChildren();
    references.forEach((reference) => {
      const item = imageSlots.find((slot) => slot.index === reference.number);
      if (item?.role && item.index === 1) item.role.value = "base_scene";
      const card = document.createElement("div");
      card.className = "generation-reference-map-item";
      const preview = previewURLs.get(reference.number);
      if (preview) {
        const image = document.createElement("img");
        image.src = preview;
        image.alt = `Изображение ${reference.number}`;
        card.append(image);
      }
      const details = document.createElement("span");
      const number = document.createElement("b");
      number.textContent = `Изображение ${reference.number}`;
      const role = document.createElement("em");
      role.textContent = referenceRoleLabels[reference.role] || "Референс";
      details.append(number, role);
      card.append(details);
      referenceMapList.append(card);
    });
  };

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
    syncReferenceMap();
  };

  const syncImageSlots = () => {
    const isMiniMax = isMiniMaxSelected() || templateID.value === "minimax-h3-video";
    const isImageEdit = templateID.value === "image-to-image";
    const referenceMode = miniMaxVideoModeSelect?.value === "references";
    const maximum = (requiresImage || allowsImages) ? activeMaxInputImages() : 0;
    const isKrea = selectedGenerationWorkflow()?.dataset.family === "krea2";
    if (imageSourceGrid) imageSourceGrid.dataset.visibleSlots = String(maximum);
    imageSlots.forEach((item) => {
      const visible = item.index <= maximum;
      item.slot.hidden = !visible;
      if (!visible) clearImageSlot(item);
      if (!item.label) return;
      if (isMiniMax && referenceMode) item.label.textContent = `Референс ${item.index}${item.index === 1 ? " · обязательно" : " · необязательно"}`;
      else if (isMiniMax) item.label.textContent = item.index === 1 ? "Первый кадр · обязательно" : "Последний кадр · необязательно";
      else if (item.index === 1) item.label.textContent = "Фото 1 · обязательно";
      else item.label.textContent = isKrea && item.index === 2 ? "Фото 2 · дополнительное" : `Фото ${item.index} · референс`;
      const roleControl = item.role?.closest(".image-reference-role");
      if (roleControl) roleControl.hidden = !isImageEdit;
      if (item.role) {
        if (item.index === 1) item.role.value = "base_scene";
        item.role.disabled = !isImageEdit || item.index === 1;
      }
    });
    const note = document.getElementById("image-source-note");
    if (isMiniMax && note) {
      note.textContent = referenceMode
        ? "Добавьте от одного до четырёх фотореференсов. Первый обязательный, остальные появятся по мере добавления."
        : "Добавьте первый кадр. Второе фото необязательно: оно станет последним кадром ролика.";
      syncReferenceMap();
      return;
    }
    if (note) note.textContent = maximum > 1
      ? isImageEdit
        ? `Фото 1 задаёт базовую сцену. Для каждого референса выберите роль, чтобы ассистент точно связал image 1–4 в промте.`
        : `Первое фото обязательно. Можно добавить ещё до ${maximum - 1} ${maximum === 2 ? "референса" : "референсов"}.`
      : "Загрузите исходное фото для редактирования.";
    syncReferenceMap();
  };

  const chooseScenario = (button) => {
    if (button.disabled) return;
    root.querySelectorAll(".scenario-choice").forEach((item) => item.classList.remove("is-selected"));
    button.classList.add("is-selected");
    templateID.value = button.dataset.workflowId;
    requiresImage = button.dataset.requiresImage === "true";
    allowsImages = button.dataset.allowsImages === "true";
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
    const needsImage = requiresImage || isMiniMaxSelected();
    workflowNext.disabled = !hasWorkflow || (needsImage && !hasImage);
    workflowNext.textContent = needsImage ? "Загрузить фото и продолжить" : "Продолжить";
  };

  const updateWorkflowCompatibility = () => {
    let visible = 0;
    root.querySelectorAll(".generation-workflow-choice").forEach((item) => {
      const matches = item.dataset.templateId === templateID.value;
      item.hidden = !matches;
      if (matches) visible += 1;
    });
    if (miniMaxVideoMode) miniMaxVideoMode.hidden = templateID.value !== "minimax-h3-video";
    if (imageSourceFields) imageSourceFields.hidden = !(requiresImage || allowsImages);
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

  const setPromptAssistantState = (message, state = "") => {
    if (!promptAssistantState) return;
    promptAssistantState.textContent = message;
    promptAssistantState.dataset.state = state;
  };

  const resetPromptAssistantReview = () => {
    promptAssistantApproved = false;
    promptAssistantOriginal = "";
    promptAssistantSuggestion = "";
    promptAssistantAction = "";
    if (promptAssistantReview) promptAssistantReview.hidden = true;
    if (promptAssistantDraft) promptAssistantDraft.value = "";
  };

  const syncPromptAssistant = () => {
    if (!promptAssistant || !promptAssistantEnabled || !promptAssistantControls || !promptAssistantTemplate) return;
    const isEdit = templateID.value === "image-to-image";
    promptAssistant.hidden = !templateID.value;
    promptAssistantControls.hidden = !promptAssistantEnabled.checked;
    [...promptAssistantTemplate.options].forEach((option) => {
      const imageOnly = option.dataset.imageOnly !== undefined;
      option.hidden = imageOnly && !isEdit;
      option.disabled = imageOnly && !isEdit;
    });
    const selectedAssistantTemplate = promptAssistantTemplate.selectedOptions[0];
    if (!isEdit && selectedAssistantTemplate?.dataset.imageOnly !== undefined) {
      promptAssistantTemplate.value = "workflow-default";
      resetPromptAssistantReview();
    }
    if (!promptAssistantEnabled.checked) {
      resetPromptAssistantReview();
      setPromptAssistantState("Ассистент выключен. Используется ваш исходный промт.");
    } else if (!promptAssistantReview?.hidden) {
      setPromptAssistantState("Проверьте вариант и примените его либо оставьте свой промт.", "review");
    } else {
      setPromptAssistantState("Вариант будет создан локальной моделью e4b и затем выгружен из видеопамяти.");
    }
  };

  const syncWorkflowFields = () => {
    const preset = selectedGenerationWorkflow();
    const family = preset?.dataset.family || model?.selectedOptions?.[0]?.dataset.family || "";
    const isEdit = preset?.dataset.templateId === "image-to-image";
    const isKrea = family === "krea2";
    const isKreaText = isKrea && !isEdit;
    const isKreaEdit = isKrea && isEdit;
    const isFluxEdit = family === "flux2" && isEdit;
    const isMiniMax = family === "minimax_h3";
    syncPromptAssistant();
    if (outputMegapixels) {
      outputMegapixels.max = isKreaEdit ? String(krea2EditMaxBaseMegapixels) : "16";
      if (isKreaEdit && numericValue(outputMegapixels.value, krea2EditMaxBaseMegapixels) > krea2EditMaxBaseMegapixels) {
        outputMegapixels.value = String(krea2EditMaxBaseMegapixels);
      }
    }
    if (isKreaEdit) {
      if (maxSide) maxSide.value = String(krea2EditMaxLongestSide);
      if (preserveOriginalLabel) preserveOriginalLabel.textContent = "Сохранить пропорции исходного фото в безопасном размере Krea2";
      applyOriginalResolution();
    } else if (preserveOriginalLabel) {
      preserveOriginalLabel.textContent = "Сохранить размер исходного фото";
    }
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
      field.hidden = Boolean(isMiniMax || (isEdit && preserveOriginalSize?.checked));
    });
    setFieldState(".krea-workflow-field", isKrea);
    setFieldState(".krea-text-workflow-field", isKreaText);
    setFieldState(".flux-edit-settings", isFluxEdit);
    setFieldState(".krea-edit-settings", isKreaEdit);
    setFieldState(".lut-workflow-field", isFluxEdit || isKreaEdit);
    renderLUT();
    setFieldState(".standard-main-settings", !isEdit && !isMiniMax);
    setFieldState(".minimax-video-settings", isMiniMax);
    setFieldState(".minimax-reference-field", isMiniMax && miniMaxVideoModeSelect?.value === "references");
    if (isFluxEdit) syncAdaptiveLoraSlots("flux");
    if (isKreaText) syncAdaptiveLoraSlots("krea");
    if (qualityField) qualityField.hidden = !isKreaText;
    if (editorProfile) editorProfile.hidden = !isEdit;
    syncSelectedImageSummary();
    if (isFluxEdit) {
      if (editorProfileTitle) editorProfileTitle.textContent = "Flux2: фото и промт";
      if (editorProfileDescription) editorProfileDescription.textContent = "До четырёх изображений: основной кадр и до трёх референсов. Flux2 работает с латентами исходных изображений.";
      if (editSourceTitle) editSourceTitle.textContent = "Исходник и референсы Flux2";
      if (editSourceDescription) editSourceDescription.textContent = "Выберите детализацию входных фото. Размер результата меняется только при включённой настройке кадра.";
      if (mainPassTitle) mainPassTitle.textContent = "Параметры Flux2";
      if (mainPassDescription) mainPassDescription.textContent = "Шаги, сила следования промту, сила перерисовки и планировщик исходной схемы Flux2.";
    } else if (isKreaEdit) {
      if (editorProfileTitle) editorProfileTitle.textContent = "Krea 2: фото и промт";
      if (editorProfileDescription) editorProfileDescription.textContent = "Основное фото и один дополнительный референс. Krea2 сохраняет внешность по исходному фото.";
      if (editSourceTitle) editSourceTitle.textContent = "Привязка Krea2 к исходнику";
      if (editSourceDescription) editSourceDescription.textContent = "Сила сохранения исходника и анализ фото управляют тем, насколько строго Krea2 держится за оригинал.";
      if (mainPassTitle) mainPassTitle.textContent = "Параметры Krea2: сохранение внешности";
      if (mainPassDescription) mainPassDescription.textContent = "Параметры редактирования до отдельного качественного апскейла Krea2.";
    } else if (isMiniMax) {
      if (mainPassTitle) mainPassTitle.textContent = "MiniMax H3";
      if (mainPassDescription) mainPassDescription.textContent = "Стабильный граф видео с оригинальными MiniMax VAE, звуком и браузерным H.264-выходом.";
    } else {
      if (mainPassTitle) mainPassTitle.textContent = "Основной проход";
      if (mainPassDescription) mainPassDescription.textContent = "Параметры основного семплирования выбранной схемы генерации.";
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
      const kreaProfiles = {
        fast: { base: "0.75", output: "1", upscaleSteps: "3", detailSteps: "1", detailDenoise: "0.02" },
        balanced: { base: "1", output: "1.9", upscaleSteps: "5", detailSteps: "2", detailDenoise: "0.03" },
        "krea-2-5": { base: "1.1", output: "2.5", upscaleSteps: "6", detailSteps: "2", detailDenoise: "0.03" },
        "krea-3-2": { base: "1.25", output: "3.2", upscaleSteps: "6", detailSteps: "3", detailDenoise: "0.032" },
        maximum: { base: "1.5", output: "4", upscaleSteps: "8", detailSteps: "3", detailDenoise: "0.035" },
        "krea-4-7": { base: "1.75", output: "4.7", upscaleSteps: "8", detailSteps: "3", detailDenoise: "0.035" },
      };
      const profile = kreaProfiles[mode] || kreaProfiles.balanced;
      baseMegapixels.value = profile.base;
      outputMegapixels.value = profile.output;
      upscaleSteps.value = profile.upscaleSteps;
      detailSteps.value = profile.detailSteps;
      detailDenoise.value = profile.detailDenoise;
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

  promptAssistantEnabled?.addEventListener("change", syncPromptAssistant);
  promptAssistantTemplate?.addEventListener("change", () => {
    resetPromptAssistantReview();
    syncPromptAssistant();
  });
  promptAssistantThink?.addEventListener("change", () => {
    resetPromptAssistantReview();
    syncPromptAssistant();
  });
  positive?.addEventListener("input", () => {
    if (promptAssistantEnabled?.checked && !promptAssistantReview?.hidden) {
      promptAssistantApproved = false;
      setPromptAssistantState("Исходный промт изменён. Подготовьте новый вариант или оставьте свой промт.", "review");
    }
	if (promptAssistantAction === "applied") promptAssistantAction = "applied_edited";
  });

  promptAssistantImprove?.addEventListener("click", async () => {
    const original = positive?.value.trim() || "";
	promptAssistantOriginal = original;
	promptAssistantSuggestion = "";
	promptAssistantAction = "";
    const mode = templateID.value;
    if (!original || (mode !== "text-to-image" && mode !== "image-to-image" && mode !== "minimax-h3-video")) {
      setPromptAssistantState("Сначала выберите схему генерации и введите позитивный промт.", "error");
      positive?.focus();
      return;
    }
    promptAssistantImprove.disabled = true;
    promptAssistantImprove.classList.add("is-loading");
    resetPromptAssistantReview();
    setPromptAssistantState(promptAssistantThink?.checked ? "Локальная модель e4b обдумывает и дорабатывает промт..." : "Локальная модель e4b дорабатывает промт...", "loading");
    try {
      const body = new URLSearchParams({
        csrf: form.elements.csrf?.value || "",
        prompt: original,
        template_id: mode,
        assistant_template: promptAssistantTemplate?.value || "workflow-default",
        assistant_think: promptAssistantThink?.checked ? "true" : "false",
      });
      promptAssistantReferences().forEach((reference) => {
        body.set(`image_role_${reference.number}`, reference.role);
      });
      const response = await fetch("/generate/prompt-assistant", {
        method: "POST",
        headers: { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8" },
        body,
        credentials: "same-origin",
      });
      const payload = await response.json().catch(() => ({}));
      if (!response.ok || !payload.prompt) throw new Error(payload.error || "Не удалось подготовить вариант");
      promptAssistantDraft.value = payload.prompt;
	  promptAssistantSuggestion = payload.prompt;
      promptAssistantReview.hidden = false;
      setPromptAssistantState(`Вариант подготовлен моделью ${payload.model || "e4b"}. Подтвердите или отредактируйте его.`, "review");
      promptAssistantDraft.focus({ preventScroll: true });
    } catch (error) {
      setPromptAssistantState(error.message || "Не удалось подготовить вариант", "error");
    } finally {
      promptAssistantImprove.disabled = false;
      promptAssistantImprove.classList.remove("is-loading");
    }
  });

  promptAssistantApply?.addEventListener("click", () => {
    const suggestion = promptAssistantDraft?.value.trim() || "";
    if (!suggestion) {
      setPromptAssistantState("Вариант ассистента пуст. Отредактируйте его или оставьте свой промт.", "error");
      return;
    }
    positive.value = suggestion;
    promptAssistantApproved = true;
	promptAssistantAction = "applied";
    promptAssistantReview.hidden = true;
    setPromptAssistantState("Вариант применён. Его можно дополнительно отредактировать перед генерацией.", "approved");
    positive.focus({ preventScroll: true });
  });

  promptAssistantKeep?.addEventListener("click", () => {
    promptAssistantApproved = true;
	promptAssistantAction = "kept_original";
    promptAssistantReview.hidden = true;
    setPromptAssistantState("Оставлен ваш исходный промт. Генерацию можно запускать.", "approved");
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
  miniMaxVideoModeSelect?.addEventListener("change", () => {
    if (miniMaxVideoModeHint) miniMaxVideoModeHint.textContent = miniMaxVideoModeSelect.value === "references"
      ? "Первое фото обязательно. Можно добавить ещё до трёх референсов: лица, стиль, предметы или окружение."
      : "Первый кадр обязателен. Второе фото можно добавить как последний кадр ролика.";
    syncImageSlots();
    updateWorkflowNext();
    syncWorkflowFields();
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
        syncReferenceMap();
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
      syncReferenceMap();
      updateWorkflowNext();
    });
    item.remove?.addEventListener("click", () => {
      clearImageSlot(item);
      updateWorkflowNext();
    });
    item.role?.addEventListener("change", () => {
      if (item.index === 1) item.role.value = "base_scene";
      syncReferenceMap();
      if (promptAssistantEnabled?.checked) resetPromptAssistantReview();
    });
  });

  workflowNext?.addEventListener("click", async () => {
    if (!generationWorkflowID.value) return;
    const requiresPrimary = requiresImage || isMiniMaxSelected();
    const selectedSlots = imageSlots.filter((item) => item.index <= activeMaxInputImages() && item.input?.files?.[0]);
    if (!selectedSlots.length && !requiresPrimary) {
      showStep(3);
      positive?.focus({ preventScroll: true });
      return;
    }
    if ((requiresPrimary && !selectedSlots.some((item) => item.index === 1)) || uploadInFlight) return;
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
      media.controls = false;
      media.muted = output.media_type === "video";
      media.loop = output.media_type === "video";
      media.playsInline = output.media_type === "video";
      media.preload = output.media_type === "video" ? "metadata" : "auto";
      media.loading = "lazy";
      media.alt = output.filename;
      const card = document.createElement("figure");
      card.className = "generation-output";
      const previewLink = document.createElement("button");
      previewLink.className = "generation-output-preview";
      previewLink.type = "button";
      previewLink.title = output.media_type === "video" ? "Открыть видеоплеер" : "Открыть на весь экран";
      if (output.media_type === "video") {
        previewLink.classList.add("generation-video-preview");
        const play = document.createElement("span");
        play.className = "generation-video-play";
        play.setAttribute("aria-hidden", "true");
        play.textContent = "▶";
        previewLink.append(media, play);
        wireVideoPreview(previewLink, output);
      } else {
        previewLink.append(media);
        previewLink.addEventListener("click", () => openLightbox(output));
      }
      const caption = document.createElement("figcaption");
      const filename = document.createElement("span");
      filename.textContent = output.filename;
      const download = document.createElement("a");
      download.className = "generation-output-download";
      download.href = downloadURL(output.url);
      download.download = output.filename;
      download.textContent = "Скачать файл";
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
    const preview = document.createElement("button");
    preview.className = "generation-output-preview";
    if (isVideo) {
      preview.type = "button";
      preview.classList.add("generation-video-preview");
      preview.title = "Открыть видеоплеер";
      const video = document.createElement("video");
      video.muted = true;
      video.loop = true;
      video.playsInline = true;
      video.preload = "metadata";
      video.src = item.url;
      const play = document.createElement("span");
      play.className = "generation-video-play";
      play.setAttribute("aria-hidden", "true");
      play.textContent = "▶";
      preview.append(video, play);
      wireVideoPreview(preview, item);
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
      grid.className = "generation-output-grid generation-library-grid";
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
  root.querySelectorAll("[data-generation-library-video]").forEach((button) => {
    wireVideoPreview(button, { filename: button.dataset.filename || "Видео", media_type: "video", url: button.dataset.url });
  });
  refreshExpiryLabels();
  window.setInterval(refreshExpiryLabels, 30000);

  const poll = async (promptID) => {
    const deadline = Date.now() + 10 * 60 * 1000;
    while (Date.now() < deadline) {
      if (activeGenerationID !== promptID) return;
      const response = await fetch(`/generate/status?prompt_id=${encodeURIComponent(promptID)}`, { credentials: "same-origin" });
      const payload = await response.json().catch(() => ({}));
      if (activeGenerationID !== promptID) return;
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
        activeGenerationID = "";
        setGenerationActions();
        resultTitle.textContent = "Готово";
        setGenerationProgress("Готово", "Результат подготовлен", 100);
        renderOutputs(payload.outputs || []);
		await refreshLibrary();
        result.scrollIntoView({ block: "start", behavior: "smooth" });
        return;
      }
      if (payload.state === "error") {
        activeGenerationID = "";
        setGenerationActions({ retry: true });
        throw new Error(payload.message || "ComfyUI завершил генерацию с ошибкой");
      }
      await new Promise((resolve) => window.setTimeout(resolve, 2000));
    }
    throw new Error("Генерация выполняется слишком долго. Проверьте результат позже в ComfyUI.");
  };

  retryGeneration?.addEventListener("click", () => form.requestSubmit());
  cancelGeneration?.addEventListener("click", async () => {
    const promptID = activeGenerationID;
    if (!promptID || cancelGeneration.disabled) return;
    cancelGeneration.disabled = true;
    cancelGeneration.textContent = "Отменяем...";
    try {
      const body = new URLSearchParams({ csrf: form.elements.csrf?.value || "", prompt_id: promptID });
      const response = await fetch("/generate/cancel", { method: "POST", headers: { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8" }, body, credentials: "same-origin" });
      const payload = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(payload.error || "Не удалось отменить генерацию");
      activeGenerationID = "";
      closeProgressSocket();
      resultTitle.textContent = payload.cancelled ? "Генерация отменена" : "Генерация завершена";
      resultStatus.textContent = payload.message || "Генерация отменена.";
      runProgress.hidden = true;
      setGenerationActions({ retry: true });
    } catch (error) {
      resultStatus.textContent = error.message || "Не удалось отменить генерацию";
      result.classList.add("has-error");
    } finally {
      cancelGeneration.disabled = false;
      cancelGeneration.textContent = "Отменить генерацию";
    }
  });

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (!selectedChoice() || !selectedGenerationWorkflow() || !model?.value || !positive.value.trim()) return;
    if (promptAssistantEnabled?.checked && !promptAssistantApproved) {
      setPromptAssistantState("Перед генерацией подтвердите вариант ассистента или выберите «Оставить мой промт».", "error");
      promptAssistant?.scrollIntoView({ block: "center", behavior: "smooth" });
      return;
    }
    const submit = document.getElementById("generation-submit");
    submit.disabled = true;
    submit.classList.add("is-loading");
    result.hidden = false;
    activeGenerationID = "";
    setGenerationActions();
    resultTitle.textContent = "Генерация выполняется";
    resultStatus.textContent = "Ставим задачу в очередь ComfyUI...";
    outputGrid.replaceChildren();
    result.classList.remove("has-error");
    runProgress.hidden = false;
    setGenerationProgress("Подготовка", "Проверяем параметры схемы генерации", null);
    result.scrollIntoView({ block: "start", behavior: "smooth" });
    try {
      const body = new FormData(form);
      // Mobile Russian keyboards commonly enter decimal fractions with a comma.
      // Convert values here as well as on the server before URL encoding them.
      const numericFieldNames = new Set([...form.querySelectorAll('input[type="number"], input[data-localized-decimal]')].map((field) => field.name));
      for (const [name, value] of [...body.entries()]) {
        if (numericFieldNames.has(name) && typeof value === "string") {
          body.set(name, value.replaceAll(",", "."));
        }
      }
      body.set("template_id", selectedChoice()?.dataset.workflowId || "");
      body.set("generation_workflow", selectedGenerationWorkflow()?.dataset.presetId || "");
	  body.set("assistant_requested", promptAssistantOriginal ? "true" : "false");
	  body.set("assistant_applied", promptAssistantAction.startsWith("applied") ? "true" : "false");
	  body.set("assistant_template_used", promptAssistantOriginal ? (promptAssistantTemplate?.value || "") : "");
	  body.set("assistant_think_used", promptAssistantOriginal && promptAssistantThink?.checked ? "true" : "false");
	  body.set("assistant_original_prompt", promptAssistantOriginal);
	  body.set("assistant_suggestion", promptAssistantSuggestion);
      ["input_image", "input_image_2", "input_image_3", "input_image_4"].forEach((name, index) => {
        body.set(name, uploadedImages.get(index + 1) || "");
      });
      const response = await fetch("/generate/run", { method: "POST", headers: { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8" }, body: new URLSearchParams(body), credentials: "same-origin" });
      const payload = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(payload.error || "Не удалось запустить генерацию");
      activeGenerationID = payload.prompt_id;
      setGenerationActions({ cancel: true });
      if (payload.state === "queued") {
        setGenerationProgress("В очереди ComfyUI", queuePositionDetail(payload.queue_position, payload.queue_total), null);
      } else if (payload.state === "running") {
        setGenerationProgress("ComfyUI начал генерацию", "Подготавливаем workflow", null);
      }
      if (payload.mining_paused) {
        resultStatus.textContent = "Майнинг подтверждённо остановлен на время этой приоритетной генерации.";
      } else if (payload.mining_warning) {
        resultStatus.textContent = payload.mining_warning;
      }
      refreshQueueOverview();
      connectProgressSocket(payload.prompt_id);
      await poll(payload.prompt_id);
    } catch (error) {
      activeGenerationID = "";
      setGenerationActions({ retry: true });
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
  syncPromptAssistant();
  refreshQueueOverview();
  window.setInterval(refreshQueueOverview, 5000);
})();
