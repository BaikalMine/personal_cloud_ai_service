(function (root, factory) {
  "use strict";
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  else Object.assign(root.AIGatewayGeneration = root.AIGatewayGeneration || {}, api);
})(typeof globalThis !== "undefined" ? globalThis : this, function () {
  "use strict";
  const validRepeatID = value => typeof value === "string" && /^[A-Za-z0-9_-]{16,96}$/.test(value);
  const readyPhases = new Set(["ready", "changed", "unverified"]);

  function createRepeatController(deps) {
    const now = deps.now || Date.now;
    const schedule = deps.schedule || setTimeout, unschedule = deps.unschedule || clearTimeout;
    let current = null, pending = null, view = null, receipt = null, error = "", phase = "idle";
    let busy = false, visible = false, epoch = 0, abort = null, timer = null, storageError = false, immediateRequest = null;
    try {
      const saved = deps.storage.load();
      if (saved != null) {
        if (saved.version !== 1 || saved.submitted !== true || !validRepeatID(saved.sourceID) || !validRepeatID(saved.requestID) || saved.sourceID === saved.requestID) throw new Error("invalid saved request");
        pending = { sourceID: saved.sourceID, requestID: saved.requestID };
        current = { ...pending }; phase = "uncertain";
      }
    } catch (_) { storageError = true; phase = "storage_error"; }

    function snapshot() {
      return { phase, ...current, view, receipt, error, busy, pending: !!pending,
        canSubmit: !busy && !storageError && readyPhases.has(phase) && !!view?.confirmation && view.expires_at > now() };
    }
    function emit() { deps.onState?.(snapshot()); }
    function clearTimer() { if (timer !== null) unschedule(timer); timer = null; }
    function invalidateCheck() {
      clearTimer();
      if (abort) { abort.abort(); abort = null; ++epoch; busy = false; }
    }
    function arm() {
      clearTimer();
      if (!visible || busy) return;
      if (phase === "pending") timer = schedule(() => { timer = null; check(); }, 2000);
      else if (readyPhases.has(phase)) timer = schedule(() => {
        timer = null; view = null; phase = "expired"; emit();
      }, Math.max(1, view.expires_at - now()));
    }
    function settleReceipt(data) {
      if (data.source_id !== current?.sourceID || data.request_id !== current?.requestID || (!data.cancelled && (!data.accepted || !validRepeatID(data.job_id))) || (data.cancelled && data.accepted)) throw new Error("Ответ не соответствует запросу повтора.");
      receipt = data; view = null; pending = null; error = "";
      phase = data.cancelled ? "cancelled" : "accepted";
      try { deps.storage.clear(); } catch (_) {}
    }
    function fail(err, fallback) { error = err?.message || fallback; phase = pending ? "uncertain" : "error"; }

    async function check() {
      if (busy || !current || storageError) return;
      invalidateCheck(); const version = ++epoch;
      abort = new AbortController(); const signal = abort.signal;
      view = null; receipt = null; error = ""; phase = "checking"; busy = true; emit();
      try {
        const data = await deps.check({ ...current }, signal);
        if (version !== epoch) return;
        if (data.accepted || data.cancelled) settleReceipt(data);
        else {
          if (data.source_id !== current.sourceID || !["pending", "blocked", ...readyPhases].includes(data.state) || (readyPhases.has(data.state) && (typeof data.confirmation !== "string" || !data.confirmation || !(data.expires_at > now()) || typeof data.seed !== "string"))) throw new Error("Подтверждение повтора недоступно.");
          view = data; phase = data.state;
        }
      } catch (err) { if (version === epoch) fail(err, "Проверка недоступна."); }
      finally {
        if (version === epoch) {
          abort = null; busy = false; arm(); emit();
          const unchanged = phase === "ready" || phase === "unverified" && !(view?.differences || []).length;
          const launch = visible && immediateRequest === current?.requestID && unchanged && snapshot().canSubmit;
          if (phase !== "pending") immediateRequest = null;
          if (launch) await submit();
        }
      }
    }
    async function recover() {
      if (busy || !pending || storageError) return;
      invalidateCheck(); current = { ...pending }; view = null; error = ""; phase = "recovering"; busy = true; emit();
      try { settleReceipt(await deps.recover({ ...current })); }
      catch (err) {
        fail(err, "Ответ о запуске не подтверждён.");
        if (err?.status === 404) error = "Запрос пока не найден. Его отправка ещё может завершиться.";
      } finally { busy = false; emit(); }
    }
    async function submit() {
      if (!snapshot().canSubmit) return;
      clearTimer(); const confirmation = view.confirmation;
      try {
        const saved = { version: 1, ...current, submitted: true };
        deps.storage.save(saved);
        const readback = deps.storage.load();
        if (JSON.stringify(readback) !== JSON.stringify(saved)) throw new Error("storage readback failed");
        pending = { ...current };
      } catch (_) {
        phase = "storage_error"; storageError = true; view = null;
        error = "Браузер не сохранил запрос. Повтор не отправлен."; emit(); return;
      }
      busy = true; phase = "submitting"; error = ""; view = null; emit();
      try { settleReceipt(await deps.submit({ ...current, confirmation })); }
      catch (err) { fail(err, "Ответ о запуске не подтверждён."); }
      finally { busy = false; emit(); }
    }
    async function discard() {
      if (busy || !pending) return;
      clearTimer(); current = { ...pending }; view = null; busy = true; phase = "discarding"; error = ""; emit();
      try { settleReceipt(await deps.discard({ ...current })); }
      catch (err) { fail(err, "Отказ от запроса не подтверждён."); }
      finally { busy = false; emit(); }
    }
    async function open(sourceID, { immediate = false } = {}) {
      visible = true;
      if (busy && !abort) { emit(); return; }
      invalidateCheck();
      immediateRequest = null;
      if (storageError) { emit(); return; }
      if (pending) { await recover(); return; }
      if (!validRepeatID(sourceID)) return;
      const requestID = deps.newID();
      if (!validRepeatID(requestID) || sourceID === requestID) throw new Error("Invalid repeat request ID");
      current = { sourceID, requestID }; receipt = null; view = null;
      if (immediate) immediateRequest = requestID;
      await check();
    }
    function setVisible(value) {
      visible = !!value;
      if (!visible) {
        immediateRequest = null;
        const checking = !!abort;
        invalidateCheck();
        if (checking) phase = pending ? "uncertain" : "paused";
      } else if (readyPhases.has(phase) && view.expires_at <= now()) { view = null; phase = "expired"; }
      arm(); emit();
    }
    return { snapshot, open, check, submit, recover, discard, setVisible };
  }
  return { createRepeatController, validRepeatID };
});
