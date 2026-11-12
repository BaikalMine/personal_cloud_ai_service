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

  const metadataDialog = document.getElementById("gallery-metadata-dialog");
  const metadataForm = metadataDialog?.querySelector("[data-gallery-metadata-form]");
  const metadataStatus = metadataDialog?.querySelector("[data-gallery-metadata-status]");
  const useDialog = document.getElementById("gallery-use-dialog");
  const useForm = useDialog?.querySelector("[data-gallery-use-form]");
  const useHint = useDialog?.querySelector("[data-gallery-use-hint]");
  const confirmDialog = document.getElementById("gallery-confirm-dialog");
  const confirmMessage = confirmDialog?.querySelector("[data-gallery-confirm-message]");
  const confirmAccept = confirmDialog?.querySelector("[data-gallery-confirm-accept]");

  let activeType = "all";
  let activeCollection = "";
  let collectionContext = null;
  let statusTimer = 0;
  let metadataCard = null;
  let metadataEpoch = 0;
  let confirmResolve = null;
  const owner = root.dataset.owner || "";
  const storageKey = /^[1-9][0-9]*$/.test(owner) ? `ai-gateway:gallery:v2:${owner}` : "";
  let restoredMediaID = "";
  let restoring = true;
  const closeMenu = button => { const menu = button.closest(".gallery-card-menu"); if (!menu) return button; menu.open = false; return menu.querySelector("summary"); };
  const collectionsDisclosure = root.querySelector(".media-library-collections");
  if (collectionsDisclosure && window.matchMedia("(max-width: 1100px)").matches) collectionsDisclosure.open = false;
  const saveState = () => {
    if (restoring || !storageKey) return;
    try { sessionStorage.setItem(storageKey, JSON.stringify({ filters: { search: search?.value, from: dateFrom?.value, to: dateTo?.value, type: activeType, collection: activeCollection, flags: [...activeFlags()] }, selected: cards().filter(card => card.querySelector("[data-gallery-select]")?.checked).map(cardMediaID), media: restoredMediaID, scroll: window.scrollY })); } catch (_) {}
  };

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
  [metadataDialog, useDialog, confirmDialog].filter(Boolean).forEach((dialog) => {
    const trap = window.AIGatewayDialogFocus?.createFocusTrap?.({ root: dialog, documentRef: document });
    if (trap) dialogTraps.set(dialog, trap);
    dialog.addEventListener("cancel", event => { event.preventDefault(); if (dialog === confirmDialog) finishConfirmation(false); else if (dialog === metadataDialog) closeMetadata(); else closeDialog(dialog); });
  });
  const closeDialog = (dialog) => {
    if (!dialog || dialog.hidden) return;
    if (dialog.open) dialog.close();
    dialog.hidden = true;
    dialogTraps.get(dialog)?.deactivate();
    if (![metadataDialog, useDialog, confirmDialog].some((item) => item && !item.hidden)) document.body.classList.remove("media-library-dialog-open");
  };
  const openDialog = (dialog, trigger, initialFocus, onEscape) => {
    if (!dialog) return;
    dialog.hidden = false;
    if (!dialog.open) dialog.showModal();
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

  const viewer = window.AIGatewayGallery.createViewer({ root, cards, status: setStatus,
    use: (card, trigger) => openUse(card, trigger), organize: (card, trigger) => openMetadata(card, trigger),
    onSelect: source => { restoredMediaID = source.mediaID; saveState(); },
  });
  root.addEventListener("click", event => {
    const open = event.target.closest("[data-gallery-open], [data-gallery-inspect]");
    if (open) {
      const card = open.closest("[data-gallery-item]");
      const from = closeMenu(open);
      viewer.open(card, from, open.hasAttribute("data-gallery-inspect"));
    }
    const repeat = event.target.closest("[data-gallery-repeat]");
    if (repeat && repeat.closest("[data-gallery-item]").dataset.operation !== "postprocess") window.AIGatewayGeneration?.openRepeat?.(repeat.closest("[data-gallery-item]").dataset.jobId, closeMenu(repeat));
    const repeatNow = event.target.closest("[data-gallery-repeat-now]");
    if (repeatNow && repeatNow.closest("[data-gallery-item]").dataset.operation !== "postprocess") window.AIGatewayGeneration?.openRepeatNow?.(repeatNow.closest("[data-gallery-item]").dataset.jobId, closeMenu(repeatNow));
    const postprocess = event.target.closest("[data-gallery-postprocess]");
    if (postprocess) window.AIGatewayGeneration?.openPostprocess?.(window.AIGatewayGallery.cardSource(postprocess.closest("[data-gallery-item]")), closeMenu(postprocess));
    const compareButton = event.target.closest("[data-gallery-compare]");
    if (compareButton) viewer.compareFrom(compareButton.closest("[data-gallery-item]"), closeMenu(compareButton));
  });
  root.querySelector("[data-gallery-bulk-compare]")?.addEventListener("click", event => {
    viewer.openCompare(cards().filter(card => card.querySelector("[data-gallery-select]")?.checked), event.currentTarget);
  });
  const visiblePreviews = new Set();
  const loadPreview = card => {
    const source = window.AIGatewayGallery.cardSource(card);
    const media = card.querySelector("[data-gallery-preview-src]");
    if (!source || !media) return;
    const unavailable = window.AIGatewayGallery.expired(source, Date.now());
    const concealed = source.sensitive && !card.classList.contains("is-revealed") && !document.body.classList.contains("show-sensitive-content");
    const message = card.querySelector("[data-gallery-preview-message]");
    if (unavailable || concealed || card.hidden) {
      if (media.hasAttribute("src")) { media.pause?.(); media.removeAttribute("src"); media.load?.(); }
      if (message) { message.textContent = unavailable ? "Срок хранения истёк" : ""; message.hidden = !unavailable; }
      return;
    }
    if (!media.hasAttribute("src") && visiblePreviews.has(card)) {
      const url = window.AIGatewayGallery.safeURL(media.dataset.galleryPreviewSrc, location.origin);
      if (url) media.src = url;
    }
  };
  const observer = new IntersectionObserver(entries => entries.forEach(entry => { if (entry.isIntersecting) { visiblePreviews.add(entry.target); loadPreview(entry.target); } else visiblePreviews.delete(entry.target); }), { rootMargin: "200px" });
  cards().forEach(card => {
    observer.observe(card);
    card.querySelector("[data-gallery-preview-src]")?.addEventListener("error", () => { const message = card.querySelector("[data-gallery-preview-message]"); if (message) { message.hidden = false; message.textContent = "Предпросмотр недоступен"; } });
  });
  new MutationObserver(() => cards().forEach(loadPreview)).observe(root, { subtree: true, attributes: true, attributeFilter: ["class"] });
  new MutationObserver(() => cards().forEach(loadPreview)).observe(document.body, { attributes: true, attributeFilter: ["class"] });

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
      const badge = button.querySelector("span:last-child");
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
    const compareButton = root.querySelector("[data-gallery-bulk-compare]"); if (compareButton) compareButton.disabled = selected.length !== 2;
    saveState();
  };

  const applyFilters = () => {
    void collectionContext?.select(activeCollection);
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
      loadPreview(card);
    });
    if (visibleCount) visibleCount.textContent = String(shown);
    if (visibleLabel) visibleLabel.textContent = shown === 1 ? "результат" : "результатов";
    if (filterEmpty) filterEmpty.hidden = shown > 0 || !cards().length;
    const activeHost = root.querySelector("[data-gallery-active-filters]");
    const labels = [query ? `Поиск: ${search.value}` : "", activeType !== "all" ? activeType === "video" ? "Видео" : "Фото" : "", activeCollection ? root.querySelector(`[data-gallery-collection-filter="${activeCollection}"] span`)?.textContent : "", dateFrom?.value ? `С ${dateFrom.value}` : "", dateTo?.value ? `По ${dateTo.value}` : "", ...[...flags].map(flag => ({ favorite: "Избранное", pinned: "Закреплённые", sensitive: "18+", error: "Ошибки" })[flag])].filter(Boolean);
    if (activeHost) {
      activeHost.replaceChildren(); activeHost.hidden = labels.length === 0;
      if (labels.length) { const summary = document.createElement("span"); summary.textContent = labels.join(" · "); const reset = document.createElement("button"); reset.type = "button"; reset.className = "ghost"; reset.textContent = "Сбросить"; reset.addEventListener("click", () => root.querySelector("[data-gallery-reset]")?.click()); activeHost.append(summary, reset); }
    }
    const filterCount = root.querySelector("[data-gallery-filter-count]"); if (filterCount) { filterCount.textContent = String(flags.size + Number(!!dateFrom?.value) + Number(!!dateTo?.value)); filterCount.hidden = filterCount.textContent === "0"; }
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
    const button = event.currentTarget;
    const ids = selectedMediaIDs();
    if (!ids.length || !await requestConfirmation(`Скрыть выбранные результаты (${ids.length})? Они исчезнут из результатов и не будут доступны для повторного использования.`, button)) return;
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
    const label = element.closest("[data-gallery-item]")?.querySelector("[data-gallery-menu-retention]");
    if (label) label.textContent = expiresAt > Date.now() ? `Хранится до ${new Date(expiresAt).toLocaleString("ru-RU")}` : "Срок хранения истёк";
  });
  refreshExpiry();
  window.setInterval(() => { refreshExpiry(); cards().forEach(loadPreview); }, 30000);

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
        const label = button.querySelector("[data-action-label]"); if (label) label.textContent = enabled ? "Обычное хранение" : "Продлить хранение";
        const expiry = card.querySelector("[data-generation-expiry]");
        if (expiry && payload.expires_unix) { expiry.dataset.generationExpiry = String(payload.expires_unix); card.querySelector("[data-gallery-open]").dataset.expires = String(payload.expires_unix); }
        refreshExpiry();
        setStatus(enabled ? `Результат закреплён на ${root.dataset.pinnedRetention}.` : `Возвращён обычный срок хранения: ${root.dataset.mediaRetention}.`);
      } else {
        button.title = enabled ? "Удалить из избранного" : "В избранное"; button.setAttribute("aria-label", button.title);
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
      marker.textContent = metadataCollections.querySelector(`[data-gallery-metadata-collection="${option.value}"] strong`)?.textContent || option.value;
      return marker;
    }));
  };

  const closeMetadata = () => { metadataEpoch++; metadataCard = null; closeDialog(metadataDialog); };
  metadataDialog?.querySelectorAll("[data-gallery-metadata-close]").forEach((button) => button.addEventListener("click", closeMetadata));
  const openMetadata = (card, button) => {
    if (!card || !metadataForm) return;
    metadataEpoch++;
    metadataCard = card;
    metadataForm.elements.media_id.value = cardMediaID(card);
    metadataForm.elements.tags.value = cardTags(card).join(", ");
    const selected = cardCollectionIDs(card);
    metadataForm.querySelectorAll('input[name="collection_id"]').forEach(input => { input.checked = selected.has(input.value); });
    if (metadataStatus) metadataStatus.textContent = "";
    openDialog(metadataDialog, closeMenu(button), metadataForm.elements.tags, closeMetadata);
  };
  root.querySelectorAll("[data-gallery-metadata-open]").forEach(button => button.addEventListener("click", () => openMetadata(button.closest("[data-gallery-item]"), button)));
  metadataForm?.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (!metadataCard) return;
    const targetCard = metadataCard;
    const token = metadataEpoch;
    const body = new URLSearchParams(new FormData(metadataForm));
    const tags = metadataForm.elements.tags.value.split(/[,;\n]/).map(tag => tag.trim()).filter(Boolean).slice(0, 20);
    const selected = [...metadataForm.querySelectorAll('input[name="collection_id"]:checked')].map(input => input.cloneNode(true));
    const submit = metadataForm.querySelector('button[type="submit"]');
    submit.disabled = true;
    if (metadataStatus) metadataStatus.textContent = "Сохраняем...";
    try {
      await postForm(metadataForm.action, body);
      renderCardTags(targetCard, tags);
      renderCardCollections(targetCard, selected);
      updateCounts();
      applyFilters();
      if (metadataEpoch === token) closeMetadata();
      setStatus("Теги и коллекции сохранены.");
    } catch (error) {
      if (metadataStatus && metadataEpoch === token) metadataStatus.textContent = error.message || "Не удалось сохранить изменения";
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
    remove.className = "media-library-collection-delete icon-button ghost";
    remove.dataset.galleryCollectionDelete = id;
    remove.dataset.collectionName = name;
    remove.setAttribute("aria-label", `Удалить коллекцию ${name}`);
    remove.title = "Удалить коллекцию";
    const removeIcon = document.createElement("i"); removeIcon.dataset.lucide = "x"; removeIcon.setAttribute("aria-hidden", "true"); remove.append(removeIcon);
    row.append(filter, remove);
    collectionList.append(row);
    window.lucide?.createIcons();
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
      activeCollection = id;
      root.querySelectorAll("[data-gallery-collection-filter]").forEach(button => { const active = button.dataset.galleryCollectionFilter === id; button.classList.toggle("is-active", active); button.setAttribute("aria-pressed", String(active)); });
      applyFilters();
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

  const useController = window.AIGatewayGallery.createUseController({ root, openDialog, closeDialog, status: setStatus, csrf });
  const openUse = (card, button) => useController.open(card, closeMenu(button));
  root.querySelectorAll("[data-gallery-use-open]").forEach(button => button.addEventListener("click", () => openUse(button.closest("[data-gallery-item]"), button)));

  root.addEventListener("submit", async (event) => {
    const form = event.target.closest("[data-gallery-hide-form]");
    if (!form) return;
    event.preventDefault();
    const card = form.closest("[data-gallery-item]");
    const button = form.querySelector("button");
    if (!card || !button || button.disabled || !await requestConfirmation("Скрыть результат? Он исчезнет из результатов и не будет доступен для повторного использования.", closeMenu(button))) return;
    button.disabled = true;
    const originalButton = button.cloneNode(true);
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
      button.replaceChildren(...originalButton.childNodes);
      setStatus(error.message || "Не удалось убрать результат", "error");
    }
  });

  collectionContext = window.AIGatewayGeneration?.collectionWorkspace?.bindGallery({ root, onRenamed: record => {
    const row = root.querySelector(`[data-gallery-collection-row="${record.id}"]`);
    const label = row?.querySelector("[data-gallery-collection-filter] span");
    if (label) label.textContent = record.name;
    const remove = row?.querySelector("[data-gallery-collection-delete]");
    if (remove) { remove.dataset.collectionName = record.name; remove.setAttribute("aria-label", `Удалить коллекцию ${record.name}`); }
    const option = metadataCollections?.querySelector(`[data-gallery-metadata-collection="${record.id}"]`);
    const optionLabel = option?.querySelector("strong");
    if (optionLabel) optionLabel.textContent = record.name;
    root.querySelectorAll(`[data-gallery-collection-id="${record.id}"]`).forEach(item => { item.textContent = record.name; });
    applyFilters();
  }});
  let restoreScroll = 0;
  try {
    const saved = storageKey ? JSON.parse(sessionStorage.getItem(storageKey) || "null") : null;
    if (saved) {
      const filters = window.AIGatewayGallery.normalizedFilters(saved.filters);
      if (search) search.value = filters.search; if (dateFrom) dateFrom.value = filters.from; if (dateTo) dateTo.value = filters.to;
      activeType = filters.type; activeCollection = filters.collection;
      if (activeCollection && !root.querySelector(`[data-gallery-collection-filter="${activeCollection}"]`)) activeCollection = "";
      typeFilters.forEach(button => { const active = button.dataset.galleryFilter === activeType; button.classList.toggle("is-active", active); button.setAttribute("aria-pressed", String(active)); });
      flagFilters.forEach(button => { const active = filters.flags.includes(button.dataset.galleryFlag); button.classList.toggle("is-active", active); button.setAttribute("aria-pressed", String(active)); });
      root.querySelectorAll("[data-gallery-collection-filter]").forEach(button => { const active = button.dataset.galleryCollectionFilter === activeCollection; button.classList.toggle("is-active", active); button.setAttribute("aria-pressed", String(active)); });
      const selected = new Set(Array.isArray(saved.selected) ? saved.selected.filter(window.AIGatewayGallery.validMediaID) : []);
      cards().forEach(card => { const checkbox = card.querySelector("[data-gallery-select]"); if (checkbox) checkbox.checked = selected.has(checkbox.value); });
      restoredMediaID = window.AIGatewayGallery.validMediaID(saved.media) ? saved.media : "";
      restoreScroll = Number.isFinite(saved.scroll) ? Math.max(0, saved.scroll) : 0;
    }
  } catch (_) {}
  const requestedCollection = new URLSearchParams(location.search).get("collection");
  if (window.AIGatewayGeneration?.collections?.validID(requestedCollection)) {
    activeCollection = requestedCollection;
    root.querySelectorAll("[data-gallery-collection-filter]").forEach(button => { const active = button.dataset.galleryCollectionFilter === activeCollection; button.classList.toggle("is-active", active); button.setAttribute("aria-pressed", String(active)); });
  }
  updateCounts(); applyFilters(); restoring = false;
  requestAnimationFrame(() => window.scrollTo({ top: restoreScroll, behavior: "instant" }));
  window.addEventListener("pagehide", saveState);
  const requested = new URLSearchParams(location.search).get("media");
  if (window.AIGatewayGallery.validMediaID(requested)) { const card = cards().find(item => cardMediaID(item) === requested); if (card) viewer.open(card); else setStatus("Этот результат больше не доступен.", "error"); }
})();
