(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (root) root.AIGatewayLoraCaptions = api;
})(typeof window !== "undefined" ? window : null, function () {
  const active = (job) => job && ["queued", "running"].includes(job.state);
  const matches = (manifest, item, job) => {
    const source = job?.source;
    return Boolean(source && item && !item.excluded && item.id === source.image.id &&
      item.asset_id === source.image.asset_id && item.caption === source.image.caption &&
      (item.caption_revision || "") === (source.image.caption_revision || "") &&
      manifest.settings.trigger_word.trim() === source.trigger_word && manifest.settings.concept_type === source.concept_type);
  };
  const latest = (jobs) => {
    const result = new Map();
    for (const job of jobs) {
      const previous = result.get(job.image_id);
      if (!previous || job.created_at > previous.created_at || (job.created_at === previous.created_at && job.job_id > previous.job_id)) result.set(job.image_id, job);
    }
    return result;
  };
  const reconcile = (manifest, jobs, newID = () => crypto.randomUUID()) => {
    let changed = 0;
    for (const job of latest(jobs).values()) {
      const item = manifest.images.find((image) => image.id === job.image_id);
      if (job.state !== "completed" || !job.caption?.trim() || item?.caption_job_id === job.job_id || !matches(manifest, item, job)) continue;
      item.caption = job.caption.trim(); item.caption_revision = newID(); item.caption_job_id = job.job_id; changed++;
    }
    return changed;
  };
  const createPoller = ({ request, onChange = () => {}, schedule = setTimeout, unschedule = clearTimeout }) => {
    const state = { datasetID: "", jobs: [], ready: false, error: "", retrySeconds: 0 };
    let epoch = 0; let serial = 0; let timer = null; let pending = null; let failures = 0; let disposed = false;
    const stop = () => { if (timer !== null) unschedule(timer); timer = null; pending?.abort(); pending = null; };
    const later = (milliseconds, callback) => { if (timer !== null) unschedule(timer); timer = schedule(() => { timer = null; callback(); }, milliseconds); };
    const countdown = () => {
      state.retrySeconds = Math.max(0, state.retrySeconds - 1); onChange(state);
      if (state.retrySeconds) later(1000, countdown); else void refresh();
    };
    const refresh = async () => {
      stop();
      if (disposed || !state.datasetID) return;
      const capturedEpoch = epoch; const capturedSerial = ++serial; const abort = new AbortController(); pending = abort;
      const current = () => !disposed && capturedEpoch === epoch && capturedSerial === serial;
      try {
        const response = await request(state.datasetID, abort.signal);
        if (!current()) return;
        state.jobs = (response.jobs || []).filter((job) => job.dataset_id === state.datasetID);
        state.ready = true; state.error = ""; state.retrySeconds = 0; failures = 0; onChange(state);
        if (current()) later(state.jobs.some(active) ? 1500 : 15000, () => void refresh());
      } catch (error) {
        if (!current() || abort.signal.aborted) return;
        state.error = error.message || "Не удалось проверить задания.";
        state.retrySeconds = Math.min(30, 3 * 2 ** Math.min(failures++, 4)); onChange(state); later(1000, countdown);
      } finally { if (pending === abort) pending = null; }
    };
    const select = (datasetID, force = false) => {
      datasetID ||= "";
      if (!force && datasetID === state.datasetID) return;
      stop(); epoch++; failures = 0;
      Object.assign(state, { datasetID, jobs: [], ready: !datasetID, error: "", retrySeconds: 0 }); onChange(state);
      if (datasetID) void refresh();
    };
    return { state, select, refresh, resume: () => { disposed = false; select(state.datasetID, true); }, dispose: () => { disposed = true; epoch++; stop(); } };
  };
  return { active, matches, latest, reconcile, createPoller };
});
