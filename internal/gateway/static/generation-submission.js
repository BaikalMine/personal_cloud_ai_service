(function bootstrapGenerationSubmission(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (root) (root.AIGatewayGeneration ||= {}).submission = api;
})(typeof window !== "undefined" ? window : null, function generationSubmissionFactory() {
  /**
   * @typedef {Object} SubmissionRecord
   * @property {string} requestID
   * @property {"single"|"batch"} kind
   * @property {number} savedAt Wall-clock timestamp, never an execution deadline.
   * @property {string} [jobID]
   * @property {string} [batchID]
   * @property {string} [promptID]
   * @property {boolean} [video]
   * @property {boolean} [confirmed] The account-scoped server lookup confirmed the binding.
   * @property {boolean} [cancelling]
   * @property {boolean} [resolvingRejection]
   * @property {boolean} [terminal]
   * @typedef {{ok:boolean, status:number, payload:Object}} SubmissionResponse
   * @typedef {{record:SubmissionRecord|null, payload:Object|null, phase:string, message:string, pending:boolean, live:boolean, retrySeconds:number}} SubmissionSnapshot
   */
  const validID = value => typeof value === "string" && /^[A-Za-z0-9_-]{16,96}$/.test(value);
  const validPromptID = value => typeof value === "string" && /^[A-Za-z0-9_-]{1,128}$/.test(value);
  const terminal = state => ["completed", "partial", "failed", "error", "cancelled", "expired"].includes(state);
  const states = new Set(["draft", "preparing", "uploading", "waiting_for_resources", "submitting", "queued", "running", "postprocessing", "archiving", "cancelling", "completed", "partial", "failed", "error", "cancelled", "expired"]);
  const storageMessage = "Не удалось сохранить запрос в браузере. Разрешите хранение данных сайта и повторите проверку.";
  const newID = crypto => {
    if (crypto?.randomUUID) return crypto.randomUUID();
    if (!crypto?.getRandomValues) throw new Error("Не удалось подготовить идентификатор запроса. Обновите страницу.");
    return [...crypto.getRandomValues(new Uint8Array(16))].map(value => value.toString(16).padStart(2, "0")).join("");
  };
  const normalizeRecord = candidate => {
    if (!candidate || !validID(candidate.requestID) || !["single", "batch"].includes(candidate.kind || "single") ||
        candidate.jobID && !validID(candidate.jobID) || candidate.batchID && !validID(candidate.batchID) ||
        candidate.promptID && !validPromptID(candidate.promptID) || !Number.isFinite(candidate.savedAt) || candidate.savedAt <= 0) throw new Error(storageMessage);
    return { version: 2, requestID: candidate.requestID, kind: candidate.kind || "single", jobID: candidate.jobID || "", batchID: candidate.batchID || "", promptID: candidate.promptID || "",
      savedAt: candidate.savedAt, video: Boolean(candidate.video), confirmed: Boolean(candidate.confirmed), cancelling: Boolean(candidate.cancelling), resolvingRejection: Boolean(candidate.resolvingRejection), terminal: Boolean(candidate.terminal) };
  };
  /**
   * Account-scoped, per-request durable pointers. Independent tabs cannot erase
   * another request by overwriting one shared JSON map. No parameters or media
   * are stored here. A known terminal marker survives a failed removal.
   * @param {{storage: () => Storage, owner: string, legacyKey?: string}} options
   * @returns {{read: () => Object|null, write: (record: Object) => Object, complete: (record: Object) => void}}
   */
  const createPersistence = ({ storage, owner, legacyKey = "ai-gateway.active-generation" }) => {
    if (!/^[1-9][0-9]*$/.test(String(owner || ""))) throw new Error(storageMessage);
    const prefix = `ai-gateway.generation-submission.${owner}.`;
    const keyFor = record => prefix + record.requestID;
    const flags = (target, record) => ({ ...record,
      cancelling: record.cancelling || target.getItem(keyFor(record) + ".cancelling") === "1",
      terminal: record.terminal || target.getItem(keyFor(record) + ".terminal") === "1" });
    const write = candidate => {
      let record = normalizeRecord(candidate);
      const target = storage(), raw = target.getItem(keyFor(record));
      if (raw) {
        const previous = normalizeRecord(JSON.parse(raw));
        if (previous.kind !== record.kind || previous.requestID !== record.requestID ||
            ["jobID", "batchID", "promptID"].some(key => previous[key] && record[key] && previous[key] !== record[key])) throw new Error(storageMessage);
        record = { ...record, jobID: record.jobID || previous.jobID, batchID: record.batchID || previous.batchID, promptID: record.promptID || previous.promptID,
          confirmed: record.confirmed || previous.confirmed, cancelling: record.cancelling || previous.cancelling, terminal: record.terminal || previous.terminal };
      }
      // Flags are independent monotonic writes, not a read/modify/write map.
      // Even overlapping writes from two tabs cannot clear an acknowledged flag.
      for (const flag of ["cancelling", "terminal"]) {
        if (!record[flag]) continue;
        const key = keyFor(record) + "." + flag;
        target.setItem(key, "1");
        if (target.getItem(key) !== "1") throw new Error(storageMessage);
      }
      record = flags(target, record);
      if (record.terminal) return record;
      const value = JSON.stringify(record);
      target.setItem(keyFor(record), value);
      if (target.getItem(keyFor(record)) !== value) throw new Error(storageMessage);
      return record;
    };
    const read = () => {
      const target = storage(), records = [], finished = new Set();
      for (let index = 0; index < target.length; index++) {
        const key = target.key(index);
        if (!key?.startsWith(prefix)) continue;
        if (key.endsWith(".terminal") || key.endsWith(".cancelling")) continue;
        const raw = target.getItem(key);
        if (!raw) continue;
        const record = flags(target, normalizeRecord(JSON.parse(raw)));
        if (key !== keyFor(record)) throw new Error(storageMessage);
        if (record.terminal) finished.add(record.requestID);
        else records.push(record);
      }
      const legacy = target.getItem(legacyKey);
      if (legacy) {
        const record = flags(target, normalizeRecord(JSON.parse(legacy)));
        if (!record.terminal && !finished.has(record.requestID) && !records.some(item => item.requestID === record.requestID)) records.push(record);
      }
      return records.sort((left, right) => left.savedAt - right.savedAt || left.requestID.localeCompare(right.requestID))[0] || null;
    };
    const complete = record => {
      write({ ...record, terminal: true });
      const target = storage();
      // An unscoped legacy pointer may belong to another logged-in account.
      // Remove it only after an owned binding and durable completion are proven.
      try {
        const legacy = JSON.parse(target.getItem(legacyKey) || "null");
        if (record.confirmed && legacy?.requestID === record.requestID) target.removeItem(legacyKey);
        target.removeItem(keyFor(record));
        // The separate one-byte terminal marker remains for other open tabs.
      } catch (_) {}
    };
    return { read, write, complete };
  };
  /**
   * HTTP adapter. submit is invoked only by start, never by a recovery timer.
   * @param {{csrf:()=>string, fetcher?:Function, timeoutSignal?:(ms:number)=>AbortSignal}} options
   * @returns {(operation:string, record:SubmissionRecord, body:URLSearchParams|undefined, signal:AbortSignal, correlationID?:string)=>Promise<SubmissionResponse>}
   */
  const createTransport = ({ csrf, fetcher = fetch, timeoutSignal = ms => AbortSignal.timeout(ms) }) => async (operation, record, body, signal, correlationID = "") => {
    const fields = new URLSearchParams({ kind: record.kind, request_id: record.requestID });
    let path, method = "GET";
    if (operation === "submit") {
      path = record.kind === "batch" ? "/generate/batches" : "/generate/run";
      body = new URLSearchParams(body);
      body.set("client_request_id", record.requestID);
      method = "POST";
    } else if (operation === "close") {
      path = "/generate/submission/close"; body = fields; method = "POST";
    } else if (operation === "cancel") {
      path = record.kind === "batch" ? "/generate/batches/cancel" : "/generate/jobs/cancel";
      body = new URLSearchParams(record.kind === "batch" ? { batch_id: record.batchID } : { job_id: record.jobID });
      method = "POST";
    } else if (operation === "status") {
      path = `/generate/status?${new URLSearchParams({ prompt_id: record.promptID })}`;
    } else if (operation === "lookup") {
      path = `/generate/submission?${fields}`;
    } else throw new Error("Unknown submission operation");
    if (method === "GET") body = undefined;
    const headers = { Accept: "application/json" };
    if (method === "POST") {
      body.set("csrf", csrf() || "");
      headers["Content-Type"] = "application/x-www-form-urlencoded;charset=UTF-8";
    }
    if (operation === "submit" && correlationID) headers["X-Correlation-ID"] = correlationID;
    const response = await fetcher(path, { method, body, headers, credentials: "same-origin", cache: "no-store", redirect: "error", signal: AbortSignal.any([signal, timeoutSignal(20000)]) });
    return { ok: response.ok, status: response.status, payload: await response.json().catch(() => ({})) };
  };
  /**
   * Single-owner submit/recovery lifecycle for ordinary runs and batches.
   * Start persists before its sole POST. Unknown replies retain the identity;
   * close fences only creation, while cancellation uses existing job/batch APIs.
   * @param {Object} options
   * @param {Function} options.transport createTransport-compatible HTTP boundary.
   * @param {Object} options.persistence read/write/complete durable pointer API.
   * @param {() => string} options.newRequestID Cryptographically random ID.
   * @param {Function} [options.socket] Factory receiving the current prompt ID.
   * @param {(snapshot:SubmissionSnapshot)=>void} [options.onState] Presentation-only lifecycle snapshot.
   * @param {Function} [options.onAccepted] First confirmed publication, once per page.
   * @param {Function} [options.onProgress] Identity-checked WebSocket event.
   * @param {Function} [options.onFinished] Confirmed outcome or accepted batch handoff.
   * @returns {{start:(body:URLSearchParams, options?:Object)=>Promise<boolean>, cancel:()=>boolean, resume:(candidate?:SubmissionRecord)=>boolean, suspend:()=>void, refresh:()=>boolean, setVisible:(visible:boolean)=>void, snapshot:()=>SubmissionSnapshot}}
   */
  const createController = ({ transport, persistence, newRequestID, socket, onState = () => {}, onAccepted = () => {}, onProgress = () => {}, onFinished = () => {}, now = () => performance.now(), wallNow = Date.now, schedule = setTimeout, unschedule = clearTimeout }) => {
    let record = null, payload = null, phase = "idle", message = "", active = true, visible = true;
    let flight = null, timer = null, epoch = 0, failures = 0, retryAt = 0, deadline = 0, forceLookup = true, accepted = false;
    let progressSocket = null, live = false, nextSocketAt = 0, socketFailures = 0, lastRejection = "";
    const snapshot = () => ({ record: record && { ...record }, payload, phase, message, pending: Boolean(record), live, retrySeconds: Math.max(0, Math.ceil((retryAt - now()) / 1000)) });
    const publish = (next = phase, text = message) => { phase = next; message = text; onState(snapshot()); };
    const clearTimer = () => { if (timer !== null) unschedule(timer); timer = null; retryAt = 0; };
    const closeSocket = () => {
      const previous = progressSocket;
      progressSocket = null; live = false;
      if (previous) { previous.onmessage = previous.onclose = previous.onerror = null; previous.close(); }
    };
    const stopWork = () => { epoch++; clearTimer(); flight?.abort(); flight = null; closeSocket(); };
    const save = next => { record = persistence.write(next) || next; };
    const connectSocket = () => {
      if (!active || !visible || !record?.promptID || progressSocket || !socket || now() < nextSocketAt) return;
      const promptID = record.promptID, stamp = epoch;
      let current;
      try { current = socket(promptID); } catch (_) { nextSocketAt = now() + 15000; return; }
      progressSocket = current;
      current.onmessage = event => {
        if (!active || progressSocket !== current || stamp !== epoch || record?.promptID !== promptID) return;
        let eventPayload;
        try { eventPayload = JSON.parse(event.data); } catch (_) { return; }
        if (eventPayload?.data?.prompt_id !== promptID || !["execution_start", "executing", "progress"].includes(eventPayload.type)) return;
        live = true; socketFailures = 0;
        onProgress(eventPayload);
      };
      current.onclose = () => {
        if (progressSocket !== current) return;
        progressSocket = null; live = false;
        nextSocketAt = now() + Math.min(30000, 2000 * ++socketFailures);
      };
      current.onerror = () => { if (progressSocket === current) current.close(); };
    };
    const queue = milliseconds => {
      clearTimer();
      if (!active || !record || ["paused", "auth_required", "storage_error"].includes(phase)) return;
      retryAt = now() + (visible ? milliseconds : Math.max(milliseconds, 30000));
      const stamp = epoch;
      const tick = () => {
        if (stamp !== epoch || !active || !record) return;
        if (now() >= retryAt) { timer = null; retryAt = 0; void poll(); }
        else { publish(); timer = schedule(tick, Math.min(1000, retryAt - now())); }
      };
      timer = schedule(tick, Math.min(1000, Math.max(0, retryAt - now())));
    };
    const finish = (result, outcome = "finished") => {
      const previous = record;
      stopWork();
      try { persistence.complete(previous); }
      catch (_) { payload = result; publish("storage_error", storageMessage); return; }
      record = null; payload = result;
      publish(outcome, result.message || result.error || "");
      onFinished(result, previous, outcome);
    };
    const acceptPayload = (response, operation) => {
      let result = response.payload;
      if (operation === "cancel") {
        const item = record.kind === "batch" ? result?.batch : result?.job;
        if (!item || (record.kind === "batch" ? item.batch_id !== record.batchID : item.job_id !== record.jobID) || item.request_id && item.request_id !== record.requestID) throw new Error("Ответ отмены не соответствует текущему запросу.");
        result = { ...item, ...result, state: item.state, request_id: record.requestID, kind: record.kind };
      }
      if (!result || result.request_id !== record.requestID || result.kind && result.kind !== record.kind || !states.has(result.state || result.batch?.state)) throw new Error("Не удалось подтвердить ответ о запуске.");
      if (record.kind === "single" && result.state === "partial" || result.state && result.batch?.state && result.state !== result.batch.state) throw new Error("Не удалось подтвердить состояние задания.");
      if (operation === "status" && result.prompt_id !== record.promptID) throw new Error("Состояние относится к другой генерации.");
      if (result.request_closed === true) {
        if (!["cancelled", "expired"].includes(result.state)) throw new Error("Некорректное подтверждение закрытия запроса.");
        if (record.resolvingRejection) result = { ...result, state: "error", message: lastRejection || "Запрос не принят. Параметры сохранены, можно повторить попытку." };
        finish(result); return true;
      }
      const jobID = result.job_id || result.job?.job_id || record.jobID;
      const promptID = result.prompt_id || record.promptID;
      const batchID = result.batch?.batch_id || record.batchID;
      if (result.job && (result.job.job_id !== jobID || result.job.request_id && result.job.request_id !== record.requestID)) throw new Error("Результат относится к другому заданию.");
      if (record.kind === "single" && !validID(jobID) || record.kind === "batch" && !validID(batchID) || promptID && !validPromptID(promptID) ||
          record.jobID && record.jobID !== jobID || record.batchID && record.batchID !== batchID || record.promptID && record.promptID !== promptID) throw new Error("Идентификатор задания не подтверждён.");
      const next = { ...record, jobID, promptID, batchID, confirmed: true, resolvingRejection: false };
      try { save(next); } catch (_) { publish("storage_error", storageMessage); return false; }
      payload = result; failures = 0; retryAt = 0;
      const state = result.state || result.batch?.state;
      if (!accepted && (record.kind === "batch" || !terminal(state) || state === "completed")) {
        accepted = true; onAccepted(result, { ...record });
      }
      if (record.kind === "batch" && !record.cancelling) { finish(result, "batch_accepted"); return true; }
      if (terminal(state)) {
        // A terminal status may omit media after server-side reconciliation.
        // The request read attaches the retained, permission-filtered job media.
        if (record.kind === "single" && operation !== "lookup" && state === "completed" && !result.outputs?.length && !result.job?.media?.length) { forceLookup = true; publish("recovering", "Получаем сохранённый результат."); return true; }
        finish(result); return true;
      }
      forceLookup = false;
      publish(record.cancelling || state === "cancelling" ? "cancelling" : promptID ? "running" : "waiting", result.message || "Задание сохранено на сервере.");
      connectSocket();
      return true;
    };
    const execute = async (operation, body, correlationID) => {
      const stamp = epoch, request = { ...record }, controller = new AbortController();
      let comfyUnavailable = false;
      flight = controller;
      try {
        const response = await transport(operation, request, body, controller.signal, correlationID);
        if (stamp !== epoch || !active || record?.requestID !== request.requestID) return;
        if ([401, 403].includes(response.status)) { publish("auth_required", response.payload?.error || "Войдите снова, чтобы проверить текущую задачу."); return; }
        if (response.ok || response.payload?.request_closed === true || response.payload?.request_id === record.requestID && validID(response.payload?.job_id) && terminal(response.payload?.state)) {
          if (acceptPayload(response, operation)) return;
          if (phase === "storage_error") return;
        }
        if (operation === "submit" && [400, 409, 422, 429].includes(response.status)) {
          lastRejection = response.payload?.error || "Запрос не принят.";
          try { save({ ...record, resolvingRejection: true }); }
          catch (_) { publish("storage_error", storageMessage); return; }
          publish("recovering", `${lastRejection} Проверяем закрытие запроса.`);
          return;
        }
        if (operation === "status" && [502, 503, 504].includes(response.status)) {
          comfyUnavailable = true;
          throw new Error("ComfyUI временно недоступен. Задание сохранено, проверим его снова.");
        }
        throw new Error(response.payload?.error || response.payload?.message || "Связь с Gateway временно потеряна. Повторная отправка не требуется.");
      } catch (error) {
        if (stamp !== epoch || !active || record?.requestID !== request.requestID) return;
        failures++; forceLookup = true; closeSocket();
        publish("recovering", error.message || "Не удалось проверить запрос.");
      } finally {
        if (stamp === epoch) {
          flight = null;
          const retryDelay = failures
            ? Math.min(comfyUnavailable ? 30000 : 15000, (comfyUnavailable ? 5000 : 2000) * failures)
            : record?.resolvingRejection || record?.cancelling && operation === "close" ? 0 : 2000;
          if (record) queue(retryDelay);
        }
      }
    };
    const poll = async () => {
      if (!active || !record || flight) return;
      if (now() >= deadline && !record.cancelling && !record.resolvingRejection) { closeSocket(); publish("paused", "Задача сохранена. Можно проверить состояние или отменить её."); return; }
      connectSocket();
      const known = record.kind === "batch" ? record.batchID : record.jobID;
      const operation = record.cancelling && known && record.confirmed ? (forceLookup ? "lookup" : "cancel") : record.cancelling || record.resolvingRejection ? "close" : record.kind === "single" && record.promptID && !forceLookup ? "status" : "lookup";
      await execute(operation);
    };
    const resume = candidate => {
      const previousID = record?.requestID;
      stopWork(); active = true; forceLookup = true; failures = 0;
      try { record = candidate ? normalizeRecord(candidate) : record || persistence.read(); }
      catch (_) { publish("storage_error", storageMessage); return false; }
      if (!record) { publish("idle", ""); return false; }
      if (record.requestID !== previousID) { accepted = false; payload = null; }
      deadline = now() + (record.video ? 60 : 20) * 60 * 1000;
      publish(record.cancelling ? "cancelling" : "recovering", "Проверяем сохранённый запрос.");
      void poll(); return true;
    };
    const start = async (body, { kind = "single", video = false, correlationID = "" } = {}) => {
      if (record || flight) throw new Error("Предыдущий запрос ещё не завершён. Проверьте его состояние или отмените.");
      let previous;
      try { previous = persistence.read(); } catch (_) { publish("storage_error", storageMessage); throw new Error(storageMessage); }
      if (previous) { resume(previous); return false; }
      const requestID = newRequestID();
      if (!validID(requestID) || !["single", "batch"].includes(kind)) throw new Error("Некорректный запрос генерации.");
      const next = normalizeRecord({ requestID, kind, video, savedAt: wallNow() });
      let durable;
      try {
        durable = persistence.write(next) || next;
        if (durable.terminal) throw new Error(storageMessage);
      } catch (_) { publish("storage_error", storageMessage); throw new Error(storageMessage); }
      stopWork(); active = true; record = durable; payload = null; accepted = false; failures = 0; lastRejection = ""; forceLookup = true;
      deadline = now() + (video ? 60 : 20) * 60 * 1000;
      publish("sending", kind === "batch" ? "Проверяем варианты и сохраняем пакет." : "Сохраняем задание в очереди Gateway...");
      await execute("submit", new URLSearchParams(body), correlationID);
      return true;
    };
    const cancel = () => {
      if (!record) return false;
      const next = { ...record, cancelling: true, resolvingRejection: false };
      let durable;
      try { durable = persistence.write(next) || next; } catch (_) { publish("storage_error", storageMessage); return false; }
      stopWork(); active = true; record = durable; forceLookup = true;
      publish("cancelling", "Закрываем запрос и проверяем подтверждение отмены.");
      void poll(); return true;
    };
    const suspend = () => { active = false; stopWork(); };
    const refresh = () => resume();
    const setVisible = value => {
      visible = Boolean(value);
      if (!visible) closeSocket();
      if (active && record && !flight && !["paused", "auth_required", "storage_error"].includes(phase)) queue(visible ? 0 : 30000);
    };
    return { start, cancel, resume, suspend, refresh, setVisible, snapshot };
  };
  return { createController, createTransport, createPersistence, validID, newID, terminal };
});
