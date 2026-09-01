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
    url.searchParams.set("download", "1");
    return url.pathname + url.search;
  };

  const createController = ({ elements = {}, documentRef, windowRef, store, sensitiveContent } = {}) => {
    const documentObject = documentRef || (typeof document !== "undefined" ? document : null);
    const windowObject = windowRef || (typeof window !== "undefined" ? window : null);
    let state = createState();
    const commit = (action) => {
      state = reduce(state, action);
      store?.setSlice?.("lightbox", state, "lightbox:change");
      return state;
    };
    const close = () => {
      if (!elements.root || elements.root.hidden) return;
      elements.root.hidden = true;
      elements.image?.removeAttribute("src");
      if (elements.video) {
        elements.video.pause();
        elements.video.removeAttribute("src");
        elements.video.load();
      }
      documentObject?.body?.classList.remove("generation-lightbox-open");
      commit({ type: "CLOSE" });
    };
    const open = (output) => {
      if (!elements.root || !output?.url) return;
      const next = commit({ type: "OPEN", output });
      const isVideo = next.mediaType === "video";
      if (elements.image) elements.image.hidden = isVideo;
      if (elements.video) elements.video.hidden = !isVideo;
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
      documentObject?.body?.classList.add("generation-lightbox-open");
      elements.root.querySelector?.(".generation-lightbox-close")?.focus();
    };
    const wireVideoPreview = (button, output) => {
      const video = button?.querySelector?.("video");
      if (!video) return;
      const markUnavailable = () => {
        button.dataset.videoUnavailable = "true";
        button.title = "This file is not supported by the browser. Download is still available.";
      };
      video.addEventListener("loadeddata", () => { delete button.dataset.videoUnavailable; }, { once: true });
      video.addEventListener("error", markUnavailable, { once: true });
      const start = () => {
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
