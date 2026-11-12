(function bootstrapGenerationRecipes(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (!root) return;
  root.AIGatewayGeneration = root.AIGatewayGeneration || {};
  root.AIGatewayGeneration.recipes = api;
})(typeof window !== "undefined" ? window : null, function generationRecipesFactory() {
  const createState = (overrides = {}) => ({ items: [], selectedID: "", loading: false, message: "", status: "", ...overrides });
  const reduce = (state = createState(), action = {}) => {
    switch (action.type) {
      case "LOAD_START":
        return { ...state, loading: true };
      case "SET_LOADING":
        return { ...state, loading: Boolean(action.loading) };
      case "SET_ITEMS": {
        const items = Array.isArray(action.items) ? action.items : [];
        const selectedID = items.some((item) => String(item.id) === String(state.selectedID)) ? state.selectedID : "";
        return { ...state, items, selectedID, loading: false };
      }
      case "SELECT":
        return { ...state, selectedID: String(action.id || "") };
      case "SET_MESSAGE":
        return { ...state, message: String(action.message || ""), status: String(action.status || "") };
      default:
        return state;
    }
  };
  const selectedRecipe = (state) => state?.items?.find((item) => String(item.id) === String(state.selectedID)) || null;
  const validRecipe = item => item && (typeof item.id !== "number" || Number.isSafeInteger(item.id)) && /^[1-9][0-9]*$/.test(String(item.id)) && typeof item.name === "string"
    && item.values && typeof item.values === "object" && !Array.isArray(item.values);
  /**
   * Non-durable saved settings session. Uses the shared read/feedback contract;
   * successful writes publish their actual response, never depend on readback.
   * @param {{feedback:Object, slice:{get:Function,dispatch:Function}, transport:Function, onState?:Function}} options
   */
  const createController = ({ feedback, slice, transport, onState = () => {} }) => {
    let active = true, epoch = 0, operation = null, needsReview = false;
    let message = "", phase = "";
    const snapshot = () => ({ ...slice.get(), read: resource.snapshot(), active,
      busy: Boolean(operation), operation: operation?.kind || "", needsReview, message, phase });
    const publish = () => onState(snapshot());
    const resource = feedback.createResource({
      initialValue: [],
      load: async signal => {
        const payload = await transport("read", null, signal);
        if (!Array.isArray(payload.recipes) || !payload.recipes.every(validRecipe)) throw feedback.failure("invalid", "Список наборов не удалось прочитать. Повторите запрос.");
        return payload.recipes;
      },
      onValue: items => slice.dispatch({ type: "SET_ITEMS", items }),
      onState: state => {
        slice.dispatch({ type: "SET_LOADING", loading: state.loading });
        if (state.phase === "ready" || state.phase === "empty") {
          if (needsReview) { message = "Список обновлён. Проверьте наборы перед повторным сохранением."; phase = "info"; }
          needsReview = false;
        }
        publish();
      },
      onAuth: () => { epoch++; operation?.controller.abort(); operation = null; message = ""; phase = ""; publish(); },
    });
    const notify = (value, state = "") => { message = value; phase = state; slice.dispatch({ type: "SET_MESSAGE", message, status: state }); publish(); };
    const select = id => {
      if (!active || operation || resource.snapshot().authRequired) return;
      slice.dispatch({ type: "SELECT", id: slice.get().items.some(item => String(item.id) === String(id)) ? String(id) : "" }); publish();
    };
    const mutate = (kind, body) => {
      if (!active || operation || needsReview || resource.snapshot().loading || resource.snapshot().authRequired) return Promise.resolve({ ok: false });
      const stamp = epoch, controller = new AbortController(), current = { kind, controller };
      operation = current;
      notify(kind === "save" ? "Сохраняем набор..." : "Удаляем набор...", "loading");
      const alive = () => active && epoch === stamp && operation === current;
      return (async () => {
        try {
          const payload = await transport(kind, new URLSearchParams(body), controller.signal);
          if (!alive()) return { ok: false };
          if (kind === "save") {
            if (!validRecipe(payload.recipe)) throw feedback.failure("invalid", "Сохранение не удалось подтвердить. Обновите список наборов.");
            resource.accept([payload.recipe, ...slice.get().items.filter(item => String(item.id) !== String(payload.recipe.id))]);
            slice.dispatch({ type: "SELECT", id: String(payload.recipe.id) });
            notify("Набор сохранён. Фото в него не включаются.", "ready");
          } else {
            if (typeof payload.deleted !== "boolean") throw feedback.failure("invalid", "Удаление не удалось подтвердить. Обновите список наборов.");
            resource.accept(slice.get().items.filter(item => String(item.id) !== body.get("recipe_id")));
            notify(payload.deleted ? "Набор удалён." : "Набор уже удалён.", "ready");
          }
          return { ok: true, ...payload };
        } catch (error) {
          if (!alive()) return { ok: false };
          if (error?.feedbackKind === "auth") resource.revoke();
          else {
            needsReview = ["network", "timeout", "invalid", "aborted"].includes(error?.feedbackKind) || Number(error?.status) >= 500;
            notify(needsReview ? "Ответ не получен. Обновите список и проверьте наборы перед повтором."
              : feedback.errorMessage(error, "Не удалось изменить набор. Повторите запрос."), needsReview ? "uncertain" : "error");
          }
          return { ok: false };
        } finally { if (operation === current) operation = null; publish(); }
      })();
    };
    const pause = () => {
      active = false; epoch++;
      if (operation) {
        operation.controller.abort(); operation = null; needsReview = true;
        message = "Проверка прервана. Обновите список и проверьте наборы перед повтором."; phase = "uncertain";
      }
      resource.pause(); publish();
    };
    return { snapshot, select, notify, pause, refresh: () => operation ? Promise.resolve(false) : resource.refresh(), revoke: () => resource.revoke(),
      resume: () => { active = true; return resource.resume(); },
      save: body => mutate("save", body), remove: body => mutate("remove", body) };
  };
  return { createState, reduce, selectedRecipe, validRecipe, createController };
});
