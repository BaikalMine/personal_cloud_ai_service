(function bootstrapAssistantJobs(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (root) (root.AIGatewayGeneration ||= {}).assistantJobs = api;
})(typeof window !== "undefined" ? window : null, function assistantJobsFactory() {
  const terminal = state => ["completed", "failed", "cancelled"].includes(state);
  const validID = id => typeof id === "string" && /^[a-zA-Z0-9_-]{16,64}$/.test(id);
  const createController = ({ transport, persist, onState = () => {}, onJob = () => {}, now = Date.now, schedule = setTimeout, unschedule = clearTimeout }) => {
    let record = null, job = null, active = true, flight = null, timer = null, epoch = 0, retryAt = 0, failures = 0;
    let submission = null, missingReceipts = 0;
    const snapshot = () => ({ record: record && { ...record }, job, pending: Boolean(record && !terminal(job?.state)), cancelling: Boolean(record?.cancelling), retrySeconds: Math.max(0, Math.ceil((retryAt - now()) / 1000)) });
    const publish = message => onState({ ...snapshot(), message });
    const clearTimer = () => { if (timer !== null) unschedule(timer); timer = null; };
    const save = () => persist(record && { ...record });
    const queue = seconds => {
      clearTimer();
      if (!active || !snapshot().pending) return;
      retryAt = now() + seconds * 1000;
      const tick = () => {
        if (!active || !snapshot().pending) return;
        publish(failures ? "Связь временно потеряна." : record.cancelling ? "Отменяем запрос." : job?.status || "Проверяем запрос ассистента.");
        if (retryAt <= now()) { timer = null; void poll(); }
        else timer = schedule(tick, Math.min(1000, retryAt - now()));
      };
      timer = schedule(tick, Math.min(1000, seconds * 1000));
    };
    const receive = payload => {
      if (payload?.id !== record?.id || !["queued", "running", "completed", "failed", "cancelled"].includes(payload.state)) throw new Error("Не удалось прочитать состояние запроса.");
      job = payload;
      submission = null;
      missingReceipts = 0;
      record.expiresAt = Date.parse(payload.expires_at) || record.expiresAt;
      if (terminal(job.state)) record.cancelling = false;
      failures = 0; retryAt = 0;
      try { save(); } catch (_) {}
      onJob(job);
      publish(job.status);
    };
    const execute = async (operation, body) => {
      const currentEpoch = epoch;
      const controller = new AbortController();
      flight = controller;
      try {
        let response = await transport(operation, body, controller.signal);
        if (currentEpoch !== epoch || !active) return;
        // A lost POST may never have reached the server. Retry its immutable
        // payload with the same idempotency key only after checking for a receipt.
        if (operation === "get" && response.status === 404 && submission && !record.cancelling) {
          publish("Восстанавливаем отправку того же запроса.");
          operation = "recover-submit";
          response = await transport("submit", new URLSearchParams(submission), controller.signal);
        }
        if (currentEpoch !== epoch || !active) return;
        if (operation === "get" && response.status === 404 && !submission) {
          if (++missingReceipts >= 2) {
            job = { id: record.id, state: "failed", status: "Сохранённый запрос не найден. Можно подготовить новый вариант." };
            record = null; save(); onJob(job); publish(job.status); return;
          }
          publish("Проверяем сохранённый запрос.");
          throw new Error("Сохранённый запрос пока не найден.");
        }
        if (!response.ok) {
          // A missing receipt after a lost POST is not evidence that POST cannot arrive later.
          if (operation === "submit" && [400, 401, 403, 409, 422, 429].includes(response.status)) {
            job = { id: record.id, state: "failed", status: response.payload?.error || "Запрос не принят." };
            submission = null;
            record = null; save();
            onJob(job); publish(job.status); return;
          }
          if ([401, 403].includes(response.status)) {
            publish(response.payload?.error || "Нет доступа к запросу. Войдите снова.");
            active = false; return;
          }
          throw new Error(response.payload?.error || "Не удалось проверить запрос.");
        }
        receive(response.payload);
      } catch (error) {
        if (currentEpoch !== epoch || !active) return;
        failures++;
        publish(error instanceof TypeError || error.name === "TimeoutError" || error.name === "AbortError"
          ? "Связь прервана. Проверяем сохранённый запрос; повторно модель не запускаем."
          : error.message || "Не удалось проверить сохранённый запрос.");
      } finally {
        if (currentEpoch === epoch) {
          flight = null;
          if (snapshot().pending) queue(failures ? Math.min(30, 3 * 2 ** Math.min(failures, 3)) : job?.progress?.started_at ? 0.5 : 2);
        }
      }
    };
    const poll = async () => {
      if (!active || !record || flight || terminal(job?.state)) return;
      if (record.expiresAt <= now()) {
        submission = null;
        job = { id: record.id, state: "failed", status: "Срок хранения запроса истёк. Подготовьте новый вариант." };
        onJob(job); publish(job.status); return;
      }
      await execute(record.cancelling ? "cancel" : "get", new URLSearchParams({ id: record.id }));
    };
    const suspend = () => { active = false; epoch++; clearTimer(); flight?.abort(); flight = null; };
    const resume = candidate => {
      if (candidate && validID(candidate.id)) {
        suspend();
        if (candidate.id !== record?.id) submission = null;
        record = { id: candidate.id, expiresAt: Number(candidate.expiresAt) || now() + 86400000, cancelling: Boolean(candidate.cancelling) };
        job = null;
      }
      active = true;
      clearTimer();
      if (record && !terminal(job?.state)) { publish("Проверяем сохранённый запрос."); void poll(); }
    };
    const start = async (id, body) => {
      if (snapshot().pending || !validID(id)) throw new Error("Предыдущий запрос ещё не завершён.");
      suspend(); active = true; job = null; failures = 0; missingReceipts = 0;
      submission = null;
      record = { id, expiresAt: now() + 86400000, cancelling: false };
      try { save(); } catch (_) { record = null; throw new Error("Не удалось сохранить запрос в браузере. Разрешите хранение данных сайта и повторите."); }
      body.set("request_id", id);
      submission = body.toString();
      publish("Сохраняем запрос.");
      await execute("submit", body);
    };
    const cancel = () => {
      if (!snapshot().pending) return;
      suspend(); active = true; record.cancelling = true;
      submission = null;
      save(); publish("Отменяем запрос."); void poll();
    };
    return { start, resume, suspend, cancel, snapshot };
  };
  return { createController, validID, terminal };
});
