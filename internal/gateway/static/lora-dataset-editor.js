(() => {
  const page = document.querySelector("[data-lora-training-page]");
  if (!page || !window.AIGatewayLoraDataset || !window.AIGatewayLoraCaptions) return;
  const captions = window.AIGatewayLoraCaptions;
  const find = (selector) => page.querySelector(selector);
  const form = find("[data-lora-training-form]");
  const grid = find("[data-lora-dataset-grid]");
  const fileInput = find("[data-lora-images]");
  const select = find("[data-dataset-select]");
  const dialog = find("[data-dataset-dialog]");
  const dialogContent = find("[data-dataset-dialog-content]");
  const csrf = page.dataset.csrf;
  const fields = ["name", "output_name", "trigger_word", "concept_type", "profile_id", "preset", "resolution", "global_caption"];
  const settings = () => Object.fromEntries(fields.map((name) => {
    const value = new FormData(form).get(name) || "";
    return [name, name === "resolution" ? Number(value) : value];
  }));
  const defaults = { version: 1, settings: settings(), images: [] };
  const files = new Map();
  const urls = new Map();
  const captionStates = new Map();
  const cards = new Map();
  let operation = "";
  let uploading = false;
  let captionAction = false;
  let captionPoller;
  let previousTrigger = defaults.settings.trigger_word;
  let outputEdited = false;
  const id = () => crypto.randomUUID();
  const formatBytes = (bytes) => `${((Number(bytes) || 0) / 1048576).toFixed(1)} МБ`;
  const plural = (count) => `${count} ${count % 10 === 1 && count % 100 !== 11 ? "изображение" : count % 10 >= 2 && count % 10 <= 4 && !(count % 100 >= 12 && count % 100 <= 14) ? "изображения" : "изображений"}`;
  const node = (tag, text, className = "") => { const result = document.createElement(tag); result.textContent = text; result.className = className; return result; };
  const icons = () => window.lucide?.createIcons({ attrs: { "aria-hidden": "true", width: 18, height: 18 } });
  const iconButton = (icon, label, action, extra = "") => {
    const button = node("button", "", `ghost dataset-icon ${extra}`);
    button.type = "button"; button.title = label; button.setAttribute("aria-label", label);
    const mark = document.createElement("i"); mark.dataset.lucide = icon; button.append(mark);
    button.addEventListener("click", action); return button;
  };
  const feedback = (message, error = false) => {
    const target = find("[data-dataset-feedback]"); target.hidden = !message; target.textContent = message;
    target.classList.toggle("is-error", error); target.classList.toggle("is-success", !error);
    const modalStatus = dialogContent.querySelector("[data-dialog-status]");
    if (modalStatus) { modalStatus.textContent = message; modalStatus.classList.toggle("is-error", error); }
  };
  const request = async (suffix, method = "GET", data, options = {}) => {
    const multipart = data instanceof FormData;
    const response = await fetch(`/api/lora-datasets${suffix}`, {
      method, credentials: "same-origin", cache: "no-store",
      headers: { Accept: "application/json", ...(method === "POST" ? { "X-CSRF-Token": csrf } : {}),
        ...(!multipart && data !== undefined ? { "Content-Type": "application/json" } : {}), ...options.headers },
      body: data === undefined ? undefined : multipart ? data : JSON.stringify(data),
      signal: options.signal ? AbortSignal.any([options.signal, AbortSignal.timeout(15000)]) : AbortSignal.timeout(multipart ? 900000 : 60000),
    });
    const payload = await response.json().catch(() => ({}));
    if (!response.ok) { const error = new Error(payload.error || `Сервис ответил HTTP ${response.status}.`); error.status = response.status; throw error; }
    return payload;
  };
  const controller = window.AIGatewayLoraDataset.createController({ request, defaults, onChange: (state, kind) => {
    if (kind === "load") {
      for (const name of fields) for (const input of form.querySelectorAll(`[name="${name}"]`)) {
        if (input.type === "radio") input.checked = input.value === String(state.manifest.settings[name]);
        else input.value = state.manifest.settings[name] ?? "";
      }
      previousTrigger = state.manifest.settings.trigger_word;
      outputEdited = Boolean(state.manifest.settings.output_name);
      captionStates.clear(); captionPoller?.select(state.dataset?.id, true); renderItems();
    }
    if (kind === "items") renderItems();
    if (kind === "list" || kind === "load") {
      captionPoller?.select(state.dataset?.id);
      select.replaceChildren(...[...(!state.dataset ? [{ id: "", name: "Новый набор" }] : []), ...state.datasets].map((item) => new Option(item.name || "Без названия", item.id)));
      if (state.dataset && ![...select.options].some((item) => item.value === state.dataset.id)) select.add(new Option(state.dataset.name || "Без названия", state.dataset.id));
      select.value = state.dataset?.id || "";
    }
    renderState();
  } });
  const state = controller.state;
  const busy = () => Boolean(operation || uploading || captionAction || state.status === "loading");
  const captionJobs = () => captionPoller && captionPoller.state.datasetID === state.dataset?.id ? captionPoller.state.jobs : [];
  const captionActive = () => captionJobs().some(captions.active);
  const renderState = () => {
    const blocked = busy();
    const dirtyFiles = files.size > 0;
    const messages = { loading: "Загружаем наборы…", empty: "Новый набор", dirty: "Есть несохранённые правки", saving: "Сохраняем…", saved: "Сохранено", error: "Не удалось сохранить", conflict: "Конфликт изменений" };
    find("[data-dataset-status]").textContent = uploading ? "Загружаем изображения…" : state.error || (dirtyFiles ? "Есть незагруженные изображения" : messages[state.status]);
    find("[data-dataset-save-state]").dataset.state = dirtyFiles ? "dirty" : state.status;
    find("[data-dataset-retention]").textContent = state.dataset ? `Хранится до ${new Date(state.dataset.expires_at).toLocaleDateString("ru-RU")}` : "";
    find("[data-dataset-conflict]").hidden = state.status !== "conflict";
    select.disabled = blocked || dirtyFiles;
    for (const button of page.querySelectorAll("[data-dataset-toolbar] button")) button.disabled = blocked;
    find("[data-dataset-new]").disabled ||= dirtyFiles;
    find("[data-dataset-delete]").disabled ||= !state.dataset || dirtyFiles;
    find("[data-dataset-export]").disabled ||= !state.manifest.images.length || dirtyFiles;
    for (const fieldset of form.querySelectorAll("fieldset")) fieldset.disabled = Boolean(operation || !state.ready);
    fileInput.disabled = blocked || !state.ready;
    find("[data-dataset-gallery]").disabled = blocked || !state.ready;
    find("[data-lora-clear-images]").disabled = blocked;
    find("[data-lora-caption-all]").disabled = blocked || (!captionActive() && (!state.manifest.images.some((item) => !item.excluded) || dirtyFiles || state.status === "conflict"));
    find("[data-lora-caption-all]").textContent = captionActive() ? "Остановить серию" : "Описать пустые";
    find("[data-lora-caption-assistant]").setAttribute("aria-busy", String(captionActive()));
    const submit = find("[data-lora-submit]");
    submit.disabled = blocked || !state.ready || dirtyFiles || captionActive() || (Boolean(state.dataset) && !captionPoller?.state.ready) || state.status === "conflict" || submit.dataset.agentReady !== "true";
    submit.textContent = operation === "train" ? "Подготавливаем обучение…" : "Запустить обучение";
    const latestJobs = captions.latest(captionJobs());
    for (const [key, card] of cards) {
      for (const button of card.querySelectorAll("button")) button.disabled = blocked;
      const describe = card.querySelector("[data-lora-caption-one]");
      const job = latestJobs.get(key);
      const item = state.manifest.images.find((image) => image.id === key);
      describe.disabled ||= !item?.asset_id || (!captions.active(job) && (item?.excluded || state.status === "conflict"));
      const label = captions.active(job) ? "Отменить описание кадра" : ["failed", "cancelled"].includes(job?.state) ? "Повторить описание кадра" : "Описать кадр";
      if (describe.getAttribute("aria-label") !== label) {
        describe.title = label; describe.setAttribute("aria-label", label);
        const mark = document.createElement("i"); mark.dataset.lucide = captions.active(job) ? "square" : ["failed", "cancelled"].includes(job?.state) ? "rotate-cw" : "sparkles"; describe.replaceChildren(mark);
      }
    }
    icons();
  };
  const assetFor = (item) => state.assets[item.asset_id] || {};
  const itemName = (item) => files.get(item.id)?.name || assetFor(item).name || "Изображение";
  const captionMessage = (text, error = false) => {
    const target = find("[data-lora-caption-status]"); target.textContent = text; target.classList.toggle("is-error", error);
  };
  const renderItems = () => {
    for (const [key, card] of cards) if (!state.manifest.images.some((item) => item.id === key)) { card.remove(); cards.delete(key); if (urls.has(key)) URL.revokeObjectURL(urls.get(key)); urls.delete(key); }
    const seen = new Set();
    const latestJobs = captions.latest(captionJobs());
    state.manifest.images.forEach((item, index) => {
      let card = cards.get(item.id);
      if (!card) {
        card = node("article", "", "lora-dataset-item"); card.dataset.fileKey = item.id;
        const media = node("div", "", "lora-dataset-media");
        const img = document.createElement("img"); img.loading = "lazy"; img.decoding = "async";
        media.append(img, node("span", ""));
        const body = node("div", "", "lora-dataset-body");
        const heading = node("div", "", "lora-dataset-heading"); const identity = node("div", ""); identity.append(node("strong", ""), node("small", ""));
        const describe = iconButton("sparkles", "Описать кадр", () => void describeImages([item.id])); describe.dataset.loraCaptionOne = "";
        heading.append(identity, describe);
        const caption = document.createElement("textarea"); caption.name = "caption"; caption.rows = 4; caption.maxLength = 1000; caption.placeholder = "Ракурс, одежда, фон, свет";
        caption.addEventListener("input", () => { const current = state.manifest.images.find((image) => image.id === item.id); if (!current) return; current.caption = caption.value; current.caption_revision = id(); delete current.caption_job_id; caption.setCustomValidity(""); captionStates.set(item.id, "Изменено вручную"); card.querySelector("[data-lora-caption-item-status]").textContent = "Изменено вручную"; controller.touch(); });
        const status = node("small", "", "lora-caption-item-status"); status.dataset.loraCaptionItemStatus = "";
        body.append(heading, caption, status);
        const footer = node("footer", "", "dataset-item-actions");
        const enabled = node("label", "", "dataset-include"); const toggle = document.createElement("input"); toggle.type = "checkbox";
        toggle.addEventListener("change", () => { const current = state.manifest.images.find((image) => image.id === item.id); current.excluded = !toggle.checked; controller.touch("items"); }); enabled.append(toggle, node("span", "В обучении"));
        const controls = node("div", "");
        controls.append(iconButton("arrow-up", "Переместить выше", () => move(item.id, -1)), iconButton("arrow-down", "Переместить ниже", () => move(item.id, 1)), iconButton("trash-2", "Убрать изображение", () => remove(item.id), "lora-dataset-remove"));
        footer.append(enabled, controls); card.append(media, body, footer); cards.set(item.id, card);
      }
      if (grid.children[index] !== card) grid.insertBefore(card, grid.children[index] || null);
      const asset = assetFor(item); const file = files.get(item.id);
      const image = card.querySelector("img");
      if (file && !urls.has(item.id)) urls.set(item.id, URL.createObjectURL(file));
      const src = urls.get(item.id) || `/api/lora-datasets/assets/${encodeURIComponent(item.asset_id)}`;
      if (image.getAttribute("src") !== src) image.src = src;
      image.alt = `Кадр ${index + 1}: ${itemName(item)}`;
      card.querySelector(".lora-dataset-media > span").textContent = String(index + 1).padStart(2, "0");
      const title = card.querySelector("strong"); title.textContent = itemName(item); title.title = itemName(item);
      card.querySelector(".lora-dataset-heading small").textContent = `${asset.width ? `${asset.width} × ${asset.height} · ` : ""}${formatBytes(file?.size || asset.size_bytes)}`;
      const caption = card.querySelector("textarea"); if (caption.value !== item.caption) caption.value = item.caption;
      caption.setAttribute("aria-label", `Описание кадра ${index + 1}`);
      card.querySelector('input[type="checkbox"]').checked = !item.excluded;
      card.classList.toggle("is-excluded", item.excluded);
      const warnings = [];
      if (!item.asset_id) warnings.push("Фото ещё не сохранено");
      if (asset.sha256 && seen.has(asset.sha256)) warnings.push("Точный дубликат");
      if (asset.sha256) seen.add(asset.sha256);
      if (asset.width && Math.min(asset.width, asset.height) < state.manifest.settings.resolution) warnings.push("Меньше рабочего разрешения");
      if (!item.excluded && !item.caption.trim() && !state.manifest.settings.global_caption.trim()) warnings.push("Нет подписи");
      const job = latestJobs.get(item.id);
      let jobMessage = "";
      if (captions.active(job)) jobMessage = job.cancel_requested ? "Отменяем описание…" : job.state === "running" ? "Ассистент анализирует этот кадр" : "Описание в очереди";
      else if (job?.state === "completed") jobMessage = item.caption_job_id === job.job_id ? "Описание готово" : "Ответ не применён: кадр или подпись изменены.";
      else if (job?.state === "failed") jobMessage = job.error || "Описание не удалось. Повторите этот кадр.";
      else if (job?.state === "cancelled") jobMessage = "Описание отменено";
      const itemStatus = card.querySelector("[data-lora-caption-item-status]");
      itemStatus.textContent = jobMessage || captionStates.get(item.id) || warnings.join(" · ") || "Готово к обучению";
      itemStatus.classList.toggle("is-error", job?.state === "failed");
    });
    const images = state.manifest.images;
    find("[data-lora-dataset-summary]").hidden = !images.length;
    find("[data-dataset-empty]").hidden = Boolean(images.length);
    find("[data-lora-image-count]").textContent = plural(images.length);
    const total = images.reduce((sum, item) => sum + (files.get(item.id)?.size || assetFor(item).size_bytes || 0), 0);
    find("[data-lora-image-size]").textContent = `${formatBytes(total)} · В обучении: ${images.filter((item) => !item.excluded).length}`;
    icons(); renderState();
  };
  const move = (key, offset) => {
    if (busy()) return;
    const images = state.manifest.images; const index = images.findIndex((item) => item.id === key); const target = index + offset;
    if (target < 0 || target >= images.length) return;
    [images[index], images[target]] = [images[target], images[index]]; controller.touch("items");
    cards.get(key)?.querySelector(`[aria-label="${offset < 0 ? "Переместить выше" : "Переместить ниже"}"]`)?.focus();
  };
  const remove = (key) => {
    if (busy()) return;
    const item = state.manifest.images.find((image) => image.id === key);
    if (item?.caption && !window.confirm("Убрать изображение вместе с его подписью из этого набора?")) return;
    files.delete(key); captionStates.delete(key); state.manifest.images = state.manifest.images.filter((image) => image.id !== key); controller.touch("items");
  };
  const uploadFiles = async () => {
    if (uploading || !files.size) return;
    uploading = true; renderState();
    try {
      const datasetID = await controller.ensure(); if (!datasetID) return;
      for (const [key, file] of files) {
        const item = state.manifest.images.find((image) => image.id === key); if (!item) { files.delete(key); continue; }
        try {
          const body = new FormData(); body.append("image", file, file.name);
          const result = await request(`/${datasetID}/assets`, "POST", body);
          item.asset_id = result.asset.id; state.assets[result.asset.id] = result.asset; files.delete(key); captionStates.delete(key);
          controller.touch("items"); if (!await controller.flush()) break;
        } catch (error) { captionStates.set(key, error.message); feedback(`${file.name}: ${error.message}`, true); break; }
      }
    } finally { uploading = false; renderItems(); }
  };
  const addFiles = async (incoming) => {
    if (busy()) return;
    const accepted = [...incoming];
    if (state.manifest.images.length + accepted.length > 100) { feedback("В одном наборе может быть не больше 100 изображений.", true); return; }
    const currentBytes = state.manifest.images.reduce((sum, item) => sum + (files.get(item.id)?.size || assetFor(item).size_bytes || 0), 0);
    if (currentBytes + accepted.reduce((sum, file) => sum + file.size, 0) > 512 * 1048576) { feedback("Набор превышает 512 МБ.", true); return; }
    for (const file of accepted) {
      if (!["image/png", "image/jpeg", "image/webp"].includes(file.type) || file.size > 24 * 1048576) { feedback(`${file.name}: нужен PNG, JPG или WebP до 24 МБ.`, true); continue; }
      const key = id(); files.set(key, file); state.manifest.images.push({ id: key, asset_id: "", caption: "", excluded: false });
    }
    controller.touch("items"); await uploadFiles();
  };
  const safely = async (kind, work) => {
    if (busy()) return;
    operation = kind; renderState();
    try { await work(); } catch (error) { feedback(error.message || "Не удалось выполнить действие.", true); }
    finally { operation = ""; applyCaptionResults(); renderState(); }
  };
  const flush = async () => {
    if (files.size) { feedback("Сначала повторите загрузку фотографий кнопкой сохранения или уберите их из набора.", true); return false; }
    return controller.flush();
  };
  const download = async (suffix) => {
    const link = document.createElement("a"); link.href = `/api/lora-datasets${suffix}/export`; link.download = "dataset.zip";
    document.body.append(link); link.click(); link.remove();
  };
  const openDialog = (title) => {
    find("[data-dataset-dialog-title]").textContent = title;
    dialogContent.replaceChildren(); const status = node("p", "Загрузка…", "dataset-dialog-status"); status.dataset.dialogStatus = ""; status.setAttribute("role", "status"); dialogContent.append(status);
    if (!dialog.open) dialog.showModal();
  };
  const showVersions = async (all = false) => {
    openDialog("Версии набора");
    try {
      const result = await request(`/versions${!all && state.dataset ? `?dataset_id=${state.dataset.id}` : ""}`);
      const status = dialogContent.firstElementChild; status.textContent = "Версии хранятся 30 дней. Используемые обучением сохраняются до удаления его записи.";
      const toolbar = node("div", "", "dataset-version-tools");
      const filter = document.createElement("select"); filter.setAttribute("aria-label", "Наборы в истории версий");
      filter.append(new Option("Этот набор", "current"), new Option("Все наборы", "all")); filter.value = all || !state.dataset ? "all" : "current";
      filter.addEventListener("change", () => void showVersions(filter.value === "all"));
      const save = node("button", "Зафиксировать версию"); save.type = "button"; save.disabled = !state.manifest.images.length;
      save.addEventListener("click", () => void safely("version", async () => { if (!await flush()) return; await request(`/${state.dataset.id}/versions`, "POST", { revision: state.dataset.revision }); await showVersions(all); }));
      toolbar.append(filter, save); dialogContent.append(toolbar);
      if (!result.versions.length) dialogContent.append(node("p", "Сохранённых версий пока нет.", "dataset-empty"));
      for (const version of result.versions) {
        const row = node("div", "", "dataset-version-row"); const info = node("div", "");
        info.append(node("strong", version.name || "Без названия"), node("small", `${new Date(version.created_at).toLocaleString("ru-RU")} · ${plural(version.image_count)}`), node("small", `Хранится до ${new Date(version.expires_at).toLocaleDateString("ru-RU")} или пока связана с обучением`));
        const actions = node("div", ""); const restore = node("button", "Восстановить копию", "ghost"); restore.type = "button";
        restore.addEventListener("click", () => void safely("restore", async () => { if (!await flush()) return; const restored = await request(`/versions/${version.id}/restore`, "POST", {}); controller.apply(restored); await controller.refreshList(); dialog.close(); feedback("Версия восстановлена в отдельный набор."); }));
        actions.append(restore, iconButton("download", "Скачать версию ZIP", () => void download(`/versions/${version.id}`)), iconButton("trash-2", "Удалить версию", () => void safely("delete-version", async () => {
          if (!window.confirm("Удалить эту сохранённую версию? Рабочий набор останется.")) return;
          await request(`/versions/${version.id}/delete`, "POST", {}); await showVersions(all);
        })));
        row.append(info, actions); dialogContent.append(row);
      }
      icons();
    } catch (error) { feedback(error.message, true); }
  };
  const showGallery = async () => {
    openDialog("Из моих генераций");
    try {
      const result = await request("/gallery"); dialogContent.firstElementChild.textContent = result.images.length ? "" : "Сохранённых фотографий пока нет.";
      const list = node("div", "", "dataset-gallery-grid"); dialogContent.append(list);
      for (const image of result.images) {
        const button = node("button", image.sensitive ? "Показать фото" : "", "ghost dataset-gallery-item"); button.type = "button";
        let revealed = !image.sensitive;
        const reveal = () => { const preview = document.createElement("img"); preview.src = image.url; preview.alt = ""; preview.loading = "lazy"; button.replaceChildren(preview, node("span", image.filename)); button.setAttribute("aria-label", `Добавить ${image.filename}`); };
        if (revealed) reveal();
        button.addEventListener("click", () => { if (!revealed) { revealed = true; reveal(); return; } void safely("reuse", async () => {
          if (state.manifest.images.length >= 100) throw new Error("В наборе уже 100 изображений.");
          const datasetID = await controller.ensure(); if (!datasetID) return;
          const result = await request(`/${datasetID}/reuse`, "POST", { media_id: image.id }); state.assets[result.asset.id] = result.asset;
          state.manifest.images.push({ id: id(), asset_id: result.asset.id, caption: "", excluded: false }); controller.touch("items"); await controller.flush(); button.disabled = true; button.classList.add("is-selected");
          dialogContent.firstElementChild.textContent = `Добавлено: ${image.filename}`;
        }); });
        list.append(button);
      }
    } catch (error) { feedback(error.message, true); }
  };
  const applyCaptionResults = () => {
    if (!state.ready || busy() || state.status === "conflict") return;
    if (captions.reconcile(state.manifest, captionJobs(), id)) controller.touch("items");
  };
  const renderCaptionSummary = () => {
    const queue = captionPoller.state;
    if (queue.error) { captionMessage(`Не удалось проверить описания. Повтор через ${queue.retrySeconds} сек.`, true); return; }
    const jobs = [...captions.latest(captionJobs()).values()].filter((job) => state.manifest.images.some((item) => item.id === job.image_id));
    if (!jobs.length) { captionMessage(state.manifest.images.length ? `Пустых подписей: ${state.manifest.images.filter(item => !item.excluded && !item.caption.trim()).length}.` : "Добавьте изображения и укажите триггер."); return; }
    const completed = jobs.filter((job) => job.state === "completed").length;
    const waiting = jobs.filter(captions.active).length;
    const failed = jobs.filter((job) => job.state === "failed").length;
    const cancelled = jobs.filter((job) => job.state === "cancelled").length;
    captionMessage(`Готово ${completed} из ${jobs.length}.${waiting ? ` В работе и очереди: ${waiting}.` : ""}${failed ? ` Ошибок: ${failed}. Повторите нужный кадр.` : ""}${cancelled ? ` Отменено: ${cancelled}.` : ""}`, Boolean(failed));
  };
  captionPoller = captions.createPoller({
    request: (datasetID, signal) => request(`/${datasetID}/captions`, "GET", undefined, { signal }),
    onChange: () => { applyCaptionResults(); renderItems(); renderCaptionSummary(); },
  });
  const captionJobAction = async (job, action) => {
    const response = await fetch(`/api/lora-training/caption/${encodeURIComponent(job.job_id)}/${action}`, {
      method: "POST", credentials: "same-origin", headers: { Accept: "application/json", "X-CSRF-Token": csrf }, signal: AbortSignal.timeout(60000),
    });
    const payload = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(payload.error || `Сервис ответил HTTP ${response.status}.`);
    return payload;
  };
  const describeImages = async (keys, onlyEmpty = false, cancelSeries = false) => {
    if (busy() || files.size) return;
    const one = keys.length === 1 && !onlyEmpty ? captions.latest(captionJobs()).get(keys[0]) : null;
    const cancelling = cancelSeries || captions.active(one);
    if (!cancelling) {
      const trigger = state.manifest.settings.trigger_word.trim();
      if (trigger.length < 2 || trigger.length > 80) { captionMessage("Сначала укажите триггер длиной 2–80 символов.", true); form.querySelector('[name="trigger_word"]').focus(); return; }
      if (!keys.length) { captionMessage("Пустых подписей нет."); return; }
    }
    captionAction = true; renderState();
    try {
      if (cancelSeries) await request(`/${state.dataset.id}/captions`, "POST", { cancel: true });
      else if (captions.active(one)) await captionJobAction(one, "cancel");
      else {
        if (!await flush()) return;
        const item = state.manifest.images.find((image) => image.id === keys[0]);
        if (one && ["failed", "cancelled"].includes(one.state) && captions.matches(state.manifest, item, one)) await captionJobAction(one, "retry");
        else await request(`/${state.dataset.id}/captions`, "POST", { revision: state.dataset.revision, image_ids: keys, only_empty: onlyEmpty });
      }
      await captionPoller.refresh();
    } catch (error) {
      feedback(`${error.message} Состояние заданий будет проверено повторно.`, true);
      await captionPoller.refresh();
    } finally { captionAction = false; applyCaptionResults(); renderItems(); renderCaptionSummary(); }
  };
  fileInput.addEventListener("change", () => { const incoming = [...fileInput.files]; fileInput.value = ""; void addFiles(incoming); });
  const dropzone = find("[data-lora-dropzone]");
  for (const eventName of ["dragenter", "dragover", "dragleave", "drop"]) dropzone.addEventListener(eventName, (event) => { event.preventDefault(); dropzone.classList.toggle("is-dragging", eventName === "dragenter" || eventName === "dragover"); if (eventName === "drop") void addFiles(event.dataTransfer?.files || []); });
  form.addEventListener("input", (event) => {
    if (!fields.includes(event.target.name)) return;
    if (["trigger_word", "concept_type"].includes(event.target.name)) for (const item of state.manifest.images) { item.caption_revision = id(); delete item.caption_job_id; }
    if (event.target.name === "output_name") outputEdited = Boolean(event.target.value);
    if (event.target.name === "name" && !outputEdited) {
      const from = "а б в г д е ё ж з и й к л м н о п р с т у ф х ц ч ш щ ъ ы ь э ю я".split(" "); const to = "a b v g d e e zh z i y k l m n o p r s t u f h ts ch sh sch _ y _ e yu ya".split(" ");
      form.querySelector('[name="output_name"]').value = event.target.value.toLowerCase().split("").map((letter) => from.includes(letter) ? to[from.indexOf(letter)] : letter).join("").replace(/[^a-z0-9]+/g, "_").replace(/^_+|_+$/g, "").slice(0, 64);
    }
    state.manifest.settings = settings(); controller.touch();
  });
  form.addEventListener("change", (event) => {
    if (!fields.includes(event.target.name)) return;
    state.manifest.settings = settings();
    if (event.target.name === "trigger_word") {
      const next = state.manifest.settings.trigger_word.trim();
      if (next && previousTrigger && next !== previousTrigger) for (const item of state.manifest.images) {
        const value = item.caption.trim(); const boundary = value.slice(previousTrigger.length, previousTrigger.length + 1);
        if (value.slice(0, previousTrigger.length).toLowerCase() === previousTrigger.toLowerCase() && (!boundary || /[\s,.;:!?-]/u.test(boundary))) { item.caption = `${next}${value.slice(previousTrigger.length)}`; item.caption_revision = id(); delete item.caption_job_id; }
      }
      previousTrigger = next;
    }
    controller.touch("items");
  });
  select.addEventListener("change", () => { const target = select.value; select.value = state.dataset?.id || ""; void safely("switch", async () => { if (await flush()) await controller.load(target); }); });
  find("[data-dataset-new]").addEventListener("click", () => void safely("new", async () => { if (await flush()) controller.startNew(); }));
  find("[data-dataset-save]").addEventListener("click", async () => { if (!state.ready) { await controller.load(state.dataset?.id); return; } await uploadFiles(); await controller.flush(); });
  find("[data-dataset-delete]").addEventListener("click", () => void safely("delete", async () => {
    if (!await flush() || !window.confirm("Удалить рабочий набор? Сохранённые версии и уже запущенное обучение останутся.")) return;
    await request(`/${state.dataset.id}/delete`, "POST", { revision: state.dataset.revision }); controller.startNew(); await controller.refreshList(); feedback("Рабочий набор удалён.");
  }));
  find("[data-dataset-versions]").addEventListener("click", () => void showVersions());
  find("[data-dataset-gallery]").addEventListener("click", () => void showGallery());
  find("[data-dataset-dialog-close]").addEventListener("click", () => dialog.close());
  find("[data-dataset-export]").addEventListener("click", () => void safely("export", async () => { if (await flush()) await download(`/${state.dataset.id}`); }));
  find("[data-dataset-import]").addEventListener("click", () => find("[data-dataset-zip]").click());
  find("[data-dataset-zip]").addEventListener("change", (event) => { const file = event.target.files[0]; event.target.value = ""; if (!file) return; void safely("import", async () => {
    if (!await flush() || (state.manifest.images.length && !window.confirm("Заменить кадры и подписи текущего набора содержимым ZIP?"))) return;
    const datasetID = await controller.ensure(); if (!datasetID) return;
    const body = new FormData(); body.append("archive", file);
    controller.apply(await request(`/${datasetID}/import`, "POST", body, { headers: { "X-Dataset-Revision": String(state.dataset.revision) } })); await controller.refreshList(); feedback("Набор импортирован.");
  }); });
  find("[data-dataset-reload]").addEventListener("click", () => { if (window.confirm("Отбросить несохранённые правки и загрузить набор с сервера?")) void controller.load(state.dataset?.id); });
  find("[data-dataset-fork]").addEventListener("click", () => { controller.startNew({ preserve: true }); void controller.flush(); });
  find("[data-lora-clear-images]").addEventListener("click", () => { if (!window.confirm("Убрать все изображения и подписи из этого набора?")) return; files.clear(); state.manifest.images = []; controller.touch("items"); });
  find("[data-lora-caption-all]").addEventListener("click", () => void describeImages(state.manifest.images.filter((item) => !item.excluded && !item.caption.trim()).map((item) => item.id), true, captionActive()));
  form.addEventListener("submit", (event) => { event.preventDefault(); void safely("train", async () => {
    if (state.dataset && !captionPoller.state.ready) throw new Error("Дождитесь проверки заданий описания.");
    if (captionActive()) throw new Error("Дождитесь описаний или остановите серию перед обучением.");
    if (!await flush()) return;
    const included = state.manifest.images.filter((item) => !item.excluded);
    if (included.length < 5 || included.length > 100) throw new Error("Включите от 5 до 100 изображений для обучения.");
    const missing = included.find((item) => !item.caption.trim() && !state.manifest.settings.global_caption.trim());
    if (missing) { cards.get(missing.id)?.querySelector("textarea")?.focus(); throw new Error("Добавьте подпись каждому включённому кадру или общее описание."); }
    const result = await request(`/${state.dataset.id}/train`, "POST", { revision: state.dataset.revision });
    feedback("Обучение поставлено в очередь. Набор сохранён отдельной версией."); page.dispatchEvent(new CustomEvent("lora-training-created", { detail: result.job }));
  }); });
  window.addEventListener("online", () => void captionPoller.refresh());
  document.addEventListener("visibilitychange", () => { if (!document.hidden) void captionPoller.refresh(); });
  window.addEventListener("pageshow", (event) => { if (event.persisted) { urls.clear(); captionPoller.resume(); renderItems(); if (state.dirty) void controller.flush(); } });
  window.addEventListener("beforeunload", (event) => { if (state.dirty || files.size || uploading) { event.preventDefault(); event.returnValue = ""; } });
  window.addEventListener("pagehide", () => { controller.dispose(); captionPoller.dispose(); for (const url of urls.values()) URL.revokeObjectURL(url); });
  icons(); void controller.load();
})();
