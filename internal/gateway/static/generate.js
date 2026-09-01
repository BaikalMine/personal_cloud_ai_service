(() => {
  const root = document.querySelector("[data-comfy-generation]");
  if (!root) return;

  const generationModules = window.AIGatewayGeneration || {};
  root.dataset.generationClient = "modular";
  root.dataset.generationModules = Object.keys(generationModules).sort().join(",");
  const generationStore = generationModules.store?.createStore?.() || null;
  const createStateSlice = (name, api, fallbackState) => {
    let localState = api?.createState?.() || { ...fallbackState };
    generationStore?.setSlice?.(name, localState, `${name}:ready`);
    return {
      get: () => generationStore?.getSlice?.(name) || localState,
      dispatch: (action, fallbackReduce = (state) => state) => {
        const current = generationStore?.getSlice?.(name) || localState;
        localState = api?.reduce?.(current, action) || fallbackReduce(current, action);
        generationStore?.setSlice?.(name, localState, `${name}:change`);
        return localState;
      },
    };
  };
  const wizardSlice = createStateSlice("wizard", generationModules.wizard, {
    step: 1, scenarioID: "", workflowID: "", requiresImage: false, allowsImages: false,
    workflowAvailable: false, uploadInFlight: false, selectedCount: 0, primarySelected: false, pendingUploads: 0,
  });
  const mediaSlice = createStateSlice("media", generationModules.media, { sources: {}, uploaded: {}, uploading: false, error: "" });
  const videoSlice = createStateSlice("video", generationModules.video, { mode: "frames", profileID: "regular" });
  const assistantSlice = createStateSlice("assistant", generationModules.assistant, {
    status: "idle", approved: false, original: "", suggestion: "", action: "", correlationID: "", error: "",
  });
  const jobSlice = createStateSlice("job", generationModules.job, { items: [], revision: 0, live: false, loading: false, activeID: "", error: "" });
  const recipeSlice = createStateSlice("recipes", generationModules.recipes, { items: [], selectedID: "", loading: false, message: "", status: "" });
  const historySlice = createStateSlice("history", generationModules.history, { variants: [], collapsed: false, stateFilter: "", templateFilter: "" });

  const generationRetentionLabel = root.dataset.generationRetention || "24 часа";
  const mediaRetentionLabel = root.dataset.mediaRetention || generationRetentionLabel;

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
  const generationModelField = document.getElementById("generation-model-field");
  const imageSourceFields = document.getElementById("image-source-fields");
  const imageSourceGrid = root.querySelector(".source-image-grid");
  const miniMaxVideoMode = document.getElementById("minimax-video-mode");
  const miniMaxVideoModeInputs = [...root.querySelectorAll('input[name="video_mode"]')];
  const miniMaxVideoModeSelect = form.elements.video_mode;
  const miniMaxVideoModeHint = document.getElementById("minimax-video-mode-hint");
  const generationModeGuide = document.getElementById("generation-mode-guide");
  const generationModeGuideEyebrow = document.getElementById("generation-mode-guide-eyebrow");
  const generationModeGuideTitle = document.getElementById("generation-mode-guide-title");
  const generationModeGuideDescription = document.getElementById("generation-mode-guide-description");
  const generationModeGuideFacts = document.getElementById("generation-mode-guide-facts");
  const generationModeGuideAdvice = document.getElementById("generation-mode-guide-advice");
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
  const generationSummaryTitle = document.getElementById("generation-summary-title");
  const generationSummaryFacts = document.getElementById("generation-summary-facts");
  const generationSummaryImpact = document.getElementById("generation-summary-impact");
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
  const imagePickerRefresh = document.getElementById("generation-image-picker-refresh");
  const fieldHelps = [...root.querySelectorAll(".field-help[data-tooltip]")];
  const panels = [...root.querySelectorAll("[data-step]")];
  const progress = [...root.querySelectorAll("[data-progress]")];
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
  let activeGenerationID = "";
  let activeGenerationRequestID = "";
  let pendingParentJobID = "";
  let generationJobEvents = null;
  let galleryPickerSlot = null;
  let galleryPickerImages = [];
  let galleryPickerImagesLoaded = false;
  let galleryPickerImagesLoading = false;
  const requestedVariantID = new URLSearchParams(window.location.search).get("variant") || "";
  let requestedVariantHandled = false;
  const activeGenerationStorageKey = "ai-gateway.active-generation";
  const generationHistoryCollapsedStorageKey = "ai-gateway.generation-history-collapsed";

  const clamp = (value, min, max) => Math.min(max, Math.max(min, value));
  const selectedImageFile = (item) => selectedImages.get(item?.index) || item?.input?.files?.[0] || null;
  const selectedImageSource = (item) => generationModules.media?.sourceFrom?.(
    selectedImageFile(item),
    gallerySelections.get(item?.index),
  ) || selectedImageFile(item) || gallerySelections.get(item?.index) || null;
  const hasSelectedImage = (item) => Boolean(selectedImageSource(item));
  const workflowManifestsByID = new Map();
  const referenceRoleLabels = {
    first_frame: "Точный первый кадр",
    last_frame: "Точный последний кадр",
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
  const workflowControls = (name) => {
    const control = form?.elements?.namedItem(name);
    if (!control) return [];
    if (typeof RadioNodeList !== "undefined" && control instanceof RadioNodeList) return [...control];
    return [control];
  };
  const selectedWorkflowManifest = () => workflowManifestsByID.get(generationWorkflowID?.value || "") || workflowManifestsByID.get(templateID?.value || "") || null;
  const applyWorkflowCapabilityConstraints = () => {
    const manifest = selectedWorkflowManifest();
    if (!manifest) return;
    (manifest.parameters || []).forEach((parameter) => {
      workflowControls(parameter.name).forEach((control) => {
        control.dataset.workflowParameter = parameter.name;
        if (parameter.minimum !== undefined && "min" in control) control.min = String(parameter.minimum);
        if (parameter.maximum !== undefined && "max" in control) control.max = String(parameter.maximum);
        if (parameter.step !== undefined && "step" in control) control.step = String(parameter.step);
        if (parameter.max_length && "maxLength" in control) control.maxLength = Number(parameter.max_length);
      });
    });
    const imageInput = (manifest.inputs || []).find((input) => input.kind === "image");
    if (!imageInput?.roles?.length) return;
    imageInput.roles.forEach((role) => { referenceRoleLabels[role.id] = role.name; });
    imageSlots.forEach((item) => {
      if (!item.role) return;
      [...item.role.options].forEach((option) => {
        const role = imageInput.roles.find((candidate) => candidate.id === option.value);
        if (role) option.textContent = role.name;
      });
    });
  };
  const loadWorkflowCapabilities = async () => {
    const endpoint = root.dataset.workflowCapabilitiesUrl;
    if (!endpoint) return;
    try {
      const response = await fetch(endpoint, { headers: { Accept: "application/json" }, credentials: "same-origin" });
      if (!response.ok) return;
      const catalog = await response.json();
      if (Number(catalog.schema_version) !== 1 || !Array.isArray(catalog.workflows)) return;
      catalog.workflows.forEach((manifest) => {
        if (manifest?.id) workflowManifestsByID.set(manifest.id, manifest);
        if (manifest?.template_id) workflowManifestsByID.set(manifest.template_id, manifest);
      });
      applyWorkflowCapabilityConstraints();
      syncMiniMaxVideoModelRules(false);
    } catch (_) {
      // The embedded form remains a functional fallback while Gateway reconnects.
    }
  };
  const miniMaxMode = () => generationModules.video?.normalizeMode?.(miniMaxVideoModeSelect?.value) || miniMaxVideoModeSelect?.value || "frames";
  const setMiniMaxMode = (value) => {
    const target = miniMaxVideoModeInputs.find((input) => input.value === value && !input.disabled);
    if (target) {
      target.checked = true;
      videoSlice.dispatch({ type: "SET_MODE", mode: target.value }, (state) => ({ ...state, mode: target.value }));
    }
  };
  const miniMaxAspectDimensions = () => {
    const dimensions = generationModules.video?.dimensionsForAspect?.({
      sourceSize: primaryImageSize,
      aspect: miniMaxVideoAspect?.value || "9:16",
      swap: Boolean(miniMaxVideoSwap?.checked),
    });
    if (dimensions) return dimensions;
    const fallback = primaryImageSize ? [primaryImageSize.width, primaryImageSize.height] : [1080, 1920];
    return miniMaxVideoSwap?.checked ? [fallback[1], fallback[0]] : fallback;
  };
  const syncMiniMaxVideoProfile = ({ applyModelDefaults = false } = {}) => {
    const option = model?.selectedOptions?.[0];
    const integratedTurbo = option?.dataset.videoIntegratedTurbo === "true";
    const referenceOnly = option?.dataset.videoReferenceOnly === "true";
    const turbo = !integratedTurbo && !applyModelDefaults && Boolean(miniMaxVideoTurbo?.checked);
    const manifest = workflowManifestsByID.get("minimax-h3-video");
    const profileID = generationModules.video?.profileID?.({ integratedTurbo, turbo }) || (integratedTurbo ? "integrated_turbo" : turbo ? "turbo" : "regular");
    videoSlice.dispatch({ type: "SET_PROFILE", profileID }, (state) => ({ ...state, profileID }));
    const profile = generationModules.video?.findProfile?.(manifest, profileID) || manifest?.quality_profiles?.find((candidate) => candidate.id === profileID);
    const profileRule = (name) => profile?.parameters?.[name] || null;
    const profileValue = (name, fallback) => profileRule(name)?.value ?? fallback;
    const frameOption = miniMaxVideoModeInputs.find((input) => input.value === "frames");
    if (frameOption) frameOption.disabled = referenceOnly;
    if (referenceOnly && miniMaxMode() !== "references") {
      setMiniMaxMode("references");
      syncImageSlots();
      syncMiniMaxAudioReference();
    }
    if (applyModelDefaults && option?.value) {
      if (miniMaxVideoSteps) miniMaxVideoSteps.value = option.dataset.defaultSteps || String(profileValue("video_steps", 25));
      if (miniMaxVideoSampler) miniMaxVideoSampler.value = option.dataset.defaultSampler || String(profileValue("video_sampler", "euler"));
      if (miniMaxVideoScheduler) miniMaxVideoScheduler.value = option.dataset.defaultScheduler || String(profileValue("video_scheduler", "simple"));
      if (miniMaxVideoShiftVideo) miniMaxVideoShiftVideo.value = option.dataset.defaultVideoShift || String(profileValue("video_shift_video", 11));
      if (miniMaxVideoShiftAudio) miniMaxVideoShiftAudio.value = option.dataset.defaultAudioShift || String(profileValue("video_shift_audio", 3));
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
      ? "Eros Max работает только со свободными референсами и уже содержит Turbo. Gateway применит подходящие параметры автоматически."
      : "MiniMax H3 v4 поддерживает текст, точные кадры и свободные референсы. Turbo остаётся опциональным.";
    if (miniMaxVideoSteps) {
      miniMaxVideoSteps.min = String(profileRule("video_steps")?.minimum ?? (integratedTurbo ? 6 : turbo ? 4 : 20));
      miniMaxVideoSteps.max = String(profileRule("video_steps")?.maximum ?? (integratedTurbo || turbo ? 8 : 25));
      const current = Number(miniMaxVideoSteps.value);
      if (!Number.isFinite(current) || current < Number(miniMaxVideoSteps.min) || current > Number(miniMaxVideoSteps.max)) {
        miniMaxVideoSteps.value = String(profileValue("video_steps", integratedTurbo ? 8 : turbo ? 6 : 25));
      }
    }
    if (miniMaxVideoSampler) {
      const samplerLocked = profileRule("video_sampler")?.locked ?? (integratedTurbo || turbo);
      if (samplerLocked) miniMaxVideoSampler.value = String(profileValue("video_sampler", "euler"));
      miniMaxVideoSampler.disabled = samplerLocked;
    }
    if (miniMaxVideoModeHint && referenceOnly) {
      miniMaxVideoModeHint.textContent = "Выбрано: Eros Max работает со свободными референсами. Фото, видео и аудио необязательны.";
    }
    if (!miniMaxVideoResolutionPreview) return;
    const quality = Number(miniMaxVideoQuality?.value);
    const calculated = generationModules.video?.scaledResolution?.({
      sourceSize: primaryImageSize,
      aspect: miniMaxVideoAspect?.value || "9:16",
      swap: Boolean(miniMaxVideoSwap?.checked),
      maxResolution: quality,
    });
    const [sourceWidth, sourceHeight] = miniMaxAspectDimensions();
    const scale = Math.min(1, quality / Math.max(1, sourceWidth, sourceHeight));
    const multiple = (value) => Math.max(32, Math.floor(value / 32) * 32);
    const targetWidth = calculated?.width || multiple(sourceWidth * scale);
    const targetHeight = calculated?.height || multiple(sourceHeight * scale);
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
    const wizard = wizardSlice.get();
    const visible = Boolean((wizard.requiresImage || wizard.allowsImages) && source && previewURL);
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

  const lightboxController = generationModules.lightbox?.createController?.({
    elements: {
      root: lightbox,
      image: lightboxImage,
      video: lightboxVideo,
      name: lightboxName,
      download: lightboxDownload,
    },
    documentRef: document,
    windowRef: window,
    store: generationStore,
    sensitiveContent: { reveal: (button) => window.aiGatewaySensitiveContent?.reveal?.(button) },
  }) || null;
  const closeLightbox = () => lightboxController?.close?.();

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

  const downloadURL = (outputURL) => generationModules.lightbox?.downloadURL?.(outputURL, window.location.origin) || (() => {
    const url = new URL(outputURL, window.location.origin);
    url.searchParams.set("download", "1");
    return url.pathname + url.search;
  })();

  const openLightbox = (output) => {
    if (lightboxController) {
      lightboxController.open(output);
      return;
    }
    if (output?.url) window.location.assign(output.url);
  };

  lightbox?.querySelectorAll("[data-lightbox-close]").forEach((button) => button.addEventListener("click", closeLightbox));
  lightboxImage?.addEventListener("click", closeLightbox);
  document.addEventListener("keydown", (event) => { if (event.key === "Escape") closeLightbox(); });
  fieldHelps.forEach((help) => {
    help.setAttribute("role", "button");
    help.setAttribute("aria-expanded", "false");
    const tooltip = help.dataset.tooltip?.trim();
    if (tooltip) help.setAttribute("aria-label", `Подсказка: ${tooltip}`);
    help.addEventListener("mouseenter", () => positionFieldHelp(help));
    help.addEventListener("focus", () => positionFieldHelp(help));
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
    if (lightboxController) {
      lightboxController.wireVideoPreview(button, output);
      return;
    }
    button?.addEventListener("click", () => openLightbox(output));
  };

  const showStep = (step) => {
    const next = wizardSlice.dispatch({ type: "SHOW_STEP", step }, (state) => ({ ...state, step: Math.max(1, Math.min(3, Number(step) || 1)) }));
    step = next.step;
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
  const isMiniMaxSelected = () => selectedGenerationWorkflow()?.dataset.family === "minimax_h3" || templateID.value === "minimax-h3-video";
  const maxInputImages = () => Math.max(1, Number(selectedGenerationWorkflow()?.dataset.maxInputImages || (templateID.value === "minimax-h3-video" ? 4 : 1)));
  const activeMaxInputImages = () => generationModules.video?.activeImageLimit?.({
    isMiniMax: isMiniMaxSelected(),
    mode: miniMaxMode(),
    maxInputImages: maxInputImages(),
  }) || (isMiniMaxSelected() && miniMaxMode() === "frames" ? Math.min(2, maxInputImages()) : maxInputImages());

  const miniMaxReferencesAreAvailable = () => generationModules.video?.referencesAvailable?.({
    isMiniMax: isMiniMaxSelected(),
    mode: miniMaxMode(),
  }) ?? (isMiniMaxSelected() && miniMaxMode() === "references");
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

  const selectedReferenceMetadata = () => imageSlots
    .filter((item) => item.index <= activeMaxInputImages() && (hasSelectedImage(item) || uploadedImages.get(item.index)))
    .map((item) => generationModules.media?.referenceMetadata?.({
      templateID: templateID.value,
      videoMode: miniMaxMode(),
      slot: item.index,
      source: mediaSlice.get().sources?.[String(item.index)] || selectedImageSource(item),
      role: item.role?.value || "",
      uploaded: uploadedImages.get(item.index) || inputImages[item.index - 1]?.value || "",
    }) || ({
      number: item.index,
      role: templateID.value === "minimax-h3-video" && miniMaxMode() !== "references"
        ? (item.index === 1 ? "first_frame" : "last_frame")
        : item.index === 1 ? "base_scene" : (item.role?.value || "details"),
      source: "unknown",
      sourceID: "",
      sourceName: selectedImageSource(item)?.name || uploadedImages.get(item.index) || "",
    }))
    .filter(Boolean);

  const promptAssistantReferences = () => {
    if (templateID.value !== "image-to-image" && !(isMiniMaxSelected() && miniMaxMode() === "references")) return [];
    return selectedReferenceMetadata();
  };

  const syncReferenceMap = () => {
    if (!referenceMap || !referenceMapList) return;
    const references = promptAssistantReferences();
    referenceMap.hidden = references.length === 0;
    referenceMapList.replaceChildren();
    references.forEach((reference) => {
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
    mediaSlice.dispatch({ type: "CLEAR_SOURCE", slot: item.index }, (state) => {
      const sources = { ...state.sources };
      const uploaded = { ...state.uploaded };
      delete sources[String(item.index)];
      delete uploaded[String(item.index)];
      return { ...state, sources, uploaded };
    });
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
    const wizard = wizardSlice.get();
    const maximum = (wizard.requiresImage || wizard.allowsImages) ? activeMaxInputImages() : 0;
    const isKrea = selectedGenerationWorkflow()?.dataset.family === "krea2";
    if (imageSourceGrid) imageSourceGrid.dataset.visibleSlots = String(maximum);
    imageSlots.forEach((item) => {
      const visible = item.index <= maximum;
      item.slot.hidden = !visible;
      if (!visible) clearImageSlot(item);
      if (item.galleryChoice) item.galleryChoice.hidden = !(wizard.requiresImage || wizard.allowsImages);
      if (!item.label) return;
      if (isMiniMax && referenceMode) item.label.textContent = `Референс ${item.index} · необязательно`;
      else if (isMiniMax) item.label.textContent = item.index === 1 ? "Первый кадр · необязательно" : "Последний кадр · необязательно";
      else if (item.index === 1) item.label.textContent = "Фото 1 · обязательно";
      else item.label.textContent = isKrea && item.index === 2 ? "Фото 2 · дополнительное" : `Фото ${item.index} · референс`;
      const roleControl = item.role?.closest(".image-reference-role");
      if (roleControl) roleControl.hidden = !(isImageEdit || (isMiniMax && referenceMode));
      if (item.role) {
        const locksBaseScene = isImageEdit && item.index === 1;
        if (locksBaseScene) item.role.value = "base_scene";
        item.role.disabled = !(isImageEdit || (isMiniMax && referenceMode)) || locksBaseScene;
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
        ? "Фото 1 задаёт базовую сцену. Для каждого дополнительного фото выберите роль, чтобы ассистент использовал референсы без путаницы."
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
    const requiresImage = button.dataset.requiresImage === "true";
    const allowsImages = button.dataset.allowsImages === "true";
    wizardSlice.dispatch({
      type: "SELECT_SCENARIO",
      scenarioID: button.dataset.workflowId,
      requiresImage,
      allowsImages,
    }, (state) => ({ ...state, step: 2, scenarioID: button.dataset.workflowId || "", workflowID: "", requiresImage, allowsImages, workflowAvailable: false }));
    generationWorkflowID.value = "";
    if (model) model.value = "";
    root.querySelectorAll(".generation-workflow-choice").forEach((item) => item.classList.remove("is-selected"));
    updateWorkflowCompatibility();
    const compatibleWorkflows = [...root.querySelectorAll(".generation-workflow-choice")].filter((item) => !item.hidden && !item.disabled);
    if (compatibleWorkflows.length === 1) chooseGenerationWorkflow(compatibleWorkflows[0]);
    showStep(2);
  };

  const updateWorkflowNext = () => {
    if (!workflowNext) return;
    const selected = selectedGenerationWorkflow();
    const hasWorkflow = Boolean(selected && selected.dataset.available === "true");
    const selectedModel = model?.selectedOptions?.[0];
    const hasModel = Boolean(model?.value && selectedModel && !selectedModel.disabled);
    const primary = imageSlots[0];
    const hasImage = Boolean(hasSelectedImage(primary) || uploadedImages.get(1));
    const needsImage = wizardSlice.get().requiresImage;
    const hasPendingUploads = imageSlots.some((item) => (
      item.index <= activeMaxInputImages()
      && hasSelectedImage(item)
      && !uploadedImages.get(item.index)
    )) || hasPendingMiniMaxAudio() || hasPendingMiniMaxVideo();
    const wizard = wizardSlice.dispatch({
      type: "SET_SELECTIONS",
      selectedCount: imageSlots.filter((item) => item.index <= activeMaxInputImages() && hasSelectedImage(item)).length,
      primarySelected: hasImage,
      pendingUploads: hasPendingUploads ? 1 : 0,
    }, (state) => ({ ...state, selectedCount: hasImage ? 1 : 0, primarySelected: hasImage, pendingUploads: hasPendingUploads ? 1 : 0 }));
    const canContinue = generationModules.wizard?.canContinue?.({ ...wizard, workflowAvailable: hasWorkflow && hasModel }) ?? (hasWorkflow && hasModel && (!needsImage || hasImage));
    workflowNext.disabled = !canContinue;
    if (!needsImage && !hasPendingUploads) {
      workflowNext.textContent = "Продолжить";
    } else if (hasPendingUploads) {
      workflowNext.textContent = "Загрузить в ComfyUI и продолжить";
    } else {
      workflowNext.textContent = "Продолжить к промту";
    }
    syncGenerationSummary();
  };

  const updateWorkflowCompatibility = () => {
    let visible = 0;
    root.querySelectorAll(".generation-workflow-choice").forEach((item) => {
      const matches = item.dataset.templateId === templateID.value;
      item.hidden = !matches;
      if (matches) visible += 1;
    });
    if (miniMaxVideoMode) miniMaxVideoMode.hidden = templateID.value !== "minimax-h3-video";
    if (generationModelField) generationModelField.hidden = !selectedGenerationWorkflow();
    const wizard = wizardSlice.get();
    if (imageSourceFields) imageSourceFields.hidden = !(wizard.requiresImage || wizard.allowsImages);
    if (workflowNote) workflowNote.hidden = visible > 0;
    syncImageSlots();
    updateWorkflowNext();
  };

  const chooseGenerationWorkflow = (button) => {
    if (button.disabled || button.hidden) return;
    root.querySelectorAll(".generation-workflow-choice").forEach((item) => item.classList.remove("is-selected"));
    button.classList.add("is-selected");
    generationWorkflowID.value = button.dataset.presetId;
    wizardSlice.dispatch({ type: "SELECT_WORKFLOW", workflowID: button.dataset.presetId, available: button.dataset.available === "true" }, (state) => ({
      ...state,
      workflowID: button.dataset.presetId || "",
      workflowAvailable: button.dataset.available === "true",
    }));
    updateQuickModelOptions(button);
    if (generationModelField) generationModelField.hidden = false;
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
    const variant = historySlice.get().variants.find((item) => String(item.id) === requestedVariantID);
    if (!variant) {
      showRepeatNotice("Вариант больше недоступен", `История хранится ${generationRetentionLabel}. Выберите другой результат в галерее.`, true);
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

  const renderDefinitionList = (target, facts = []) => {
    if (!target) return;
    target.replaceChildren(...facts.map((fact) => {
      const item = document.createElement("div");
      const term = document.createElement("dt");
      const value = document.createElement("dd");
      term.textContent = fact.label || "Параметр";
      value.textContent = fact.value || "Не выбрано";
      item.append(term, value);
      return item;
    }));
  };

  const currentGenerationContext = () => {
    const preset = selectedGenerationWorkflow();
    const selectedModel = model?.selectedOptions?.[0];
    const family = preset?.dataset.family || "";
    const references = selectedReferenceMetadata();
    const hasAudio = Boolean(miniMaxAudioIsAvailable() && (uploadedAudio || miniMaxAudioFile?.files?.[0]));
    const hasVideo = Boolean(miniMaxReferencesAreAvailable() && (uploadedVideo || miniMaxVideoFile?.files?.[0]));
    return { preset, selectedModel, family, references, hasAudio, hasVideo };
  };

  const syncGenerationModeGuide = () => {
    if (!generationModeGuide) return;
    const { preset, family, references, hasAudio, hasVideo } = currentGenerationContext();
    if (!preset) {
      generationModeGuide.hidden = true;
      return;
    }
    const guide = generationModules.summary?.guideFor?.({
      family,
      templateID: templateID.value,
      videoMode: miniMaxMode(),
      references,
      hasAudio,
      hasVideo,
    });
    if (!guide) {
      generationModeGuide.hidden = true;
      return;
    }
    generationModeGuide.hidden = false;
    if (generationModeGuideEyebrow) generationModeGuideEyebrow.textContent = guide.eyebrow || "Текущий способ";
    if (generationModeGuideTitle) generationModeGuideTitle.textContent = guide.title || "Как будет создан результат";
    if (generationModeGuideDescription) generationModeGuideDescription.textContent = guide.description || "";
    renderDefinitionList(generationModeGuideFacts, guide.facts);
    if (generationModeGuideAdvice) generationModeGuideAdvice.textContent = guide.advice || "";
  };

  const selectedHeavyOptions = (family) => {
    const options = [];
    if (family === "minimax_h3") {
      if (form.elements.video_rife_enabled?.checked) options.push("плавность движения");
      if (form.elements.video_rtx_enabled?.checked) options.push(`апскейл RTX ${form.elements.video_rtx_scale?.value || "2"}×`);
      if (form.elements.video_color_match?.checked) options.push("перенос палитры");
      if (form.elements.video_sharpen_enabled?.checked) options.push("финальная резкость");
      return options;
    }
    if (family === "flux2" && fluxUpscaleMode?.value && fluxUpscaleMode.value !== "none") {
      const labels = { ultimate: "Ultimate SD Upscale", seedvr2: "SeedVR2", both: "два этапа апскейла" };
      options.push(labels[fluxUpscaleMode.value] || "финальный апскейл");
    }
    if (family === "krea2") {
      const megapixels = Number(outputMegapixels?.value || 0);
      if (templateID.value === "image-to-image") {
        const factor = Number(form.elements.upscale_factor?.value || 1);
        if (!preserveOriginalSize?.checked && factor > 1) options.push(`апскейл ${factor}×`);
      } else {
        if (Number(upscaleSteps?.value || 0) > 0) options.push("апскейл Krea2");
        if (Number(detailSteps?.value || 0) > 0) options.push("финальная детализация");
      }
      if (megapixels >= 3) options.push(`${megapixels.toLocaleString("ru-RU")} Мп`);
    }
    if (!family && Number(steps?.value || 0) > 30) options.push("более 30 шагов");
    return options;
  };

  const generationOutputLabel = (family) => {
    if (family === "minimax_h3") return miniMaxVideoResolutionPreview?.textContent || `${miniMaxVideoQuality?.value || "720"}p`;
    if (family === "krea2") return `${outputMegapixels?.value || "1.9"} Мп`;
    if (family === "flux2" && templateID.value === "image-to-image") {
      if (preserveOriginalSize?.checked) return "Размер исходного фото";
      return `${width?.value || "1024"} × ${height?.value || "1024"}`;
    }
    return `${width?.value || "1024"} × ${height?.value || "1024"}`;
  };

  const syncGenerationSummary = () => {
    syncGenerationModeGuide();
    if (!generationSummaryFacts) return;
    const { preset, selectedModel, family, references, hasAudio, hasVideo } = currentGenerationContext();
    if (!preset || !selectedModel?.value) {
      if (generationSummaryTitle) generationSummaryTitle.textContent = "Выберите workflow и модель";
      renderDefinitionList(generationSummaryFacts, [{ label: "Следующий шаг", value: "Вернитесь к шагу 2 и завершите выбор" }]);
      if (generationSummaryImpact) {
        generationSummaryImpact.dataset.load = "normal";
        generationSummaryImpact.textContent = "Сводка обновится автоматически после выбора параметров.";
      }
      return;
    }
    const loraCount = [...root.querySelectorAll(".lora-row:not([hidden]) .generation-lora-select")]
      .filter((item) => !item.disabled && item.value).length;
    const summary = generationModules.summary?.buildSummary?.({
      family,
      templateID: templateID.value,
      workflowName: preset.querySelector("strong")?.textContent || "Текущая конфигурация",
      modelName: selectedModel.textContent.trim(),
      videoMode: miniMaxMode(),
      references,
      hasAudio,
      hasVideo,
      output: generationOutputLabel(family),
      duration: family === "minimax_h3" ? `${form.elements.video_duration_seconds?.value || "5"} сек.` : "",
      loraCount,
      heavyOptions: selectedHeavyOptions(family),
    });
    if (!summary) return;
    if (generationSummaryTitle) generationSummaryTitle.textContent = summary.title;
    renderDefinitionList(generationSummaryFacts, summary.facts);
    if (generationSummaryImpact) {
      generationSummaryImpact.dataset.load = summary.load || "normal";
      generationSummaryImpact.textContent = summary.impact || "";
    }
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
    assistantSlice.dispatch({ type: "RESET" }, () => ({
      status: "idle", approved: false, original: "", suggestion: "", action: "", correlationID: "", error: "",
    }));
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
    applyWorkflowCapabilityConstraints();
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
      if (editorProfileDescription) editorProfileDescription.textContent = "До четырёх изображений: основной кадр и до трёх дополнительных референсов.";
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
    updateWorkflowNext();
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
      assistantSlice.dispatch({ type: "PROMPT_EDITED" }, (state) => ({
        ...state,
        approved: false,
        action: state.action === "applied" ? "applied_edited" : state.action,
      }));
      setPromptAssistantState("Исходный промт изменён. Подготовьте новый вариант или оставьте свой промт.", "review");
    }
    if (assistantSlice.get().action === "applied") {
      assistantSlice.dispatch({ type: "PROMPT_EDITED" }, (state) => ({ ...state, approved: false, action: "applied_edited" }));
    }
  });

  promptAssistantImprove?.addEventListener("click", async () => {
    const original = positive?.value.trim() || "";
    const mode = templateID.value;
    resetPromptAssistantReview();
    if (!original || (mode !== "text-to-image" && mode !== "image-to-image" && mode !== "minimax-h3-video")) {
      setPromptAssistantState("Сначала выберите схему генерации и введите позитивный промт.", "error");
      positive?.focus();
      return;
    }
    promptAssistantImprove.disabled = true;
    promptAssistantImprove.classList.add("is-loading");
    assistantSlice.dispatch({ type: "REQUEST_START", original }, (state) => ({
      ...state,
      status: "loading",
      approved: false,
      original,
      suggestion: "",
      action: "",
      correlationID: "",
      error: "",
    }));
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
      assistantSlice.dispatch({
        type: "REQUEST_SUCCESS",
        suggestion: payload.prompt,
        correlationID: payload.correlation_id || "",
      }, (state) => ({
        ...state,
        status: "review",
        suggestion: payload.prompt,
        correlationID: payload.correlation_id || "",
        error: "",
      }));
      promptAssistantReview.hidden = false;
      setPromptAssistantState(`Вариант подготовлен моделью ${payload.model || "e4b"}. Подтвердите или отредактируйте его.`, "review");
      promptAssistantDraft.focus({ preventScroll: true });
    } catch (error) {
      assistantSlice.dispatch({ type: "REQUEST_ERROR", error: error.message }, (state) => ({ ...state, status: "error", error: error.message || "Request failed" }));
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
    assistantSlice.dispatch({ type: "APPLY", suggestion }, (state) => ({ ...state, status: "approved", approved: true, action: "applied", suggestion }));
    promptAssistantReview.hidden = true;
    setPromptAssistantState("Вариант применён. Его можно дополнительно отредактировать перед генерацией.", "approved");
    positive.focus({ preventScroll: true });
  });

  promptAssistantKeep?.addEventListener("click", () => {
    assistantSlice.dispatch({ type: "KEEP_ORIGINAL" }, (state) => ({ ...state, status: "approved", approved: true, action: "kept_original" }));
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
    button.addEventListener("click", () => showStep(Math.max(1, wizardSlice.get().step - 1)));
  });
  const handleMiniMaxModeChange = () => {
    const referenceOnly = model?.selectedOptions?.[0]?.dataset.videoReferenceOnly === "true";
    if (referenceOnly && miniMaxMode() !== "references") setMiniMaxMode("references");
    videoSlice.dispatch({ type: "SET_MODE", mode: miniMaxMode() }, (state) => ({ ...state, mode: miniMaxMode() }));
    if (miniMaxVideoModeHint) miniMaxVideoModeHint.textContent = miniMaxMode() === "references"
      ? referenceOnly
        ? "Выбрано: Eros Max работает в режиме свободных референсов. Ролик строится по промту; фото, видео и аудио необязательны."
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

  const renderGalleryImagePicker = () => {
    if (!imagePickerGrid || !imagePickerState) return;
    imagePickerGrid.replaceChildren();
    if (!galleryPickerImages.length) {
      imagePickerState.hidden = false;
      if (galleryPickerImagesLoading) imagePickerState.textContent = "Загружаем ваши изображения...";
      else if (galleryPickerImagesLoaded) imagePickerState.textContent = `За последние ${mediaRetentionLabel} доступных изображений нет. Можно загрузить фото с устройства.`;
      return;
    }
    imagePickerState.hidden = true;
    imagePickerGrid.replaceChildren(...galleryPickerImages.map((entry) => {
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
      const meta = document.createElement("small");
      const created = new Date(entry.createdUnix);
      const dateLabel = Number.isFinite(created.getTime())
        ? new Intl.DateTimeFormat("ru-RU", { day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit" }).format(created)
        : "";
      meta.textContent = [dateLabel, entry.name].filter(Boolean).join(" · ");
      details.append(title, meta);
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

  const refreshGalleryPickerImages = async () => {
    if (galleryPickerImagesLoading) return;
    galleryPickerImagesLoading = true;
    if (imagePickerRefresh) {
      imagePickerRefresh.disabled = true;
      imagePickerRefresh.classList.add("is-loading");
    }
    renderGalleryImagePicker();
    try {
      const response = await fetch("/generate/library/images", { credentials: "same-origin" });
      const payload = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(payload.error || "Не удалось загрузить изображения");
      galleryPickerImages = (Array.isArray(payload.images) ? payload.images : []).map((entry) => ({
        id: Number(entry.id || 0),
        url: String(entry.url || ""),
        name: String(entry.filename || `Изображение ${entry.id || ""}`),
        modelName: String(entry.model_name || "Сгенерированное изображение"),
        createdUnix: Number(entry.created_unix || 0),
        expiresUnix: Number(entry.expires_unix || 0),
        sensitive: Boolean(entry.sensitive),
      })).filter((entry) => entry.id > 0 && entry.url);
      galleryPickerImagesLoaded = true;
    } catch (error) {
      if (!galleryPickerImages.length && imagePickerState) {
        imagePickerState.hidden = false;
        imagePickerState.textContent = error.message || "Не удалось загрузить изображения. Попробуйте ещё раз.";
      }
      throw error;
    } finally {
      galleryPickerImagesLoading = false;
      if (imagePickerRefresh) {
        imagePickerRefresh.disabled = false;
        imagePickerRefresh.classList.remove("is-loading");
      }
      renderGalleryImagePicker();
    }
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
    if (!imagePicker || !item || !selectedGenerationWorkflow()) return;
    galleryPickerSlot = item;
    if (imagePickerSlot) {
      imagePickerSlot.textContent = isMiniMaxSelected()
        ? miniMaxMode() === "references" ? `референсе ${item.index}` : item.index === 1 ? "первом кадре" : "последнем кадре"
        : item.index === 1 ? "основном фото" : `референсе ${item.index}`;
    }
    imagePicker.hidden = false;
    document.body.classList.add("generation-image-picker-open");
    renderGalleryImagePicker();
    imagePicker.querySelector(".generation-image-picker-close")?.focus({ preventScroll: true });
    try {
      await refreshGalleryPickerImages();
    } catch (_) {}
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
    const source = generationModules.media?.gallerySource?.(entry) || entry;
    mediaSlice.dispatch({ type: "SELECT_SOURCE", slot: item.index, source }, (state) => ({
      ...state,
      sources: { ...state.sources, [String(item.index)]: source },
      error: "",
    }));
    uploadedImages.delete(item.index);
    if (item.input) item.input.value = "";
    if (inputImages[item.index - 1]) inputImages[item.index - 1].value = "";
    applyImageSelectionPreview(item, entry, entry.url, "Выбрано из моих генераций. Передадим фото в ComfyUI при продолжении.");
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
    const source = generationModules.media?.deviceSource?.(file) || file;
    mediaSlice.dispatch({ type: "SELECT_SOURCE", slot: item.index, source }, (state) => ({
      ...state,
      sources: { ...state.sources, [String(item.index)]: source },
      error: "",
    }));
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
      if (templateID.value === "image-to-image" && item.index === 1) item.role.value = "base_scene";
      syncReferenceMap();
      syncGenerationSummary();
      if (promptAssistantEnabled?.checked) resetPromptAssistantReview();
    });
  });

  imagePicker?.querySelectorAll("[data-gallery-image-picker-close]").forEach((button) => button.addEventListener("click", closeGalleryImagePicker));
  imagePickerRefresh?.addEventListener("click", () => { refreshGalleryPickerImages().catch(() => {}); });
  document.addEventListener("keydown", (event) => { if (event.key === "Escape") closeGalleryImagePicker(); });

  const uploadSelectedImage = async (item) => {
    const file = selectedImageFile(item);
    const galleryImage = gallerySelections.get(item.index);
    const source = generationModules.media?.sourceFrom?.(file, galleryImage) || file || galleryImage;
    if (!source) throw new Error("Не удалось прочитать выбранное фото");
    if (generationModules.media?.uploadImageSource) {
      return generationModules.media.uploadImageSource(source, {
        fetcher: fetch,
        csrf: form.elements.csrf?.value || "",
      });
    }
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
    return { ...payload, value: [payload.subfolder, payload.name].filter(Boolean).join("/"), source };
  };

  workflowNext?.addEventListener("click", async () => {
    if (!generationWorkflowID.value) return;
    const requiresPrimary = wizardSlice.get().requiresImage;
    const selectedSlots = imageSlots.filter((item) => item.index <= activeMaxInputImages() && hasSelectedImage(item));
    if (!selectedSlots.length && !requiresPrimary && !hasPendingMiniMaxAudio() && !hasPendingMiniMaxVideo()) {
      showStep(3);
      positive?.focus({ preventScroll: true });
      return;
    }
    if ((requiresPrimary && !selectedSlots.some((item) => item.index === 1)) || wizardSlice.get().uploadInFlight) return;
    const pendingSlots = selectedSlots.filter((item) => !uploadedImages.get(item.index));
    const pendingAudio = hasPendingMiniMaxAudio();
    const pendingVideo = hasPendingMiniMaxVideo();
    if (!pendingSlots.length && !pendingAudio && !pendingVideo) {
      showStep(3);
      positive?.focus({ preventScroll: true });
      return;
    }
    wizardSlice.dispatch({ type: "UPLOAD_START" }, (state) => ({ ...state, uploadInFlight: true }));
    mediaSlice.dispatch({ type: "UPLOAD_START" }, (state) => ({ ...state, uploading: true, error: "" }));
    workflowNext.disabled = true;
    workflowNext.classList.add("is-loading");
    try {
      for (const item of pendingSlots) {
        const file = selectedImageFile(item);
        const galleryImage = gallerySelections.get(item.index);
        if (!file && !galleryImage) throw new Error("Не удалось прочитать выбранное фото");
        if (item.state) item.state.textContent = galleryImage ? "Передаём ранее созданное фото в ComfyUI..." : "Загружаем фото с устройства...";
        const payload = await uploadSelectedImage(item);
        const value = payload.value;
        uploadedImages.set(item.index, value);
        mediaSlice.dispatch({ type: "UPLOAD_SUCCESS", slot: item.index, value }, (state) => ({
          ...state,
          uploading: false,
          uploaded: { ...state.uploaded, [String(item.index)]: value },
          error: "",
        }));
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
      mediaSlice.dispatch({ type: "UPLOAD_ERROR", error: error.message }, (state) => ({ ...state, uploading: false, error: error.message || "Upload failed" }));
      const failed = pendingSlots.find((item) => !uploadedImages.get(item.index));
      if (failed?.state) failed.state.textContent = error.message || "Не удалось загрузить фото";
      if (pendingAudio && miniMaxAudioState && !uploadedAudio) miniMaxAudioState.textContent = error.message || "Не удалось загрузить аудио";
      if (pendingVideo && miniMaxVideoState && !uploadedVideo) miniMaxVideoState.textContent = error.message || "Не удалось загрузить видео";
      updateWorkflowNext();
    } finally {
      wizardSlice.dispatch({ type: "UPLOAD_FINISH" }, (state) => ({ ...state, uploadInFlight: false }));
      mediaSlice.dispatch({ type: "UPLOAD_FINISH" }, (state) => ({ ...state, uploading: false }));
      workflowNext.classList.remove("is-loading");
      updateWorkflowNext();
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
      heading.innerHTML = `<div><p class="section-kicker">Моя галерея</p><h2>Последние результаты</h2><p class="panel-intro">Результаты доступны в вашем профиле ${mediaRetentionLabel}, затем удаляются без возможности восстановления.</p></div>`;
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
    const assistant = assistantSlice.get();
    const numericFieldNames = new Set([...form.querySelectorAll('input[type="number"], input[data-localized-decimal]')].map((field) => field.name));
    for (const [name, value] of [...body.entries()]) {
      if (numericFieldNames.has(name) && typeof value === "string") body.set(name, value.replaceAll(",", "."));
    }
    body.set("template_id", selectedChoice()?.dataset.workflowId || "");
    body.set("generation_workflow", selectedGenerationWorkflow()?.dataset.presetId || "");
    body.set("assistant_requested", assistant.original ? "true" : "false");
    body.set("assistant_applied", assistant.action.startsWith("applied") ? "true" : "false");
    body.set("assistant_template_used", assistant.original ? (promptAssistantTemplate?.value || "") : "");
    body.set("assistant_think_used", assistant.original && promptAssistantThink?.checked ? "true" : "false");
    body.set("assistant_original_prompt", assistant.original);
    body.set("assistant_suggestion", assistant.suggestion);
    if (assistant.correlationID) body.set("correlation_id", assistant.correlationID);
    ["input_image", "input_image_2", "input_image_3", "input_image_4"].forEach((name, index) => body.set(name, uploadedImages.get(index + 1) || ""));
    for (let index = 1; index <= 4; index += 1) {
      body.delete(`image_role_${index}`);
      body.delete(`image_source_${index}`);
      body.delete(`image_source_id_${index}`);
      body.delete(`image_source_name_${index}`);
    }
    selectedReferenceMetadata().forEach((reference) => {
      body.set(`image_role_${reference.number}`, reference.role || "details");
      body.set(`image_source_${reference.number}`, reference.source || "unknown");
      if (reference.sourceID) body.set(`image_source_id_${reference.number}`, reference.sourceID);
      if (reference.sourceName) body.set(`image_source_name_${reference.number}`, reference.sourceName);
    });
    body.set("input_audio", miniMaxAudioIsAvailable() ? uploadedAudio : "");
    body.set("input_video", miniMaxReferencesAreAvailable() ? uploadedVideo : "");
    body.set("video_reference_audio", miniMaxReferencesAreAvailable() && uploadedVideo && form.elements.video_reference_audio?.checked ? "true" : "false");
    if (pendingParentJobID) body.set("parent_job_id", pendingParentJobID);
    return new URLSearchParams(body);
  };

  const preflightField = (name) => {
    if (!name) return null;
    const field = form.elements.namedItem(name);
    if (field instanceof HTMLElement) return field;
    if (field && typeof field.length === "number") {
      return [...field].find((candidate) => candidate instanceof HTMLElement) || null;
    }
    return null;
  };

  const revealPreflightField = (name) => {
    const field = preflightField(name);
    if (!field) return false;
    const panel = field.closest("[data-step]");
    const step = Number(panel?.dataset.step || 0);
    if (step > 0 && step !== wizardSlice.get().step) showStep(step);
    for (let details = field.closest("details"); details; details = details.parentElement?.closest("details")) {
      details.open = true;
    }
    const target = field.closest("label, .video-enhancement, .workflow-settings-section") || field;
    const behavior = window.matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth";
    window.setTimeout(() => {
      target.classList.remove("preflight-field-target");
      target.scrollIntoView({ block: "center", behavior });
      target.classList.add("preflight-field-target");
      if (!field.disabled) field.focus({ preventScroll: true });
      window.setTimeout(() => target.classList.remove("preflight-field-target"), 1800);
    }, 120);
    return true;
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
      if (preflightField(check.field)) {
        const action = document.createElement("button");
        action.type = "button";
        action.className = "generation-preflight-jump";
        action.textContent = "Открыть настройку";
        action.setAttribute("aria-label", `Открыть настройку: ${check.label || check.field}`);
        action.addEventListener("click", () => revealPreflightField(check.field));
        item.append(action);
      }
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
    const items = Array.isArray(recipes) ? recipes : [];
    recipeSlice.dispatch({ type: "SET_ITEMS", items }, (state) => ({ ...state, items, loading: false }));
    if (!recipeSelect) return;
    const selected = recipeSelect.value;
    recipeSelect.replaceChildren(new Option("Выберите сохранённый набор", ""));
    items.forEach((recipe) => recipeSelect.append(new Option(recipe.name, String(recipe.id))));
    recipeSelect.value = items.some((recipe) => String(recipe.id) === selected) ? selected : "";
    recipeSlice.dispatch({ type: "SELECT", id: recipeSelect.value }, (state) => ({ ...state, selectedID: recipeSelect.value }));
    if (recipeApply) recipeApply.disabled = !recipeSelect.value;
    if (recipeDelete) recipeDelete.disabled = !recipeSelect.value;
  };

  const refreshRecipes = async () => {
    recipeSlice.dispatch({ type: "LOAD_START" }, (state) => ({ ...state, loading: true }));
    const response = await fetch("/generate/recipes", { credentials: "same-origin" });
    const payload = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(payload.error || "Не удалось загрузить наборы");
    renderRecipes(payload.recipes || []);
  };

  const setRecipeState = (message, state = "") => {
    recipeSlice.dispatch({ type: "SET_MESSAGE", message, status: state }, (current) => ({ ...current, message: message || "", status: state || "" }));
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
      recipeSlice.dispatch({ type: "SELECT", id: recipeSelect.value }, (state) => ({ ...state, selectedID: recipeSelect.value }));
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
    const recipe = generationModules.recipes?.selectedRecipe?.({ ...recipeSlice.get(), selectedID: recipeSelect?.value || "" })
      || recipeSlice.get().items.find((item) => String(item.id) === recipeSelect?.value);
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
    const variants = Array.isArray(payload.variants) ? payload.variants : [];
    historySlice.dispatch({ type: "SET_VARIANTS", variants }, (state) => ({ ...state, variants }));
    if (imagePicker && !imagePicker.hidden) renderGalleryImagePicker();
    restoreRequestedVariant();
  };

  const syncGenerationHistoryVisibility = ({ persist = false } = {}) => {
    if (!variantsContent || !variantsToggle) return;
    const collapsed = historySlice.get().collapsed;
    variantsContent.hidden = collapsed;
    variantsToggle.textContent = collapsed ? "Показать" : "Свернуть";
    variantsToggle.setAttribute("aria-expanded", String(!collapsed));
    if (!persist) return;
    try { window.localStorage.setItem(generationHistoryCollapsedStorageKey, collapsed ? "true" : "false"); } catch (_) {}
  };

  const jobStateLabels = {
    draft: "Создано",
    submitting: "Подготовка",
    preparing: "Подготовка",
    uploading: "Загрузка файлов",
    waiting_for_resources: "Ожидает ресурсы",
    queued: "В очереди",
    running: "В работе",
    cancelling: "Отменяется",
    postprocessing: "Обработка результата",
    archiving: "Сохранение результата",
    completed: "Готово",
    failed: "Ошибка",
    error: "Ошибка",
    cancelled: "Отменено",
    expired: "Истекло",
  };
  const jobTemplateLabels = {
    "text-to-image": "Текст в изображение",
    "image-to-image": "Фото и промт",
    "minimax-h3-video": "Видео MiniMax H3",
  };
  const jobStateLabel = (state) => jobStateLabels[state] || "Подготовка";
  const jobTemplateLabel = (template) => jobTemplateLabels[template] || "Генерация";
  const formatJobDuration = (seconds) => {
    const value = Math.max(0, Number(seconds) || 0);
    if (value < 1) return "меньше 1 сек.";
    if (value < 60) return `${Math.round(value)} сек.`;
    if (value < 3600) return `${Math.max(1, Math.round(value / 60))} мин.`;
    const hours = Math.floor(value / 3600);
    const minutes = Math.round((value % 3600) / 60);
    return minutes ? `${hours} ч. ${minutes} мин.` : `${hours} ч.`;
  };
  const formatJobTime = (value) => {
    const date = new Date(value);
    if (!Number.isFinite(date.getTime())) return "";
    return new Intl.DateTimeFormat("ru-RU", { day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit" }).format(date);
  };
  const cleanModelName = (value) => String(value || "Модель не определена").replaceAll("\\", "/").split("/").pop().replace(/\.(safetensors|ckpt|gguf)$/i, "");

  const setJobsConnectionState = (connected) => {
    jobSlice.dispatch({ type: "SET_LIVE", live: connected }, (state) => ({ ...state, live: Boolean(connected) }));
    renderJobCount();
  };

  const jobCountLabel = (count) => {
    const value = Math.abs(Number(count) || 0);
    const lastTwo = value % 100;
    if (lastTwo >= 11 && lastTwo <= 14) return `${value} заданий`;
    switch (value % 10) {
      case 1: return `${value} задание`;
      case 2:
      case 3:
      case 4: return `${value} задания`;
      default: return `${value} заданий`;
    }
  };

  const renderJobCount = (shown = null) => {
    if (!variantCount) return;
    const jobs = jobSlice.get();
    const filteredCount = shown === null ? jobs.items.length : shown;
    const count = filteredCount === jobs.items.length ? jobCountLabel(jobs.items.length) : `Показано ${filteredCount} из ${jobs.items.length}`;
    const live = jobs.live ? "обновляются автоматически" : "переподключаем обновления";
    variantCount.textContent = `${count} · ${live} · хранятся ${generationRetentionLabel}`;
  };

  const renderJobMedia = (job) => {
    const media = job.media?.find((item) => item.media_type === "image") || job.media?.[0];
    if (!media) {
      const placeholder = document.createElement("div");
      placeholder.className = "generation-job-placeholder";
      const mark = document.createElement("span");
      mark.setAttribute("aria-hidden", "true");
      mark.textContent = job.template_id === "minimax-h3-video" ? "▶" : "◇";
      const label = document.createElement("small");
      label.textContent = job.state === "completed" ? "Результат недоступен" : jobStateLabel(job.state);
      placeholder.append(mark, label);
      return placeholder;
    }
    const preview = document.createElement("button");
    preview.type = "button";
    preview.className = "generation-job-preview";
    preview.setAttribute("aria-label", media.media_type === "video" ? "Открыть видео" : "Открыть изображение");
    if (media.sensitive) {
      preview.classList.add("sensitive-media");
      preview.dataset.sensitiveMedia = "";
    }
    if (media.media_type === "video") {
      const video = document.createElement("video");
      video.muted = true;
      video.playsInline = true;
      video.preload = "metadata";
      video.src = media.url;
      const play = document.createElement("span");
      play.className = "generation-job-play";
      play.setAttribute("aria-hidden", "true");
      play.textContent = "▶";
      preview.append(video, play);
      wireVideoPreview(preview, media);
    } else {
      const image = document.createElement("img");
      image.loading = "lazy";
      image.src = media.url;
      image.alt = "Результат генерации";
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
    return preview;
  };

  const loadJobDetails = async (details, jobID) => {
    const content = details.querySelector("[data-job-details-content]");
    if (!content || details.dataset.loaded === "true" || details.dataset.loading === "true") return;
    details.dataset.loading = "true";
    content.textContent = "Загружаем этапы...";
    try {
      const response = await fetch(`/generate/jobs/detail?job_id=${encodeURIComponent(jobID)}`, { credentials: "same-origin" });
      const payload = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(payload.error || "Не удалось загрузить этапы");
      const transitions = Array.isArray(payload.transitions) ? payload.transitions : [];
      const list = document.createElement("ol");
      list.className = "generation-job-timeline";
      transitions.forEach((transition) => {
        const item = document.createElement("li");
        const time = document.createElement("time");
        time.dateTime = transition.created_at || "";
        time.textContent = formatJobTime(transition.created_at);
        const copy = document.createElement("span");
        const state = document.createElement("strong");
        state.textContent = jobStateLabel(transition.state === "failed" ? "error" : transition.state);
        const message = document.createElement("small");
        message.textContent = transition.message || "Состояние обновлено";
        copy.append(state, message);
        item.append(time, copy);
        list.append(item);
      });
      content.replaceChildren(list);
      details.dataset.loaded = "true";
    } catch (error) {
      content.textContent = error.message || "Не удалось загрузить этапы";
      content.classList.add("has-error");
    } finally {
      delete details.dataset.loading;
    }
  };

  const cancelJob = async (job, button) => {
    if (!job?.job_id || button.disabled) return;
    button.disabled = true;
    button.textContent = "Отменяем...";
    try {
      const body = new URLSearchParams({ csrf: form.elements.csrf?.value || "", job_id: job.job_id });
      const response = await fetch("/generate/jobs/cancel", { method: "POST", headers: { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8" }, body, credentials: "same-origin" });
      const payload = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(payload.error || "Не удалось отменить задание");
      if (job.prompt_id && activeGenerationID === job.prompt_id) {
        clearActiveGeneration();
        closeProgressSocket();
        resultTitle.textContent = payload.cancelled ? "Генерация отменена" : "Отмена выполняется";
        resultStatus.textContent = payload.job?.message || "Состояние задания обновлено.";
        runProgress.hidden = true;
        setGenerationActions({ retry: true });
      }
      await refreshJobs();
    } catch (error) {
      showRepeatNotice("Не удалось отменить задание", error.message || "Gateway временно недоступен.", true);
    } finally {
      button.disabled = false;
      button.textContent = "Отменить";
    }
  };

  const retryJob = async (job, button) => {
    if (!job?.job_id || button.disabled) return;
    button.disabled = true;
    button.textContent = "Готовим...";
    try {
      const body = new URLSearchParams({ csrf: form.elements.csrf?.value || "", job_id: job.job_id });
      const response = await fetch("/generate/jobs/retry", { method: "POST", headers: { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8" }, body, credentials: "same-origin" });
      const payload = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(payload.error || "Не удалось восстановить параметры");
      if (!applySavedValues(payload.values)) throw new Error("Модель или workflow этого задания сейчас недоступны");
      pendingParentJobID = payload.parent_job_id || "";
      if (job.prompt_id && activeGenerationID === job.prompt_id) clearActiveGeneration();
      if (payload.requires_inputs) {
        showRepeatNotice("Параметры восстановлены", "Заново выберите исходные файлы или референсы, затем запустите генерацию.");
        window.requestAnimationFrame(() => form.scrollIntoView({ behavior: "smooth", block: "start" }));
      } else {
        form.requestSubmit();
      }
    } catch (error) {
      showRepeatNotice("Не удалось повторить задание", error.message || "Параметры больше недоступны.", true);
    } finally {
      button.disabled = false;
      button.textContent = "Повторить";
    }
  };

  const renderJobs = () => {
    if (!variantsSection || !variantList) return;
    const items = jobSlice.get().items;
    const filters = historySlice.dispatch({
      type: "SET_FILTERS",
      stateFilter: variantStateFilter?.value || "",
      templateFilter: variantTemplateFilter?.value || "",
    }, (state) => ({ ...state, stateFilter: variantStateFilter?.value || "", templateFilter: variantTemplateFilter?.value || "" }));
    const filteredJobs = generationModules.history?.filterJobs?.(items, filters) || items.filter((job) => (
      (!filters.stateFilter || job.state === filters.stateFilter)
      && (!filters.templateFilter || job.template_id === filters.templateFilter)
    ));
    renderJobCount(filteredJobs.length);
    if (filteredJobs.length === 0) {
      const empty = document.createElement("p");
      empty.className = "generation-variant-empty ui-empty-state";
      empty.textContent = items.length ? "По этим фильтрам заданий нет." : "Заданий пока нет. Первый запуск появится здесь автоматически.";
      variantList.replaceChildren(empty);
      return;
    }
    variantList.replaceChildren(...filteredJobs.map((job) => {
      const card = document.createElement("article");
      card.className = "generation-job ui-media-card";
      card.dataset.state = job.state || "submitting";
      if (["submitting", "preparing", "uploading", "queued", "waiting_resources", "running", "postprocessing", "archiving"].includes(job.state)) {
        card.setAttribute("aria-busy", "true");
      } else if (job.state === "completed") {
        card.classList.add("is-success");
      } else if (job.state === "failed" || job.state === "error") {
        card.classList.add("is-error");
      }
      card.append(renderJobMedia(job));
      const body = document.createElement("div");
      body.className = "generation-job-body";
      const heading = document.createElement("div");
      heading.className = "generation-job-heading";
      const headingCopy = document.createElement("span");
      const title = document.createElement("strong");
      title.textContent = jobTemplateLabel(job.template_id);
      const modelName = document.createElement("small");
      modelName.textContent = cleanModelName(job.model_name);
      headingCopy.append(title, modelName);
      const state = document.createElement("span");
      state.className = "generation-job-state";
      state.textContent = jobStateLabel(job.state);
      heading.append(headingCopy, state);
      const message = document.createElement("p");
      message.className = "generation-job-message";
      message.textContent = job.message || "Состояние задания обновляется";
      const prompt = document.createElement("p");
      prompt.className = "generation-job-prompt";
      prompt.textContent = job.prompt || "Промт пока не сохранён";
      const meta = document.createElement("small");
      meta.className = "generation-job-meta";
      const seed = Number(job.seed) >= 0 ? `Seed ${job.seed}` : "Случайный seed";
      meta.textContent = `${formatJobTime(job.created_at)} · ${seed} · ${formatJobDuration(job.duration_seconds)}`;
      if (job.expires_at) {
        const expiry = document.createElement("time");
        expiry.dataset.generationExpiry = String(new Date(job.expires_at).getTime());
        expiry.textContent = formatExpiry(Number(expiry.dataset.generationExpiry) - Date.now());
        meta.append(" · ", expiry);
      }
      const actions = document.createElement("div");
      actions.className = "generation-job-actions";
      if (job.retryable) {
        const repeat = document.createElement("button");
        repeat.type = "button";
        repeat.className = "button ghost";
        repeat.textContent = "Повторить";
        repeat.addEventListener("click", () => retryJob(job, repeat));
        actions.append(repeat);
      }
      if (job.cancellable) {
        const cancel = document.createElement("button");
        cancel.type = "button";
        cancel.className = "button danger ghost";
        cancel.textContent = "Отменить";
        cancel.addEventListener("click", () => cancelJob(job, cancel));
        actions.append(cancel);
      }
      const details = document.createElement("details");
      details.className = "generation-job-details";
      const summary = document.createElement("summary");
      summary.textContent = "Этапы";
      const detailsContent = document.createElement("div");
      detailsContent.dataset.jobDetailsContent = "";
      details.append(summary, detailsContent);
      details.addEventListener("toggle", () => { if (details.open) loadJobDetails(details, job.job_id); });
      body.append(heading, message, prompt, meta);
      const footer = document.createElement("div");
      footer.className = "generation-job-footer";
      footer.append(actions);
      card.append(body, footer, details);
      return card;
    }));
    refreshExpiryLabels();
  };

  const refreshJobs = async () => {
    if (jobSlice.get().loading) return;
    jobSlice.dispatch({ type: "LOAD_START" }, (state) => ({ ...state, loading: true, error: "" }));
    try {
      const response = await fetch("/generate/jobs", { credentials: "same-origin", cache: "no-store" });
      const payload = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(payload.error || "Не удалось загрузить задания");
      const items = Array.isArray(payload.jobs) ? payload.jobs : [];
      jobSlice.dispatch({ type: "SET_JOBS", items, revision: payload.revision }, (state) => ({
        ...state,
        loading: false,
        items,
        revision: Math.max(Number(state.revision) || 0, Number(payload.revision) || 0),
        error: "",
      }));
      renderJobs();
    } catch (error) {
      jobSlice.dispatch({ type: "LOAD_ERROR", error: error.message }, (state) => ({ ...state, loading: false, error: error.message || "Load failed" }));
      if (!jobSlice.get().items.length && variantList) {
        const empty = document.createElement("p");
        empty.className = "generation-variant-empty generation-job-load-error ui-empty-state";
        empty.textContent = error.message || "Не удалось загрузить задания";
        variantList.replaceChildren(empty);
      }
      setJobsConnectionState(false);
    }
  };

  const connectGenerationJobEvents = () => {
    generationJobEvents?.close();
    if (!("EventSource" in window)) {
      setJobsConnectionState(false);
      return;
    }
    generationJobEvents = new EventSource(`/generate/jobs/events?since=${encodeURIComponent(jobSlice.get().revision)}`);
    generationJobEvents.addEventListener("open", () => setJobsConnectionState(true));
    generationJobEvents.addEventListener("ready", (event) => {
      jobSlice.dispatch({ type: "SET_REVISION", revision: event.data }, (state) => ({ ...state, revision: Math.max(Number(state.revision) || 0, Number(event.data) || 0) }));
      setJobsConnectionState(true);
    });
    generationJobEvents.addEventListener("jobs", (event) => {
      jobSlice.dispatch({ type: "SET_REVISION", revision: event.data }, (state) => ({ ...state, revision: Math.max(Number(state.revision) || 0, Number(event.data) || 0) }));
      setJobsConnectionState(true);
      refreshJobs();
    });
    generationJobEvents.addEventListener("error", () => setJobsConnectionState(false));
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
          try { await refreshJobs(); } catch (_) {}
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
  variantStateFilter?.addEventListener("change", renderJobs);
  variantTemplateFilter?.addEventListener("change", renderJobs);
  try {
    const collapsed = window.localStorage.getItem(generationHistoryCollapsedStorageKey) === "true";
    historySlice.dispatch({ type: "SET_COLLAPSED", collapsed }, (state) => ({ ...state, collapsed }));
  } catch (_) {}
  syncGenerationHistoryVisibility();
  variantsToggle?.addEventListener("click", () => {
    historySlice.dispatch({ type: "TOGGLE_COLLAPSED" }, (state) => ({ ...state, collapsed: !state.collapsed }));
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
  recipeSelect?.addEventListener("change", () => {
    recipeSlice.dispatch({ type: "SELECT", id: recipeSelect.value }, (state) => ({ ...state, selectedID: recipeSelect.value }));
    if (recipeApply) recipeApply.disabled = !recipeSelect.value;
    if (recipeDelete) recipeDelete.disabled = !recipeSelect.value;
  });
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
      refreshJobs();
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
    if (promptAssistantEnabled?.checked && !assistantSlice.get().approved) {
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
      const headers = { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8" };
      if (assistantSlice.get().correlationID) headers["X-Correlation-ID"] = assistantSlice.get().correlationID;
      const response = await fetch("/generate/run", { method: "POST", headers, body, credentials: "same-origin" });
      const payload = await response.json().catch(() => ({}));
      if (!response.ok) {
        clearActiveGeneration();
        setGenerationActions({ retry: true });
        resultTitle.textContent = "Не удалось выполнить генерацию";
        resultStatus.textContent = payload.error || "Gateway отклонил запуск. Параметры сохранены, можно повторить попытку.";
        result.classList.add("has-error");
        runProgress.hidden = true;
        return;
      }
      pendingParentJobID = "";
      updateGenerationQuota(payload.quota);
      refreshJobs();
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
  loadWorkflowCapabilities();
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
  refreshJobs().finally(connectGenerationJobEvents);
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
  window.setInterval(() => refreshJobs().catch(() => {}), 30000);
  window.setInterval(() => refreshVariants().catch(() => {}), 30000);
  window.addEventListener("beforeunload", () => generationJobEvents?.close(), { once: true });
  generationStore?.emit?.("page:ready", { root, modules: root.dataset.generationModules });
})();
