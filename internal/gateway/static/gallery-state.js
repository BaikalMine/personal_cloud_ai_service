(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (root) root.AIGatewayGallery = api;
})(typeof window === "undefined" ? null : window, function () {
  "use strict";
  const validID = value => typeof value === "string" && /^[a-zA-Z0-9_-]{16,96}$/.test(value);
  const validMediaID = value => typeof value === "string" && /^[1-9][0-9]{0,18}$/.test(value);
  const clamp = (value, min, max) => Math.min(max, Math.max(min, value));
  const fitScale = (width, height, viewportWidth, viewportHeight) => {
    if (![width, height, viewportWidth, viewportHeight].every(value => Number.isFinite(value) && value > 0)) return 1;
    return Math.min(viewportWidth / width, viewportHeight / height, 1);
  };
  const normalizedFilters = value => ({
    search: String(value?.search || "").slice(0, 300),
    type: ["all", "image", "video"].includes(value?.type) ? value.type : "all",
    collection: /^[1-9][0-9]{0,18}$/.test(String(value?.collection || "")) ? String(value.collection) : "",
    from: /^\d{4}-\d{2}-\d{2}$/.test(value?.from || "") ? value.from : "",
    to: /^\d{4}-\d{2}-\d{2}$/.test(value?.to || "") ? value.to : "",
    flags: Array.isArray(value?.flags) ? [...new Set(value.flags.filter(flag => ["favorite", "pinned", "sensitive", "error"].includes(flag)))] : [],
  });
  const safeURL = (value, origin) => {
    try {
      const url = new URL(value, origin);
      return url.origin === origin && ["http:", "https:"].includes(url.protocol) && !url.username && !url.password ? url.pathname + url.search + url.hash : "";
    } catch (_) { return ""; }
  };
  const sourceKey = source => `${source?.jobID || ""}:${source?.mediaID || ""}`;
  const expired = (source, now) => Number(source?.expiresAt) > 0 && Number(source.expiresAt) <= now;
  const validMeasurement = (value, source) => value?.version === 1 && value.source === "completed_file_stream_metadata"
    && value.media_type === source.type && /^[a-f0-9]{64}$/.test(value.sha256 || "")
    && Number.isInteger(value.width) && value.width > 0 && Number.isInteger(value.height) && value.height > 0
    && Number.isSafeInteger(value.size_bytes) && value.size_bytes > 0
    && (!source.sizeBytes || value.size_bytes === source.sizeBytes) && Number.isFinite(Date.parse(value.measured_at));

  /** One selected output, one abortable read chain. No mutation or storage API. */
  function createInspectorController({ readPassport, readMeasurement, onState = () => {}, now = Date.now }) {
    let epoch = 0, abort = null, visible = true;
    let state = { phase: "idle", source: null, passport: null, raw: "", measurement: null, measurementPhase: "idle", error: "" };
    const publish = patch => { state = { ...state, ...patch }; onState(state); };
    const cancel = () => { epoch++; abort?.abort(); abort = null; };
    const current = (token, source) => token === epoch && visible && sourceKey(source) === sourceKey(state.source);
    const unavailable = () => publish({ phase: "unavailable", passport: null, raw: "", measurement: null, measurementPhase: "idle", error: "" });
    async function measure() {
      const source = state.source;
      if (!visible || !source || state.phase !== "ready" || !validID(source.jobID) || !validMediaID(source.mediaID) || state.measurementPhase === "loading") return;
      const token = epoch;
      publish({ measurementPhase: "loading", measurementError: "" });
      try {
        const result = await readMeasurement(source, abort?.signal);
        if (!current(token, source)) return;
        if (expired(source, now())) { unavailable(); return; }
        if (result?.job_id !== source.jobID || result?.media_id !== source.mediaID || result?.measurement_state !== "measured" || !validMeasurement(result.measurement, source)) throw new Error("invalid_measurement_response");
        publish({ measurement: result.measurement, measurementPhase: "ready" });
      } catch (error) {
        if (!current(token, source)) return;
        if ([401, 403, 404].includes(error.status)) { unavailable(); return; }
        publish({ measurementPhase: "error", measurementError: error.message || "measurement_unavailable", measurement: null });
      }
    }
    async function load() {
      const source = state.source;
      cancel();
      if (!source || !visible) return;
      if (expired(source, now())) { unavailable(); return; }
      if (!validID(source.jobID)) { publish({ phase: "legacy", error: "" }); return; }
      const token = epoch;
      abort = new AbortController();
      publish({ phase: "loading", error: "", passport: null, raw: "", measurement: null, measurementPhase: "idle" });
      try {
        const result = await readPassport(source, abort.signal);
        if (!current(token, source)) return;
        if (expired(source, now())) { unavailable(); return; }
        const data = result?.data;
        const postprocess = source.operation === "postprocess";
        const typed = postprocess
          ? data?.operation === "postprocess" && data.seed === undefined && data.postprocess?.version === 1 && data.postprocess.plan?.version === 1
          : data?.operation !== "postprocess" && typeof data?.seed === "string";
        if (data?.version !== 1 || data.job_id !== source.jobID || !typed) throw new Error("invalid_passport_response");
        const output = Array.isArray(data.outputs) ? data.outputs.find(item => item.media_id === source.mediaID) : null;
        if (Array.isArray(data.outputs) && !output && !data.outputs_truncated) { unavailable(); return; }
        const cached = output?.measurement_state === "measured" && validMeasurement(output.measurement, source) ? output.measurement : null;
        publish({ phase: "ready", passport: data, raw: result.raw || "", measurement: cached,
          measurementPhase: cached ? "ready" : "idle", error: "" });
        if (validMediaID(source.mediaID) && !state.measurement) void measure();
      } catch (error) {
        if (!current(token, source)) return;
        if ([401, 403, 404].includes(error.status)) { unavailable(); return; }
        publish({ phase: "error", error: error.message || "passport_unavailable", measurementPhase: "idle" });
      }
    }
    function select(source) {
      cancel();
      state = { phase: "idle", source, passport: null, raw: "", measurement: null, measurementPhase: "idle", error: "" };
      return load();
    }
    function setVisible(value) {
      visible = !!value;
      if (!visible) { cancel(); if (state.source) publish({ phase: "paused", measurementPhase: "idle" }); }
      else if (state.source) void load();
    }
    function close() { cancel(); state = { phase: "idle", source: null, passport: null, raw: "", measurement: null, measurementPhase: "idle", error: "" }; onState(state); }
    return { select, retry: load, measure, setVisible, close, snapshot: () => state };
  }
  return { validID, validMediaID, clamp, fitScale, normalizedFilters, safeURL, sourceKey, expired, validMeasurement, createInspectorController };
});
