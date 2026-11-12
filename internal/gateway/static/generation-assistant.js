(function bootstrapGenerationAssistant(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (!root) return;
  root.AIGatewayGeneration = root.AIGatewayGeneration || {};
  root.AIGatewayGeneration.assistant = api;
})(typeof window !== "undefined" ? window : null, function generationAssistantFactory() {
  const createState = (overrides = {}) => ({
    status: "idle",
    approved: false,
    original: "",
    originalNegative: "",
    originalAudio: "",
    suggestion: "",
    negativeSuggestion: "",
    audioSuggestion: "",
    action: "",
    correlationID: "",
    references: [],
    usage: {},
    draftEdited: false,
    error: "",
    stale: false,
    ...overrides,
  });

  const reduce = (state = createState(), action = {}) => {
    switch (action.type) {
      case "RESET":
        return createState();
      case "REQUEST_START":
        return { ...createState(), status: "loading", original: String(action.original || ""), originalNegative: String(action.originalNegative || ""), originalAudio: String(action.originalAudio || "") };
      case "REQUEST_SUCCESS":
        return {
          ...state,
          status: "review",
          suggestion: String(action.suggestion || ""),
          negativeSuggestion: String(action.negativeSuggestion || ""),
          audioSuggestion: String(action.audioSuggestion || ""),
          correlationID: String(action.correlationID || ""),
          references: Array.isArray(action.references) ? action.references : [],
          usage: action.usage && typeof action.usage === "object" ? action.usage : {},
          draftEdited: false,
          error: "",
        };
      case "REQUEST_ERROR":
        return { ...state, status: "error", error: String(action.error || "Request failed") };
      case "INVALIDATE":
        return { ...state, stale: true, approved: false, action: "", status: "stale" };
		case "DECISION_ERROR":
		  return { ...state, status: "error", approved: false, error: String(action.error || "Decision was not saved") };
      case "APPLY":
		if (state.stale) return state;
		return {
		  ...state,
		  status: "approved",
		  approved: true,
		  action: action.edited || state.draftEdited ? "edited_after_apply" : "applied",
		};
      case "KEEP_ORIGINAL":
        return { ...state, status: "approved", approved: true, stale: false, action: "kept_original" };
		case "DRAFT_EDITED":
		  return { ...state, draftEdited: true };
      case "PROMPT_EDITED":
		return {
		  ...state,
		  approved: state.action === "applied" || state.action === "edited_after_apply",
		  action: state.action === "applied" || state.action === "edited_after_apply" ? "edited_after_apply" : state.action,
		};
      default:
        return state;
    }
  };

  const tokenize = (value) => String(value || "").match(/\s+|[^\s]+/gu) || [];

  const pushSegment = (segments, type, value) => {
	if (!value) return;
	const last = segments[segments.length - 1];
	if (last?.type === type) last.value += value;
	else segments.push({ type, value });
  };

  const fallbackDiff = (before, after) => ({
	original: before ? [{ type: "removed", value: before }] : [],
	suggestion: after ? [{ type: "added", value: after }] : [],
  });

  const diff = (before, after) => {
	const original = tokenize(before);
	const suggestion = tokenize(after);
	if (original.length * suggestion.length > 1_000_000) return fallbackDiff(String(before || ""), String(after || ""));
	const width = suggestion.length + 1;
	const table = new Uint16Array((original.length + 1) * width);
	for (let i = original.length - 1; i >= 0; i -= 1) {
	  for (let j = suggestion.length - 1; j >= 0; j -= 1) {
		table[i * width + j] = original[i] === suggestion[j]
		  ? table[(i + 1) * width + j + 1] + 1
		  : Math.max(table[(i + 1) * width + j], table[i * width + j + 1]);
	  }
	}
	const result = { original: [], suggestion: [] };
	let i = 0;
	let j = 0;
	while (i < original.length || j < suggestion.length) {
	  if (i < original.length && j < suggestion.length && original[i] === suggestion[j]) {
		pushSegment(result.original, "same", original[i]);
		pushSegment(result.suggestion, "same", suggestion[j]);
		i += 1;
		j += 1;
	  } else if (j < suggestion.length && (i === original.length || table[i * width + j + 1] >= table[(i + 1) * width + j])) {
		pushSegment(result.suggestion, "added", suggestion[j]);
		j += 1;
	  } else {
		pushSegment(result.original, "removed", original[i]);
		i += 1;
	  }
	}
	return result;
  };

  const decisions = new Set(["applied", "edited_after_apply", "kept_original"]);
  const supportsNegative = input => ["text-to-image", "image-to-image"].includes(input.mode) && (input.profile === "qwen21-edit" || input.workflow === "krea2-pro"
    || (input.mode === "image-to-image" ? input.workflow === "qwen-image21-edit" : ["krea2-ultra", "moss-image", "qwen-image21"].includes(input.workflow)));
  const promptCharacterLimit = input => {
    if (input.mode !== "minimax-h3-video") return 4000;
    const parts = Number(input.longVideo?.parts);
    return input.workflow !== "aihub-minimax-h3" && input.longVideo?.enabled && Number.isInteger(parts) && parts >= 2 && parts <= 20
      ? 4000 + parts * 1600 : 7000;
  };
  const staleMessage = "Подготовьте новый вариант для текущей модели и материалов.";
  const storageMessage = "Не удалось сохранить запрос в браузере. Разрешите хранение данных сайта и повторите.";
  /**
   * @typedef {{number:number, role:string, local:*, value:string}} AssistantImage
   * @typedef {Object} AssistantInput
   * @property {string} prompt Current editor text, not the assistant proposal.
   * @property {string} draft Current editable assistant proposal.
   * @property {boolean} enabled
   * @property {string} workflow
   * @property {string} model
   * @property {string} assistantModel Authorized assistant catalog ID, empty for default.
   * @property {string} mode Generation template ID.
   * @property {string} videoMode
   * @property {string} duration
   * @property {string} profile
   * @property {boolean} think
   * @property {AssistantImage[]} images Active images only, in slot order.
   * @property {{local:*, value:string}} audio Empty when inactive.
   * @property {{local:*, value:string}} video Empty when inactive.
   */
  const sourceFor = (input, identifier) => {
    const number = Number(String(identifier).match(/(?:Picture|image)\s*(\d+)/i)?.[1]);
    const reference = input.mode === "minimax-h3-video" ? input.images[number - 1] : input.images.find(item => item.number === number);
    return reference ? { slot: reference.number, value: reference.value || "" } : null;
  };
  /** Build the existing assistant wire contract from active editor inputs. */
  const buildRequest = (input, corrections = {}) => {
    const body = new URLSearchParams({ prompt: input.prompt.trim(), template_id: input.mode,
      assistant_template: input.profile || "workflow-default", assistant_think: input.think ? "true" : "false" });
    if (input.assistantModel) body.set("assistant_model", input.assistantModel);
    if (input.mode === "image-to-image") body.set("generation_workflow", input.workflow || "");
    if (supportsNegative(input)) {
      body.set("generation_workflow", input.workflow || "");
      if (input.workflow === "krea2-pro") body.set("krea_pro_mode", input.kreaProMode || "text");
      body.set("cfg", String(input.cfg || 1));
      body.set("negative_prompt", Number(input.cfg) > 1 ? input.negative || "" : "");
    }
    if (input.workflow === "aihub-minimax-h3") {
      body.set("generation_workflow", input.workflow);
      const options = input.aiHub || {};
      body.set("aihub_mode", options.mode || "video");
      body.set("aihub_carousel_pipeline", options.carouselPipeline || "legacy");
      body.set("aihub_carousel_refiner", options.carouselRefiner || "flux");
      body.set("aihub_carousel_source", options.carouselSource || "video");
      body.set("aihub_task", options.mode === "video" ? options.task || "reference" : "reference");
      body.set("aihub_audio_reference", options.audioReference ? "true" : "false");
      body.set("aihub_t8", options.t8 ? "true" : "false");
      body.set("aihub_match_duration", options.mode === "video" && options.matchDuration ? "true" : "false");
      body.set("aihub_audio_source", options.audioSource || "native");
      body.set("aihub_audio_prompt", options.audioPrompt || "");
      body.set("aihub_voice_conversion", options.voiceConversion ? "true" : "false");
      body.set("aihub_photo_frames", options.photoFrames || "50");
      body.set("video_reference_start", options.videoStart || "0");
      if (input.video.value) body.set("input_video", input.video.value);
      if (options.voiceConversion && input.audio.value) body.set("input_audio", input.audio.value);
    }
    if (input.mode === "minimax-h3-video") {
      body.set("video_mode", input.videoMode);
      body.set("video_duration_seconds", input.duration || "5");
      if (input.longVideo?.enabled) {
        body.set("video_long_enabled", "true");
        body.set("video_long_parts", String(input.longVideo.parts));
        body.set("video_long_transition", input.longVideo.transition || "continuous");
      }
      body.set("video_has_audio", input.workflow !== "aihub-minimax-h3" && input.audio.value ? "true" : "false");
      body.set("video_has_video", input.video.value ? "true" : "false");
    }
    for (const reference of input.images) {
      if (reference.value) body.set(reference.number === 1 ? "input_image" : `input_image_${reference.number}`, reference.value);
      if (input.mode === "image-to-image" || input.mode === "minimax-h3-video" && input.videoMode === "references") body.set(`image_role_${reference.number}`, reference.role);
    }
    for (const [identifier, correction] of Object.entries(corrections)) {
      const source = sourceFor(input, identifier);
      if (source?.value && source.value === correction.source) body.set(`image_correction_${source.slot}`, correction.text);
    }
    return body;
  };
  /** HTTP boundary shared by proposals, durable jobs and explicit decisions. */
  const createTransport = ({ csrf, fetcher = fetch, timeoutSignal = ms => AbortSignal.timeout(ms) }) => async (operation, fields, signal) => {
    const body = new URLSearchParams(fields), method = operation === "get" ? "GET" : "POST";
    const path = operation === "legacy" ? "/generate/prompt-assistant"
      : operation === "decision" ? "/generate/prompt-assistant/decision"
        : operation === "get" ? `/generate/prompt-assistant/jobs?${new URLSearchParams({ id: body.get("id") || "" })}`
          : operation === "cancel" ? "/generate/prompt-assistant/jobs/cancel"
            : operation === "submit" ? "/generate/prompt-assistant/jobs" : "";
    if (!path) throw new Error("Unknown assistant operation");
    const headers = { Accept: "application/json" };
    if (method === "POST") {
      body.set("csrf", csrf() || "");
      headers["Content-Type"] = "application/x-www-form-urlencoded;charset=UTF-8";
    }
    // The legacy endpoint keeps its server policy deadline; it is never retried.
    const signals = [signal, operation !== "legacy" && timeoutSignal(20000)].filter(Boolean);
    const response = await fetcher(path, { method, body: method === "GET" ? undefined : body, headers,
      credentials: "same-origin", cache: "no-store", redirect: "error", signal: signals.length ? AbortSignal.any(signals) : undefined });
    return { ok: response.ok, status: response.status, payload: await response.json().catch(() => ({})) };
  };
  /** Existing account-scoped job pointer; never stores prompts or reference data. */
  const createPersistence = ({ storage, owner, validID, onChange = () => {} }) => {
    if (!/^[1-9][0-9]*$/.test(String(owner || ""))) throw new Error(storageMessage);
    const key = `ai_gateway_assistant_job_v1_${owner}`;
    const normalize = record => {
      if (!record || !validID(record.id) || record.expiresAt !== undefined && (!Number.isFinite(record.expiresAt) || record.expiresAt <= 0)) throw new Error(storageMessage);
      return { id: record.id, ...(record.expiresAt ? { expiresAt: record.expiresAt } : {}), cancelling: Boolean(record.cancelling) };
    };
    return {
      read: () => { const raw = storage().getItem(key); return raw ? normalize(JSON.parse(raw)) : null; },
      persist: record => {
        const target = storage(), previous = target.getItem(key);
        const value = record ? JSON.stringify(normalize(record)) : null;
        if (previous === value) return;
        if (value === null) target.removeItem(key); else target.setItem(key, value);
        if (target.getItem(key) !== value) throw new Error(storageMessage);
        onChange();
      },
    };
  };
  /**
   * Owns assistant request context, review decisions and restored review state.
   * The existing jobs controller remains the only durable submit/recovery owner.
   * @param {Object} options
   * @param {{get:Function, dispatch:Function}} options.state Existing named store slice.
   * @param {()=>AssistantInput} options.read Active editor input adapter.
   * @param {()=>Promise<void>} options.prepare Shared active-media preparation.
   * @param {Function} options.transport createTransport-compatible HTTP boundary.
   * @param {Object} options.jobs Existing assistantJobs module.
   * @param {Object} options.persistence read/persist account-scoped pointer boundary.
   * @param {boolean} options.durable Legacy adapter opt-out; production uses durable jobs.
   * @returns {{snapshot:Function, improve:()=>Promise<boolean>, apply:()=>Promise<boolean>,
   * keep:()=>Promise<boolean>, restoreOriginal:()=>Promise<boolean>, editPrompt:()=>void, savePromptEdit:()=>Promise<void>,
   * beforeLaunch:()=>Promise<boolean>, restore:Function, reset:()=>void,
   * invalidate:()=>boolean, syncContext:()=>void, initialize:Function,
   * suspend:()=>void, resume:()=>void, cancel:Function, corrections:Function,
   * correction:Function, setCorrection:Function, editDraft:()=>void}}
   * Commands and read-only snapshots; no DOM or rendering owner.
   */
  const createSession = ({ state, read, prepare, transport, jobs, persistence, newRequestID, durable = true,
    onState = () => {}, onProgress = () => {}, onProposal = () => {}, onSavedResponse = () => {}, onApplied = () => {}, onKept = () => {},
    onReset = () => {}, onRestore = () => {}, onOriginalRestored = () => {}, onRequest = () => {}, onDirty = () => {} }) => {
    let context = "", serial = 0, preparing = false, deciding = 0, ready = false, active = true;
    let restoringOriginal = false;
    let originalRestore = Promise.resolve(false);
    let restoredJobID = "", legacyFlight = null, legacyInterrupted = false, decisionTail = Promise.resolve();
    const corrections = new Map(), localIDs = new WeakMap();
    let nextLocalID = 0, controller;
    const capture = () => {
      const input = read();
      return { ...input, images: input.images.map(image => ({ ...image })), audio: { ...input.audio }, video: { ...input.video } };
    };
    const correctionValues = () => Object.fromEntries([...corrections].map(([key, value]) => [key, { ...value }]));
    const localIdentity = source => {
      if (!source || typeof source !== "object") return source || "";
      if (!localIDs.has(source)) localIDs.set(source, `local-source-${++nextLocalID}`);
      return localIDs.get(source);
    };
    const contextKey = (input = capture(), persisted = false) => JSON.stringify({
      workflow: input.workflow, model: input.model, mode: input.mode, videoMode: input.videoMode, duration: input.duration, longVideo: input.longVideo,
      profile: input.profile, think: Boolean(input.think),
      ...(supportsNegative(input) ? { negative: input.negative, cfg: input.cfg } : {}),
      ...(input.workflow === "krea2-pro" ? {kreaProMode:input.kreaProMode} : {}),
      ...(input.assistantModel ? { assistantModel: input.assistantModel } : {}),
      ...(input.aiHub ? { aiHub: input.aiHub } : {}),
      images: input.images.map(ref => [ref.number, ref.role, persisted ? ref.value || "" : localIdentity(ref.local)]),
      audio: persisted ? input.audio.value || "" : localIdentity(input.audio.local),
      video: persisted ? input.video.value || "" : localIdentity(input.video.local),
      ...(persisted ? { corrections: [...corrections].filter(([id, item]) => sourceFor(input, id)?.value === item.source)
        .map(([id, item]) => [id, item.source, item.text]).sort((a, b) => a[0].localeCompare(b[0])) } : {}),
    });
    const snapshot = () => {
      const current = state.get(), job = controller?.snapshot() || { pending: false, record: null };
      const input = capture(), busy = preparing || job.pending || restoringOriginal;
      const usesLTX = input.workflow === "aihub-minimax-h3" && input.aiHub?.audioSource === "ltx";
      const hasOriginal = Boolean((current.original || usesLTX) && current.correlationID && ["applied", "edited_after_apply"].includes(current.action) && (input.prompt !== current.original || supportsNegative(input) && input.negative !== current.originalNegative || usesLTX && input.aiHub.audioPrompt !== current.originalAudio));
      return { state: current, job, record: job.record, busy, preparing, deciding: deciding > 0, ready, hasOriginal,
        canRestoreOriginal: ready && !busy && hasOriginal,
        canApply: !busy && !deciding && !current.stale && Boolean(current.correlationID && input.draft.trim()) && context === contextKey(input) };
    };
    const publish = (message = "", phase = "") => { if (active) onState({ ...snapshot(), message, phase }); };
    const invalidate = () => {
      if (state.get().status === "idle" || state.get().stale) return false;
      state.dispatch({ type: "INVALIDATE" });
      publish(staleMessage, "stale"); onDirty(); return true;
    };
    const syncContext = () => { if (context && context !== contextKey()) invalidate(); };
    const reset = () => {
      serial++; context = ""; restoredJobID = ""; corrections.clear();
      state.dispatch({ type: "RESET" }); onReset(); publish();
    };
    const error = (message, decision = false) => {
      state.dispatch({ type: decision ? "DECISION_ERROR" : "REQUEST_ERROR", error: message });
      publish(message, "error"); onDirty();
    };
    const receiveProposal = (payload, original, originalNegative = "") => {
      if (typeof payload?.prompt !== "string" || !payload.prompt.trim() || typeof payload.correlation_id !== "string" || !payload.correlation_id) throw new Error("Не удалось подтвердить вариант ассистента.");
      const input = capture();
      const result = { ...payload, references: (Array.isArray(payload.references) ? payload.references : []).map(reference => ({
        ...reference, source_value: sourceFor(input, reference.id)?.value || "",
      })) };
      const usesLTX = input.workflow === "aihub-minimax-h3" && input.aiHub?.mode !== "carousel" && input.aiHub?.audioSource === "ltx";
      result.audio_prompt = usesLTX && typeof result.audio_prompt === "string" ? result.audio_prompt : "";
      result.negative_prompt = supportsNegative(input) && Number(input.cfg) > 1
        ? typeof result.negative_prompt === "string" ? result.negative_prompt : originalNegative : "";
      state.dispatch({ type: "REQUEST_SUCCESS", suggestion: result.prompt, negativeSuggestion: result.negative_prompt, audioSuggestion: result.audio_prompt, correlationID: result.correlation_id, references: result.references, usage: result.usage || {} });
      onProposal(result, original);
      publish(`Вариант подготовлен: ${result.model || "локальная модель"}. Подтвердите или отредактируйте его.`, "review"); onDirty();
    };
    const receiveJob = job => {
      onProgress(job);
      if (!jobs.terminal(job.state)) return;
      if (job.state === "completed" && job.result?.prompt) {
        const current = state.get(), input = capture();
        if (current.correlationID === job.id) return;
        if (job.result.correlation_id !== job.id) { error("Ответ относится к другому запросу ассистента."); return; }
        if (job.context !== contextKey(input, true) || job.original !== input.prompt.trim() || (supportsNegative(input) && (job.original_negative || "") !== (Number(input.cfg) > 1 ? input.negative.trim() : "")) || current.stale || current.draftEdited || current.approved) {
          onSavedResponse(job.result);
          if (!current.approved) invalidate();
          publish("Ответ сохранён отдельно: текущий текст или материалы уже изменились.", "stale");
          return;
        }
        context = contextKey(input);
        state.dispatch({ type: "REQUEST_START", original: job.original, originalNegative: job.original_negative, originalAudio: job.original_audio });
        try { receiveProposal(job.result, job.original, job.original_negative); } catch (failure) { error(failure.message); }
      } else if (!state.get().approved) {
        const message = job.status || "Не удалось подготовить вариант. Повторите запрос.";
        state.dispatch({ type: "REQUEST_ERROR", error: message });
        publish(message, job.state === "cancelled" ? "cancelled" : "error");
      }
      onDirty();
    };
    controller = jobs.createController({ transport, persist: persistence.persist, onJob: receiveJob,
      onState: job => {
        if (job.job) onProgress(job.job);
        const countdown = job.retrySeconds > 0 ? ` Следующая проверка через ${job.retrySeconds} сек.` : "";
        const message = job.message || "Ожидаем ответа.";
        publish(job.pending ? message + (countdown && !/[.!?]$/.test(message) ? "." : "") + countdown : "", job.pending ? "loading" : "");
      },
    });
    const improve = async () => {
      if (!ready || snapshot().busy || deciding) return false;
      const input = capture(), original = input.prompt.trim(), originalNegative = supportsNegative(input) && Number(input.cfg) > 1 ? input.negative.trim() : "";
      if ((!original && !(input.mode === "minimax-h3-video" && input.images.length)) || !["text-to-image", "image-to-image", "minimax-h3-video"].includes(input.mode)) {
        publish(input.mode === "minimax-h3-video" ? "Введите промт или добавьте хотя бы одно фото, которое ассистент сможет разобрать." : "Сначала выберите схему генерации и введите позитивный промт.", "error");
        return false;
      }
      const stamp = ++serial, expected = contextKey(input);
      context = expected; preparing = true; active = true;
      state.dispatch({ type: "REQUEST_START", original, originalNegative, originalAudio: input.aiHub?.audioPrompt || "" }); onRequest();
      publish(input.think ? "Локальная модель обдумывает и дорабатывает промт..." : "Локальная модель дорабатывает промт...", "loading");
      try {
        await prepare();
        if (!active || stamp !== serial) return false;
        if (expected !== contextKey() || original !== capture().prompt.trim()) { invalidate(); return false; }
        const body = buildRequest(capture(), correctionValues());
        if (durable) {
          body.set("context", contextKey(capture(), true));
          await controller.start(newRequestID(), body);
          return true;
        }
        legacyFlight = new AbortController();
        const response = await transport("legacy", body, legacyFlight.signal);
        if (!active || stamp !== serial) return false;
        if (expected !== contextKey() || original !== capture().prompt.trim() || state.get().stale || state.get().draftEdited) { invalidate(); return false; }
        if (!response.ok) throw new Error(response.payload?.error || "Не удалось подготовить вариант");
        receiveProposal(response.payload, original, originalNegative);
        return true;
      } catch (failure) {
        if (active && stamp === serial) error(failure.message || "Не удалось подготовить вариант");
        return false;
      } finally { preparing = false; legacyFlight = null; publish(); }
    };
    const persistDecision = (decision, prompt, correlationID, negative = "") => {
      if (!correlationID) return Promise.reject(new Error("Не найден идентификатор варианта ассистента"));
      deciding++; publish();
      // Serialize decisions for one page so an older save cannot finish last.
      const task = decisionTail.catch(() => {}).then(async () => {
        const response = await transport("decision", new URLSearchParams({ correlation_id: correlationID, decision, final_prompt: prompt || "", final_negative_prompt: negative || "" }));
        if (!response.ok) throw new Error(response.payload?.error || "Не удалось сохранить выбор");
        const saved = response.payload?.decision || decision;
        if (!decisions.has(saved) || (decision === "kept_original") !== (saved === "kept_original")) throw new Error("Не удалось подтвердить сохранённый выбор.");
        return saved;
      });
      decisionTail = task;
      return task.finally(() => { deciding--; publish(); });
    };
    const matches = (stamp, correlationID) => active && stamp === serial && state.get().correlationID === correlationID;
    const apply = async () => {
      if (deciding) return false;
      if (!snapshot().canApply) { invalidate(); return false; }
      const input = capture(), current = state.get(), stamp = serial;
      const suggestion = input.draft.trim(), negative = supportsNegative(input) && Number(input.cfg) > 1 ? input.negativeDraft.trim() : "";
      const requested = suggestion === current.suggestion && negative === current.negativeSuggestion ? "applied" : "edited_after_apply";
      try {
        const saved = await persistDecision(requested, suggestion, current.correlationID, negative);
        if (!matches(stamp, current.correlationID)) return false;
        if (state.get().stale || context !== contextKey() || capture().prompt !== input.prompt || capture().draft.trim() !== suggestion || capture().negativeDraft.trim() !== input.negativeDraft.trim()) { invalidate(); return false; }
        const usesLTX = input.workflow === "aihub-minimax-h3" && input.aiHub?.mode !== "carousel" && input.aiHub?.audioSource === "ltx";
        onApplied(suggestion, usesLTX ? current.audioSuggestion : "", negative);
        context = contextKey();
        state.dispatch({ type: "APPLY", edited: saved === "edited_after_apply" });
        publish(saved === "edited_after_apply" ? "Ваш отредактированный вариант применён." : "Вариант ассистента применён.", "approved");
        onDirty(); return true;
      } catch (failure) { if (matches(stamp, current.correlationID)) error(failure.message, true); return false; }
    };
    const keep = async () => {
      if (deciding) return false;
      const current = state.get(), input = capture(), stamp = serial;
      try {
        const retainApplied = !current.stale && ["applied", "edited_after_apply"].includes(current.action);
        if (!current.stale && current.correlationID) {
          await persistDecision(retainApplied ? current.action : "kept_original", input.prompt.trim(), current.correlationID, retainApplied ? input.negative || "" : current.originalNegative);
          if (!matches(stamp, current.correlationID)) return false;
          if (context !== contextKey() || capture().prompt !== input.prompt) { invalidate(); return false; }
        }
        if (current.stale || !current.correlationID) reset(); else serial++;
        if (!retainApplied) state.dispatch({ type: "KEEP_ORIGINAL" });
        context = contextKey(); onKept();
        publish("Оставлен ваш исходный промт. Генерацию можно запускать.", "approved"); onDirty(); return true;
      } catch (failure) { if (matches(stamp, current.correlationID)) error(failure.message, true); return false; }
    };
    const restoreOriginal = () => {
      if (!snapshot().canRestoreOriginal) return Promise.resolve(false);
      const current = state.get(), input = capture(), stamp = serial, expected = contextKey(input);
      restoringOriginal = true;
      originalRestore = (async () => {
        try {
          // A blur-triggered edit save may already be queued. Restore after it,
          // rather than disabling the clicked button before its click can fire.
          await persistDecision("kept_original", current.original, current.correlationID, current.originalNegative);
          if (!matches(stamp, current.correlationID)) return false;
          if (expected !== contextKey() || capture().prompt !== input.prompt) { invalidate(); return false; }
          onOriginalRestored(current.original, current.originalNegative, current.originalAudio);
          state.dispatch({ type: "KEEP_ORIGINAL" });
          context = contextKey();
          publish("Исходный текст возвращён. Его можно отредактировать.", "restored");
          onDirty(); return true;
        } catch (failure) { if (matches(stamp, current.correlationID)) error(failure.message, true); return false; }
        finally { restoringOriginal = false; publish(); }
      })();
      return originalRestore;
    };
    const editPrompt = () => {
      const current = state.get();
      if (capture().enabled && !current.approved && current.status !== "idle") { invalidate(); return; }
      if (["applied", "edited_after_apply"].includes(current.action)) {
        state.dispatch({ type: "PROMPT_EDITED" });
        publish("Применённый вариант изменён вами. Генерацию можно запускать.", "approved");
      }
    };
    const savePromptEdit = async () => {
      const current = state.get(), input = capture(), stamp = serial;
      if (current.action !== "edited_after_apply" || !current.correlationID) return;
      try { await persistDecision("edited_after_apply", input.prompt.trim(), current.correlationID, supportsNegative(input) ? input.negative.trim() : ""); }
      catch (failure) { if (matches(stamp, current.correlationID) && capture().prompt === input.prompt) error(failure.message, true); }
    };
    const beforeLaunch = async () => {
      if (!ready) { publish("Не удалось восстановить состояние ассистента. Обновите страницу и повторите проверку.", "error"); return false; }
      await originalRestore;
      await decisionTail.catch(() => {});
      const current = state.get(), input = capture(), stamp = serial, expected = contextKey(input);
      if (input.enabled && !current.approved) {
        publish("Перед генерацией подтвердите вариант ассистента или выберите «Оставить мой промт».", "error"); return false;
      }
      if (!current.correlationID || !current.action) return true;
      try {
        await persistDecision(current.action, current.action === "kept_original" ? current.original : input.prompt.trim(), current.correlationID, supportsNegative(input) ? current.action === "kept_original" ? current.originalNegative : input.negative.trim() : "");
        if (!matches(stamp, current.correlationID) || expected !== contextKey() || input.prompt !== capture().prompt || capture().enabled && !state.get().approved) {
          invalidate(); return false;
        }
        return true;
      } catch (failure) { if (matches(stamp, current.correlationID)) error(failure.message, true); return false; }
    };
    const restore = values => {
      restoredJobID = values.assistant_job_id || "";
      if ((values.assistant_original_prompt || values.assistant_suggestion || restoredJobID) && capture().enabled) {
        let references = [];
        try { references = JSON.parse(values.assistant_references || "[]"); } catch (_) {}
        state.dispatch({ type: "REQUEST_START", original: values.assistant_original_prompt, originalNegative: values.assistant_original_negative, originalAudio: values.assistant_original_audio });
        state.dispatch({ type: "REQUEST_SUCCESS", suggestion: values.assistant_suggestion, negativeSuggestion: values.assistant_negative_suggestion, audioSuggestion: values.assistant_audio_suggestion, correlationID: values.correlation_id, references });
        if (["applied", "edited_after_apply"].includes(values.assistant_action)) state.dispatch({ type: "APPLY", edited: values.assistant_action === "edited_after_apply" });
        else if (values.assistant_action === "kept_original") state.dispatch({ type: "KEEP_ORIGINAL" });
        const draft = values.assistant_draft || values.assistant_suggestion || "";
        if (values.assistant_draft_edited === "true" || values.assistant_draft_edited === undefined && draft !== values.assistant_suggestion) state.dispatch({ type: "DRAFT_EDITED" });
        try { for (const [id, value] of Object.entries(JSON.parse(values.assistant_corrections || "{}"))) {
          if (value && typeof value.text === "string" && typeof value.source === "string") corrections.set(id, { text: value.text, source: value.source });
        } } catch (_) {}
        if (values.assistant_stale === "true") state.dispatch({ type: "INVALIDATE" });
        onRestore(state.get(), draft, values.assistant_negative_draft ?? values.assistant_negative_suggestion ?? "");
        publish(state.get().stale ? staleMessage : state.get().approved ? "Подтверждённый вариант восстановлен из черновика." : "Вариант восстановлен. Проверьте его и примените либо оставьте свой промт.", state.get().stale ? "stale" : state.get().approved ? "approved" : "review");
      }
      context = contextKey(); publish();
    };
    const initialize = ({ allowResume = true } = {}) => {
      active = true;
      try {
        const saved = allowResume ? persistence.read() : null;
        ready = true;
        const candidate = saved || allowResume && jobs.validID(restoredJobID) && { id: restoredJobID };
        if (candidate) controller.resume(candidate);
      } catch (_) { ready = false; publish(storageMessage, "error"); }
      publish();
    };
    const suspend = () => {
      active = false; serial++;
      if (preparing || legacyFlight) legacyInterrupted = true;
      controller.suspend(); legacyFlight?.abort();
    };
    const resume = () => {
      active = true; controller.resume();
      if (legacyInterrupted && !controller.snapshot().pending) {
        legacyInterrupted = false;
        error("Ответ не получен. Проверьте текущий текст перед новым запросом.");
      }
      publish();
    };
    return { snapshot, improve, apply, keep, restoreOriginal, editPrompt, savePromptEdit, beforeLaunch, restore, reset, invalidate, syncContext,
      initialize, suspend, resume, cancel: () => controller.cancel(), corrections: correctionValues,
      correction: id => corrections.has(id) ? { ...corrections.get(id) } : null,
      setCorrection: (id, text, source) => { corrections.set(id, { text, source }); invalidate(); onDirty(); },
      editDraft: () => { state.dispatch({ type: "DRAFT_EDITED" }); publish(); },
    };
  };

  return { createState, reduce, diff, promptCharacterLimit, supportsNegative, buildRequest, createTransport, createPersistence, createSession };
});
