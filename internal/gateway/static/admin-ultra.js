(function () {
  "use strict";
  const page = document.querySelector("[data-ultra-history]");
  if (!page) return;
  const status = page.querySelector("[data-ultra-read-status]");
  const dialog = document.querySelector("[data-ultra-viewer]");
  const viewport = dialog?.querySelector("[data-ultra-viewport]");
  let timer, controller, departed = false, stopped = false, viewer, opener;
  const snapshot = () => page.querySelector("[data-ultra-snapshot]");
  const icons = () => window.lucide?.createIcons();
  function schedule() {
    clearTimeout(timer);
    if (!departed && !stopped && !document.hidden && snapshot()) timer = setTimeout(() => refresh(false), snapshot().dataset.ultraActive === "true" ? 5000 : 15000);
  }
  function accessLost() {
    stopped = true;
    dialog?.close();
    const message = document.createElement("p"), link = document.createElement("a");
    message.textContent = "Доступ завершён. Войдите снова.";
    link.textContent = "Войти"; link.href = "/login"; link.className = "button";
    page.replaceChildren(message, link);
  }
  async function refresh(manual) {
    if (controller || departed || stopped || !snapshot()) return;
    if (!manual && (document.hidden || dialog?.open || snapshot().contains(document.activeElement))) { schedule(); return; }
    controller = new AbortController();
    const current = controller;
    const timeout = setTimeout(() => current.abort(), 12000);
    if (manual) status.textContent = "Обновление";
    page.setAttribute("aria-busy", "true");
    try {
      const url = new URL(location.href); url.searchParams.set("partial", "1");
      const response = await fetch(url, { credentials: "same-origin", cache: "no-store", signal: current.signal, headers: { Accept: "text/html" } });
      if (departed || current.signal.aborted) return;
      if ([401, 403].includes(response.status) || response.redirected) { accessLost(); return; }
      if (!response.ok) throw new Error("read failed");
      const html = await response.text();
      if (departed || current.signal.aborted) return;
      const next = new DOMParser().parseFromString(html, "text/html").querySelector("[data-ultra-snapshot]");
      if (!next) throw new Error("invalid snapshot");
      const previous = snapshot();
      if ((manual || next.dataset.ultraVersion !== previous.dataset.ultraVersion) && !dialog?.open && !previous.contains(document.activeElement)) { previous.replaceWith(next); icons(); }
      status.textContent = manual ? "Обновлено" : "";
    } catch (_) {
      if (!departed && !document.hidden) status.textContent = "Не удалось обновить. Повторите проверку.";
    } finally {
      clearTimeout(timeout);
      if (controller === current) controller = null;
      page.removeAttribute("aria-busy");
      schedule();
    }
  }
  page.addEventListener("click", async event => {
    if (event.button || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    const refreshLink = event.target.closest("[data-ultra-refresh]");
    if (refreshLink && snapshot()) { event.preventDefault(); refresh(true); return; }
    const copy = event.target.closest("[data-ultra-copy]");
    if (copy) {
      try { await navigator.clipboard.writeText(page.querySelector(copy.dataset.ultraCopy).textContent); status.textContent = "Скопировано"; }
      catch (_) { status.textContent = "Не удалось скопировать"; }
      return;
    }
    const image = event.target.closest("[data-ultra-image]");
    if (!image || !window.AIGatewayGallery?.createViewportGroup || !dialog?.showModal) return;
    event.preventDefault(); opener = image;
    dialog.querySelector("[data-ultra-viewer-error]").hidden = true;
    dialog.showModal(); document.documentElement.classList.add("has-ultra-viewer");
    viewer = window.AIGatewayGallery.createViewportGroup({ toolbar: dialog.querySelector("[data-ultra-zoom-toolbar]"), onError: () => { dialog.querySelector("[data-ultra-viewer-error]").hidden = false; } });
    viewer.add(viewport, { type: "image", url: image.href, filename: image.querySelector("img")?.alt });
    dialog.querySelector("[data-ultra-viewer-close]").focus();
  });
  dialog?.querySelector("[data-ultra-viewer-close]").addEventListener("click", () => dialog.close());
  dialog?.addEventListener("close", () => {
    viewer?.destroy(); viewer = null;
    document.documentElement.classList.remove("has-ultra-viewer");
    if (opener?.isConnected) opener.focus({ preventScroll: true });
    schedule();
  });
  page.addEventListener("error", event => {
    if (!(event.target instanceof HTMLImageElement)) return;
    event.target.hidden = true;
    const parent = event.target.parentElement;
    if (parent?.hasAttribute("data-ultra-image")) { parent.removeAttribute("data-ultra-image"); parent.textContent = "Изображение недоступно"; }
  }, true);
  document.addEventListener("visibilitychange", () => { if (document.hidden) { clearTimeout(timer); controller?.abort(); } else schedule(); });
  window.addEventListener("pagehide", () => { departed = true; clearTimeout(timer); controller?.abort(); dialog?.close(); });
  window.addEventListener("pageshow", () => { departed = false; schedule(); });
  schedule();
})();
