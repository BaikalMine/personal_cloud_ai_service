(() => {
  const root = document.querySelector("[data-user-gallery]");
  if (!root) return;

  const csrf = root.dataset.csrf || "";
  const search = root.querySelector("[data-gallery-search]");
  const dateFrom = root.querySelector("[data-gallery-date-from]");
  const dateTo = root.querySelector("[data-gallery-date-to]");
  const typeFilters = [...root.querySelectorAll("[data-gallery-filter]")];
  const flagFilters = [...root.querySelectorAll("[data-gallery-flag]")];
  const visibleCount = root.querySelector("[data-gallery-visible-count]");
  const visibleLabel = root.querySelector("[data-gallery-visible-label]");
  const filterEmpty = root.querySelector("[data-gallery-filter-empty]");
  const selectionBar = root.querySelector("[data-gallery-selection-bar]");
  const selectionCount = root.querySelector("[data-gallery-selection-count]");
  const selectAll = root.querySelector("[data-gallery-select-all]");
  const status = root.querySelector("[data-gallery-status]");
  const collectionList = root.querySelector(".media-library-collection-list");
  const collectionHeadingCount = root.querySelector(".media-library-collections-heading span");
  const metadataCollections = root.querySelector("[data-gallery-metadata-collections]");

  const lightbox = document.getElementById("gallery-lightbox");
  const lightboxImage = lightbox?.querySelector("[data-gallery-lightbox-image]");
  const lightboxVideo = lightbox?.querySelector("[data-gallery-lightbox-video]");
  const lightboxName = lightbox?.querySelector("[data-gallery-lightbox-name]");
  const lightboxDownload = lightbox?.querySelector("[data-gallery-lightbox-download]");
  const metadataDialog = document.getElementById("gallery-metadata-dialog");
  const metadataForm = metadataDialog?.querySelector("[data-gallery-metadata-form]");
  const metadataStatus = metadataDialog?.querySelector("[data-gallery-metadata-status]");
  const useDialog = document.getElementById("gallery-use-dialog");
  const useForm = useDialog?.querySelector("[data-gallery-use-form]");
  const useHint = useDialog?.querySelector("[data-gallery-use-hint]");
  const compareDialog = document.getElementById("gallery-compare-dialog");
  const compareGrid = compareDialog?.querySelector("[data-gallery-compare-grid]");
  const confirmDialog = document.getElementById("gallery-confirm-dialog");
  const confirmMessage = confirmDialog?.querySelector("[data-gallery-confirm-message]");
  const confirmAccept = confirmDialog?.querySelector("[data-gallery-confirm-accept]");

  let activeType = "all";
  let activeCollection = "";
  let statusTimer = 0;
  let metadataCard = null;
  let confirmResolve = null;
  let roleTouched = false;

  const cards = () => [...root.querySelectorAll("[data-gallery-item]")];
  const normalize = (value) => String(value || "").trim().toLocaleLowerCase("ru-RU");
  const isTrue = (value) => String(value) === "true";
  const cardMediaID = (card) => card?.querySelector("[data-media-id]")?.dataset.mediaId || "";
  const cardCollectionIDs = (card) => new Set([...card.querySelectorAll("[data-gallery-collection-id]")].map((item) => item.dataset.galleryCollectionId));
  const activeFlags = () => new Set(flagFilters.filter((button) => button.getAttribute("aria-pressed") === "true").map((button) => button.dataset.galleryFlag));

  const setStatus = (message, tone = "ready") => {
    if (!status) return;
    window.clearTimeout(statusTimer);
    status.textContent = message;
    status.dataset.tone = tone;
    status.hidden = !message;
    if (message) statusTimer = window.setTimeout(() => { status.hidden = true; }, 5000);
  };

  const postForm = async (url, body) => {
    const response = await fetch(url, {
      method: "POST",
      headers: { Accept: "application/json", "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8" },
      body,
      credentials: "same-origin",
    });
    const raw = await response.text();
    let payload = {};
    try { payload = raw ? JSON.parse(raw) : {}; } catch (_) {}
    if (!response.ok) throw new Error(payload.error || raw.trim() || "Действие не выполнено");
    return payload;
  };

  const dialogTraps = new Map();
  [metadataDialog, useDialog, compareDialog, confirmDialog].filter(Boolean).forEach((dialog) => {
    const trap = window.AIGatewayDialogFocus?.createFocusTrap?.({ root: dialog, documentRef: document });
    if (trap) dialogTraps.set(dialog, trap);
  });
  const closeDialog = (dialog) => {
    if (!dialog || dialog.hidden) return;
    dialog.hidden = true;
    dialogTraps.get(dialog)?.deactivate();
    if (![metadataDialog, useDialog, compareDialog, confirmDialog].some((item) => item && !item.hidden)) document.body.classList.remove("media-library-dialog-open");
  };
  const openDialog = (dialog, trigger, initialFocus, onEscape) => {
    if (!dialog) return;
    dialog.hidden = false;
    document.body.classList.add("media-library-dialog-open");
    const trap = dialogTraps.get(dialog);
    const escape = onEscape || (() => closeDialog(dialog));
    if (trap) trap.activate({ trigger, initialFocus, onEscape: escape });
    else initialFocus?.focus({ preventScroll: true });
  };

  const finishConfirmation = (accepted) => {
    const resolve = confirmResolve;
    confirmResolve = null;
    closeDialog(confirmDialog);
    resolve?.(accepted);
  };
  const requestConfirmation = (message, trigger) => new Promise((resolve) => {
    if (!confirmDialog || !confirmMessage || !confirmAccept) {
      resolve(window.confirm(message));
      return;
    }
    if (confirmResolve) confirmResolve(false);
    confirmResolve = resolve;
    confirmMessage.textContent = message;
    openDialog(confirmDialog, trigger, confirmDialog.querySelector("[data-gallery-confirm-cancel]"), () => finishConfirmation(false));
  });
  confirmDialog?.querySelectorAll("[data-gallery-confirm-cancel]").forEach((button) => button.addEventListener("click", () => finishConfirmation(false)));
  confirmAccept?.addEventListener("click", () => finishConfirmation(true));

  const downloadURL = (source) => {
    const url = new URL(source, window.location.origin);
    url.searchParams.set("download", "1");
    return `${url.pathname}${url.search}`;
  };
  const lightboxTrap = window.AIGatewayDialogFocus?.createFocusTrap?.({ root: lightbox, documentRef: document }) || null;
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
    lightboxTrap?.deactivate();
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
    if (lightboxTrap) lightboxTrap.activate({ trigger, initialFocus: closeButton, onEscape: closeLightbox });
    else closeButton?.focus({ preventScroll: true });
  };
  root.querySelectorAll("[data-gallery-open]").forEach((trigger) => trigger.addEventListener("click", () => {
    if (window.aiGatewaySensitiveContent?.reveal(trigger)) return;
    openLightbox(trigger);
  }));
  lightbox?.querySelectorAll("[data-gallery-close]").forEach((button) => button.addEventListener("click", closeLightbox));
  lightboxImage?.addEventListener("click", closeLightbox);
  lightboxVideo?.addEventListener("dblclick", closeLightbox);
  if (!lightboxTrap) document.addEventListener("keydown", (event) => { if (event.key === "Escape") closeLightbox(); });

  root.querySelectorAll("[data-gallery-open] img, [data-gallery-open] video").forEach((media) => {
    let retries = 0;
    media.addEventListener("error", () => {
      if (retries >= 2 || !media.src) return;
      retries += 1;
      const url = new URL(media.src, window.location.origin);
      url.searchParams.set("retry", `${Date.now()}-${retries}`);
      window.setTimeout(() => {
        media.src = url.toString();
        if (media instanceof HTMLVideoElement) media.load();
      }, 300 * retries);
    });
  });

  const parseDateStart = (value) => value ? new Date(`${value}T00:00:00`).getTime() : Number.NEGATIVE_INFINITY;
  const parseDateEnd = (value) => value ? new Date(`${value}T00:00:00`).getTime() + 86400000 : Number.POSITIVE_INFINITY;
  const cardMatchesFlags = (card, flags) => {
    for (const flag of flags) {
      if (flag === "pinned" && !isTrue(card.dataset.pinned)) return false;
      if (flag === "favorite" && !isTrue(card.dataset.favorite)) return false;
      if (flag === "sensitive" && !isTrue(card.dataset.sensitive)) return false;
      if (flag === "error" && card.dataset.mediaType !== "error" && !["error", "failed"].includes(card.dataset.state)) return false;
    }
    return true;
  };

  const updateCounts = () => {
    const items = cards();
    typeFilters.forEach((button) => {
      const type = button.dataset.galleryFilter || "all";
      const count = type === "all" ? items.length : items.filter((card) => card.dataset.mediaType === type).length;
      const badge = button.querySelector("span");
      if (badge) badge.textContent = String(count);
    });
    flagFilters.forEach((button) => {
      const count = items.filter((card) => cardMatchesFlags(card, new Set([button.dataset.galleryFlag]))).length;
      const badge = button.querySelector("span");
      if (badge) badge.textContent = String(count);
    });
    root.querySelectorAll("[data-gallery-collection-filter]").forEach((button) => {
      const id = button.dataset.galleryCollectionFilter || "";
      const count = id ? items.filter((card) => cardCollectionIDs(card).has(id)).length : items.length;
      const badge = button.querySelector("b");
      if (badge) badge.textContent = String(count);
    });
    root.querySelectorAll("[data-gallery-metadata-collection]").forEach((label) => {
      const id = label.dataset.galleryMetadataCollection;
      const count = items.filter((card) => cardCollectionIDs(card).has(id)).length;
      const small = label.querySelector("small");
      if (small) small.textContent = `${count} результатов`;
    });
  };

  const syncSelection = () => {
    const selected = [...root.querySelectorAll("[data-gallery-select]:checked")];
    if (selectionCount) selectionCount.textContent = String(selected.length);
    if (selectionBar) selectionBar.hidden = selected.length === 0;
    const visible = [...root.querySelectorAll("[data-gallery-item]:not([hidden]) [data-gallery-select]")];
    const allVisibleSelected = visible.length > 0 && visible.every((input) => input.checked);
    if (selectAll) selectAll.textContent = allVisibleSelected ? "Снять выбор" : "Выбрать все видимые";
  };

  const applyFilters = () => {
    const query = normalize(search?.value);
    const from = parseDateStart(dateFrom?.value || "");
    const to = parseDateEnd(dateTo?.value || "");
    const flags = activeFlags();
    let shown = 0;
    cards().forEach((card) => {
      const created = new Date(card.dataset.created || "").getTime();
      const visible = (activeType === "all" || card.dataset.mediaType === activeType)
        && (!activeCollection || cardCollectionIDs(card).has(activeCollection))
        && (!query || normalize(card.dataset.search).includes(query))
        && (!Number.isFinite(created) || (created >= from && created < to))
        && cardMatchesFlags(card, flags);
      card.hidden = !visible;
      if (visible) shown += 1;
    });
    if (visibleCount) visibleCount.textContent = String(shown);
    if (visibleLabel) visibleLabel.textContent = shown === 1 ? "результат" : "результатов";
    if (filterEmpty) filterEmpty.hidden = shown > 0;
    syncSelection();
  };

  typeFilters.forEach((button) => button.addEventListener("click", () => {
    activeType = button.dataset.galleryFilter || "all";
    typeFilters.forEach((candidate) => {
      const active = candidate === button;
      candidate.classList.toggle("is-active", active);
      candidate.setAttribute("aria-pressed", String(active));
    });
    applyFilters();
  }));
  flagFilters.forEach((button) => button.addEventListener("click", () => {
    const active = button.getAttribute("aria-pressed") !== "true";
    button.setAttribute("aria-pressed", String(active));
    button.classList.toggle("is-active", active);
    applyFilters();
  }));
  [search, dateFrom, dateTo].filter(Boolean).forEach((control) => control.addEventListener(control === search ? "input" : "change", applyFilters));
  root.querySelector("[data-gallery-reset]")?.addEventListener("click", () => {
    if (search) search.value = "";
    if (dateFrom) dateFrom.value = "";
    if (dateTo) dateTo.value = "";
    activeType = "all";
    activeCollection = "";
    typeFilters.forEach((button) => {
      const active = button.dataset.galleryFilter === "all";
      button.classList.toggle("is-active", active);
      button.setAttribute("aria-pressed", String(active));
    });
    flagFilters.forEach((button) => { button.classList.remove("is-active"); button.setAttribute("aria-pressed", "false"); });
    root.querySelectorAll("[data-gallery-collection-filter]").forEach((button) => {
      const active = !button.dataset.galleryCollectionFilter;
      button.classList.toggle("is-active", active);
      button.setAttribute("aria-pressed", String(active));
    });
    applyFilters();
  });
  collectionList?.addEventListener("click", (event) => {
    const filter = event.target.closest("[data-gallery-collection-filter]");
    if (!filter) return;
    activeCollection = filter.dataset.galleryCollectionFilter || "";
    root.querySelectorAll("[data-gallery-collection-filter]").forEach((button) => {
      const active = button === filter;
      button.classList.toggle("is-active", active);
      button.setAttribute("aria-pressed", String(active));
    });
    applyFilters();
  });

  root.addEventListener("change", (event) => { if (event.target.matches("[data-gallery-select]")) syncSelection(); });
  selectAll?.addEventListener("click", () => {
    const visible = [...root.querySelectorAll("[data-gallery-item]:not([hidden]) [data-gallery-select]")];
    const next = !visible.length || !visible.every((input) => input.checked);
    visible.forEach((input) => { input.checked = next; });
    syncSelection();
  });
  root.querySelector("[data-gallery-selection-clear]")?.addEventListener("click", () => {
    root.querySelectorAll("[data-gallery-select]").forEach((input) => { input.checked = false; });
    syncSelection();
  });

  const selectedMediaIDs = () => [...root.querySelectorAll("[data-gallery-select]:checked")].map((input) => input.value);
  root.querySelector("[data-gallery-bulk-export]")?.addEventListener("click", () => {
    const ids = selectedMediaIDs();
    if (!ids.length) return;
    const form = document.createElement("form");
    form.method = "post";
    form.action = "/generate/library/export";
    form.hidden = true;
    [["csrf", csrf], ...ids.map((id) => ["media_id", id])].forEach(([name, value]) => {
      const input = document.createElement("input");
      input.type = "hidden";
      input.name = name;
      input.value = value;
      form.append(input);
    });
    document.body.append(form);
    form.submit();
    form.remove();
  });
  root.querySelector("[data-gallery-bulk-hide]")?.addEventListener("click", async (event) => {
    const ids = selectedMediaIDs();
    if (!ids.length || !await requestConfirmation(`Убрать выбранные результаты (${ids.length}) из медиатеки?`, event.currentTarget)) return;
    const button = event.currentTarget;
    button.disabled = true;
    try {
      const body = new URLSearchParams({ csrf });
      ids.forEach((id) => body.append("media_id", id));
      await postForm("/generate/library/bulk-hide", body);
      const selected = new Set(ids);
      cards().forEach((card) => { if (selected.has(cardMediaID(card))) card.remove(); });
      updateCounts();
      applyFilters();
      setStatus("Выбранные результаты убраны из медиатеки.");
    } catch (error) {
      setStatus(error.message || "Не удалось убрать результаты", "error");
    } finally {
      button.disabled = false;
    }
  });

  const formatExpiry = (milliseconds) => {
    if (milliseconds <= 0) return "Срок хранения истёк";
    const totalMinutes = Math.ceil(milliseconds / 60000);
    const days = Math.floor(totalMinutes / 1440);
    const hours = Math.floor((totalMinutes % 1440) / 60);
    const minutes = totalMinutes % 60;
    if (days > 0) return `Ещё ${days} дн. ${hours} ч.`;
    if (hours > 0) return `Ещё ${hours} ч. ${minutes} мин.`;
    return `Ещё ${Math.max(1, minutes)} мин.`;
  };
  const refreshExpiry = () => root.querySelectorAll("[data-generation-expiry]").forEach((element) => {
    const expiresAt = Number(element.dataset.generationExpiry);
    element.textContent = Number.isFinite(expiresAt) ? formatExpiry(expiresAt - Date.now()) : "Срок неизвестен";
  });
  refreshExpiry();
  window.setInterval(refreshExpiry, 30000);

  const toggleMediaFlag = async (button, card, type) => {
    const mediaID = cardMediaID(card);
    if (!mediaID || button.disabled) return;
    const enabled = !isTrue(card.dataset[type]);
    button.disabled = true;
    try {
      const payload = await postForm(`/generate/library/${type}`, new URLSearchParams({ csrf, media_id: mediaID, enabled: String(enabled) }));
      card.dataset[type] = String(enabled);
      button.setAttribute("aria-pressed", String(enabled));
      if (type === "pinned") {
        card.classList.toggle("is-pinned", enabled);
        button.textContent = enabled ? "Закреплено" : "Закрепить";
        const expiry = card.querySelector("[data-generation-expiry]");
        if (expiry && payload.expires_unix) expiry.dataset.generationExpiry = String(payload.expires_unix);
        refreshExpiry();
        setStatus(enabled ? `Результат закреплён на ${root.dataset.pinnedRetention}.` : `Возвращён обычный срок хранения: ${root.dataset.mediaRetention}.`);
      } else {
        button.textContent = enabled ? "В избранном" : "В избранное";
        setStatus(enabled ? "Добавлено в избранное." : "Удалено из избранного.");
      }
      updateCounts();
      applyFilters();
    } catch (error) {
      setStatus(error.message || "Не удалось обновить результат", "error");
    } finally {
      button.disabled = false;
    }
  };
  root.addEventListener("click", (event) => {
    const pin = event.target.closest("[data-gallery-pin]");
    const favorite = event.target.closest("[data-gallery-favorite]");
    if (pin) toggleMediaFlag(pin, pin.closest("[data-gallery-item]"), "pinned");
    if (favorite) toggleMediaFlag(favorite, favorite.closest("[data-gallery-item]"), "favorite");
  });

  const cardTags = (card) => [...card.querySelectorAll("[data-gallery-tag]")].map((tag) => tag.dataset.galleryTag || tag.textContent.trim());
  const rebuildCardSearch = (card) => {
    card.dataset.search = [card.querySelector(".media-library-card-heading span")?.textContent, card.querySelector(".media-library-model")?.textContent, card.querySelector(".media-library-prompt")?.textContent, ...cardTags(card)].filter(Boolean).join(" ");
  };
  const renderCardTags = (card, tags) => {
    const target = card.querySelector("[data-gallery-tags]");
    if (!target) return;
    target.replaceChildren();
    if (!tags.length) {
      const empty = document.createElement("span");
      empty.className = "is-empty";
      empty.textContent = "Без тегов";
      target.append(empty);
    } else {
      tags.forEach((tag) => {
        const chip = document.createElement("span");
        chip.dataset.galleryTag = tag;
        chip.textContent = tag;
        target.append(chip);
      });
    }
    rebuildCardSearch(card);
  };
  const renderCardCollections = (card, selectedOptions) => {
    const target = card.querySelector(".media-library-collection-markers");
    if (!target) return;
    target.replaceChildren(...selectedOptions.map((option) => {
      const marker = document.createElement("span");
      marker.dataset.galleryCollectionId = option.value;
      marker.textContent = option.closest("label")?.querySelector("strong")?.textContent || option.value;
      return marker;
    }));
  };

  const closeMetadata = () => { metadataCard = null; closeDialog(metadataDialog); };
  metadataDialog?.querySelectorAll("[data-gallery-metadata-close]").forEach((button) => button.addEventListener("click", closeMetadata));
  root.querySelectorAll("[data-gallery-metadata-open]").forEach((button) => button.addEventListener("click", () => {
    const card = button.closest("[data-gallery-item]");
    if (!card || !metadataForm) return;
    metadataCard = card;
    metadataForm.elements.media_id.value = cardMediaID(card);
    metadataForm.elements.tags.value = cardTags(card).join(", ");
    const selected = cardCollectionIDs(card);
    metadataForm.querySelectorAll('input[name="collection_id"]').forEach((input) => { input.checked = selected.has(input.value); });
    if (metadataStatus) metadataStatus.textContent = "";
    openDialog(metadataDialog, button, metadataForm.elements.tags);
  }));
  metadataForm?.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (!metadataCard) return;
    const submit = metadataForm.querySelector('button[type="submit"]');
    submit.disabled = true;
    if (metadataStatus) metadataStatus.textContent = "Сохраняем...";
    try {
      await postForm(metadataForm.action, new URLSearchParams(new FormData(metadataForm)));
      const tags = metadataForm.elements.tags.value.split(/[,;\n]/).map((tag) => tag.trim()).filter(Boolean).slice(0, 20);
      const selected = [...metadataForm.querySelectorAll('input[name="collection_id"]:checked')];
      renderCardTags(metadataCard, tags);
      renderCardCollections(metadataCard, selected);
      updateCounts();
      applyFilters();
      closeMetadata();
      setStatus("Теги и коллекции сохранены.");
    } catch (error) {
      if (metadataStatus) metadataStatus.textContent = error.message || "Не удалось сохранить изменения";
    } finally {
      submit.disabled = false;
    }
  });

  const addCollectionOption = (id, name) => {
    if (!metadataCollections || metadataCollections.querySelector(`[data-gallery-metadata-collection="${id}"]`)) return;
    metadataCollections.querySelector("[data-gallery-no-collections]")?.remove();
    const label = document.createElement("label");
    label.className = "ui-choice";
    label.dataset.galleryMetadataCollection = id;
    const input = document.createElement("input");
    input.type = "checkbox";
    input.name = "collection_id";
    input.value = id;
    const copy = document.createElement("span");
    const strong = document.createElement("strong");
    strong.textContent = name;
    const small = document.createElement("small");
    small.textContent = "0 результатов";
    copy.append(strong, small);
    label.append(input, copy);
    metadataCollections.append(label);
  };
  const addCollectionRow = (id, name) => {
    if (!collectionList || collectionList.querySelector(`[data-gallery-collection-row="${id}"]`)) return;
    const row = document.createElement("div");
    row.className = "media-library-collection-row";
    row.dataset.galleryCollectionRow = id;
    const filter = document.createElement("button");
    filter.type = "button";
    filter.dataset.galleryCollectionFilter = id;
    filter.setAttribute("aria-pressed", "false");
    const title = document.createElement("span");
    title.textContent = name;
    const count = document.createElement("b");
    count.textContent = "0";
    filter.append(title, count);
    const remove = document.createElement("button");
    remove.type = "button";
    remove.className = "media-library-collection-delete";
    remove.dataset.galleryCollectionDelete = id;
    remove.dataset.collectionName = name;
    remove.setAttribute("aria-label", `Удалить коллекцию ${name}`);
    remove.title = "Удалить коллекцию";
    remove.textContent = "×";
    row.append(filter, remove);
    collectionList.append(row);
  };
  root.querySelector("[data-gallery-collection-create]")?.addEventListener("submit", async (event) => {
    event.preventDefault();
    const form = event.currentTarget;
    const submit = form.querySelector('button[type="submit"]');
    const name = form.elements.name.value.trim();
    if (!name) return;
    submit.disabled = true;
    try {
      const payload = await postForm(form.action, new URLSearchParams(new FormData(form)));
      const collection = payload.collection || {};
      const id = String(collection.id || collection.ID || "");
      const savedName = String(collection.name || collection.Name || name);
      if (!id) throw new Error("Gateway не вернул коллекцию");
      addCollectionRow(id, savedName);
      addCollectionOption(id, savedName);
      form.reset();
      if (collectionHeadingCount) collectionHeadingCount.textContent = String(root.querySelectorAll("[data-gallery-collection-row]").length);
      setStatus(`Коллекция «${savedName}» создана.`);
    } catch (error) {
      setStatus(error.message || "Не удалось создать коллекцию", "error");
    } finally {
      submit.disabled = false;
    }
  });
  root.addEventListener("click", async (event) => {
    const button = event.target.closest("[data-gallery-collection-delete]");
    if (!button) return;
    const id = button.dataset.galleryCollectionDelete;
    const name = button.dataset.collectionName || "коллекцию";
    if (!await requestConfirmation(`Удалить коллекцию «${name}»? Результаты останутся в медиатеке.`, button)) return;
    button.disabled = true;
    try {
      await postForm("/generate/library/collections/delete", new URLSearchParams({ csrf, collection_id: id }));
      root.querySelector(`[data-gallery-collection-row="${id}"]`)?.remove();
      metadataCollections?.querySelector(`[data-gallery-metadata-collection="${id}"]`)?.remove();
      cards().forEach((card) => card.querySelector(`[data-gallery-collection-id="${id}"]`)?.remove());
      if (activeCollection === id) {
        activeCollection = "";
        const all = root.querySelector('[data-gallery-collection-filter=""]');
        all?.classList.add("is-active");
        all?.setAttribute("aria-pressed", "true");
      }
      if (collectionHeadingCount) collectionHeadingCount.textContent = String(root.querySelectorAll("[data-gallery-collection-row]").length);
      updateCounts();
      applyFilters();
      setStatus(`Коллекция «${name}» удалена.`);
    } catch (error) {
      button.disabled = false;
      setStatus(error.message || "Не удалось удалить коллекцию", "error");
    }
  });

  const closeUse = () => closeDialog(useDialog);
  useDialog?.querySelectorAll("[data-gallery-use-close]").forEach((button) => button.addEventListener("click", closeUse));
  const syncUseForm = ({ resetRole = false } = {}) => {
    if (!useForm) return;
    const workflow = useForm.querySelector('input[name="workflow"]:checked');
    const slot = useForm.elements.slot;
    const role = useForm.elements.role;
    const maximum = Number(workflow?.dataset.maxSlots || 1);
    [...slot.options].forEach((option) => {
      const unavailable = Number(option.value) > maximum;
      option.hidden = unavailable;
      option.disabled = unavailable;
    });
    if (Number(slot.value) > maximum) slot.value = "1";
    if (resetRole || !roleTouched) {
      role.value = workflow?.value === "minimax-h3-video"
        ? (slot.value === "1" ? "first_frame" : slot.value === "2" ? "last_frame" : "style")
        : (slot.value === "1" ? "base_scene" : "style");
    }
    const workflowName = workflow?.closest("label")?.querySelector("strong")?.textContent || "выбранный workflow";
    if (useHint) useHint.textContent = `Изображение откроется в слоте ${slot.value}: ${workflowName}.`;
  };
  useForm?.querySelectorAll('input[name="workflow"]').forEach((input) => input.addEventListener("change", () => { roleTouched = false; syncUseForm({ resetRole: true }); }));
  useForm?.elements.slot?.addEventListener("change", () => { if (!roleTouched) syncUseForm({ resetRole: true }); else syncUseForm(); });
  useForm?.elements.role?.addEventListener("change", () => { roleTouched = true; syncUseForm(); });
  root.querySelectorAll("[data-gallery-use-open]").forEach((button) => button.addEventListener("click", () => {
    const card = button.closest("[data-gallery-item]");
    if (!card || !useForm) return;
    useForm.elements.media_id.value = cardMediaID(card);
    const defaultWorkflow = useForm.querySelector('input[name="workflow"]');
    if (!defaultWorkflow) return;
    defaultWorkflow.checked = true;
    useForm.elements.slot.value = "1";
    roleTouched = false;
    syncUseForm({ resetRole: true });
    openDialog(useDialog, button, defaultWorkflow);
  }));
  useForm?.addEventListener("submit", (event) => {
    event.preventDefault();
    const workflow = useForm.querySelector('input[name="workflow"]:checked');
    const mediaID = useForm.elements.media_id.value;
    if (!workflow || !mediaID) return;
    const params = new URLSearchParams({ template: workflow.dataset.templateId, workflow: workflow.value, media: mediaID, slot: useForm.elements.slot.value, role: useForm.elements.role.value });
    window.location.assign(`/generate?${params.toString()}`);
  });

  const closeCompare = () => {
    compareGrid?.querySelectorAll("video").forEach((video) => { video.pause(); video.removeAttribute("src"); video.load(); });
    closeDialog(compareDialog);
  };
  compareDialog?.querySelectorAll("[data-gallery-compare-close]").forEach((button) => button.addEventListener("click", closeCompare));
  root.querySelectorAll("[data-gallery-compare]").forEach((button) => button.addEventListener("click", () => {
    const current = button.closest("[data-gallery-item]");
    if (!current || !compareGrid) return;
    const siblings = cards().filter((card) => card.dataset.variantId === current.dataset.variantId && cardMediaID(card));
    compareGrid.replaceChildren(...siblings.map((card) => {
      const trigger = card.querySelector("[data-gallery-open]");
      const figure = document.createElement("figure");
      const media = document.createElement(trigger?.dataset.mediaType === "video" ? "video" : "img");
      media.src = trigger?.dataset.url || "";
      if (media instanceof HTMLVideoElement) { media.controls = true; media.playsInline = true; media.preload = "metadata"; }
      else media.alt = card.querySelector(".media-library-prompt")?.textContent || "Вариант генерации";
      figure.append(media);
      if (isTrue(card.dataset.sensitive)) {
        figure.classList.add("sensitive-media");
        figure.dataset.sensitiveMedia = "";
        const cover = document.createElement("button");
        cover.type = "button";
        cover.className = "sensitive-media-cover";
        const title = document.createElement("b");
        title.textContent = "Контент 18+";
        const hint = document.createElement("small");
        hint.textContent = "Нажмите, чтобы показать";
        cover.append(title, hint);
        cover.addEventListener("click", () => window.aiGatewaySensitiveContent?.reveal(figure));
        figure.append(cover);
      }
      const caption = document.createElement("figcaption");
      const model = document.createElement("strong");
      model.textContent = card.querySelector(".media-library-model")?.textContent || "Вариант";
      const name = document.createElement("span");
      name.textContent = trigger?.dataset.filename || "Результат";
      caption.append(model, name);
      figure.append(caption);
      return figure;
    }));
    openDialog(compareDialog, button, compareDialog.querySelector("[data-gallery-compare-close]"), closeCompare);
  }));

  root.addEventListener("submit", async (event) => {
    const form = event.target.closest("[data-gallery-hide-form]");
    if (!form) return;
    event.preventDefault();
    const card = form.closest("[data-gallery-item]");
    const button = form.querySelector("button");
    if (!card || !button || button.disabled || !await requestConfirmation("Убрать этот результат из медиатеки?", button)) return;
    button.disabled = true;
    button.textContent = "Убираем...";
    try {
      const payload = await postForm(form.action, new URLSearchParams(new FormData(form)));
      if (!payload.removed) throw new Error("Результат уже недоступен");
      card.remove();
      updateCounts();
      applyFilters();
      setStatus("Результат убран из медиатеки.");
    } catch (error) {
      button.disabled = false;
      button.textContent = "Убрать из медиатеки";
      setStatus(error.message || "Не удалось убрать результат", "error");
    }
  });

  updateCounts();
  applyFilters();
})();
