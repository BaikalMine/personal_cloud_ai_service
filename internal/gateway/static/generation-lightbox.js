(function bootstrapGenerationLightbox(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (!root) return;
  root.AIGatewayGeneration = root.AIGatewayGeneration || {};
  root.AIGatewayGeneration.lightbox = api;
})(typeof window !== "undefined" ? window : null, function generationLightboxFactory() {
  const createState = (overrides = {}) => ({ open: false, mediaType: "", url: "", filename: "", ...overrides });
  const reduce = (state = createState(), action = {}) => {
    switch (action.type) {
      case "OPEN":
        return {
          open: true,
          mediaType: action.output?.media_type === "video" ? "video" : "image",
          url: String(action.output?.url || ""),
          filename: String(action.output?.filename || ""),
        };
      case "CLOSE":
        return createState();
      default:
        return state;
    }
  };

  const downloadURL = (outputURL, origin = "http://localhost") => {
    const url = new URL(outputURL, origin);
    if (url.protocol === "blob:") return url.href;
    url.searchParams.set("download", "1");
    return url.pathname + url.search;
  };

  const createImageZoom = ({ viewport, image, toolbar, output, windowObject, comparison } = {}) => {
    if (!viewport || !image) return { reset: () => {}, render: () => {} };
    const pointers = new Map();
    let scale = 1, center = { x: 0.5, y: 0.5 }, left = 0, top = 0;
    let lastTap = null, pointerStart = null, pinching = false;
    const clamp = (value, min, max) => Math.min(max, Math.max(min, value));
    const fit = () => image.naturalWidth && image.naturalHeight
      ? Math.min(viewport.clientWidth / image.naturalWidth, viewport.clientHeight / image.naturalHeight) : 1;
    const render = () => {
      const width = image.naturalWidth * fit() * scale;
      const height = image.naturalHeight * fit() * scale;
      if (!width || !height) return;
      left = width <= viewport.clientWidth ? (viewport.clientWidth - width) / 2
        : clamp(viewport.clientWidth / 2 - center.x * width, viewport.clientWidth - width, 0);
      top = height <= viewport.clientHeight ? (viewport.clientHeight - height) / 2
        : clamp(viewport.clientHeight / 2 - center.y * height, viewport.clientHeight - height, 0);
      Object.assign(image.style, { width: width + "px", height: height + "px", transform: `translate(${left}px, ${top}px)` });
      comparison?.renderGeometry({ width, height, left, top });
      viewport.classList.toggle("is-zoomed", scale > 1.01);
      if (output) output.textContent = Math.round(scale * 100) + "%";
    };
    const reset = () => {
      pointers.clear();
      comparison?.cancelDrag();
      scale = 1; center = { x: 0.5, y: 0.5 }; lastTap = null; pinching = false;
      render();
    };
    const setScale = (next, point) => {
      const previousWidth = image.naturalWidth * fit() * scale;
      const previousHeight = image.naturalHeight * fit() * scale;
      const rect = viewport.getBoundingClientRect();
      const x = point?.x - rect.left, y = point?.y - rect.top;
      const anchorX = previousWidth ? (x - left) / previousWidth : 0.5;
      const anchorY = previousHeight ? (y - top) / previousHeight : 0.5;
      scale = clamp(next, Math.min(1, 1 / fit()), 8);
      if (point) {
        const width = image.naturalWidth * fit() * scale;
        const height = image.naturalHeight * fit() * scale;
        center.x = clamp(anchorX + (viewport.clientWidth / 2 - x) / width, 0, 1);
        center.y = clamp(anchorY + (viewport.clientHeight / 2 - y) / height, 0, 1);
      }
      if (scale === 1) center = { x: 0.5, y: 0.5 };
      render();
    };
    const action = (name, point) => {
      if (name === "fit") return reset();
      if (name === "actual") return setScale(1 / fit());
      setScale(scale * (name === "in" ? 1.3 : 1 / 1.3), point);
    };
    toolbar?.addEventListener("click", event => {
      const button = event.target.closest("[data-lightbox-zoom]");
      if (button) action(button.dataset.lightboxZoom);
    });
    image.addEventListener("load", reset);
    const resize = typeof ResizeObserver !== "undefined" ? new ResizeObserver(render) : null;
    resize?.observe(viewport);
    const position = event => ({ x: event.clientX, y: event.clientY });
    viewport.addEventListener("pointerdown", event => {
      if (image.hidden || event.button !== 0) return;
      pointers.set(event.pointerId, position(event));
      pointerStart = position(event);
      if (comparison?.pointerDown(event)) { lastTap = null; event.preventDefault(); }
      if (pointers.size > 1) { pinching = true; lastTap = null; comparison?.cancelDrag(); }
      viewport.setPointerCapture(event.pointerId);
    });
    viewport.addEventListener("pointermove", event => {
      const previous = pointers.get(event.pointerId);
      if (!previous || !image.naturalWidth) return;
      const before = [...pointers.values()];
      pointers.set(event.pointerId, position(event));
      const after = [...pointers.values()];
      if (after.length === 2) {
        const distance = points => Math.hypot(points[0].x - points[1].x, points[0].y - points[1].y);
        const oldDistance = distance(before);
        if (oldDistance) {
          const midpoint = { x: (after[0].x + after[1].x) / 2, y: (after[0].y + after[1].y) / 2 };
          setScale(scale * distance(after) / oldDistance, midpoint);
        }
      } else if (comparison?.pointerMove(event)) {
        event.preventDefault();
      } else if (scale > 1) {
        center.x = clamp(center.x - (event.clientX - previous.x) / (image.naturalWidth * fit() * scale), 0, 1);
        center.y = clamp(center.y - (event.clientY - previous.y) / (image.naturalHeight * fit() * scale), 0, 1);
        render();
      }
    });
    const endPointer = event => {
      if (!pointers.has(event.pointerId)) return;
      pointers.delete(event.pointerId);
      if (comparison?.pointerEnd(event)) { lastTap = null; return; }
      if (pinching) {
        lastTap = null;
        if (!pointers.size) pinching = false;
        return;
      }
      if (event.type !== "pointerup" || event.pointerType !== "touch" || !pointerStart) return;
      const moved = Math.hypot(event.clientX - pointerStart.x, event.clientY - pointerStart.y);
      if (moved > 16) { lastTap = null; return; }
      const now = Date.now(), point = position(event);
      if (lastTap && now - lastTap.time < 320 && Math.hypot(point.x - lastTap.x, point.y - lastTap.y) < 30) {
        setScale(scale > 1.01 ? 1 : 2, point);
        lastTap = null;
      } else lastTap = { ...point, time: now };
    };
    for (const type of ["pointerup", "pointercancel", "lostpointercapture"]) viewport.addEventListener(type, endPointer);
    viewport.addEventListener("dblclick", event => {
      if (event.pointerType === "touch" || image.hidden || event.target.closest("[data-comparison-divider]")) return;
      setScale(scale > 1.01 ? 1 : 2, position(event));
    });
    viewport.addEventListener("wheel", event => {
      if (image.hidden) return;
      event.preventDefault();
      setScale(scale * Math.exp(-event.deltaY * 0.0015), position(event));
    }, { passive: false });
    viewport.addEventListener("keydown", event => {
      if (event.target !== viewport || image.hidden) return;
      const key = { "+": "in", "=": "in", "-": "out", "0": "fit", "1": "actual" }[event.key];
      if (key) { event.preventDefault(); action(key); }
      if (scale > 1 && ["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown"].includes(event.key)) {
        event.preventDefault();
        center.x = clamp(center.x + (event.key === "ArrowLeft" ? -0.05 : event.key === "ArrowRight" ? 0.05 : 0), 0, 1);
        center.y = clamp(center.y + (event.key === "ArrowUp" ? -0.05 : event.key === "ArrowDown" ? 0.05 : 0), 0, 1);
        render();
      }
    });
    windowObject?.addEventListener?.("resize", render);
    return { reset, render };
  };

  const createController = ({ elements = {}, documentRef, windowRef, store, sensitiveContent, focusTrap } = {}) => {
    const documentObject = documentRef || (typeof document !== "undefined" ? document : null);
    const windowObject = windowRef || (typeof window !== "undefined" ? window : null);
    const comparison = elements.viewport && elements.image && windowObject?.AIGatewayImageComparison?.createController({
      viewport: elements.viewport, image: elements.image, toggle: elements.compare,
      documentRef: documentObject, windowRef: windowObject,
      onChange: ({ active }) => {
        if (elements.title) elements.title.textContent = active ? "Сравнение" : "Результат";
        elements.root?.classList.toggle("is-comparing", active);
      },
    });
    const imageZoom = createImageZoom({ viewport: elements.viewport, image: elements.image, toolbar: elements.zoom, output: elements.scale, windowObject, comparison });
    let state = createState();
    const commit = (action) => {
      state = reduce(state, action);
      store?.setSlice?.("lightbox", state, "lightbox:change");
      return state;
    };
    const close = () => {
      if (!elements.root || elements.root.hidden) return;
      elements.root.hidden = true;
      comparison?.clear();
      imageZoom.reset();
      elements.image?.removeAttribute("src");
      if (elements.video) {
        elements.video.pause();
        elements.video.removeAttribute("src");
        elements.video.load();
      }
      documentObject?.body?.classList.remove("generation-lightbox-open");
      focusTrap?.deactivate?.();
      commit({ type: "CLOSE" });
    };
    const open = (output) => {
      if (!elements.root || !output?.url) return;
      const next = commit({ type: "OPEN", output });
      const isVideo = next.mediaType === "video";
      comparison?.clear();
      imageZoom.reset();
      if (elements.image) elements.image.hidden = isVideo;
      if (elements.video) elements.video.hidden = !isVideo;
      if (elements.zoom) elements.zoom.hidden = isVideo;
      elements.viewport?.classList.toggle("is-image", !isVideo);
      elements.viewport?.setAttribute("aria-label", isVideo ? "Просмотр видео" : "Просмотр изображения; жест двумя пальцами изменяет масштаб");
      if (elements.viewport) elements.viewport.tabIndex = isVideo ? -1 : 0;
      if (isVideo && elements.video) {
        elements.video.src = next.url;
        elements.video.muted = false;
        elements.video.play().catch(() => {});
      } else if (elements.image) {
        elements.image.src = next.url;
      }
      if (elements.name) elements.name.textContent = next.filename;
      if (elements.download) {
        elements.download.href = downloadURL(next.url, windowObject?.location?.origin);
        elements.download.download = next.filename;
      }
      elements.root.hidden = false;
      imageZoom.render();
      if (!isVideo) void comparison?.setSource(output.comparison_job_id || output.generation_job_public_id || "");
      documentObject?.body?.classList.add("generation-lightbox-open");
      const closeButton = elements.root.querySelector?.(".generation-lightbox-close");
      if (focusTrap) {
        focusTrap.activate?.({ trigger: documentObject?.activeElement, initialFocus: closeButton, onEscape: close });
      } else {
        closeButton?.focus();
      }
    };
    const wireVideoPreview = (button, output, { canPlay = () => !documentObject?.hidden } = {}) => {
      const video = button?.querySelector?.("video");
      if (!video) return;
      const markUnavailable = () => {
        button.dataset.videoUnavailable = "true";
        button.title = "This file is not supported by the browser. Download is still available.";
      };
      video.addEventListener("loadeddata", () => { delete button.dataset.videoUnavailable; }, { once: true });
      video.addEventListener("error", markUnavailable, { once: true });
      const start = () => {
        if (!canPlay()) return;
        video.muted = true;
        video.play().catch(() => {});
      };
      const stop = () => {
        video.pause();
        video.currentTime = 0;
      };
      button.addEventListener("pointerenter", start);
      button.addEventListener("pointerleave", stop);
      button.addEventListener("focusin", start);
      button.addEventListener("focusout", stop);
      button.addEventListener("click", () => {
        if (sensitiveContent?.reveal?.(button)) return;
        open(output);
      });
    };
    return { getState: () => state, open, close, wireVideoPreview };
  };

  return { createState, reduce, downloadURL, createController };
});
