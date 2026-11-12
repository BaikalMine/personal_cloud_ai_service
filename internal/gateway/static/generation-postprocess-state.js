(function (root, factory) {
  "use strict";
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  else Object.assign(root.AIGatewayGeneration = root.AIGatewayGeneration || {}, api);
})(typeof globalThis !== "undefined" ? globalThis : this, function () {
  "use strict";
  const validID = value => typeof value === "string" && /^[A-Za-z0-9_-]{16,96}$/.test(value);
  const validMediaID = value => typeof value === "string" && /^[1-9][0-9]{0,18}$/.test(value) && BigInt(value) <= 9223372036854775807n;
  const validSource = value => validID(value?.source_job_id) && validMediaID(value?.source_media_id);
  const copy = value => value == null ? value : JSON.parse(JSON.stringify(value));
  const validOptions = o => o && Number.isInteger(o.width) && o.width >= 64 && o.width <= 4096 && Number.isInteger(o.height) && o.height >= 64 && o.height <= 4096
    && Number.isInteger(o.interpolate) && o.interpolate >= 1 && o.interpolate <= 4
    && [undefined, "", "LOW", "MEDIUM", "HIGH", "ULTRA"].includes(o.rtx_quality)
    && (o.interpolate === 1 ? !o.rife_checkpoint : ["rife47.pth", "rife49.pth", "rife417.pth", "rife426.pth"].includes(o.rife_checkpoint))
    && (o.color_method ? ["adain", "mean_std"].includes(o.color_method) && Number.isFinite(o.color_strength) && o.color_strength > 0 && o.color_strength <= 1 : !o.color_strength);
  const validDraft = d => validSource(d) && validOptions(d.options) && (d.options.color_method
    ? validID(d.reference_job_id) && validMediaID(d.reference_media_id) && d.reference_media_id !== d.source_media_id
    : !d.reference_job_id && !d.reference_media_id);

  function createPostprocessController(deps) {
    const now = deps.now || Date.now, schedule = deps.schedule || setTimeout, unschedule = deps.unschedule || clearTimeout;
    let phase = "idle", current = null, pending = null, quote = null, receipt = null, options = null, error = "";
    let busy = false, visible = false, epoch = 0, abort = null, timer = null, storageError = false;
    try {
      const saved = deps.storage.load();
      if (saved != null) {
        if (saved.version !== 1 || !["quote", "confirm"].includes(saved.stage) || !validID(saved.request_id) || !validDraft(saved.draft)) throw Error("invalid saved request");
        pending = copy(saved); current = copy(saved.draft); phase = "uncertain";
      }
    } catch (_) { storageError = true; phase = "storage_error"; }
    function snapshot() {
      return { phase, current: copy(current), pending: !!pending, quote: copy(quote), receipt: copy(receipt), options: copy(options), error, busy,
        canQuote: !busy && !pending && !storageError && phase === "editing" && !!options,
        canConfirm: !busy && !storageError && phase === "quoted" && !!quote && Date.parse(quote.expires_at) > now() };
    }
    function emit() { deps.onState?.(snapshot()); }
    function clearTimer() { if (timer !== null) unschedule(timer); timer = null; }
    function arm() {
      clearTimer();
      if (visible && !busy && phase === "quoted") timer = schedule(() => { timer = null; quote = null; phase = "expired"; emit(); }, Math.max(1, Date.parse(quote.expires_at) - now()));
    }
    function cancelOptions() {
      if (abort) { abort.abort(); abort = null; ++epoch; busy = false; }
    }
    function save(value) {
      deps.storage.save(copy(value));
      if (JSON.stringify(deps.storage.load()) !== JSON.stringify(value)) throw Error("storage readback failed");
      pending = copy(value);
    }
    function storageFailed() {
      storageError = true; phase = "storage_error"; quote = null;
      error = "Браузер не сохранил запрос для восстановления. Отправка остановлена."; emit();
    }
    function settle(data) {
      if (!pending || data?.request_id !== pending.request_id) throw Error("Ответ не соответствует запросу постобработки.");
      if (data.state === "accepted" && data.accepted === true && validID(data.job_id) || data.state === "cancelled" && data.accepted === false) {
        receipt = copy(data); quote = null; phase = data.state; pending = null; error = "";
        try { deps.storage.clear(); } catch (_) {}
        return;
      }
      if (data.state !== "quoted" || data.accepted !== false || !/^[a-f0-9]{64}$/.test(data.confirmation) || !Number.isFinite(Date.parse(data.expires_at))
          || data.cost !== 1 || !validOptions(data.plan?.options) || !Array.isArray(data.sources) || data.sources.length !== (current.options.color_method ? 2 : 1)
          || data.sources[0].job_id !== current.source_job_id || data.sources[0].media_id !== current.source_media_id
          || current.options.color_method && (data.sources[1].job_id !== current.reference_job_id || data.sources[1].media_id !== current.reference_media_id)
          || ["width", "height", "interpolate", "rtx_quality", "rife_checkpoint", "color_method", "color_strength"].some(key => (data.plan.options[key] || "") !== (current.options[key] || ""))) throw Error("Расчёт не соответствует выбранному результату и параметрам.");
      receipt = null; error = "";
      if (Date.parse(data.expires_at) <= now()) { phase = "expired"; quote = null; }
      else { quote = copy(data); phase = "quoted"; }
    }
    function failed(err) {
      quote = null; phase = pending ? "uncertain" : "error"; error = err?.message || "Ответ сервера не подтверждён.";
      if (pending && err?.status === 404) error = "Запрос пока не найден. Его отправка ещё может завершиться.";
      if (pending && err?.status === 410) { phase = "expired"; error = "Срок расчёта истёк. Отмените его перед новым расчётом."; }
    }
    async function loadOptions() {
      if (busy || pending || storageError || !validSource(current)) return;
      cancelOptions(); const version = ++epoch; abort = new AbortController();
      options = null; quote = null; receipt = null; error = ""; busy = true; phase = "loading"; emit();
      try {
        const data = await deps.options(copy(current), abort.signal);
        if (version !== epoch) return;
        if (data?.version !== 1 || data.source?.job_id !== current.source_job_id || data.source?.media_id !== current.source_media_id
            || !validOptions({ width: data.source?.measurement?.width, height: data.source?.measurement?.height, interpolate: 1 })
            || ![data.upscale, data.interpolation, data.color].every(list => Array.isArray(list) && list.every(item => validOptions(item.options)))) throw Error("Доступные операции не подтверждены.");
        options = copy(data); phase = "editing";
      } catch (err) { if (version === epoch) failed(err); }
      finally { if (version === epoch) { abort = null; busy = false; emit(); } }
    }
    async function open(source) {
      visible = true;
      if (busy && !abort) { emit(); return; }
      cancelOptions(); clearTimer();
      if (storageError) { emit(); return; }
      if (pending) { await recover(); return; }
      if (!validSource(source)) return;
      current = { source_job_id: source.source_job_id, source_media_id: source.source_media_id };
      await loadOptions();
    }
    async function calculate(draft) {
      if (!snapshot().canQuote || !validDraft(draft) || draft.source_job_id !== current.source_job_id || draft.source_media_id !== current.source_media_id) return;
      const requestID = deps.newID();
      if (!validID(requestID)) return;
      current = copy(draft);
      try { save({ version: 1, stage: "quote", request_id: requestID, draft: copy(current) }); } catch (_) { storageFailed(); return; }
      busy = true; phase = "quoting"; error = ""; emit();
      try { settle(await deps.quote({ request_id: requestID, ...copy(current) })); }
      catch (err) { failed(err); }
      finally { busy = false; arm(); emit(); }
    }
    async function confirm() {
      if (!snapshot().canConfirm) return;
      const confirmation = quote.confirmation;
      try { save({ ...pending, stage: "confirm" }); } catch (_) { storageFailed(); return; }
      clearTimer(); busy = true; phase = "confirming"; error = ""; quote = null; emit();
      try { settle(await deps.confirm({ request_id: pending.request_id, confirmation })); }
      catch (err) { failed(err); }
      finally { busy = false; arm(); emit(); }
    }
    async function recover() {
      if (busy || !pending || storageError) return;
      clearTimer(); busy = true; phase = "recovering"; quote = null; error = ""; emit();
      try { settle(await deps.status({ request_id: pending.request_id })); }
      catch (err) { failed(err); }
      finally { busy = false; arm(); emit(); }
    }
    async function discard() {
      if (busy || !pending || storageError) return;
      clearTimer(); busy = true; phase = "discarding"; quote = null; error = ""; emit();
      try {
        const data = await deps.discard({ request_id: pending.request_id });
        if (!["accepted", "cancelled"].includes(data?.state)) throw Error("Отмена расчёта не подтверждена.");
        settle(data);
      } catch (err) { failed(err); }
      finally { busy = false; emit(); }
    }
    function setVisible(value) {
      visible = !!value;
      if (!visible) { clearTimer(); if (abort) { cancelOptions(); phase = "paused"; } }
      else if (quote && Date.parse(quote.expires_at) <= now()) { quote = null; phase = "expired"; }
      arm(); emit();
    }
    return { snapshot, open, loadOptions, calculate, confirm, recover, discard, setVisible };
  }
  return { createPostprocessController, validPostprocessMediaID: validMediaID };
});
