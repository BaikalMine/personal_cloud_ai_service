(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  else {
    root.AIGatewayOperations = api;
    api.mount(root.document, root);
  }
})(typeof globalThis === "object" ? globalThis : this, function () {
  "use strict";

  function create(options, environment = globalThis) {
    let active = true;
    let expired = false;
    let generation = 0;
    let pending = null;
    let controller = null;
    let failures = 0;
    let retryAt = 0;
    const poll = options.poll(() => refresh(false), 10000, 60000);
    function refresh(manual = true) {
      if (!active || expired) return Promise.resolve();
      if (pending) return pending;
      if (!manual && environment.Date.now() < retryAt) return Promise.resolve();
      const current = generation;
      controller = new environment.AbortController();
      const signal = controller.signal;
      const timeout = environment.setTimeout(() => controller?.abort(), 25000);
      options.state("loading");
      pending = (async () => {
        try {
          const response = await options.read(signal);
          if (!active || current !== generation) return;
          if (response.redirected || response.status === 401 || response.status === 403) {
            expired = true; poll.stop(); options.state("access"); return;
          }
          if (!response.ok) throw new Error("overview_unavailable");
          const value = await response.json();
          if (!active || current !== generation) return;
          if (signal.aborted) throw new Error("overview_timeout");
          options.apply(value);
          failures = 0; retryAt = 0;
          options.state("ready");
        } catch (_) {
          if (active && current === generation) {
            failures = Math.min(failures + 1, 3);
            retryAt = environment.Date.now() + Math.min(60000, 10000 * 2 ** failures);
            options.state("offline");
          }
        } finally {
          environment.clearTimeout(timeout);
          controller = null; pending = null;
          // A bfcache return can precede the old fetch's abort acknowledgement.
          if (active && !expired && current !== generation) refresh();
        }
      })();
      return pending;
    }
    return {
      refresh,
      start() { poll.start(); return refresh(); },
      suspend() { active = false; generation++; controller?.abort(); poll.stop(); },
      resume() { if (active || expired) return; active = true; poll.start(); return refresh(); },
      visibility(hidden) {
        poll.stop();
        if (!active || expired) return;
        poll.start(); if (!hidden) return refresh();
      },
    };
  }

  function mount(document, environment) {
    const page = document.querySelector("[data-operations-page]");
    if (!page || !environment.AIGatewayLiveEvents) return;
    const button = page.querySelector("[data-ops-refresh]");
    const status = page.querySelector("[data-ops-live-status]");
    const banner = page.querySelector("[data-ops-error]");
    const login = page.querySelector("[data-ops-login]");
    page.querySelector("[data-ops-controls]").hidden = false;
    const apply = value => {
      const system = value?.system;
      if (typeof value?.snapshot_html !== "string" || value.snapshot_html.length > 2 * 1024 * 1024 ||
          !system || typeof system.agent !== "object" || !system.agent ||
          ["history", "generation_markers", "online_users", "dependencies", "workers"].some(key => system[key] != null && !Array.isArray(system[key]))) {
        throw new Error("invalid_overview");
      }
      const template = document.createElement("template");
      template.innerHTML = value.snapshot_html;
      const next = template.content.firstElementChild;
      if (template.content.children.length !== 1 || !next?.matches("[data-ops-snapshot]") || next.querySelector("script,iframe,object,form")) {
        throw new Error("invalid_snapshot");
      }
      const previous = page.querySelector("[data-ops-snapshot]");
      const focused = previous.contains(document.activeElement) ? document.activeElement : null;
      const focusKey = focused?.getAttribute("href");
      const disclosureKey = focused?.closest("[data-ops-disclosure]")?.dataset.opsDisclosure;
      previous.querySelectorAll("[data-ops-disclosure][open]").forEach(item => {
        next.querySelector(`[data-ops-disclosure="${environment.CSS.escape(item.dataset.opsDisclosure)}"]`)?.setAttribute("open", "");
      });
      previous.replaceWith(next);
      page.dispatchEvent(new environment.CustomEvent("operations:snapshot", { detail: system }));
      if (focused) {
        const replacement = focusKey ? [...next.querySelectorAll("a[href]")].find(link => link.getAttribute("href") === focusKey) :
          disclosureKey ? next.querySelector(`[data-ops-disclosure="${environment.CSS.escape(disclosureKey)}"] > summary`) : null;
        (replacement || button).focus({ preventScroll: true });
      }
    };
    const owner = create({
      poll: (run, interval, hiddenInterval) => environment.AIGatewayLiveEvents.poll(run, interval, hiddenInterval, environment),
      read: signal => environment.fetch("/admin/operations/overview", { signal, headers: { Accept: "application/json" }, cache: "no-store", credentials: "same-origin" }),
      apply,
      state(state) {
        const restoreAccessFocus = state === "access" && page.contains(document.activeElement);
        page.dataset.opsState = state;
        button.disabled = state === "access";
        button.setAttribute("aria-disabled", String(state === "loading" || state === "access"));
        button.setAttribute("aria-busy", String(state === "loading"));
        status.textContent = { loading: "Обновляется", ready: "Сводка обновлена", offline: "Нет обновлений", access: "Доступ завершён" }[state];
        if (state === "loading") return;
        banner.hidden = state === "ready";
        login.hidden = state !== "access";
        page.querySelector("[data-ops-error-message]").textContent = state === "access" ? "Сеанс завершён или доступ к управлению отозван." : "Не удалось обновить сводку. Показаны последние полученные данные.";
        if (state === "access") {
          page.querySelector("[data-ops-snapshot]").replaceChildren();
          const host = page.querySelector(".operations-host");
          host.replaceChildren(); host.hidden = true;
          page.dispatchEvent(new environment.CustomEvent("operations:access-lost"));
          if (restoreAccessFocus) login.focus({ preventScroll: true });
        }
      },
    }, environment);
    button.addEventListener("click", () => owner.refresh());
    environment.addEventListener("pagehide", () => owner.suspend());
    environment.addEventListener("pageshow", () => owner.resume());
    document.addEventListener("visibilitychange", () => owner.visibility(document.hidden));
    owner.start();
    return owner;
  }
  return { create, mount };
});
