(() => {
  const root = document.querySelector("[data-comfy-generation]");
  if (!root) return;

  const generationModules = window.AIGatewayGeneration || {};
  root.dataset.generationClient = "modular";
  root.dataset.generationModules = Object.keys(generationModules).sort().join(",");
  const submissionAvailable = generationModules.submission?.createController && generationModules.submission?.createPersistence && generationModules.submission?.createTransport && /^[1-9][0-9]*$/.test(root.dataset.generationOwner || "");
  const assistantAvailable = generationModules.assistant?.createSession && generationModules.assistant?.createPersistence && generationModules.assistant?.createTransport && generationModules.assistantJobs?.createController;
  const resultsAvailable = generationModules.results?.createView && generationModules.history?.createController && generationModules.history?.createTransport && generationModules.lightbox?.createController && generationModules.batch?.selectedJobs && generationModules.job?.reduce;
  const feedback = generationModules.feedback;
  if (!generationModules.payload?.createAdapter || !submissionAvailable || !assistantAvailable || !resultsAvailable || !feedback?.createResource || !generationModules.recipes?.createController) {
    root.dataset.generationClient = "unavailable";
    const banner = document.createElement("section");
    banner.className = "ui-status-banner";
    banner.setAttribute("role", "alert");
    const message = document.createElement("p");
    message.textContent = !generationModules.payload?.createAdapter ? "Не удалось загрузить параметры редактора. Обновите страницу." : !submissionAvailable ? "Не удалось загрузить управление запуском. Обновите страницу." : !assistantAvailable ? "Не удалось загрузить управление ассистентом. Обновите страницу." : !resultsAvailable ? "Не удалось загрузить результаты и историю. Обновите страницу." : "Не удалось загрузить состояния редактора. Обновите страницу.";
    const reload = document.createElement("button");
    reload.type = "button";
    reload.className = "button ghost";
    reload.textContent = "Обновить страницу";
    reload.addEventListener("click", () => window.location.reload());
    banner.append(message, reload);
    root.prepend(banner);
    const unavailableForm = document.getElementById("generation-form");
    unavailableForm?.querySelectorAll("button").forEach(button => { button.disabled = true; });
    unavailableForm?.addEventListener("submit", event => event.preventDefault());
    return;
  }
  const generationStore = generationModules.store?.createStore?.() || null;
  let generationPageActive = true;
  let generationPageEpoch = 0;
  const pageRequests = new Set();
  const loginURL = () => `/login?${new URLSearchParams({ next: window.location.pathname + window.location.search })}`;
  const readFeedbackElements = prefix => ({ root: document.getElementById(prefix + "-feedback"), message: document.getElementById(prefix + "-message"), retry: document.getElementById(prefix + "-refresh"), login: document.getElementById(prefix + "-login") });
  const showReadFeedback = (prefix, state, messages) => feedback.resourceView(readFeedbackElements(prefix), state, { ...messages, loginURL: loginURL() });
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
  const assistantSlice = createStateSlice("assistant", generationModules.assistant);
  const batchSlice = createStateSlice("batch", generationModules.batch, {
    enabled: false, mode: "seeds", count: 4, parameter: "", from: "", to: "", batches: [], selections: {},
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
    document.getElementById("input-image-5"),
    document.getElementById("input-image-6"),
    document.getElementById("input-image-7"),
    document.getElementById("input-image-8"),
    document.getElementById("input-image-9"),
    document.getElementById("input-image-10"),
  ];
  const inputAudio = document.getElementById("input-audio");
  const inputVideo = document.getElementById("input-video");
  const imageSlots = [...root.querySelectorAll("[data-image-slot]")].map((slot) => ({
    slot,
    index: Number(slot.dataset.imageSlot),
    input: slot.querySelector("[data-image-file]"),
    preview: slot.querySelector("[data-image-preview]"),
    previewImage: slot.querySelector("[data-image-preview-image]"),
    previewOpen: slot.querySelector("[data-image-preview-open]"),
    name: slot.querySelector("[data-image-name]"),
    state: slot.querySelector("[data-image-state]"),
    remove: slot.querySelector("[data-remove-image]"),
    label: slot.querySelector("[data-image-slot-label]"),
    role: slot.querySelector("[data-image-role]"),
    galleryChoice: slot.querySelector("[data-gallery-image-picker-choice]"),
    galleryButton: slot.querySelector("[data-gallery-image-picker-open]"),
  }));
  const studio = generationModules.studio?.bindUI?.({ root, window, document });
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
  const miniMaxAudioPreviewMedia = document.getElementById("minimax-audio-preview-media");
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
  const negativeCFGNote = document.getElementById("generation-negative-cfg-note");
  const generationPromptFields = root.querySelector(".generation-prompt-fields");
  const promptAssistant = document.getElementById("prompt-assistant");
  const promptAssistantEnabled = document.getElementById("prompt-assistant-enabled");
  const promptAssistantDescription = document.getElementById("prompt-assistant-description");
  const promptAssistantControls = document.getElementById("prompt-assistant-controls");
  const promptAssistantTemplate = document.getElementById("prompt-assistant-template");
  const promptAssistantModel = document.getElementById("prompt-assistant-model");
  const promptAssistantThink = document.getElementById("prompt-assistant-think");
  const promptAssistantImprove = document.getElementById("prompt-assistant-improve");
  const promptAssistantState = document.getElementById("prompt-assistant-state");
  const promptAssistantReview = document.getElementById("prompt-assistant-review");
  const promptAssistantDraft = document.getElementById("prompt-assistant-draft");
  const promptAssistantNegativeDraft = document.getElementById("prompt-assistant-negative-draft");
  const promptAssistantNegativeReview = document.getElementById("prompt-assistant-negative-review");
  const promptAssistantReviewMeta = document.getElementById("prompt-assistant-review-meta");
  const promptAssistantReferenceReview = document.getElementById("prompt-assistant-reference-review");
  const promptAssistantReferenceCount = document.getElementById("prompt-assistant-reference-count");
  const promptAssistantReferenceList = document.getElementById("prompt-assistant-reference-list");
  const promptAssistantDiffOriginal = document.getElementById("prompt-assistant-diff-original");
  const promptAssistantDiffSuggestion = document.getElementById("prompt-assistant-diff-suggestion");
  const promptAssistantDraftLimit = document.getElementById("prompt-assistant-draft-limit");
  const promptAssistantApply = document.getElementById("prompt-assistant-apply");
  const promptAssistantKeep = document.getElementById("prompt-assistant-keep");
  const promptAssistantOpen = document.getElementById("prompt-assistant-open");
  const promptAssistantStale = document.getElementById("prompt-assistant-stale");
  let assistantSession = null;
  const durableAssistantEnabled = promptAssistant?.dataset.durableJobs === "true";
  promptAssistantImprove.disabled = true;
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
  const qwenEditMegapixels = document.getElementById("generation-qwen-edit-megapixels");
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
  const batchBuilder = document.getElementById("generation-batch-builder");
  const batchEnabled = document.getElementById("generation-batch-enabled");
  const batchControls = document.getElementById("generation-batch-controls");
  const batchCount = document.getElementById("generation-batch-count");
  const batchParameter = document.getElementById("generation-batch-parameter");
  const batchFrom = document.getElementById("generation-batch-from");
  const batchTo = document.getElementById("generation-batch-to");
  const batchParameterField = root.querySelector(".generation-batch-parameter-field");
  const batchRangeFields = [...root.querySelectorAll(".generation-batch-range-field")];
  const batchEstimate = document.getElementById("generation-batch-estimate");
  const batchError = document.getElementById("generation-batch-error");
  const generationQuota = document.getElementById("generation-quota");
  const repeatNotice = document.getElementById("generation-repeat-notice");
  const repeatNoticeTitle = document.getElementById("generation-repeat-title");
  const repeatNoticeMessage = document.getElementById("generation-repeat-message");
  const repeatNoticeDismiss = document.getElementById("generation-repeat-dismiss");
  const outputGrid = document.getElementById("generation-output-grid");
  const previousResult = document.getElementById("generation-previous-result");
  const resultActions = document.getElementById("generation-result-actions");
  const generationSubmit = document.getElementById("generation-submit");
  const retryGeneration = document.getElementById("generation-retry");
  const viewGenerationResult = document.getElementById("generation-view-result");
  const repeatGenerationNow = document.getElementById("generation-repeat-now");
  let resultSourceJobID = "";
  const cancelGeneration = document.getElementById("generation-cancel");
  const submissionLogin = document.getElementById("generation-login");
  const submissionTask = document.getElementById("generation-open-task");
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
  const lightboxViewport = document.getElementById("generation-lightbox-viewport");
  const lightboxZoom = document.getElementById("generation-lightbox-zoom");
  const lightboxScale = document.getElementById("generation-lightbox-scale");
  const lightboxName = document.getElementById("generation-lightbox-name");
  const lightboxDownload = document.getElementById("generation-lightbox-download");
  const lightboxCompare = document.getElementById("generation-lightbox-compare");
  const lightboxTitle = document.getElementById("generation-lightbox-title");
  const imagePicker = document.getElementById("generation-image-picker");
  const imagePickerGrid = document.getElementById("generation-image-picker-grid");
  const imagePickerState = document.getElementById("generation-image-picker-state");
  const imagePickerSlot = document.getElementById("generation-image-picker-slot");
  const imagePickerRefresh = document.getElementById("generation-image-picker-refresh");
  const fieldHelps = [...root.querySelectorAll(".field-help[data-tooltip]")];
  const previewURLs = new Map();
  const selectedImages = new Map();
  const gallerySelections = new Map();
  const uploadedImages = new Map();
  const restoredImages = new Map();
  const draftAssets = new Map();
  let draftController = null;
  let collectionSession = null;
  const imageDraftField = (index) => index === 1 ? "input_image" : `input_image_${index}`;
  const markDraftDirty = () => draftController?.markDirty();
  const clearPrompt = document.getElementById("generation-prompt-clear");
  const undoClearPrompt = document.getElementById("generation-prompt-undo-clear");
  let clearedPrompt = null;
  const syncPromptActions = () => {
    const busy = Boolean(assistantSession?.snapshot().busy);
    if (clearPrompt) clearPrompt.disabled = busy || !(positive?.value || negativePrompt?.value);
    if (undoClearPrompt) {
      undoClearPrompt.hidden = !clearedPrompt;
      undoClearPrompt.disabled = busy;
    }
  };
  const draftFieldIsActive = (field) => field === "input_audio" ? miniMaxAudioIsAvailable() : field === "input_video"
    ? miniMaxVideoIsAvailable()
    : imageSlots.some((item) => imageDraftField(item.index) === field && item.index <= activeMaxInputImages());
  const hasMissingDraftAssets = () => [...draftAssets.entries()].some(([field, asset]) => draftFieldIsActive(field) && !asset.available);
  let uploadedAudio = "";
  let uploadedVideo = "";
  const audioPlayer = generationModules.studio?.bindReferencePlayer?.({
    media: miniMaxAudioPreviewMedia, button: document.getElementById("minimax-audio-play"),
    status: document.getElementById("minimax-audio-playback-status"),
    start: () => form.elements.video_audio_start?.value, duration: () => form.elements.video_duration_seconds?.value,
  });
  const videoPlayer = generationModules.studio?.bindReferencePlayer?.({
    media: miniMaxVideoPreviewMedia, button: document.getElementById("minimax-video-play"),
    status: document.getElementById("minimax-video-playback-status"),
    start: () => aiHubMatchesDuration() ? 0 : form.elements.video_reference_start?.value,
    duration: () => aiHubMatchesDuration() ? aiHubReferenceDuration() : isAIHubSelected() ? form.elements.video_duration_seconds?.value : form.elements.video_reference_duration?.value,
    fallback: async ({ signal }) => {
      const file = miniMaxVideoFile?.files?.[0];
      const reference = uploadedVideo || await uploadSelectedAV("video");
      if (signal.aborted || file !== miniMaxVideoFile?.files?.[0] || reference !== uploadedVideo) throw new Error("Видео изменилось");
      const response = await fetch("/generate/preview/video", {
        method: "POST", credentials: "same-origin", signal,
        body: new URLSearchParams({ csrf: form.elements.csrf?.value || "", input_video: reference }),
      });
      if (!response.ok || !response.headers.get("Content-Type")?.startsWith("video/mp4")) throw new Error("Предпросмотр недоступен");
      return { blob: await response.blob(), duration: response.headers.get("X-Source-Video-Duration") };
    },
  });
  let primaryImageSize = null;

  const krea2EditMaxBaseMegapixels = 4.7;
  const krea2EditMaxLongestSide = 4096;
  let submissionController = null;
  let submitInFlight = false;
  let pendingParentJobID = "";
  let galleryPickerSlot = null;
  const requestedQuery = new URLSearchParams(window.location.search);
  const requestedVariantID = requestedQuery.get("variant") || "";
  const requestedLibraryMediaID = requestedQuery.get("media") || "";
  const requestedLibraryTemplateID = requestedQuery.get("template") || "";
  const requestedLibraryWorkflowID = requestedQuery.get("workflow") || "";
  const requestedLibraryModelID = requestedQuery.get("library_model") || "";
  const requestedLibrarySlot = requestedQuery.get("slot") || "1";
  const requestedLibraryRole = requestedQuery.get("role") || "base_scene";
  const requestedJobID = requestedQuery.get("job") || "";
  const requestedBatchID = requestedQuery.get("batch") || "";
  const requestedEditJobID = requestedQuery.get("edit_job") || "";
  const requestedLoraJobID = requestedQuery.get("lora_job") || "";
  const requestedLoraModelID = requestedQuery.get("lora_model") || "";
  const loraSourceRequested = requestedQuery.has("lora_job") || requestedQuery.has("lora_model");
  const jobSourceRequested = requestedQuery.has("edit_job") || loraSourceRequested;
  const sourceID = loraSourceRequested ? requestedLoraJobID : requestedEditJobID;
  let jobSourceController = null;
  let loraVerificationController = null;
  let requestedVariantHandled = false;
  let requestedLibraryMediaHandled = false;

  const clamp = (value, min, max) => Math.min(max, Math.max(min, value));
  const selectedImageFile = (item) => selectedImages.get(item?.index) || item?.input?.files?.[0] || null;
  const selectedImageSource = (item) => generationModules.media?.sourceFrom?.(
    selectedImageFile(item),
    gallerySelections.get(item?.index),
  ) || selectedImageFile(item) || gallerySelections.get(item?.index) || restoredImages.get(item?.index) || null;
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
  const maxVideoGenerationQuality = numericValue(root.dataset.maxVideoGenerationQuality, 1440);
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
  const capabilitiesResource = feedback.createResource({
    load: async signal => {
      const catalog = await feedback.requestJSON(root.dataset.workflowCapabilitiesUrl, { signal, message: "Не удалось обновить доступность моделей. Можно продолжить с уже загруженными настройками и проверить их перед запуском." });
      if (Number(catalog.schema_version) !== 1 || !Array.isArray(catalog.workflows) || catalog.workflows.some(item => !item || typeof item.id !== "string")) throw feedback.failure("invalid", "Доступность моделей не обновлена. Повторите проверку.");
      return catalog;
    },
    onValue: catalog => {
      if (!catalog) return;
      catalog.workflows.forEach((manifest) => {
        if (manifest?.id) workflowManifestsByID.set(manifest.id, manifest);
        if (manifest?.template_id && !workflowManifestsByID.has(manifest.template_id)) workflowManifestsByID.set(manifest.template_id, manifest);
      });
      applyWorkflowCapabilityConstraints();
      syncMiniMaxVideoProfile();
    },
    onState: state => showReadFeedback("generation-capabilities", state, { loading: "Проверяем доступность моделей..." }),
  });
  const loadWorkflowCapabilities = () => root.dataset.workflowCapabilitiesUrl ? capabilitiesResource.refresh() : Promise.resolve(false);
  document.getElementById("generation-capabilities-refresh")?.addEventListener("click", loadWorkflowCapabilities);
  const miniMaxMode = () => generationModules.video?.normalizeMode?.(miniMaxVideoModeSelect?.value) || miniMaxVideoModeSelect?.value || "frames";
  const miniMaxLongEnabled = () => templateID.value === "minimax-h3-video" && !isAIHubSelected() && Boolean(form.elements.video_long_enabled?.checked);
  let miniMaxLongPreviousMode = null;
  const renderMiniMaxLongPlan = () => {
    const plan = document.getElementById("minimax-long-plan");
    if (!plan || !miniMaxLongEnabled()) return;
    const parts = Number(form.elements.video_long_parts?.value || 2);
    const timeline = generationModules.video?.longTimeline?.(positive?.value, parts) || [];
    const clock = value => `${String(Math.floor(value / 60)).padStart(2, "0")}:${String(value % 60).padStart(2, "0")}`;
    plan.replaceChildren(...timeline.map(part => {
      const item = document.createElement("li");
      const range = document.createElement("strong");
      range.textContent = `${clock(part.start)}–${clock(part.end)}`;
      const text = document.createElement("textarea");
      text.rows = 3;
      text.value = part.text;
      text.placeholder = "Действие и состояние сцены";
      text.setAttribute("aria-label", `Часть ${part.number}: ${clock(part.start)}–${clock(part.end)}`);
      text.addEventListener("change", () => {
        positive.value = generationModules.video.editLongPart(positive.value, parts, part.number, text.value);
        positive.dispatchEvent(new Event("input", { bubbles: true }));
        positive.dispatchEvent(new Event("change", { bubbles: true }));
      });
      item.append(range, text);
      return item;
    }));
    const status = document.getElementById("minimax-long-plan-status");
    if (status) status.textContent = `· ${timeline.filter(part => part.valid).length}/${timeline.length}`;
  };
  const syncMiniMaxLong = () => {
    const available = templateID.value === "minimax-h3-video" && !isAIHubSelected();
    const enabled = miniMaxLongEnabled();
    const control = document.getElementById("minimax-long-control");
    if (control) control.hidden = !available;
    if (form.elements.video_long_enabled) form.elements.video_long_enabled.disabled = !available;
    const fields = document.getElementById("minimax-long-fields");
    if (fields) fields.hidden = !enabled;
    for (const name of ["video_long_parts", "video_long_context", "video_long_transition"]) {
      if (form.elements[name]) form.elements[name].disabled = !enabled;
    }
    const duration = form.elements.video_duration_seconds;
    if (duration) {
      if (enabled && !duration.readOnly) duration.dataset.shortDuration = duration.value;
      if (!enabled && duration.readOnly) duration.value = duration.dataset.shortDuration || "5";
      duration.readOnly = enabled;
      if (enabled) duration.value = "15";
    }
    for (const name of ["video_rife_enabled", "video_rtx_enabled", "video_color_match", "video_sharpen_enabled", "video_clear_vram"]) {
      const field = form.elements[name];
      if (!field) continue;
      if (enabled) {
        if (field.dataset.longPrevious === undefined) field.dataset.longPrevious = String(field.checked);
        field.checked = false;
        field.disabled = true;
      } else if (field.dataset.longPrevious !== undefined) {
        field.checked = field.dataset.longPrevious === "true";
        delete field.dataset.longPrevious;
        field.disabled = false;
      }
      field.closest("label")?.classList.toggle("is-locked", enabled);
    }
    const summary = document.getElementById("minimax-long-summary");
    const parts = Math.min(20, Math.max(2, Number(form.elements.video_long_parts?.value || 2)));
    if (summary) summary.textContent = `${parts} × 15 с = ${parts * 15} с · 24 FPS`;
    renderMiniMaxLongPlan();
  };
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
  const syncMiniMaxBranchCopy = () => {
    const option = model?.selectedOptions?.[0];
    const integratedTurbo = option?.dataset.videoIntegratedTurbo === "true";
    const referenceOnly = option?.dataset.videoReferenceOnly === "true";
    const referenceMode = referenceOnly || miniMaxMode() === "references";
    if (miniMaxVideoModeHint) {
      miniMaxVideoModeHint.textContent = referenceMode
        ? integratedTurbo
          ? "Ветка REF2VA (Eros Max): ролик строится по промту; фото, видео и аудио при наличии используются как свободные референсы."
          : "Ветка REF2VA: ролик строится по промту; фото, видео и аудио при наличии используются как свободные референсы."
        : "Ветка точных кадров: 0 фото — T2VA, 1 фото — I2VA с точным началом, 2 фото — FL2VA с точными началом и финалом.";
    }
    if (miniMaxVideoModelProfile) {
      miniMaxVideoModelProfile.textContent = referenceMode
        ? integratedTurbo
          ? `Активная ветка: REF2VA · ${option?.textContent || 'Eros Max'}. Turbo встроен в модель.`
          : "Активная ветка: REF2VA · MiniMax_H3_Ref2VA. Она принимает до четырёх фото, аудио и видео как свободные референсы."
        : "Активная модель: MiniMax_H3_FL2VA. Режим автоматически становится T2VA, I2VA или FL2VA по количеству точных кадров.";
    }
  };
  const syncMiniMaxVideoProfile = ({ applyModelDefaults = false } = {}) => {
    if (isAIHubSelected()) {
      syncMiniMaxLong();
      setMiniMaxMode("references");
      if (form.elements.video_duration_seconds) form.elements.video_duration_seconds.max = "15";
      const option = model?.selectedOptions?.[0], steps = form.elements.aihub_steps;
      if (steps) {
        const turbo = option?.dataset.videoIntegratedTurbo === "true";
        const beta6 = turbo && Number(option?.dataset.defaultSteps) === 10;
        steps.min = beta6 ? "8" : turbo ? "6" : "20";
        steps.max = beta6 ? "12" : turbo ? "8" : "25";
        if (applyModelDefaults) steps.value = option?.dataset.defaultSteps || (turbo ? "6" : "25");
      }
      return;
    }
    if (form.elements.video_duration_seconds) form.elements.video_duration_seconds.max = "60";
    const option = model?.selectedOptions?.[0];
    const integratedTurbo = option?.dataset.videoIntegratedTurbo === "true";
    const referenceOnly = option?.dataset.videoReferenceOnly === "true";
    const turbo = !integratedTurbo && !applyModelDefaults && Boolean(miniMaxVideoTurbo?.checked);
    const manifest = workflowManifestsByID.get("minimax-h3-video");
    const beta5 = integratedTurbo && option?.dataset.defaultSampler === "er_sde";
    const beta6 = integratedTurbo && Number(option?.dataset.defaultSteps) === 10;
    const profileID = beta6 ? "integrated_turbo_beta6" : beta5 ? "integrated_turbo_beta5" : generationModules.video?.profileID?.({ integratedTurbo, turbo }) || (integratedTurbo ? "integrated_turbo" : turbo ? "turbo" : "regular");
    videoSlice.dispatch({ type: "SET_PROFILE", profileID }, (state) => ({ ...state, profileID }));
    const profile = generationModules.video?.findProfile?.(manifest, profileID) || manifest?.quality_profiles?.find((candidate) => candidate.id === profileID);
    const profileRule = (name) => profile?.parameters?.[name] || null;
    const profileValue = (name, fallback) => profileRule(name)?.value ?? fallback;
    if (miniMaxVideoQuality) {
      [...miniMaxVideoQuality.options].forEach((item) => { item.disabled = numericValue(item.value) > maxVideoGenerationQuality; });
      if (numericValue(miniMaxVideoQuality.value, 720) > maxVideoGenerationQuality) {
        miniMaxVideoQuality.value = String(maxVideoGenerationQuality);
      }
    }
    const frameOption = miniMaxVideoModeInputs.find((input) => input.value === "frames");
    if (miniMaxLongEnabled() && miniMaxLongPreviousMode === null) miniMaxLongPreviousMode = miniMaxMode();
    if (frameOption) frameOption.disabled = referenceOnly || miniMaxLongEnabled();
    if (!miniMaxLongEnabled() && miniMaxLongPreviousMode !== null) {
      if (!referenceOnly) setMiniMaxMode(miniMaxLongPreviousMode);
      miniMaxLongPreviousMode = null;
      syncImageSlots();
      syncMiniMaxAudioReference();
    }
    if ((referenceOnly || miniMaxLongEnabled()) && miniMaxMode() !== "references") {
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
    syncMiniMaxBranchCopy();
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
    if (miniMaxVideoScheduler) {
      const locked = Boolean(profileRule("video_scheduler")?.locked);
      if (locked) miniMaxVideoScheduler.value = String(profileValue("video_scheduler", "simple"));
      miniMaxVideoScheduler.disabled = locked;
    }
    syncMiniMaxLong();
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
    const multiple = (value) => Math.max(32, Math.floor(Math.round(value) / 32) * 32);
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
  const newGenerationRequestID = () => generationModules.submission.newID(window.crypto);
  const submissionBlocksLaunch = () => {
    const state = submissionController?.snapshot();
    return Boolean(state?.pending || ["auth_required", "storage_error"].includes(state?.phase));
  };

  const setGenerationActions = ({ retry = false, cancel = false, authenticate = false, task = false, repeatNow = false, view = false } = {}) => {
    if (!resultActions) return;
    resultActions.hidden = !retry && !cancel && !authenticate && !task && !view;
    if (viewGenerationResult) viewGenerationResult.hidden = !view;
    if (retryGeneration) retryGeneration.hidden = !retry;
    if (repeatGenerationNow) repeatGenerationNow.hidden = !repeatNow;
    if (cancelGeneration) cancelGeneration.hidden = !cancel;
    if (submissionLogin) submissionLogin.hidden = !authenticate;
    if (submissionTask) submissionTask.hidden = !task;
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
    // Mobile decimal keypads may omit the minus key.
    field.inputMode = Number(field.min) < 0 ? "text" : "decimal";
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
    const isQwenEdit = selected?.dataset.family === "qwen21" && selected?.dataset.templateId === "image-to-image";
    const isFluxEdit = selected?.dataset.family === "flux2" && selected?.dataset.templateId === "image-to-image";
    if (isQwenEdit) {
      const target = generationModules.output?.qwen21EditSize(primaryImageSize, outputMegapixels?.value);
      originalResolution.textContent = target
        ? `Исходник: ${sourceWidth} × ${sourceHeight} · ${sourceMegapixelsValue.toFixed(2).replace(".", ",")} Мп. Qwen обработает ${target.width} × ${target.height}, сохранив пропорции.`
        : "Пропорции исходного фото не поддерживаются Qwen Image 2.1.";
      return;
    }
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
    if (selected?.dataset.templateId !== "image-to-image") return;
    const isQwenEdit = selected?.dataset.family === "qwen21";
    const isKreaEdit = selected?.dataset.family === "krea2" && selected?.dataset.templateId === "image-to-image";
    const isFluxEdit = selected?.dataset.family === "flux2" && selected?.dataset.templateId === "image-to-image";
    const minimumDimension = isKreaEdit || isFluxEdit ? 16 : 256;
    const maximumPixels = isKreaEdit ? krea2EditMaxBaseMegapixels * 1024 * 1024 : Infinity;
    const maximumSide = isKreaEdit ? krea2EditMaxLongestSide : 4096;
    const scale = Math.min(1, maximumSide / Math.max(primaryImageSize.width, primaryImageSize.height), Math.sqrt(maximumPixels / (primaryImageSize.width * primaryImageSize.height)));
    const qwenSize = isQwenEdit ? generationModules.output?.qwen21EditSize(primaryImageSize, outputMegapixels?.value) : null;
    if (isQwenEdit && !qwenSize) {
      updateOriginalResolution();
      return;
    }
    const targetWidth = qwenSize?.width ?? roundToMultiple(primaryImageSize.width * scale, 8, minimumDimension);
    const targetHeight = qwenSize?.height ?? roundToMultiple(primaryImageSize.height * scale, 8, minimumDimension);
    width.value = String(targetWidth);
    height.value = String(targetHeight);
    aspect.value = "custom";
    if (!isQwenEdit) outputMegapixels.value = (targetWidth * targetHeight / (1024 * 1024)).toFixed(2);
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

  const calculateResolution = (event) => {
    if (!aspect) {
      updateResolutionPreview();
      return;
    }
    const custom = aspect.value === "custom";
    if (custom && event?.target !== outputMegapixels) {
      updateResolutionPreview();
      return;
    }
    if (custom && !outputMegapixels.value.trim()) return;
    const [ratioWidth, ratioHeight] = custom
      ? [Number(width.value), Number(height.value)]
      : aspect.value.split(":").map(Number);
    if (!(ratioWidth > 0 && ratioHeight > 0)) return;
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

  const setGenerationProgress = (stage, detail = "", percent = null, mode = "work") => {
    if (!runProgress || !generationStage || !generationStageDetail || !generationProgressbarFill) return;
    const isQueueWaiting = mode === "queue";
    runProgress.hidden = false;
    runProgress.classList.toggle("is-queue-waiting", isQueueWaiting);
    generationStage.textContent = stage;
    generationStageDetail.textContent = detail;
    generationProgressbar.hidden = isQueueWaiting;
    generationProgressbarFill.classList.remove("is-indeterminate");
    generationProgressbar.removeAttribute("aria-valuenow");
    if (isQueueWaiting) {
      generationProgressbarFill.style.width = "0%";
      return;
    }
    if (percent === null) {
      generationProgressbarFill.classList.add("is-indeterminate");
      generationProgressbarFill.style.width = "34%";
      return;
    }
    const bounded = clamp(Math.round(percent), 0, 100);
    generationProgressbar.setAttribute("aria-valuenow", String(bounded));
    generationProgressbarFill.classList.remove("is-indeterminate");
    generationProgressbarFill.style.width = `${bounded}%`;
  };

  const setQueueProgress = (detail) => {
    setGenerationProgress("В очереди ComfyUI", detail, null, "queue");
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
    root.dataset.generationAverageSeconds = String(Math.max(0, Number(queue?.average_task_seconds) || 0));
    syncBatchBuilder();
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

  const queueResource = feedback.createResource({
    load: async signal => {
      const queue = await feedback.requestJSON("/generate/queue", { signal, message: "Очередь не обновлена. Оценка ожидания временно недоступна." });
      if (![queue.running, queue.pending].every(value => Number.isFinite(Number(value)) && Number(value) >= 0)) throw feedback.failure("invalid", "Состояние очереди не подтверждено. Повторите обновление.");
      return queue;
    },
    onValue: queue => renderQueueOverview(queue),
    onState: state => {
      showReadFeedback("generation-queue", state, { loading: "Проверяем очередь..." });
      if (state.phase === "error" || state.authRequired) {
        root.dataset.generationAverageSeconds = "0";
        syncBatchBuilder();
        if (generationQueue) generationQueue.hidden = true;
      }
    },
  });
  const refreshQueueOverview = () => queueResource.refresh();
  document.getElementById("generation-queue-refresh")?.addEventListener("click", refreshQueueOverview);



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

  const renderSubmissionProgress = (message) => {
    const data = message.data;
    switch (message.type) {
      case "execution_start":
        setGenerationProgress("Генерация началась", "Подготавливаем модель", null);
        break;
      case "executing":
        if (data.node === null) setGenerationProgress("Завершаем", "Получаем результат", 100);
        else setGenerationProgress(workflowStage(data.node), "Выполняется текущий этап", null);
        break;
      case "progress": {
        const max = Number(data.max) || 0, value = Number(data.value) || 0;
        setGenerationProgress(workflowStage(data.node), max > 0 ? `${value} из ${max} шагов` : "Выполняется", max > 0 ? value / max * 100 : null);
        break;
      }
    }
  };

  const lightboxFocusTrap = window.AIGatewayDialogFocus?.createFocusTrap?.({ root: lightbox, documentRef: document }) || null;
  const imagePickerFocusTrap = window.AIGatewayDialogFocus?.createFocusTrap?.({ root: imagePicker, documentRef: document }) || null;
  const lightboxController = generationModules.lightbox.createController({
    elements: { root: lightbox, image: lightboxImage, video: lightboxVideo, viewport: lightboxViewport, zoom: lightboxZoom, scale: lightboxScale, name: lightboxName, download: lightboxDownload, compare: lightboxCompare, title: lightboxTitle },
    documentRef: document, windowRef: window, store: generationStore,
    sensitiveContent: { reveal: button => window.aiGatewaySensitiveContent?.reveal?.(button) }, focusTrap: lightboxFocusTrap,
  });
  const closeLightbox = () => lightboxController.close();

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

  const openLightbox = output => lightboxController.open(output);

  lightbox?.querySelectorAll("[data-lightbox-close]").forEach((button) => button.addEventListener("click", closeLightbox));
  if (!lightboxFocusTrap) document.addEventListener("keydown", (event) => { if (event.key === "Escape") closeLightbox(); });
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

  const resultsView = generationModules.results.createView({
    onReveal: () => studio?.show("result"),
    documentRef: document, windowRef: window, viewer: lightboxController, feedback,
    downloadURL: generationModules.lightbox.downloadURL, batchAPI: generationModules.batch,
    sensitiveContent: { reveal: button => window.aiGatewaySensitiveContent?.reveal?.(button) },
    postprocess: window.AIGatewayPostprocess, retention: generationRetentionLabel,
    openLoraComparison: id => loraVerificationController?.openComparison(id),
  });
  const renderOutputs = outputs => resultsView.outputs(outputs);
  viewGenerationResult?.addEventListener("click", async () => {
    if (!resultSourceJobID) return;
    try { await refreshJobs(); } catch (_) {}
    if (resultsView.openJob(resultSourceJobID)) result.classList.add("has-viewer");
    else outputGrid.querySelector(".generation-output-preview")?.click();
  });
  const renderJobMedia = job => resultsView.jobMedia(job);
  const formatJobDuration = generationModules.results.formatDuration;

  const showStep = (step) => {
    // Older saved drafts contain a step number; the studio has one editor now.
    wizardSlice.dispatch({ type: "SHOW_STEP", step });
    // A late draft must not hide an already recovered result on mobile.
    if (!draftController?.restoring) studio?.show("configure");
  };

  const selectedChoice = () => root.querySelector(".scenario-choice.is-selected");
  const selectedGenerationWorkflow = () => root.querySelector(".generation-workflow-choice.is-selected");
  const isMiniMaxSelected = () => selectedGenerationWorkflow()?.dataset.family === "minimax_h3" || templateID.value === "minimax-h3-video";
  const isAIHubSelected = () => selectedGenerationWorkflow()?.dataset.presetId === "aihub-minimax-h3";
  const isKreaProSelected = () => selectedGenerationWorkflow()?.dataset.presetId === "krea2-pro";
  const isKreaUltraSelected = () => ["krea2-ultra","krea2-pro"].includes(selectedGenerationWorkflow()?.dataset.presetId);
  const kreaProMode = () => form.elements.krea_pro_mode?.value || "text";
  const applyKreaProSourceSize = () => {
    if (!isKreaProSelected() || kreaProMode() === "text" || !form.elements.krea_pro_match_source?.checked || !primaryImageSize) return;
    const source = primaryImageSize, pixels = numericValue(outputMegapixels?.value,1.9)*1024*1024;
    const scale = Math.min(Math.sqrt(pixels/(source.width*source.height)),4096/Math.max(source.width,source.height));
    width.value = String(Math.max(256,Math.floor(source.width*scale/16)*16));
    height.value = String(Math.max(256,Math.floor(source.height*scale/16)*16));
    aspect.value = "custom";
  };
  let kreaProUI = null;
  const aiHubMode = () => form.querySelector('[name="aihub_mode"]:checked')?.value || "video";
  const aiHubCarouselV2 = () => aiHubMode() === "carousel" && form.elements.aihub_carousel_pipeline?.value === "v2";
  const aiHubCarouselPrompt = () => aiHubCarouselV2() && form.elements.aihub_carousel_source?.value === "prompt";
  const aiHubMatchesDuration = () => isAIHubSelected() && aiHubMode() === "video" && Boolean(form.elements.aihub_match_duration?.checked);
  const aiHubReferenceDuration = () => {
    const value = Number(videoPlayer?.sourceDuration ?? miniMaxVideoPreviewMedia?.duration);
    return Number.isFinite(value) && value > 0 ? value : null;
  };
  const syncAIHubDuration = () => {
    const match = aiHubMatchesDuration();
    const duration = aiHubReferenceDuration();
    const control = form.elements.aihub_match_duration;
    const status = document.getElementById("aihub-duration-status");
    // Native media.duration can include a trailing audio packet; the server checks the video stream.
    const invalid = match && duration !== null && (duration > 30 + 1 / 24 || duration < 5 / 24);
    control?.setCustomValidity(invalid ? "Длина видеореференса должна быть от 5 кадров до 30 секунд" : "");
    if (status) status.textContent = invalid ? "Референс вне диапазона: от 5 кадров до 30 секунд"
      : duration === null ? (match ? "Длительность будет определена при проверке · до 30 секунд" : "Весь ролик · до 30 секунд")
      : `${duration.toLocaleString("ru-RU", { maximumFractionDigits: 3 })} сек. · весь ролик`;
    for (const name of ["video_duration_seconds", "video_reference_start"]) {
      const input = form.elements[name];
      const label = input?.closest("label");
      if (!label) continue;
      if (match) {
        if (!label.hasAttribute("data-duration-prior-hidden")) label.dataset.durationPriorHidden = String(label.hidden);
        label.hidden = true;
        input.disabled = true;
      } else if (label.hasAttribute("data-duration-prior-hidden")) {
        label.hidden = isAIHubSelected() ? aiHubMode() === "carousel" || name === "video_reference_start" && aiHubMode() === "photo" : label.dataset.durationPriorHidden === "true";
        input.disabled = label.hidden || Boolean(input.closest("[inert]"));
        delete label.dataset.durationPriorHidden;
      }
    }
  };
  const maxInputImages = () => Math.max(1, Number(selectedGenerationWorkflow()?.dataset.maxInputImages || (templateID.value === "minimax-h3-video" ? 4 : 1)));
  const activeMaxInputImages = () => isKreaProSelected() ? ({text:0,identity:2,control:1,inpaint:2,outpaint:1}[kreaProMode()] ?? 4) : isAIHubSelected() ? (aiHubMode() === "carousel" ? 5 : 9) : generationModules.studio.activeImageLimit({
    requiresImage: wizardSlice.get().requiresImage, allowsImages: wizardSlice.get().allowsImages,
    isVideo: isMiniMaxSelected(), videoMode: miniMaxMode(), maximum: maxInputImages(),
  });

  const miniMaxReferencesAreAvailable = () => generationModules.video?.referencesAvailable?.({
    isMiniMax: isMiniMaxSelected(),
    mode: miniMaxMode(),
  }) ?? (isMiniMaxSelected() && miniMaxMode() === "references");
  const miniMaxAudioIsAvailable = () => miniMaxReferencesAreAvailable() && (!isAIHubSelected() || (aiHubMode() !== "carousel" && form.elements.aihub_voice_conversion?.checked));
  const miniMaxVideoIsAvailable = () => miniMaxReferencesAreAvailable() && (!isAIHubSelected() || aiHubMode() !== "photo" && !aiHubCarouselPrompt());

  const syncMiniMaxAudioReference = () => {
    const available = miniMaxAudioIsAvailable();
    const videoAvailable = miniMaxVideoIsAvailable();
    if (miniMaxReferenceMedia) miniMaxReferenceMedia.hidden = !videoAvailable && !available;
    if (miniMaxAudioReference) miniMaxAudioReference.hidden = !available;
    if (miniMaxVideoReference) miniMaxVideoReference.hidden = !videoAvailable;
    if (!available) audioPlayer?.pause();
    if (!videoAvailable) videoPlayer?.pause();
    if (inputAudio) inputAudio.value = available ? uploadedAudio : "";
    if (inputVideo) inputVideo.value = videoAvailable ? uploadedVideo : "";
  };

  const hasPendingMiniMaxAudio = () => miniMaxAudioIsAvailable() && Boolean(miniMaxAudioFile?.files?.[0]) && !uploadedAudio;
  const hasPendingMiniMaxVideo = () => miniMaxVideoIsAvailable() && Boolean(miniMaxVideoFile?.files?.[0]) && !uploadedVideo;

  const clearMiniMaxAudio = () => {
    audioPlayer?.clear();
    draftAssets.delete("input_audio");
    if (miniMaxAudioFile) miniMaxAudioFile.value = "";
    uploadedAudio = "";
    if (inputAudio) inputAudio.value = "";
    if (miniMaxAudioName) miniMaxAudioName.textContent = "";
    if (miniMaxAudioState) miniMaxAudioState.textContent = "Готово к загрузке";
    if (miniMaxAudioPreview) miniMaxAudioPreview.hidden = true;
  };

  const clearMiniMaxVideo = () => {
    draftAssets.delete("input_video");
    if (miniMaxVideoFile) miniMaxVideoFile.value = "";
    uploadedVideo = "";
    if (inputVideo) inputVideo.value = "";
    videoPlayer?.clear();
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
    restoredImages.delete(item.index);
    draftAssets.delete(imageDraftField(item.index));
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
    syncPromptAssistant();
  };

  const syncImageSlots = () => {
    const isMiniMax = isMiniMaxSelected() || templateID.value === "minimax-h3-video";
    const isImageEdit = templateID.value === "image-to-image" || isKreaProSelected() && kreaProMode() !== "text";
    const referenceMode = miniMaxMode() === "references";
    const wizard = wizardSlice.get();
    const maximum = (wizard.requiresImage || wizard.allowsImages) ? activeMaxInputImages() : 0;
    const lastSelected = imageSlots.filter(item => item.index <= maximum && (hasSelectedImage(item) || uploadedImages.get(item.index))).reduce((last, item) => Math.max(last, item.index), 0);
    const isKrea = selectedGenerationWorkflow()?.dataset.family === "krea2";
    if (imageSourceGrid) imageSourceGrid.dataset.visibleSlots = String(maximum);
    imageSlots.forEach((item) => {
      const visible = item.index <= maximum && (!isAIHubSelected() || item.index <= lastSelected + 1);
      item.slot.hidden = !visible;
      if (inputImages[item.index - 1]) inputImages[item.index - 1].value = visible ? (uploadedImages.get(item.index) || "") : "";
      if (item.galleryChoice) item.galleryChoice.hidden = !(wizard.requiresImage || wizard.allowsImages);
      if (!item.label) return;
      if (isAIHubSelected() && aiHubMode() !== "video" && item.index === 1) item.label.textContent = "Фото 1 · обязательно";
      else if (isMiniMax && referenceMode) item.label.textContent = `Референс ${item.index} · необязательно`;
      else if (isMiniMax) item.label.textContent = item.index === 1 ? "Первый кадр · необязательно" : "Последний кадр · необязательно";
      else if (isKreaProSelected() && kreaProMode() === "inpaint" && item.index === 2) item.label.textContent = "Фото 2 · маска";
      else if (item.index === 1) item.label.textContent = "Фото 1 · обязательно";
      else item.label.textContent = isKrea && item.index === 2 ? "Фото 2 · дополнительное" : `Фото ${item.index} · референс`;
      const roleControl = item.role?.closest(".image-reference-role");
      if (roleControl) roleControl.hidden = !(isImageEdit || (isMiniMax && referenceMode));
      if (item.role) {
        const locksBaseScene = isImageEdit && item.index === 1;
        if (locksBaseScene) {
          if (item.savedReferenceRole === undefined) item.savedReferenceRole = item.role.value;
          item.role.value = "base_scene";
        } else if (!locksBaseScene && item.savedReferenceRole !== undefined) {
          item.role.value = item.savedReferenceRole;
          delete item.savedReferenceRole;
        }
        item.role.disabled = !(isImageEdit || (isMiniMax && referenceMode)) || locksBaseScene;
      }
    });
    const held = imageSlots.filter((item) => item.index > maximum && (hasSelectedImage(item) || draftAssets.has(imageDraftField(item.index)))).map((item) => `Фото ${item.index}`);
    if (!miniMaxAudioIsAvailable() && (uploadedAudio || miniMaxAudioFile?.files?.length || draftAssets.has("input_audio"))) held.push("аудио");
    if (!miniMaxVideoIsAvailable() && (uploadedVideo || miniMaxVideoFile?.files?.length || draftAssets.has("input_video"))) held.push("видео");
    const heldNotice = root.querySelector("#studio-held-media");
    if (heldNotice) {
      heldNotice.hidden = held.length === 0;
      heldNotice.textContent = `${held.join(", ")} сохранены в черновике, но не участвуют в этом режиме. Вернитесь к совместимому режиму, чтобы использовать или удалить их.`;
    }
    const note = document.getElementById("image-source-note");
    if (isMiniMax && note) {
      note.textContent = isAIHubSelected()
        ? aiHubMode() === "carousel" ? "Фото 1 обязательно · до 5 фото" : aiHubMode() === "photo" ? "Фото 1 обязательно · до 9 фото-референсов" : "До 9 фото-референсов"
        : referenceMode
        ? "Файлы необязательны. Добавьте до четырёх фото с устройства или из своей галереи, а также видео и/или аудио; для каждого фото укажите его роль."
        : "Фото необязательны: без них работает текст в видео. Первый и последний кадры можно загрузить с устройства или выбрать из своей галереи.";
      syncReferenceMap();
      syncMiniMaxAudioReference();
      syncPromptAssistant();
      return;
    }
    if (note) note.textContent = maximum > 1
      ? isImageEdit
        ? "Фото 1 задаёт базовую сцену. Для каждого дополнительного фото выберите роль, чтобы ассистент использовал референсы без путаницы."
        : `Первое фото обязательно. Можно добавить ещё до ${maximum - 1} ${maximum === 2 ? "референса" : "референсов"}.`
      : "Загрузите исходное фото для редактирования.";
    syncReferenceMap();
    syncMiniMaxAudioReference();
    syncPromptAssistant();
  };

  const chooseScenario = (button) => {
    if (button.disabled || button === selectedChoice()) return;
    root.querySelectorAll(".scenario-choice").forEach((item) => { item.classList.remove("is-selected"); item.setAttribute("aria-pressed", "false"); });
    button.classList.add("is-selected");
    button.setAttribute("aria-pressed", "true");
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
    if (compatibleWorkflows.length) chooseGenerationWorkflow(compatibleWorkflows[0]);
    else syncWorkflowFields();
    showStep(2);
  };

  const syncStudioSelection = () => {
    const selected = selectedGenerationWorkflow();
    root.querySelectorAll(".generation-workflow-choice").forEach(item => item.setAttribute("aria-pressed", String(item === selected)));
    studio?.syncWorkflowPicker();
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
    wizardSlice.dispatch({
      type: "SET_SELECTIONS",
      selectedCount: imageSlots.filter((item) => item.index <= activeMaxInputImages() && hasSelectedImage(item)).length,
      primarySelected: hasImage,
      pendingUploads: hasPendingUploads ? 1 : 0,
    }, (state) => ({ ...state, selectedCount: hasImage ? 1 : 0, primarySelected: hasImage, pendingUploads: hasPendingUploads ? 1 : 0 }));
    root.dataset.studioReady = String(hasWorkflow && hasModel && (!needsImage || hasImage));
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
    syncStudioSelection();
  };

  let ordinaryMiniMaxMode = "frames";
  const chooseGenerationWorkflow = (button) => {
    if (button.disabled || button.hidden) return;
    const previous = selectedGenerationWorkflow();
    if (previous === button) return;
    if (previous?.dataset.presetId === "minimax-h3-video") ordinaryMiniMaxMode = miniMaxMode();
    root.querySelectorAll(".generation-workflow-choice").forEach((item) => {
      item.classList.remove("is-selected");
      item.setAttribute("aria-pressed", "false");
    });
    button.classList.add("is-selected");
    button.setAttribute("aria-pressed", "true");
    generationWorkflowID.value = button.dataset.presetId;
    wizardSlice.dispatch({ type: "SELECT_WORKFLOW", workflowID: button.dataset.presetId, available: button.dataset.available === "true", requiresImage: button.dataset.requiresImage === "true", allowsImages: button.dataset.allowsImages === "true" }, (state) => ({
      ...state,
      workflowID: button.dataset.presetId || "",
      workflowAvailable: button.dataset.available === "true",
      requiresImage: button.dataset.requiresImage === "true", allowsImages: button.dataset.allowsImages === "true",
    }));
    updateQuickModelOptions(button);
    if (generationModelField) generationModelField.hidden = false;
    if (button.dataset.family === "qwen21" && previous?.dataset.family !== "qwen21" && outputMegapixels) outputMegapixels.value = "1";
    applyQuality();
    if (button.dataset.family === "minimax_h3") syncMiniMaxVideoProfile({ applyModelDefaults: true });
    if (button.dataset.presetId === "minimax-h3-video") {
      setMiniMaxMode(ordinaryMiniMaxMode);
    }
    syncWorkflowFields();
    syncImageSlots();
    syncStudioSelection();
  };

  const kreaModules = [...root.querySelectorAll("[data-krea-module]")];
  const syncKreaOptionalModules = () => {
    kreaModules.forEach((module) => {
      const toggle = module.querySelector("[data-krea-module-toggle]");
      const options = module.querySelector("[data-krea-module-options]");
      const state = module.querySelector("[data-krea-module-state]");
      if (!toggle || !options) return;
      const enabled = toggle.checked;
      module.classList.toggle("is-enabled", enabled);
      options.hidden = !enabled;
      toggle.setAttribute("aria-expanded", String(enabled));
      if (state) state.textContent = enabled ? "Включено" : "Выключено";
      options.querySelectorAll("input, select, textarea").forEach((control) => {
        control.disabled = !enabled || toggle.disabled;
      });
    });
  };

  kreaModules.forEach((module) => {
    module.querySelector("[data-krea-module-toggle]")?.addEventListener("change", () => {
      syncKreaOptionalModules();
      syncGenerationSummary();
      syncBatchBuilder();
    });
  });

  const activeLoraSlotKind = () => {
    const family = selectedGenerationWorkflow()?.dataset.family || "";
    if (family === "minimax_h3") return "minimax";
    if (family === "flux2") return "flux";
    if (family === "krea2") return "krea";
    if (family === "moss") return "moss";
    if (family === "qwen21") return "qwen21";
    return "";
  };

  const generationPayload = generationModules.payload.createAdapter({ form, root, loraKind: activeLoraSlotKind });
  const namedControlCandidates = generationPayload.controls;
  const setNamedControlValue = generationPayload.set;

  const applySavedValues = (values, { openStep = true } = {}) => {
    if (!values || typeof values !== "object") return false;
    let selection;
    try { selection = generationPayload.resolveSelection(values); } catch (_) { return false; }
    const { scenario, workflow } = selection;
    if (scenario && !scenario.disabled && !scenario.classList.contains("is-selected")) chooseScenario(scenario);
    if (workflow && !workflow.disabled && !workflow.hidden && !workflow.classList.contains("is-selected")) chooseGenerationWorkflow(workflow);
    syncWorkflowFields();
    generationPayload.apply(values, { afterLora: () => syncAdaptiveLoraSlots(activeLoraSlotKind()) });
    syncAdaptiveLoraSlots("krea");
    syncAdaptiveLoraSlots("flux");
    syncWorkflowFields();
    syncKreaOptionalModules();
    calculateResolution();
    syncMiniMaxVideoProfile();
    syncMiniMaxSharpenFields();
    if (openStep) {
      const needsImage = selectedChoice()?.dataset.requiresImage === "true";
      showStep(needsImage && !uploadedImages.get(1) ? 2 : 3);
    }
    markDraftDirty();
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
    if (jobSourceRequested) return;
    if (!requestedVariantID || requestedVariantHandled) return;
    requestedVariantHandled = true;
    const variant = historySlice.get().variants.find((item) => String(item.id) === requestedVariantID);
    if (!variant) {
      showRepeatNotice("Вариант больше недоступен", `История хранится ${generationRetentionLabel}. Выберите другой результат в галерее.`, true);
      clearRequestedVariantQuery();
      return;
    }
    if (window.AIGatewayPostprocess?.isJob(variant)) {
      showRepeatNotice("Это постобработка готового файла", "Параметры генерации не изменены. Откройте результат в галерее.", true);
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
      const beta6 = integratedTurbo && Number(option?.dataset.defaultSteps) === 10;
      const draftSteps = beta6 ? "8" : integratedTurbo ? "6" : externalTurbo ? "4" : "20";
      const balancedSteps = beta6 ? "10" : integratedTurbo ? "8" : externalTurbo ? "6" : "25";
      const maximumSteps = beta6 ? "12" : integratedTurbo ? "8" : externalTurbo ? "8" : "25";
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
    } else if (family === "krea2" && isKreaUltraSelected()) {
      Object.assign(values, profile === "draft"
        ? { output_megapixels: "1", steps: "8", cfg: "2", krea_ultra_step_swap: "5" }
        : profile === "maximum"
          ? { output_megapixels: "4.27", steps: "14", cfg: "2", krea_ultra_step_swap: "8" }
          : { output_megapixels: "1.9", steps: "10", cfg: "2", krea_ultra_step_swap: "6" });
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
    } else if (family === "moss") {
      Object.assign(values, profile === "draft"
        ? { steps: "20", cfg: "3", output_megapixels: "1", sampler: "euler", scheduler: "simple" }
        : profile === "maximum"
          ? { steps: "50", cfg: "3", output_megapixels: "2", sampler: "euler", scheduler: "simple" }
          : { steps: "30", cfg: "3", output_megapixels: "1", sampler: "euler", scheduler: "simple" });
    } else if (family === "qwen21") {
      Object.assign(values, profile === "draft"
        ? { steps: "16", cfg: "1", width: "768", height: "768", output_megapixels: "0.6" }
        : profile === "maximum"
          ? { steps: "40", cfg: "1", width: "1248", height: "1248", output_megapixels: "1.5" }
          : { steps: "25", cfg: "1", width: "1024", height: "1024", output_megapixels: "1" });
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
    const hasVideo = Boolean(miniMaxVideoIsAvailable() && (uploadedVideo || miniMaxVideoFile?.files?.[0]));
    return { preset, selectedModel, family, references, hasAudio, hasVideo };
  };

  const batchQuotaSnapshot = () => {
    const kind = templateID?.value === "minimax-h3-video" ? "Video" : "Image";
    return {
      dailyLimit: numericValue(root.dataset[`generation${kind}DailyLimit`]),
      dailyRemaining: numericValue(root.dataset[`generation${kind}DailyRemaining`]),
      totalLimit: numericValue(root.dataset[`generation${kind}TotalLimit`]),
      totalRemaining: numericValue(root.dataset[`generation${kind}TotalRemaining`]),
    };
  };

  const activeWorkflowControl = (name) => workflowControls(name).find((control) => (
    !control.disabled && !control.closest("[hidden]")
  )) || workflowControls(name)[0] || null;

  const batchContext = () => {
    const { family } = currentGenerationContext();
    return {
      family,
      templateID: templateID?.value || "",
      sparseAttention: Boolean(form.elements.video_sparse_attention?.checked),
      rife: Boolean(form.elements.video_rife_enabled?.checked),
      rtx: Boolean(form.elements.video_rtx_enabled?.checked),
      colorMatch: Boolean(form.elements.video_color_match?.checked),
      sharpen: Boolean(form.elements.video_sharpen_enabled?.checked),
      sharpenMax: numericValue(form.elements.video_sharpen_strength?.max, 1),
      loras: [...root.querySelectorAll(".lora-row:not([hidden]) .generation-lora-select")]
        .filter((control) => !control.disabled)
        .map((control) => control.value),
    };
  };

  const currentBatchConfig = () => ({
    enabled: Boolean(batchEnabled?.checked),
    mode: form.elements.batch_mode?.value || "seeds",
    count: batchCount?.value ?? "4",
    parameter: batchParameter?.value || "",
    from: batchFrom?.value || "",
    to: batchTo?.value || "",
  });

  const formatBatchNumber = (value, step = 1) => {
    const precision = Math.min(6, Math.max(0, String(step).split(".")[1]?.length || 0));
    return Number(value.toFixed(precision)).toString();
  };

  const batchVariantCountLabel = (count) => {
    const value = Math.max(0, Math.round(Number(count) || 0));
    const lastTwo = value % 100;
    if (lastTwo >= 11 && lastTwo <= 14) return `${value} вариантов`;
    if (value % 10 === 1) return `${value} вариант`;
    if (value % 10 >= 2 && value % 10 <= 4) return `${value} варианта`;
    return `${value} вариантов`;
  };

  const setDefaultBatchRange = (option, count) => {
    if (!option || !batchFrom || !batchTo) return;
    const current = numericValue(activeWorkflowControl(option.name)?.value, option.min);
    const span = Math.max(option.step, (count - 1) * option.step);
    let from = current - Math.floor((count - 1) / 2) * option.step;
    from = clamp(from, option.min, Math.max(option.min, option.max - span));
    const to = clamp(from + span, option.min, option.max);
    batchFrom.value = formatBatchNumber(from, option.step);
    batchTo.value = formatBatchNumber(to, option.step);
  };

  let batchParameterSignature = "";
  const syncBatchBuilder = ({ resetRange = false } = {}) => {
    if (loraVerificationController?.sync()) {
      if (generationSubmit && (submitInFlight || submissionBlocksLaunch())) generationSubmit.disabled = true;
      return { valid: true, count: 1, error: "" };
    }
    if (!batchBuilder || !generationModules.batch) return { valid: true, count: 1, error: "" };
    const enabled = Boolean(batchEnabled?.checked);
    const mode = form.elements.batch_mode?.value || "seeds";
    const count = generationModules.batch.boundedCount(batchCount?.value);
    if (batchControls) batchControls.hidden = !enabled;
    if (batchParameterField) batchParameterField.hidden = mode !== "parameter";
    batchRangeFields.forEach((field) => { field.hidden = mode !== "parameter"; });

    const options = generationModules.batch.parameterOptions(batchContext());
    const signature = options.map((option) => `${option.name}:${option.min}:${option.max}:${option.step}`).join("|");
    const previousParameter = batchParameter?.value || "";
    if (batchParameter && (signature !== batchParameterSignature || batchParameter.options.length !== options.length + 1)) {
      const fragment = document.createDocumentFragment();
      const placeholder = document.createElement("option");
      placeholder.value = "";
      placeholder.textContent = "Выберите параметр";
      fragment.append(placeholder);
      options.forEach((option) => {
        const item = document.createElement("option");
        item.value = option.name;
        item.textContent = option.label;
        fragment.append(item);
      });
      batchParameter.replaceChildren(fragment);
      batchParameter.value = options.some((option) => option.name === previousParameter) ? previousParameter : "";
      batchParameterSignature = signature;
      resetRange = true;
    }

    const option = options.find((item) => item.name === batchParameter?.value);
    if (option && batchFrom && batchTo) {
      [batchFrom, batchTo].forEach((field) => {
        field.min = String(option.min);
        field.max = String(option.max);
        field.step = String(option.step);
      });
      if (resetRange || batchFrom.value === "" || batchTo.value === "") setDefaultBatchRange(option, count);
    }

    const config = currentBatchConfig();
    batchSlice.dispatch({ type: "SET_CONFIG", config });
    const validation = generationModules.batch.validate(config, options, batchQuotaSnapshot());
    const remaining = generationModules.batch.remainingQuota(batchQuotaSnapshot());
    const averageSeconds = numericValue(root.dataset.generationAverageSeconds);
    const estimateSeconds = averageSeconds > 0 ? averageSeconds * validation.count : 0;
    if (batchEstimate) {
      const title = batchEstimate.querySelector("strong") || document.createElement("strong");
      const detail = batchEstimate.querySelector("span") || document.createElement("span");
      title.textContent = batchVariantCountLabel(validation.count);
      const cost = Number.isFinite(remaining)
        ? `Будет списано ${validation.count}. После запуска останется ${Math.max(0, remaining - validation.count)}.`
        : `Будет списано ${validation.count} генерации.`;
      detail.textContent = estimateSeconds > 0 ? `${cost} Ориентир: ${formatJobDuration(estimateSeconds)}.` : cost;
      if (!title.parentElement) batchEstimate.append(title, detail);
      batchEstimate.classList.toggle("has-error", enabled && !validation.valid);
    }
    if (batchError) {
      batchError.hidden = !enabled || validation.valid;
      batchError.textContent = validation.error || "";
    }
    batchBuilder.classList.toggle("is-enabled", enabled);
    batchBuilder.classList.toggle("has-error", enabled && !validation.valid);
    if (generationSubmit) {
      generationSubmit.dataset.batchInvalid = enabled && !validation.valid ? "true" : "false";
      const label = enabled ? `Запустить ${batchVariantCountLabel(validation.count)}` : "Создать";
      if (generationSubmit.textContent !== label) generationSubmit.textContent = label;
      if (!generationSubmit.classList.contains("is-loading")) generationSubmit.disabled = submitInFlight || submissionBlocksLaunch() || enabled && !validation.valid;
    }
    return validation;
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
      if (isAIHubSelected() && form.elements.aihub_dlss_enabled?.checked) options.push("DLSS 5");
      if (isAIHubSelected() && aiHubMode() !== "carousel" && form.elements.aihub_fg_enabled?.checked) options.push("плавность DLSS FG");
      if (form.elements.video_rtx_enabled?.checked) options.push(`апскейл RTX ${form.elements.video_rtx_scale?.value || "2"}×`);
      if (form.elements.video_color_match?.checked) options.push("перенос палитры");
      if (form.elements.video_sharpen_enabled?.checked) options.push("финальная резкость");
      return options;
    }
    if (family === "flux2" && fluxUpscaleMode?.value && fluxUpscaleMode.value !== "none") {
      const labels = { ultimate: "Ultimate SD Upscale", seedvr2: "SeedVR2", both: "два этапа апскейла" };
      options.push(labels[fluxUpscaleMode.value] || "финальный апскейл");
    }
    if (family === "moss" && form.elements.moss_seedvr2?.checked) options.push("SeedVR2 Sharp");
    if (family === "krea2") {
      const megapixels = Number(outputMegapixels?.value || 0);
      if (templateID.value === "image-to-image") {
        const factor = Number(form.elements.upscale_factor?.value || 1);
        if (!preserveOriginalSize?.checked && factor > 1) options.push(`апскейл ${factor}×`);
      } else if (isKreaUltraSelected()) {
        options.push("Lonecat · два прохода");
        if (form.elements.krea_ultra_speed_boost?.checked) options.push("Speed Boost");
      } else {
        if (Number(upscaleSteps?.value || 0) > 0) options.push("апскейл Krea2");
        if (form.elements.krea_sage_enabled?.checked) options.push("SageAttention");
        if (form.elements.detail_enabled?.checked && Number(detailSteps?.value || 0) > 0) options.push("финальная детализация");
        if (form.elements.color_transfer?.checked) options.push("согласование цвета");
        if (form.elements.image_filter_enabled?.checked) options.push("фильтр и уровни");
      }
      if (megapixels >= 3) options.push(`${megapixels.toLocaleString("ru-RU")} Мп`);
    }
    if (!family && Number(steps?.value || 0) > 30) options.push("более 30 шагов");
    return options;
  };

  const generationOutputSummary = (family, references) => {
    const output = generationModules.output;
    if (!output) return null;
    const names = ["width", "height", "aspect_ratio", "output_megapixels", "dimension_multiple", "max_longest_side", "base_megapixels",
      "preserve_original_size", "edit_use_custom_size", "edit_aspect_preset", "edit_swap_dimensions", "edit_proportion",
      "source_megapixels", "upscale_factor", "flux_upscale_mode", "video_duration_seconds", "video_rife_enabled",
      "moss_seedvr2", "moss_upscale_resolution", "krea_pro_mode", "krea_pro_upscaler", "krea_pro_upscale", "krea_pro_relay_scale", "krea_pro_settings",
      "video_rife_multiplier", "video_rtx_enabled", "video_rtx_scale", "aihub_half_resolution", "aihub_latent_scale", "aihub_photo_frames", "aihub_photo_stride", "aihub_photo_skip", "aihub_fg_enabled", "aihub_fg_multiplier", "aihub_carousel_pipeline", "aihub_carousel_source", "aihub_carousel_refiner", "aihub_photo_count"];
    const values = Object.fromEntries(names.map(name => {
      const control = namedControlCandidates(name).find(item => !item.disabled);
      return [name, control?.type === "checkbox" ? control.checked : control?.value];
    }));
    if (isKreaProSelected()) values.generation_workflow = "krea2-pro";
    if (isAIHubSelected()) {
      values.generation_workflow = "aihub-minimax-h3"; values.aihub_mode = aiHubMode();
      values.aihub_match_duration = aiHubMatchesDuration();
      values.aihub_reference_duration = aiHubReferenceDuration();
    }
    const hasPrimary = references.some(item => Number(item.number) === 1);
    const sourceSize = hasPrimary ? primaryImageSize : null;
    const videoSize = hasPrimary && !sourceSize ? null : generationModules.video?.scaledResolution?.({
      sourceSize, aspect: miniMaxVideoAspect?.value || "9:16", swap: Boolean(miniMaxVideoSwap?.checked),
      maxResolution: Number(miniMaxVideoQuality?.value) || 720,
    });
    const config = currentBatchConfig();
    const validation = generationModules.batch?.validate(config, generationModules.batch.parameterOptions(batchContext()));
    let variants = [values];
    let variation = "";
    if (config.enabled && config.mode === "parameter") {
      if (validation?.valid && validation.option) {
        const { from, to, count, option } = validation;
        variants = Array.from({ length: count }, (_, index) => ({ ...values,
          [option.name]: Number((Math.round((from + (to - from) * index / (count - 1)) / option.step) * option.step).toFixed(6)),
        }));
        variation = `${option.label}: ${from.toLocaleString("ru-RU")} → ${to.toLocaleString("ru-RU")}`;
      } else variation = "Проверьте параметр и диапазон";
    }
    const plans = variants.map(variant => output.plan({ family, templateID: templateID.value, values: variant, sourceSize, videoSize }));
    return output.facts(plans, { video: family === "minimax_h3" && !(isAIHubSelected() && aiHubMode() === "carousel"), count: config.enabled ? config.count : 1, variation });
  };

  const syncGenerationSummary = () => {
    syncGenerationModeGuide();
    if (!generationSummaryFacts) return;
    const { preset, selectedModel, family, references, hasAudio, hasVideo } = currentGenerationContext();
    if (!preset || !selectedModel?.value) {
      if (generationSummaryTitle) generationSummaryTitle.textContent = "Выберите workflow и модель";
      renderDefinitionList(generationSummaryFacts, [{ label: "Настройки", value: "Выберите способ обработки и доступную модель" }]);
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
      outputSummary: generationOutputSummary(family, references),
      loraCount,
      heavyOptions: selectedHeavyOptions(family),
    });
    if (!summary) return;
    if (generationSummaryTitle) generationSummaryTitle.textContent = summary.title;
    if (isAIHubSelected() && miniMaxVideoResolutionPreview) {
      miniMaxVideoResolutionPreview.textContent = summary.title;
    }
    renderDefinitionList(generationSummaryFacts, summary.facts);
    if (generationSummaryImpact) {
      generationSummaryImpact.dataset.load = summary.load || "normal";
      generationSummaryImpact.textContent = summary.impact || "";
    }
  };

  const profileChanges = document.getElementById("studio-profile-changes");
  const clearProfileFeedback = () => {
    root.querySelectorAll("[data-generation-profile]").forEach(button => button.classList.remove("is-active"));
    if (profileChanges) profileChanges.hidden = true;
  };
  const profileSnapshot = (names) => Object.fromEntries(names.flatMap(name => {
    const control = namedControlCandidates(name).find(item => !item.disabled && item.type !== "hidden" && !item.closest("[hidden], [inert]"));
    if (!control) return [];
    const label = control.closest("label");
    const title = label?.querySelector(".ui-field-label, strong, b")?.textContent?.trim()
      || [...(label?.childNodes || [])].find(node => node.nodeType === 3 && node.textContent.trim())?.textContent.trim();
    if (!title) return [];
    const value = control.type === "checkbox" ? (control.checked ? "Включено" : "Выключено")
      : control.tagName === "SELECT" ? control.selectedOptions[0]?.textContent.trim() || "Не выбрано" : control.value;
    return [[name, { label: title, value }]];
  }));
  const applyGenerationProfile = (profile) => {
    if (!selectedGenerationWorkflow()) return;
    const names = [...new Set([...Object.keys(profileValues(profile)), "width", "height", "output_megapixels", "base_megapixels"])];
    const before = profileSnapshot(names);
    Object.entries(profileValues(profile)).forEach(([name, value]) => setNamedControlValue(name, value));
    root.querySelectorAll("[data-generation-profile]").forEach((button) => button.classList.toggle("is-active", button.dataset.generationProfile === profile));
    if (quality && selectedGenerationWorkflow()?.dataset.family === "krea2" && templateID.value !== "image-to-image" && !isKreaUltraSelected()) {
      quality.value = profile === "draft" ? "fast" : profile === "maximum" ? "krea-4-7" : "balanced";
    }
    calculateResolution();
    syncMiniMaxVideoProfile();
    syncGenerationSummary();
    const changes = generationModules.studio.settingChanges(before, profileSnapshot(names));
    if (profileChanges) {
      renderDefinitionList(profileChanges.querySelector("dl"), changes.map(change => ({ label: change.label, value: `${change.before} → ${change.after}` })));
      profileChanges.hidden = changes.length === 0;
    }
    markDraftDirty();
  };

  const updateQuickModelOptions = (workflow) => {
    if (!model || !workflow) return;
    clearProfileFeedback();
    [...model.options].forEach((option) => {
      if (!option.value) return;
      const allowed = option.dataset.family === workflow.dataset.family && (workflow.dataset.presetId === "aihub-minimax-h3" ? option.dataset.aihubOnly === "true" : option.dataset.aihubOnly !== "true") && (workflow.dataset.presetId !== "krea2-ultra" || !option.textContent.toLowerCase().includes("raw"));
      option.hidden = !allowed;
      option.disabled = option.dataset.available !== "true" || !allowed;
    });
    const defaultID = workflow.dataset.defaultModelId || "";
    const defaultOption = generationModules.studio.chooseModel([...model.options], defaultID);
    model.value = defaultOption?.value || "";
  };

  const syncAdaptiveLoraSlots = (kind) => {
    const rows = [...root.querySelectorAll(`[data-lora-slots="${kind}"]`)];
    const lastSelected = rows.findLastIndex(row => row.querySelector(".generation-lora-select")?.value);
    rows.forEach((row, index) => {
      const visible = index <= lastSelected + 1;
      row.hidden = !visible;
      row.querySelectorAll("input, select").forEach((control) => { control.disabled = !visible; });
      const select = row.querySelector(".generation-lora-select");
      const remove = row.querySelector(".generation-lora-remove");
      if (remove) {
        remove.disabled = !visible || !select?.value;
        remove.style.visibility = select?.value ? "" : "hidden";
        remove.title = select?.value ? `Удалить LoRA: ${select.selectedOptions[0].textContent}` : "Удалить LoRA";
        remove.setAttribute("aria-label", remove.title);
      }
    });
  };
  const compactLoraSlots = (kind) => {
    const rows = [...root.querySelectorAll(`[data-lora-slots="${kind}"]`)];
    const fields = [".generation-lora-select", ".generation-lora-model", '[name^="lora_clip_strength_"]'];
    // Compact only after an explicit removal, not during incremental draft restoration.
    const selected = rows.filter(row => row.querySelector(fields[0])?.value)
      .map(row => fields.map(selector => row.querySelector(selector)?.value));
    rows.forEach((row, index) => {
      fields.forEach((selector, field) => {
        const control = row.querySelector(selector);
        if (control) control.value = selected[index]?.[field] ?? ["", "0", "1"][field];
      });
    });
  };

  const setPromptAssistantState = (message, state = "") => {
    if (!promptAssistantState) return;
    const launchState = document.getElementById("prompt-assistant-launch-state");
    feedback.text(launchState, { loading: "Готовится вариант", review: "Вариант готов", approved: "Выбор сохранён", restored: "Исходный текст возвращён", stale: "Вариант неактуален", error: "Нужна проверка" }[state] || "");
    if (message === "нельзя создавать сексуализированный контент с участием несовершеннолетних или персонажей с неоднозначным возрастом") {
      const title = document.createElement("strong");
      title.textContent = "Запрос не обработан";
      const detail = document.createElement("span");
      detail.textContent = "Нельзя создавать сексуализированный контент с участием несовершеннолетних или персонажей с неопределённым возрастом. Измените описание и попробуйте снова.";
      promptAssistantState.replaceChildren(title, detail);
      feedback.present(promptAssistantState, "safety");
      return;
    }
    feedback.present(promptAssistantState, state, message);
  };

  const assistantModelState = () => {
    const option = promptAssistantModel?.selectedOptions[0];
    const selected = Boolean(promptAssistantModel?.value);
    const unavailable = selected && (!durableAssistantEnabled || option?.dataset.available !== "true");
    const needsVision = selectedReferenceMetadata().length > 0 || isAIHubSelected() && miniMaxVideoIsAvailable() && Boolean(uploadedVideo || miniMaxVideoFile?.files?.length);
    const unsupported = selected && !unavailable && needsVision && option?.dataset.vision !== "true";
    return { blocked: unavailable || unsupported, message: unavailable
      ? "Сохранённая модель недоступна. Выберите другую модель ассистента."
      : unsupported ? "Эта модель не поддерживает изображения. Выберите модель с анализом референсов."
        : promptAssistantModel?.dataset.loadError === "true" ? "Список дополнительных моделей не загрузился. Обновите страницу." : "" };
  };
  const syncAssistantModel = () => {
    const status = assistantModelState();
    const field = document.getElementById("prompt-assistant-model-field");
    const message = document.getElementById("prompt-assistant-model-status");
    if (field) field.hidden = !promptAssistantModel?.value && !(durableAssistantEnabled && promptAssistantModel?.options.length > 1);
    if (message) { message.textContent = status.message; message.hidden = !status.message; }
    const session = assistantSession?.snapshot();
    promptAssistantImprove.disabled = status.blocked || !session?.ready || session.busy || session.deciding;
  };
  const restoreAssistantModel = (id = "") => {
    if (!promptAssistantModel) return;
    promptAssistantModel.querySelectorAll("[data-missing]").forEach(option => option.remove());
    if (id && ![...promptAssistantModel.options].some(option => option.value === id)) {
      const option = new Option("Сохранённая модель (недоступна)", id);
      option.dataset.missing = "true";
      option.disabled = true;
      promptAssistantModel.append(option);
    }
    promptAssistantModel.value = id;
    syncAssistantModel();
  };
  const captureAssistantInput = () => ({
    prompt: positive?.value || "", draft: promptAssistantDraft?.value || "", enabled: Boolean(promptAssistantEnabled?.checked),
    negative: negativePrompt?.value || "", negativeDraft: promptAssistantNegativeDraft?.value || "", cfg: (cfg?.value || "1").replace(",", "."),
    workflow: generationWorkflowID?.value, model: model?.value, mode: isKreaProSelected() && kreaProMode() !== "text" ? "image-to-image" : templateID.value,
    kreaProMode: isKreaProSelected() ? kreaProMode() : "",
    videoMode: miniMaxMode(), duration: form.elements.video_duration_seconds?.value,
    longVideo: { enabled: miniMaxLongEnabled(), parts: Number(form.elements.video_long_parts?.value || 2), ...(miniMaxLongEnabled() ? { transition: form.elements.video_long_transition?.value || "continuous" } : {}) },
    profile: promptAssistantTemplate?.value, think: Boolean(promptAssistantThink?.checked),
    assistantModel: promptAssistantModel?.value || "",
    ...(isAIHubSelected() ? { aiHub: { mode: aiHubMode(), audioSource: aiHubMode() === "carousel" ? "native" : form.elements.aihub_audio_source?.value || "native",
      carouselPipeline: form.elements.aihub_carousel_pipeline?.value || "legacy",
      carouselRefiner: form.elements.aihub_carousel_refiner?.value || "flux",
      carouselSource: form.elements.aihub_carousel_source?.value || "video",
      task: aiHubMode() === "video" ? form.elements.aihub_task?.value || "reference" : "reference",
      audioReference: !form.elements.aihub_audio_reference?.disabled && Boolean(form.elements.aihub_audio_reference?.checked),
      t8: !form.elements.aihub_t8?.disabled && Boolean(form.elements.aihub_t8?.checked),
      audioPrompt: aiHubMode() !== "carousel" && form.elements.aihub_audio_source?.value === "ltx" ? form.elements.aihub_audio_prompt?.value || "" : "",
      voiceConversion: aiHubMode() !== "carousel" && Boolean(form.elements.aihub_voice_conversion?.checked),
      matchDuration: aiHubMatchesDuration(),
      photoFrames: aiHubMode() === "carousel" ? form.elements.aihub_photo_frames?.value : "",
      videoStart: miniMaxVideoIsAvailable() ? form.elements.video_reference_start?.value || "0" : "0" } } : {}),
    images: selectedReferenceMetadata().map(ref => ({ number: ref.number, role: ref.role,
      local: previewURLs.get(ref.number) || ref.sourceName, value: uploadedImages.get(ref.number) || "" })),
    audio: { local: miniMaxAudioIsAvailable() ? miniMaxAudioFile?.files?.[0] || uploadedAudio : "", value: miniMaxAudioIsAvailable() ? uploadedAudio : "" },
    video: { local: miniMaxVideoIsAvailable() ? miniMaxVideoFile?.files?.[0] || uploadedVideo : "", value: miniMaxVideoIsAvailable() ? uploadedVideo : "" },
  });
  const invalidatePromptAssistant = () => assistantSession?.invalidate();
  const renderAssistantSources = () => {
    const list = document.getElementById("prompt-assistant-source-list");
    const section = document.getElementById("prompt-assistant-sources");
    if (!list || !section) return;
    const references = selectedReferenceMetadata();
    section.hidden = references.length === 0;
    list.replaceChildren(...references.map((reference, index) => {
      const item = document.createElement("li");
      item.className = "assistant-source";
      const img = document.createElement("img");
      const src = previewURLs.get(reference.number);
      if (src) img.src = src;
      img.alt = `Фото ${reference.number}`;
      const text = document.createElement("div");
      const title = document.createElement("strong");
      title.textContent = templateID.value === "minimax-h3-video" ? `Picture ${index + 1} · Фото ${reference.number}` : `Фото ${reference.number}`;
      const role = document.createElement("span");
      role.textContent = promptAssistantRoleLabels[reference.role] || reference.role;
      text.append(title, role);
      item.append(img, text);
      return item;
    }));
    const files = [
      [miniMaxAudioIsAvailable() && (uploadedAudio || miniMaxAudioFile?.files?.length), isAIHubSelected() ? "Voice 1" : "Audio 1", miniMaxAudioName?.textContent, isAIHubSelected() ? "Образец голоса · без прослушивания" : "Голос и звук"],
      [miniMaxVideoIsAvailable() && (uploadedVideo || miniMaxVideoFile?.files?.length), "Video 1", miniMaxVideoName?.textContent, isAIHubSelected() ? "Кадры видео · движение и сцена" : "Движение и темп"],
    ];
    files.filter(([active]) => active).forEach(([, id, name, role]) => {
      const item = document.createElement("li");
      item.className = "assistant-source assistant-source-file";
      const text = document.createElement("div");
      const title = document.createElement("strong");
      title.textContent = `${id} · ${role}`;
      const filename = document.createElement("span");
      filename.textContent = name || id;
      text.append(title, filename);
      item.append(text);
      list.append(item);
    });
    section.hidden = list.children.length === 0;
  };
  const openPromptAssistant = () => {
    if (!promptAssistant) return;
    studio?.show("configure");
    if (promptAssistantEnabled) promptAssistantEnabled.checked = true;
    if (promptAssistantReview) promptAssistantReview.hidden = !Boolean(promptAssistantDraft?.value);
    syncPromptAssistant();
    renderAssistantSources();
    if (!promptAssistant.open) promptAssistant.showModal();
  };

  const promptAssistantRoleLabels = {
    base_scene: "Основная сцена",
    identity: "Внешность",
    wardrobe_object: "Одежда или предмет",
    pose_composition: "Поза и композиция",
    style: "Стиль",
    background: "Фон",
    details: "Детали",
    first_frame: "Первый кадр",
    last_frame: "Последний кадр",
    motion_timing: "Движение и темп",
    voice_sound: "Голос и звук",
    voice_sample: "Образец голоса · Seed-VC",
  };

  const renderAssistantDiffSegments = (target, segments) => {
    if (!target) return;
    const children = (segments || []).map((segment) => {
      const element = segment.type === "added" ? document.createElement("ins") : segment.type === "removed" ? document.createElement("del") : document.createElement("span");
      element.textContent = segment.value;
      return element;
    });
    target.replaceChildren(...children);
  };

  const renderPromptAssistantDiff = (original, suggestion) => {
    const result = generationModules.assistant?.diff?.(original, suggestion) || {
      original: [{ type: "removed", value: original }], suggestion: [{ type: "added", value: suggestion }],
    };
    renderAssistantDiffSegments(promptAssistantDiffOriginal, result.original);
    renderAssistantDiffSegments(promptAssistantDiffSuggestion, result.suggestion);
  };

  const renderPromptAssistantReferences = (references = []) => {
    if (!promptAssistantReferenceReview || !promptAssistantReferenceList) return;
    promptAssistantReferenceReview.hidden = references.length === 0;
    if (promptAssistantReferenceCount) promptAssistantReferenceCount.textContent = references.length ? `${references.length} ${references.length === 1 ? "источник" : "источника"}` : "";
    promptAssistantReferenceList.replaceChildren(...references.map((reference) => {
      const item = document.createElement("li");
      const heading = document.createElement("div");
      const identifier = document.createElement("strong");
      identifier.textContent = reference.id || "Референс";
      const role = document.createElement("span");
      role.textContent = promptAssistantRoleLabels[reference.role] || reference.role || "Референс";
      heading.append(identifier, role);
      const summary = document.createElement("p");
      summary.textContent = reference.summary || "Описание референса не получено.";
      const usage = document.createElement("small");
      usage.textContent = reference.use || "Используется согласно выбранной роли.";
      if (reference.inspected === false) {
        const status = document.createElement("em");
        status.textContent = "Файл подключён без анализа содержимого";
        item.append(heading, summary, usage, status);
      } else if (reference.kind === "video") {
        const status = document.createElement("em");
        status.textContent = "Проанализированы отдельные кадры, без звука";
        item.append(heading, summary, usage, status);
      } else {
        item.append(heading, summary, usage);
        const correction = document.createElement("details");
        correction.className = "assistant-correction";
        const label = document.createElement("summary");
        label.textContent = "Исправить наблюдение";
        const field = document.createElement("textarea");
        field.maxLength = 500;
        field.rows = 2;
        field.setAttribute("aria-label", `Уточнение для ${reference.id}`);
        field.placeholder = "Например: волосы светлые, а не тёмные";
        const savedCorrection = assistantSession?.correction(reference.id);
        field.value = savedCorrection && savedCorrection.source === reference.source_value ? savedCorrection.text : "";
        field.dataset.assistantCorrection = reference.id;
        field.addEventListener("input", () => {
          assistantSession?.setCorrection(reference.id, field.value, reference.source_value || "");
        });
        correction.append(label, field);
        item.append(correction);
      }
      return item;
    }));
  };

  const renderPromptAssistantReview = (payload, original) => {
    if (promptAssistantNegativeReview && promptAssistantNegativeDraft) {
      const input = captureAssistantInput();
      const showNegative = generationModules.assistant.supportsNegative(input) && Number(input.cfg) > 1;
      promptAssistantNegativeReview.hidden = !showNegative;
      promptAssistantNegativeDraft.value = showNegative ? payload.negative_prompt || "" : "";
    }
    const audioReview = document.getElementById("prompt-assistant-audio-review");
    const audioText = document.getElementById("prompt-assistant-audio-text");
    if (audioReview && audioText) {
      audioText.value = payload.audio_prompt || "";
      audioReview.hidden = !audioText.value;
    }
    const references = Array.isArray(payload.references) ? payload.references : [];
    renderPromptAssistantReferences(references);
    renderPromptAssistantDiff(original, payload.prompt || "");
    if (promptAssistantReviewMeta) {
      const promptTokens = Number(payload.usage?.prompt_tokens || 0);
      const completionTokens = Number(payload.usage?.completion_tokens || 0);
      const tokens = promptTokens + completionTokens;
      promptAssistantReviewMeta.textContent = tokens > 0 ? `${payload.model || "Локальная модель"} · ${tokens} токенов` : (payload.model || "Локальная модель");
    }
  };

  const resetPromptAssistantReview = () => assistantSession?.reset();

  const syncPromptAssistant = () => {
    if (!promptAssistant || !promptAssistantEnabled || !promptAssistantControls || !promptAssistantTemplate) return;
    syncAssistantModel();
    const isEdit = templateID.value === "image-to-image" || isKreaProSelected() && kreaProMode() !== "text";
    const isVideo = templateID.value === "minimax-h3-video";
    const videoProfile = miniMaxMode() === "references" ? "minimax-h3-ref2va" : "minimax-h3-fl2va";
    const frameReferenceCount = isVideo && miniMaxMode() !== "references" ? selectedReferenceMetadata().length : 0;
    const frameAssistantMode = frameReferenceCount >= 2 ? "FL2VA" : frameReferenceCount === 1 ? "I2VA" : "T2VA";
    const promptCharacterLimit = generationModules.assistant.promptCharacterLimit({
      mode: templateID.value, longVideo: { enabled: miniMaxLongEnabled(), parts: Number(form.elements.video_long_parts?.value || 2) },
    });
    if (positive) positive.maxLength = promptCharacterLimit;
    if (promptAssistantDraft) promptAssistantDraft.maxLength = promptCharacterLimit;
    if (promptAssistantDraftLimit) promptAssistantDraftLimit.textContent = `Можно изменить перед применением. До ${promptCharacterLimit} символов.`;
    promptAssistant.hidden = !templateID.value;
    promptAssistantControls.hidden = false;
    promptAssistantTemplate.disabled = isVideo;
    const family = selectedGenerationWorkflow()?.dataset.family || "";
    const isQwenEdit = isEdit && family === "qwen21";
    document.getElementById("prompt-assistant-template-field").hidden = isVideo || isQwenEdit;
    document.getElementById("prompt-assistant-mode").textContent = isVideo
      ? `MiniMax H3 · ${miniMaxMode() === "references" ? "REF2VA" : frameAssistantMode}`
      : `${family === "moss" ? "MOSS Image" : family === "flux2" ? "Flux2" : family === "qwen21" ? "Qwen Image 2.1" : "Krea2"} · ${isEdit ? "Редактирование фото" : "Изображение по тексту"}`;
    [...promptAssistantTemplate.options].forEach((option) => {
      const imageOnly = option.dataset.imageOnly !== undefined;
      const videoOnly = option.dataset.videoOnly !== undefined;
      const qwenOnly = option.dataset.qwenEdit !== undefined;
      const kreaOnly = option.dataset.kreaEdit !== undefined;
      const fluxOnly = option.dataset.fluxEdit !== undefined;
      const selectedVideoProfile = option.value === videoProfile;
      option.hidden = isVideo ? !selectedVideoProfile : qwenOnly ? !isQwenEdit : kreaOnly ? !isEdit || family !== "krea2" : fluxOnly ? !isEdit || family !== "flux2" : (imageOnly && !isEdit) || videoOnly;
      option.disabled = option.hidden;
    });
    const selectedAssistantTemplate = promptAssistantTemplate.selectedOptions[0];
    if ((isVideo && selectedAssistantTemplate?.value !== videoProfile) || (!isEdit && selectedAssistantTemplate?.dataset.imageOnly !== undefined) || (!isVideo && selectedAssistantTemplate?.dataset.videoOnly !== undefined)) {
      promptAssistantTemplate.value = isVideo ? videoProfile : "workflow-default";
    }
    if (isQwenEdit) promptAssistantTemplate.value = "qwen21-edit";
    else if (promptAssistantTemplate.value === "qwen21-edit") promptAssistantTemplate.value = "workflow-default";
    if (isEdit && family === "flux2" && promptAssistantTemplate.value === "workflow-default") promptAssistantTemplate.value = "flux-edit";
    if (isEdit && family === "krea2" && promptAssistantTemplate.value === "workflow-default") promptAssistantTemplate.value = "krea2-edit";
    if (promptAssistantTemplate.selectedOptions[0]?.disabled) promptAssistantTemplate.value = isQwenEdit ? "qwen21-edit" : isEdit && family === "flux2" ? "flux-edit" : isEdit && family === "krea2" ? "krea2-edit" : "workflow-default";
    if (!draftController?.restoring) assistantSession?.syncContext();
    if (promptAssistantApply) promptAssistantApply.disabled = !assistantSession?.snapshot().canApply;
    if (promptAssistantDescription) promptAssistantDescription.textContent = isVideo
      ? miniMaxMode() === "references"
        ? "REF2VA-ассистент сначала разберёт каждый свободный референс и свяжет его роль с Picture, Video или Audio."
        : frameAssistantMode === "FL2VA"
          ? "FL2VA-ассистент проанализирует оба точных кадра и построит один непрерывный переход между ними."
          : frameAssistantMode === "I2VA"
            ? "I2VA-ассистент проанализирует точный первый кадр и опишет дальнейшее развитие сцены."
            : "T2VA-ассистент соберёт полноценную видео- и аудиосцену только по вашему тексту."
      : "Локальная модель подготовит вариант, но генерация начнётся только после вашего подтверждения.";
    if (assistantSession?.snapshot().job.pending) {
      // The controller owns the live status/countdown even when the panel reopens.
      promptAssistantApply.disabled = true;
    } else if (!promptAssistantEnabled.checked) {
      resetPromptAssistantReview();
      setPromptAssistantState("Ассистент выключен. Используется ваш исходный промт.");
    } else if (assistantSlice.get().stale) {
      setPromptAssistantState("Подготовьте новый вариант для текущей модели и материалов.", "stale");
    } else if (assistantSession?.snapshot().busy) {
      setPromptAssistantState("Ассистент готовит вариант. Панель можно закрыть.", "loading");
    } else if (assistantSlice.get().status === "error") {
      setPromptAssistantState(assistantSlice.get().error || "Не удалось подготовить вариант. Попробуйте ещё раз.", "error");
    } else if (assistantSlice.get().approved) {
      setPromptAssistantState(assistantSlice.get().action === "kept_original" ? "Используется ваш исходный текст." : "Вариант применён. Его можно снова отредактировать.", "approved");
    } else if (!promptAssistantReview?.hidden) {
      setPromptAssistantState("Проверьте вариант и примените его либо оставьте свой промт.", "review");
    } else {
      setPromptAssistantState(isVideo
        ? miniMaxMode() === "references"
          ? "REF2VA-ассистент подготовит Context-IR со строгой картой свободных референсов."
          : `${frameAssistantMode}-ассистент подготовит структуру MiniMax H3 v5 для текущего количества точных кадров.`
        : "Ассистент подготовит вариант по текущему тексту и выбранным материалам.");
    }
  };

  const syncAIHubFields = () => {
    const active = isAIHubSelected();
    const carousel = aiHubMode() === "carousel";
    const carouselV2 = aiHubCarouselV2();
    const photo = aiHubMode() === "photo";
    const audioSource = form.elements.aihub_audio_source;
    const originalAudio = audioSource?.querySelector('option[value="original"]');
    if (originalAudio) { originalAudio.disabled = active && photo; originalAudio.hidden = active && photo; }
    if (active && photo && audioSource?.value === "original") audioSource.value = "native";
    const source = form.elements.aihub_audio_source?.value || "native";
    const show = (element, visible) => {
      element.hidden = !visible;
      element.querySelectorAll("input, select, textarea, button").forEach(control => { control.disabled = !visible || Boolean(control.closest("[inert]")); });
    };
    root.querySelectorAll("[data-aihub-settings]").forEach(element => show(element, active));
    const audioSection = root.querySelector(".aihub-audio-settings");
    if (audioSection) show(audioSection, active && !carousel);
    root.querySelectorAll("[data-aihub-mode]").forEach(element => show(element, active && element.dataset.aihubMode.split(" ").includes(aiHubMode())));
    root.querySelectorAll("[data-aihub-carousel-v2]").forEach(element => show(element, active && carouselV2));
    root.querySelectorAll("[data-aihub-carousel-legacy]").forEach(element => show(element, active && carousel && !carouselV2));
    root.querySelectorAll("[data-aihub-ltx]").forEach(element => show(element, active && !carousel && source === "ltx"));
    root.querySelectorAll("[data-aihub-external-audio]").forEach(element => show(element, active && !carousel && source !== "native"));
    root.querySelectorAll("[data-aihub-sam]").forEach(element => show(element, active && aiHubMode() === "video" && form.elements.aihub_mask?.value === "sam3"));
    const allowed = new Set((workflowManifestsByID.get("aihub-minimax-h3")?.parameters || []).map(parameter => parameter.name));
    allowed.add("seed");
    if (carousel) {
      if (!carouselV2) allowed.delete("video_duration_seconds");
      allowed.delete("video_output_crf");
      if (carouselV2) for (const name of ["video_rtx_enabled", "video_rtx_scale", "video_rtx_quality", "aihub_dlss_enabled", "aihub_dlss_strength"]) allowed.delete(name);
    }
    if (photo) allowed.delete("video_reference_start");
    root.querySelectorAll(".minimax-video-settings").forEach(section => {
      section.querySelectorAll("label").forEach(label => {
        const controls = [...label.querySelectorAll("[name]")];
        if (!controls.length || label.closest("[data-lora-slots]")) return;
        if (!label.dataset.aihubPriorHidden) label.dataset.aihubPriorHidden = String(label.hidden);
        const keep = controls.some(control => allowed.has(control.name));
        if (active) show(label, keep && !section.closest("[inert]"));
        else if (label.dataset.aihubFiltered) {
          label.hidden = label.dataset.aihubPriorHidden === "true";
          delete label.dataset.aihubFiltered;
        }
        if (active) label.dataset.aihubFiltered = "true";
      });
      section.querySelectorAll(".video-enhancement").forEach(part => {
        part.hidden = !active && part.hasAttribute("data-aihub-settings") || active && ![...part.querySelectorAll("[name]")].some(control => allowed.has(control.name));
      });
      if (active && ![...section.querySelectorAll("[name]")].some(control => !control.disabled)) section.hidden = true;
      const heading = section.querySelector(".workflow-settings-heading strong");
      if (heading) {
        heading.dataset.originalText ||= heading.textContent;
        if (!active) heading.textContent = heading.dataset.originalText;
        else if (section.querySelector('[name="video_quality"]')) heading.textContent = "AIHUB · размер результата";
        else if (section.querySelector("[data-lora-slots]")) heading.textContent = "MiniMax H3 · LoRA";
        else if (section.querySelector('[name="seed"]')) heading.textContent = "Seed";
      }
      section.querySelectorAll(".workflow-settings-heading small, .minimax-model-profile").forEach(note => { note.hidden = active; });
    });
    root.querySelectorAll("[data-aihub-dlss-controls]").forEach(element => show(element, active && Boolean(form.elements.aihub_dlss_enabled?.checked)));
    root.querySelectorAll("[data-aihub-fg]").forEach(element => show(element, active && !carousel));
    root.querySelectorAll("[data-aihub-fg-controls]").forEach(element => show(element, active && !carousel && Boolean(form.elements.aihub_fg_enabled?.checked)));
    const oldModes = root.querySelector("#minimax-video-mode");
    oldModes?.querySelectorAll(":scope > strong, :scope > .minimax-mode-segments, :scope > small").forEach(element => { element.hidden = active; });
    for (const name of ["video_audio_start", "video_reference_duration", "video_reference_audio"]) {
      const control = form.querySelector('[name="' + name + '"]');
      const label = control?.closest("label");
      if (label) { label.hidden = active; control.disabled = active || Boolean(control.closest("[inert]")); }
    }
    const audioTitle = miniMaxAudioReference?.querySelector(".upload-zone strong");
    if (audioTitle) audioTitle.textContent = active ? "Добавить образец голоса" : "Добавить аудиореференс";
    const audioHint = miniMaxAudioReference?.querySelector(":scope > .field-hint");
    if (audioHint) audioHint.hidden = active;
    const videoLabel = miniMaxVideoReference?.querySelector(".upload-slot-label");
    if (videoLabel) videoLabel.textContent = active ? "Видео · обязательно" : "Видео · необязательно";
    const sourceNote = document.getElementById("image-source-note");
    if (active && sourceNote) sourceNote.textContent = carousel ? "Фото 1 обязательно · до 5 фото" : photo ? "Фото 1 обязательно · до 9 фото-референсов" : "До 9 фото-референсов";
    if (active && (carousel || photo)) {
      const first = root.querySelector('[data-image-slot="1"] [data-image-slot-label]');
      if (first) first.textContent = "Фото 1 · обязательно";
    }
    if (active && miniMaxVideoResolutionPreview) {
      const references = selectedReferenceMetadata();
      const facts = generationOutputSummary("minimax_h3", references);
      miniMaxVideoResolutionPreview.textContent = facts?.compact || "";
    }
    root.querySelectorAll("[data-generation-profile]").forEach(button => { button.hidden = active; button.disabled = active; });
    root.querySelectorAll(".generation-profile-picker").forEach(element => { element.hidden = active; });
    if (active && positivePromptLabel) positivePromptLabel.textContent = carousel ? "Основной промпт · фотокарусель" : "Основной промпт · сцена и действие";
    if (positive) positive.placeholder = active
      ? "Девушка с фото 1 идёт по улице, поворачивается к камере и улыбается. Камера плавно приближается. Она говорит: «Доброе утро»."
      : "Например: портрет в мягком вечернем свете, высокая детализация";
    root.querySelectorAll('[data-lora-slots="minimax"] [name^="lora_clip_strength_"]').forEach(control => {
      if (control.closest("label")) control.closest("label").hidden = active;
      if (active) control.disabled = true;
    });
    syncMiniMaxAudioReference();
    syncAIHubDuration();
  };

  const syncWorkflowFields = () => {
    const preset = selectedGenerationWorkflow();
    const family = preset?.dataset.family || model?.selectedOptions?.[0]?.dataset.family || "";
    const isEdit = preset?.dataset.templateId === "image-to-image";
    const isKrea = family === "krea2";
    const isKreaText = isKrea && !isEdit;
    const isKreaEdit = isKrea && isEdit;
    const isKreaUltra = isKreaUltraSelected();
    const resolutionLabel = outputMegapixels?.closest('label')?.querySelector('.ui-field-label');
    if (resolutionLabel?.firstChild?.nodeType === 3) resolutionLabel.firstChild.textContent = isKreaProSelected() ? 'Мегапиксели генерации ' : 'Итоговые мегапиксели ';
    if (isKreaProSelected()) {
      wizardSlice.dispatch({type:"SELECT_WORKFLOW",workflowID:"krea2-pro",available:preset?.dataset.available === "true",requiresImage:kreaProMode()!=="text",allowsImages:kreaProMode()!=="text"});
    }
    if (imageSourceFields) imageSourceFields.hidden = !(wizardSlice.get().requiresImage || wizardSlice.get().allowsImages);
    const isFluxEdit = family === "flux2" && isEdit;
    const isQwenEdit = family === "qwen21" && isEdit;
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
    if (family === "qwen21" && maxSide) maxSide.value = "4096";
    if (outputMegapixels) {
      outputMegapixels.max = isKreaEdit ? String(krea2EditMaxBaseMegapixels) : family === "moss" ? "4" : family === "qwen21" || isKreaUltra ? "4.27" : "16";
      if (family === "moss" && numericValue(outputMegapixels.value, 1) > 4) outputMegapixels.value = "4";
      if (isKreaUltra && numericValue(outputMegapixels.value) > 4.27) outputMegapixels.value = "4.27";
      if (isKreaEdit && numericValue(outputMegapixels.value, krea2EditMaxBaseMegapixels) > krea2EditMaxBaseMegapixels) {
        outputMegapixels.value = String(krea2EditMaxBaseMegapixels);
      } else if (family === "qwen21" && numericValue(outputMegapixels.value, 1) > 4.27) {
        outputMegapixels.value = "1";
      }
    }
    if (isKreaEdit) {
      if (maxSide) maxSide.value = String(krea2EditMaxLongestSide);
      if (preserveOriginalLabel) preserveOriginalLabel.textContent = "Сохранить пропорции исходного фото в безопасном размере Krea2";
      applyOriginalResolution();
    } else if (isQwenEdit) {
      if (maxSide) maxSide.value = "4096";
      if (preserveOriginalLabel) preserveOriginalLabel.textContent = "Сохранить пропорции исходного фото в безопасном размере Qwen";
      if (qwenEditMegapixels) qwenEditMegapixels.value = outputMegapixels?.value || "1";
      applyOriginalResolution();
    } else if (preserveOriginalLabel) {
      preserveOriginalLabel.textContent = "Сохранить размер исходного фото";
    }
    if (isFluxEdit && maxSide?.value === "0") maxSide.value = "2160";
    const setFieldState = (selector, visible) => {
      root.querySelectorAll(selector).forEach((field) => {
        const permitted = !field.closest("[inert]");
        field.hidden = !visible || !permitted;
        field.querySelectorAll("input, select, textarea, button").forEach((control) => { control.disabled = !visible || !permitted; });
      });
    };
    setFieldState(".image-edit-field", isEdit);
    setFieldState(".qwen-edit-resolution-field", isQwenEdit && Boolean(preserveOriginalSize?.checked));
    setFieldState(".krea-edit-field", isKreaEdit);
    setFieldState(".flux-edit-field", isFluxEdit);
    root.querySelectorAll(".size-workflow-field").forEach((field) => {
      field.hidden = Boolean(isMiniMax || (isEdit && preserveOriginalSize?.checked));
    });
    setFieldState(".krea-workflow-field", isKrea);
    setFieldState(".krea-text-workflow-field", isKreaText && !isKreaUltra);
    setFieldState(".krea-lora-settings", isKreaText);
    setFieldState(".moss-settings", family === "moss");
    setFieldState(".qwen-lora-settings", family === "qwen21");
    if (form.elements.moss_upscale_resolution) form.elements.moss_upscale_resolution.disabled = family !== "moss" || !form.elements.moss_seedvr2?.checked;
    setFieldState(".krea-ultra-settings", isKreaUltra);
    setFieldState(".krea-pro-settings", isKreaProSelected());
    kreaProUI?.sync();
    setFieldState(".krea-photoflow-settings", isKrea && !isKreaUltra);
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
    setFieldState(".standard-main-settings", (!isEdit || isQwenEdit) && !isMiniMax);
    setFieldState(".minimax-video-settings", isMiniMax);
    setFieldState(".minimax-reference-field", isMiniMax && miniMaxMode() === "references");
    const qwenCFGOne = family === "qwen21" && Number((cfg?.value || "1").replace(",", ".")) <= 1;
    const hideNegativePrompt = isMiniMax || isKreaText && !isKreaUltra || qwenCFGOne;
    if (negativePromptField) negativePromptField.hidden = hideNegativePrompt;
    if (negativePrompt) negativePrompt.disabled = hideNegativePrompt;
    if (negativeCFGNote) negativeCFGNote.hidden = family !== "qwen21" || !qwenCFGOne;
    if (generationPromptFields) generationPromptFields.classList.toggle("is-single", hideNegativePrompt);
    if (positivePromptLabel) positivePromptLabel.textContent = isMiniMax ? "Промт видео" : isKreaText ? "Промт" : "Позитивный промт";
    if (isMiniMax) {
      syncMiniMaxVideoProfile();
      syncMiniMaxSharpenFields();
    }
    if (isFluxEdit) syncAdaptiveLoraSlots("flux");
    if (isKreaText) syncAdaptiveLoraSlots("krea");
    if (isMiniMax) syncAdaptiveLoraSlots("minimax");
    if (family === "moss") syncAdaptiveLoraSlots("moss");
    if (family === "qwen21") syncAdaptiveLoraSlots("qwen21");
    syncKreaOptionalModules();
    if (qualityField) qualityField.hidden = !isKreaText || isKreaUltra;
    if (editorProfile) editorProfile.hidden = !isEdit;
    syncSelectedImageSummary();
    if (isQwenEdit) {
      if (editorProfileTitle) editorProfileTitle.textContent = "Qwen Image 2.1: редактирование";
      if (editorProfileDescription) editorProfileDescription.textContent = "Первое фото изменяется, до девяти следующих задают референсы. В промте используйте <image1>, <image2> … <image10>.";
      if (editSourceTitle) editSourceTitle.textContent = "Исходное фото и референсы";
      if (editSourceDescription) editSourceDescription.textContent = "Первое фото задаёт кадр результата; остальные помогают перенести конкретные детали.";
      if (mainPassTitle) mainPassTitle.textContent = "Параметры Qwen Image 2.1";
      if (mainPassDescription) mainPassDescription.textContent = "Обычно достаточно 25 шагов, CFG 1 и Euler / Simple.";
    } else if (isFluxEdit) {
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
    syncAIHubFields();
    studio?.syncOptions();
    syncPromptActions();
  };

  const applyQuality = () => {
    if (isKreaUltraSelected()) {
      if (steps) steps.value = selectedGenerationWorkflow().dataset.defaultSteps || "10";
      if (cfg) cfg.value = "2";
      if (sampler) sampler.value = "euler";
      if (scheduler) scheduler.value = "simple";
      calculateResolution();
      syncGenerationSummary();
      return;
    }
    const option = model?.selectedOptions[0];
    if (!option?.value) return;
    const baseSteps = Number(option.dataset.defaultSteps || 25);
    const mode = quality?.value || "balanced";
    const isKrea = option.dataset.family === "krea2";
    const isQwen = option.dataset.family === "qwen21";
    const isEdit = selectedGenerationWorkflow()?.dataset.templateId === "image-to-image";
    const isFluxEdit = option.dataset.family === "flux2" && isEdit;
    if (steps) steps.value = isKrea ? (mode === "fast" ? 4 : baseSteps) : (mode === "fast" ? Math.max(4, Math.round(baseSteps * 0.65)) : mode === "maximum" ? Math.min(100, Math.round(baseSteps * 1.5)) : baseSteps);
    if (cfg && option.dataset.defaultCfg) cfg.value = isFluxEdit ? "1" : option.dataset.defaultCfg;
    if (sampler && option.dataset.defaultSampler) sampler.value = option.dataset.defaultSampler;
    if (scheduler && option.dataset.defaultScheduler) scheduler.value = option.dataset.defaultScheduler;
    if (isQwen && dimensionMultiple) dimensionMultiple.value = "32";
    if (option.dataset.family === "moss" && dimensionMultiple) dimensionMultiple.value = "16";
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

  form.elements.moss_seedvr2?.addEventListener("change", () => {
    syncWorkflowFields();
    syncGenerationSummary();
  });

  model?.addEventListener("change", () => {
    applyQuality();
    if (model.selectedOptions?.[0]?.dataset.family === "minimax_h3") {
      syncMiniMaxVideoProfile({ applyModelDefaults: true });
      syncWorkflowFields();
    }
    syncStudioSelection();
  });
  quality?.addEventListener("change", applyQuality);
  [aspect, outputMegapixels, dimensionMultiple, maxSide].forEach((input) => input?.addEventListener("change", calculateResolution));
  outputMegapixels?.addEventListener("input", calculateResolution);
  qwenEditMegapixels?.addEventListener("change", () => {
    const value = clamp(numericValue(qwenEditMegapixels.value, 1), 0.3, 4.27);
    qwenEditMegapixels.value = String(value);
    outputMegapixels.value = String(value);
    applyOriginalResolution();
  });
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
    const maximumDimension = 4096;
    const value = clamp(Number(input.value) || 1024, minimumDimension, maximumDimension);
    input.value = String(Math.round(value / 8) * 8);
    aspect.value = "custom";
    outputMegapixels.value = (Number(width.value) * Number(height.value) / (1024 * 1024)).toFixed(2);
    updateResolutionPreview();
  }));
  root.querySelectorAll(".generation-lora-select").forEach((select) => {
    const row = select.closest(".lora-row");
    const kind = row?.dataset.loraSlots;
    const remove = document.createElement("button");
    remove.type = "button";
    remove.className = "generation-lora-remove ghost";
    const icon = document.createElement("i");
    icon.dataset.lucide = "x";
    icon.setAttribute("aria-hidden", "true");
    remove.append(icon);
    row?.append(remove);
    remove.addEventListener("click", () => {
      select.value = "";
      select.dispatchEvent(new Event("change", { bubbles: true }));
      const target = !remove.disabled ? remove : [...root.querySelectorAll(`[data-lora-slots="${kind}"] .generation-lora-remove`)].findLast(button => !button.disabled);
      (target || select).focus({ preventScroll: true });
    });
    select.addEventListener("change", () => {
      const strength = row?.querySelector(".generation-lora-model");
      if (strength) strength.value = select.value ? (select.selectedOptions[0]?.dataset.defaultStrength || "1") : "0";
      if (kind) {
        if (!select.value) compactLoraSlots(kind);
        syncAdaptiveLoraSlots(kind);
      }
    });
  });
  window.lucide?.createIcons();

  const assistantProgress = generationModules.assistantProgress?.bindUI({ root });
  assistantSession = generationModules.assistant.createSession({
    state: assistantSlice, read: captureAssistantInput, prepare: () => prepareActiveMedia(),
    transport: generationModules.assistant.createTransport({ csrf: () => form.elements.csrf?.value || "" }),
    persistence: generationModules.assistant.createPersistence({
      storage: () => window.localStorage, owner: promptAssistant.dataset.ownerId,
      validID: generationModules.assistantJobs.validID, onChange: markDraftDirty,
    }),
    jobs: generationModules.assistantJobs, newRequestID: newGenerationRequestID,
    onDirty: markDraftDirty,
    onProgress: job => assistantProgress?.update(job),
    onState: state => {
      syncPromptActions();
      promptAssistantImprove.disabled = state.busy || state.deciding || !state.ready || assistantModelState().blocked;
      promptAssistantImprove.classList.toggle("is-loading", state.preparing && !state.job.pending);
      promptAssistantApply.disabled = !state.canApply;
      promptAssistantKeep.disabled = state.deciding;
      const restoreOriginal = document.getElementById("prompt-assistant-restore-original");
      if (restoreOriginal) {
        restoreOriginal.hidden = !state.hasOriginal;
        restoreOriginal.disabled = !state.canRestoreOriginal;
      }
      if (promptAssistantStale) promptAssistantStale.hidden = !state.state.stale;
      const cancel = document.getElementById("prompt-assistant-cancel");
      cancel.hidden = !state.job.pending;
      cancel.disabled = Boolean(state.job.cancelling);
      if (state.job.pending && !state.state.approved) promptAssistantEnabled.checked = true;
      if (state.message) setPromptAssistantState(state.message, state.phase === "cancelled" ? "" : state.phase);
    },
    onRequest: () => { assistantProgress?.reset(); document.getElementById("prompt-assistant-saved-response").hidden = true; },
    onReset: () => {
      assistantProgress?.reset();
      promptAssistantReview.hidden = true;
      promptAssistantDraft.value = "";
      if (promptAssistantNegativeDraft) promptAssistantNegativeDraft.value = "";
      renderPromptAssistantReferences([]);
      renderPromptAssistantDiff("", "");
      promptAssistantReviewMeta.textContent = "";
      promptAssistantStale.hidden = true;
      promptAssistantApply.disabled = true;
    },
    onProposal: (payload, original) => {
      promptAssistantDraft.value = payload.prompt;
      renderPromptAssistantReview(payload, original);
      promptAssistantReview.hidden = false;
    },
    onSavedResponse: payload => {
      document.getElementById("prompt-assistant-saved-text").textContent = payload.prompt + (payload.negative_prompt ? "\n\nНегативный промт\n" + payload.negative_prompt : "") + (payload.audio_prompt ? "\n\nОзвучка LTX\n" + payload.audio_prompt : "");
      document.getElementById("prompt-assistant-saved-response").hidden = false;
    },
    onApplied: (text, audioPrompt, negative) => {
      positive.value = text;
      renderMiniMaxLongPlan();
      const input = captureAssistantInput();
      if (generationModules.assistant.supportsNegative(input) && Number(input.cfg) > 1 && negativePrompt) negativePrompt.value = negative;
      if (audioPrompt && form.elements.aihub_audio_source?.value === "ltx") form.elements.aihub_audio_prompt.value = audioPrompt;
      promptAssistantReview.hidden = true;
    },
    onOriginalRestored: (text, originalNegative, originalAudio) => {
      positive.value = text;
      renderMiniMaxLongPlan();
      if (generationModules.assistant.supportsNegative(captureAssistantInput()) && negativePrompt) negativePrompt.value = originalNegative;
      if (generationWorkflowID?.value === "aihub-minimax-h3" && form.elements.aihub_audio_source?.value === "ltx") form.elements.aihub_audio_prompt.value = originalAudio;
      promptAssistantReview.hidden = true;
    },
    onKept: () => { promptAssistantReview.hidden = true; },
    onRestore: (state, draft, negativeDraft) => {
      promptAssistantDraft.value = draft;
      if (promptAssistantNegativeDraft) promptAssistantNegativeDraft.value = negativeDraft;
      promptAssistantReview.hidden = state.approved;
      renderPromptAssistantReview({ prompt: draft, negative_prompt: negativeDraft, audio_prompt: state.audioSuggestion, references: state.references }, state.original);
    },
  });
  document.getElementById("prompt-assistant-cancel")?.addEventListener("click", () => {
    try { assistantSession.cancel(); } catch (error) { setPromptAssistantState(error.message, "error"); }
  });
  window.addEventListener("pagehide", () => assistantSession.suspend());
  window.addEventListener("pageshow", event => { if (event.persisted) assistantSession.resume(); });
  const resumeSavedAssistant = () => {
    if (document.visibilityState === "visible") assistantSession.resume();
  };
  window.addEventListener("online", resumeSavedAssistant);
  document.addEventListener("visibilitychange", resumeSavedAssistant);
  promptAssistantOpen?.addEventListener("click", openPromptAssistant);
  document.getElementById("prompt-assistant-close")?.addEventListener("click", () => promptAssistant.close());
  document.getElementById("prompt-assistant-edit")?.addEventListener("click", () => promptAssistantDraft?.focus());
  promptAssistant?.addEventListener("close", () => {
    if (assistantSlice.get().status === "idle" && promptAssistantEnabled) promptAssistantEnabled.checked = false;
    promptAssistantOpen?.focus({ preventScroll: true });
    markDraftDirty();
  });
  promptAssistantEnabled?.addEventListener("change", syncPromptAssistant);
  promptAssistantModel?.addEventListener("change", () => {
    assistantSession?.syncContext();
    syncAssistantModel();
    markDraftDirty();
  });
  promptAssistantTemplate?.addEventListener("change", () => {
    invalidatePromptAssistant();
    syncPromptAssistant();
  });
  promptAssistantThink?.addEventListener("change", () => {
    invalidatePromptAssistant();
    syncPromptAssistant();
  });
  const promptEdited = () => {
    clearedPrompt = null;
    assistantSession.editPrompt();
    syncPromptActions();
  };
  clearPrompt?.addEventListener("click", () => {
    if (assistantSession.snapshot().busy) return;
    clearedPrompt = { positive: positive.value, negative: negativePrompt?.value || "" };
    positive.value = "";
    if (negativePrompt) negativePrompt.value = "";
    resetPromptAssistantReview();
    syncPromptAssistant();
    syncPromptActions();
    markDraftDirty();
  });
  undoClearPrompt?.addEventListener("click", () => {
    if (!clearedPrompt || assistantSession.snapshot().busy) return;
    positive.value = clearedPrompt.positive;
    if (negativePrompt) negativePrompt.value = clearedPrompt.negative;
    clearedPrompt = null;
    assistantSession.editPrompt();
    syncPromptActions();
    markDraftDirty();
  });
  positive?.addEventListener("input", promptEdited);
  positive?.addEventListener("input", renderMiniMaxLongPlan);
  positive?.addEventListener("change", renderMiniMaxLongPlan);
  positive?.addEventListener("change", () => { void assistantSession.savePromptEdit(); });
  negativePrompt?.addEventListener("input", promptEdited);
  negativePrompt?.addEventListener("change", () => { void assistantSession.savePromptEdit(); });
  cfg?.addEventListener("change", () => { assistantSession.syncContext(); syncWorkflowFields(); });
  promptAssistantDraft?.addEventListener("input", () => {
    assistantSession.editDraft();
    renderPromptAssistantDiff(assistantSlice.get().original, promptAssistantDraft.value);
  });
  promptAssistantNegativeDraft?.addEventListener("input", () => { assistantSession.editDraft(); markDraftDirty(); });
  promptAssistantImprove?.addEventListener("click", async () => {
    if (assistantModelState().blocked) { syncAssistantModel(); return; }
    if (assistantSlice.get().draftEdited && !window.confirm("Заменить отредактированный вариант новым ответом?")) return;
    await assistantSession.improve();
  });
  promptAssistantApply?.addEventListener("click", async () => {
    if (await assistantSession.apply()) promptAssistant.close();
  });
  promptAssistantKeep?.addEventListener("click", async () => {
    if (await assistantSession.keep()) promptAssistant.close();
  });
  document.getElementById("prompt-assistant-restore-original")?.addEventListener("click", async () => {
    const current = assistantSlice.get();
    if (current.action === "edited_after_apply" && !window.confirm("Вернуть исходный текст? Текущие правки в поле промпта будут заменены.")) return;
    if (await assistantSession.restoreOriginal()) promptAssistantOpen?.focus({ preventScroll: true });
  });

  root.querySelectorAll(".scenario-choice").forEach((button) => {
    button.addEventListener("click", () => chooseScenario(button));
  });
  root.querySelectorAll(".generation-workflow-choice").forEach((button) => {
    button.addEventListener("click", () => chooseGenerationWorkflow(button));
  });

  const handleMiniMaxModeChange = () => {
    const referenceOnly = model?.selectedOptions?.[0]?.dataset.videoReferenceOnly === "true";
    if (referenceOnly && miniMaxMode() !== "references") setMiniMaxMode("references");
    videoSlice.dispatch({ type: "SET_MODE", mode: miniMaxMode() }, (state) => ({ ...state, mode: miniMaxMode() }));
    syncMiniMaxBranchCopy();
    syncImageSlots();
    syncMiniMaxAudioReference();
    syncStudioSelection();
    syncWorkflowFields();
  };
  miniMaxVideoModeInputs.forEach((input) => input.addEventListener("change", handleMiniMaxModeChange));
  [form.elements.video_long_enabled, form.elements.video_long_parts, form.elements.video_long_context, form.elements.video_long_transition].forEach(control => control?.addEventListener("change", () => {
    syncMiniMaxVideoProfile();
    syncImageSlots();
    syncMiniMaxAudioReference();
    syncPromptAssistant();
    invalidatePromptAssistant();
    form.dispatchEvent(new Event("input", { bubbles: true }));
  }));

  miniMaxAudioFile?.addEventListener("change", () => {
    audioPlayer?.clear();
    draftAssets.delete("input_audio");
    const file = miniMaxAudioFile.files?.[0];
    uploadedAudio = "";
    if (inputAudio) inputAudio.value = "";
    if (!file) {
      if (miniMaxAudioPreview) miniMaxAudioPreview.hidden = true;
      syncStudioSelection();
      return;
    }
    if (file.size > 32 * 1024 * 1024) {
      clearMiniMaxAudio();
      if (miniMaxAudioState) miniMaxAudioState.textContent = "Аудиофайл должен быть не больше 32 МБ";
      if (miniMaxAudioPreview) miniMaxAudioPreview.hidden = false;
      syncStudioSelection();
      return;
    }
    if (miniMaxAudioName) miniMaxAudioName.textContent = file.name;
    audioPlayer?.setSource(file);
    if (miniMaxAudioState) miniMaxAudioState.textContent = "Аудио выбрано. Оно загрузится вместе с фото.";
    if (miniMaxAudioPreview) miniMaxAudioPreview.hidden = false;
    syncStudioSelection();
  });
  miniMaxAudioRemove?.addEventListener("click", () => {
    clearMiniMaxAudio();
    syncStudioSelection();
  });
  document.getElementById("minimax-video-browse")?.addEventListener("change", (event) => {
    const picker = event.currentTarget;
    if (!picker.files?.length || !miniMaxVideoFile) return;
    const transfer = new DataTransfer();
    for (const file of picker.files) transfer.items.add(file);
    miniMaxVideoFile.files = transfer.files;
    picker.value = "";
    miniMaxVideoFile.dispatchEvent(new Event("change", { bubbles: true }));
  });
  for (const event of ["loadedmetadata", "emptied", "error"]) {
    miniMaxVideoPreviewMedia?.addEventListener(event, () => { syncAIHubDuration(); syncStudioSelection(); });
  }
  miniMaxVideoFile?.addEventListener("change", () => {
    draftAssets.delete("input_video");
    const file = miniMaxVideoFile.files?.[0];
    uploadedVideo = "";
    if (inputVideo) inputVideo.value = "";
    videoPlayer?.clear();
    if (!file) {
      if (miniMaxVideoPreview) miniMaxVideoPreview.hidden = true;
      syncStudioSelection();
      return;
    }
    if (file.size > 512 * 1024 * 1024) {
      clearMiniMaxVideo();
      if (miniMaxVideoState) miniMaxVideoState.textContent = "Видеофайл должен быть не больше 512 МБ";
      if (miniMaxVideoPreview) miniMaxVideoPreview.hidden = false;
      syncStudioSelection();
      return;
    }
    videoPlayer?.setSource(file);
    if (miniMaxVideoName) miniMaxVideoName.textContent = file.name;
    if (miniMaxVideoState) miniMaxVideoState.textContent = "Видео выбрано. Оно загрузится перед продолжением.";
    if (miniMaxVideoPreview) miniMaxVideoPreview.hidden = false;
    syncStudioSelection();
  });
  miniMaxVideoRemove?.addEventListener("click", () => {
    clearMiniMaxVideo();
    syncStudioSelection();
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

  let galleryPickerSignature = "";
  const renderGalleryImagePicker = () => {
    if (!imagePickerGrid || !imagePickerState) return;
    const state = galleryResource.snapshot(), galleryPickerImages = state.value;
    feedback.resourceView({ root: document.getElementById("generation-image-picker-feedback"), message: imagePickerState,
      retry: imagePickerRefresh, login: document.getElementById("generation-image-picker-login") }, state, {
      loading: "Загружаем ваши изображения...", idle: "Выберите обновление, чтобы загрузить изображения.",
      empty: `За последние ${mediaRetentionLabel} доступных изображений нет. Можно загрузить фото с устройства.`,
      showRefresh: true, showBackgroundLoading: true, loginURL: loginURL(),
    });
    imagePickerGrid.setAttribute("aria-busy", String(state.loading));
    const signature = JSON.stringify(galleryPickerImages);
    if (signature === galleryPickerSignature) return;
    galleryPickerSignature = signature;
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

  const galleryResource = feedback.createResource({
    initialValue: [],
    load: async signal => {
      const payload = await feedback.requestJSON("/generate/library/images", { signal, message: "Не удалось обновить изображения. Повторите запрос." });
      if (!Array.isArray(payload.images) || payload.images.some(entry => !entry || !Number.isSafeInteger(Number(entry.id)) || Number(entry.id) < 1 || typeof entry.url !== "string")) throw feedback.failure("invalid", "Список изображений не удалось прочитать. Повторите запрос.");
      return payload.images.map((entry) => ({
        id: Number(entry.id || 0),
        url: String(entry.url || ""),
        name: String(entry.filename || `Изображение ${entry.id || ""}`),
        modelName: String(entry.model_name || "Сгенерированное изображение"),
        createdUnix: Number(entry.created_unix || 0),
        expiresUnix: Number(entry.expires_unix || 0),
        sensitive: Boolean(entry.sensitive),
      })).filter(entry => entry.url);
    },
    onState: () => renderGalleryImagePicker(),
  });
  const refreshGalleryPickerImages = () => galleryResource.refresh();

  const closeGalleryImagePicker = () => {
    if (!imagePicker || imagePicker.hidden) return;
    const selectedSlot = galleryPickerSlot;
    imagePicker.hidden = true;
    galleryResource.pause();
    galleryPickerSlot = null;
    document.body.classList.remove("generation-image-picker-open");
    if (imagePickerFocusTrap) imagePickerFocusTrap.deactivate();
    else selectedSlot?.galleryButton?.focus({ preventScroll: true });
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
    galleryResource.resume({ read: false });
    document.body.classList.add("generation-image-picker-open");
    renderGalleryImagePicker();
    const closeButton = imagePicker.querySelector(".generation-image-picker-close");
    if (imagePickerFocusTrap) {
      imagePickerFocusTrap.activate({ trigger: item.galleryButton, initialFocus: closeButton, onEscape: closeGalleryImagePicker });
    } else {
      closeButton?.focus({ preventScroll: true });
    }
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
        applyKreaProSourceSize();
        if (isKreaProSelected()) calculateResolution();
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
    syncImageSlots();
    syncReferenceMap();
    syncPromptAssistant();
    syncStudioSelection();
  };

  const selectGalleryImage = (item, entry) => {
    restoredImages.delete(item.index);
    draftAssets.delete(imageDraftField(item.index));
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
    markDraftDirty();
  };

  const clearRequestedLibraryMediaQuery = () => {
    const url = new URL(window.location.href);
    ["media", "template", "workflow", "slot", "role", "library_model"].forEach((name) => url.searchParams.delete(name));
    window.history.replaceState({}, "", `${url.pathname}${url.search}${url.hash}`);
  };

  const applyRequestedLibraryMedia = async () => {
    if (!requestedLibraryMediaID || requestedLibraryMediaHandled) return;
    requestedLibraryMediaHandled = true;
    const workflowTemplates = {
      "photoflow-krea2-edit": "image-to-image",
      "photoflow-flux2-edit": "image-to-image",
      "minimax-h3-video": "minimax-h3-video",
    };
    const template = requestedLibraryTemplateID || workflowTemplates[requestedLibraryWorkflowID] || "image-to-image";
    const workflowID = requestedLibraryWorkflowID || (template === "minimax-h3-video" ? "minimax-h3-video" : "photoflow-krea2-edit");
    const scenario = root.querySelector(`.scenario-choice[data-workflow-id="${CSS.escape(template)}"]`);
    if (!scenario || scenario.disabled) {
      showRepeatNotice("Сценарий недоступен", "У вашей учётной записи нет доступа к выбранному типу генерации.", true);
      clearRequestedLibraryMediaQuery();
      return;
    }
    if (!scenario.classList.contains("is-selected")) chooseScenario(scenario);
    const workflow = root.querySelector(`.generation-workflow-choice[data-preset-id="${CSS.escape(workflowID)}"]`);
    if (!workflow || workflow.disabled || workflow.hidden) {
      showRepeatNotice("Workflow недоступен", "Выберите другой способ генерации или проверьте доступность workflow.", true);
      clearRequestedLibraryMediaQuery();
      return;
    }
    if (!workflow.classList.contains("is-selected")) chooseGenerationWorkflow(workflow);
    if (requestedLibraryModelID) {
      const option = [...(model?.options || [])].find(item => item.value === requestedLibraryModelID && !item.disabled && !item.hidden);
      if (!option) { showRepeatNotice("Модель недоступна", "Выбранная модель больше не доступна. Повторите выбор назначения в результатах.", true); clearRequestedLibraryMediaQuery(); return; }
      model.value = option.value; model.dispatchEvent(new Event("change", { bubbles: true }));
    }

    if (template === "minimax-h3-video") {
      const frameRole = requestedLibraryRole === "first_frame" || requestedLibraryRole === "last_frame";
      if (frameRole && model?.selectedOptions?.[0]?.dataset.videoReferenceOnly === "true") { showRepeatNotice("Роль недоступна", "Эта модель принимает только свободные референсы, не первый или последний кадр.", true); clearRequestedLibraryMediaQuery(); return; }
      setMiniMaxMode(frameRole ? "frames" : "references");
      syncImageSlots();
      syncMiniMaxAudioReference();
    }
    const slotNumber = /^[1-9][0-9]*$/.test(requestedLibrarySlot) ? Number(requestedLibrarySlot) : 0;
    if (template === "minimax-h3-video" && (requestedLibraryRole === "first_frame" && slotNumber !== 1 || requestedLibraryRole === "last_frame" && slotNumber !== 2)) {
      showRepeatNotice("Роль не соответствует слоту", "Первый кадр относится к слоту 1, последний кадр к слоту 2. Повторите выбор назначения.", true);
      clearRequestedLibraryMediaQuery(); return;
    }
    const item = imageSlots.find((candidate) => candidate.index === slotNumber);
    if (!Number.isSafeInteger(slotNumber) || slotNumber > activeMaxInputImages() || !item || item.slot.hidden) {
      showRepeatNotice("Слот недоступен", "Выбранный workflow поддерживает меньше изображений. Выберите доступный слот.", true);
      clearRequestedLibraryMediaQuery();
      return;
    }
    const previousSource = mediaSlice.get().sources?.[String(item.index)];
    const previousFile = item.input?.files?.[0];
    const previousGallery = gallerySelections.get(item.index);
    const selectedModelID = model?.value;
    const stamp = generationPageEpoch, controller = new AbortController();
    pageRequests.add(controller);
    try {
      const payload = await feedback.requestJSON(`/generate/library/images?media=${encodeURIComponent(requestedLibraryMediaID)}`, { signal: controller.signal, message: "Не удалось загрузить изображение. Повторите перенос из результатов." });
      if (!generationPageActive || stamp !== generationPageEpoch) return;
      const raw = Array.isArray(payload.images) && payload.images.length === 1 ? payload.images[0] : null;
      if (payload.media_id !== requestedLibraryMediaID || !raw) throw new Error(`Изображение больше не доступно. Результаты хранятся ${mediaRetentionLabel}.`);
      if (!workflow.classList.contains("is-selected") || !scenario.classList.contains("is-selected") || model?.value !== selectedModelID || item.slot.hidden || item.input?.files?.[0] !== previousFile || gallerySelections.get(item.index) !== previousGallery || mediaSlice.get().sources?.[String(item.index)] !== previousSource) throw new Error("Параметры или изображение изменены во время загрузки. Повторите перенос из результатов.");
      const entry = { id: requestedLibraryMediaID, url: String(raw.url || ""), name: String(raw.filename || "Изображение"), modelName: String(raw.model_name || ""), createdUnix: Number(raw.created_unix), expiresUnix: Number(raw.expires_unix), sensitive: Boolean(raw.sensitive) };
      if (!entry.url || entry.expiresUnix <= Date.now()) throw new Error("Срок хранения изображения истёк.");
      if (item.role && requestedLibraryRole) item.role.value = requestedLibraryRole;
      selectGalleryImage(item, entry);
      showStep(2);
      const workflowLabel = workflow.querySelector("strong")?.textContent?.trim() || workflowID;
      showRepeatNotice("Изображение добавлено", `${entry.name} выбрано для «${workflowLabel}», слот ${slotNumber}.`);
      window.requestAnimationFrame(() => item.slot.scrollIntoView({ behavior: "smooth", block: "center" }));
    } catch (error) {
      if (generationPageActive && stamp === generationPageEpoch) showRepeatNotice("Не удалось добавить изображение", error.message || "Обновите медиатеку и выберите другой результат.", true);
    } finally {
      pageRequests.delete(controller);
      if (generationPageActive && stamp === generationPageEpoch) clearRequestedLibraryMediaQuery();
      else requestedLibraryMediaHandled = false;
    }
  };

  const handleImageSelection = (item) => {
    restoredImages.delete(item.index);
    draftAssets.delete(imageDraftField(item.index));
    const file = item.input?.files?.[0] || null;
    const previousURL = previewURLs.get(item.index);
    if (previousURL?.startsWith("blob:")) URL.revokeObjectURL(previousURL);
    previewURLs.delete(item.index);
    gallerySelections.delete(item.index);
    uploadedImages.delete(item.index);
    if (inputImages[item.index - 1]) inputImages[item.index - 1].value = "";
    if (!file) {
      clearImageSlot(item);
      syncStudioSelection();
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
    item.previewOpen?.addEventListener("click", () => {
      const url = previewURLs.get(item.index);
      if (url) openLightbox({ url, filename: selectedImageSource(item)?.name || `Фото ${item.index}`, media_type: "image" });
    });
    item.input?.addEventListener("input", () => handleImageSelection(item));
    item.remove?.addEventListener("click", () => {
      clearImageSlot(item);
      syncImageSlots();
      syncStudioSelection();
    });
    item.galleryButton?.addEventListener("click", () => openGalleryImagePicker(item));
    item.role?.addEventListener("change", () => {
      if (templateID.value === "image-to-image" && item.index === 1) item.role.value = "base_scene";
      syncReferenceMap();
      syncGenerationSummary();
      if (promptAssistantEnabled?.checked) invalidatePromptAssistant();
    });
  });

  imagePicker?.querySelectorAll("[data-gallery-image-picker-close]").forEach((button) => button.addEventListener("click", closeGalleryImagePicker));
  imagePickerRefresh?.addEventListener("click", () => { refreshGalleryPickerImages().catch(() => {}); });
  if (!imagePickerFocusTrap) document.addEventListener("keydown", (event) => { if (event.key === "Escape") closeGalleryImagePicker(); });

  const mediaUploads = generationModules.media?.createUploadController?.({ settled: () => syncStudioSelection() });
  const requireMediaUploads = () => {
    if (!mediaUploads) throw new Error("Модуль загрузки недоступен. Обновите страницу.");
    return mediaUploads;
  };
  const uploadSelectedImage = async (item) => {
    const field = imageDraftField(item.index);
    return requireMediaUploads().upload({
      field,
      readSource: () => selectedImageFile(item) || gallerySelections.get(item.index),
      changedMessage: "Выбранное фото изменилось",
      transfer: (source) => generationModules.media.uploadImageSource(source, {
        fetcher: fetch, csrf: form.elements.csrf?.value || "", errorMessage: "ComfyUI не принял фото",
      }),
      commit: (payload) => {
        uploadedImages.set(item.index, payload.value);
        draftAssets.delete(field);
        mediaSlice.dispatch({ type: "UPLOAD_SUCCESS", slot: item.index, value: payload.value });
        if (inputImages[item.index - 1]) inputImages[item.index - 1].value = payload.value;
        feedback.present(item.state, "ready", "Загружено в вашу сессию");
        return payload;
      },
    });
  };

  const uploadSelectedAV = async (kind) => {
    const field = `input_${kind}`;
    const input = kind === "audio" ? miniMaxAudioFile : miniMaxVideoFile;
    return requireMediaUploads().upload({
      field, readSource: () => input?.files?.[0], changedMessage: "Выбранный файл изменился",
      transfer: (source) => generationModules.media.uploadSource(source, { kind, fetcher: fetch, errorMessage: "Не удалось загрузить файл" }),
      commit: ({ value }) => {
        draftAssets.delete(field);
        if (kind === "audio") uploadedAudio = value;
        else uploadedVideo = value;
        syncMiniMaxAudioReference();
        const state = kind === "audio" ? miniMaxAudioState : miniMaxVideoState;
        feedback.present(state, "ready", "Загружено в вашу сессию");
        return value;
      },
    });
  };

  const prepareActiveMedia = async () => requireMediaUploads().prepare({
    selection: () => [templateID.value, generationWorkflowID.value, model?.value, miniMaxMode()].join("|"),
    validate: () => {
      if (hasMissingDraftAssets()) throw new Error("Материал черновика недоступен. Замените его или удалите.");
      if (wizardSlice.get().requiresImage && !hasSelectedImage(imageSlots[0]) && !uploadedImages.get(1)) {
        studio?.revealField(imageSlots[0]?.input);
        throw new Error("Добавьте основное фото для редактирования.");
      }
    },
    pending: () => pendingDraftFiles(),
    progress: (item, state, error) => {
      if (item.state && isCurrentPendingFile(item)) feedback.present(item.state, state === "error" ? "error" : "loading", state === "error" ? error.message || "Загрузка не удалась" : "Загружаем материал...");
    },
    ready: () => markDraftDirty(),
    changedMessage: "Материалы или режим изменились во время загрузки. Проверьте их и повторите запуск.",
  });


  const capturePayloadState = () => {
    const references = selectedReferenceMetadata();
    return {
      selection: { scenarioID: selectedChoice()?.dataset.workflowId || "", workflowID: selectedGenerationWorkflow()?.dataset.presetId || "" },
      assistant: {
        ...assistantSlice.get(), enabled: Boolean(promptAssistantEnabled?.checked),
        template: promptAssistantTemplate?.value || "", think: Boolean(promptAssistantThink?.checked),
        modelID: promptAssistantModel?.value || "",
        jobID: assistantSession?.snapshot().record?.id || "", draft: promptAssistantDraft?.value || "",
        negativeDraft: promptAssistantNegativeDraft?.value || "",
        corrections: assistantSession?.corrections() || {},
      },
      media: {
        images: imageSlots.map(item => ({
          slot: item.index, value: uploadedImages.get(item.index) || "", active: item.index <= activeMaxInputImages(),
          role: item.savedReferenceRole ?? item.role?.value ?? "details", name: selectedImageSource(item)?.name || "",
          reference: references.find(reference => reference.number === item.index),
        })),
        audio: { value: uploadedAudio, active: miniMaxAudioIsAvailable(), name: miniMaxAudioFile?.files?.[0]?.name || miniMaxAudioName?.textContent || "" },
        video: { value: uploadedVideo, active: miniMaxVideoIsAvailable(), name: miniMaxVideoFile?.files?.[0]?.name || miniMaxVideoName?.textContent || "", referenceAudio: Boolean(form.elements.video_reference_audio?.checked) },
        assets: draftAssets,
      },
      editor: { parentJobID: pendingParentJobID, quality: quality?.value || "", step: wizardSlice.get().step || 1, advanced: Boolean(generationAdvanced?.open) },
    };
  };
  const buildGenerationPayload = () => generationPayload.launch(capturePayloadState());

  const pendingDraftFiles = ({ all = false } = {}) => [
    ...imageSlots.filter((item) => (all || item.index <= activeMaxInputImages()) && !uploadedImages.get(item.index))
      .map((item) => ({ field: imageDraftField(item.index), source: selectedImageFile(item) || gallerySelections.get(item.index), upload: () => uploadSelectedImage(item), state: item.state })),
    { field: "input_audio", source: (all || miniMaxAudioIsAvailable()) && !uploadedAudio ? miniMaxAudioFile?.files?.[0] : null, upload: () => uploadSelectedAV("audio"), state: miniMaxAudioState },
    { field: "input_video", source: (all || miniMaxVideoIsAvailable()) && !uploadedVideo ? miniMaxVideoFile?.files?.[0] : null, upload: () => uploadSelectedAV("video"), state: miniMaxVideoState },
  ].filter((item) => item.source);

  const isCurrentPendingFile = (item) => pendingDraftFiles({ all: true }).some((current) => current.field === item.field && current.source === item.source);

  const applyGenerationDraft = (draft) => {
    if (!draftController?.apply) throw new Error("Модуль черновика недоступен. Обновите страницу.");
    draftController.apply(draft);
  };
  const renderGenerationDraft = (draft) => {
    clearedPrompt = null;
    const values = draft.values || {};
    if (draft.fresh) form.reset();
    pendingParentJobID = generationModules.jobSource?.validID(values.parent_job_id) ? values.parent_job_id : "";
    imageSlots.forEach(clearImageSlot);
    clearMiniMaxAudio();
    clearMiniMaxVideo();
    draftAssets.clear();
    resetPromptAssistantReview();
    restoreAssistantModel(values.assistant_model || "");
    if (!Object.keys(values).length) {
      form.reset();
      if (positive) positive.value = "";
      collectionSession?.restore("");
      root.querySelectorAll(".scenario-choice, .generation-workflow-choice").forEach((choice) => choice.classList.remove("is-selected"));
      templateID.value = generationWorkflowID.value = "";
      wizardSlice.dispatch({ type: "SELECT_SCENARIO", scenarioID: "", requiresImage: false, allowsImages: false });
      if (generationAdvanced) generationAdvanced.open = false;
      syncWorkflowFields();
      const first = root.querySelector(".scenario-choice:not(:disabled)");
      if (first) chooseScenario(first);
      showStep(1);
      return;
    }
    applySavedValues(values, { openStep: false });
    collectionSession?.restore(values.collection_id || "");
    imageSlots.forEach((item) => {
      const role = values[`image_role_${item.index}`];
      if (item.role && [...item.role.options].some((option) => option.value === role)) {
        if (item.savedReferenceRole !== undefined) item.savedReferenceRole = role;
        else item.role.value = role;
      }
    });
    syncImageSlots();
    (draft.assets || []).forEach((asset) => {
      if (!/^input_(?:image(?:_[2-9])?|audio|video)$/.test(asset.field)) return;
      draftAssets.set(asset.field, asset);
      const item = imageSlots.find((slot) => imageDraftField(slot.index) === asset.field);
      const message = asset.available ? "Восстановлено из черновика" : "Материал недоступен. Замените его или удалите.";
      if (item) {
        if (asset.available) {
          const source = { kind: "restored", name: asset.name, url: asset.url };
          restoredImages.set(item.index, source);
          uploadedImages.set(item.index, asset.value);
          inputImages[item.index - 1].value = asset.value;
          mediaSlice.dispatch({ type: "SELECT_SOURCE", slot: item.index, source });
          mediaSlice.dispatch({ type: "UPLOAD_SUCCESS", slot: item.index, value: asset.value });
        }
        applyImageSelectionPreview(item, { name: asset.name }, asset.available ? asset.url : "", message);
      } else if (asset.field === "input_audio") {
        uploadedAudio = asset.available ? asset.value : "";
        if (miniMaxAudioName) miniMaxAudioName.textContent = asset.name;
        if (asset.available) audioPlayer?.setSource(asset.url);
        if (miniMaxAudioState) miniMaxAudioState.textContent = message;
        if (miniMaxAudioPreview) miniMaxAudioPreview.hidden = false;
      } else {
        uploadedVideo = asset.available ? asset.value : "";
        if (asset.available) videoPlayer?.setSource(asset.url);
        if (miniMaxVideoName) miniMaxVideoName.textContent = asset.name;
        if (miniMaxVideoState) miniMaxVideoState.textContent = message;
        if (miniMaxVideoPreview) miniMaxVideoPreview.hidden = false;
      }
    });
    if (quality && values.quality_preset) quality.value = values.quality_preset;
    if (generationAdvanced) generationAdvanced.open = values.draft_advanced === "true";
    if (promptAssistantEnabled) promptAssistantEnabled.checked = values.assistant_enabled === "true";
    if (promptAssistantTemplate && values.assistant_template_used) promptAssistantTemplate.value = values.assistant_template_used;
    if (promptAssistantThink) promptAssistantThink.checked = values.assistant_think_used === "true";
    syncMiniMaxAudioReference();
    syncPromptAssistant();
    assistantSession.restore(values);
    syncBatchBuilder();
    syncGenerationSummary();
    syncImageSlots();
    syncStudioSelection();
    showStep(Math.max(1, Math.min(3, Number(values.draft_step) || 2)));
  };

  const initializeGenerationDraft = () => {
    draftController = generationModules.draft?.createSession?.({
      document, window, root, form, store: generationStore, advanced: generationAdvanced, feedback,
      parameters: generationPayload, capture: capturePayloadState, apply: renderGenerationDraft,
      files: { pending: () => pendingDraftFiles({ all: true }), inFlight: () => mediaUploads?.pendingCount || 0, current: isCurrentPendingFile },
      onNavigationBlocked: () => {
        document.dispatchEvent(new CustomEvent("workspace-overlay", { detail: "draft" }));
        studio?.show("configure");
      },
      onSaved: (draft) => (draft.assets || []).forEach((asset) => {
        const item = imageSlots.find((slot) => imageDraftField(slot.index) === asset.field);
        const current = item ? uploadedImages.get(item.index) : asset.field === "input_audio" ? uploadedAudio : uploadedVideo;
        if (asset.available && current === asset.value) draftAssets.set(asset.field, asset);
        else if (!asset.available && asset.id && draftAssets.get(asset.field)?.id === asset.id) {
          draftAssets.set(asset.field, asset);
          if (item) {
            uploadedImages.delete(item.index);
            restoredImages.delete(item.index);
            inputImages[item.index - 1].value = "";
            if (item.state) item.state.textContent = "Срок хранения истёк. Замените материал или удалите его.";
          } else if (asset.field === "input_audio") uploadedAudio = "";
          else uploadedVideo = "";
        }
      }),
    });
    return draftController?.load({ preserveLocal: Boolean(requestedVariantID || requestedLibraryMediaID || requestedJobID || requestedBatchID || jobSourceRequested) });
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
    studio?.revealField(field);
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
    if (payload?.queue) queueResource.accept(payload.queue);
  };

  const preflightResource = feedback.createResource({
    load: async signal => {
      try { await prepareActiveMedia(); }
      catch (error) { throw feedback.failure("validation", error.message || "Не удалось подготовить материалы для проверки."); }
      if (signal.aborted) throw feedback.failure("aborted");
      const checkedBody = buildGenerationPayload();
      const payload = await feedback.requestJSON("/generate/preflight", { method: "POST", body: checkedBody, signal, message: "Не удалось проверить готовность workflow. Повторите запрос." });
      if (typeof payload.ok !== "boolean" || !Array.isArray(payload.checks) || payload.checks.some(check => !check || typeof check !== "object")) throw feedback.failure("invalid", "Проверка не подтверждена. Повторите запрос.");
      if (checkedBody.toString() !== buildGenerationPayload().toString()) throw feedback.failure("stale", "Настройки изменились во время проверки. Проверьте их ещё раз.");
      return payload;
    },
    onValue: payload => { if (payload) renderPreflight(payload); },
    onState: state => {
      const failed = state.phase === "error" || state.authRequired;
      feedback.button(preflightButton, { busy: state.loading, disabled: state.loading || state.authRequired || !state.active });
      feedback.button(preflightRepeat, { busy: state.loading, disabled: state.loading || state.authRequired || !state.active });
      feedback.present(preflight, state.loading ? "loading" : failed || state.value?.ok === false ? "error" : state.value?.ok ? "ready" : "");
      const login = document.getElementById("generation-preflight-login");
      if (login) { login.hidden = !state.authRequired; login.href = loginURL(); }
      if (state.loading) {
        if (preflight) preflight.hidden = false;
        if (preflightChecks) preflightChecks.replaceChildren();
        feedback.text(preflightSummary, "Проверяем параметры и материалы...");
      } else if (failed) renderPreflight({ ok: false, checks: [{ level: "error", label: "Проверка", message: state.message }] });
      else if (!state.active && preflight && !preflight.hidden) feedback.text(preflightSummary, "Проверка приостановлена. Повторите её перед запуском.");
    },
    message: "Не удалось подготовить материалы для проверки. Проверьте выбранные файлы и повторите запрос.",
  });
  const runPreflight = async ({ reveal = true } = {}) => {
    if (!selectedChoice() || !selectedGenerationWorkflow() || !model?.value || !positive.value.trim()) {
      renderPreflight({ ok: false, checks: [{ level: "error", label: "Запуск", message: "Выберите сценарий, workflow, модель и заполните позитивный промт." }] });
      if (reveal) preflight?.scrollIntoView({ block: "center", behavior: "smooth" });
      return false;
    }
    const read = await preflightResource.refresh();
    if (generationPageActive && reveal) preflight?.scrollIntoView({ block: "center", behavior: "smooth" });
    return read && Boolean(preflightResource.snapshot().value?.ok);
  };

  let recipeOptionsSignature = "";
  const renderRecipes = state => {
    if (!recipeSelect) return;
    const signature = JSON.stringify(state.items.map(item => [item.id, item.name]));
    if (signature !== recipeOptionsSignature) {
      recipeOptionsSignature = signature;
      recipeSelect.replaceChildren(new Option("Выберите сохранённый набор", ""));
      state.items.forEach(recipe => recipeSelect.append(new Option(recipe.name, String(recipe.id))));
    }
    recipeSelect.value = state.selectedID;
    const blocked = state.busy || state.read.loading || state.read.authRequired || !state.active;
    recipeSelect.disabled = blocked;
    feedback.button(recipeApply, { disabled: blocked || !state.selectedID });
    feedback.button(recipeDelete, { busy: state.operation === "remove", disabled: blocked || state.needsReview || !state.selectedID });
    feedback.button(recipeSave, { busy: state.operation === "save", disabled: blocked || state.needsReview });
    showReadFeedback("generation-recipe", state.read, { loading: "Загружаем наборы...", empty: "Сохранённых наборов пока нет." });
    const refresh = document.getElementById("generation-recipe-refresh");
    if (refresh) {
      refresh.hidden = state.read.authRequired;
      feedback.button(refresh, { busy: state.read.loading, disabled: state.busy || state.read.loading || state.read.authRequired || !state.active });
    }
    if (recipeState) {
      recipeState.hidden = !state.message || state.read.authRequired;
      feedback.present(recipeState, state.phase, state.message);
    }
  };
  const recipeController = generationModules.recipes.createController({
    feedback, slice: recipeSlice, onState: renderRecipes,
    transport: (operation, body, signal) => feedback.requestJSON(operation === "remove" ? "/generate/recipes/delete" : "/generate/recipes", {
      signal, method: operation === "read" ? "GET" : "POST", body: body || undefined,
      message: operation === "read" ? "Не удалось обновить наборы. Повторите запрос." : "Не удалось подтвердить изменение набора.",
    }),
  });
  const refreshRecipes = () => recipeController.refresh();
  const setRecipeState = (message, state = "") => recipeController.notify(message, state);
  document.getElementById("generation-recipe-refresh")?.addEventListener("click", refreshRecipes);

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
    const body = buildGenerationPayload();
    body.set("recipe_name", name);
    const saved = await recipeController.save(body);
    if (saved.ok && recipeName?.value.trim() === name) recipeName.value = "";
  };

  const applyRecipe = () => {
    const recipe = generationModules.recipes.selectedRecipe(recipeSlice.get());
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
    const body = new URLSearchParams({ csrf: form.elements.csrf?.value || "", recipe_id: id });
    await recipeController.remove(body);
  };

  const captureBranchEditor = () => ({
    values: generationPayload.draft(capturePayloadState()).toString(),
    files: [...imageSlots.map(item => selectedImageFile(item) || gallerySelections.get(item.index) || restoredImages.get(item.index) || null),
      miniMaxAudioFile?.files?.[0] || null, miniMaxVideoFile?.files?.[0] || null],
  });
  const applyBatchBranch = (payload, batch, job, captured) => {
    const current = captureBranchEditor();
    if (captured.values !== current.values || captured.files.some((file, index) => file !== current.files[index])) {
      throw new Error("Параметры или материалы изменились. Откройте новую ветку ещё раз.");
    }
    applyGenerationDraft({ values: { ...payload.values, parent_job_id: payload.parent_job_id || job.job_id, assistant_enabled: "false", assistant_job_id: "" }, assets: payload.assets || [] });
    pendingParentJobID = payload.parent_job_id || job.job_id;
    if (batchEnabled) batchEnabled.checked = true;
    const mode = batch.mode === "parameter" ? "parameter" : "seeds";
    const modeControl = [...(form.elements.batch_mode || [])].find?.(control => control.value === mode);
    if (modeControl) modeControl.checked = true;
    if (batchCount) batchCount.value = String(generationModules.batch.boundedCount(batch.total_count || 4));
    syncBatchBuilder();
    if (mode === "parameter" && batchParameter) {
      batchParameter.value = batch.parameter_name || "";
      const difference = (batch.differences || []).find(item => item.name === batch.parameter_name);
      const values = (difference?.values || []).map(item => numericValue(item.value, NaN)).filter(Number.isFinite);
      if (values.length && batchFrom && batchTo) {
        batchFrom.value = String(Math.min(...values)); batchTo.value = String(Math.max(...values));
      }
    }
    syncBatchBuilder(); markDraftDirty();
    showRepeatNotice("Ветка подготовлена", payload.requires_inputs
      ? "Параметры победителя восстановлены. Добавьте исходные файлы заново и настройте следующее различие."
      : "Параметры победителя восстановлены. Измените диапазон или параметр и запустите следующий пакет.");
    window.requestAnimationFrame(() => form.scrollIntoView({ behavior: "smooth", block: "start" }));
  };
  const historyController = generationModules.history.createController({
    job: jobSlice, batch: batchSlice, history: historySlice, batchAPI: generationModules.batch,
    transport: generationModules.history.createTransport({ csrf: () => form.elements.csrf?.value || "", fetcher: fetch }),
    live: window.AIGatewayLiveEvents, storage: () => window.localStorage, requestedJobID, requestedBatchID,
    onState: state => resultsView.render(state), onNotice: showRepeatNotice, onRevoked: () => resultsView.clear(),
    onVariants: () => { if (imagePicker && !imagePicker.hidden) renderGalleryImagePicker(); restoreRequestedVariant(); },
    onVariantsError: () => {
      if (requestedVariantID && !requestedVariantHandled) showRepeatNotice("Не удалось загрузить вариант", "Проверьте соединение и обновите страницу.", true);
    },
    onRepeat: (id, trigger) => {
      if (window.AIGatewayGeneration?.openRepeat) window.AIGatewayGeneration.openRepeat(id, trigger);
      else showRepeatNotice("Повтор недоступен", "Обновите страницу и откройте повтор заново.", true);
    },
    cancelActive: id => {
      if (submissionController?.snapshot().record?.jobID !== id) return false;
      submissionController.cancel(); return true;
    },
    branchEditor: { capture: captureBranchEditor, apply: applyBatchBranch },
  });
  resultsView.bind(historyController);
  const refreshJobs = () => historyController.refresh();
  const refreshVariants = () => historyController.refreshVariants();

  const updateGenerationQuota = (quota) => {
    if (!quota || typeof quota !== "object") return;
    const datasetValues = {};
    ["image", "video"].forEach((kind) => {
      const bucket = quota[kind];
      if (!bucket || typeof bucket !== "object") return;
      const prefix = kind[0].toUpperCase() + kind.slice(1);
      datasetValues[`generation${prefix}DailyRemaining`] = bucket.daily_remaining;
      datasetValues[`generation${prefix}DailyLimit`] = bucket.daily_limit;
      datasetValues[`generation${prefix}TotalRemaining`] = bucket.total_remaining;
      datasetValues[`generation${prefix}TotalLimit`] = bucket.total_limit;
    });
    Object.entries(datasetValues).forEach(([name, value]) => {
      if (Number.isFinite(Number(value))) root.dataset[name] = String(Math.max(0, Number(value)));
    });
    if (!generationQuota) {
      syncBatchBuilder();
      return;
    }
    const assign = (selector, value) => {
      const target = generationQuota.querySelector(selector);
      if (target && Number.isFinite(Number(value))) target.textContent = String(Math.max(0, Number(value)));
    };
    ["image", "video"].forEach((kind) => {
      const bucket = quota[kind];
      if (!bucket || typeof bucket !== "object") return;
      assign(`[data-generation-quota-${kind}-daily-remaining]`, bucket.daily_remaining);
      assign(`[data-generation-quota-${kind}-daily-limit]`, bucket.daily_limit);
      assign(`[data-generation-quota-${kind}-total-remaining]`, bucket.total_remaining);
      assign(`[data-generation-quota-${kind}-total-limit]`, bucket.total_limit);
    });
    syncBatchBuilder();
  };

  const setSubmissionActionLabel = (button, text) => {
    if (!button) return;
    const label = button?.querySelector("span");
    const compact = button === cancelGeneration ? {
      "Отменить генерацию": "Отменить", "Отменить пакет": "Отменить пакет", "Отмена запрошена": "Отменяем",
    }[text] : "";
    feedback.text(label, compact || text);
    button.setAttribute("aria-label", text);
    button.title = text;
    button.classList.toggle("is-status-check", button === retryGeneration && text === "Проверить состояние");
  };
  const setSubmissionTask = (record) => {
    const id = record?.kind === "batch" ? record.batchID : record?.jobID;
    if (!submissionTask || !id) return false;
    const kind = record.kind === "batch" ? "generation_batch" : "generation";
    submissionTask.href = `/tasks?${new URLSearchParams({ task: kind + ":" + id })}`;
    return true;
  };
  let submissionPresented = false;
  submissionController = generationModules.submission.createController({
    persistence: generationModules.submission.createPersistence({ storage: () => window.localStorage, owner: root.dataset.generationOwner }),
    transport: generationModules.submission.createTransport({ csrf: () => form.elements.csrf?.value || "" }),
    newRequestID: newGenerationRequestID,
    socket: root.dataset.generationLiveProgress === "true" ? () => {
      const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
      return new WebSocket(`${protocol}//${window.location.host}/generate/ws`);
    } : undefined,
    onState: state => {
      result.dataset.submissionState = state.phase;
      resultsView.submissionPhase(state.phase);
      feedback.button(generationSubmit, { busy: state.phase === "sending" });
      syncBatchBuilder();
      if (generationSubmit) generationSubmit.disabled = submitInFlight || submissionBlocksLaunch() || generationSubmit.dataset.batchInvalid === "true";
      result.setAttribute("aria-busy", String(state.phase === "sending"));
      if (["finished", "batch_accepted"].includes(state.phase)) return;
      if (state.phase === "idle") {
        resultSourceJobID = "";
        if (submissionPresented) {
          resultTitle.textContent = "Можно запустить генерацию";
          resultStatus.textContent = "Параметры сохранены в редакторе.";
          result.classList.remove("has-error");
          setSubmissionActionLabel(retryGeneration, "Повторить");
          setGenerationActions({ retry: true });
        }
        return;
      }
      const firstPresentation = !submissionPresented;
      submissionPresented = true;
      result.hidden = false;
      if (previousResult) previousResult.hidden = !outputGrid.childElementCount;
      if (firstPresentation || state.phase === "sending") {
        studio?.show("result");
        window.requestAnimationFrame(() => result.scrollIntoView({ block: "start", behavior: "smooth" }));
      }
      const knownTask = setSubmissionTask(state.record);
      resultSourceJobID = "";
      const cancelling = state.phase === "cancelling" || state.record?.cancelling;
      const paused = ["paused", "auth_required", "storage_error"].includes(state.phase);
      setSubmissionActionLabel(retryGeneration, "Проверить состояние");
      setSubmissionActionLabel(cancelGeneration, cancelling ? "Отмена запрошена" : state.record?.kind === "batch" ? "Отменить пакет" : "Отменить генерацию");
      if (cancelGeneration) cancelGeneration.disabled = Boolean(cancelling) || state.phase === "auth_required";
      setGenerationActions({ retry: true, cancel: state.pending, authenticate: state.phase === "auth_required", task: knownTask });
      if (submissionLogin) submissionLogin.href = `/login?${new URLSearchParams({ next: window.location.pathname + window.location.search })}`;
      const titles = {
        sending: state.record?.kind === "batch" ? "Создаём пакет вариантов" : "Отправляем задание",
        waiting: state.payload?.dispatch_waiting ? "В очереди Gateway" : "Проверяем запуск",
        running: state.payload?.state === "queued" ? "В очереди ComfyUI" : "Генерация выполняется",
        recovering: "Проверяем состояние",
        cancelling: "Отменяем генерацию",
        paused: "Проверка приостановлена",
        auth_required: "Нужен повторный вход",
        storage_error: "Не удалось сохранить состояние",
      };
      resultTitle.textContent = titles[state.phase] || "Проверяем состояние";
      const countdown = state.retrySeconds && state.phase === "recovering" ? ` Следующая проверка через ${state.retrySeconds} сек.` : "";
      feedback.present(resultStatus, state.phase, state.message + countdown);
      result.classList.toggle("has-error", ["auth_required", "storage_error"].includes(state.phase));
      if (paused || cancelling || ["sending", "waiting", "recovering"].includes(state.phase)) runProgress.hidden = true;
      else if (!state.live) {
        if (state.payload?.state === "queued") setQueueProgress(queuePositionDetail(state.payload.queue_position, state.payload.queue_total, state.payload.estimated_wait_seconds));
        else setGenerationProgress("Генерация выполняется", "Ожидаем результат", null);
      }
    },
    onAccepted: payload => {
      renderOutputs([]);
      result.classList.remove("has-viewer");
      pendingParentJobID = "";
      updateGenerationQuota(payload.quota);
      refreshQueueOverview();
      refreshJobs().catch(() => {});
    },
    onProgress: renderSubmissionProgress,
    onFinished: (payload, record, outcome) => {
      const state = payload.state || payload.batch?.state;
      const failed = ["error", "failed", "expired"].includes(state);
      const focusedAction = resultActions?.contains(document.activeElement);
      result.classList.toggle("has-error", failed);
      runProgress.hidden = true;
      if (cancelGeneration) cancelGeneration.disabled = false;
      setSubmissionActionLabel(retryGeneration, "Повторить");
      resultSourceJobID = record.kind === "single" ? record.jobID || "" : "";
      setGenerationActions({ retry: outcome !== "batch_accepted", repeatNow: Boolean(resultSourceJobID), task: setSubmissionTask(record),
        view: Boolean(resultSourceJobID && state === "completed") });
      if (record.kind === "batch") {
        if (payload.batch) historyController.acceptBatch(payload.batch);
        historyController.setCollapsed(false);
        resultTitle.textContent = outcome === "batch_accepted" ? "Пакет сохранён" : state === "cancelled" ? "Пакет отменён" : "Пакет завершён";
        resultStatus.textContent = payload.message || payload.error || (outcome === "batch_accepted" ? "Состояние вариантов доступно в задачах." : "Готовые варианты сохранены.");
      } else if (state === "completed") {
        resultTitle.textContent = "Готово";
        const media = payload.outputs?.length ? payload.outputs : payload.job?.media || [];
        renderOutputs(media.map(item => ({ ...item, comparison_job_id: resultSourceJobID })));
        if (media.length) setGenerationProgress("Готово", "Результат подготовлен", 100);
        resultStatus.textContent = media.length ? payload.message || "Результат сохранён." : "Задание завершено, но сохранённый файл уже недоступен.";
        refreshVariants().catch(() => {});
      } else {
        resultTitle.textContent = state === "cancelled" ? "Генерация отменена" : state === "expired" ? "Генерация прервана" : "Генерация завершилась с ошибкой";
        resultStatus.textContent = payload.message || payload.error || "Параметры сохранены, можно повторить попытку.";
      }
      if (focusedAction) resultTitle.focus({ preventScroll: true });
      if (record.kind === "single" && state === "completed" && !document.hidden && resultSourceJobID) {
        const completedID = resultSourceJobID;
        refreshJobs().then(() => {
          if (resultSourceJobID === completedID && !document.hidden && resultsView.openJob(completedID)) result.classList.add("has-viewer");
        }).catch(() => {});
      } else refreshJobs().catch(() => {});
      syncBatchBuilder();
    },
  });

  const startSubmission = async (kind) => {
    result.hidden = false;
    studio?.show("result");
    try {
      await submissionController.start(buildGenerationPayload(), {
        kind, video: selectedGenerationWorkflow()?.dataset.family === "minimax_h3",
        correlationID: assistantSlice.get().correlationID || "",
      });
    } catch (error) {
      resultTitle.textContent = "Не удалось отправить задание";
      resultStatus.textContent = error.message || "Проверьте состояние запроса.";
      result.classList.add("has-error");
      runProgress.hidden = true;
      setSubmissionActionLabel(retryGeneration, submissionBlocksLaunch() ? "Проверить состояние" : "Повторить");
      setGenerationActions({ retry: true, cancel: submissionController.snapshot().pending });
    }
  };
  const launchGenerationBatch = async () => {
    const validation = syncBatchBuilder();
    if (!validation.valid) {
      batchBuilder?.scrollIntoView({ block: "center", behavior: "smooth" });
      (batchParameter?.value ? batchFrom : batchParameter)?.focus({ preventScroll: true });
      return;
    }
    await startSubmission("batch");
  };

  root.querySelectorAll("[data-generation-profile]").forEach((button) => {
    button.addEventListener("click", () => applyGenerationProfile(button.dataset.generationProfile));
  });
  generationOpenExact?.addEventListener("click", () => {
    studio?.openSettings();
  });
  form.addEventListener("input", (event) => {
    if (event.target?.name in profileValues("balanced") || event.target?.closest(".studio-basic-settings, .studio-models")) clearProfileFeedback();
    syncBatchBuilder();
    syncGenerationSummary();
    if (!event.target.closest("#prompt-assistant")) assistantSession?.syncContext();
  });
  form.addEventListener("change", (event) => {
    if (event.target?.name?.startsWith("aihub_")) { syncWorkflowFields(); syncImageSlots(); syncAIHubFields(); }
    if (event.target?.name in profileValues("balanced") || event.target?.closest(".studio-basic-settings, .studio-models")) clearProfileFeedback();
    syncBatchBuilder();
    syncGenerationSummary();
    if (!event.target.closest("#prompt-assistant")) assistantSession?.syncContext();
  });
  batchParameter?.addEventListener("change", () => syncBatchBuilder({ resetRange: true }));
  recipeSelect?.addEventListener("change", () => {
    recipeController.select(recipeSelect.value);
  });
  recipeApply?.addEventListener("click", applyRecipe);
  recipeDelete?.addEventListener("click", deleteRecipe);
  recipeSave?.addEventListener("click", saveRecipe);
  preflightButton?.addEventListener("click", () => runPreflight());
  preflightRepeat?.addEventListener("click", () => runPreflight({ reveal: false }));
  retryGeneration?.addEventListener("click", () => {
    if (submissionBlocksLaunch()) submissionController.refresh();
    else if (resultSourceJobID) generationModules.openRepeat?.(resultSourceJobID);
    else studio?.show("configure");
  });
  repeatGenerationNow?.addEventListener("click", () => {
    if (!submissionBlocksLaunch() && resultSourceJobID) generationModules.openRepeatNow?.(resultSourceJobID, repeatGenerationNow);
  });
  cancelGeneration?.addEventListener("click", () => submissionController.cancel());

  const submitGeneration = async () => {
    if (submissionBlocksLaunch()) {
      result.hidden = false;
      studio?.show("result");
      submissionController.refresh();
      return;
    }
    if (!form.checkValidity()) {
      const invalid = [...form.elements].find((field) => field.willValidate && !field.validity.valid);
      studio?.revealField(invalid);
      invalid?.reportValidity();
      return;
    }
    if (!selectedChoice() || !selectedGenerationWorkflow() || !model?.value || !positive.value.trim()) {
      studio?.revealField(!positive.value.trim() ? positive : model);
      return;
    }
    if (loraVerificationController?.active()) { await loraVerificationController.quote(); return; }
    const batchLaunch = Boolean(batchEnabled?.checked);
    if (!await assistantSession.beforeLaunch()) {
      openPromptAssistant();
      return;
    }
    if (!await runPreflight({ reveal: false })) {
      preflight?.scrollIntoView({ block: "center", behavior: "smooth" });
      return;
    }
    if (batchLaunch) {
      await launchGenerationBatch();
      return;
    }
    await startSubmission("single");
  };
  if (generationModules.loraVerification) {
    loraVerificationController = generationModules.loraVerification.create({
      form, root, getPayload: buildGenerationPayload, prepareInputs: () => runPreflight({ reveal: false }),
      newRequestID: newGenerationRequestID,
      onDirty: markDraftDirty, showResults: () => { studio?.show("result"); refreshJobs(); },
      renderMedia: media => renderJobMedia({ media: [media], template_id: "text-to-image", state: "completed" }),
    });
  }
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (jobSourceRequested && (!jobSourceController || jobSourceController.snapshot().blocked)) {
      document.getElementById("generation-job-source")?.focus({ preventScroll: false });
      return;
    }
    if (submitInFlight) return;
    submitInFlight = true;
    try { await submitGeneration(); }
    finally {
      submitInFlight = false;
      syncBatchBuilder();
      if (generationSubmit && !loraVerificationController?.active()) generationSubmit.disabled = submissionBlocksLaunch() || generationSubmit.dataset.batchInvalid === "true";
    }
  });

  if (jobSourceRequested && generationModules.jobSource) {
    const banner = document.getElementById("generation-job-source");
    const message = document.getElementById("generation-job-source-message");
    const retry = document.getElementById("generation-job-source-retry");
    const replace = document.getElementById("generation-job-source-replace");
    const keep = document.getElementById("generation-job-source-keep");
    const back = document.getElementById("generation-job-source-back");
    back.hidden = !generationModules.jobSource.validID(sourceID);
    if (!back.hidden) {
      back.href = loraSourceRequested ? `/train-lora?job=${encodeURIComponent(sourceID)}` : `/tasks?task=generation:${encodeURIComponent(sourceID)}`;
      if (loraSourceRequested) back.textContent = "К LoRA";
      try {
        const target = new URL(requestedQuery.get("return_to") || "", window.location.origin);
        if (!loraSourceRequested && target.origin === window.location.origin && ["/tasks", "/preview/tasks"].includes(target.pathname)) back.href = target.pathname + target.search;
      } catch (_) {}
    }
    jobSourceController = generationModules.jobSource.createController({
      id: sourceID, kind: loraSourceRequested ? "lora_training" : "generation", enabled: true,
      load: async (id, signal) => {
        const conflict = ["variant", "media", "job"].some(key => requestedQuery.has(key)) || (loraSourceRequested
          ? requestedQuery.has("edit_job") || requestedQuery.getAll("lora_job").length !== 1 || requestedQuery.getAll("lora_model").length !== 1 || !requestedLoraModelID || requestedLoraModelID.length > 3000 || ["template", "workflow", "slot", "role"].some(key => requestedQuery.has(key))
          : requestedQuery.getAll("edit_job").length !== 1);
        if (conflict) throw new Error("Некорректный адрес или выбрано несколько источников. Откройте одну задачу или LoRA из списка.");
        if (loraSourceRequested) {
          const response = await fetch(`/api/lora-training/source/${encodeURIComponent(id)}?${new URLSearchParams({ model: requestedLoraModelID })}`, {
            credentials: "same-origin", cache: "no-store", signal: AbortSignal.any([signal, AbortSignal.timeout(60000)]),
          });
          if (response.redirected || response.status === 401) throw new Error("Сеанс завершён. Войдите в аккаунт снова.");
          const payload = await response.json().catch(() => null);
          if (!response.ok || !payload) throw new Error(payload?.error || "Не удалось проверить LoRA. Повторите запрос.");
          return payload;
        }
        const body = new URLSearchParams({ csrf: form.elements.csrf?.value || "", job_id: id });
        const response = await fetch("/generate/jobs/retry", {
          method: "POST", headers: { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8" }, body,
          credentials: "same-origin", cache: "no-store", signal: AbortSignal.any([signal, AbortSignal.timeout(20000)]),
        });
        if (response.redirected || response.status === 401) throw new Error("Сеанс завершён. Войдите в аккаунт снова.");
        const payload = await response.json().catch(() => null);
        if (!response.ok || !payload) throw new Error(payload?.error || "Не удалось загрузить параметры задачи. Проверьте соединение и повторите попытку.");
        return payload;
      },
      apply: payload => {
        // Opening a saved job is editing, never an automatic repeat or batch.
        const values = { ...payload.values, parent_job_id: loraSourceRequested ? "" : payload.parent_job_id, assistant_enabled: "false", assistant_job_id: "", batch_enabled: "false" };
        applyGenerationDraft({ values, assets: payload.assets || [], fresh: true });
        if (loraSourceRequested && requestedQuery.get("lora_check") === "1") loraVerificationController?.start(sourceID, payload.source_name);
        studio?.show("configure");
        markDraftDirty();
      },
      onConsumed: () => {
        const url = new URL(window.location.href);
        if (url.searchParams.get(loraSourceRequested ? "lora_job" : "edit_job") === sourceID || !sourceID) {
          ["edit_job", "lora_job", "lora_model", "lora_check", "return_to"].forEach(key => url.searchParams.delete(key));
          window.history.replaceState(null, "", url.pathname + url.search + url.hash);
        }
      },
      onState: state => {
        const focused = document.activeElement;
        banner.hidden = state.status === "idle" || state.status === "kept";
        banner.dataset.state = state.status;
        banner.setAttribute("aria-busy", String(state.status === "loading" || state.status === "waiting"));
        message.textContent = state.status === "error" ? state.error : state.status === "conflict"
          ? "Параметры задачи загружены. Ваши изменения в форме пока не заменены."
          : state.status === "ready" ? loraSourceRequested ? state.requiresInputs ? "LoRA выбрана. Добавьте исходное изображение." : "LoRA выбрана. Генерация ещё не запущена." : state.requiresInputs ? "Параметры задачи перенесены. Некоторые исходные материалы недоступны: замените их или удалите." : "Параметры и исходные материалы перенесены. Генерация ещё не запущена."
          : state.status === "paused" ? "Перенос параметров приостановлен." : "Загружаем параметры исходной задачи...";
        retry.hidden = state.status !== "error";
        replace.hidden = state.status !== "conflict";
        keep.hidden = !state.blocked;
        keep.textContent = state.status === "conflict" ? "Оставить мои параметры" : "Не переносить параметры";
        if ([retry, replace, keep].includes(focused) && focused.hidden) {
          const target = !state.blocked ? positive : !retry.hidden ? retry : keep;
          target?.focus({ preventScroll: true });
        }
      },
    });
    retry.addEventListener("click", () => jobSourceController.start());
    replace.addEventListener("click", () => jobSourceController.confirmReplace());
    keep.addEventListener("click", () => jobSourceController.keepCurrent());
    const edit = event => { if (event.isTrusted) jobSourceController.markEdited(); };
    form.addEventListener("input", edit); form.addEventListener("change", edit);
    form.addEventListener("click", event => {
      if (event.target.closest(".scenario-choice, .generation-workflow-choice, [data-generation-profile], #generation-recipe-apply, #generation-draft-remote, #generation-draft-delete, [data-remove-image], #minimax-audio-remove, #minimax-video-remove, #prompt-assistant-apply, #prompt-assistant-keep")) edit(event);
    });
    document.addEventListener("visibilitychange", () => jobSourceController.setActive(!document.hidden));
    window.addEventListener("pagehide", () => jobSourceController.setActive(false));
    window.addEventListener("pageshow", () => jobSourceController.setActive(!document.hidden));
    jobSourceController.setActive(!document.hidden);
  } else if (jobSourceRequested) {
    showRepeatNotice("Не удалось открыть параметры задачи", "Обновите страницу или вернитесь к исходной задаче.", true);
  }

  collectionSession = generationModules.collectionWorkspace?.bindStudio({
    root, form, draft: () => draftController, validate: values => generationPayload.resolveSelection(values),
    revealDraft: ({ focus = true, scroll = true } = {}) => {
      studio?.show("configure");
      const bar = document.getElementById("generation-draft-bar");
      const disclosure = bar?.closest("details");
      if (disclosure) disclosure.open = true;
      if (scroll) bar?.scrollIntoView({ block: "nearest" });
      if (focus) document.getElementById(draftController?.snapshot().status === "conflict" ? "generation-draft-remote" : "generation-draft-save")?.focus();
    },
  });
  void collectionSession?.start();
  const initialID = root.dataset.selectedWorkflow || "";
  const workflowCapabilitiesReady = loadWorkflowCapabilities();
  const initial = (initialID ? root.querySelector(`[data-workflow-id="${CSS.escape(initialID)}"]:not(:disabled)`) : null) || root.querySelector(".scenario-choice:not(:disabled)");
  if (initial) chooseScenario(initial);
  let initialSetupPromise = null, initialSetupComplete = false, initialDraftPromise = null;
  const initializeEditor = () => {
    if (!generationPageActive || initialSetupComplete) return Promise.resolve();
    if (initialSetupPromise) return initialSetupPromise;
    const stamp = generationPageEpoch, current = () => generationPageActive && stamp === generationPageEpoch;
    initialSetupPromise = (async () => {
      await (initialDraftPromise ||= initializeGenerationDraft());
      if (!current()) return;
      const requestedCollection = new URLSearchParams(location.search).get("collection");
      if (!jobSourceRequested && generationModules.collections?.validID(requestedCollection)) await collectionSession?.request(requestedCollection);
      if (!current()) return;
      if (jobSourceController) await jobSourceController.start();
      if (!current()) return;
      if (!jobSourceRequested) await applyRequestedLibraryMedia();
      if (!current()) return;
      assistantSession.initialize({ allowResume: !jobSourceRequested });
      initialSetupComplete = true;
    })().catch(error => {
      if (current()) showRepeatNotice("Не удалось восстановить редактор", error.message || "Обновите страницу, чтобы повторить восстановление.", true);
    }).finally(() => {
      initialSetupPromise = null;
      if (generationPageActive && !initialSetupComplete && generationPageEpoch !== stamp) void initializeEditor();
    });
    return initialSetupPromise;
  };
  kreaProUI = generationModules.kreaPro?.bind({form,onChange:()=>{syncWorkflowFields();syncImageSlots();syncGenerationSummary();assistantSession.syncContext();}});
  [form.elements.krea_pro_mode,form.elements.krea_pro_upscale,form.elements.krea_pro_match_source].forEach(input=>input?.addEventListener("change",()=>{applyKreaProSourceSize();syncWorkflowFields();syncImageSlots();calculateResolution();syncGenerationSummary();assistantSession.syncContext();}));
  outputMegapixels?.addEventListener("input",()=>{applyKreaProSourceSize();calculateResolution();});
  [width,height,aspect].forEach(input=>input?.addEventListener("input",event=>{if(event.isTrusted && isKreaProSelected() && form.elements.krea_pro_match_source)form.elements.krea_pro_match_source.checked=false;}));
  workflowCapabilitiesReady.then(initializeEditor);
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
  syncBatchBuilder();
  refreshQueueOverview();
  refreshRecipes();
  historyController.start(!document.hidden);
  resultsView.setVisible(!document.hidden);
  submissionController.setVisible(!document.hidden);
  submissionController.resume();
  const queuePoll = window.AIGatewayLiveEvents.poll(() => {
    if (!historyController.snapshot().authRequired) return refreshQueueOverview();
  }, 5000, 30000);
  queuePoll.start();
  window.addEventListener("pagehide", () => {
    generationPageActive = false;
    generationPageEpoch++;
    pageRequests.forEach(request => request.abort()); pageRequests.clear();
    [capabilitiesResource, queueResource, galleryResource, preflightResource].forEach(resource => resource.pause());
    recipeController.pause(); closeGalleryImagePicker();
    closeLightbox();
    submissionController.suspend(); historyController.suspend(); resultsView.suspend(); queuePoll.stop();
  });
  window.addEventListener("pageshow", event => {
    if (!event.persisted || generationPageActive) return;
    generationPageActive = true;
    capabilitiesResource.resume({ read: !capabilitiesResource.snapshot().loaded });
    queueResource.resume(); preflightResource.resume({ read: false }); recipeController.resume();
    void initializeEditor();
    submissionController.setVisible(!document.hidden); submissionController.resume();
    historyController.setVisible(!document.hidden); historyController.resume();
    resultsView.setVisible(!document.hidden); queuePoll.start();
  });
  document.addEventListener("visibilitychange", () => {
    submissionController.setVisible(!document.hidden);
    queuePoll.stop(); resultsView.setVisible(!document.hidden);
    if (!generationPageActive) return;
    queuePoll.start(); historyController.setVisible(!document.hidden);
    if (!document.hidden && !historyController.snapshot().authRequired) refreshQueueOverview();
  });
  generationStore?.emit?.("page:ready", { root, modules: root.dataset.generationModules });
})();
