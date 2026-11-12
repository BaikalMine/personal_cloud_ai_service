(function bootstrapGenerationMedia(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (!root) return;
  root.AIGatewayGeneration = root.AIGatewayGeneration || {};
  root.AIGatewayGeneration.media = api;
})(typeof window !== "undefined" ? window : null, function generationMediaFactory() {
  const SOURCE_DEVICE = "device";
  const SOURCE_GALLERY = "gallery";
  const SOURCE_RESTORED = "restored";
  const referenceRoles = new Set([
    "first_frame", "last_frame", "base_scene", "identity", "wardrobe_object",
    "pose_composition", "style", "background", "details",
  ]);

  const deviceSource = (file) => file ? {
    kind: SOURCE_DEVICE,
    file,
    name: String(file.name || "image"),
    size: Number(file.size) || 0,
    type: String(file.type || ""),
  } : null;

  const gallerySource = (entry) => entry ? {
    kind: SOURCE_GALLERY,
    entry,
    id: String(entry.id || ""),
    name: String(entry.filename || entry.name || "gallery-image"),
    url: String(entry.url || ""),
  } : null;

  const normalizeSource = (source) => {
    if (!source) return null;
    if ([SOURCE_DEVICE, SOURCE_GALLERY, SOURCE_RESTORED].includes(source.kind)) return source;
    if (source.id && (source.url || source.filename)) return gallerySource(source);
    return deviceSource(source);
  };

  const sourceFrom = (device, gallery) => deviceSource(device) || gallerySource(gallery);

  const referenceRole = ({ templateID = "", videoMode = "frames", slot = 1, role = "" } = {}) => {
    const index = Math.max(1, Math.min(9, Math.trunc(Number(slot)) || 1));
    if (templateID === "minimax-h3-video" && videoMode !== "references") {
      return index === 1 ? "first_frame" : "last_frame";
    }
    if (templateID === "image-to-image" && index === 1) return "base_scene";
    return referenceRoles.has(role) ? role : index === 1 ? "base_scene" : "details";
  };

  const referenceMetadata = ({ templateID = "", videoMode = "frames", slot = 1, source = null, role = "", uploaded = "" } = {}) => {
    if (!source && !uploaded) return null;
    const index = Math.max(1, Math.min(9, Math.trunc(Number(slot)) || 1));
    const normalized = normalizeSource(source);
    const sourceKind = normalized?.kind || (uploaded ? SOURCE_RESTORED : "unknown");
    return {
      number: index,
      role: referenceRole({ templateID, videoMode, slot: index, role }),
      source: sourceKind,
      sourceID: normalized?.kind === SOURCE_GALLERY ? String(normalized.id || "") : "",
      sourceName: String(normalized?.name || uploaded || "").trim(),
    };
  };

  const createState = (overrides = {}) => ({
    sources: {},
    uploaded: {},
    uploading: false,
    error: "",
    ...overrides,
  });

  const reduce = (state = createState(), action = {}) => {
    const slot = String(action.slot || "");
    switch (action.type) {
      case "SELECT_SOURCE":
        return { ...state, sources: { ...state.sources, [slot]: normalizeSource(action.source) }, error: "" };
      case "CLEAR_SOURCE": {
        const sources = { ...state.sources };
        const uploaded = { ...state.uploaded };
        delete sources[slot];
        delete uploaded[slot];
        return { ...state, sources, uploaded, error: "" };
      }
      case "UPLOAD_START":
        return { ...state, uploading: true, error: "" };
      case "UPLOAD_SUCCESS":
        return { ...state, uploaded: { ...state.uploaded, [slot]: String(action.value || "") }, error: "" };
      case "UPLOAD_FINISH":
        return { ...state, uploading: false };
      case "UPLOAD_ERROR":
        return { ...state, uploading: false, error: String(action.error || "Upload failed") };
      default:
        return state;
    }
  };

  /** Upload a device or gallery source; AV keeps the server's multipart `image` field. */
  const uploadSource = async (source, options = {}) => {
    const normalized = normalizeSource(source);
    const kind = options.kind || "image";
    if (!["image", "audio", "video"].includes(kind)) throw new Error("Unsupported media kind");
    if (!normalized) throw new Error("Media source is required");
    if (normalized.kind === SOURCE_RESTORED) throw new Error("Restored media cannot be uploaded");
    const fetcher = options.fetcher || (typeof fetch === "function" ? fetch : null);
    if (!fetcher) throw new Error("Fetch is unavailable");

    let response;
    if (normalized.kind === SOURCE_GALLERY) {
      if (kind !== "image") throw new Error("Gallery reuse is only available for images");
      if (!normalized.id) throw new Error("Gallery media id is required");
      const makeSearchParams = options.searchParamsFactory || ((values) => new URLSearchParams(values));
      const body = makeSearchParams({ csrf: String(options.csrf || ""), media_id: normalized.id });
      response = await fetcher(options.galleryURL || "/generate/library/reuse-image", {
        method: "POST",
        headers: { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8" },
        body,
        credentials: "same-origin",
      });
    } else {
      if (!normalized.file) throw new Error("Device file is required");
      const makeFormData = options.formDataFactory || (() => new FormData());
      const body = makeFormData();
      body.append("image", normalized.file, normalized.name);
      body.append("type", "input");
      body.append("overwrite", "true");
      response = await fetcher(options.deviceURL || `/generate/upload/${kind}`, {
        method: "POST",
        headers: Number.isSafeInteger(normalized.file.size) && normalized.file.size > 0
          ? { "X-Upload-File-Size": String(normalized.file.size) } : {},
        body,
        credentials: "same-origin",
      });
    }

    const payload = await response.json().catch(() => ({}));
    if (!response.ok && [502, 503, 504].includes(response.status) && !payload?.error) {
      throw new Error("ComfyUI временно недоступен. Повторите загрузку после восстановления сервиса.");
    }
    if (!response.ok || !payload?.name) throw new Error(payload?.error || options.errorMessage || "Media upload failed");
    return {
      ...payload,
      value: [payload.subfolder, payload.name].filter(Boolean).join("/"),
      source: normalized,
    };
  };

  const uploadImageSource = (source, options = {}) => uploadSource(source, { ...options, kind: "image" });

  /**
   * @typedef {Object} UploadRequest
   * @property {string} field Stable draft field name, independent of source identity.
   * @property {() => Object|null|undefined} readSource Returns the same object while selection is unchanged.
   * @property {(source: Object) => Promise<Object>} transfer Uploads the captured source.
   * @property {(payload: Object) => *} commit Synchronously publishes a current result and returns its public value.
   * @property {string} [changedMessage]
   */
  /**
   * @typedef {Object} PrepareRequest
   * @property {() => string} selection Stable key for the current workflow, model and mode.
   * @property {() => void} validate Throws for missing or unavailable required sources.
   * @property {() => Array<{upload: () => Promise<*>}>} pending Fresh list, excluding committed uploads.
   * @property {(entry: Object, state: string, error?: Error) => void} [progress]
   * @property {() => void} [ready]
   * @property {string} [changedMessage]
   */
  /**
   * Owns in-flight uploads shared by autosave and submit, without owning the DOM.
   * upload({field, readSource, transfer, commit, changedMessage}) captures a source
   * by identity; only the latest request for that field may commit its result.
   * prepare({selection, validate, pending, progress, ready, changedMessage})
   * serially uploads pending entries and rechecks the selection before ready.
   * transfer and entry.upload return promises. Other callbacks are synchronous.
   * @param {{settled?: () => void}} options
   * @returns {{upload: (request: UploadRequest) => Promise<*>, prepare: (request: PrepareRequest) => Promise<void>, readonly pendingCount: number}}
   */
  const createUploadController = ({ settled = () => {} } = {}) => {
    const latest = new Map();
    const active = new Set();
    let preparing = null;
    const upload = ({ field, readSource, transfer, commit, changedMessage = "Media source changed" }) => {
      const source = readSource();
      if (!source) return Promise.reject(new Error("Файл не выбран"));
      const existing = latest.get(field);
      if (existing?.source === source) return existing.promise;
      const entry = { source, promise: null };
      const assertCurrent = () => {
        if (readSource() !== source || latest.get(field) !== entry) throw new Error(changedMessage);
      };
      entry.promise = Promise.resolve().then(() => {
        assertCurrent();
        return transfer(source);
      }).then((payload) => {
        assertCurrent();
        return commit(payload);
      }).finally(() => {
        if (latest.get(field) === entry) latest.delete(field);
        active.delete(entry);
        settled();
      });
      latest.set(field, entry);
      active.add(entry);
      return entry.promise;
    };
    const prepare = ({ selection, validate, pending, progress = () => {}, ready = () => {}, changedMessage = "Media selection changed" }) => {
      if (preparing) return preparing;
      const expected = selection();
      preparing = Promise.resolve().then(async () => {
        validate();
        for (const entry of pending()) {
          progress(entry, "loading");
          try { await entry.upload(); }
          catch (error) { progress(entry, "error", error); throw error; }
        }
        if (selection() !== expected || pending().length) throw new Error(changedMessage);
        validate();
        ready();
      }).finally(() => { preparing = null; settled(); });
      return preparing;
    };
    return { upload, prepare, get pendingCount() { return active.size; } };
  };

  return {
    SOURCE_DEVICE,
    SOURCE_GALLERY,
    SOURCE_RESTORED,
    createState,
    reduce,
    deviceSource,
    gallerySource,
    normalizeSource,
    sourceFrom,
    referenceRole,
    referenceMetadata,
    uploadImageSource,
    uploadSource,
    createUploadController,
  };
});
