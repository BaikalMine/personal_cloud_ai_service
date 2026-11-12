(function () {
  "use strict";
  const api = window.AIGatewayGallery;
  if (!api) return;
  const endpoint = "/api/lora-datasets/gallery-import";
  const node = (tag, text) => { const value = document.createElement(tag); if (text !== undefined) value.textContent = text; return value; };
  api.createUseController = function ({ root, openDialog, closeDialog, status, csrf }) {
    const dialog = root.querySelector("#gallery-use-dialog");
    if (!dialog) return { open: () => {} };
    const form = dialog.querySelector("form");
    const q = selector => dialog.querySelector(selector);
    const stateLabel = q("[data-gallery-use-status]"), submit = q("[data-gallery-use-submit]"), retry = q("[data-gallery-use-retry]");
    const recovery = q("[data-gallery-import-recovery]"), link = q("[data-gallery-use-dataset-link]");
    const fields = q("[data-gallery-use-fields]"), datasetFields = q("[data-gallery-dataset-fields]"), destinations = q("[data-gallery-destinations]");
    const storageKey = `ai-gateway:gallery-import:v1:${root.dataset.owner || "none"}`;
    let source = null, presets = [], datasets = [], catalogAbort = null, epoch = 0, busy = false, pending = null;
    const validPending = value => value?.version === 1 && api.validID(value.body?.request_id) && api.validMediaID(value.body?.media_id)
      && typeof value.body.dataset_id === "string" && /^[a-zA-Z0-9_-]{0,96}$/.test(value.body.dataset_id) && Number.isSafeInteger(value.body.revision)
      && value.body.revision >= 0 && typeof value.body.name === "string" && value.body.name.length <= 160;
    try { const stored = JSON.parse(sessionStorage.getItem(storageKey) || "null"); if (validPending(stored)) pending = stored; } catch (_) {}
    const persist = value => {
      try { const raw = JSON.stringify(value); sessionStorage.setItem(storageKey, raw); if (sessionStorage.getItem(storageKey) !== raw) throw new Error(); pending = value; return true; }
      catch (_) { stateLabel.textContent = "Не удалось сохранить идентификатор переноса. Запрос не отправлен."; return false; }
    };
    const clearPending = () => { pending = null; try { sessionStorage.removeItem(storageKey); } catch (_) {} };
    const read = async (url, options = {}) => {
      const response = await fetch(url, { credentials: "same-origin", cache: "no-store", ...options, headers: { Accept: "application/json", ...(options.method === "POST" ? { "Content-Type": "application/json", "X-CSRF-Token": csrf } : {}) } });
      const data = await response.json().catch(() => ({}));
      if (!response.ok) { const error = new Error(data.error || "Не удалось выполнить запрос."); error.status = response.status; throw error; }
      return data;
    };
    const setBusy = value => { busy = value; recovery.querySelectorAll("button").forEach(button => { button.disabled = value; }); submit.disabled = value || !!pending; };
    const showRecovery = message => {
      fields.hidden = datasetFields.hidden = destinations.hidden = true; recovery.hidden = false; retry.hidden = true; submit.disabled = true;
      stateLabel.textContent = message || "Ответ на перенос не получен. Сначала проверьте его результат.";
      q("[data-gallery-use-hint]").textContent = "Повторный запрос использует прежний идентификатор. Отказ запретит запоздалое добавление, если оно ещё не состоялось.";
    };
    function accept(data, requestID) {
      const receipt = data?.receipt;
      if (!receipt || receipt.request_id !== requestID || typeof receipt.cancelled !== "boolean" || !receipt.cancelled && (!/^[a-zA-Z0-9_-]{1,96}$/.test(receipt.dataset_id || "") || !api.validID(receipt.image_id))) throw new Error("Получен некорректный ответ на перенос.");
      clearPending(); recovery.hidden = true; submit.disabled = true;
      q("[data-gallery-use-hint]").textContent = "";
      if (receipt.cancelled) { stateLabel.textContent = "Перенос отменён. Запоздалый запрос не добавит изображение."; return; }
      stateLabel.textContent = receipt.already_present ? "Это изображение уже было в наборе. Повтор не добавлен." : "Изображение добавлено в датасет.";
      link.hidden = data.dataset_available !== true;
      if (data.dataset_available) { link.href = `/train-lora?dataset=${encodeURIComponent(receipt.dataset_id)}&image=${encodeURIComponent(receipt.image_id)}`; }
      else { link.removeAttribute("href"); stateLabel.textContent += " Сам набор больше не доступен."; }
      status(stateLabel.textContent);
    }
    async function check() {
      if (!pending || busy) return;
      const requestID = pending.body.request_id; setBusy(true);
      try { accept(await read(`${endpoint}?request=${encodeURIComponent(requestID)}`), requestID); }
      catch (error) { showRecovery(error.status === 404 ? "Подтверждение пока не найдено. Это не доказывает, что перенос не состоялся." : "Не удалось проверить перенос. Его результат пока неизвестен."); }
      finally { setBusy(false); submit.disabled = true; }
    }
    async function send(discard = false) {
      if (!pending || busy) return;
      const body = pending.body, id = body.request_id; setBusy(true); stateLabel.textContent = discard ? "Проверяем отказ..." : "Добавляем изображение...";
      try { accept(await read(endpoint + (discard ? "/discard" : ""), { method: "POST", body: JSON.stringify(discard ? { request_id: id, media_id: body.media_id } : body) }), id); }
      catch (error) { showRecovery(error.status ? `${error.message} Перенос не подтверждён; проверьте результат или подтвердите отказ.` : "Связь прервалась. Результат переноса пока неизвестен."); }
      finally { setBusy(false); submit.disabled = true; }
    }
    function option(select, value, label) { const item = node("option", label); item.value = value; select.append(item); }
    const kind = () => form.querySelector('input[name="destination"]:checked')?.value || "";
    const eligible = value => presets.filter(preset => value === "image" ? preset.template_id === "image-to-image" : preset.template_id === "minimax-h3-video" && (value === "reference" || preset.frames !== false) && (value !== "last_frame" || preset.maximum >= 2));
    function sync(resetModel = false) {
      const destination = kind(), dataset = destination === "dataset";
      fields.hidden = !destination || dataset; datasetFields.hidden = !dataset;
      for (const input of fields.querySelectorAll("input,select")) input.disabled = fields.hidden;
      for (const input of datasetFields.querySelectorAll("input,select")) input.disabled = datasetFields.hidden;
      form.elements.dataset_name.required = dataset && !form.elements.dataset.value;
      q("[data-gallery-dataset-name-field]").hidden = !!form.elements.dataset.value;
      form.elements.dataset_name.disabled = !dataset || !!form.elements.dataset.value;
      if (!dataset) {
        const matching = eligible(destination), previous = form.elements.workflow.value;
        if (resetModel) { form.elements.workflow.replaceChildren(); matching.forEach(preset => option(form.elements.workflow, preset.key || preset.id, preset.name)); if (matching.some(preset => (preset.key || preset.id) === previous)) form.elements.workflow.value = previous; }
        const preset = matching.find(item => (item.key || item.id) === form.elements.workflow.value);
        const oldSlot = form.elements.slot.value; form.elements.slot.replaceChildren();
        if (destination === "first_frame" || destination === "last_frame") option(form.elements.slot, destination === "first_frame" ? "1" : "2", destination === "first_frame" ? "Первый кадр" : "Последний кадр");
        else for (let index = 1; index <= (preset?.maximum || 0); index++) option(form.elements.slot, String(index), `Слот ${index}`);
        if ([...form.elements.slot.options].some(item => item.value === oldSlot)) form.elements.slot.value = oldSlot;
        const fixed = destination === "first_frame" || destination === "last_frame" || destination === "image" && form.elements.slot.value === "1";
        q("[data-gallery-role-field]").hidden = fixed; form.elements.role.disabled = fixed;
        q("[data-gallery-use-hint]").textContent = "";
        submit.textContent = "Открыть в редакторе"; submit.disabled = !preset || !form.elements.slot.value || !!pending;
      } else { submit.textContent = "Добавить в датасет"; submit.disabled = !source || !!pending; q("[data-gallery-use-hint]").textContent = ""; }
    }
    function destination(value, label) {
      const row = node("label"); row.className = "ui-choice";
      const radio = node("input"); radio.type = "radio"; radio.name = "destination"; radio.value = value;
      const copy = node("span"); copy.append(node("strong", label)); row.append(radio, copy); destinations.append(row);
      radio.addEventListener("change", () => sync(true));
    }
    async function loadDestinations() {
      const token = ++epoch; catalogAbort?.abort(); catalogAbort = new AbortController();
      fields.hidden = datasetFields.hidden = recovery.hidden = destinations.hidden = true; submit.disabled = true; retry.hidden = true; link.hidden = true;
      stateLabel.textContent = "Проверяем доступные назначения...";
      try {
        const data = await read(`/generate/library/destinations?media=${encodeURIComponent(source.mediaID)}`, { signal: catalogAbort.signal });
        if (token !== epoch) return;
        if (data.media_id !== source.mediaID || !Array.isArray(data.presets)) throw new Error("Не удалось проверить назначения.");
        presets = data.presets.filter(item => typeof item.id === "string" && typeof item.name === "string" && ["image-to-image", "minimax-h3-video"].includes(item.template_id) && Number.isInteger(item.maximum) && item.maximum > 0 && item.maximum <= 16);
        datasets = [];
        let datasetError = false;
        if (data.can_dataset) {
          try { const result = await read("/api/lora-datasets", { signal: catalogAbort.signal }); datasets = Array.isArray(result.datasets) ? result.datasets : []; }
          catch (_) { datasetError = true; }
        }
        if (token !== epoch) return;
        destinations.replaceChildren(node("legend", "Куда добавить"));
        if (eligible("image").length) destination("image", "Редактирование изображения");
        if (eligible("first_frame").length) destination("first_frame", "Видео: первый кадр");
        if (eligible("reference").length) destination("reference", "Видео: референс");
        if (eligible("last_frame").length) destination("last_frame", "Видео: последний кадр");
        if (data.can_dataset && !datasetError) destination("dataset", "Датасет LoRA");
        form.elements.dataset.replaceChildren(); option(form.elements.dataset, "", "Новый набор");
        datasets.forEach(dataset => { if (typeof dataset.id === "string" && /^[a-zA-Z0-9_-]{1,96}$/.test(dataset.id) && Number.isSafeInteger(dataset.revision)) option(form.elements.dataset, dataset.id, `${dataset.name} · ${dataset.image_count} фото`); });
        const first = destinations.querySelector("input"); destinations.hidden = !first;
        stateLabel.textContent = datasetError ? "Не удалось загрузить датасеты. Другие назначения доступны." : !first ? "Сейчас нет доступных назначений для этого изображения." : !data.catalog_online ? "Сервис генерации недоступен. Модели показаны по последним известным данным." : "";
        retry.hidden = !datasetError && !!first;
        if (first) { first.checked = true; sync(true); }
      } catch (error) { if (token !== epoch) return; stateLabel.textContent = error.status === 404 ? "Изображение больше не доступно." : "Не удалось проверить доступные назначения."; retry.hidden = false; }
    }
    function open(card, trigger) {
      epoch++; catalogAbort?.abort(); source = api.cardSource(card); if (!source && !pending) return;
      link.hidden = true;
      q("[data-gallery-use-source]").textContent = pending ? "Незавершённый перенос изображения" : source.filename;
      openDialog(dialog, trigger, q("[data-gallery-use-close]"));
      if (pending) { showRecovery(); void check(); }
      else void loadDestinations();
    }
    function close() { epoch++; catalogAbort?.abort(); closeDialog(dialog); }
    dialog.querySelectorAll("[data-gallery-use-close]").forEach(button => button.addEventListener("click", close));
    dialog.addEventListener("cancel", () => { epoch++; catalogAbort?.abort(); });
    retry.addEventListener("click", () => { if (source && !pending) void loadDestinations(); });
    q("[data-gallery-import-check]").addEventListener("click", check);
    q("[data-gallery-import-resend]").addEventListener("click", () => send());
    q("[data-gallery-import-discard]").addEventListener("click", () => send(true));
    form.elements.workflow.addEventListener("change", () => sync()); form.elements.slot.addEventListener("change", () => sync()); form.elements.dataset.addEventListener("change", () => sync());
    form.addEventListener("submit", event => {
      event.preventDefault(); if (pending || busy || !source || submit.disabled) return;
      if (api.expired(source, Date.now())) { stateLabel.textContent = "Срок хранения изображения истёк."; submit.disabled = true; return; }
      const destination = kind();
      if (destination === "dataset") {
        const dataset = datasets.find(item => item.id === form.elements.dataset.value);
        if (form.elements.dataset.value && !dataset) return;
        const bytes = new Uint8Array(18);
        crypto.getRandomValues(bytes);
        const requestID = Array.from(bytes, byte => byte.toString(16).padStart(2, "0")).join("");
        const value = { version: 1, body: { request_id: requestID, media_id: source.mediaID, dataset_id: dataset?.id || "", revision: dataset?.revision || 0, name: dataset ? "" : form.elements.dataset_name.value.trim() } };
        if (persist(value)) { showRecovery(); void send(); }
      } else {
        const preset = eligible(destination).find(item => (item.key || item.id) === form.elements.workflow.value), slot = Number(form.elements.slot.value);
        if (!preset || !Number.isInteger(slot) || slot < 1 || slot > preset.maximum) return;
        const role = ["first_frame", "last_frame"].includes(destination) ? destination : destination === "image" && slot === 1 ? "base_scene" : form.elements.role.value;
        const params = new URLSearchParams({ template: preset.template_id, workflow: preset.id, media: source.mediaID, slot: String(slot), role });
        if (preset.model_id) params.set("library_model", preset.model_id);
        location.assign(`/generate?${params}`);
      }
    });
    if (pending) queueMicrotask(() => open(null, root.querySelector("h1")));
    return { open };
  };
})();
