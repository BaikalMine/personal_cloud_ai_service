(function bootstrapCollections(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (root) { root.AIGatewayGeneration = root.AIGatewayGeneration || {}; root.AIGatewayGeneration.collections = api; }
})(typeof window !== "undefined" ? window : null, function collectionsFactory() {
  const validID = value => /^[1-9][0-9]{0,18}$/.test(String(value || ""));
  const error = (message, properties = {}) => Object.assign(new Error(message), properties);
  const normalize = value => {
    const id = String(value?.id || value?.ID || "");
    if (!validID(id)) throw error("Не удалось прочитать коллекцию.");
    return { ...value, id, name: String(value.name ?? value.Name ?? ""), revision: Number(value.revision ?? value.Revision ?? 0) };
  };
  const createTransport = ({ fetcher = fetch, csrf, timeout = ms => AbortSignal.timeout(ms) }) => async (operation, values = {}, signal) => {
    const reading = operation === "read" || operation === "list";
    let url = "/generate/library/collections";
    if (!["list", "create"].includes(operation)) url += "/context";
    if (operation === "read") url += `?collection_id=${encodeURIComponent(values.collection_id)}`;
    const body = reading ? undefined : new URLSearchParams({ ...values, csrf: csrf() || "" });
    if (body && operation !== "create") body.set("action", operation);
    let response, payload;
    try {
      response = await fetcher(url, { method: reading ? "GET" : "POST", body, credentials: "same-origin", cache: "no-store",
        headers: body ? { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8" } : { Accept: "application/json" }, signal: signal || timeout(20000) });
      if (response.redirected || response.status === 401 || response.status === 403) throw error("Доступ к коллекциям недоступен. Проверьте вход и права.", { status: 403, code: "access" });
      payload = await response.json();
    } catch (cause) {
      if (cause.code === "access") throw cause;
      throw error(reading ? "Не удалось загрузить коллекции. Повторите проверку." : "Результат изменения неизвестен. Обновите состояние перед повтором.", { uncertain: !reading, cause });
    }
    if (!response.ok) throw error(payload.error || "Действие с коллекцией не выполнено.", { status: response.status, code: payload.code, uncertain: !reading && response.status >= 500 });
    return payload;
  };
  const createController = ({ transport, onState = () => {}, onSaved = () => {}, onRecord = () => {} }) => {
    let id = "", record = null, status = "empty", message = "", pending = false, uncertain = false, active = true, epoch = 0;
    let refreshAfterWrite = false;
    const snapshot = () => ({ id, record, status, message, pending, uncertain });
    const publish = () => onState(snapshot());
    const select = async (next, force = false) => {
      next = validID(next) ? String(next) : "";
      if (!force && next === id) return record;
      const stamp = ++epoch;
      id = next; record = null; message = "";
      status = id ? "loading" : "empty";
      publish();
      if (!id || !active) return null;
      try {
        const payload = await transport("read", { collection_id: id });
        if (stamp !== epoch || !active) return null;
        const value = normalize(payload.collection);
        if (value.id !== id || !(value.revision > 0) || typeof value.has_context !== "boolean") throw error("Получена другая или неполная коллекция.");
        record = value; status = "ready"; uncertain = false;
        onRecord(record);
        publish();
        return record;
      } catch (cause) {
        if (stamp !== epoch || !active) return null;
        status = cause.status === 403 ? "access" : cause.status === 404 ? "missing" : "error";
        message = cause.message;
        publish();
        return null;
      }
    };
    const write = async (operation, values, expected = record) => {
      if (pending || uncertain || status !== "ready" || !expected || expected.id !== id || expected.revision !== record?.revision) throw error("Состояние коллекции изменилось. Обновите его перед сохранением.");
      const target = id, stamp = epoch;
      pending = true; message = ""; publish();
      try {
        const payload = await transport(operation, { ...values, collection_id: target, collection_revision: expected.revision });
        if (payload.collection) {
          const next = normalize(payload.collection);
          if (next.id !== target || !(next.revision > expected.revision)) throw error("Не удалось подтвердить сохранение коллекции.", { uncertain: true });
          onSaved(next);
          if (stamp === epoch && active) { record = next; onRecord(next); }
        } else if (operation !== "open" || !payload.draft?.revision || !payload.draft.values || !Array.isArray(payload.draft.assets)) {
          throw error("Не удалось подтвердить сохранение коллекции.", { uncertain: true });
        }
        return payload;
      } catch (cause) {
        if (stamp === epoch && active) {
          uncertain = Boolean(cause.uncertain);
          status = cause.status === 403 ? "access" : cause.status === 404 ? "missing" : cause.status === 409 ? "conflict" : "error";
          if (cause.status === 403 || cause.status === 404) record = null;
          message = cause.message;
        }
        throw cause;
      } finally {
        pending = false;
        if (active && refreshAfterWrite) { refreshAfterWrite = false; void select(id, true); }
        else if (active) publish();
      }
    };
    const setActive = value => {
      active = Boolean(value);
      if (!active) { epoch++; record = null; status = id ? "loading" : "empty"; return; }
      if (id && pending) refreshAfterWrite = true;
      else if (id) void select(id, true);
    };
    return { select, refresh: () => select(id, true), write, snapshot, setActive };
  };
  return { validID, normalize, createTransport, createController };
});
