(function bootstrapImageComparison(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (root) root.AIGatewayImageComparison = api;
})(typeof window !== "undefined" ? window : null, function imageComparisonFactory() {
  "use strict";
  const clamp = (value, min, max) => Math.max(min, Math.min(max, value));
  const validJobID = value => typeof value === "string" && /^[a-zA-Z0-9_-]{16,128}$/.test(value);
  const originalURL = (value, origin) => {
    try {
      const url = new URL(value, origin);
      if (url.origin !== origin || url.pathname !== "/generate/draft/asset" || url.username || url.password || url.hash ||
          [...url.searchParams.keys()].length !== 1 || !/^[a-zA-Z0-9_-]{16,128}$/.test(url.searchParams.get("id") || "")) return "";
      return url.pathname + url.search;
    } catch (_) { return ""; }
  };
  const visibleBounds = (geometry, width, height) => {
    const left = clamp(geometry.left, 0, width), top = clamp(geometry.top, 0, height);
    return { left, top, width: Math.max(0, Math.min(width, geometry.left + geometry.width) - left),
      height: Math.max(0, Math.min(height, geometry.top + geometry.height) - top) };
  };
  const createController = ({ viewport, image, toggle, documentRef: document, windowRef: window, onChange = () => {} }) => {
    const node = (tag, className, text = "") => {
      const item = document.createElement(tag); item.className = className;
      if (text) item.textContent = text;
      return item;
    };
    const layer = node("div", "image-comparison-layer");
    layer.setAttribute("aria-hidden", "true");
    const original = node("img", "image-comparison-image"); original.alt = ""; original.draggable = false;
    layer.append(original);
    const divider = node("div", "image-comparison-divider"); divider.dataset.comparisonDivider = "";
    divider.tabIndex = 0; divider.setAttribute("role", "slider");
    divider.setAttribute("aria-label", "Граница оригинала и результата");
    divider.setAttribute("aria-valuemin", "0"); divider.setAttribute("aria-valuemax", "100");
    divider.setAttribute("aria-orientation", "horizontal");
    const grip = node("span", "image-comparison-grip"); grip.setAttribute("aria-hidden", "true");
    for (const name of ["ChevronLeft", "ChevronRight"]) {
      const definition = window.lucide?.icons?.[name];
      if (definition) grip.append(window.lucide.createElement(definition));
    }
    divider.append(grip);
    const labels = node("div", "image-comparison-labels"); labels.setAttribute("aria-hidden", "true");
    const before = node("span", "image-comparison-label", "Оригинал"), after = node("span", "image-comparison-label", "Результат");
    labels.append(before, after);
    const notice = node("p", "image-comparison-notice"); notice.setAttribute("role", "status");
    viewport.append(layer, labels, divider, notice);
    let jobID = "", phase = "idle", active = false, position = 50, dragID = null, dragOffset = 0, request = null, revision = 0, disposed = false;
    let geometry = { left: 0, top: 0, width: 0, height: 0 }, timeout = null;
    const message = text => { notice.textContent = text; notice.hidden = !text; };
    const render = () => {
      const bounds = visibleBounds(geometry, viewport.clientWidth, viewport.clientHeight);
      const boundary = bounds.left + bounds.width * position / 100;
      layer.hidden = labels.hidden = divider.hidden = !active;
      viewport.classList.toggle("is-comparing", active);
      Object.assign(original.style, { width: `${geometry.width}px`, height: `${geometry.height}px`,
        transform: `translate(${geometry.left}px, ${geometry.top}px)` });
      layer.style.clipPath = `inset(0 ${Math.max(0, viewport.clientWidth - boundary)}px 0 0)`;
      Object.assign(divider.style, { left: `${boundary}px`, top: `${bounds.top}px`, height: `${bounds.height}px` });
      grip.style.left = `${clamp(boundary, bounds.left + 27, Math.max(bounds.left + 27, bounds.left + bounds.width - 27)) - boundary}px`;
      Object.assign(labels.style, { left: `${bounds.left}px`, top: `${bounds.top}px`, width: `${bounds.width}px` });
      before.hidden = position < 22; after.hidden = position > 78;
      divider.setAttribute("aria-valuenow", String(Math.round(position)));
      divider.setAttribute("aria-valuetext", `Оригинал ${Math.round(position)}%, результат ${100 - Math.round(position)}%`);
      if (toggle) {
        toggle.hidden = !["ready", "error"].includes(phase);
        toggle.setAttribute("aria-pressed", String(active));
        const label = phase === "error" ? "Повторить загрузку оригинала" : active ? "Показать только результат" : "Сравнить с оригиналом";
        toggle.title = label; toggle.setAttribute("aria-label", label);
      }
    };
    const announce = () => { render(); onChange({ active, phase, jobID }); };
    const releaseRequest = () => {
      request?.abort(); request = null;
      if (timeout !== null) window.clearTimeout(timeout);
      timeout = null;
    };
    const clear = () => {
      revision++; releaseRequest(); original.onload = original.onerror = null; original.removeAttribute("src");
      jobID = ""; active = false; phase = "idle"; position = 50; dragID = null; message(""); announce();
    };
    const aspectNotice = () => {
      const ratio = image.naturalWidth / image.naturalHeight, sourceRatio = original.naturalWidth / original.naturalHeight;
      message(active && Number.isFinite(ratio) && Math.abs(ratio / sourceRatio - 1) > .015
        ? "Пропорции изменились. Оригинал показан целиком." : "");
    };
    const finishLoading = () => {
      if (disposed || phase !== "loading" || !original.naturalWidth || !image.naturalWidth) return;
      releaseRequest(); phase = "ready"; active = true; aspectNotice(); announce();
    };
    image.addEventListener("load", finishLoading);
    const setSource = async value => {
      clear();
      if (disposed || !validJobID(value)) return;
      jobID = value; phase = "loading";
      const version = revision, pending = new window.AbortController(); request = pending;
      const fail = () => {
        if (disposed || version !== revision || phase !== "loading") return;
        releaseRequest(); original.onload = original.onerror = null; original.removeAttribute("src");
        active = false; phase = "error"; message("Оригинал не загрузился. Результат доступен."); announce();
      };
      timeout = window.setTimeout(fail, 15000);
      try {
        const response = await window.fetch(`/generate/jobs/comparison?job=${encodeURIComponent(jobID)}`,
          { credentials: "same-origin", cache: "no-store", signal: pending.signal, headers: { Accept: "application/json" } });
        if (disposed || version !== revision || phase !== "loading") return;
        if ([404, 410].includes(response.status)) { releaseRequest(); phase = "unavailable"; announce(); return; }
        if (!response.ok) throw new Error("comparison_unavailable");
        const data = await response.json();
        if (disposed || version !== revision || phase !== "loading") return;
        if (!data.available) { releaseRequest(); phase = "unavailable"; announce(); return; }
        const url = originalURL(data.original?.url, window.location.origin);
        if (!url) throw new Error("invalid_original");
        original.onload = () => {
          if (disposed || version !== revision) return;
          finishLoading();
        };
        original.onerror = fail;
        original.src = url;
      } catch (_) {
        if (version === revision && !disposed && phase === "loading") fail();
      }
    };
    const setPosition = value => { position = clamp(value, 0, 100); render(); };
    const keydown = event => {
      const delta = { ArrowLeft: -1, ArrowRight: 1, PageDown: -10, PageUp: 10 }[event.key];
      if (delta === undefined && !["Home", "End"].includes(event.key)) return;
      event.preventDefault(); event.stopPropagation();
      setPosition(event.key === "Home" ? 0 : event.key === "End" ? 100 : position + delta * (event.shiftKey ? 10 : 1));
    };
    divider.addEventListener("keydown", keydown);
    const toggleClick = () => {
      if (phase === "error") { void setSource(jobID); return; }
      if (phase !== "ready") return;
      active = !active; dragID = null; aspectNotice(); announce();
    };
    toggle?.addEventListener("click", toggleClick);
    window.addEventListener("pagehide", clear);
    render(); message("");
    return {
      setSource, clear,
      renderGeometry: value => { geometry = value; render(); },
      pointerDown: event => {
        if (!active || !event.target.closest?.("[data-comparison-divider]")) return false;
        const bounds = visibleBounds(geometry, viewport.clientWidth, viewport.clientHeight), rect = viewport.getBoundingClientRect();
        dragOffset = event.clientX - rect.left - bounds.left - bounds.width * position / 100;
        dragID = event.pointerId; divider.focus({ preventScroll: true }); return true;
      },
      pointerMove: event => {
        if (dragID !== event.pointerId || !active) return false;
        const bounds = visibleBounds(geometry, viewport.clientWidth, viewport.clientHeight), rect = viewport.getBoundingClientRect();
        if (bounds.width) setPosition((event.clientX - rect.left - bounds.left - dragOffset) / bounds.width * 100);
        return true;
      },
      pointerEnd: event => { const owned = dragID === event.pointerId; if (owned) dragID = null; return owned; },
      cancelDrag: () => { dragID = null; },
      destroy: () => {
        disposed = true; clear(); toggle?.removeEventListener("click", toggleClick); divider.removeEventListener("keydown", keydown);
        window.removeEventListener("pagehide", clear);
        image.removeEventListener("load", finishLoading);
        for (const item of [layer, labels, divider, notice]) item.remove();
      },
    };
  };
  return { validJobID, originalURL, visibleBounds, createController };
});
