(function () {
  "use strict";
  const api = window.AIGatewayGallery;
  if (!api) return;

  // One geometry model can drive one viewport or both comparison panes.
  api.createViewportGroup = function ({ toolbar, comparisonToggle, onError = () => {} }) {
    const panes = [];
    let scale = 1, center = { x: 0.5, y: 0.5 }, mode = "fit", destroyed = false;
    const output = toolbar?.querySelector("[data-zoom-value]");
    function fit() {
      const ready = panes.filter(pane => pane.width && pane.height);
      return ready.length ? Math.min(...ready.map(pane => api.fitScale(pane.width, pane.height, pane.host.clientWidth, pane.host.clientHeight))) : 1;
    }
    function render() {
      if (destroyed) return;
      if (mode === "fit") scale = fit();
      for (const pane of panes) {
        if (!pane.width || !pane.height) continue;
        const width = pane.width * scale, height = pane.height * scale;
        const x = width <= pane.host.clientWidth ? (pane.host.clientWidth - width) / 2 : api.clamp(pane.host.clientWidth / 2 - center.x * width, pane.host.clientWidth - width, 0);
        const y = height <= pane.host.clientHeight ? (pane.host.clientHeight - height) / 2 : api.clamp(pane.host.clientHeight / 2 - center.y * height, pane.host.clientHeight - height, 0);
        Object.assign(pane.media.style, { width: `${width}px`, height: `${height}px`, transform: `translate(${x}px, ${y}px)` });
        pane.comparison?.renderGeometry({ width, height, left: x, top: y });
        pane.host.classList.toggle("is-zoomed", width > pane.host.clientWidth + 1 || height > pane.host.clientHeight + 1);
      }
      if (output) output.textContent = `${Math.round(scale * 100)}%`;
    }
    function zoom(action) {
      mode = action === "fit" ? "fit" : "manual";
      if (action === "fit") center = { x: 0.5, y: 0.5 };
      else scale = api.clamp(action === "actual" ? 1 : scale * (action === "in" ? 1.25 : 0.8), Math.min(fit(), 0.05), 8);
      render();
    }
    const toolbarClick = event => { const button = event.target.closest("[data-zoom]"); if (button) zoom(button.dataset.zoom); };
    toolbar?.addEventListener("click", toolbarClick);
    const resize = new ResizeObserver(render);
    function add(host, source, { nativeControls = true, originalJobID = "" } = {}) {
      const media = document.createElement(source.type === "video" ? "video" : "img");
      const pane = { host, media, width: 0, height: 0, pointers: new Map(), cleanup: [] };
      media.className = "gallery-viewport-media";
      if (source.type === "video") { media.controls = nativeControls; media.playsInline = true; media.preload = "metadata"; media.dataset.galleryLightboxVideo = ""; }
      else { media.alt = source.filename || "Результат"; media.draggable = false; media.dataset.galleryLightboxImage = ""; }
      const loaded = () => { pane.width = media.videoWidth || media.naturalWidth; pane.height = media.videoHeight || media.naturalHeight; host.classList.remove("is-loading"); render(); };
      media.addEventListener(source.type === "video" ? "loadedmetadata" : "load", loaded);
      media.addEventListener("error", () => { if (destroyed) return; pane.comparison?.clear(); host.classList.remove("is-loading"); onError(source, host); });
      host.replaceChildren(media);
      if (source.type === "image" && comparisonToggle) {
        pane.comparison = window.AIGatewayImageComparison?.createController({
          viewport: host, image: media, toggle: comparisonToggle, documentRef: document, windowRef: window,
        });
        void pane.comparison?.setSource(originalJobID);
      }
      host.classList.add("is-loading");
      panes.push(pane);
      resize.observe(host);
      function listen(type, handler, options) { host.addEventListener(type, handler, options); pane.cleanup.push(() => host.removeEventListener(type, handler, options)); }
      const position = event => ({ x: event.clientX, y: event.clientY });
      listen("pointerdown", event => {
        if (event.button !== 0 || source.type === "video" && nativeControls) return;
        pane.pointers.set(event.pointerId, position(event));
        if (pane.comparison?.pointerDown(event)) event.preventDefault();
        if (pane.pointers.size > 1) pane.comparison?.cancelDrag();
        host.setPointerCapture(event.pointerId);
        if (pane.pointers.size > 1 || host.classList.contains("is-zoomed")) event.preventDefault();
      });
      listen("pointermove", event => {
        const previous = pane.pointers.get(event.pointerId);
        if (!previous || !pane.width) return;
        const before = [...pane.pointers.values()];
        pane.pointers.set(event.pointerId, position(event));
        const after = [...pane.pointers.values()];
        if (after.length === 2) {
          const distance = points => Math.hypot(points[0].x - points[1].x, points[0].y - points[1].y);
          if (distance(before) > 0) { mode = "manual"; scale = api.clamp(scale * distance(after) / distance(before), Math.min(fit(), 0.05), 8); }
        } else if (pane.comparison?.pointerMove(event)) {
          event.preventDefault();
        } else {
          center.x = api.clamp(center.x - (event.clientX - previous.x) / (pane.width * scale), 0, 1);
          center.y = api.clamp(center.y - (event.clientY - previous.y) / (pane.height * scale), 0, 1);
        }
        render();
      });
      for (const type of ["pointerup", "pointercancel", "lostpointercapture"]) listen(type, event => { pane.pointers.delete(event.pointerId); pane.comparison?.pointerEnd(event); });
      listen("wheel", event => { if (!event.ctrlKey) return; event.preventDefault(); zoom(event.deltaY < 0 ? "in" : "out"); }, { passive: false });
      listen("keydown", event => {
        if (event.target !== host) return;
        if (["+", "=", "-", "0", "1"].includes(event.key)) { event.preventDefault(); zoom({ "+": "in", "=": "in", "-": "out", "0": "fit", "1": "actual" }[event.key]); }
        if (event.shiftKey && ["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown"].includes(event.key)) {
          event.preventDefault(); event.stopPropagation();
          center.x = api.clamp(center.x + (event.key === "ArrowLeft" ? -0.05 : event.key === "ArrowRight" ? 0.05 : 0), 0, 1);
          center.y = api.clamp(center.y + (event.key === "ArrowUp" ? -0.05 : event.key === "ArrowDown" ? 0.05 : 0), 0, 1);
          render();
        }
      });
      const url = api.safeURL(source.url, location.origin);
      if (url) media.src = url;
      else onError(source, host);
      render();
      return media;
    }
    function destroy() {
      destroyed = true;
      resize.disconnect();
      toolbar?.removeEventListener("click", toolbarClick);
      for (const pane of panes) {
        pane.comparison?.destroy();
        pane.cleanup.forEach(cleanup => cleanup());
        if (pane.media instanceof HTMLVideoElement) pane.media.pause();
        pane.media.removeAttribute("src");
        if (pane.media instanceof HTMLVideoElement) pane.media.load();
        pane.host.replaceChildren();
      }
    }
    return { add, zoom, render, destroy, pause: () => panes.forEach(pane => pane.media.pause?.()), snapshot: () => ({ scale, center: { ...center }, mode }) };
  };
})();
