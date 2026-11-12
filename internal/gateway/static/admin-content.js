(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  else { root.AIGatewayAdminContent = api; api.mount(root.document, root); }
})(typeof globalThis === "object" ? globalThis : this, function () {
  "use strict";

  function create(options, env = globalThis) {
    let active = false, expired = false, cycle = 0, pending = null, controller = null;
    let queued = false, failures = 0, retry = 0;
    const clearRetry = () => { env.clearTimeout(retry); retry = 0; };
    const poll = options.poll(() => refresh(false), 30000, 120000);
    const stream = options.stream({
      refresh: () => refresh(false),
      disconnected: () => { if (active && !expired) options.state("offline"); },
    });
    function refresh(manual = true) {
      if (!active || expired) return Promise.resolve();
      if (pending) { queued = true; return pending; }
      if (retry && !manual) return Promise.resolve();
      clearRetry();
      const current = cycle;
      controller = new env.AbortController();
      const signal = controller.signal;
      const timeout = env.setTimeout(() => controller?.abort(), 25000);
      queued = false;
      options.state("loading");
      pending = (async () => {
        try {
          const response = await options.read(signal);
          if (!active || current !== cycle) return;
          if (response.redirected || response.status === 401 || response.status === 403) {
            expired = true; stream.close(); poll.stop(); clearRetry(); options.state("access"); return;
          }
          if (!response.ok) throw new Error("content_unavailable");
          const html = await response.text();
          if (!active || current !== cycle) return;
          if (signal.aborted) throw new Error("content_timeout");
          options.apply(html);
          failures = 0; options.state("ready");
        } catch (_) {
          if (!active || current !== cycle) return;
          failures = Math.min(5, failures + 1);
          options.state("offline");
        } finally {
          env.clearTimeout(timeout); controller = null; pending = null;
          if (!active || expired) return;
          // A resumed page waits for the old fetch to settle before reading again.
          if (current !== cycle) { refresh(); return; }
          if (failures) {
            retry = env.setTimeout(() => { retry = 0; refresh(); }, Math.min(30000, 1800 * 2 ** (failures - 1)));
          } else if (queued) {
            retry = env.setTimeout(() => { retry = 0; refresh(); }, 100);
          }
        }
      })();
      return pending;
    }
    return {
      refresh,
      resume() { if (active || expired) return; active = true; refresh(); stream.connect(); poll.start(); },
      suspend() { active = false; cycle++; queued = false; clearRetry(); controller?.abort(); stream.close(); poll.stop(); },
    };
  }

  function mount(document, env) {
    const page = document.querySelector("[data-admin-content-page]");
    const gallery = page?.querySelector("[data-admin-content-gallery]");
    const dialog = document.getElementById("content-detail-dialog");
    const body = document.getElementById("content-detail-body");
    if (!page || !gallery || !dialog || !body || !env.AIGatewayLiveEvents) return;
    const refreshButton = page.querySelector("[data-content-refresh]");
    const label = page.querySelector("[data-content-live-label]");
    const indicator = page.querySelector("[data-content-live-indicator]");
    const closeButton = dialog.querySelector(".content-detail-toolbar [data-content-detail-close]");
    const copyStatus = dialog.querySelector("[data-content-copy-status]");
    const trap = env.AIGatewayDialogFocus.createFocusTrap({ root: dialog, documentRef: document });
    let revision = Number(page.dataset.contentRevision) || 0;
    let taskKey = "", tabKey = "result", trigger = null, triggerKind = "", active = !document.hidden, expired = false;
    let lastRead = "";
    const retryTimers = new Set();
    const icons = () => env.lucide?.createIcons({ attrs: { "aria-hidden": "true", width: 18, height: 18 } });
    const cards = root => [...root.querySelectorAll("[data-content-task-key]")];
    const cardFor = key => cards(gallery).find(card => card.dataset.contentTaskKey === key);
    const detailFor = (root, key) => [...root.querySelectorAll("template[data-content-task-detail-key]")].find(item => item.dataset.contentTaskDetailKey === key);
    const allMediaVisible = () => document.body.classList.contains("show-sensitive-content");
    const pauseMedia = root => root.querySelectorAll("video").forEach(video => video.pause());
    const clearImageRetries = () => { retryTimers.forEach(timer => env.clearTimeout(timer)); retryTimers.clear(); };
    const bindImages = root => root.querySelectorAll("img").forEach(image => {
      if (image.dataset.contentRetryBound) return;
      image.dataset.contentRetryBound = "true";
      let attempts = 0;
      image.addEventListener("error", () => {
        if (!active || expired || !image.isConnected || attempts >= 2) return;
        const src = image.getAttribute("src");
        if (!src) return;
        const url = new URL(src, env.location.origin);
        url.searchParams.set("retry", String(++attempts) + "-" + env.Date.now());
        const timer = env.setTimeout(() => {
          retryTimers.delete(timer);
          if (active && !expired && image.isConnected) image.src = url.href;
        }, attempts * 300);
        retryTimers.add(timer);
      });
    });
    function close({ restore = true } = {}) {
      if (dialog.hidden) return;
      pauseMedia(body); trap.deactivate({ restore: false });
      dialog.hidden = true; body.replaceChildren(); copyStatus.textContent = "";
      document.body.classList.remove("content-detail-open");
      page.inert = false;
      if (restore) (cardFor(taskKey)?.querySelector(triggerKind === "title" ? ".content-task-title" : ".content-gallery-preview") || refreshButton).focus({ preventScroll: true });
      taskKey = ""; trigger = null;
    }
    function selectTab(key, focus = false) {
      const tabs = [...body.querySelectorAll("[data-content-tab]")];
      const selected = tabs.find(tab => tab.dataset.contentTab === key) || tabs[0];
      if (!selected) return;
      tabKey = selected.dataset.contentTab;
      for (const tab of tabs) {
        const chosen = tab === selected;
        tab.setAttribute("aria-selected", String(chosen)); tab.tabIndex = chosen ? 0 : -1;
      }
      body.querySelectorAll("[data-content-pane]").forEach(pane => {
        pane.hidden = pane.dataset.contentPane !== tabKey;
        if (pane.hidden) pauseMedia(pane);
      });
      if (focus) selected.focus({ preventScroll: true });
    }
    function render(nextTrigger, updating = false) {
      const key = nextTrigger.dataset.contentTaskOpen;
      const template = detailFor(gallery, key);
      if (!template) return;
      const old = body.querySelector(".content-detail");
      const next = template.content.cloneNode(true);
      const nextArticle = next.querySelector(".content-detail");
      const focused = updating && body.contains(document.activeElement) ? document.activeElement : null;
      const focusID = focused?.id;
      const focusCopy = focused?.dataset.contentCopy;
      const focusHref = focused?.getAttribute("href");
      const focusReveal = focused?.hasAttribute("data-content-reveal");
      const scroll = updating ? body.scrollTop : 0;
      const preScroll = new Map([...body.querySelectorAll("pre[id]")].map(pre => [pre.id, [pre.scrollTop, pre.scrollLeft]]));
      const playing = updating ? [...body.querySelectorAll("video")].filter(video => !video.paused) : [];
      // Media identity, including its sensitive classification, owns reveal/playback.
      if (updating && old?.dataset.contentMediaSignature === nextArticle?.dataset.contentMediaSignature) {
        const oldMedia = old.querySelector("[data-content-result-media]");
        const nextMedia = next.querySelector("[data-content-result-media]");
        if (oldMedia && nextMedia) nextMedia.replaceWith(oldMedia);
      } else { pauseMedia(body); }
      body.replaceChildren(next);
      if (nextTrigger.closest(".sensitive-media")?.classList.contains("is-revealed")) {
        body.querySelectorAll(".sensitive-media").forEach(media => media.classList.add("is-revealed"));
      }
      if (!updating) triggerKind = nextTrigger.classList.contains("content-task-title") ? "title" : "preview";
      taskKey = key; trigger = nextTrigger;
      dialog.hidden = false; page.inert = true; document.body.classList.add("content-detail-open");
      selectTab(updating ? tabKey : "result");
      icons(); bindImages(body);
      for (const pre of body.querySelectorAll("pre[id]")) {
        const position = updating && preScroll.get(pre.id);
        if (position) { pre.scrollTop = position[0]; pre.scrollLeft = position[1]; }
      }
      if (!updating) trap.activate({ trigger, initialFocus: closeButton, onEscape: () => close() });
      else if (focused) {
        const replacement = focused.isConnected ? focused : [...body.querySelectorAll("button,a,[tabindex]")].find(item =>
          (focusID && item.id === focusID) || (focusCopy && item.dataset.contentCopy === focusCopy) ||
          (focusHref && item.getAttribute("href") === focusHref) || (focusReveal && item.hasAttribute("data-content-reveal")));
        (replacement || body.querySelector('[data-content-tab][aria-selected="true"]') || closeButton).focus({ preventScroll: true });
      }
      body.scrollTop = scroll;
      for (const video of playing) {
        const mayPlay = () => video.isConnected && body.contains(video) && !dialog.hidden && taskKey === key &&
          active && !expired && !document.hidden && tabKey === "result" &&
          (!video.closest(".sensitive-media") || video.closest(".sensitive-media").classList.contains("is-revealed") || allMediaVisible());
        if (mayPlay() && video.paused) video.play().then(() => { if (!mayPlay()) video.pause(); }).catch(() => {});
      }
    }
    gallery.addEventListener("click", event => {
      const selected = event.target.closest("[data-content-task-open]");
      if (!selected || expired || !active) return;
      if (env.aiGatewaySensitiveContent?.reveal(selected)) return;
      render(selected);
    });
    dialog.querySelectorAll("[data-content-detail-close]").forEach(button => button.addEventListener("click", () => close()));
    body.addEventListener("click", async event => {
      const tab = event.target.closest("[data-content-tab]");
      if (tab) { selectTab(tab.dataset.contentTab); return; }
      const reveal = event.target.closest("[data-content-reveal]");
      if (reveal) {
        reveal.closest(".sensitive-media")?.classList.add("is-revealed");
        cardFor(taskKey)?.classList.add("is-revealed");
        body.querySelector('[data-content-tab][aria-selected="true"]')?.focus({ preventScroll: true });
        return;
      }
      const copy = event.target.closest("[data-content-copy]");
      if (!copy) return;
      const target = body.querySelector(copy.dataset.contentCopy);
      if (!target) return;
      const key = taskKey;
      try {
        await env.navigator.clipboard.writeText(target.textContent.trim());
        if (active && !expired && !dialog.hidden && key === taskKey) copyStatus.textContent = "Скопировано";
      } catch (_) {
        if (!active || expired || dialog.hidden || key !== taskKey || !target.isConnected) return;
        env.getSelection()?.selectAllChildren(target);
        copyStatus.textContent = "Текст выделен. Скопируйте его.";
      }
    });
    body.addEventListener("keydown", event => {
      const tab = event.target.closest("[data-content-tab]");
      if (!tab || !["ArrowRight", "ArrowLeft", "Home", "End"].includes(event.key)) return;
      event.preventDefault();
      const tabs = [...body.querySelectorAll("[data-content-tab]")];
      const index = tabs.indexOf(tab);
      const next = event.key === "Home" ? 0 : event.key === "End" ? tabs.length - 1 :
        (index + (event.key === "ArrowRight" ? 1 : -1) + tabs.length) % tabs.length;
      selectTab(tabs[next].dataset.contentTab, true);
    });
    const replace = (current, next) => current.replaceChildren(...[...next.childNodes].map(node => document.importNode(node, true)));
    function apply(html) {
      const nextDocument = new env.DOMParser().parseFromString(html, "text/html");
      const nextPage = nextDocument.querySelector("[data-admin-content-page]");
      const nextGallery = nextPage?.querySelector("[data-admin-content-gallery]");
      const fields = ["[data-content-overview]", "[data-content-list-heading]", "[data-content-retention]"];
      const nextCards = nextGallery ? cards(nextGallery) : [];
      const keys = nextCards.map(card => card.dataset.contentTaskKey);
      if (!nextPage || !nextGallery || fields.some(selector => !nextPage.querySelector(selector)) ||
          new Set(keys).size !== keys.length || keys.some(key => !key || !detailFor(nextGallery, key)) ||
          (!nextCards.length && !nextGallery.querySelector("[data-content-empty]"))) throw new Error("invalid_content");
      const focused = gallery.contains(document.activeElement) ? document.activeElement : null;
      const focusKey = focused?.closest("[data-content-task-key]")?.dataset.contentTaskKey;
      const focusHref = focused?.getAttribute("href");
      const focusTitle = focused?.classList.contains("content-task-title");
      const anchor = dialog.hidden && env.scrollY > 220 ? cards(gallery).find(card => card.getBoundingClientRect().bottom > 0) : null;
      const anchorKey = anchor?.dataset.contentTaskKey, anchorTop = anchor?.getBoundingClientRect().top;
      const previous = new Map(cards(gallery).map(card => [card.dataset.contentTaskKey, card]));
      const previousDetails = new Map([...gallery.querySelectorAll("template[data-content-task-detail-key]")].map(detail => [detail.dataset.contentTaskDetailKey, detail]));
      const kept = new Set(), changed = new Set();
      for (const nextCard of nextCards) {
        const key = nextCard.dataset.contentTaskKey, nextDetail = detailFor(nextGallery, key);
        const oldCard = previous.get(key), oldDetail = previousDetails.get(key);
        const unchanged = oldCard && oldDetail && oldCard.dataset.contentVersion === nextCard.dataset.contentVersion &&
          oldDetail.dataset.contentVersion === nextDetail.dataset.contentVersion &&
          oldCard.dataset.contentMediaSignature === nextCard.dataset.contentMediaSignature;
        const card = unchanged ? oldCard : document.importNode(nextCard, true);
        const detail = unchanged ? oldDetail : document.importNode(nextDetail, true);
        if (!unchanged) {
          changed.add(key);
          if (oldCard?.dataset.contentMediaSignature === card.dataset.contentMediaSignature) {
            if (oldCard.classList.contains("is-revealed")) card.classList.add("is-revealed");
            // Preserve the original image/video nodes even when only task text changes.
            const media = oldCard.querySelector(".content-gallery-preview > img, .content-gallery-preview > video");
            const replacement = card.querySelector(".content-gallery-preview > img, .content-gallery-preview > video");
            if (media && replacement) replacement.replaceWith(media);
          }
        }
        gallery.append(card, detail); kept.add(card); kept.add(detail);
      }
      [...gallery.children].forEach(node => { if (!kept.has(node)) { pauseMedia(node); node.remove(); } });
      if (!nextCards.length) replace(gallery, nextGallery);
      fields.forEach(selector => replace(page.querySelector(selector), nextPage.querySelector(selector)));
      revision = Number(nextPage.dataset.contentRevision) || 0;
      page.dataset.contentRevision = String(revision);
      icons(); bindImages(gallery);
      if (!dialog.hidden) {
        const selected = cardFor(taskKey)?.querySelector("[data-content-task-open]");
        if (!selected) close();
        else if (changed.has(taskKey)) render(selected, true);
      } else if (focused) {
        const card = cardFor(focusKey);
        const replacement = focusHref ? [...(card?.querySelectorAll("a[href]") || [])].find(link => link.getAttribute("href") === focusHref) :
          card?.querySelector(focusTitle ? ".content-task-title" : "[data-content-task-open]");
        (replacement || refreshButton).focus({ preventScroll: true });
      }
      if (anchorKey && cardFor(anchorKey)) env.scrollBy(0, cardFor(anchorKey).getBoundingClientRect().top - anchorTop);
    }
    const owner = create({
      read: signal => {
        const url = new URL(env.location.href); url.searchParams.set("live", "1");
        return env.fetch(url, { signal, credentials: "same-origin", cache: "no-store", headers: { Accept: "text/html" } });
      },
      apply,
      poll: (run, interval, hidden) => env.AIGatewayLiveEvents.poll(run, interval, hidden, env),
      stream: callbacks => env.AIGatewayLiveEvents.create({
        url: () => "/admin/content/events?since=" + encodeURIComponent(revision),
        events: { ready: callbacks.refresh, content: callbacks.refresh },
        onState: state => { if (state === "offline") callbacks.disconnected(); },
        onError: callbacks.refresh,
      }, env),
      state(state) {
        page.dataset.contentState = state;
        refreshButton.setAttribute("aria-disabled", String(state === "loading" || state === "access"));
        refreshButton.setAttribute("aria-busy", String(state === "loading"));
        indicator.classList.toggle("is-offline", state === "offline" || state === "access");
        indicator.classList.toggle("is-connecting", state === "loading");
        if (state === "ready") lastRead = new env.Date().toLocaleTimeString("ru-RU");
        label.textContent = state === "ready" ? "Обновлено " + lastRead : state === "loading" ? "Обновление" :
          state === "access" ? "Нет доступа" : "Нет связи" + (lastRead ? " · данные на " + lastRead : "");
        if (state === "access") {
          expired = true; clearImageRetries(); close({ restore: false });
          pauseMedia(page); page.querySelector("[data-content-private]").replaceChildren();
          const access = page.querySelector("[data-content-access]"); access.hidden = false;
          refreshButton.disabled = true; access.querySelector("a").focus({ preventScroll: true });
        }
      },
    }, env);
    page.querySelector("[data-content-controls]").hidden = false;
    refreshButton.addEventListener("click", () => owner.refresh());
    function suspend() { active = false; owner.suspend(); clearImageRetries(); pauseMedia(page); pauseMedia(body); }
    function resume() { if (document.hidden || expired) return; active = true; owner.resume(); }
    env.addEventListener("pagehide", suspend);
    env.addEventListener("pageshow", resume);
    document.addEventListener("visibilitychange", () => document.hidden ? suspend() : resume());
    document.querySelectorAll("[data-sensitive-toggle]").forEach(toggle => toggle.addEventListener("change", () => {
      if (!allMediaVisible()) { pauseMedia(body); pauseMedia(gallery); }
    }));
    icons(); bindImages(gallery); if (active) owner.resume();
  }
  return { create, mount };
});
