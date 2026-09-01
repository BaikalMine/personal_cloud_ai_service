(() => {
  const root = document.querySelector("[data-user-gallery]");
  if (!root) return;

  const filters = [...root.querySelectorAll("[data-gallery-filter]")];
  const visibleCount = root.querySelector("[data-gallery-visible-count]");
  const filterEmpty = root.querySelector("[data-gallery-filter-empty]");
  const lightbox = document.getElementById("gallery-lightbox");
  const lightboxImage = lightbox?.querySelector("[data-gallery-lightbox-image]");
  const lightboxVideo = lightbox?.querySelector("[data-gallery-lightbox-video]");
  const lightboxName = lightbox?.querySelector("[data-gallery-lightbox-name]");
  const lightboxDownload = lightbox?.querySelector("[data-gallery-lightbox-download]");
  const lightboxFocusTrap = window.AIGatewayDialogFocus?.createFocusTrap?.({ root: lightbox, documentRef: document }) || null;

  const downloadURL = (source) => {
    const url = new URL(source, window.location.origin);
    url.searchParams.set("download", "1");
    return `${url.pathname}${url.search}`;
  };

  const closeLightbox = () => {
    if (!lightbox || lightbox.hidden) return;
    lightbox.hidden = true;
    lightboxImage?.removeAttribute("src");
    if (lightboxVideo) {
      lightboxVideo.pause();
      lightboxVideo.removeAttribute("src");
      lightboxVideo.load();
    }
    document.body.classList.remove("generation-lightbox-open");
    lightboxFocusTrap?.deactivate();
  };

  const openLightbox = (trigger) => {
    if (!lightbox || !lightboxImage || !lightboxVideo || !lightboxName || !lightboxDownload) return;
    const source = trigger.dataset.url || "";
    const filename = trigger.dataset.filename || "Результат генерации";
    const isVideo = trigger.dataset.mediaType === "video";
    lightboxImage.hidden = isVideo;
    lightboxVideo.hidden = !isVideo;
    if (isVideo) {
      lightboxVideo.src = source;
      lightboxVideo.play().catch(() => {});
    } else {
      lightboxImage.src = source;
    }
    lightboxName.textContent = filename;
    lightboxDownload.href = downloadURL(source);
    lightboxDownload.download = filename;
    lightbox.hidden = false;
    document.body.classList.add("generation-lightbox-open");
    const closeButton = lightbox.querySelector(".generation-lightbox-close");
    if (lightboxFocusTrap) {
      lightboxFocusTrap.activate({ trigger, initialFocus: closeButton, onEscape: closeLightbox });
    } else {
      closeButton?.focus();
    }
  };

  root.querySelectorAll("[data-gallery-open]").forEach((trigger) => {
    trigger.addEventListener("click", () => {
      if (window.aiGatewaySensitiveContent?.reveal(trigger)) return;
      openLightbox(trigger);
    });
  });

  root.querySelectorAll("[data-gallery-open] img, [data-gallery-open] video").forEach((media) => {
    let retries = 0;
    media.addEventListener("error", () => {
      if (retries >= 2 || !media.src) return;
      retries++;
      const url = new URL(media.src, window.location.origin);
      url.searchParams.set("retry", `${Date.now()}-${retries}`);
      window.setTimeout(() => {
        media.src = url.toString();
        if (media instanceof HTMLVideoElement) media.load();
      }, 300 * retries);
    });
  });
  lightbox?.querySelectorAll("[data-gallery-close]").forEach((button) => button.addEventListener("click", closeLightbox));
  lightboxImage?.addEventListener("click", closeLightbox);
  lightboxVideo?.addEventListener("dblclick", closeLightbox);
  if (!lightboxFocusTrap) document.addEventListener("keydown", (event) => { if (event.key === "Escape") closeLightbox(); });

  const applyFilter = (type) => {
    let count = 0;
    const cards = [...root.querySelectorAll("[data-gallery-item]")];
    cards.forEach((card) => {
      const visible = type === "all" || card.dataset.mediaType === type;
      card.hidden = !visible;
      if (visible) count++;
    });
    filters.forEach((button) => {
      const active = button.dataset.galleryFilter === type;
      button.classList.toggle("is-active", active);
      button.setAttribute("aria-pressed", String(active));
      const badge = button.querySelector("span");
      if (badge) {
        const buttonType = button.dataset.galleryFilter || "all";
        badge.textContent = String(buttonType === "all" ? cards.length : cards.filter((card) => card.dataset.mediaType === buttonType).length);
      }
    });
    if (visibleCount) visibleCount.textContent = String(count);
    if (filterEmpty) filterEmpty.hidden = count > 0;
  };
  filters.forEach((button) => button.addEventListener("click", () => applyFilter(button.dataset.galleryFilter || "all")));

  const formatExpiry = (milliseconds) => {
    if (milliseconds <= 0) return "Срок хранения истёк";
    const totalMinutes = Math.ceil(milliseconds / 60000);
    const hours = Math.floor(totalMinutes / 60);
    const minutes = totalMinutes % 60;
    if (hours > 0) return `Ещё ${hours} ч. ${minutes} мин.`;
    return `Ещё ${Math.max(1, minutes)} мин.`;
  };
  const refreshExpiry = () => {
    root.querySelectorAll("[data-generation-expiry]").forEach((element) => {
      const expiresAt = Number(element.dataset.generationExpiry);
      element.textContent = Number.isFinite(expiresAt) ? formatExpiry(expiresAt - Date.now()) : "Срок неизвестен";
    });
  };
  refreshExpiry();
  window.setInterval(refreshExpiry, 30000);

  root.addEventListener("submit", async (event) => {
    const form = event.target.closest("[data-gallery-hide-form]");
    if (!form) return;
    event.preventDefault();
    const card = form.closest("[data-gallery-item]");
    const button = form.querySelector("button");
    if (!card || !button || button.disabled) return;
    button.disabled = true;
    button.textContent = "Убираем…";
    try {
      const response = await fetch(form.action, {
        method: "POST",
        headers: { Accept: "application/json", "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8" },
        body: new URLSearchParams(new FormData(form)),
        credentials: "same-origin",
      });
      const payload = await response.json().catch(() => ({}));
      if (!response.ok || !payload.removed) throw new Error(payload.error || "Не удалось убрать результат");
      card.remove();
      const activeFilter = filters.find((filter) => filter.classList.contains("is-active"))?.dataset.galleryFilter || "all";
      applyFilter(activeFilter);
    } catch (error) {
      button.disabled = false;
      button.textContent = error.message || "Повторить";
    }
  });
})();
