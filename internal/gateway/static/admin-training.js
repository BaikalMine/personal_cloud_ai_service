(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  else { root.AIGatewayAdminTraining = api; api.mount(root.document, root); }
})(typeof globalThis === "object" ? globalThis : this, function () {
  "use strict";

  function create(options, env = globalThis) {
    let active = true, visible = true, expired = false, deferred = false, epoch = 0, pending = null, timer, failures = 0;
    const clear = () => { env.clearTimeout(timer); timer = undefined; };
    const schedule = () => {
      clear();
      if (active && visible && !expired) timer = env.setTimeout(() => refresh(), Math.min(120000, 30000 * 2 ** failures));
    };
    function refresh() {
      if (!active || !visible || expired) return Promise.resolve();
      if (pending) return pending.promise;
      if (options.blocked()) { deferred = true; options.state("deferred"); schedule(); return Promise.resolve(); }
      clear();
      const operation = { epoch, controller: new env.AbortController() };
      pending = operation;
      const timeout = env.setTimeout(() => operation.controller.abort(), 25000);
      options.state("loading");
      operation.promise = (async () => {
        try {
          const response = await options.read(operation.controller.signal);
          if (!active || !visible || operation.epoch !== epoch) return;
          if (response.redirected || [401, 403].includes(response.status)) {
            expired = true; options.state("access"); return;
          }
          if (!response.ok) throw new Error("training_unavailable");
          const value = await response.json();
          if (!active || !visible || operation.epoch !== epoch) return;
          if (operation.controller.signal.aborted) throw new Error("training_timeout");
          // Native confirmation owns the original form and submitter until it
          // closes. Never replace that DOM with an in-flight read's fragment.
          if (options.blocked()) { deferred = true; options.state("deferred"); return; }
          options.apply(value); deferred = false; failures = 0; options.state("ready");
        } catch (_) {
          if (active && visible && operation.epoch === epoch) { failures = Math.min(2, failures + 1); options.state("offline"); }
        } finally {
          env.clearTimeout(timeout);
          if (pending === operation) {
            pending = null;
            if (active && visible && !expired && operation.epoch !== epoch) refresh(); else schedule();
          }
        }
      })();
      return operation.promise;
    }
    const stop = () => { clear(); epoch++; pending?.controller.abort(); };
    return {
      refresh,
      unblock() { if (deferred) return refresh(); },
      suspend() { active = false; stop(); },
      resume() { if (active || expired) return; active = true; refresh(); },
      visibility(hidden) { visible = !hidden; if (hidden) stop(); else refresh(); },
      deny() { expired = true; stop(); options.state("access"); },
    };
  }

  function mount(document, env) {
    const page = document.querySelector("[data-training-page]");
    if (!page || !env.AIGatewayTaskGPU) return;
    const compact = env.matchMedia("(max-width: 760px)");
    if (compact.matches) page.querySelector("[data-training-filters]").open = false;
    compact.addEventListener("change", () => { if (!compact.matches) page.querySelector("[data-training-filters]").open = true; });
    const refreshButton = page.querySelector("[data-training-refresh]"), live = page.querySelector("[data-training-live]");
    const banner = page.querySelector("[data-training-error]"), message = page.querySelector("[data-training-error-message]");
    const login = page.querySelector("[data-training-login]");
    let navigating = false, denied = false, owner, rows = new Map();
    const text = (node, value) => { if (node.textContent !== value) node.textContent = value; };
    const gpu = env.AIGatewayTaskGPU.createListMonitor({
      async fetchSnapshots(sources, signal) {
        const query = new env.URLSearchParams();
        sources.forEach(source => query.append("task", source.kind + ":" + source.id));
        const abort = new env.AbortController(), cancel = () => abort.abort();
        signal.addEventListener("abort", cancel, { once: true });
        if (signal.aborted) cancel();
        const timer = env.setTimeout(cancel, 8000);
        try {
          const response = await env.fetch("/admin/lora-training/gpu?" + query, { signal: abort.signal, credentials: "same-origin", cache: "no-store", headers: { Accept: "application/json" } });
          if (response.redirected || !response.ok) throw Object.assign(new Error("gpu_read"), { status: response.redirected ? 401 : response.status });
          return await response.json();
        } finally { env.clearTimeout(timer); signal.removeEventListener("abort", cancel); }
      },
      onChange(values) {
        for (const value of values) {
          const node = rows.get(value.key)?.querySelector("[data-training-gpu]");
          if (!node) continue;
          node.hidden = value.hidden; node.dataset.tone = value.tone;
          text(node, value.message + (value.position ? " · позиция " + value.position : ""));
          node.title = value.timestamp ? "Снимок GPU: " + new env.Date(value.timestamp).toLocaleString("ru-RU") : "";
        }
      },
      onAccessError() { owner?.deny(); },
    });
    const selectRows = () => {
      rows = new Map([...page.querySelectorAll("[data-training-id]")].map(row => ["lora_training:" + row.dataset.trainingId, row]));
      gpu.select([...rows.values()].map(row => ({ kind: "lora_training", id: row.dataset.trainingId, terminal: row.dataset.terminal === "true" })));
    };
    const blocked = () => navigating || Boolean(document.querySelector(".confirm-dialog[open]")) || page.querySelector("[data-training-snapshot]").contains(document.activeElement);
    owner = create({
      blocked,
      read: signal => {
        const url = new env.URL(refreshButton.href);
        return env.fetch("/admin/lora-training/overview" + url.search, { signal, credentials: "same-origin", cache: "no-store", headers: { Accept: "application/json" } });
      },
      apply(value) {
        if (typeof value?.snapshot_html !== "string" || value.snapshot_html.length > 2 * 1024 * 1024) throw new Error("invalid_training_snapshot");
        const template = document.createElement("template"); template.innerHTML = value.snapshot_html;
        const next = template.content.firstElementChild;
        if (template.content.children.length !== 1 || !next?.matches("[data-training-snapshot]") || next.querySelector("script,iframe,object")) throw new Error("invalid_training_fragment");
        const url = new env.URL(next.dataset.refreshUrl, env.location.origin);
        if (url.origin !== env.location.origin || url.pathname !== "/admin/lora-training") throw new Error("invalid_training_url");
        const previous = page.querySelector("[data-training-snapshot]");
        const open = new Set([...previous.querySelectorAll("[data-training-detail][open]")].map(node => node.dataset.trainingDetail));
        next.querySelectorAll("[data-training-detail]").forEach(node => { node.open = open.has(node.dataset.trainingDetail); });
        // Keep the independently owned GPU sample while replacing source rows.
        next.querySelectorAll("[data-training-id]").forEach(row => {
          const old = rows.get("lora_training:" + row.dataset.trainingId);
          if (old && old.dataset.terminal === row.dataset.terminal) row.querySelector("[data-training-gpu]").replaceWith(old.querySelector("[data-training-gpu]"));
        });
        previous.replaceWith(next); refreshButton.href = url.pathname + url.search;
        selectRows(); env.lucide?.createIcons();
      },
      state(state) {
        page.dataset.trainingState = state;
        refreshButton.setAttribute("aria-busy", String(state === "loading"));
        text(live, { loading: "Обновляется", ready: "Список обновлён", offline: "Нет обновлений", access: "Доступ завершён", deferred: "Обновление отложено" }[state]);
        if (["loading", "deferred"].includes(state)) return;
        banner.hidden = state === "ready"; login.hidden = state !== "access";
        text(message, state === "access" ? "Сеанс завершён или административный доступ отозван." : "Не удалось обновить список. Показан последний полученный снимок.");
        if (state === "access") {
          denied = true; gpu.clear(); gpu.setConnected(false);
          const focused = page.contains(document.activeElement) || Boolean(document.querySelector(".confirm-dialog[open]"));
          page.querySelector("[data-training-snapshot]").replaceChildren(); rows.clear();
          const confirmation = document.querySelector(".confirm-dialog");
          if (confirmation?.open) confirmation.close();
          const confirmationMessage = confirmation?.querySelector(".confirm-dialog__body p");
          if (confirmationMessage) text(confirmationMessage, "");
          page.querySelectorAll("form input, form select, form button").forEach(node => { node.disabled = true; });
          refreshButton.setAttribute("aria-disabled", "true");
          if (focused) login.focus({ preventScroll: true });
        }
      },
    }, env);
    refreshButton.addEventListener("click", event => { event.preventDefault(); if (!denied) { owner.refresh(); gpu.refresh({ force: true }); } });
    // app.js handles confirmation first. Only the accepted native POST suspends
    // reads; cancelling the dialog must never suppress future refreshes.
    document.addEventListener("submit", event => {
      if (event.target.matches("[data-training-action]") && !event.defaultPrevented) { navigating = true; owner.suspend(); gpu.setActive(false); }
    });
    page.addEventListener("focusout", () => env.setTimeout(() => { if (!blocked() && !denied) owner.unblock(); }, 0));
    new env.MutationObserver(records => {
      if (records.some(record => record.target.matches(".confirm-dialog") && !record.target.open) && !navigating && !denied) owner.unblock();
    }).observe(document.body, { attributes: true, subtree: true, attributeFilter: ["open"] });
    const visibility = () => { owner.visibility(document.hidden); gpu.setActive(!document.hidden && !navigating && !denied); };
    document.addEventListener("visibilitychange", visibility);
    env.addEventListener("pagehide", () => { owner.suspend(); gpu.setActive(false); });
    env.addEventListener("pageshow", () => { navigating = false; owner.resume(); visibility(); });
    selectRows(); gpu.setConnected(true); visibility(); owner.refresh();
    return owner;
  }
  return { create, mount };
});
