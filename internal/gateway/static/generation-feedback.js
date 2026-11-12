(function bootstrapGenerationFeedback(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (!root) return;
  root.AIGatewayGeneration = root.AIGatewayGeneration || {};
  root.AIGatewayGeneration.feedback = api;
})(typeof window !== "undefined" ? window : null, function generationFeedbackFactory() {
  const authMessage = "Сеанс завершён или доступ изменился. Войдите в аккаунт снова.";
  const failure = (kind, message = "", status = 0) => Object.assign(new Error(message), { feedbackKind: kind, status });
  const errorMessage = (error, fallback = "Не удалось получить данные. Повторите запрос.") => {
    if (error?.feedbackKind === "auth") return authMessage;
    if (error?.feedbackKind === "timeout") return "Сервер не ответил вовремя. Повторите запрос.";
    if (error?.feedbackKind === "aborted") return "";
    return error?.feedbackKind && error.message ? error.message : fallback;
  };
  const tone = phase => {
    if (["loading", "sending", "preparing", "uploading", "saving", "deleting", "checking"].includes(phase)) return "loading";
    if (["error", "failed", "auth_required", "storage_error", "safety"].includes(phase)) return "error";
    if (["ready", "saved", "success", "approved", "completed"].includes(phase)) return "success";
    if (["warning", "dirty", "stale", "conflict", "paused", "recovering", "uncertain"].includes(phase)) return "warning";
    return "info";
  };
  const text = (node, value) => {
    if (!node) return;
    const next = String(value ?? "");
    if (node.textContent !== next) node.textContent = next;
  };
  /** Presentation only: does not own an operation, hide content or change focus. */
  const present = (node, phase = "", message) => {
    if (!node) return;
    const state = tone(phase);
    node.dataset.state = phase;
    node.dataset.feedbackState = state;
    for (const name of ["loading", "error", "warning", "success"]) node.classList.toggle("is-" + name, state === name);
    node.setAttribute("aria-busy", String(state === "loading"));
    if (message !== undefined) text(node, message);
  };
  /** Keeps disabled-state ownership with the caller when `disabled` is omitted. */
  const button = (node, { busy = false, disabled } = {}) => {
    if (!node) return;
    node.classList.toggle("is-loading", Boolean(busy));
    node.setAttribute("aria-busy", String(Boolean(busy)));
    if (disabled !== undefined) node.disabled = Boolean(disabled);
  };
  /** Shared read-state UI; known data remains owned and rendered by the caller. */
  const resourceView = ({ root, message, retry, login }, state, { loading = "Загружаем данные...", empty = "", ready = "", idle = "",
    showRefresh = false, showBackgroundLoading = false, loginURL = "/login?next=%2Fgenerate" } = {}) => {
    const failed = state.phase === "error" || state.authRequired;
    const pending = state.loading && (!state.loaded || showBackgroundLoading);
    const copy = failed ? state.message : pending ? loading : state.phase === "empty" ? empty : state.loaded ? ready : idle;
    if (root) root.hidden = !copy;
    present(root, failed ? state.phase : pending ? "loading" : "", undefined);
    text(message, copy);
    if (retry) {
      retry.hidden = state.authRequired || !(showRefresh || failed);
      button(retry, { busy: state.loading, disabled: state.loading || !state.active || state.authRequired });
    }
    if (login) { login.hidden = !state.authRequired; login.href = loginURL; }
  };

  /**
   * Bounded JSON boundary for non-durable studio reads and explicit operations.
   * Never retries. Durable generation/assistant transports remain their owners.
   * @param {string} url Same-origin application endpoint.
   * @param {{fetcher?:Function, method?:string, body?:URLSearchParams, signal?:AbortSignal,
   * timeoutSignal?:Function, message?:string}} options
   */
  const requestJSON = async (url, { fetcher = fetch, method = "GET", body, signal,
    timeoutSignal = ms => AbortSignal.timeout(ms), message = "Не удалось получить данные. Повторите запрос." } = {}) => {
    const timeout = timeoutSignal(20000), combined = AbortSignal.any([signal, timeout].filter(Boolean));
    try {
      const headers = { Accept: "application/json" };
      if (body) headers["Content-Type"] = "application/x-www-form-urlencoded;charset=UTF-8";
      const response = await fetcher(url, { method, body, headers, credentials: "same-origin", cache: "no-store", redirect: "manual", signal: combined });
      if (response.type === "opaqueredirect" || response.redirected || response.status === 401 || response.status === 403 || response.status >= 300 && response.status < 400) throw failure("auth", authMessage, response.status);
      const payload = await response.json().catch(() => null);
      if (!response.ok) throw failure("http", typeof payload?.error === "string" ? payload.error : message, response.status);
      if (!payload || typeof payload !== "object" || Array.isArray(payload)) throw failure("invalid", "Ответ сервера не удалось прочитать. Повторите запрос.");
      return payload;
    } catch (error) {
      if (signal?.aborted) throw failure("aborted");
      if (timeout?.aborted) throw failure("timeout");
      if (error?.feedbackKind) throw error;
      throw failure("network", message);
    }
  };

  /**
   * Optional read model: coalesces concurrent reads, retains data on transient
   * failure, clears it on access loss and fences replies after pause/accept.
   * It has no polling or mutation retry loop; the owning page supplies lifecycle.
   * @param {{load:Function, initialValue?:any, isEmpty?:Function, onValue?:Function,
   * onState?:Function, onAuth?:Function, message?:string}} options
   */
  const createResource = ({ load, initialValue = null, isEmpty = value => Array.isArray(value) && value.length === 0,
    onValue = () => {}, onState = () => {}, onAuth = () => {}, message }) => {
    let active = true, authRequired = false, epoch = 0, flight = null;
    let state = { value: initialValue, loaded: false, phase: "idle", message: "" };
    const snapshot = () => ({ ...state, active, authRequired, loading: state.phase === "loading" });
    const publish = () => onState(snapshot());
    const invalidate = () => { epoch++; const old = flight; flight = null; old?.controller.abort(); };
    const accept = value => {
      if (!active || authRequired) return false;
      invalidate();
      state = { value, loaded: true, phase: isEmpty(value) ? "empty" : "ready", message: "" };
      onValue(value); publish(); return true;
    };
    const revoke = () => {
      if (authRequired) return;
      invalidate(); authRequired = true;
      state = { value: initialValue, loaded: false, phase: "auth_required", message: authMessage };
      onValue(initialValue); publish(); onAuth();
    };
    const refresh = () => {
      if (!active || authRequired) return Promise.resolve(false);
      if (flight) return flight.promise;
      const controller = new AbortController(), stamp = epoch;
      const current = { controller, promise: null };
      flight = current;
      const alive = () => active && !authRequired && epoch === stamp && flight === current;
      state = { ...state, phase: "loading", message: "" }; publish();
      current.promise = (async () => {
        try {
          const value = await load(controller.signal);
          if (!alive()) return false;
          state = { value, loaded: true, phase: isEmpty(value) ? "empty" : "ready", message: "" };
          onValue(value); publish(); return true;
        } catch (error) {
          if (!alive()) return false;
          if (error?.feedbackKind === "auth") revoke();
          else if (error?.feedbackKind !== "aborted") {
            state = { ...state, phase: "error", message: errorMessage(error, message) }; publish();
          }
          return false;
        } finally { if (flight === current) flight = null; }
      })();
      return current.promise;
    };
    const pause = () => {
      active = false; invalidate();
      if (state.phase === "loading") state = { ...state, phase: state.loaded ? "stale" : "idle", message: "" };
      publish();
    };
    const resume = ({ read = true } = {}) => {
      active = true;
      publish();
      return read ? refresh() : Promise.resolve(false);
    };
    return { snapshot, refresh, accept, revoke, pause, resume };
  };
  return { authMessage, failure, errorMessage, tone, text, present, button, resourceView, requestJSON, createResource };
});
