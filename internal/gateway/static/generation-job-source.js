(function bootstrapJobSource(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (root) {
    root.AIGatewayGeneration = root.AIGatewayGeneration || {};
    root.AIGatewayGeneration.jobSource = api;
  }
})(typeof window !== "undefined" ? window : null, function jobSourceFactory() {
  const validID = id => /^[A-Za-z0-9_-]{1,200}$/.test(id || "");
  const createController = ({ id, kind = "generation", enabled = Boolean(id), load, apply, onState = () => {}, onConsumed = () => {} }) => {
    let status = enabled ? "waiting" : "idle", error = "", edited = false, payload = null;
    let controller = null, epoch = 0, active = true, armed = false, requiresInputs = false;
    const snapshot = () => ({ status, error, requiresInputs, blocked: !["idle", "ready", "kept"].includes(status) });
    const publish = (next, message = "") => { status = next; error = message; onState(snapshot()); };
    const consume = () => { payload = null; onConsumed(); };
    const applyPending = () => {
      if (!active || !payload) return;
      try {
        apply(payload);
        edited = false;
        publish("ready");
        consume();
      } catch (failure) { publish("error", failure.message || "Не удалось перенести параметры задачи."); }
    };
    const start = async () => {
      armed = true;
      if (!active || ["idle", "ready", "kept", "loading"].includes(status)) return;
      if (!validID(id) || !["generation", "lora_training"].includes(kind)) { publish("error", "Некорректный адрес исходной задачи."); return; }
      controller?.abort(); controller = new AbortController();
      const current = ++epoch, request = controller;
      publish("loading");
      try {
        const data = await load(id, request.signal);
        if (current !== epoch || request.signal.aborted || !active) return;
        const identityMatches = kind === "generation" ? data?.parent_job_id === id && !data?.source_kind
          : data?.source_kind === kind && data?.source_id === id && !data?.parent_job_id && !data?.values?.parent_job_id &&
            data?.values?.loras_configured === "true" && typeof data?.values?.lora_1 === "string" && data.values.lora_1.trim() &&
            ["lora_model_strength_1", "lora_clip_strength_1"].every(name => typeof data.values[name] === "string" && data.values[name].trim() && Number.isFinite(Number(data.values[name])));
        if (!identityMatches || !data.values || typeof data.values !== "object" || Array.isArray(data.values) ||
            !["template_id", "generation_workflow", "model"].every(name => typeof data.values[name] === "string" && data.values[name]) ||
            !Object.values(data.values).every(value => typeof value === "string")) {
          throw new Error("В ответе нет полного набора параметров этой задачи.");
        }
        payload = data; requiresInputs = Boolean(data.requires_inputs);
        if (edited) publish("conflict"); else applyPending();
      } catch (failure) {
        if (current === epoch && !request.signal.aborted && active) publish("error", failure instanceof TypeError || ["AbortError", "TimeoutError"].includes(failure.name)
          ? "Не удалось получить параметры задачи. Проверьте соединение и повторите попытку."
          : failure.message || "Не удалось загрузить параметры задачи.");
      } finally { if (current === epoch) controller = null; }
    };
    const markEdited = () => { if (!["idle", "ready", "kept"].includes(status)) edited = true; };
    const keepCurrent = () => {
      epoch++; controller?.abort(); controller = null;
      publish("kept"); consume();
    };
    const setActive = value => {
      active = Boolean(value);
      if (!active && status === "loading") { epoch++; controller?.abort(); controller = null; publish("paused"); }
      if (active && armed && (status === "paused" || status === "waiting")) start();
    };
    publish(status);
    return { start, markEdited, keepCurrent, setActive, snapshot, confirmReplace: () => { if (status === "conflict") applyPending(); } };
  };
  return { validID, createController };
});
