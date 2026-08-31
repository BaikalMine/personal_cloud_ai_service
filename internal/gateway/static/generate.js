(() => {
  const root = document.querySelector("[data-comfy-generation]");
  if (!root) return;

  const workflowGuides = root.querySelector(".workflow-guides");

  const form = document.getElementById("generation-form");
  const templateID = document.getElementById("template-id");
  const generationWorkflowID = document.getElementById("generation-workflow-id");
  const inputImages = [
    document.getElementById("input-image"),
    document.getElementById("input-image-2"),
    document.getElementById("input-image-3"),
    document.getElementById("input-image-4"),
  ];
  const inputAudio = document.getElementById("input-audio");
  const inputVideo = document.getElementById("input-video");
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
    galleryChoice: slot.querySelector("[data-gallery-image-picker-choice]"),
    galleryButton: slot.querySelector("[data-gallery-image-picker-open]"),
  }));
  const workflowNext = document.getElementById("workflow-next");
  const imageSourceFields = document.getElementById("image-source-fields");
  const imageSourceGrid = root.querySelector(".source-image-grid");
  const miniMaxVideoMode = document.getElementById("minimax-video-mode");
  const miniMaxVideoModeInputs = [...root.querySelectorAll('input[name="video_mode"]')];
  const miniMaxVideoModeSelect = form.elements.video_mode;
  const miniMaxVideoModeHint = document.getElementById("minimax-video-mode-hint");
  const miniMaxReferenceMedia = document.getElementById("minimax-reference-media");
  const miniMaxAudioReference = document.getElementById("minimax-audio-reference");
  const miniMaxAudioFile = document.getElementById("minimax-audio-file");
  const miniMaxAudioPreview = document.getElementById("minimax-audio-preview");
  const miniMaxAudioName = document.getElementById("minimax-audio-name");
  const miniMaxAudioState = document.getElementById("minimax-audio-state");
  const miniMaxAudioRemove = document.getElementById("minimax-audio-remove");
  const miniMaxVideoReference = document.getElementById("minimax-video-reference");
  const miniMaxVideoFile = document.getElementById("minimax-video-file");
  const miniMaxVideoPreview = document.getElementById("minimax-video-preview");
  const miniMaxVideoPreviewMedia = document.getElementById("minimax-video-preview-media");
  const miniMaxVideoName = document.getElementById("minimax-video-name");
  const miniMaxVideoState = document.getElementById("minimax-video-state");
  const miniMaxVideoRemove = document.getElementById("minimax-video-remove");
  const miniMaxVideoQuality = document.getElementById("minimax-video-quality");
  const miniMaxVideoAspect = document.getElementById("minimax-video-aspect");
  const miniMaxUseSourceAspect = document.getElementById("minimax-use-source-aspect");
  const miniMaxVideoSwap = document.getElementById("minimax-video-swap");
  const miniMaxSourceAspectControl = document.getElementById("minimax-source-aspect-control");
  const miniMaxVideoSteps = document.getElementById("minimax-video-steps");
  const miniMaxVideoTurbo = document.getElementById("minimax-video-turbo");
  const miniMaxVideoTurboControl = document.getElementById("minimax-video-turbo-control");
  const miniMaxVideoTurboTitle = document.getElementById("minimax-video-turbo-title");
  const miniMaxVideoTurboDescription = document.getElementById("minimax-video-turbo-description");
  const miniMaxVideoResolutionPreview = document.getElementById("minimax-video-resolution-preview");
  const miniMaxVideoModelProfile = document.getElementById("minimax-video-model-profile");
  const miniMaxVideoSampler = document.getElementById("minimax-video-sampler");
  const miniMaxVideoScheduler = document.getElementById("minimax-video-scheduler");
  const miniMaxVideoShiftVideo = document.getElementById("minimax-video-shift-video");
  const miniMaxVideoShiftAudio = document.getElementById("minimax-video-shift-audio");
  const miniMaxVideoSharpenMethod = document.getElementById("minimax-video-sharpen-method");
  const workflowNote = document.getElementById("generation-workflow-note");
  const positive = document.getElementById("positive-prompt");
  const positivePromptLabel = document.getElementById("generation-positive-prompt-label");
  const negativePrompt = document.getElementById("negative-prompt");
  const negativePromptField = document.getElementById("generation-negative-prompt-field");
  const generationPromptFields = root.querySelector(".generation-prompt-fields");
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
  const preflight = document.getElementById("generation-preflight");
  const preflightSummary = document.getElementById("generation-preflight-summary");
  const preflightChecks = document.getElementById("generation-preflight-checks");
  const preflightButton = document.getElementById("generation-preflight-button");
  const preflightRepeat = document.getElementById("generation-preflight-repeat");
  const recipeSelect = document.getElementById("generation-recipe-select");
  const recipeApply = document.getElementById("generation-recipe-apply");
  const recipeDelete = document.getElementById("generation-recipe-delete");
  const recipeName = document.getElementById("generation-recipe-name");
  const recipeSave = document.getElementById("generation-recipe-save");
  const recipeState = document.getElementById("generation-recipe-state");
  const generationSummaryValue = document.getElementById("generation-summary-value");
  const generationOpenExact = document.getElementById("generation-open-exact");
  const generationAdvanced = root.querySelector(".generation-advanced");
  const variantsSection = document.getElementById("generation-variants");
  const variantsContent = document.getElementById("generation-history-content");
  const variantsToggle = document.getElementById("generation-history-toggle");
  const variantList = document.getElementById("generation-variant-list");
  const variantStateFilter = document.getElementById("generation-variant-state");
  const variantTemplateFilter = document.getElementById("generation-variant-template");
  const generationQuota = document.getElementById("generation-quota");
  const variantCount = document.getElementById("generation-variant-count");
  const repeatNotice = document.getElementById("generation-repeat-notice");
  const repeatNoticeTitle = document.getElementById("generation-repeat-title");
  const repeatNoticeMessage = document.getElementById("generation-repeat-message");
  const repeatNoticeDismiss = document.getElementById("generation-repeat-dismiss");
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
  const imagePicker = document.getElementById("generation-image-picker");
  const imagePickerGrid = document.getElementById("generation-image-picker-grid");
  const imagePickerState = document.getElementById("generation-image-picker-state");
  const imagePickerSlot = document.getElementById("generation-image-picker-slot");
  const fieldHelps = [...root.querySelectorAll(".field-help[data-tooltip]")];
  const panels = [...root.querySelectorAll("[data-step]")];
  const progress = [...root.querySelectorAll("[data-progress]")];
  let currentStep = 1;
  let requiresImage = false;
  let allowsImages = false;
  let uploadInFlight = false;
  const previewURLs = new Map();
  const selectedImages = new Map();
  const gallerySelections = new Map();
  const uploadedImages = new Map();
  let uploadedAudio = "";
  let uploadedVideo = "";
  let videoPreviewURL = "";
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
  let activeGenerationRequestID = "";
  let savedRecipes = [];
  let savedVariants = [];
  let variantsLoaded = false;
  let galleryPickerSlot = null;
  const requestedVariantID = new URLSearchParams(window.location.search).get("variant") || "";
  let requestedVariantHandled = false;
  const activeGenerationStorageKey = "ai-gateway.active-generation";
  const generationHistoryCollapsedStorageKey = "ai-gateway.generation-history-collapsed";
  let generationHistoryCollapsed = false;

  const clamp = (value, min, max) => Math.min(max, Math.max(min, value));
  const selectedImageFile = (item) => selectedImages.get(item?.index) || item?.input?.files?.[0] || null;
  const selectedImageSource = (item) => selectedImageFile(item) || gallerySelections.get(item?.index) || null;
  const hasSelectedImage = (item) => Boolean(selectedImageSource(item));
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
  const miniMaxMode = () => miniMaxVideoModeSelect?.value || "frames";
  const setMiniMaxMode = (value) => {
    const target = miniMaxVideoModeInputs.find((input) => input.value === value && !input.disabled);
    if (target) target.checked = true;
  };
  const miniMaxAspectDimensions = () => {
    const presets = {
      "1:1": [1080, 1080], "4:5": [1080, 1350], "16:9": [1344, 768], "9:16": [1080, 1920],
      "4:1": [1600, 400], "2:3": [832, 1248], "3:2": [1248, 832], "3:4": [896, 1152],
      "4:3": [1152, 896], "21:9": [1536, 640],
    };
    const source = primaryImageSize
      ? [primaryImageSize.width, primaryImageSize.height]
      : (presets[miniMaxVideoAspect?.value || "9:16"] || presets["9:16"]);
    return miniMaxVideoSwap?.checked ? [source[1], source[0]] : source;
  };
  const syncMiniMaxVideoProfile = ({ applyModelDefaults = false } = {}) => {
    const option = model?.selectedOptions?.[0];
    const integratedTurbo = option?.dataset.videoIntegratedTurbo === "true";
    const referenceOnly = option?.dataset.videoReferenceOnly === "true";
    const frameOption = miniMaxVideoModeInputs.find((input) => input.value === "frames");
    if (frameOption) frameOption.disabled = referenceOnly;
    if (referenceOnly && miniMaxMode() !== "references") {
      setMiniMaxMode("references");
      syncImageSlots();
      syncMiniMaxAudioReference();
    }
    if (applyModelDefaults && option?.value) {
      if (miniMaxVideoSteps) miniMaxVideoSteps.value = option.dataset.defaultSteps || (integratedTurbo ? "8" : "25");
      if (miniMaxVideoSampler) miniMaxVideoSampler.value = option.dataset.defaultSampler || "euler";
      if (miniMaxVideoScheduler) miniMaxVideoScheduler.value = option.dataset.defaultScheduler || "simple";
      if (miniMaxVideoShiftVideo) miniMaxVideoShiftVideo.value = option.dataset.defaultVideoShift || (integratedTurbo ? "12" : "11");
      if (miniMaxVideoShiftAudio) miniMaxVideoShiftAudio.value = option.dataset.defaultAudioShift || (integratedTurbo ? "7" : "3");
      if (miniMaxVideoTurbo) miniMaxVideoTurbo.checked = false;
    }
    if (integratedTurbo && miniMaxVideoTurbo) miniMaxVideoTurbo.checked = false;
    if (miniMaxVideoTurbo) miniMaxVideoTurbo.disabled = integratedTurbo;
    if (miniMaxVideoTurboControl) miniMaxVideoTurboControl.classList.toggle("is-locked", integratedTurbo);
    if (miniMaxVideoTurboTitle) miniMaxVideoTurboTitle.textContent = integratedTurbo ? "Turbo встроен в Eros Max" : "Turbo MiniMax H3";
    if (miniMaxVideoTurboDescription) miniMaxVideoTurboDescription.textContent = integratedTurbo
      ? "Дополнительная Turbo LoRA не применяется: модель уже содержит её слияние."
      : "Опциональная официальная LoRA v4: 4–8 шагов для быстрых проб.";
    if (miniMaxVideoModelProfile) miniMaxVideoModelProfile.textContent = integratedTurbo
      ? "H3 Eros Max beta4 · только reference-путь · встроенный Turbo · Euler · 6–8 шагов · Sigma 12 / 7."
      : "MiniMax H3 v4 · FL2VA для точных кадров, REF2VA для свободных референсов · Turbo опционален.";
    const turbo = !integratedTurbo && Boolean(miniMaxVideoTurbo?.checked);
    if (miniMaxVideoSteps) {
      miniMaxVideoSteps.min = integratedTurbo ? "6" : turbo ? "4" : "20";
      miniMaxVideoSteps.max = integratedTurbo ? "8" : turbo ? "8" : "25";
      const current = Number(miniMaxVideoSteps.value);
      if (!Number.isFinite(current) || current < Number(miniMaxVideoSteps.min) || current > Number(miniMaxVideoSteps.max)) {
        miniMaxVideoSteps.value = integratedTurbo ? "8" : turbo ? "6" : "25";
      }
    }
    if (miniMaxVideoSampler) {
      if (integratedTurbo) miniMaxVideoSampler.value = "euler";
      miniMaxVideoSampler.disabled = integratedTurbo || turbo;
    }
    if (miniMaxVideoModeHint && referenceOnly) {
      miniMaxVideoModeHint.textContent = "Выбрано: Eros Max использует REF2VA. Ролик строится по промту; фото, видео и аудио необязательны.";
    }
    if (!miniMaxVideoResolutionPreview) return;
    const quality = Number(miniMaxVideoQuality?.value);
    const [sourceWidth, sourceHeight] = miniMaxAspectDimensions();
    const scale = Math.min(1, quality / Math.max(1, sourceWidth, sourceHeight));
    const multiple = (value) => Math.max(32, Math.floor(value / 32) * 32);
    const targetWidth = multiple(sourceWidth * scale);
    const targetHeight = multiple(sourceHeight * scale);
    const sourceLabel = primaryImageSize ? "пропорции Фото 1" : `формат ${miniMaxVideoAspect?.value || "9:16"}`;
    miniMaxVideoResolutionPreview.textContent = `${targetWidth} × ${targetHeight} · ${sourceLabel}`;
  };

  const syncMiniMaxSharpenFields = () => {
    const method = miniMaxVideoSharpenMethod?.value || "rcas";
    root.querySelectorAll("[data-sharpen-field]").forEach((field) => {
      const name = field.dataset.sharpenField;
      field.hidden = !(
        (name === "radius" && method !== "rcas") ||
        (name === "threshold" && method === "adaptive_usm") ||
        (name === "iterations" && method === "deconvolution")
      );
    });
    const strength = form.elements.video_sharpen_strength;
    if (strength) {
      strength.max = method === "adaptive_usm" || method === "high_pass" ? "3" : "1";
      if (numericValue(strength.value) > numericValue(strength.max, 1)) strength.value = strength.max;
    }
  };
  const roundToMultiple = (value, multiple, minimum = 256) => Math.max(minimum, Math.floor(value / multiple) * multiple);
  const pause = (milliseconds) => new Promise((resolve) => window.setTimeout(resolve, milliseconds));
  const pauseWithCountdown = async (milliseconds, update, isActive = () => true) => {
    const retryAt = Date.now() + milliseconds;
    let shownSeconds = -1;
    while (isActive()) {
      const remaining = retryAt - Date.now();
      if (remaining <= 0) return;
      const seconds = Math.ceil(remaining / 1000);
      if (seconds !== shownSeconds) {
        shownSeconds = seconds;
        update(seconds);
      }
      await pause(Math.min(250, remaining));
    }
  };
  const newGenerationRequestID = () => window.crypto?.randomUUID?.() || `generation-${Date.now()}-${Math.random().toString(36).slice(2, 14)}`;
  const persistActiveGeneration = () => {
    if (!activeGenerationRequestID) return;
    try {
      window.localStorage.setItem(activeGenerationStorageKey, JSON.stringify({ requestID: activeGenerationRequestID, promptID: activeGenerationID, savedAt: Date.now() }));
    } catch (_) {
      // Private browsing can reject storage; the current tab can still poll normally.
    }
  };
  const clearActiveGeneration = () => {
    activeGenerationID = "";
    activeGenerationRequestID = "";
    try { window.localStorage.removeItem(activeGenerationStorageKey); } catch (_) {}
  };
  const storedActiveGeneration = () => {
    try {
      const value = JSON.parse(window.localStorage.getItem(activeGenerationStorageKey) || "null");
      if (!value || typeof value.requestID !== "string" || Date.now() - Number(value.savedAt || 0) > 24 * 60 * 60 * 1000) return null;
      return value;
    } catch (_) {
      return null;
    }
  };

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
    const isFluxEdit = selected?.dataset.family === "flux2" && selected?.dataset.templateId === "image-to-image";
    const minimumDimension = isKreaEdit || isFluxEdit ? 16 : 256;
    const maximumPixels = isKreaEdit ? krea2EditMaxBaseMegapixels * 1024 * 1024 : Infinity;
    const maximumSide = isKreaEdit ? krea2EditMaxLongestSide : 4096;
    const scale = Math.min(1, maximumSide / Math.max(sourceWidth, sourceHeight), Math.sqrt(maximumPixels / (sourceWidth * sourceHeight)));
    const targetWidth = roundToMultiple(sourceWidth * scale, 8, minimumDimension);
    const targetHeight = roundToMultiple(sourceHeight * scale, 8, minimumDimension);
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
    const isFluxEdit = selected?.dataset.family === "flux2" && selected?.dataset.templateId === "image-to-image";
    const minimumDimension = isKreaEdit || isFluxEdit ? 16 : 256;
    const maximumPixels = isKreaEdit ? krea2EditMaxBaseMegapixels * 1024 * 1024 : Infinity;
    const maximumSide = isKreaEdit ? krea2EditMaxLongestSide : 4096;
    const scale = Math.min(1, maximumSide / Math.max(primaryImageSize.width, primaryImageSize.height), Math.sqrt(maximumPixels / (primaryImageSize.width * primaryImageSize.height)));
    const targetWidth = roundToMultiple(primaryImageSize.width * scale, 8, minimumDimension);
    const targetHeight = roundToMultiple(primaryImageSize.height * scale, 8, minimumDimension);
    width.value = String(targetWidth);
    height.value = String(targetHeight);
    aspect.value = "custom";
    outputMegapixels.value = (targetWidth * targetHeight / (1024 * 1024)).toFixed(2);
    if (sourceMegapixels) sourceMegapixels.value = clamp(targetWidth * targetHeight / (1024 * 1024), 0.25, 16).toFixed(2);
    if (maxSide) maxSide.value = isKreaEdit ? String(krea2EditMaxLongestSide) : "4096";
    updateOriginalResolution();
    updateResolutionPreview();
    syncGenerationSummary();
  };

  const syncSelectedImageSummary = () => {
    const primary = imageSlots[0];
    const source = selectedImageSource(primary);
    const previewURL = previewURLs.get(1);
    const visible = Boolean((requiresImage || allowsImages) && source && previewURL);
    if (selectedImageSummary) selectedImageSummary.hidden = !visible;
    if (!visible) return;
    if (selectedImagePreview) selectedImagePreview.src = previewURL;
    if (selectedImageName) selectedImageName.textContent = source.name;
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

  const formatWait = (seconds) => {
    const safe = Math.max(0, Math.round(Number(seconds) || 0));
    if (!safe) return "";
    if (safe < 60) return `около ${safe} сек.`;
    const minutes = Math.ceil(safe / 60);
    return `около ${minutes} мин.`;
  };

  const queuePositionDetail = (position, total, estimatedSeconds = 0) => {
    const safePosition = Number(position) || 0;
    const safeTotal = Number(total) || 0;
    if (!safePosition || !safeTotal) return "Ожидаем свободный слот";
    const ahead = Math.max(0, safePosition - 1);
    const detail = ahead > 0
      ? `Ваше место: ${safePosition} из ${safeTotal}. Перед вами: ${ahead}.`
      : `Ваше место: 1 из ${safeTotal}. Генерация начнётся следующей.`;
    const wait = formatWait(estimatedSeconds);
    return wait ? `${detail} Ожидание: ${wait}` : detail;
  };

  const renderQueueOverview = (queue) => {
    if (!generationQueue) return;
    const running = Math.max(0, Number(queue?.running) || 0);
    const pending = Math.max(0, Number(queue?.pending) || 0);
    generationQueue.hidden = running + pending === 0;
    if (generationQueue.hidden) return;
    if (generationQueueTitle) generationQueueTitle.textContent = queue?.current_task || (running > 0 ? "Сервер занят" : "Ожидают запуска");
    if (generationQueueDetails) {
      const parts = [];
      if (running > 0) parts.push(`Выполняется: ${running}`);
      if (pending > 0) parts.push(`В очереди: ${pending}`);
      const wait = formatWait(queue?.estimated_wait_seconds);
      if (wait) parts.push(`Полная очередь: ${wait}`);
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
    const tooltip = help.dataset.tooltip?.trim();
    if (tooltip) help.setAttribute("aria-label", `Подсказка: ${tooltip}`);
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
    const markUnavailable = () => {
      button.dataset.videoUnavailable = "true";
      button.title = "Этот файл не поддерживается браузером. Его можно скачать.";
    };
    video.addEventListener("loadeddata", () => { delete button.dataset.videoUnavailable; }, { once: true });
    video.addEventListener("error", markUnavailable, { once: true });
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
    button.addEventListener("click", () => {
      if (window.aiGatewaySensitiveContent?.reveal(button)) return;
      openLightbox(output);
    });
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
  const activeMaxInputImages = () => isMiniMaxSelected() && miniMaxMode() === "frames" ? Math.min(2, maxInputImages()) : maxInputImages();

  const miniMaxReferencesAreAvailable = () => isMiniMaxSelected() && miniMaxMode() === "references";
  const miniMaxAudioIsAvailable = () => miniMaxReferencesAreAvailable();

  const syncMiniMaxAudioReference = () => {
    const available = miniMaxAudioIsAvailable();
    if (miniMaxReferenceMedia) miniMaxReferenceMedia.hidden = !available;
    if (miniMaxAudioReference) miniMaxAudioReference.hidden = !available;
    if (miniMaxVideoReference) miniMaxVideoReference.hidden = !available;
    if (inputAudio) inputAudio.value = available ? uploadedAudio : "";
    if (inputVideo) inputVideo.value = available ? uploadedVideo : "";
  };

  const hasPendingMiniMaxAudio = () => miniMaxAudioIsAvailable() && Boolean(miniMaxAudioFile?.files?.[0]) && !uploadedAudio;
  const hasPendingMiniMaxVideo = () => miniMaxReferencesAreAvailable() && Boolean(miniMaxVideoFile?.files?.[0]) && !uploadedVideo;

  const clearMiniMaxAudio = () => {
    if (miniMaxAudioFile) miniMaxAudioFile.value = "";
    uploadedAudio = "";
    if (inputAudio) inputAudio.value = "";
    if (miniMaxAudioName) miniMaxAudioName.textContent = "";
    if (miniMaxAudioState) miniMaxAudioState.textContent = "Готово к загрузке";
    if (miniMaxAudioPreview) miniMaxAudioPreview.hidden = true;
  };

  const clearMiniMaxVideo = () => {
    if (miniMaxVideoFile) miniMaxVideoFile.value = "";
    uploadedVideo = "";
    if (inputVideo) inputVideo.value = "";
    if (videoPreviewURL) URL.revokeObjectURL(videoPreviewURL);
    videoPreviewURL = "";
    if (miniMaxVideoPreviewMedia) miniMaxVideoPreviewMedia.removeAttribute("src");
    if (miniMaxVideoName) miniMaxVideoName.textContent = "";
    if (miniMaxVideoState) miniMaxVideoState.textContent = "Готово к загрузке";
    if (miniMaxVideoPreview) miniMaxVideoPreview.hidden = true;
  };

  const promptAssistantReferences = () => {
    if (templateID.value !== "image-to-image" && !(isMiniMaxSelected() && miniMaxMode() === "references")) return [];
    return imageSlots
      .filter((item) => item.index <= activeMaxInputImages() && hasSelectedImage(item))
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
	item.slot.classList.remove("has-image");
    const oldURL = previewURLs.get(item.index);
    if (oldURL?.startsWith("blob:")) URL.revokeObjectURL(oldURL);
    previewURLs.delete(item.index);
	selectedImages.delete(item.index);
    gallerySelections.delete(item.index);
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
    const referenceMode = miniMaxMode() === "references";
    const maximum = (requiresImage || allowsImages) ? activeMaxInputImages() : 0;
    const isKrea = selectedGenerationWorkflow()?.dataset.family === "krea2";
    if (imageSourceGrid) imageSourceGrid.dataset.visibleSlots = String(maximum);
    imageSlots.forEach((item) => {
      const visible = item.index <= maximum;
      item.slot.hidden = !visible;
      if (!visible) clearImageSlot(item);
      if (item.galleryChoice) item.galleryChoice.hidden = !isMiniMax;
      if (!item.label) return;
      if (isMiniMax && referenceMode) item.label.textContent = `Референс ${item.index} · необязательно`;
      else if (isMiniMax) item.label.textContent = item.index === 1 ? "Первый кадр · необязательно" : "Последний кадр · необязательно";
      else if (item.index === 1) item.label.textContent = "Фото 1 · обязательно";
      else item.label.textContent = isKrea && item.index === 2 ? "Фото 2 · дополнительное" : `Фото ${item.index} · референс`;
      const roleControl = item.role?.closest(".image-reference-role");
      if (roleControl) roleControl.hidden = !(isImageEdit || (isMiniMax && referenceMode));
      if (item.role) {
        if (item.index === 1) item.role.value = "base_scene";
        item.role.disabled = !(isImageEdit || (isMiniMax && referenceMode)) || item.index === 1;
      }
    });
    const note = document.getElementById("image-source-note");
    if (isMiniMax && note) {
      note.textContent = referenceMode
        ? "Файлы необязательны. Добавьте до четырёх фото с устройства или из своей галереи, а также видео и/или аудио; для каждого фото укажите его роль."
        : "Фото необязательны: без них работает текст в видео. Первый и последний кадры можно загрузить с устройства или выбрать из своей галереи.";
      syncReferenceMap();
      syncMiniMaxAudioReference();
      return;
    }
    if (note) note.textContent = maximum > 1
      ? isImageEdit
        ? `Фото 1 задаёт базовую сцену. Для каждого референса выберите роль, чтобы ассистент точно связал image 1–4 в промте.`
        : `Первое фото обязательно. Можно добавить ещё до ${maximum - 1} ${maximum === 2 ? "референса" : "референсов"}.`
      : "Загрузите исходное фото для редактирования.";
    syncReferenceMap();
    syncMiniMaxAudioReference();
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
    const hasImage = Boolean(hasSelectedImage(primary) || uploadedImages.get(1));
    const needsImage = requiresImage;
    const hasPendingUploads = imageSlots.some((item) => (
      item.index <= activeMaxInputImages()
      && hasSelectedImage(item)
      && !uploadedImages.get(item.index)
    )) || hasPendingMiniMaxAudio() || hasPendingMiniMaxVideo();
    workflowNext.disabled = !hasWorkflow || (needsImage && !hasImage);
    if (!needsImage && !hasPendingUploads) {
      workflowNext.textContent = "Продолжить";
    } else if (hasPendingUploads) {
      workflowNext.textContent = "Загрузить в ComfyUI и продолжить";
    } else {
      workflowNext.textContent = "Продолжить к промту";
    }
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
    if (button.dataset.family === "minimax_h3") syncMiniMaxVideoProfile({ applyModelDefaults: true });
    syncImageSlots();
    updateWorkflowNext();
  };

  const setNamedControlValue = (name, value) => {
    const control = form.elements.namedItem(name);
    if (!control) return;
    if (typeof control === "object" && "length" in control && !control.tagName) {
      const option = [...control].find((item) => item.value === value);
      if (option) option.checked = true;
      return;
    }
    if (control.type === "checkbox") {
      control.checked = value === "true" || value === "on" || value === "1";
      return;
    }
    control.value = value;
  };

  const applySavedValues = (values, { openStep = true } = {}) => {
    if (!values || typeof values !== "object") return false;
    const scenario = values.template_id ? root.querySelector(`.scenario-choice[data-workflow-id="${CSS.escape(values.template_id)}"]`) : null;
    if (scenario && !scenario.disabled && !scenario.classList.contains("is-selected")) chooseScenario(scenario);
    const workflow = values.generation_workflow ? root.querySelector(`.generation-workflow-choice[data-preset-id="${CSS.escape(values.generation_workflow)}"]`) : null;
    if (workflow && !workflow.disabled && !workflow.hidden && !workflow.classList.contains("is-selected")) chooseGenerationWorkflow(workflow);
    Object.entries(values).forEach(([name, value]) => {
      if (name === "template_id" || name === "generation_workflow" || name === "input_image" || name.startsWith("input_image_")) return;
      setNamedControlValue(name, String(value));
    });
    syncAdaptiveLoraSlots("krea");
    syncAdaptiveLoraSlots("flux");
    syncWorkflowFields();
    calculateResolution();
    syncMiniMaxVideoProfile();
    syncMiniMaxSharpenFields();
    if (openStep) {
      const needsImage = selectedChoice()?.dataset.requiresImage === "true";
      showStep(needsImage && !uploadedImages.get(1) ? 2 : 3);
    }
    return Boolean(selectedChoice() && selectedGenerationWorkflow());
  };

  const showRepeatNotice = (title, message, error = false) => {
    if (!repeatNotice || !repeatNoticeTitle || !repeatNoticeMessage) return;
    repeatNoticeTitle.textContent = title;
    repeatNoticeMessage.textContent = message;
    repeatNotice.classList.toggle("has-error", error);
    repeatNotice.hidden = false;
  };

  const clearRequestedVariantQuery = () => {
    const url = new URL(window.location.href);
    url.searchParams.delete("variant");
    window.history.replaceState({}, "", `${url.pathname}${url.search}${url.hash}`);
  };

  const restoreRequestedVariant = () => {
    if (!requestedVariantID || requestedVariantHandled) return;
    requestedVariantHandled = true;
    const variant = savedVariants.find((item) => String(item.id) === requestedVariantID);
    if (!variant) {
      showRepeatNotice("Вариант больше недоступен", "История хранится 24 часа. Выберите другой результат в галерее.", true);
      clearRequestedVariantQuery();
      return;
    }
    if (!applySavedValues(variant.values)) {
      showRepeatNotice("Не удалось перенести параметры", "Модель или workflow этого результата сейчас недоступны.", true);
      clearRequestedVariantQuery();
      return;
    }
    const needsFiles = variant.template_id === "image-to-image" || variant.template_id === "minimax-h3-video";
    showRepeatNotice(
      "Параметры перенесены",
      needsFiles ? "Сценарий и настройки восстановлены. Исходные файлы и референсы при необходимости добавьте заново." : "Сценарий, промт и настройки восстановлены. Проверьте их перед запуском."
    );
    clearRequestedVariantQuery();
    window.requestAnimationFrame(() => repeatNotice?.scrollIntoView({ behavior: "smooth", block: "center" }));
  };

  repeatNoticeDismiss?.addEventListener("click", () => { repeatNotice.hidden = true; });

  const profileValues = (profile) => {
    const family = selectedGenerationWorkflow()?.dataset.family || "checkpoint";
    const isEdit = templateID.value === "image-to-image";
    const values = {};
    if (family === "minimax_h3") {
      const option = model?.selectedOptions?.[0];
      const integratedTurbo = option?.dataset.videoIntegratedTurbo === "true";
      const externalTurbo = !integratedTurbo && Boolean(miniMaxVideoTurbo?.checked);
      const draftSteps = integratedTurbo ? "6" : externalTurbo ? "4" : "20";
      const balancedSteps = integratedTurbo ? "8" : externalTurbo ? "6" : "25";
      const maximumSteps = integratedTurbo ? "8" : externalTurbo ? "8" : "25";
      Object.assign(values, profile === "draft"
        ? { video_quality: "480", video_duration_seconds: "5", video_steps: draftSteps }
        : profile === "maximum"
          ? { video_quality: "1080", video_duration_seconds: "10", video_steps: maximumSteps, video_reference_size: "max" }
          : { video_quality: "720", video_duration_seconds: "5", video_steps: balancedSteps, video_reference_size: "match" });
    } else if (family === "flux2") {
      Object.assign(values, profile === "draft"
        ? { source_megapixels: "0.75", flux_detailer_steps: "16", flux_guidance: "3.5", flux_upscale_mode: "none" }
        : profile === "maximum"
          ? { source_megapixels: "2", flux_detailer_steps: "32", flux_guidance: "4", flux_upscale_mode: "both" }
          : { source_megapixels: "1", flux_detailer_steps: "25", flux_guidance: "4", flux_upscale_mode: "none" });
    } else if (family === "krea2") {
      if (isEdit) {
        Object.assign(values, profile === "draft"
          ? { output_megapixels: "1", reference_boost: "4", grounding_pixels: "512", upscale_steps: "3", upscale_denoise: "0.14" }
          : profile === "maximum"
            ? { output_megapixels: "4.7", reference_boost: "4", grounding_pixels: "1024", upscale_steps: "8", upscale_denoise: "0.22" }
            : { output_megapixels: "1.9", reference_boost: "4", grounding_pixels: "768", upscale_steps: "5", upscale_denoise: "0.18" });
      } else {
        Object.assign(values, profile === "draft"
          ? { base_megapixels: "0.75", output_megapixels: "1", steps: "6", upscale_steps: "3", detail_steps: "1", detail_denoise: "0.02" }
          : profile === "maximum"
            ? { base_megapixels: "1.5", output_megapixels: "4.7", steps: "10", upscale_steps: "8", detail_steps: "4", detail_denoise: "0.04" }
            : { base_megapixels: "1", output_megapixels: "1.9", steps: "8", upscale_steps: "5", detail_steps: "2", detail_denoise: "0.03" });
      }
    } else {
      Object.assign(values, profile === "draft"
        ? { steps: "18", cfg: "6", width: "768", height: "1024" }
        : profile === "maximum"
          ? { steps: "35", cfg: "7", width: "1024", height: "1536" }
          : { steps: "25", cfg: "7", width: "1024", height: "1024" });
    }
    return values;
  };

  const syncGenerationSummary = () => {
    if (!generationSummaryValue) return;
    const preset = selectedGenerationWorkflow();
    const selectedModel = model?.selectedOptions?.[0];
    if (!preset || !selectedModel?.value) {
      generationSummaryValue.textContent = "Выберите workflow и модель.";
      return;
    }
    const parts = [preset.querySelector("strong")?.textContent || "Workflow", selectedModel.textContent.trim()];
    const family = preset.dataset.family || "";
    if (family === "krea2") {
      const mp = outputMegapixels?.value || quality?.selectedOptions?.[0]?.textContent || "";
      if (mp) parts.push(`${mp} Мп`);
    } else if (family === "minimax_h3") {
      parts.push(`${miniMaxVideoQuality?.value || "720"}p`, `${form.elements.video_duration_seconds?.value || "5"} сек.`);
      if (selectedModel.dataset.videoIntegratedTurbo === "true") parts.push("Turbo встроен");
      else if (miniMaxVideoTurbo?.checked) parts.push("Turbo");
    } else if (isFinite(Number(width?.value)) && isFinite(Number(height?.value))) {
      parts.push(`${width.value} × ${height.value}`);
    }
    const loras = [...root.querySelectorAll(".lora-row:not([hidden]) .generation-lora-select")].filter((item) => !item.disabled && item.value).length;
    if (loras) parts.push(`LoRA: ${loras}`);
    generationSummaryValue.textContent = parts.filter(Boolean).join(" · ");
  };

  const applyGenerationProfile = (profile) => {
    if (!selectedGenerationWorkflow()) return;
    Object.entries(profileValues(profile)).forEach(([name, value]) => setNamedControlValue(name, value));
    root.querySelectorAll("[data-generation-profile]").forEach((button) => button.classList.toggle("is-active", button.dataset.generationProfile === profile));
    if (quality && selectedGenerationWorkflow()?.dataset.family === "krea2" && templateID.value !== "image-to-image") {
      quality.value = profile === "draft" ? "fast" : profile === "maximum" ? "krea-4-7" : "balanced";
    }
    calculateResolution();
    syncMiniMaxVideoProfile();
    syncGenerationSummary();
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
      const clipStrength = row.querySelector(".generation-lora-clip");
      if (select) select.value = "";
      if (strength) strength.value = "0";
      if (clipStrength) clipStrength.value = "1";
    });
  };

  const setPromptAssistantState = (message, state = "") => {
    if (!promptAssistantState) return;
    if (message === "нельзя создавать сексуализированный контент с участием несовершеннолетних или персонажей с неоднозначным возрастом") {
      const title = document.createElement("strong");
      title.textContent = "Запрос не обработан";
      const detail = document.createElement("span");
      detail.textContent = "Нельзя создавать сексуализированный контент с участием несовершеннолетних или персонажей с неопределённым возрастом. Измените описание и попробуйте снова.";
      promptAssistantState.replaceChildren(title, detail);
      promptAssistantState.dataset.state = "safety";
      return;
    }
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
    const isVideo = templateID.value === "minimax-h3-video";
    promptAssistant.hidden = !templateID.value;
    promptAssistantControls.hidden = !promptAssistantEnabled.checked;
	  promptAssistantTemplate.disabled = isVideo;
    [...promptAssistantTemplate.options].forEach((option) => {
      const imageOnly = option.dataset.imageOnly !== undefined;
      const videoOnly = option.dataset.videoOnly !== undefined;
      const miniMaxOnly = option.value === "minimax-h3";
      option.hidden = isVideo ? !miniMaxOnly : (imageOnly && !isEdit) || videoOnly;
      option.disabled = isVideo ? !miniMaxOnly : (imageOnly && !isEdit) || videoOnly;
    });
    const selectedAssistantTemplate = promptAssistantTemplate.selectedOptions[0];
    if ((isVideo && selectedAssistantTemplate?.value !== "minimax-h3") || (!isEdit && selectedAssistantTemplate?.dataset.imageOnly !== undefined) || (!isVideo && selectedAssistantTemplate?.dataset.videoOnly !== undefined)) {
      promptAssistantTemplate.value = isVideo ? "minimax-h3" : "workflow-default";
      resetPromptAssistantReview();
    }
    if (!promptAssistantEnabled.checked) {
      resetPromptAssistantReview();
      setPromptAssistantState("Ассистент выключен. Используется ваш исходный промт.");
    } else if (!promptAssistantReview?.hidden) {
      setPromptAssistantState("Проверьте вариант и примените его либо оставьте свой промт.", "review");
    } else {
      setPromptAssistantState(isVideo && promptAssistantTemplate.value === "minimax-h3"
        ? "Ассистент подготовит один структурированный MiniMax H3 Context-IR для прямого запуска."
        : "Вариант будет создан локальной моделью e4b и затем выгружен из видеопамяти.");
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
    const minimumDimension = isEdit && preserveOriginalSize?.checked ? 16 : 256;
    if (width) width.min = String(minimumDimension);
    if (height) height.min = String(minimumDimension);
    if (generationOpenExact) {
      generationOpenExact.textContent = isMiniMax ? "Параметры видео" : "Точные настройки";
      generationOpenExact.setAttribute("aria-label", isMiniMax ? "Перейти к параметрам видео MiniMax H3" : "Открыть точные настройки workflow");
    }
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
    if (upscaleFactor && isKreaEdit) {
      if (preserveOriginalSize?.checked) {
        if (!upscaleFactor.dataset.restoreValue) upscaleFactor.dataset.restoreValue = upscaleFactor.value || "1.5";
        upscaleFactor.value = "1";
        upscaleFactor.disabled = true;
      } else {
        upscaleFactor.disabled = false;
        if (upscaleFactor.value === "1" && upscaleFactor.dataset.restoreValue) {
          upscaleFactor.value = upscaleFactor.dataset.restoreValue;
          delete upscaleFactor.dataset.restoreValue;
        }
      }
    }
    root.querySelectorAll("[data-workflow-guide]").forEach((guide) => {
      const type = guide.dataset.workflowGuide;
      guide.hidden = !(
        (type === "text" && !isEdit && !isMiniMax) ||
        (type === "krea-edit" && isKreaEdit) ||
        (type === "flux-edit" && isFluxEdit) ||
        (type === "video" && isMiniMax)
      );
    });
    if (workflowGuides) {
      workflowGuides.hidden = ![...workflowGuides.querySelectorAll("[data-workflow-guide]")].some((guide) => !guide.hidden);
    }
    renderLUT();
    setFieldState(".standard-main-settings", !isEdit && !isMiniMax);
    setFieldState(".minimax-video-settings", isMiniMax);
    setFieldState(".minimax-reference-field", isMiniMax && miniMaxMode() === "references");
    const hideNegativePrompt = isMiniMax || isKreaText;
    if (negativePromptField) negativePromptField.hidden = hideNegativePrompt;
    if (negativePrompt) negativePrompt.disabled = hideNegativePrompt;
    if (generationPromptFields) generationPromptFields.classList.toggle("is-single", hideNegativePrompt);
    if (positivePromptLabel) positivePromptLabel.textContent = isMiniMax ? "Промт видео" : isKreaText ? "Промт" : "Позитивный промт";
    if (isMiniMax) {
      syncMiniMaxVideoProfile();
      syncMiniMaxSharpenFields();
    }
    if (isFluxEdit) syncAdaptiveLoraSlots("flux");
    if (isKreaText) syncAdaptiveLoraSlots("krea");
    if (isMiniMax) syncAdaptiveLoraSlots("minimax");
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
    syncGenerationSummary();
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

  model?.addEventListener("change", () => {
    applyQuality();
    if (model.selectedOptions?.[0]?.dataset.family === "minimax_h3") {
      syncMiniMaxVideoProfile({ applyModelDefaults: true });
      syncWorkflowFields();
    }
  });
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
    const selected = selectedGenerationWorkflow();
    const minimumDimension = selected?.dataset.templateId === "image-to-image" && preserveOriginalSize?.checked ? 16 : 256;
    const value = clamp(Number(input.value) || 1024, minimumDimension, 4096);
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
      if (mode === "minimax-h3-video") {
        body.set("video_mode", miniMaxMode());
        body.set("video_duration_seconds", form.elements.video_duration_seconds?.value || "5");
        body.set("video_has_audio", miniMaxAudioIsAvailable() && uploadedAudio ? "true" : "false");
        body.set("video_has_video", miniMaxReferencesAreAvailable() && uploadedVideo ? "true" : "false");
      }
      inputImages.forEach((input, index) => {
        const value = uploadedImages.get(index + 1) || input?.value || "";
        if (value) body.set(index === 0 ? "input_image" : `input_image_${index + 1}`, value);
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
  const handleMiniMaxModeChange = () => {
    const referenceOnly = model?.selectedOptions?.[0]?.dataset.videoReferenceOnly === "true";
    if (referenceOnly && miniMaxMode() !== "references") setMiniMaxMode("references");
    if (miniMaxVideoModeHint) miniMaxVideoModeHint.textContent = miniMaxMode() === "references"
      ? referenceOnly
        ? "Выбрано: Eros Max использует REF2VA. Ролик строится по промту; фото, видео и аудио необязательны."
        : "Выбрано: ролик строится по промту; фото, видео и аудио при наличии используются как свободные референсы."
      : "Выбрано: ролик строится по промту; Фото 1 и Фото 2 при наличии фиксируют точные начало и финал.";
    syncImageSlots();
    syncMiniMaxAudioReference();
    updateWorkflowNext();
    syncWorkflowFields();
  };
  miniMaxVideoModeInputs.forEach((input) => input.addEventListener("change", handleMiniMaxModeChange));

  miniMaxAudioFile?.addEventListener("change", () => {
    const file = miniMaxAudioFile.files?.[0];
    uploadedAudio = "";
    if (inputAudio) inputAudio.value = "";
    if (!file) {
      if (miniMaxAudioPreview) miniMaxAudioPreview.hidden = true;
      updateWorkflowNext();
      return;
    }
    if (file.size > 32 * 1024 * 1024) {
      clearMiniMaxAudio();
      if (miniMaxAudioState) miniMaxAudioState.textContent = "Аудиофайл должен быть не больше 32 МБ";
      if (miniMaxAudioPreview) miniMaxAudioPreview.hidden = false;
      updateWorkflowNext();
      return;
    }
    if (miniMaxAudioName) miniMaxAudioName.textContent = file.name;
    if (miniMaxAudioState) miniMaxAudioState.textContent = "Аудио выбрано. Оно загрузится вместе с фото.";
    if (miniMaxAudioPreview) miniMaxAudioPreview.hidden = false;
    updateWorkflowNext();
  });
  miniMaxAudioRemove?.addEventListener("click", () => {
    clearMiniMaxAudio();
    updateWorkflowNext();
  });
  miniMaxVideoFile?.addEventListener("change", () => {
    const file = miniMaxVideoFile.files?.[0];
    uploadedVideo = "";
    if (inputVideo) inputVideo.value = "";
    if (videoPreviewURL) URL.revokeObjectURL(videoPreviewURL);
    videoPreviewURL = "";
    if (miniMaxVideoPreviewMedia) miniMaxVideoPreviewMedia.removeAttribute("src");
    if (!file) {
      if (miniMaxVideoPreview) miniMaxVideoPreview.hidden = true;
      updateWorkflowNext();
      return;
    }
    if (file.size > 512 * 1024 * 1024) {
      if (miniMaxVideoState) miniMaxVideoState.textContent = "Видеофайл должен быть не больше 512 МБ";
      if (miniMaxVideoPreview) miniMaxVideoPreview.hidden = false;
      updateWorkflowNext();
      return;
    }
    try {
      videoPreviewURL = URL.createObjectURL(file);
      if (miniMaxVideoPreviewMedia) miniMaxVideoPreviewMedia.src = videoPreviewURL;
    } catch (_) {}
    if (miniMaxVideoName) miniMaxVideoName.textContent = file.name;
    if (miniMaxVideoState) miniMaxVideoState.textContent = "Видео выбрано. Оно загрузится перед продолжением.";
    if (miniMaxVideoPreview) miniMaxVideoPreview.hidden = false;
    updateWorkflowNext();
  });
  miniMaxVideoRemove?.addEventListener("click", () => {
    clearMiniMaxVideo();
    updateWorkflowNext();
  });
  miniMaxVideoQuality?.addEventListener("change", () => {
    syncMiniMaxVideoProfile();
    syncGenerationSummary();
  });
  miniMaxVideoTurbo?.addEventListener("change", () => {
    syncMiniMaxVideoProfile();
    syncGenerationSummary();
  });
  [miniMaxVideoAspect, miniMaxUseSourceAspect, miniMaxVideoSwap, form.elements.video_duration_seconds].forEach((control) => control?.addEventListener("change", () => {
    syncMiniMaxVideoProfile();
    syncGenerationSummary();
  }));
  miniMaxVideoSharpenMethod?.addEventListener("change", syncMiniMaxSharpenFields);

  const generationGalleryImages = () => {
    const seen = new Set();
    const images = [];
    savedVariants.forEach((variant) => {
      (variant.media || []).forEach((media) => {
        const id = Number(media.id || 0);
        if (media.media_type !== "image" || id <= 0 || !media.url || seen.has(id)) return;
        seen.add(id);
        images.push({
          id,
          url: media.url,
          name: media.filename || `Изображение ${id}`,
          sensitive: Boolean(media.sensitive),
          modelName: String(variant.model_name || "Сгенерированное изображение").replaceAll("\\", "/").split("/").pop().replace(/\.(safetensors|ckpt|gguf)$/i, ""),
        });
      });
    });
    return images;
  };

  const renderGalleryImagePicker = () => {
    if (!imagePickerGrid || !imagePickerState) return;
    const images = generationGalleryImages();
    imagePickerGrid.replaceChildren();
    if (!images.length) {
      imagePickerState.hidden = false;
      imagePickerState.textContent = variantsLoaded
        ? "В галерее пока нет доступных изображений. Можно загрузить фото с устройства."
        : "Загружаем ваши изображения...";
      return;
    }
    imagePickerState.hidden = true;
    imagePickerGrid.replaceChildren(...images.map((entry) => {
      const card = document.createElement("button");
      card.type = "button";
      card.className = "generation-image-picker-card";
      card.setAttribute("aria-label", `Выбрать ${entry.name}`);
      if (entry.sensitive) {
        card.classList.add("sensitive-media");
        card.dataset.sensitiveMedia = "";
      }
      const image = document.createElement("img");
      image.loading = "lazy";
      image.src = entry.url;
      image.alt = entry.name;
      const details = document.createElement("span");
      const title = document.createElement("strong");
      title.textContent = entry.modelName;
      const filename = document.createElement("small");
      filename.textContent = entry.name;
      details.append(title, filename);
      card.append(image, details);
      if (entry.sensitive) {
        const cover = document.createElement("span");
        cover.className = "sensitive-media-cover";
        const coverTitle = document.createElement("b");
        coverTitle.textContent = "Контент 18+";
        const hint = document.createElement("small");
        hint.textContent = "Нажмите, чтобы показать";
        cover.append(coverTitle, hint);
        card.append(cover);
      }
      card.addEventListener("click", () => {
        if (window.aiGatewaySensitiveContent?.reveal(card)) return;
        if (!galleryPickerSlot) return;
        selectGalleryImage(galleryPickerSlot, entry);
      });
      return card;
    }));
  };

  const closeGalleryImagePicker = () => {
    if (!imagePicker || imagePicker.hidden) return;
    const selectedSlot = galleryPickerSlot;
    imagePicker.hidden = true;
    galleryPickerSlot = null;
    document.body.classList.remove("generation-image-picker-open");
    selectedSlot?.galleryButton?.focus({ preventScroll: true });
  };

  const openGalleryImagePicker = async (item) => {
    if (!imagePicker || !item || !isMiniMaxSelected()) return;
    galleryPickerSlot = item;
    if (imagePickerSlot) imagePickerSlot.textContent = miniMaxMode() === "references" ? `референсе ${item.index}` : item.index === 1 ? "первом кадре" : "последнем кадре";
    imagePicker.hidden = false;
    document.body.classList.add("generation-image-picker-open");
    renderGalleryImagePicker();
    imagePicker.querySelector(".generation-image-picker-close")?.focus({ preventScroll: true });
    try {
      await refreshVariants();
    } catch (error) {
      if (!generationGalleryImages().length && imagePickerState) {
        imagePickerState.hidden = false;
        imagePickerState.textContent = error.message || "Не удалось загрузить галерею. Попробуйте ещё раз.";
      }
    }
  };

  const applyImageSelectionPreview = (item, source, url, stateMessage) => {
    item.slot.classList.add("has-image");
    if (item.index === 1) primaryImageSize = null;
    if (url) previewURLs.set(item.index, url);
    if (item.previewImage) {
      item.previewImage.onload = () => {
        if (item.index !== 1) return;
        primaryImageSize = { width: item.previewImage.naturalWidth, height: item.previewImage.naturalHeight };
        applyOriginalResolution();
        updateOriginalResolution();
        syncSelectedImageSummary();
        syncMiniMaxVideoProfile();
      };
      item.previewImage.onerror = () => {
        if (item.state) item.state.textContent = "Не удалось показать предпросмотр, но фото можно использовать";
      };
      if (url) item.previewImage.src = url;
      else item.previewImage.removeAttribute("src");
    }
    if (item.name) item.name.textContent = source.name;
    if (item.state) item.state.textContent = stateMessage;
    if (item.preview) item.preview.hidden = false;
    syncSelectedImageSummary();
    syncReferenceMap();
    updateWorkflowNext();
  };

  const selectGalleryImage = (item, entry) => {
    const previousURL = previewURLs.get(item.index);
    if (previousURL?.startsWith("blob:")) URL.revokeObjectURL(previousURL);
    previewURLs.delete(item.index);
    selectedImages.delete(item.index);
    gallerySelections.set(item.index, entry);
    uploadedImages.delete(item.index);
    if (item.input) item.input.value = "";
    if (inputImages[item.index - 1]) inputImages[item.index - 1].value = "";
    applyImageSelectionPreview(item, entry, entry.url, "Выбрано из галереи. Подготовим фото для ComfyUI при продолжении.");
    closeGalleryImagePicker();
  };

  const handleImageSelection = (item) => {
    const file = item.input?.files?.[0] || null;
    const previousURL = previewURLs.get(item.index);
    if (previousURL?.startsWith("blob:")) URL.revokeObjectURL(previousURL);
    previewURLs.delete(item.index);
    gallerySelections.delete(item.index);
    uploadedImages.delete(item.index);
    if (inputImages[item.index - 1]) inputImages[item.index - 1].value = "";
    if (!file) {
      clearImageSlot(item);
      updateWorkflowNext();
      return;
    }
    selectedImages.set(item.index, file);
    let url = "";
    try {
      url = URL.createObjectURL(file);
    } catch (_) {}
    applyImageSelectionPreview(
      item,
      file,
      url,
      isMiniMaxSelected() ? "Фото выбрано. Нажмите «Загрузить в ComfyUI и продолжить»." : "Готово к загрузке",
    );
  };

  imageSlots.forEach((item) => {
    item.input?.addEventListener("change", () => handleImageSelection(item));
    item.input?.addEventListener("input", () => handleImageSelection(item));
    item.remove?.addEventListener("click", () => {
      clearImageSlot(item);
      updateWorkflowNext();
    });
    item.galleryButton?.addEventListener("click", () => openGalleryImagePicker(item));
    item.role?.addEventListener("change", () => {
      if (item.index === 1) item.role.value = "base_scene";
      syncReferenceMap();
      if (promptAssistantEnabled?.checked) resetPromptAssistantReview();
    });
  });

  imagePicker?.querySelectorAll("[data-gallery-image-picker-close]").forEach((button) => button.addEventListener("click", closeGalleryImagePicker));
  document.addEventListener("keydown", (event) => { if (event.key === "Escape") closeGalleryImagePicker(); });

  workflowNext?.addEventListener("click", async () => {
    if (!generationWorkflowID.value) return;
    const requiresPrimary = requiresImage;
    const selectedSlots = imageSlots.filter((item) => item.index <= activeMaxInputImages() && hasSelectedImage(item));
    if (!selectedSlots.length && !requiresPrimary && !hasPendingMiniMaxAudio() && !hasPendingMiniMaxVideo()) {
      showStep(3);
      positive?.focus({ preventScroll: true });
      return;
    }
    if ((requiresPrimary && !selectedSlots.some((item) => item.index === 1)) || uploadInFlight) return;
    const pendingSlots = selectedSlots.filter((item) => !uploadedImages.get(item.index));
    const pendingAudio = hasPendingMiniMaxAudio();
    const pendingVideo = hasPendingMiniMaxVideo();
    if (!pendingSlots.length && !pendingAudio && !pendingVideo) {
      showStep(3);
      positive?.focus({ preventScroll: true });
      return;
    }
    uploadInFlight = true;
    workflowNext.disabled = true;
    workflowNext.classList.add("is-loading");
    try {
      for (const item of pendingSlots) {
        const file = selectedImageFile(item);
        const galleryImage = gallerySelections.get(item.index);
        if (!file && !galleryImage) throw new Error("Не удалось прочитать выбранное фото");
        if (item.state) item.state.textContent = galleryImage ? "Подготавливаем фото из галереи..." : "Загружаем в вашу сессию...";
        let response;
        if (galleryImage) {
          const body = new URLSearchParams({ csrf: form.elements.csrf?.value || "", media_id: String(galleryImage.id) });
          response = await fetch("/generate/library/reuse-image", {
            method: "POST",
            headers: { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8" },
            body,
            credentials: "same-origin",
          });
        } else {
          const body = new FormData();
          body.append("image", file, file.name);
          body.append("type", "input");
          body.append("overwrite", "true");
          response = await fetch("/generate/upload/image", { method: "POST", body, credentials: "same-origin" });
        }
        const payload = await response.json().catch(() => ({}));
        if (!response.ok || !payload.name) throw new Error(payload.error || "ComfyUI не принял фото");
        const value = [payload.subfolder, payload.name].filter(Boolean).join("/");
        uploadedImages.set(item.index, value);
        if (inputImages[item.index - 1]) inputImages[item.index - 1].value = value;
        if (item.state) item.state.textContent = "Загружено в вашу сессию";
      }
      if (pendingAudio && miniMaxAudioFile?.files?.[0]) {
        if (miniMaxAudioState) miniMaxAudioState.textContent = "Загружаем аудио в вашу сессию...";
        const body = new FormData();
        const file = miniMaxAudioFile.files[0];
        body.append("image", file, file.name);
        body.append("type", "input");
        body.append("overwrite", "true");
        const response = await fetch("/generate/upload/audio", { method: "POST", body, credentials: "same-origin" });
        const payload = await response.json().catch(() => ({}));
        if (!response.ok || !payload.name) throw new Error(payload.error || "ComfyUI не принял аудиофайл");
        uploadedAudio = [payload.subfolder, payload.name].filter(Boolean).join("/");
        syncMiniMaxAudioReference();
        if (miniMaxAudioState) miniMaxAudioState.textContent = "Загружено в вашу сессию";
      }
      if (pendingVideo && miniMaxVideoFile?.files?.[0]) {
        if (miniMaxVideoState) miniMaxVideoState.textContent = "Загружаем видео в вашу сессию...";
        const body = new FormData();
        const file = miniMaxVideoFile.files[0];
        body.append("image", file, file.name);
        body.append("type", "input");
        body.append("overwrite", "true");
        const response = await fetch("/generate/upload/video", { method: "POST", body, credentials: "same-origin" });
        const payload = await response.json().catch(() => ({}));
        if (!response.ok || !payload.name) throw new Error(payload.error || "ComfyUI не принял видеореференс");
        uploadedVideo = [payload.subfolder, payload.name].filter(Boolean).join("/");
        syncMiniMaxAudioReference();
        if (miniMaxVideoState) miniMaxVideoState.textContent = "Загружено в вашу сессию";
      }
      showStep(3);
      positive?.focus({ preventScroll: true });
    } catch (error) {
      const failed = pendingSlots.find((item) => !uploadedImages.get(item.index));
      if (failed?.state) failed.state.textContent = error.message || "Не удалось загрузить фото";
      if (pendingAudio && miniMaxAudioState && !uploadedAudio) miniMaxAudioState.textContent = error.message || "Не удалось загрузить аудио";
      if (pendingVideo && miniMaxVideoState && !uploadedVideo) miniMaxVideoState.textContent = error.message || "Не удалось загрузить видео";
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
      media.preload = "auto";
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
    if (item.sensitive) card.classList.add("sensitive-media");
    const isVideo = item.media_type === "video";
    const preview = document.createElement("button");
    preview.className = "generation-output-preview";
    if (item.sensitive) preview.dataset.sensitiveMedia = "";
    if (isVideo) {
      preview.type = "button";
      preview.classList.add("generation-video-preview");
      preview.title = "Открыть видеоплеер";
      const video = document.createElement("video");
      video.muted = true;
      video.loop = true;
      video.playsInline = true;
      video.preload = "auto";
      video.src = item.url;
      const play = document.createElement("span");
      play.className = "generation-video-play";
      play.setAttribute("aria-hidden", "true");
      play.textContent = "▶";
      preview.append(video, play);
      if (item.sensitive) {
        const cover = document.createElement("span");
        cover.className = "sensitive-media-cover";
        cover.innerHTML = "<b>Контент 18+</b><small>Нажмите, чтобы показать</small>";
        preview.append(cover);
      }
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
      if (item.sensitive) {
        const cover = document.createElement("span");
        cover.className = "sensitive-media-cover";
        cover.innerHTML = "<b>Контент 18+</b><small>Нажмите, чтобы показать</small>";
        preview.append(cover);
      }
      preview.addEventListener("click", () => {
        if (window.aiGatewaySensitiveContent?.reveal(preview)) return;
        openLightbox({ filename: item.filename, media_type: "image", url: item.url });
      });
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
    if (!document.getElementById("my-results")) return;
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
    button.addEventListener("click", () => {
      if (window.aiGatewaySensitiveContent?.reveal(button)) return;
      openLightbox({
      filename: button.dataset.filename || "Результат генерации",
      media_type: "image",
      url: button.dataset.url,
      });
    });
  });
  root.querySelectorAll("[data-generation-library-video]").forEach((button) => {
    wireVideoPreview(button, { filename: button.dataset.filename || "Видео", media_type: "video", url: button.dataset.url });
  });
  refreshExpiryLabels();
  window.setInterval(refreshExpiryLabels, 30000);

  const buildGenerationPayload = () => {
    const body = new FormData(form);
    const numericFieldNames = new Set([...form.querySelectorAll('input[type="number"], input[data-localized-decimal]')].map((field) => field.name));
    for (const [name, value] of [...body.entries()]) {
      if (numericFieldNames.has(name) && typeof value === "string") body.set(name, value.replaceAll(",", "."));
    }
    body.set("template_id", selectedChoice()?.dataset.workflowId || "");
    body.set("generation_workflow", selectedGenerationWorkflow()?.dataset.presetId || "");
    body.set("assistant_requested", promptAssistantOriginal ? "true" : "false");
    body.set("assistant_applied", promptAssistantAction.startsWith("applied") ? "true" : "false");
    body.set("assistant_template_used", promptAssistantOriginal ? (promptAssistantTemplate?.value || "") : "");
    body.set("assistant_think_used", promptAssistantOriginal && promptAssistantThink?.checked ? "true" : "false");
    body.set("assistant_original_prompt", promptAssistantOriginal);
    body.set("assistant_suggestion", promptAssistantSuggestion);
    ["input_image", "input_image_2", "input_image_3", "input_image_4"].forEach((name, index) => body.set(name, uploadedImages.get(index + 1) || ""));
    body.set("input_audio", miniMaxAudioIsAvailable() ? uploadedAudio : "");
    body.set("input_video", miniMaxReferencesAreAvailable() ? uploadedVideo : "");
    body.set("video_reference_audio", miniMaxReferencesAreAvailable() && uploadedVideo && form.elements.video_reference_audio?.checked ? "true" : "false");
    return new URLSearchParams(body);
  };

  const renderPreflight = (payload) => {
    if (!preflight || !preflightChecks) return;
    const checks = Array.isArray(payload?.checks) ? payload.checks : [];
    preflight.hidden = false;
    preflight.classList.toggle("has-error", !payload?.ok);
    if (preflightSummary) preflightSummary.textContent = payload?.ok ? "Проверка выполнена. Можно ставить задачу в очередь." : "Найдены проблемы, которые нужно исправить до запуска.";
    preflightChecks.replaceChildren(...checks.map((check) => {
      const item = document.createElement("div");
      item.className = `generation-preflight-check ${check.level || "info"}`;
      const label = document.createElement("strong");
      label.textContent = check.label || "Проверка";
      const message = document.createElement("span");
      message.textContent = check.message || "";
      item.append(label, message);
      return item;
    }));
    if (!payload?.ok && payload?.recovery_profile) {
      const recover = document.createElement("button");
      recover.type = "button";
      recover.className = "button ghost generation-preflight-recover";
      recover.textContent = payload.recovery_label || "Вернуть безопасные параметры";
      recover.addEventListener("click", () => {
        applyGenerationProfile(payload.recovery_profile);
        preflightSummary.textContent = "Применён безопасный профиль. Проверьте промт и запустите проверку ещё раз.";
      });
      preflightChecks.append(recover);
    }
    if (payload?.queue) renderQueueOverview(payload.queue);
  };

  const runPreflight = async ({ reveal = true } = {}) => {
    if (!selectedChoice() || !selectedGenerationWorkflow() || !model?.value || !positive.value.trim()) {
      const payload = { ok: false, checks: [{ level: "error", label: "Запуск", message: "Выберите сценарий, workflow, модель и заполните позитивный промт." }] };
      renderPreflight(payload);
      if (reveal) preflight?.scrollIntoView({ block: "center", behavior: "smooth" });
      return false;
    }
    if (preflightButton) preflightButton.disabled = true;
    if (preflightRepeat) preflightRepeat.disabled = true;
    try {
      const response = await fetch("/generate/preflight", {
        method: "POST",
        headers: { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8" },
        body: buildGenerationPayload(),
        credentials: "same-origin",
      });
      const payload = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(payload.error || "Не удалось проверить готовность workflow");
      renderPreflight(payload);
      if (reveal) preflight?.scrollIntoView({ block: "center", behavior: "smooth" });
      return Boolean(payload.ok);
    } catch (error) {
      renderPreflight({ ok: false, checks: [{ level: "error", label: "Проверка", message: error.message || "Gateway временно недоступен" }] });
      return false;
    } finally {
      if (preflightButton) preflightButton.disabled = false;
      if (preflightRepeat) preflightRepeat.disabled = false;
    }
  };

  const renderRecipes = (recipes) => {
    savedRecipes = Array.isArray(recipes) ? recipes : [];
    if (!recipeSelect) return;
    const selected = recipeSelect.value;
    recipeSelect.replaceChildren(new Option("Выберите сохранённый набор", ""));
    savedRecipes.forEach((recipe) => recipeSelect.append(new Option(recipe.name, String(recipe.id))));
    recipeSelect.value = savedRecipes.some((recipe) => String(recipe.id) === selected) ? selected : "";
    if (recipeApply) recipeApply.disabled = !recipeSelect.value;
    if (recipeDelete) recipeDelete.disabled = !recipeSelect.value;
  };

  const refreshRecipes = async () => {
    const response = await fetch("/generate/recipes", { credentials: "same-origin" });
    const payload = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(payload.error || "Не удалось загрузить наборы");
    renderRecipes(payload.recipes || []);
  };

  const setRecipeState = (message, state = "") => {
    if (!recipeState) return;
    recipeState.textContent = message || "";
    recipeState.dataset.state = state;
  };

  const saveRecipe = async () => {
    const name = recipeName?.value.trim() || "";
    if (name.length < 2) {
      setRecipeState("Введите название набора от 2 символов.", "error");
      recipeName?.focus();
      return;
    }
    if (!selectedChoice() || !selectedGenerationWorkflow()) {
      setRecipeState("Сначала выберите сценарий и workflow.", "error");
      return;
    }
    recipeSave.disabled = true;
    setRecipeState("Сохраняем защищённый набор...");
    try {
      const body = buildGenerationPayload();
      body.set("recipe_name", name);
      const response = await fetch("/generate/recipes", { method: "POST", headers: { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8" }, body, credentials: "same-origin" });
      const payload = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(payload.error || "Не удалось сохранить набор");
      await refreshRecipes();
      recipeSelect.value = String(payload.recipe?.id || "");
      if (recipeApply) recipeApply.disabled = !recipeSelect.value;
      if (recipeDelete) recipeDelete.disabled = !recipeSelect.value;
      if (recipeName) recipeName.value = "";
      setRecipeState("Набор сохранён. Фото в него не включаются.", "ready");
    } catch (error) {
      setRecipeState(error.message || "Не удалось сохранить набор", "error");
    } finally {
      recipeSave.disabled = false;
    }
  };

  const applyRecipe = () => {
    const recipe = savedRecipes.find((item) => String(item.id) === recipeSelect?.value);
    if (!recipe) return;
    if (!applySavedValues(recipe.values)) {
      setRecipeState("Этот workflow сейчас недоступен. Проверьте модели и зависимости ComfyUI.", "error");
      return;
    }
    setRecipeState("Набор применён. Для режима с фото заново выберите исходник.", "ready");
  };

  const deleteRecipe = async () => {
    const id = recipeSelect?.value || "";
    if (!id || !recipeDelete) return;
    recipeDelete.disabled = true;
    setRecipeState("Удаляем набор...");
    try {
      const body = new URLSearchParams({ csrf: form.elements.csrf?.value || "", recipe_id: id });
      const response = await fetch("/generate/recipes/delete", { method: "POST", headers: { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8" }, body, credentials: "same-origin" });
      const payload = await response.json().catch(() => ({}));
      if (!response.ok || !payload.deleted) throw new Error(payload.error || "Не удалось удалить набор");
      await refreshRecipes();
      setRecipeState("Набор удалён.", "ready");
    } catch (error) {
      setRecipeState(error.message || "Не удалось удалить набор", "error");
    } finally {
      recipeDelete.disabled = !recipeSelect?.value;
    }
  };

  const refreshVariants = async () => {
    const response = await fetch("/generate/variants", { credentials: "same-origin" });
    const payload = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(payload.error || "Не удалось загрузить историю вариантов");
    savedVariants = Array.isArray(payload.variants) ? payload.variants : [];
    variantsLoaded = true;
    renderVariants();
    if (imagePicker && !imagePicker.hidden) renderGalleryImagePicker();
    restoreRequestedVariant();
  };

  const syncGenerationHistoryVisibility = ({ persist = false } = {}) => {
    if (!variantsContent || !variantsToggle) return;
    variantsContent.hidden = generationHistoryCollapsed;
    variantsToggle.textContent = generationHistoryCollapsed ? "Показать" : "Свернуть";
    variantsToggle.setAttribute("aria-expanded", String(!generationHistoryCollapsed));
    if (!persist) return;
    try { window.localStorage.setItem(generationHistoryCollapsedStorageKey, generationHistoryCollapsed ? "true" : "false"); } catch (_) {}
  };

  const renderVariants = () => {
    if (!variantsSection || !variantList) return;
    const filteredVariants = savedVariants.filter((variant) => (!variantStateFilter?.value || variant.state === variantStateFilter.value) && (!variantTemplateFilter?.value || variant.template_id === variantTemplateFilter.value));
    variantsSection.hidden = savedVariants.length === 0;
    if (variantCount) {
      const count = filteredVariants.length === savedVariants.length ? `${savedVariants.length} вариантов` : `Показано ${filteredVariants.length} из ${savedVariants.length}`;
      variantCount.textContent = `${count} · хранится 24 часа`;
    }
    if (filteredVariants.length === 0) {
      const empty = document.createElement("p");
      empty.className = "generation-variant-empty";
      empty.textContent = "По этим фильтрам вариантов пока нет.";
      variantList.replaceChildren(empty);
      updateCompareButton();
      return;
    }
    variantList.replaceChildren(...filteredVariants.map((variant) => {
      const card = document.createElement("article");
      card.className = "generation-variant";
      card.dataset.state = variant.state || "queued";
      const media = variant.media?.find((item) => item.media_type === "image") || variant.media?.[0];
      if (media?.sensitive) card.classList.add("sensitive-media");
      if (media) {
        const preview = document.createElement("button");
        preview.type = "button";
        preview.className = "generation-variant-preview";
        if (media.sensitive) preview.dataset.sensitiveMedia = "";
        if (media.media_type === "video") {
          const video = document.createElement("video");
          video.muted = true;
          video.loop = true;
          video.playsInline = true;
          video.preload = "auto";
          video.src = media.url;
          preview.append(video);
          wireVideoPreview(preview, media);
        } else {
          const image = document.createElement("img");
          image.loading = "lazy";
          image.src = media.url;
          image.alt = variant.model_name || "Результат варианта";
          preview.append(image);
          preview.addEventListener("click", () => {
            if (window.aiGatewaySensitiveContent?.reveal(preview)) return;
            openLightbox(media);
          });
        }
        if (media.sensitive) {
          const cover = document.createElement("span");
          cover.className = "sensitive-media-cover";
          const title = document.createElement("b");
          title.textContent = "Контент 18+";
          const hint = document.createElement("small");
          hint.textContent = "Нажмите, чтобы показать";
          cover.append(title, hint);
          preview.append(cover);
        }
        card.append(preview);
      }
      const body = document.createElement("div");
      body.className = "generation-variant-body";
      const title = document.createElement("strong");
      const modelName = String(variant.model_name || "Сохранённый вариант").replaceAll("\\", "/").split("/").pop();
      title.textContent = modelName.replace(/\.(safetensors|ckpt|gguf)$/i, "");
      const meta = document.createElement("small");
      const duration = Number(variant.duration_seconds || 0);
      const time = duration > 0 ? ` · ${duration < 60 ? `${duration} сек.` : `${Math.round(duration / 60)} мин.`}` : "";
      meta.textContent = `Seed ${variant.seed} · ${variant.state === "completed" ? "готово" : variant.state === "error" ? "ошибка" : variant.state === "cancelled" ? "отменено" : variant.state === "queued" ? "в очереди" : "в работе"}${time}`;
      const prompt = document.createElement("p");
      prompt.textContent = variant.values?.positive_prompt || "Промт не сохранён";
      const actions = document.createElement("div");
      actions.className = "generation-variant-actions";
      const apply = document.createElement("button");
      apply.type = "button";
      apply.className = "button ghost";
      apply.textContent = "Взять параметры";
      apply.addEventListener("click", () => applySavedValues(variant.values));
      actions.append(apply);
      if (variant.template_id === "text-to-image" && variant.state === "completed") {
        const repeat = document.createElement("button");
        repeat.type = "button";
        repeat.className = "button ghost";
        repeat.textContent = "Повторить с этим seed";
        repeat.addEventListener("click", () => { if (applySavedValues(variant.values)) form.requestSubmit(); });
        actions.append(repeat);
      }
      body.append(title, meta, prompt, actions);
      if (variant.error_message) {
        const error = document.createElement("small");
        error.className = "generation-variant-error";
        error.textContent = variant.error_message;
        body.append(error);
      }
      card.append(body);
      return card;
    }));
  };

  const updateGenerationQuota = (quota) => {
    if (!generationQuota || !quota || typeof quota !== "object") return;
    const assign = (selector, value) => {
      const target = generationQuota.querySelector(selector);
      if (target && Number.isFinite(Number(value))) target.textContent = String(Math.max(0, Number(value)));
    };
    assign("[data-generation-quota-daily-remaining]", quota.daily_remaining);
    assign("[data-generation-quota-daily-limit]", quota.daily_limit);
    assign("[data-generation-quota-total-remaining]", quota.total_remaining);
    assign("[data-generation-quota-total-limit]", quota.total_limit);
  };

  const poll = async (promptID) => {
    const isVideo = selectedGenerationWorkflow()?.dataset.family === "minimax_h3";
    const deadline = Date.now() + (isVideo ? 60 : 20) * 60 * 1000;
    let failedAttempts = 0;
    while (Date.now() < deadline) {
      if (activeGenerationID !== promptID) return;
      try {
        const response = await fetch(`/generate/status?prompt_id=${encodeURIComponent(promptID)}`, { credentials: "same-origin" });
        const payload = await response.json().catch(() => ({}));
        if (activeGenerationID !== promptID) return;
        if (!response.ok) {
          if (response.status >= 400 && response.status < 500) {
            clearActiveGeneration();
            setGenerationActions({ retry: true });
            resultTitle.textContent = "Не удалось восстановить генерацию";
            resultStatus.textContent = payload.error || "Доступ к сохранённой генерации больше недоступен.";
            result.classList.add("has-error");
            return;
          }
          throw new Error(payload.error || "Не удалось получить статус");
        }
        failedAttempts = 0;
        resultStatus.textContent = payload.message || "Проверяем состояние...";
        if (!liveProgressReceived) {
          if (payload.state === "queued") {
            const detail = queuePositionDetail(payload.queue_position, payload.queue_total, payload.estimated_wait_seconds);
            setGenerationProgress("В очереди ComfyUI", detail, null);
          } else if (payload.state === "running") {
            setGenerationProgress("ComfyUI выполняет workflow", "Статус восстанавливается через HTTP", null);
          }
        }
        if (payload.state === "completed") {
          clearActiveGeneration();
          setGenerationActions();
          resultTitle.textContent = "Готово";
          setGenerationProgress("Готово", "Результат подготовлен", 100);
          renderOutputs(payload.outputs || []);
          try { await refreshLibrary(); } catch (_) {}
          try { await refreshVariants(); } catch (_) {}
          result.scrollIntoView({ block: "start", behavior: "smooth" });
          return;
        }
        if (payload.state === "error") {
          clearActiveGeneration();
          setGenerationActions({ retry: true });
          resultTitle.textContent = "Генерация завершилась с ошибкой";
          resultStatus.textContent = payload.message || "ComfyUI завершил генерацию с ошибкой";
          result.classList.add("has-error");
          return;
        }
      } catch (_) {
        failedAttempts += 1;
        if (activeGenerationID !== promptID) return;
        const retryAfter = Math.min(15000, 2000 * failedAttempts);
        setGenerationProgress("Восстанавливаем связь", "Генерация в ComfyUI не отменена", null);
        await pauseWithCountdown(
          retryAfter,
          (seconds) => { resultStatus.textContent = `Связь с Gateway временно потеряна. Повторяем проверку через ${seconds} сек.`; },
          () => activeGenerationID === promptID,
        );
        continue;
      }
      await pause(2000);
    }
    if (activeGenerationID === promptID) {
      resultTitle.textContent = "Генерация всё ещё выполняется";
      resultStatus.textContent = "Статус сохранён. Откройте страницу позже: Gateway автоматически продолжит проверку результата.";
      setGenerationActions({ cancel: true });
    }
  };

  const monitorGeneration = async (promptID) => {
    if (!promptID) return false;
    activeGenerationID = promptID;
    persistActiveGeneration();
    setGenerationActions({ cancel: true });
    refreshQueueOverview();
    connectProgressSocket(promptID);
    try {
      await poll(promptID);
    } finally {
      closeProgressSocket();
    }
    return true;
  };

  const recoverGeneration = async (requestID, { attempts = 24 } = {}) => {
    if (!requestID) return false;
    activeGenerationRequestID = requestID;
    persistActiveGeneration();
    result.hidden = false;
    runProgress.hidden = false;
    result.classList.remove("has-error");
    resultTitle.textContent = "Восстанавливаем генерацию";
    setGenerationActions({ cancel: Boolean(activeGenerationID) });
    for (let attempt = 0; attempt < attempts; attempt += 1) {
      try {
        const response = await fetch(`/generate/recover?request_id=${encodeURIComponent(requestID)}`, { credentials: "same-origin" });
        const payload = await response.json().catch(() => ({}));
        if (response.status === 404) {
          clearActiveGeneration();
          setGenerationActions({ retry: true });
          resultTitle.textContent = "Запуск не был подтверждён";
          resultStatus.textContent = "Задача не была поставлена в очередь. Её можно запустить ещё раз.";
          result.classList.add("has-error");
          return false;
        }
        if (!response.ok) throw new Error(payload.error || "Не удалось восстановить запуск");
        if (payload.prompt_id) {
          activeGenerationID = payload.prompt_id;
          activeGenerationRequestID = payload.request_id || requestID;
          persistActiveGeneration();
          resultTitle.textContent = "Генерация выполняется";
          resultStatus.textContent = payload.message || "Генерация восстановлена.";
          if (payload.state === "queued") {
            setGenerationProgress("В очереди ComfyUI", queuePositionDetail(payload.queue_position, payload.queue_total, payload.estimated_wait_seconds), null);
          } else if (payload.state === "running") {
            setGenerationProgress("ComfyUI выполняет workflow", "Статус восстанавливается через HTTP", null);
          }
          await monitorGeneration(payload.prompt_id);
          return true;
        }
      } catch (_) {
        // The request id remains in local storage, so a page reload can continue recovery.
      }
      const retryAfter = Math.min(15000, 1500 * (attempt + 1));
      setGenerationProgress("Восстанавливаем запуск", "ComfyUI не получит дубликат задачи", null);
      await pauseWithCountdown(
        retryAfter,
        (seconds) => { resultStatus.textContent = `Подтверждаем запуск в Gateway. Повторяем через ${seconds} сек.`; },
        () => activeGenerationRequestID === requestID,
      );
    }
    resultTitle.textContent = "Статус запуска ещё уточняется";
    resultStatus.textContent = "Попробуем снова автоматически после обновления страницы. Повторная отправка не нужна.";
    setGenerationActions();
    return false;
  };

  root.querySelectorAll("[data-generation-profile]").forEach((button) => {
    button.addEventListener("click", () => applyGenerationProfile(button.dataset.generationProfile));
  });
  variantStateFilter?.addEventListener("change", renderVariants);
  variantTemplateFilter?.addEventListener("change", renderVariants);
  try { generationHistoryCollapsed = window.localStorage.getItem(generationHistoryCollapsedStorageKey) === "true"; } catch (_) {}
  syncGenerationHistoryVisibility();
  variantsToggle?.addEventListener("click", () => {
    generationHistoryCollapsed = !generationHistoryCollapsed;
    syncGenerationHistoryVisibility({ persist: true });
  });
  generationOpenExact?.addEventListener("click", () => {
    if (!generationAdvanced) return;
    generationAdvanced.open = true;
    generationAdvanced.scrollIntoView({ block: "start", behavior: "smooth" });
    if (isMiniMaxSelected()) {
      generationAdvanced.querySelector(".minimax-video-settings select, .minimax-video-settings input")?.focus({ preventScroll: true });
    }
  });
  form.addEventListener("input", syncGenerationSummary);
  form.addEventListener("change", syncGenerationSummary);
  recipeSelect?.addEventListener("change", () => { if (recipeApply) recipeApply.disabled = !recipeSelect.value; if (recipeDelete) recipeDelete.disabled = !recipeSelect.value; });
  recipeApply?.addEventListener("click", applyRecipe);
  recipeDelete?.addEventListener("click", deleteRecipe);
  recipeSave?.addEventListener("click", saveRecipe);
  preflightButton?.addEventListener("click", () => runPreflight());
  preflightRepeat?.addEventListener("click", () => runPreflight({ reveal: false }));
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
      clearActiveGeneration();
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
    if (activeGenerationID || activeGenerationRequestID) {
      result.hidden = false;
      resultTitle.textContent = "Генерация уже выполняется";
      resultStatus.textContent = "Сначала дождитесь результата или отмените текущую задачу.";
      return;
    }
    if (promptAssistantEnabled?.checked && !promptAssistantApproved) {
      setPromptAssistantState("Перед генерацией подтвердите вариант ассистента или выберите «Оставить мой промт».", "error");
      promptAssistant?.scrollIntoView({ block: "center", behavior: "smooth" });
      return;
    }
    if (!await runPreflight({ reveal: false })) {
      preflight?.scrollIntoView({ block: "center", behavior: "smooth" });
      return;
    }
    const submit = document.getElementById("generation-submit");
    submit.disabled = true;
    submit.classList.add("is-loading");
    result.hidden = false;
    activeGenerationRequestID = newGenerationRequestID();
    activeGenerationID = "";
    persistActiveGeneration();
    setGenerationActions();
    resultTitle.textContent = "Генерация выполняется";
    resultStatus.textContent = "Ставим задачу в очередь ComfyUI...";
    outputGrid.replaceChildren();
    result.classList.remove("has-error");
    runProgress.hidden = false;
    setGenerationProgress("Подготовка", "Проверяем параметры схемы генерации", null);
    result.scrollIntoView({ block: "start", behavior: "smooth" });
    try {
      const body = buildGenerationPayload();
      body.set("client_request_id", activeGenerationRequestID);
      const response = await fetch("/generate/run", { method: "POST", headers: { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8" }, body, credentials: "same-origin" });
      const payload = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(payload.error || "Не удалось запустить генерацию");
      updateGenerationQuota(payload.quota);
      activeGenerationRequestID = payload.request_id || activeGenerationRequestID;
      if (!payload.prompt_id) {
        await recoverGeneration(activeGenerationRequestID);
        return;
      }
      activeGenerationID = payload.prompt_id;
      persistActiveGeneration();
      if (payload.state === "queued") {
        setGenerationProgress("В очереди ComfyUI", queuePositionDetail(payload.queue_position, payload.queue_total, payload.estimated_wait_seconds), null);
      } else if (payload.state === "running") {
        setGenerationProgress("ComfyUI начал генерацию", "Подготавливаем workflow", null);
      }
      if (payload.mining_paused) {
        resultStatus.textContent = "Майнинг подтверждённо остановлен на время этой приоритетной генерации.";
      } else if (payload.mining_warning) {
        resultStatus.textContent = payload.mining_warning;
      }
      await monitorGeneration(payload.prompt_id);
    } catch (error) {
      const recovered = await recoverGeneration(activeGenerationRequestID, { attempts: 8 });
      if (!recovered && !activeGenerationRequestID) {
        setGenerationActions({ retry: true });
        resultTitle.textContent = "Не удалось выполнить генерацию";
        resultStatus.textContent = error.message || "Неизвестная ошибка";
        result.classList.add("has-error");
      }
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
  syncMiniMaxSharpenFields();
  syncPromptAssistant();
  refreshQueueOverview();
  refreshRecipes().catch((error) => setRecipeState(error.message || "Не удалось загрузить наборы", "error"));
  refreshVariants().catch(() => {
    if (requestedVariantID && !requestedVariantHandled) showRepeatNotice("Не удалось загрузить вариант", "Проверьте соединение и обновите страницу.", true);
  });
  const savedGeneration = storedActiveGeneration();
  if (savedGeneration?.requestID) {
    activeGenerationRequestID = savedGeneration.requestID;
    activeGenerationID = savedGeneration.promptID || "";
    if (activeGenerationID) {
      result.hidden = false;
      runProgress.hidden = false;
      resultTitle.textContent = "Восстанавливаем генерацию";
      resultStatus.textContent = "Проверяем сохранённую задачу в Gateway...";
      monitorGeneration(activeGenerationID);
    } else {
      recoverGeneration(activeGenerationRequestID);
    }
  }
  window.setInterval(refreshQueueOverview, 5000);
  window.setInterval(() => refreshVariants().catch(() => {}), 15000);
})();
