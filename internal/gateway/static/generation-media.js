(function bootstrapGenerationMedia(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (!root) return;
  root.AIGatewayGeneration = root.AIGatewayGeneration || {};
  root.AIGatewayGeneration.media = api;
})(typeof window !== "undefined" ? window : null, function generationMediaFactory() {
  const SOURCE_DEVICE = "device";
  const SOURCE_GALLERY = "gallery";

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
    if (source.kind === SOURCE_DEVICE || source.kind === SOURCE_GALLERY) return source;
    if (source.id && (source.url || source.filename)) return gallerySource(source);
    return deviceSource(source);
  };

  const sourceFrom = (device, gallery) => deviceSource(device) || gallerySource(gallery);

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

  const uploadImageSource = async (source, options = {}) => {
    const normalized = normalizeSource(source);
    if (!normalized) throw new Error("Image source is required");
    const fetcher = options.fetcher || (typeof fetch === "function" ? fetch : null);
    if (!fetcher) throw new Error("Fetch is unavailable");

    let response;
    if (normalized.kind === SOURCE_GALLERY) {
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
      const makeFormData = options.formDataFactory || (() => new FormData());
      const body = makeFormData();
      body.append("image", normalized.file, normalized.name);
      body.append("type", "input");
      body.append("overwrite", "true");
      response = await fetcher(options.deviceURL || "/generate/upload/image", {
        method: "POST",
        body,
        credentials: "same-origin",
      });
    }

    const payload = await response.json().catch(() => ({}));
    if (!response.ok || !payload.name) throw new Error(payload.error || "Image upload failed");
    return {
      ...payload,
      value: [payload.subfolder, payload.name].filter(Boolean).join("/"),
      source: normalized,
    };
  };

  return {
    SOURCE_DEVICE,
    SOURCE_GALLERY,
    createState,
    reduce,
    deviceSource,
    gallerySource,
    normalizeSource,
    sourceFrom,
    uploadImageSource,
  };
});
