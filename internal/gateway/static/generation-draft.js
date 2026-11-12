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
    let replacing = false;
    const snapshot = () => ({ revision, dirty: epoch !== savedEpoch, ready, status, remote, replacing });
    const publish = (next, error = "") => { status = next; onState({ ...snapshot(), error }); };
    const queue = () => {
      if (timer !== null) unschedule(timer);
      if (ready && !stopped && !replacing && status !== "conflict") timer = schedule(() => { timer = null; flush().catch(() => {}); }, delay);
    };
    const load = async ({ preserveLocal = false } = {}) => {
      if (inFlight) await inFlight.catch(() => {});
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
    const settle = async () => {
      if (!ready || status === "conflict") return false;
      // A save already in flight may not contain the most recent edit.
      while (inFlight || epoch !== savedEpoch) {
        if (!await flush()) return false;
      }
      return true;
    };
    const replace = async operation => {
      if (replacing || !await settle() || replacing) return false;
      const capturedEpoch = epoch, expectedRevision = revision;
      if (timer !== null) { unschedule(timer); timer = null; }
      replacing = true;
      publish("saving");
      inFlight = (async () => {
        try {
          const next = await operation(expectedRevision);
          if (!next?.revision || !next.values || !Array.isArray(next.assets)) throw new Error("Не удалось подтвердить новую версию черновика. Обновите состояние.");
          remote = { ...next, fresh: true };
          revision = remote.revision;
          replacing = false;
          if (epoch !== capturedEpoch) { publish("conflict"); return false; }
          try { await apply(remote); }
          catch (error) { publish("conflict", error.message); return false; }
          savedEpoch = epoch;
          publish("saved");
          return true;
        } catch (error) {
          replacing = false;
          publish("error", error.message);
          throw error;
        } finally { replacing = false; inFlight = null; }
      })();
      return inFlight;
    };
    return { load, markDirty, flush, settle, replace, useRemote, keepLocal, remove, snapshot };
  };
  const bindUI = ({ document, window, feedback, transport, capture, apply, onSaved = () => {}, hasUnsavedFiles = () => false, onNavigationBlocked = () => {}, schedule, unschedule }) => {
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
    const controller = createController({ transport, capture, apply, schedule, unschedule, onState(state) {
      bar.hidden = false;
      feedback.present(bar, state.status);
      feedback.text(status, state.error || labels[state.status] || "");
      const missing = state.remote?.assets?.filter((asset) => !asset.available).length || 0;
      if (state.status === "saved" && missing) feedback.text(status, `Настройки сохранены. Недоступных материалов: ${missing}`);
      expiry.textContent = state.remote?.expires_at
        ? `Настройки до ${new Date(state.remote.expires_at).toLocaleDateString("ru-RU")}` : "";
      remote.hidden = local.hidden = state.status !== "conflict";
      feedback.text(remote, state.remote ? "Взять сохранённый" : "Очистить локальный");
      save.hidden = state.status === "conflict";
      feedback.button(save, { busy: ["loading", "saving"].includes(state.status), disabled: ["loading", "saving"].includes(state.status) });
      remove.disabled = !state.revision || ["loading", "saving", "conflict"].includes(state.status);
      if (["saved", "dirty"].includes(state.status) && state.remote) onSaved(state.remote);
    }});
    const reportError = (error) => {
      feedback.present(bar, controller.snapshot().status === "conflict" ? "conflict" : "error");
      feedback.text(status, error.message || "Не удалось выполнить действие");
    };
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
    let departureAccepted = false;
    window.addEventListener("beforeunload", (event) => {
      if (departureAccepted) return;
      if (!controller.snapshot().dirty && !controller.snapshot().replacing && !hasUnsavedFiles()) return;
      event.preventDefault();
      event.returnValue = "";
    });
    let pageActive = true;
    window.addEventListener("pagehide", () => { pageActive = false; });
    window.addEventListener("pageshow", () => { pageActive = true; departureAccepted = false; });
    document.addEventListener("visibilitychange", () => {
      // An accepted unload must not start another write in the departing page.
      if (pageActive && document.visibilityState === "hidden") controller.flush();
    });
    let navigating = false;
    document.addEventListener("click", async (event) => {
      if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
      const link = event.target.closest?.("a[href]");
      if (!link || link.hasAttribute("download") || (link.target && link.target !== "_self")) return;
      const destination = new URL(link.href, window.location.href);
      if (destination.origin !== window.location.origin || !["http:", "https:"].includes(destination.protocol)) return;
      if (destination.pathname === window.location.pathname && destination.search === window.location.search && destination.hash) return;
      if (!controller.snapshot().dirty && !controller.snapshot().replacing && !hasUnsavedFiles()) return;
      event.preventDefault();
      if (navigating) return;
      navigating = true;
      const leaveUnsaved = (message) => {
        if (!window.confirm(message + "\n\nПерейти без сохранения этих изменений? Уже сохранённый черновик останется.")) return;
        departureAccepted = true;
        pageActive = false;
        window.location.assign(destination.href);
      };
      try {
        if (!await controller.settle()) {
          leaveUnsaved(controller.snapshot().status === "conflict"
            ? "Есть другая версия черновика. Текущие изменения не сохранены."
            : "Не удалось сохранить последние изменения черновика.");
          return;
        }
        if (hasUnsavedFiles()) {
          leaveUnsaved("Не все выбранные файлы загружены. При переходе их потребуется выбрать повторно.");
          return;
        }
        window.location.assign(destination.href);
      } catch (error) { leaveUnsaved("Не удалось сохранить черновик перед переходом."); }
      finally { navigating = false; }
    });
    return controller;
  };
  /**
   * Draft HTTP contract. This transport never retries a mutation automatically.
   * @param {{fetcher?: typeof fetch, csrf: () => string, timeoutSignal?: (ms: number) => AbortSignal}} options
   * @returns {(operation: string, body?: URLSearchParams) => Promise<{ok: boolean, status: number, payload: Object}>}
   */
  const createTransport = ({ fetcher = fetch, csrf, timeoutSignal = ms => AbortSignal.timeout(ms) }) => async (operation, body) => {
    if (!["load", "save", "delete"].includes(operation)) throw new Error("Unknown draft operation");
    if (body) body.set("csrf", csrf() || "");
    const response = await fetcher(operation === "delete" ? "/generate/draft/delete" : "/generate/draft", {
      method: operation === "load" ? "GET" : "POST", body, credentials: "same-origin", cache: "no-store",
      headers: body ? { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8" } : {},
      signal: timeoutSignal(20000),
    });
    return { ok: response.ok, status: response.status, payload: await response.json() };
  };
  /**
   * Studio draft orchestration: validates before synchronous presentation,
   * suppresses restore-generated dirty events and shares the upload controller.
   * Model parameter serialization is delegated to the supplied payload adapter.
   * @param {Object} options
   * @param {Object} options.parameters Adapter with draft(state) and resolveSelection(values).
   * @param {() => Object} options.capture Current payload state, without side effects.
   * @param {(draft: Object) => void} options.apply Synchronous component presentation only.
   * @param {Object} options.files pending() returns all active/inactive source entries;
   * inFlight() returns transport count; current(entry) checks selection identity.
   * Entries contain field, source, upload() and optional state element.
   * @returns {Object} Versioned controller API plus apply(draft) and readonly restoring.
   */
  const createSession = ({ document, window, root, form, store, advanced, feedback, parameters, capture, apply: render, files, onSaved, onNavigationBlocked, transport, schedule, unschedule }) => {
    const attempts = new Map();
    let restoring = 0;
    let advancedOpen = Boolean(advanced?.open);
    let controller = null;
    const markDirty = () => { if (!restoring) controller?.markDirty(); };
    const apply = draft => {
      parameters.resolveSelection(draft.values || {});
      restoring++;
      try { render({ ...draft, fresh: true }); }
      finally { restoring--; advancedOpen = Boolean(advanced?.open); }
    };
    const captureDraft = () => {
      const pending = files.pending();
      // Save text first, then bind one newly uploaded material in the next save.
      const next = !files.inFlight() && pending.find(item => attempts.get(item.field) !== item.source);
      if (next) {
        attempts.set(next.field, next.source);
        if (next.state) next.state.textContent = "Сохраняем материал...";
        next.upload().catch(error => {
          if (next.state && files.current(next)) next.state.textContent = error.message || "Материал не загружен";
        }).finally(markDirty);
      }
      const state = capture();
      return parameters.draft({ ...state, media: { ...state.media, pending } });
    };
    controller = bindUI({
      document, window, feedback, capture: captureDraft, apply,
      transport: transport || createTransport({ csrf: () => form.elements.csrf?.value || "" }),
      hasUnsavedFiles: () => files.pending().length > 0,
      onSaved, onNavigationBlocked, schedule, unschedule,
    });
    form.addEventListener("input", markDirty);
    form.addEventListener("change", markDirty);
    advanced?.addEventListener("toggle", () => {
      if (advancedOpen === advanced.open) return;
      advancedOpen = advanced.open;
      markDirty();
    });
    root.addEventListener("click", event => {
      if (event.target.closest?.(".scenario-choice, .generation-workflow-choice, [data-remove-image], #minimax-audio-remove, #minimax-video-remove, #prompt-assistant-apply, #prompt-assistant-keep")) markDirty();
    });
    store?.subscribe?.("assistant:change", ({ previous, current }) => {
      if (["original", "suggestion", "action", "correlationID"].some(key => previous?.[key] !== current?.[key])) markDirty();
    });
    store?.subscribe?.("wizard:change", ({ previous, current }) => {
      if (["step", "scenarioID", "workflowID"].some(key => previous?.[key] !== current?.[key])) markDirty();
    });
    const settleFiles = async () => {
      if (!controller?.snapshot().ready || controller.snapshot().status === "conflict") return false;
      const pending = files.pending();
      for (const item of pending) {
        if (!files.current(item)) throw new Error("Исходники изменились. Проверьте их перед продолжением.");
        await item.upload();
        markDirty();
      }
      markDirty();
      if (!await controller.settle()) return false;
      if (files.pending().length || files.inFlight()) throw new Error("Материалы ещё загружаются или изменились. Дождитесь загрузки и повторите действие.");
      return true;
    };
    return { ...controller, apply, markDirty, settleFiles, load: options => controller?.load(options), get restoring() { return restoring > 0; } };
  };
  return { createController, bindUI, createTransport, createSession };
});
