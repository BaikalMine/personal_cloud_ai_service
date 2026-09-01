(function bootstrapGenerationBatch(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (!root) return;
  root.AIGatewayGeneration = root.AIGatewayGeneration || {};
  root.AIGatewayGeneration.batch = api;
})(typeof window !== "undefined" ? window : null, function generationBatchFactory() {
  const MIN_COUNT = 2;
  const MAX_COUNT = 20;

  const createState = (overrides = {}) => ({
    enabled: false,
    mode: "seeds",
    count: 4,
    parameter: "",
    from: "",
    to: "",
    batches: [],
    selections: {},
    ...overrides,
  });

  const reduce = (state = createState(), action = {}) => {
    switch (action.type) {
      case "SET_CONFIG":
        return { ...state, ...action.config };
      case "SET_BATCHES": {
        const batches = Array.isArray(action.batches) ? action.batches : [];
        const available = new Map(batches.map((batch) => [String(batch.batch_id), new Set((batch.jobs || []).map((job) => String(job.job_id)))]));
        const selections = {};
        Object.entries(state.selections || {}).forEach(([batchID, jobIDs]) => {
          const jobs = available.get(batchID);
          if (!jobs) return;
          const kept = (jobIDs || []).filter((jobID) => jobs.has(String(jobID))).slice(-2);
          if (kept.length) selections[batchID] = kept;
        });
        return { ...state, batches, selections };
      }
      case "TOGGLE_COMPARE": {
        const batchID = String(action.batchID || "");
        const jobID = String(action.jobID || "");
        if (!batchID || !jobID) return state;
        const current = [...(state.selections?.[batchID] || [])];
        const existing = current.indexOf(jobID);
        if (existing >= 0) current.splice(existing, 1);
        else current.push(jobID);
        return { ...state, selections: { ...state.selections, [batchID]: current.slice(-2) } };
      }
      case "CLEAR_COMPARE": {
        const selections = { ...state.selections };
        delete selections[String(action.batchID || "")];
        return { ...state, selections };
      }
      default:
        return state;
    }
  };

  const parameterOptions = (context = {}) => {
    const family = String(context.family || "");
    const template = String(context.templateID || "");
    const image = family === "krea2" || family === "flux2";
    const krea = family === "krea2";
    const flux = family === "flux2";
    const video = family === "minimax_h3";
    const definitions = [
      { name: "steps", label: "Шаги", min: 1, max: 100, step: 1, available: image },
      { name: "cfg", label: "CFG", min: 0, max: 30, step: 0.1, available: krea },
      { name: "denoise", label: "Сила изменения", min: 0.01, max: 1, step: 0.01, available: template === "image-to-image" && image },
      { name: "output_megapixels", label: "Итоговые мегапиксели", min: 0.5, max: 4.7, step: 0.1, available: krea && template === "text-to-image" },
      { name: "source_megapixels", label: "Мегапиксели исходного прохода", min: 0.25, max: 16, step: 0.25, available: flux && template === "image-to-image" },
      { name: "reference_boost", label: "Сохранение референса", min: 0, max: 8, step: 0.1, available: krea && template === "image-to-image" },
      { name: "flux_guidance", label: "Flux Guidance", min: 0, max: 10, step: 0.1, available: flux },
      { name: "flux_active_scale", label: "Flux Active Scale", min: 0, max: 10, step: 0.05, available: flux },
      { name: "upscale_factor", label: "Коэффициент апскейла", min: 1, max: 2, step: 0.05, available: krea && template === "image-to-image" },
      { name: "upscale_denoise", label: "Сила перерисовки апскейла", min: 0.01, max: 0.5, step: 0.01, available: krea },
      { name: "detail_denoise", label: "Сила финальной детализации", min: 0.005, max: 0.2, step: 0.005, available: krea && template === "text-to-image" },
      { name: "video_steps", label: "Шаги видео", min: 1, max: 100, step: 1, available: video },
      { name: "video_shift_video", label: "Сдвиг Video Sigma", min: 1, max: 30, step: 1, available: video },
      { name: "video_shift_audio", label: "Сдвиг Audio Sigma", min: 1, max: 30, step: 1, available: video },
      { name: "video_duration_seconds", label: "Длительность видео", min: 5, max: 15, step: 5, available: video },
      { name: "video_sparse_budget", label: "Бюджет Sparse Attention", min: 0.05, max: 1, step: 0.05, available: video && Boolean(context.sparseAttention) },
      { name: "video_rife_multiplier", label: "Множитель кадров RIFE", min: 2, max: 4, step: 1, available: video && Boolean(context.rife) },
      { name: "video_rtx_scale", label: "Масштаб RTX", min: 1, max: 4, step: 0.25, available: video && Boolean(context.rtx) },
      { name: "video_color_strength", label: "Сила ColorMatch", min: 0, max: 1, step: 0.05, available: video && Boolean(context.colorMatch) },
      { name: "video_sharpen_strength", label: "Сила резкости видео", min: 0, max: Number(context.sharpenMax) || 1, step: 0.05, available: video && Boolean(context.sharpen) },
      { name: "video_output_crf", label: "Качество H.264 (CRF)", min: 1, max: 51, step: 1, available: video },
    ];
    (Array.isArray(context.loras) ? context.loras : []).forEach((lora, index) => {
      if (!String(lora || "").trim()) return;
      definitions.push({ name: `lora_model_strength_${index + 1}`, label: `Сила LoRA ${index + 1}`, min: -4, max: 4, step: 0.05, available: true });
    });
    return definitions.filter((item) => item.available).map(({ available, ...item }) => item);
  };

  const boundedCount = (value) => Math.min(MAX_COUNT, Math.max(MIN_COUNT, Math.round(Number(value) || MIN_COUNT)));

  const remainingQuota = ({ dailyLimit = 0, dailyRemaining = 0, totalLimit = 0, totalRemaining = 0 } = {}) => {
    const values = [];
    if (Number(dailyLimit) > 0) values.push(Math.max(0, Number(dailyRemaining) || 0));
    if (Number(totalLimit) > 0) values.push(Math.max(0, Number(totalRemaining) || 0));
    return values.length ? Math.min(...values) : Infinity;
  };

  const validate = (config = {}, options = [], quota = {}) => {
    if (!config.enabled) return { valid: true, count: 1, error: "" };
    const count = boundedCount(config.count);
    const remaining = remainingQuota(quota);
    if (remaining < count) return { valid: false, count, error: `Доступно запусков: ${remaining}. Уменьшите количество вариантов.` };
    if (config.mode === "seeds") return { valid: true, count, error: "" };
    if (config.mode !== "parameter") return { valid: false, count, error: "Выберите способ изменения вариантов." };
    const option = options.find((item) => item.name === config.parameter);
    if (!option) return { valid: false, count, error: "Выберите параметр текущего workflow." };
    const from = Number(String(config.from).replace(",", "."));
    const to = Number(String(config.to).replace(",", "."));
    if (!Number.isFinite(from) || !Number.isFinite(to)) return { valid: false, count, error: "Укажите начало и конец диапазона." };
    if (from < option.min || from > option.max || to < option.min || to > option.max) {
      return { valid: false, count, error: `${option.label}: допустимо от ${option.min} до ${option.max}.` };
    }
    const unique = new Set();
    for (let index = 0; index < count; index += 1) {
      const raw = from + ((to - from) * index) / (count - 1);
      const rounded = Math.round(raw / option.step) * option.step;
      unique.add(rounded.toFixed(6));
    }
    if (unique.size !== count) return { valid: false, count, error: "Диапазон слишком узкий для выбранного количества вариантов." };
    return { valid: true, count, error: "", option, from, to };
  };

  const batchJobIDs = (batches) => new Set((Array.isArray(batches) ? batches : []).flatMap((batch) => (batch.jobs || []).map((job) => String(job.job_id))));

  const filterBatches = (batches, filters = {}) => (Array.isArray(batches) ? batches : []).filter((batch) => {
    const jobs = Array.isArray(batch.jobs) ? batch.jobs : [];
    return jobs.some((job) => (
      (!filters.stateFilter || job.state === filters.stateFilter)
      && (!filters.templateFilter || job.template_id === filters.templateFilter)
    ));
  });

  const selectedJobs = (state, batch) => {
    const selected = new Set(state?.selections?.[String(batch?.batch_id)] || []);
    return (batch?.jobs || []).filter((job) => selected.has(String(job.job_id)));
  };

  const selectedDifferences = (state, batch) => {
    const jobs = selectedJobs(state, batch);
    if (jobs.length !== 2) return [];
    const selected = new Set(jobs.map((job) => String(job.job_id)));
    return (batch?.differences || []).map((difference) => {
      const values = (difference.values || []).filter((item) => selected.has(String(item.job_id)));
      return { ...difference, values };
    }).filter((difference) => difference.values.length === 2 && new Set(difference.values.map((item) => String(item.value))).size === 2);
  };

  return {
    MIN_COUNT,
    MAX_COUNT,
    createState,
    reduce,
    parameterOptions,
    boundedCount,
    remainingQuota,
    validate,
    batchJobIDs,
    filterBatches,
    selectedJobs,
    selectedDifferences,
  };
});
