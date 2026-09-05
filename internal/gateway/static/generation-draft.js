(function bootstrapGenerationDraft(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (!root) return;
  root.AIGatewayGeneration = root.AIGatewayGeneration || {};
  root.AIGatewayGeneration.draft = api;
})(typeof window !== "undefined" ? window : null, function generationDraftFactory() {
  /**
   * Versioned autosave. transport returns {ok,status,payload}; capture returns
   * URLSearchParams. Only an explicit conflict choice can replace remote work.
   */
  const createController = ({ transport, capture, apply, onState = () => {}, delay = 900, schedule = setTimeout, unschedule = clearTimeout }) => {
    let revision = 0;
    let epoch = 0;
    let savedEpoch = 0;
    let ready = false;
    let stopped = false;
    let timer = null;
    let inFlight = null;
    let remote = null;
    let status = "loading";
    const snapshot = () => ({ revision, dirty: epoch !== savedEpoch, ready, status, remote });
    const publish = (next, error = "") => { status = next; onState({ ...snapshot(), error }); };
    const queue = () => {
      if (timer !== null) unschedule(timer);
      if (ready && !stopped && status !== "conflict") timer = schedule(() => { timer = null; flush().catch(() => {}); }, delay);
    };
    const load = async ({ preserveLocal = false } = {}) => {
      publish("loading");
      try {
        const result = await transport("load");
        if (!result.ok) throw new Error(result.payload?.error || "Не удалось загрузить черновик");
        remote = result.payload?.draft || null;
        revision = remote?.revision || 0;
        ready = true;
        if (remote && (epoch !== savedEpoch || preserveLocal)) {
          publish("conflict");
          return;
        }
        if (remote) {
          try { await apply(remote); }
          catch (error) { publish("conflict", error.message); return; }
        }
        if (!remote && preserveLocal && epoch === savedEpoch) epoch += 1;
        publish(epoch !== savedEpoch ? "dirty" : remote ? "saved" : "empty");
        if (epoch !== savedEpoch) queue();
      } catch (error) { publish("error", error.message); }
    };
    const markDirty = () => {
      stopped = false;
      epoch += 1;
      if (status !== "conflict") publish("dirty");
      queue();
    };
    const flush = () => {
      if (inFlight) return inFlight;
      if (!ready || stopped || status === "conflict" || epoch === savedEpoch) return Promise.resolve(false);
      if (timer !== null) { unschedule(timer); timer = null; }
      const capturedEpoch = epoch;
      publish("saving");
      inFlight = (async () => {
        try {
          const body = await capture();
          body.set("draft_revision", String(revision));
          const result = await transport("save", body);
          if (result.status === 409) {
            remote = result.payload?.draft || null;
            publish("conflict");
            return false;
          }
          if (!result.ok || !result.payload?.draft) throw new Error(result.payload?.error || "Не удалось сохранить черновик");
          remote = result.payload.draft;
          revision = remote.revision;
          savedEpoch = capturedEpoch;
          publish(epoch === savedEpoch ? "saved" : "dirty");
          return true;
        } catch (error) { publish("error", error.message); return false; }
        finally {
          inFlight = null;
          if (epoch !== savedEpoch && status !== "conflict" && status !== "error") queue();
        }
      })();
      return inFlight;
    };
    const useRemote = async () => {
      if (inFlight) await inFlight;
      if (remote) await apply(remote);
      else await apply({ values: {}, assets: [], revision: 0 });
      revision = remote?.revision || 0;
      savedEpoch = epoch;
      publish(remote ? "saved" : "empty");
    };
    const keepLocal = async () => {
      if (inFlight) await inFlight;
      revision = remote?.revision || 0;
      epoch += 1;
      publish("dirty");
      return flush();
    };
    const remove = async () => {
      if (inFlight) await inFlight;
      if (!revision || status === "conflict") return false;
      try {
        const result = await transport("delete", new URLSearchParams({ draft_revision: String(revision) }));
        if (result.status === 409) { remote = result.payload?.draft || null; publish("conflict"); return false; }
        if (!result.ok) throw new Error(result.payload?.error || "Не удалось удалить черновик");
        stopped = true;
        if (timer !== null) unschedule(timer);
        revision = 0;
        remote = null;
        savedEpoch = epoch;
        publish("empty");
        return true;
      } catch (error) { publish("error", error.message); return false; }
    };
    return { load, markDirty, flush, useRemote, keepLocal, remove, snapshot };
  };
  const bindUI = ({ document, window, transport, capture, apply, onSaved = () => {}, hasUnsavedFiles = () => false }) => {
    const bar = document.getElementById("generation-draft-bar");
    if (!bar) return null;
    const status = document.getElementById("generation-draft-status");
    const expiry = document.getElementById("generation-draft-expiry");
    const save = document.getElementById("generation-draft-save");
    const remote = document.getElementById("generation-draft-remote");
    const local = document.getElementById("generation-draft-local");
    const remove = document.getElementById("generation-draft-delete");
    const labels = {
      loading: "Загружаем...", empty: "Нет сохранённого черновика", dirty: "Есть несохранённые изменения",
      saving: "Сохраняем настройки и материалы...", saved: "Сохранено", error: "Не удалось сохранить",
      conflict: "Есть другая сохранённая версия. Выберите, какую оставить.",
    };
    const controller = createController({ transport, capture, apply, onState(state) {
      bar.hidden = false;
      bar.dataset.state = state.status;
      status.textContent = state.error || labels[state.status] || "";
      const missing = state.remote?.assets?.filter((asset) => !asset.available).length || 0;
      if (state.status === "saved" && missing) status.textContent = `Настройки сохранены. Недоступных материалов: ${missing}`;
      expiry.textContent = state.remote?.expires_at
        ? `Настройки до ${new Date(state.remote.expires_at).toLocaleDateString("ru-RU")}` : "";
      remote.hidden = local.hidden = state.status !== "conflict";
      remote.textContent = state.remote ? "Взять сохранённый" : "Очистить локальный";
      save.hidden = state.status === "conflict";
      save.disabled = state.status === "loading" || state.status === "saving";
      remove.disabled = !state.revision || ["loading", "saving", "conflict"].includes(state.status);
      if (["saved", "dirty"].includes(state.status) && state.remote) onSaved(state.remote);
    }});
    const reportError = (error) => { status.textContent = error.message || "Не удалось выполнить действие"; };
    save.addEventListener("click", async () => {
      if (!controller.snapshot().ready) await controller.load({ preserveLocal: true });
      controller.markDirty();
      await controller.flush();
    });
    remote.addEventListener("click", () => controller.useRemote().catch(reportError));
    local.addEventListener("click", () => controller.keepLocal().catch(reportError));
    remove.addEventListener("click", () => {
      if (window.confirm("Удалить сохранённый черновик? Текущая форма и результаты генераций останутся.")) controller.remove().catch(reportError);
    });
    window.addEventListener("online", () => {
      if (controller.snapshot().ready) controller.flush();
      else controller.load({ preserveLocal: true });
    });
    window.addEventListener("beforeunload", (event) => {
      if (!controller.snapshot().dirty && !hasUnsavedFiles()) return;
      event.preventDefault();
      event.returnValue = "";
    });
    document.addEventListener("visibilitychange", () => {
      if (document.visibilityState === "hidden") controller.flush();
    });
    return controller;
  };
  return { createController, bindUI };
});
