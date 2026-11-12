(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  else { root.AIGatewayResources = api; api.mount(root.document, root); }
})(typeof globalThis === "object" ? globalThis : this, function () {
  "use strict";

  function create(options, environment = globalThis) {
    let active = false, expired = false, epoch = 0, pending = null, controller = null;
    let failures = 0, retryAt = 0, deferred = false;
    const poll = options.poll(() => refresh(false), 30000, 120000);
    function refresh(manual = true) {
      if (!active || expired) return Promise.resolve();
      if (pending) return pending;
      if (!manual && environment.Date.now() < retryAt) return Promise.resolve();
      const current = epoch;
      controller = new environment.AbortController();
      const signal = controller.signal;
      const timer = environment.setTimeout(() => controller?.abort(), 25000);
      options.state("loading");
      pending = (async () => {
        try {
          const response = await options.read(signal);
          if (!active || current !== epoch) return;
          if (response.redirected || response.status === 401 || response.status === 403) {
            expired = true; poll.stop(); options.state("access"); return;
          }
          if (!response.ok) throw new Error("unavailable");
          const value = await response.json();
          if (!active || current !== epoch) return;
          if (signal.aborted) throw new Error("timeout");
          if (options.blocked()) { deferred = true; options.state("deferred"); return; }
          options.apply(value);
          failures = 0; retryAt = 0; deferred = false;
          options.state("ready");
        } catch (_) {
          if (active && current === epoch) {
            failures = Math.min(failures + 1, 3);
            retryAt = environment.Date.now() + Math.min(120000, 15000 * 2 ** failures);
            options.state("offline");
          }
        } finally {
          environment.clearTimeout(timer);
          controller = null; pending = null;
          if (active && !expired && current !== epoch) refresh();
        }
      })();
      return pending;
    }
    return {
      refresh,
      resume() { if (active || expired) return; active = true; poll.start(); return refresh(); },
      suspend() { active = false; epoch++; poll.stop(); controller?.abort(); },
      unblock() { if (deferred && !options.blocked()) return refresh(); },
    };
  }

  function mount(document, environment) {
    mountMiningFiles(document, environment);
    const page = document.querySelector("[data-resource-page]");
    if (!page || !environment.AIGatewayLiveEvents) return;
    const refresh = page.querySelector("[data-resource-refresh]");
    const banner = page.querySelector("[data-resource-error]");
    const message = page.querySelector("[data-resource-error-message]");
    const login = page.querySelector("[data-resource-login]");
    const reload = page.querySelector("[data-resource-reload]");
    let changed = false, accepted = false, accessLost = false;
    const lockActions = () => page.querySelectorAll("[data-miner-action]").forEach(button => { button.disabled = true; });
    const parse = (html, selector) => {
      if (typeof html !== "string" || html.length > 2 * 1024 * 1024) throw new Error("invalid_html");
      const template = document.createElement("template"); template.innerHTML = html;
      const next = template.content.firstElementChild;
      if (template.content.children.length !== 1 || !next?.matches(selector) || next.querySelector("script,iframe,object,form,input,button")) throw new Error("invalid_fragment");
      return next;
    };
    const owner = create({
      poll: (run, interval, hiddenInterval) => environment.AIGatewayLiveEvents.poll(run, interval, hiddenInterval, environment),
      read: signal => environment.fetch(page.dataset.resourceEndpoint || "/admin/resources/overview?service=" + encodeURIComponent(page.dataset.service), { signal, cache: "no-store", credentials: "same-origin", headers: { Accept: "application/json" } }),
      blocked: () => accepted || !!document.querySelector(".confirm-dialog[open]"),
      apply(value) {
        const next = parse(value?.snapshot_html, "[data-resource-snapshot]");
        const gpu = page.dataset.service === "mining" ? parse(value?.gpu_html, "[data-resource-gpu]") : null;
        if (typeof value.profile_version !== "string" || !value.profiles || typeof value.profiles !== "object" || Array.isArray(value.profiles)) throw new Error("invalid_profiles");
        const states = new Map();
        for (const [id, state] of Object.entries(value.profiles)) {
          if (!/^[1-9][0-9]{0,18}$/.test(id) || !state || ["running", "start", "stop", "update", "change"].some(key => typeof state[key] !== "boolean")) throw new Error("invalid_state");
          states.set(id, { ...state, node: parse(state.html, `[data-miner-state="${id}"]`) });
        }
        const rows = [...page.querySelectorAll("[data-miner]")];
        changed = changed || value.profile_version !== page.dataset.profileVersion || states.size !== rows.length || rows.some(row => !states.has(row.dataset.miner));
        // Forms and their submitters never leave the document during a read.
        const focused = document.activeElement;
        const focusHref = focused?.getAttribute("href");
        const restoreFocus = focusHref && (page.querySelector("[data-resource-snapshot]").contains(focused) || page.querySelector("[data-resource-gpu]")?.contains(focused));
        page.querySelector("[data-resource-snapshot]").replaceWith(next);
        if (gpu) page.querySelector("[data-resource-gpu]").replaceWith(gpu);
        if (restoreFocus) ([...page.querySelectorAll("a[href]")].find(link => link.getAttribute("href") === focusHref) || refresh).focus({ preventScroll: true });
        if (changed) { lockActions(); return; }
        for (const row of rows) {
          const state = states.get(row.dataset.miner);
          row.querySelector("[data-miner-state]").replaceWith(state.node);
          row.querySelectorAll("[data-miner-action]").forEach(button => {
            const key = button.dataset.minerAction;
            if (key === "start") button.hidden = state.running;
            if (key === "stop" || key === "restart") button.hidden = !state.running;
            if (key === "restart") { button.disabled = state.restart !== true; return; }
            button.disabled = button.hasAttribute("data-config-disabled") || (key in state ? !state[key] : false);
          });
        }
        if (focused?.matches("[data-miner-action]") && (focused.hidden || focused.disabled)) refresh.focus({ preventScroll: true });
      },
      state(state) {
        page.dataset.resourceState = state;
        refresh.setAttribute("aria-busy", String(state === "loading"));
        page.querySelector("[data-resource-live]").textContent = { loading: "Обновляется", ready: "Снимок обновлён", offline: "Нет обновлений", access: "Доступ завершён", deferred: "Обновление после подтверждения" }[state];
        if (state === "loading" || state === "deferred") return;
        banner.hidden = state === "ready" && !changed;
        login.hidden = state !== "access";
        reload.hidden = !changed || state === "access";
        message.textContent = state === "access" ? "Сеанс завершён или доступ отозван." : changed ? "Состав или настройки профилей изменились. Перезагрузите страницу перед командой." : "Не удалось обновить состояние. Показаны последние полученные данные; команды профилей временно недоступны.";
        if (state === "offline") lockActions();
        if (state === "access") {
          accessLost = true;
          const dialog = document.querySelector(".confirm-dialog[open]");
          if (dialog) { dialog.querySelectorAll(".confirm-dialog__body p").forEach(node => { node.textContent = ""; }); dialog.close(); }
          page.querySelector("[data-resource-private]").replaceChildren();
          page.querySelectorAll(".alert, .success-box").forEach(node => node.remove());
          login.focus({ preventScroll: true });
        }
      },
    }, environment);
    refresh.addEventListener("click", event => { if (!accessLost) { event.preventDefault(); owner.refresh(); } });
    document.addEventListener("submit", event => {
      if (!event.defaultPrevented && page.contains(event.target) && event.target.method === "post") { accepted = true; owner.suspend(); }
    });
    document.addEventListener("close", () => { if (!accepted) owner.unblock(); }, true);
    environment.addEventListener("pagehide", () => owner.suspend());
    environment.addEventListener("pageshow", () => { accepted = false; if (!document.hidden) owner.resume(); });
    document.addEventListener("visibilitychange", () => { if (document.hidden) owner.suspend(); else if (!accepted) owner.resume(); });
    if (!document.hidden) owner.resume();
    return owner;
  }
  function mountMiningFiles(document, environment) {
    document.addEventListener("click", event => {
      const button=event.target.closest("[data-mining-pick]");
      const form=document.querySelector("[data-mining-profile-form]");
      if (!button || !form) return;
      event.preventDefault();
      form.elements.script_path.value=button.dataset.miningPick;
      form.elements.process_name.value=button.dataset.process;
      if (!form.elements.name.value) form.elements.name.value=button.dataset.name.slice(0,80);
      form.scrollIntoView({behavior:"smooth",block:"start"});
      form.elements.name.focus({preventScroll:true});
    });
    const browser = document.querySelector("[data-mining-browser]");
    if (browser) {
      const folder = browser.querySelector("[data-mining-folder]");
      const favorites = browser.querySelector("[data-mining-favorites-only]");
      const search = browser.querySelector("[data-mining-search]");
      const rows = [...browser.querySelectorAll("[data-mining-file]")];
      const error = browser.querySelector("[data-mining-file-error]");
      let favoritePending = false;
      const update = (syncURL = false) => {
        let visible = 0;
        const term = search.value.trim().toLocaleLowerCase();
        for (const row of rows) {
          row.hidden = !(favorites.checked ? row.dataset.favorite === "true" : row.dataset.folder === folder.value) || !row.querySelector(".mining-file-identity").textContent.toLocaleLowerCase().includes(term);
          if (!row.hidden) visible++;
          const form = row.querySelector("form");
          form.elements.folder.value = folder.value;
          form.elements.favorites.value = favorites.checked ? "1" : "";
        }
        folder.disabled = favorites.checked;
        browser.querySelector("[data-mining-files-empty]").hidden = visible > 0;
        browser.querySelector("[data-mining-file-count]").textContent = `${visible} файлов`;
        if (syncURL) {
          const url = new URL(environment.location.href);
          if (folder.value) url.searchParams.set("folder", folder.value);
          else url.searchParams.delete("folder");
          if (favorites.checked) url.searchParams.set("favorites", "1");
          else url.searchParams.delete("favorites");
          environment.history.replaceState(null, "", url);
        }
      };
      folder.addEventListener("change", () => update(true));
      favorites.addEventListener("change", () => update(true));
      search.addEventListener("input", () => update());
      browser.querySelectorAll("[data-mining-favorite-form]").forEach(form => form.addEventListener("submit", async event => {
        event.preventDefault();
        const button = form.querySelector("button");
        if (favoritePending || button.disabled) return;
        favoritePending = true;
        const buttons = [...browser.querySelectorAll("[data-mining-favorite-form] button")];
        buttons.forEach(item => { item.disabled = true; });
        const controller = new environment.AbortController();
        const timeout = environment.setTimeout(() => controller.abort(), 10000);
        error.hidden = true;
        try {
          const response = await environment.fetch(form.action, { signal: controller.signal, method: "POST", credentials: "same-origin", cache: "no-store", headers: { Accept: "application/json" }, body: new environment.FormData(form) });
          if (!response.ok) throw new Error("favorite_failed");
          const result = await response.json();
          if (typeof result.favorite !== "boolean" || !Number.isInteger(result.count)) throw new Error("invalid_favorite");
          if (!form.isConnected) return;
          const row = form.closest("[data-mining-file]");
          row.dataset.favorite = String(result.favorite);
          form.elements.enabled.value = String(!result.favorite);
          button.setAttribute("aria-pressed", String(result.favorite));
          button.title = result.favorite ? "Убрать из избранного" : "Добавить в избранное";
          button.setAttribute("aria-label", `${button.title}: ${row.querySelector("strong").textContent}`);
          browser.querySelector("[data-mining-favorite-count]").textContent = String(result.count);
          update();
        } catch (_) {
          if (error.isConnected) { error.textContent = "Не удалось подтвердить сохранение избранного. Повторите попытку."; error.hidden = false; }
        } finally {
          environment.clearTimeout(timeout);
          favoritePending = false;
          buttons.forEach(item => { item.disabled = false; });
        }
      }));
      update();
    }
    const picker = document.querySelector("[data-mining-script-picker]");
    if (picker) {
      const form = picker.closest("form");
      picker.addEventListener("change", () => {
        if (!picker.value) return;
        form.elements.script_path.value = picker.value;
        const option = picker.selectedOptions[0];
        const names = new Set(Array.from(form.querySelectorAll("#mining-executables option"))
          .filter(item => item.dataset.folder === option.dataset.folder).map(item => item.value));
        form.elements.process_name.value = names.size === 1 ? Array.from(names)[0] : "";
        if (!form.elements.name.value) form.elements.name.value = option.textContent.trim().slice(0, 80);
      });
    }
    const editor = document.querySelector("[data-mining-editor]");
    if (editor) {
      const initial = editor.elements.content.value;
      let submitted = false;
      document.addEventListener("submit", event => { if (event.target === editor && !event.defaultPrevented) submitted = true; });
      environment.addEventListener("beforeunload", event => {
        if (!submitted && editor.elements.content.value !== initial) { event.preventDefault(); event.returnValue = ""; }
      });
      environment.addEventListener("pageshow", () => { submitted = false; });
    }
  }
  return { create, mount, mountMiningFiles };
});
